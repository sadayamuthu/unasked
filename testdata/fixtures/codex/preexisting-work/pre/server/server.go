package server

import (
	"log"
	"net/http"
)

// Run serves the API on addr.
func Run(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/items", items)
	log.Printf("listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func legacyPing(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pong"))
}

func items(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("[]"))
}
