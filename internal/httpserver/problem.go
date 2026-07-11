package httpserver

import (
	"encoding/json"
	"net/http"
)

// WriteProblem emits the contract's Problem shape as application/problem+json.
func WriteProblem(w http.ResponseWriter, status int, title, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"title": title, "status": status, "code": code,
	})
}
