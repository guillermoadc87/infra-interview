package obs

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// probePaths are excluded from traces AND metrics.
//
// This is not tidiness, it is correctness for canary analysis. The kubelet hits
// /readyz every 5 seconds per pod and /healthz every 10, which on a two-replica
// deployment is more requests than this application will ever serve -- probe
// traffic would be ~99% of every trace and every latency histogram.
//
// Worse: /readyz returns 503 while a pod is still connecting to Postgres, by
// design. Counted as server errors those 503s look exactly like an outage, so
// an Argo CD sync that starts a new pod would spike the measured error rate and
// abort a canary that is behaving perfectly. The analysis must see USER traffic.
var probePaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
}

// Middleware instruments an entire handler tree: a span and the semconv HTTP
// server metrics (http.server.request.duration and friends) per request.
//
// Those metric names are what the Argo Rollouts AnalysisTemplate queries, via
// the gateway collector's Prometheus exporter -- which renames them on the way
// out, so http.server.request.duration is queried as
// http_server_request_duration_seconds_bucket. Changing the instrumentation
// here silently breaks automated rollback, so don't, without changing the
// AnalysisTemplate in the same commit.
//
// PASS IT AN http.ServeMux. The http.route attribute -- the thing that keeps
// /orders/1 and /orders/2 from becoming two span names and two metric time
// series -- is read from http.Request.Pattern, which only the stdlib router
// populates. Instrument a bare http.HandlerFunc instead and the route label
// silently disappears, leaving unbounded cardinality: the most common way a
// team takes down its own metrics backend. otelhttp used to require an explicit
// WithRouteTag for this; since v0.72 it is automatic, and the requirement moved
// from "remember a call" to "route through a mux".
func Middleware(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "", otelhttp.WithFilter(instrumentable))
}

// instrumentable reports whether a request should produce telemetry. Named
// rather than inlined as a closure so it can be asserted on directly.
func instrumentable(r *http.Request) bool {
	return !probePaths[r.URL.Path]
}
