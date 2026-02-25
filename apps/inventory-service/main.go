package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	_ "github.com/lib/pq"
)

type Product struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
	Price    float64 `json:"price"`
}

type UpdateInventoryRequest struct {
	Quantity int `json:"quantity"`
}

var db *sql.DB

func main() {
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "postgres"
	}
	dbPassword := os.Getenv("DB_PASSWORD")
	if dbPassword == "" {
		dbPassword = "postgres"
	}

	connStr := fmt.Sprintf("host=%s port=5432 user=postgres password=%s dbname=inventory sslmode=disable",
		dbHost, dbPassword)

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	initDB()

	// Seed some initial data
	seedData()

	http.HandleFunc("/products", handleProducts)
	http.HandleFunc("/products/", handleProductByID)
	http.HandleFunc("/inventory/", handleInventory)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	log.Printf("Inventory Service starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func initDB() {
	schema := `
	CREATE TABLE IF NOT EXISTS products (
		id SERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		quantity INTEGER NOT NULL DEFAULT 0,
		price DECIMAL(10,2) NOT NULL
	);
	`
	_, err := db.Exec(schema)
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}
}

func seedData() {
	// Check if we already have data
	var count int
	db.QueryRow("SELECT COUNT(*) FROM products").Scan(&count)
	if count > 0 {
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
		_, err := db.Exec(
			"INSERT INTO products (name, quantity, price) VALUES ($1, $2, $3)",
			p.name, p.quantity, p.price,
		)
		if err != nil {
			log.Printf("Failed to seed product %s: %v", p.name, err)
		}
	}
}

func handleProducts(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		listProducts(w, r)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleProductByID(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		getProduct(w, r)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleInventory(w http.ResponseWriter, r *http.Request) {
	// Update inventory for a product
	if r.Method == "PUT" {
		updateInventory(w, r)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func listProducts(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name, quantity, price FROM products")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Quantity, &p.Price); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		products = append(products, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(products)
}

func getProduct(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/products/"):]

	var p Product
	err := db.QueryRow(
		"SELECT id, name, quantity, price FROM products WHERE id = $1",
		id,
	).Scan(&p.ID, &p.Name, &p.Quantity, &p.Price)

	if err == sql.ErrNoRows {
		http.Error(w, "Product not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func updateInventory(w http.ResponseWriter, r *http.Request) {
	// Extract product ID from path
	id := r.URL.Path[len("/inventory/"):]
	productID, err := strconv.Atoi(id)
	if err != nil {
		http.Error(w, "Invalid product ID", http.StatusBadRequest)
		return
	}

	var req UpdateInventoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// No validation - could set negative inventory!
	_, err = db.Exec(
		"UPDATE products SET quantity = $1 WHERE id = $2",
		req.Quantity, productID,
	)

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}
