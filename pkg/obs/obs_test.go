package obs

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInitRequiresAServiceName(t *testing.T) {
	// An unnamed service is invisible in every backend, so this is a hard error
	// rather than a default -- the failure has to happen at start-up, not three
	// weeks later when someone goes looking for the dashboard.
	if _, err := Init(context.Background(), Service{Version: "abc1234"}); err == nil {
		t.Fatal("expected an error when Service.Name is empty, got nil")
	}
}

func TestInitIsANoopWithoutAnEndpoint(t *testing.T) {
	// `go run ./apps/api-service` and `go test` must work with no collector
	// anywhere. Unsetting the endpoint has to yield a USABLE shutdown function,
	// not nil -- callers defer it unconditionally.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background(), Service{Name: "api-service"})
	if err != nil {
		t.Fatalf("expected no error with the endpoint unset, got %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must never be nil; callers defer it unconditionally")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown returned %v", err)
	}
}

func TestProbesAreNotInstrumentedButRealTrafficIs(t *testing.T) {
	// Guards the canary. /readyz answers 503 by design while a pod connects to
	// Postgres; if those were measured as server errors, every Argo CD sync
	// would spike the error rate and abort a healthy rollout.
	for path, want := range map[string]bool{
		"/healthz":        false,
		"/readyz":         false,
		"/orders":         true,
		"/orders/summary": true,
		"/orders/1":       true,
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if got := instrumentable(req); got != want {
			t.Errorf("instrumentable(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestResourceCarriesEnvironmentAndInstanceIdentity(t *testing.T) {
	t.Setenv("ENVIRONMENT", "staging")
	t.Setenv("K8S_POD_NAME", "api-service-7d9f-abcde")
	t.Setenv("K8S_NAMESPACE_NAME", "shop")

	res, err := newResource(Service{Name: "api-service", Version: "638e6b6"})
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}

	got := map[string]string{}
	for _, kv := range res.Attributes() {
		got[string(kv.Key)] = kv.Value.AsString()
	}

	for key, want := range map[string]string{
		"service.name":                "api-service",
		"service.version":             "638e6b6",
		"service.namespace":           "shop",
		"deployment.environment.name": "staging",
		"k8s.pod.name":                "api-service-7d9f-abcde",
		"k8s.namespace.name":          "shop",
		// Must equal the pod name: it is what makes "which replica served this
		// request" an answerable question.
		"service.instance.id": "api-service-7d9f-abcde",
	} {
		if got[key] != want {
			t.Errorf("resource[%q] = %q, want %q", key, got[key], want)
		}
	}
}

func TestResourceFallsBackWhenTheEnvironmentIsUnset(t *testing.T) {
	// Reading ENVIRONMENT rather than taking it as an argument is what keeps the
	// telemetry's environment identical to the one the app reports on
	// /orders/summary. Locally neither is set, and that must not be an error.
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("K8S_POD_NAME", "")

	res, err := newResource(Service{Name: "inventory-service"})
	if err != nil {
		t.Fatalf("newResource: %v", err)
	}

	got := map[string]string{}
	for _, kv := range res.Attributes() {
		got[string(kv.Key)] = kv.Value.AsString()
	}
	if got["deployment.environment.name"] != "local" {
		t.Errorf("environment = %q, want %q", got["deployment.environment.name"], "local")
	}
	if got["service.version"] != "dev" {
		t.Errorf("version = %q, want %q", got["service.version"], "dev")
	}
	if _, present := got["service.instance.id"]; present {
		t.Error("service.instance.id should be absent when there is no pod name, not empty")
	}
}

func TestLoggerIsUsableBeforeInit(t *testing.T) {
	// Start-up messages are emitted before Init has run (Init itself logs), so a
	// nil logger here would be a panic on the first line of main.
	if Logger() == nil {
		t.Fatal("Logger() returned nil before Init")
	}
	Logger().Info("safe to call")
}

func TestALoggerCapturedBeforeInitStillReachesTheProvider(t *testing.T) {
	// REGRESSION. Both services do `var Logger = obs.Logger()` at package level,
	// which runs before main() and therefore before Init. The first version of
	// this package returned a concrete handler, so that variable bound itself to
	// stdout-only and every later record went to stdout and nowhere else --
	// silently, with the logs visible in `kubectl logs` and absent from the
	// backend.
	//
	// This asserts the indirection that fixes it: a logger captured early must
	// pick up a provider installed later.
	captured := Logger()

	mu.RLock()
	before := otelHandler
	mu.RUnlock()
	if before != nil {
		t.Skip("a provider is already installed; this test needs a clean process")
	}

	// A handler added after the fact must become visible to the captured logger.
	sentinel := &countingHandler{}
	mu.Lock()
	otelHandler = sentinel
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		otelHandler = nil
		mu.Unlock()
	})

	captured.Info("after the provider was installed")
	if sentinel.n == 0 {
		t.Fatal("a logger captured before Init did not reach the later provider")
	}

	// Derived loggers must behave the same way, since WithAttrs cannot be
	// applied eagerly to a handler that does not exist yet.
	sentinel.n = 0
	captured.With("key", "value").Info("derived")
	if sentinel.n == 0 {
		t.Fatal("a derived logger did not reach the later provider")
	}
}

type countingHandler struct {
	n     int
	attrs []slog.Attr
}

func (c *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *countingHandler) Handle(context.Context, slog.Record) error {
	c.n++
	return nil
}
func (c *countingHandler) WithAttrs(a []slog.Attr) slog.Handler {
	// Returns ITSELF so the counter stays shared: the test asserts that a
	// derived logger still reaches this handler.
	c.attrs = append(c.attrs, a...)
	return c
}
func (c *countingHandler) WithGroup(string) slog.Handler { return c }
