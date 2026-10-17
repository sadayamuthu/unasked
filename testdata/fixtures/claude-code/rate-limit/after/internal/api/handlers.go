package api

import (
	"encoding/json"
	"net/http"
)

// listProducts writes the product catalogue as JSON. It never fails.
func listProducts(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode([]string{"tea", "coffee"})
}

// createOrder accepts an order.
func createOrder(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusCreated)
}
