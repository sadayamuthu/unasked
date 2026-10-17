package api

import (
	"net/http"

	"example.com/shop/internal/api/middleware"
	"example.com/shop/internal/config"
	"example.com/shop/internal/ratelimit"
)

// NewRouter wires the public API routes.
func NewRouter(cfg config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/products", listProducts)
	mux.HandleFunc("/v1/orders", createOrder)
	limiter := ratelimit.New(ratelimit.Limits{Anon: cfg.RateLimitAnon, Keyed: cfg.RateLimitKeyed, Window: cfg.RateLimitWindow})
	return middleware.Logging(withRequestID(ratelimit.Middleware(limiter, mux)))
}
