package httperr

import (
	"encoding/json"
	"net/http"
)

// Write responds with a JSON error body: {"error": "message"}.
func Write(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
