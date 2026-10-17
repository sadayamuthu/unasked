package middleware

import (
	"log"
	"net/http"
	"time"
)

// Logging logs one line per request with its duration and request id.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now().UTC()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s id=%s took=%s", start.Format(time.RFC3339Nano), r.Method, r.URL.Path, r.Header.Get("X-Request-Id"), time.Since(start))
	})
}
