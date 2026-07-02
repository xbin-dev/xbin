// Email backend (demo): shows one app consuming another's API server-side.
// GET /today proxies to apps/calendar with this element's verified identity;
// the calendar sees X-Buxon-From: apps/email, X-Buxon-Role: reader.
// The IMAP password lives in this element's private vault (never in source).
package main

import (
	"encoding/json"
	"io"
	"net/http"

	buxon "github.com/magik6k/buxon/sdk"
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /today", func(w http.ResponseWriter, r *http.Request) {
		resp, err := buxon.Client().Get("http://buxon/api/apps/calendar/events")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})

	// Demo of vault-held secrets: reports whether the IMAP password is set
	// (bx vault set apps/email imap-pass). Real code would open IMAP with it.
	mux.HandleFunc("GET /imap-status", func(w http.ResponseWriter, r *http.Request) {
		_, err := buxon.Secret("imap-pass")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"configured": err == nil})
	})

	buxon.Serve(mux)
}
