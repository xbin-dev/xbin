//go:build integration

package test

import (
	"encoding/json"
	"testing"
	"time"
)

// An "alwaysOn" backend starts without any request — nothing would ever
// send it one — and comes back by itself after it exits (runner/alwayson.go).
func TestAlwaysOnBackend(t *testing.T) {
	write(t, "apps/always/xbin.json", `{"runtime":"go","alwaysOn":true}`)
	write(t, "apps/always/go.mod", "module always\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n")
	write(t, "apps/always/backend/main.go", `package main

import (
	"net/http"
	"os"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /die", func(w http.ResponseWriter, r *http.Request) {
		go func() { time.Sleep(50 * time.Millisecond); os.Exit(3) }()
		_, _ = w.Write([]byte("bye"))
	})
	xbin.Serve(mux)
}
`)
	state := func() (string, float64) {
		_, body := get(t, "/api/xbin/backends")
		var all map[string]map[string]any
		_ = json.Unmarshal([]byte(body), &all)
		b := all["apps/always"]
		if b == nil {
			return "", 0
		}
		gen, _ := b["gen"].(float64)
		st, _ := b["state"].(string)
		return st, gen
	}
	var gen0 float64
	if !waitFor(func() bool { st, g := state(); gen0 = g; return st == "healthy" }, 180*time.Second) {
		t.Fatalf("an always-on backend never started by itself: %v", func() string { s, _ := state(); return s }())
	}
	if c, _ := req(t, "POST", "/api/apps/always/die", ""); c != 200 {
		t.Fatalf("die: %d", c)
	}
	if !waitFor(func() bool { st, g := state(); return st == "healthy" && g > gen0 }, 60*time.Second) {
		st, g := state()
		t.Fatalf("it never came back after exiting: %s gen %v (was %v)", st, g, gen0)
	}
}
