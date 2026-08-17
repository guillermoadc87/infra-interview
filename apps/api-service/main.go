package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
)

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
		log.Printf("warning: %s=%q is not a non-negative integer, using %d", key, v, fallback)
		return fallback
	}
	return n
}

func main() {
	cfg := loadSettings()

	connStr := fmt.Sprintf("host=%s port=5432 user=%s password=%s dbname=%s sslmode=disable",
		envOr("DB_HOST", "postgres"),
		envOr("DB_USER", "postgres"),
		envOr("DB_PASSWORD", "postgres"),
		envOr("DB_NAME", "orders"),
	)

	// sql.Open does not dial, so this cannot fail for connectivity reasons.
	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("invalid database configuration: %v", err)
	}

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
	mux.HandleFunc("/orders", handleOrders(cfg))
	mux.HandleFunc("/orders/", handleOrderByID)

	log.Printf("api-service version=%s env=%s starting on :%s (order limit %d, payments %s, log %s)",
		version, cfg.environment, cfg.port, cfg.featureOrderLim, cfg.paymentsURL, cfg.logLevel)
	log.Fatal(http.ListenAndServe(":"+cfg.port, mux))
}

func connectWithRetry() {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		if err := initDB(); err != nil {
			log.Printf("database not ready (attempt %d): %v; retrying in %s", attempt, err, backoff)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		dbReady.Store(true)
		log.Printf("database ready after %d attempt(s)", attempt)
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
		"preview": "hello-from-pr",
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
		internalError(w, "listing orders", err)
		return
	}
	defer rows.Close()

	orders := []Order{}
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.ProductID, &o.Quantity, &o.CustomerID, &o.Status, &o.CreatedAt); err != nil {
			internalError(w, "scanning order", err)
			return
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		internalError(w, "iterating orders", err)
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
		internalError(w, "creating order", err)
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
		internalError(w, "fetching order", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// internalError logs the detail and returns a generic message. The original
// code passed err.Error() straight to the client, leaking schema and connection
// details to anyone who could provoke a failure.
func internalError(w http.ResponseWriter, action string, err error) {
	log.Printf("error %s: %v", action, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
