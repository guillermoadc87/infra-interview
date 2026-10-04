// Package obs is the ONLY way a service in this repository obtains a tracer,
// meter or logger.
//
// It exists because observability standards decay the moment each service picks
// its own metric names, resource attributes and sampling. A dashboard or a
// canary analysis query that has to special-case per service is a dashboard
// nobody maintains. So the contract lives here, once:
//
//   - resource attributes are assembled in exactly one place
//   - HTTP metrics are whatever the semconv instrumentation emits, unmodified
//   - the collector address is read from the environment and never from code
//   - sampling is 100% at the SDK, because sampling decisions belong to the
//     gateway collector (see gitops/platform/otel-gateway)
//
// CI enforces this structurally: nothing under apps/ may import
// go.opentelemetry.io/otel directly. See .github/workflows/gitops-validate.yml.
package obs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Service is the only thing a workload knows about itself that the platform
// cannot discover for it. Everything else -- environment, pod, node, namespace
// -- arrives from the environment, so it cannot drift from where the thing is
// actually running.
type Service struct {
	// Name becomes service.name. Required: an unnamed service is invisible in
	// every backend, so this fails loudly rather than defaulting.
	Name string
	// Version becomes service.version. Both Dockerfiles already inject this via
	// -ldflags "-X main.version=<sha>", so it is the deployed commit.
	Version string
}

// ShutdownFunc flushes every pipeline. Call it before the process exits or the
// last batch of spans, metrics and logs -- usually the interesting ones, since
// something made you exit -- is lost.
type ShutdownFunc func(context.Context) error

// Init wires the three signals and installs them as the global providers.
//
// With OTEL_EXPORTER_OTLP_ENDPOINT unset it installs NOTHING and returns a
// no-op shutdown. That is deliberate: `go run ./apps/api-service` and `go test`
// must work with no collector anywhere, and a library that panics or blocks
// without one would make local development worse to buy nothing.
func Init(ctx context.Context, svc Service) (ShutdownFunc, error) {
	if svc.Name == "" {
		return nil, errors.New("obs.Init: Service.Name is required")
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		Logger().Info("observability disabled: OTEL_EXPORTER_OTLP_ENDPOINT is unset")
		return func(context.Context) error { return nil }, nil
	}

	res, err := newResource(svc)
	if err != nil {
		return nil, fmt.Errorf("obs.Init: resource: %w", err)
	}

	// Context propagation must be set even though nothing upstream currently
	// sends traceparent: the moment api-service calls inventory-service, the
	// trace joins up only if both ends agree on the format. W3C is the default
	// everywhere and costs nothing to install now.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	var shutdowns []ShutdownFunc

	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("obs.Init: trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExp),
		// AlwaysSample, on purpose.
		//
		// The gateway collector does tail_sampling -- it keeps every error and
		// slow trace and a baseline percentage of the rest. It can only do that
		// for traces it receives, so head sampling here would silently discard
		// exactly the errors tail sampling exists to catch. Volume is bounded
		// at the gateway, not at the process.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	shutdowns = append(shutdowns, tp.Shutdown)

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("obs.Init: metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		// 15s, matched to the gateway's Prometheus scrape shape. Argo Rollouts
		// analysis queries these metrics, and an interval long enough to leave a
		// canary step with no data points makes the analysis inconclusive rather
		// than passing or failing.
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second))),
	)
	otel.SetMeterProvider(mp)
	shutdowns = append(shutdowns, mp.Shutdown)

	logExp, err := otlploggrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("obs.Init: log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	setLoggerProvider(lp)
	shutdowns = append(shutdowns, lp.Shutdown)

	Logger().Info("observability initialised",
		"service", svc.Name, "version", svc.Version, "endpoint", endpoint)

	return func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdowns {
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}

// newResource assembles the identity every span, metric and log carries.
//
// The attribute keys are OpenTelemetry semantic conventions, written as string
// literals rather than taken from a semconv package: the semconv import path
// carries its version (semconv/v1.26.0, v1.27.0, ...), so depending on it means
// a mechanical import rewrite every time the spec moves, for attributes whose
// names have been stable for years.
func newResource(svc Service) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", svc.Name),
		attribute.String("service.version", valueOr(svc.Version, "dev")),
		// Groups the two services as one system in the backend, so a trace
		// spanning both is obviously one application.
		attribute.String("service.namespace", "shop"),
		// Reuses the EXISTING ENVIRONMENT variable, which is config category 3
		// (variants/env/<env>, never promoted). So the environment a span is
		// tagged with is the same string the application reports on
		// /orders/summary, and cannot disagree with it.
		attribute.String("deployment.environment.name", envOr("ENVIRONMENT", "local")),
	}

	// Populated from the downward API in gitops/apps/*/base/deployment.yaml.
	// The agent's k8sattributes processor also enriches by source IP, but that
	// association fails for anything it cannot see; carrying the values here
	// means a pod identifies itself even when enrichment does not happen.
	for key, env := range map[string]string{
		"k8s.pod.name":       "K8S_POD_NAME",
		"k8s.namespace.name": "K8S_NAMESPACE_NAME",
		"k8s.node.name":      "K8S_NODE_NAME",
	} {
		if v := os.Getenv(env); v != "" {
			attrs = append(attrs, attribute.String(key, v))
		}
	}
	// service.instance.id must be unique per running copy. The pod name is
	// exactly that, and it is what makes "which replica served this" answerable.
	if pod := os.Getenv("K8S_POD_NAME"); pod != "" {
		attrs = append(attrs, attribute.String("service.instance.id", pod))
	}

	return resource.Merge(resource.Default(), resource.NewWithAttributes(
		resource.Default().SchemaURL(), attrs...,
	))
}

func envOr(key, fallback string) string { return valueOr(os.Getenv(key), fallback) }

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
