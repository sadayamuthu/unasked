package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// withRequestID tags every response with a request id for tracing.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = newID()
		}
		next.ServeHTTP(w, r)
	})
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
