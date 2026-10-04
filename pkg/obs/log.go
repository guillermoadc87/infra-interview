package obs

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

var (
	mu          sync.RWMutex
	otelHandler slog.Handler // nil until Init installs a log provider

	stdoutOnce sync.Once
	stdoutH    slog.Handler

	logger = slog.New(&dynamic{})
)

// Logger returns the process logger: structured JSON on stdout always, plus
// OTLP log records carrying trace_id/span_id once Init has run.
//
// The returned logger is SAFE TO CAPTURE BEFORE Init -- including in a
// package-level `var Logger = obs.Logger()`, which is how both services use it.
// That property is the entire reason for the indirection below, and it is not
// hypothetical: the first version of this package returned a concrete handler,
// so a package-level variable bound itself to the stdout-only handler during
// program initialisation and every subsequent log line went to stdout and
// nowhere else. Forty error logs, visible in `kubectl logs`, absent from Loki,
// with nothing anywhere reporting a problem. The caller cannot reasonably be
// expected to know that, so the library carries it.
func Logger() *slog.Logger { return logger }

func setLoggerProvider(lp *sdklog.LoggerProvider) {
	mu.Lock()
	defer mu.Unlock()
	// otelslog attaches the ACTIVE SPAN's trace_id and span_id to every record,
	// which is what turns "find the logs for this slow trace" from a
	// timestamp-and-grep exercise into a click in Grafana. It only works for
	// records emitted with a request context, which is why handlers log through
	// the ...Context methods.
	otelHandler = otelslog.NewHandler("obs", otelslog.WithLoggerProvider(lp))
}

// dynamic resolves its destination handlers on every call rather than at
// construction, so a logger obtained before Init starts emitting to OTLP the
// moment Init installs the provider.
//
// WithAttrs/WithGroup cannot be applied eagerly for the same reason -- the
// handler they would wrap may not exist yet -- so they are recorded and
// replayed onto whichever handlers are live. Logging already allocates; this
// adds a slice walk per record, which is not a cost worth optimising at the
// volume these services produce.
type dynamic struct {
	ops []func(slog.Handler) slog.Handler
}

func (d *dynamic) targets() []slog.Handler {
	mu.RLock()
	oh := otelHandler
	mu.RUnlock()

	out := make([]slog.Handler, 0, 2)
	for _, h := range []slog.Handler{stdout(), oh} {
		if h == nil {
			continue
		}
		for _, op := range d.ops {
			h = op(h)
		}
		out = append(out, h)
	}
	return out
}

func (d *dynamic) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range d.targets() {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (d *dynamic) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range d.targets() {
		if h.Enabled(ctx, r.Level) {
			// Each handler gets its own copy: a Record carries internal state
			// that is not safe to hand to two consumers.
			if err := h.Handle(ctx, r.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *dynamic) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &dynamic{ops: append(d.clone(), func(h slog.Handler) slog.Handler {
		return h.WithAttrs(attrs)
	})}
}

func (d *dynamic) WithGroup(name string) slog.Handler {
	return &dynamic{ops: append(d.clone(), func(h slog.Handler) slog.Handler {
		return h.WithGroup(name)
	})}
}

// clone copies the op chain so sibling loggers derived from the same parent do
// not append into each other's slice.
func (d *dynamic) clone() []func(slog.Handler) slog.Handler {
	out := make([]func(slog.Handler) slog.Handler, len(d.ops))
	copy(out, d.ops)
	return out
}

// stdout keeps logs in `kubectl logs` as well as in the backend.
//
// Shipping them ONLY over OTLP would mean that debugging a pod which cannot
// reach the collector -- the exact situation where you most need its logs --
// leaves you with nothing. Structured JSON on stdout costs almost nothing and
// is also what a filelog receiver would read if one is ever added.
func stdout() slog.Handler {
	stdoutOnce.Do(func() {
		level := slog.LevelInfo
		// Reuses LOG_LEVEL, already supplied per tier by variants/tier/<tier>
		// (config category 3). One variable, one meaning, set in one place.
		switch os.Getenv("LOG_LEVEL") {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
		stdoutH = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	})
	return stdoutH
}
