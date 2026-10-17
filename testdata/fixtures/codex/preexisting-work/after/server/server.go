package server

import (
	"log"
	"net/http"
)

// Run serves the API on addr.
func Run(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/items", items)
	mux.HandleFunc("/healthz", healthz)
	log.Printf("listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func items(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("[]"))
}

// healthz reports that the server is up with 200 OK.
func healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
