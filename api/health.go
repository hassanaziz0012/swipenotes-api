package api

import (
	"encoding/json"
	"net/http"
)

// Handler is the Vercel serverless function handler for /api/health
func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}
