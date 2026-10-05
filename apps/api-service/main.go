package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	// The ONLY observability import permitted outside pkg/obs. CI fails the
	// build if anything under apps/ imports go.opentelemetry.io directly --
	// see the "observability goes through pkg/obs" gate in gitops-validate.yml.
	"infra-interview/pkg/obs"
	"infra-interview/pkg/pgcreds"
)

// Logger is the process logger: structured JSON on stdout for `kubectl logs`,
// and OTLP log records carrying trace_id/span_id for the backend. Assigned once
// here so every call site is a plain Logger.X and nothing reaches for the
// standard log package, which would bypass both.
var Logger = obs.Logger()

// Set at build time with -ldflags "-X main.version=<sha>". This is what makes a
// deployment verifiable end to end: /healthz reports the exact commit serving
// traffic, so "the new version is live" is an observation, not an assumption.
var version = "dev"

type Order struct {
	ID         int       `json:"id"`
	ProductID  int       `json:"product_id"`
	Quantity   int       `json:"quantity"`
	CustomerID string    `json:"customer_id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

type CreateOrderRequest struct {
	ProductID  int    `json:"product_id"`
	Quantity   int    `json:"quantity"`
	CustomerID string `json:"customer_id"`
}

var (
	db *sql.DB
	// dbReady is flipped once the schema is in place. Readiness consults this
	// plus a live ping, so a pod that has not finished initialising never
	// receives traffic.
	dbReady atomic.Bool
)

// settings holds the environment-scoped configuration. Which file each of these
// lives in is the whole point of the gitops/ layout -- see gitops/README.md.
type settings struct {
	port            string
	environment     string // variants/env/<env>      -- never promoted
	paymentsURL     string // variants/tier/<tier>    -- never promoted
	logLevel        string // variants/tier/<tier>    -- never promoted
	featureOrderLim int    // envs/<env>/settings.yaml -- PROMOTED
}

func loadSettings() settings {
	return settings{
		port:            envOr("PORT", "8080"),
		environment:     envOr("ENVIRONMENT", "local"),
		paymentsURL:     envOr("PAYMENTS_URL", "https://payments.sandbox.example.com"),
		logLevel:        envOr("LOG_LEVEL", "debug"),
		featureOrderLim: envIntOr("FEATURE_ORDER_LIMIT", 100),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		Logger.Warn("value is not a non-negative integer; using the fallback",
			"key", key, "value", v, "fallback", fallback)
		return fallback
	}
	return n
}

func main() {
	cfg := loadSettings()

	// Telemetry first, so even the start-up path is instrumented. With
	// OTEL_EXPORTER_OTLP_ENDPOINT unset this is a no-op, which is what keeps
	// `go run` and `go test` working with no collector anywhere.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := obs.Init(ctx, obs.Service{Name: "api-service", Version: version})
	if err != nil {
		Logger.Error("could not initialise observability", "error", err.Error())
		os.Exit(1)
	}

	// CREDENTIALS FROM A MOUNTED FILE, re-read on every connection.
	//
	// They used to be DB_USER/DB_PASSWORD environment variables, which are read
	// once at container start -- so a Vault rotation only took effect if
	// something restarted the pod. Nothing could: Reloader restarts a workload by
	// patching its pod template, and under the Rollout's workloadRef that
	// template is on a Deployment scaled to zero while the Rollout itself has
	// none. The pod kept a credential Vault had already revoked and every request
	// failed with "pq: permission denied", while the pod stayed Running and Ready.
	//
	// Kubernetes updates a mounted Secret's files in place, so the credential on
	// disk is always current and the restart is simply not needed.
	creds, err := pgcreds.New(pgcreds.Config{
		Dir:      envOr("DB_CRED_DIR", "/var/run/secrets/db"),
		Host:     envOr("DB_HOST", "postgres"),
		Port:     envOr("DB_PORT", "5432"),
		Database: envOr("DB_NAME", "orders"),
		SSLMode:  envOr("DB_SSLMODE", "disable"),
	})
	if err != nil {
		Logger.Error("database credentials are not readable", "error", err.Error())
		os.Exit(1)
	}

	// obs.OpenDBConnector, not obs.OpenDB: a DSN string is fixed for the life of
	// the pool, which is exactly how the credential went stale. The connector
	// reads it per connection instead. Every query still gets a span and the
	// pool still reports metrics.
	db = obs.OpenDBConnector(creds)

	// The other half of the rotation design. Reading the credential per
	// CONNECTION does nothing for a connection already open with the old role --
	// that one keeps working until Vault revokes it and then starts failing
	// mid-request. Capping the lifetime forces the pool to recycle onto whatever
	// is currently on disk.
	db.SetConnMaxLifetime(pgcreds.MaxConnLifetime)

	// Connect in the BACKGROUND and start serving immediately.
	//
	// The original code called log.Fatal if the database was not up, so pod
	// start order mattered and a database blip became a CrashLoopBackOff. Now an
	// unreachable database means "not Ready" -- the pod leaves the Service and
	// rejoins on its own once the database returns. That is what makes the
	// GitOps convergence story true rather than aspirational.
	go connectWithRetry()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/readyz", handleReadyz)
	mux.HandleFunc("/orders/summary", handleSummary(cfg))
	mux.HandleFunc("/orders", handleOrders(cfg))
	mux.HandleFunc("/orders/", handleOrderByID)

	Logger.Info("starting",
		"service", "api-service", "version", version, "env", cfg.environment,
		"port", cfg.port, "order_limit", cfg.featureOrderLim,
		"payments_url", cfg.paymentsURL, "log_level", cfg.logLevel)

	// A real http.Server with graceful shutdown, replacing
	// log.Fatal(http.ListenAndServe(...)).
	//
	// log.Fatal calls os.Exit, which skips every deferred function -- so the
	// final batch of spans, metrics and logs, usually the interesting ones,
	// would never be flushed. Draining in-flight requests on SIGTERM also
	// matters to the rollout strategy: maxUnavailable: 0 only guarantees a pod
	// is READY before the old one goes away, not that the old one finishes what
	// it was already doing.
	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           obs.Middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			Logger.Error("server stopped unexpectedly", "error", err.Error())
			flush(shutdownTelemetry)
			os.Exit(1)
		}
	case <-ctx.Done():
		Logger.Info("shutting down")
	}

	drain, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil {
		Logger.Error("graceful shutdown failed", "error", err.Error())
	}
	flush(shutdownTelemetry)
}

// flush gives the telemetry pipelines their own deadline, separate from the
// drain above: a collector that has gone away must not hold the pod in
// Terminating until the kubelet force-kills it.
func flush(shutdown obs.ShutdownFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		Logger.Warn("telemetry did not flush cleanly", "error", err.Error())
	}
}

func connectWithRetry() {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		if err := initDB(); err != nil {
			Logger.Warn("database not ready; retrying",
				"attempt", attempt, "error", err.Error(), "retry_in", backoff.String())
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		dbReady.Store(true)
		Logger.Info("database ready", "attempts", attempt)
		return
	}
}

func initDB() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
	CREATE TABLE IF NOT EXISTS orders (
		id SERIAL PRIMARY KEY,
		product_id INTEGER NOT NULL,
		quantity INTEGER NOT NULL,
		customer_id VARCHAR(255) NOT NULL,
		status VARCHAR(50) NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`)
	return err
}

// handleHealthz is LIVENESS: is this process alive? It deliberately does NOT
// touch the database. If it did, a database outage would fail every pod's
// liveness probe at once and restart the whole fleet, turning a dependency
// problem into an application outage.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": version,
	})
}

// handleReadyz is READINESS: should this pod receive traffic? This one DOES
// check the database, so an unhealthy pod is removed from the Service endpoints
// without being killed.
func handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if !dbReady.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "initialising", "db": "connecting", "version": version,
		})
		return
	}
	if err := db.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "degraded", "db": "unreachable", "version": version,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok", "db": "ok", "version": version,
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// handleSummary reports how many orders exist alongside the two settings that
// govern this environment. It exists to make the config model observable: the
// order limit is PROMOTED between environments, while the environment name is
// never promoted, so the same build reports different values per environment.
func handleSummary(cfg settings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var count int
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM orders").Scan(&count); err != nil {
			internalError(w, r, "counting orders", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"orders":      count,
			"order_limit": cfg.featureOrderLim,
			"environment": cfg.environment,
			"version":     version,
		})
	}
}

func handleOrders(cfg settings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listOrders(w, r)
		case http.MethodPost:
			createOrder(w, r, cfg)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleOrderByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	getOrder(w, r)
}

func listOrders(w http.ResponseWriter, r *http.Request) {
	rows, err := db.QueryContext(r.Context(),
		"SELECT id, product_id, quantity, customer_id, status, created_at FROM orders ORDER BY created_at DESC LIMIT 100")
	if err != nil {
		internalError(w, r, "listing orders", err)
		return
	}
	defer rows.Close()

	orders := []Order{}
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.ProductID, &o.Quantity, &o.CustomerID, &o.Status, &o.CreatedAt); err != nil {
			internalError(w, r, "scanning order", err)
			return
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, "iterating orders", err)
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

// validateOrder is pure so it can be unit tested without a database.
func validateOrder(req CreateOrderRequest, maxQty int) error {
	if req.ProductID <= 0 {
		return fmt.Errorf("product_id must be a positive integer")
	}
	if req.Quantity <= 0 {
		return fmt.Errorf("quantity must be a positive integer")
	}
	if req.Quantity > maxQty {
		return fmt.Errorf("quantity %d exceeds the limit of %d for this environment", req.Quantity, maxQty)
	}
	if req.CustomerID == "" {
		return fmt.Errorf("customer_id is required")
	}
	return nil
}

func createOrder(w http.ResponseWriter, r *http.Request, cfg settings) {
	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed JSON body", http.StatusBadRequest)
		return
	}
	if err := validateOrder(req, cfg.featureOrderLim); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var orderID int
	err := db.QueryRowContext(r.Context(),
		"INSERT INTO orders (product_id, quantity, customer_id, status) VALUES ($1, $2, $3, $4) RETURNING id",
		req.ProductID, req.Quantity, req.CustomerID, "pending",
	).Scan(&orderID)
	if err != nil {
		internalError(w, r, "creating order", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int{"order_id": orderID})
}

func getOrder(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/orders/"):]
	if _, err := strconv.Atoi(id); err != nil {
		http.Error(w, "order id must be an integer", http.StatusBadRequest)
		return
	}

	var o Order
	err := db.QueryRowContext(r.Context(),
		"SELECT id, product_id, quantity, customer_id, status, created_at FROM orders WHERE id = $1", id,
	).Scan(&o.ID, &o.ProductID, &o.Quantity, &o.CustomerID, &o.Status, &o.CreatedAt)

	if err == sql.ErrNoRows {
		http.Error(w, "Order not found", http.StatusNotFound)
		return
	}
	if err != nil {
		internalError(w, r, "fetching order", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// internalError logs the detail and returns a generic message. The original
// code passed err.Error() straight to the client, leaking schema and connection
// details to anyone who could provoke a failure.
func internalError(w http.ResponseWriter, r *http.Request, action string, err error) {
	// ErrorContext, not Error: the context carries the active span, so the log
	// record is stamped with trace_id/span_id and the failing request is one
	// click away from its own trace in the backend. Logging without the context
	// still works and is simply unlinkable, which is the hard thing to debug.
	Logger.ErrorContext(r.Context(), "request failed", "action", action, "error", err.Error())
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
