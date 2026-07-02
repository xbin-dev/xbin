// Calendar backend: events in the scope's kv resource, change notifications
// on the scope's bus. The reference example for the buxon resource + RBAC
// model — apps/email consumes it read-only (see examples/email).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	buxon "github.com/magik6k/buxon/sdk"
)

type Event struct {
	ID    string `json:"id"`
	Day   string `json:"day"`  // YYYY-MM-DD
	Time  string `json:"time"` // HH:MM
	Title string `json:"title"`
}

func main() {
	kv := buxon.KV(buxon.Resource("events"))

	mux := http.NewServeMux()

	// GET /events?day=YYYY-MM-DD (default today) — role: reader
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		day := r.URL.Query().Get("day")
		if day == "" {
			day = time.Now().Format("2006-01-02")
		}
		keys, err := kv.List(day + "/")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		events := []Event{}
		for _, k := range keys {
			var ev Event
			if kv.GetJSON(k, &ev) == nil {
				events = append(events, ev)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"day": day, "events": events})
	})

	// POST /events {day, time, title} — role: writer
	mux.Handle("POST /events", buxon.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil || ev.Day == "" || ev.Title == "" {
			http.Error(w, `{"error":"need {day, time?, title}"}`, http.StatusBadRequest)
			return
		}
		ev.ID = fmt.Sprintf("%s/%d", ev.Day, time.Now().UnixNano())
		if err := kv.PutJSON(ev.ID, ev); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Anyone granted reader on res:apps/calendar/bus sees this live.
		_ = buxon.Publish(buxon.Resource("bus"), "events/created", ev)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ev)
	}))

	buxon.Serve(mux)
}
