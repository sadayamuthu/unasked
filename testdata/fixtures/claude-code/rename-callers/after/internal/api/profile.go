package api

import (
	"encoding/json"
	"net/http"

	"example.com/shop/internal/users"
)

func profile(w http.ResponseWriter, r *http.Request) {
	u, err := users.FetchUser(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	json.NewEncoder(w).Encode(u)
}
