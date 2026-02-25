package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

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

var db *sql.DB

func main() {
	// Hardcoded database connection - not ideal!
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "postgres"
	}
	dbPassword := os.Getenv("DB_PASSWORD")
	if dbPassword == "" {
		dbPassword = "postgres" // Default password in plain text!
	}

	connStr := fmt.Sprintf("host=%s port=5432 user=postgres password=%s dbname=orders sslmode=disable",
		dbHost, dbPassword)

	var err error
	// No retry logic - fails if DB isn't ready
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Initialize database schema
	initDB()

	// No health check endpoint
	// No metrics endpoint
	// No graceful shutdown

	http.HandleFunc("/orders", handleOrders)
	http.HandleFunc("/orders/", handleOrderByID)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("API Service starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func initDB() {
	schema := `
	CREATE TABLE IF NOT EXISTS orders (
		id SERIAL PRIMARY KEY,
		product_id INTEGER NOT NULL,
		quantity INTEGER NOT NULL,
		customer_id VARCHAR(255) NOT NULL,
		status VARCHAR(50) NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := db.Exec(schema)
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}
}

func handleOrders(w http.ResponseWriter, r *http.Request) {
	// No request validation
	// No authentication
	// Basic error handling

	switch r.Method {
	case "GET":
		listOrders(w, r)
	case "POST":
		createOrder(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleOrderByID(w http.ResponseWriter, r *http.Request) {
	// No proper routing - manual path parsing
	// No validation

	if r.Method == "GET" {
		getOrder(w, r)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func listOrders(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, product_id, quantity, customer_id, status, created_at FROM orders ORDER BY created_at DESC LIMIT 100")
	if err != nil {
		// Exposing internal errors to client
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var orders []Order
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.ProductID, &o.Quantity, &o.CustomerID, &o.Status, &o.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		orders = append(orders, o)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(orders)
}

func createOrder(w http.ResponseWriter, r *http.Request) {
	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// No validation of input
	// No inventory check
	// No transaction handling

	var orderID int
	err := db.QueryRow(
		"INSERT INTO orders (product_id, quantity, customer_id, status) VALUES ($1, $2, $3, $4) RETURNING id",
		req.ProductID, req.Quantity, req.CustomerID, "pending",
	).Scan(&orderID)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]int{"order_id": orderID})
}

func getOrder(w http.ResponseWriter, r *http.Request) {
	// Manual path parsing - no router
	// SQL injection possible if not careful

	id := r.URL.Path[len("/orders/"):]

	var o Order
	err := db.QueryRow(
		"SELECT id, product_id, quantity, customer_id, status, created_at FROM orders WHERE id = $1",
		id,
	).Scan(&o.ID, &o.ProductID, &o.Quantity, &o.CustomerID, &o.Status, &o.CreatedAt)

	if err == sql.ErrNoRows {
		http.Error(w, "Order not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(o)
}
