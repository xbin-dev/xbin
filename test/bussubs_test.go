//go:build integration

package test

import (
	"strings"
	"testing"
	"time"
)

// A bus push subscription (D85) delivers a published event to a backend's
// own endpoint as xbin/bus — starting the backend, which has never run.
func TestBusPushSubscription(t *testing.T) {
	write(t, "apps/busy/scope.json", `{"resources":{"bus":{"type":"bus"}}}`)
	write(t, "apps/busy/xbin.json", `{"runtime":"go","uses":[{"target":"res:apps/busy/bus","role":"reader"}]}`)
	write(t, "apps/busy/go.mod", "module busy\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n")
	write(t, "apps/busy/backend/main.go", `package main

import (
	"encoding/json"
	"net/http"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	var mu sync.Mutex
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /on", func(w http.ResponseWriter, r *http.Request) {
		var ev xbin.BusEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		seen = append(seen, xbin.Caller(r).From+" "+ev.Subscription+" "+ev.Topic+" "+string(ev.Data))
		mu.Unlock()
	})
	mux.HandleFunc("GET /seen", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		xbin.WriteJSON(w, 200, seen)
	})
	xbin.Serve(mux)
}
`)
	if !waitFor(func() bool {
		c, _ := req(t, "PUT", "/api/xbin/bus/subscriptions",
			`{"name":"s","resource":"res:apps/busy/bus","prefix":"ev/","path":"/on","component":"apps/busy"}`)
		return c == 200
	}, 30*time.Second) {
		c, body := req(t, "PUT", "/api/xbin/bus/subscriptions",
			`{"name":"s","resource":"res:apps/busy/bus","prefix":"ev/","path":"/on","component":"apps/busy"}`)
		t.Fatalf("subscribe: %d %s", c, body)
	}
	for _, topic := range []string{"other/0", "ev/1"} {
		if c, body := req(t, "POST", "/api/xbin/bus/publish", `{"resource":"res:apps/busy/bus","topic":"`+topic+`","data":{"n":1}}`); c != 200 {
			t.Fatalf("publish: %d %s", c, body)
		}
	}
	var body string
	if !waitFor(func() bool { _, body = get(t, "/api/apps/busy/seen"); return strings.Contains(body, "ev/1") }, 180*time.Second) {
		t.Fatalf("never delivered: %s", body)
	}
	if !strings.Contains(body, `xbin/bus s ev/1 {\"n\":1}`) || strings.Contains(body, "other/0") {
		t.Fatalf("delivery: %s", body)
	}
	_, list := get(t, "/api/xbin/bus/subscriptions")
	if !strings.Contains(list, `"delivered":1`) {
		t.Fatalf("counters: %s", list)
	}
}
