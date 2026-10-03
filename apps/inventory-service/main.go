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

	_ "github.com/lib/pq"
)

// Logger is the process logger: structured JSON on stdout for `kubectl logs`,
// and OTLP log records carrying trace_id/span_id for the backend. Assigned once
// here so every call site is a plain Logger.X and nothing reaches for the
// standard log package, which would bypass both.
var Logger = obs.Logger()

// Set at build time with -ldflags "-X main.version=<sha>".
var version = "dev"

type Product struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
	LowStock bool    `json:"low_stock"`
}

type UpdateInventoryRequest struct {
	Quantity int `json:"quantity"`
}

var (
	db      *sql.DB
	dbReady atomic.Bool
)

type settings struct {
	port        string
	environment string // variants/env/<env>       -- never promoted
	logLevel    string // variants/tier/<tier>     -- never promoted
	lowStock    int    // envs/<env>/settings.yaml -- PROMOTED
}

func loadSettings() settings {
	return settings{
		port:        envOr("PORT", "8081"),
		environment: envOr("ENVIRONMENT", "local"),
		logLevel:    envOr("LOG_LEVEL", "debug"),
		lowStock:    envIntOr("LOW_STOCK_THRESHOLD", 10),
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

	shutdownTelemetry, err := obs.Init(ctx, obs.Service{Name: "inventory-service", Version: version})
	if err != nil {
		Logger.Error("could not initialise observability", "error", err.Error())
		os.Exit(1)
	}

	connStr := fmt.Sprintf("host=%s port=5432 user=%s password=%s dbname=%s sslmode=disable",
		envOr("DB_HOST", "postgres"),
		envOr("DB_USER", "postgres"),
		envOr("DB_PASSWORD", "postgres"),
		envOr("DB_NAME", "inventory"),
	)

	// obs.OpenDB, not sql.Open: every query gets a span and the connection pool
	// gets metrics. The pool metrics earn their keep here because Vault mints a
	// new database role every 45 minutes, so connection churn is normal and
	// worth being able to see.
	db, err = obs.OpenDB("postgres", connStr)
	if err != nil {
		Logger.Error("invalid database configuration", "error", err.Error())
		os.Exit(1)
	}

	// Serve immediately; connect in the background. See api-service/main.go for
	// why this matters for pod start ordering.
	go connectWithRetry()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/readyz", handleReadyz)
	mux.HandleFunc("/products", handleProducts(cfg))
	mux.HandleFunc("/products/", handleProductByID(cfg))
	mux.HandleFunc("/inventory/", handleInventory)

	Logger.Info("starting",
		"service", "inventory-service", "version", version, "env", cfg.environment,
		"port", cfg.port, "low_stock_threshold", cfg.lowStock,
		"log_level", cfg.logLevel)

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
		seedData()
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
	CREATE TABLE IF NOT EXISTS products (
		id SERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		quantity INTEGER NOT NULL DEFAULT 0,
		price DECIMAL(10,2) NOT NULL
	);`)
	return err
}

func seedData() {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM products").Scan(&count); err != nil || count > 0 {
		return
	}
	products := []struct {
		name     string
		quantity int
		price    float64
	}{
		{"Laptop", 50, 999.99},
		{"Mouse", 200, 29.99},
		{"Keyboard", 150, 79.99},
		{"Monitor", 75, 299.99},
		{"Headphones", 100, 149.99},
	}
	for _, p := range products {
		if _, err := db.Exec(
			"INSERT INTO products (name, quantity, price) VALUES ($1, $2, $3)",
			p.name, p.quantity, p.price,
		); err != nil {
			Logger.Warn("failed to seed product", "product", p.name, "error", err.Error())
		}
	}
}

// Liveness: never touches the database.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
}

// Readiness: does check the database.
func handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if !dbReady.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "initialising", "db": "connecting", "version": version})
		return
	}
	if err := db.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "degraded", "db": "unreachable", "version": version})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "db": "ok", "version": version})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// isLowStock is pure so the promoted threshold can be unit tested.
func isLowStock(quantity, threshold int) bool { return quantity < threshold }

func handleProducts(cfg settings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		listProducts(w, r, cfg)
	}
}

func handleProductByID(cfg settings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		getProduct(w, r, cfg)
	}
}

func handleInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	updateInventory(w, r)
}

func listProducts(w http.ResponseWriter, r *http.Request, cfg settings) {
	rows, err := db.QueryContext(r.Context(), "SELECT id, name, quantity, price FROM products")
	if err != nil {
		internalError(w, r, "listing products", err)
		return
	}
	defer rows.Close()

	products := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Quantity, &p.Price); err != nil {
			internalError(w, r, "scanning product", err)
			return
		}
		p.LowStock = isLowStock(p.Quantity, cfg.lowStock)
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, "iterating products", err)
		return
	}
	writeJSON(w, http.StatusOK, products)
}

func getProduct(w http.ResponseWriter, r *http.Request, cfg settings) {
	id := r.URL.Path[len("/products/"):]
	if _, err := strconv.Atoi(id); err != nil {
		http.Error(w, "product id must be an integer", http.StatusBadRequest)
		return
	}

	var p Product
	err := db.QueryRowContext(r.Context(),
		"SELECT id, name, quantity, price FROM products WHERE id = $1", id).
		Scan(&p.ID, &p.Name, &p.Quantity, &p.Price)

	if err == sql.ErrNoRows {
		http.Error(w, "Product not found", http.StatusNotFound)
		return
	}
	if err != nil {
		internalError(w, r, "fetching product", err)
		return
	}
	p.LowStock = isLowStock(p.Quantity, cfg.lowStock)
	writeJSON(w, http.StatusOK, p)
}

func updateInventory(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/inventory/"):]
	productID, err := strconv.Atoi(id)
	if err != nil {
		http.Error(w, "Invalid product ID", http.StatusBadRequest)
		return
	}

	var req UpdateInventoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed JSON body", http.StatusBadRequest)
		return
	}
	// The original code accepted negative inventory without complaint.
	if req.Quantity < 0 {
		http.Error(w, "quantity must not be negative", http.StatusBadRequest)
		return
	}

	res, err := db.ExecContext(r.Context(),
		"UPDATE products SET quantity = $1 WHERE id = $2", req.Quantity, productID)
	if err != nil {
		internalError(w, r, "updating inventory", err)
		return
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		http.Error(w, "Product not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func internalError(w http.ResponseWriter, r *http.Request, action string, err error) {
	// ErrorContext, not Error: the context carries the active span, so the log
	// record is stamped with trace_id/span_id and the failing request is one
	// click away from its own trace in the backend. Logging without the context
	// still works and is simply unlinkable, which is the hard thing to debug.
	Logger.ErrorContext(r.Context(), "request failed", "action", action, "error", err.Error())
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
