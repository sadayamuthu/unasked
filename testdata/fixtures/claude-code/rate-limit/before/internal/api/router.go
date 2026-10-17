package api

import (
	"net/http"

	"example.com/shop/internal/api/middleware"
	"example.com/shop/internal/config"
)

// NewRouter wires the public API routes.
func NewRouter(cfg config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/products", listProducts)
	mux.HandleFunc("/v1/orders", createOrder)
	return middleware.Logging(withRequestID(mux))
}
