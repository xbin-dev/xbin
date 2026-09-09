package xbin

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as the JSON body of a response with the given status.
// The one shape every backend answers with; the builtin tiles and the agent
// template each carried a copy before it lived here.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes the `{"error": msg}` body xbin's own API uses, so a
// tile's refusals read the same as the daemon's (bx prints the field, the
// in-frame client throws it).
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}
