//go:build integration

package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A backend may call xbind before it calls xbin.Serve (docs/sdk.md →
// Startup): register its cron jobs and bus subscriptions, read and write its
// kv, read its own row and whoami, call a peer tile — the report of
// 2026-11-16, a tile whose main() registered its cron job before Serve and
// was left "failed · gen 2" with "timeout dialing backend socket". These
// tests pin that nothing in xbind holds such a call until the backend is
// healthy, on every way a generation starts: the first start, a live-reload
// rebuild, a crash restart, a deployments restart and an xbind restart, with
// and without --isolate. And what xbind does when a start does stall: time
// spent waiting on xbind's own answer isn't charged to the backend's
// health deadline, and a backend that never listened says so in its error
// and its log.

// preServeMain is a backend that makes each call before Serve, each bounded
// (a call that hangs fails the test by name, not by a health timeout), and
// serves what it saw at /pre.
const preServeMain = `package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	var seen []string
	call := func(method, url, body string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, method, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := xbin.Client().Do(req)
		if err != nil {
			seen = append(seen, fmt.Sprintf("%s %s: %v", method, url, err))
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		seen = append(seen, fmt.Sprintf("%s %s: %d %.80s", method, url, resp.StatusCode, b))
	}
	call("PUT", "http://xbin/api/xbin/cron/jobs", ` + "`" + `{"name":"tick","resource":"` + "`" + `+xbin.Resource("cron")+` + "`" + `","schedule":"@every 1h","path":"/tick"}` + "`" + `)
	call("PUT", "http://xbin/api/xbin/bus/subscriptions", ` + "`" + `{"name":"s","resource":"` + "`" + `+xbin.Resource("bus")+` + "`" + `","prefix":"ev/","path":"/tick"}` + "`" + `)
	call("PUT", "http://xbin/api/xbin/kv/"+xbin.Resource("kv")+"/k", ` + "`" + `"v"` + "`" + `)
	call("GET", "http://xbin/api/xbin/kv/"+xbin.Resource("kv")+"/k", "")
	call("GET", "http://xbin/api/xbin/whoami", "")
	call("GET", "http://xbin/api/xbin/components/"+xbin.Self(), "")
	call("GET", "http://xbin/api/apps/prestart/peer/hi", "")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /tick", func(w http.ResponseWriter, r *http.Request) {})
	sock := os.Getenv("XBIN_SOCKET") // .../g<gen>.sock: the generation answering
	mux.HandleFunc("GET /pre", func(w http.ResponseWriter, r *http.Request) {
		xbin.WriteJSON(w, 200, map[string]any{"sock": sock, "seen": seen})
	})
	mux.HandleFunc("GET /die", func(w http.ResponseWriter, r *http.Request) { os.Exit(3) })
	xbin.Serve(mux)
}
`

const peerMain = `package main

import (
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hi", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) })
	xbin.Serve(mux)
}
`

// preServeTiles are the scope apps/prestart: its resources, the probe tile
// (alwaysOn, so an xbind restart starts it at boot) and a peer it calls.
func preServeTiles() map[string]string {
	gomod := func(m string) string {
		return "module " + m + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n"
	}
	return map[string]string{
		"apps/prestart/scope.json":           `{"resources":{"cron":{"type":"cron"},"kv":{"type":"kv"},"bus":{"type":"bus"}}}`,
		"apps/prestart/peer/xbin.json":       `{"runtime":"go"}`,
		"apps/prestart/peer/go.mod":          gomod("peer"),
		"apps/prestart/peer/backend/main.go": peerMain,
		"apps/prestart/probe/xbin.json": `{"runtime":"go","alwaysOn":true,"uses":[
			{"target":"res:apps/prestart/cron","role":"writer"},{"target":"res:apps/prestart/kv","role":"writer"},
			{"target":"res:apps/prestart/bus","role":"reader"},{"target":"apps/prestart/peer","role":"reader"}]}`,
		"apps/prestart/probe/go.mod":          gomod("probe"),
		"apps/prestart/probe/backend/main.go": preServeMain,
	}
}

// preServeDaemon is what the checks need of an xbind: a request, and its
// workspace.
type preServeDaemon struct {
	do func(method, path, body string) (int, string)
	ws string
}

func writeTiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// checkPreServe waits until a generation newer than after answers — the
// generation that answers says which it is (its socket is g<gen>.sock; the
// tile's status names the generation starting, before it serves) — then
// asserts every call it made before Serve answered 200. It returns that
// generation.
func checkPreServe(t *testing.T, d preServeDaemon, how string, after int) int {
	t.Helper()
	var pre struct {
		Sock string   `json:"sock"`
		Seen []string `json:"seen"`
	}
	var gen int
	var body string
	if !waitFor(func() bool {
		var c int
		c, body = d.do("GET", "/api/apps/prestart/probe/pre", "")
		if c != 200 || json.Unmarshal([]byte(body), &pre) != nil {
			return false
		}
		_, _ = fmt.Sscanf(filepath.Base(pre.Sock), "g%d.sock", &gen)
		return gen > after
	}, 3*time.Minute) {
		t.Fatalf("%s: no generation after %d answered: %s", how, after, body)
	}
	if len(pre.Seen) != 7 {
		t.Fatalf("%s: the probe made %d calls: %q", how, len(pre.Seen), pre.Seen)
	}
	for _, s := range pre.Seen {
		if !strings.Contains(s, ": 200 ") {
			t.Errorf("%s (gen %d): a call before Serve failed: %s", how, gen, s)
		}
	}
	return gen
}

// preServeStarts drives the probe through the first start, a live-reload
// rebuild and a crash restart, checking its calls each time.
func preServeStarts(t *testing.T, d preServeDaemon) int {
	t.Helper()
	// the peer warm first: a call to a tile that is still building waits for
	// its build, which takes longer than any health deadline (docs/sdk.md)
	if !waitFor(func() bool { c, _ := d.do("GET", "/api/apps/prestart/peer/hi", ""); return c == 200 }, 3*time.Minute) {
		t.Fatal("the peer never came up")
	}
	gen := checkPreServe(t, d, "first start", 0)
	writeTiles(t, d.ws, map[string]string{"apps/prestart/probe/backend/main.go": preServeMain + "\n// a save\n"})
	gen = checkPreServe(t, d, "live-reload rebuild", gen)
	d.do("GET", "/api/apps/prestart/probe/die", "")
	return checkPreServe(t, d, "crash restart", gen)
}

func TestPreServeCalls(t *testing.T) {
	writeTiles(t, ws, preServeTiles())
	d := preServeDaemon{ws: ws, do: func(method, path, body string) (int, string) { return isoHTTP(baseURL, "", method, path, body) }}
	preServeStarts(t, d)
}

func TestPreServeCallsIsolated(t *testing.T) {
	iso := startIsolatedDaemon(t, isoOpts{Auth: true})
	d := preServeDaemon{ws: iso.WS, do: func(method, path, body string) (int, string) { return iso.do(t, method, path, body) }}
	writeTiles(t, iso.WS, preServeTiles())
	gen := preServeStarts(t, d)

	// a deployments restart (live reload paused: the tile has a record)
	if c, b := d.do("POST", "/api/xbin/deployments/live-reload/pause", `{"tile":"apps/prestart/probe"}`); c != 200 {
		t.Fatalf("pause: %d %s", c, b)
	}
	if c, b := d.do("POST", "/api/xbin/deployments/deploy", `{"tile":"apps/prestart/probe","deployment":"main","restart":true}`); c != 200 {
		t.Fatalf("restart: %d %s", c, b)
	}
	checkPreServe(t, d, "deployments restart", gen)

	// an xbind restart: alwaysOn starts it at boot, its artifact kept
	iso.restart(t)
	checkPreServe(t, d, "xbind restart", 0)
}

// A pre-Serve call xbind takes longer to answer than the health deadline (a
// log follow the backend gives up on after 7 s) doesn't fail the start: the
// time is xbind's, not the backend's. Before, the start failed at 5 s with
// "timeout dialing backend socket" while the backend was still waiting.
func TestPreServeSlowXbindCall(t *testing.T) {
	writeTiles(t, ws, map[string]string{
		"apps/slowstart/xbin.json": `{"runtime":"go"}`,
		"apps/slowstart/go.mod":    "module slowstart\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"apps/slowstart/backend/main.go": `package main

import (
	"context"
	"io"
	"net/http"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://xbin/api/xbin/logs?component="+xbin.Self()+"&follow=1", nil)
	if resp, err := xbin.Client().Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body) // until the deadline ends it
		resp.Body.Close()
	}
	cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hi", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) })
	xbin.Serve(mux)
}
`})
	var c int
	var body string
	if !waitFor(func() bool {
		c, body = get(t, "/api/apps/slowstart/hi")
		return c == 200 || strings.Contains(body, "healthy")
	}, 3*time.Minute) {
		t.Fatalf("never answered: %d %s", c, body)
	}
	if c != 200 || body != "hi" {
		t.Fatalf("a backend that waited 7 s on xbind before Serve: %d %s", c, body)
	}
}

// A backend that runs but never listens says so: the error names the state
// (not "exited", not "listening but unhealthy"), that it printed nothing,
// and the startup docs; its log says it after the start marker.
func TestPreServeNeverListens(t *testing.T) {
	writeTiles(t, ws, map[string]string{
		"apps/neverserves/xbin.json": `{"runtime":"go"}`,
		"apps/neverserves/go.mod":    "module neverserves\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"apps/neverserves/backend/main.go": `package main

import xbin "github.com/xbin-dev/xbin/sdk"

func main() {
	_ = xbin.Self()
	select {} // stuck before Serve
}
`})
	var c int
	var body string
	if !waitFor(func() bool {
		c, body = get(t, "/api/apps/neverserves/x")
		return c == 502 && strings.Contains(body, "healthy")
	}, 3*time.Minute) {
		t.Fatalf("never failed: %d %s", c, body)
	}
	for _, want := range []string{"timeout dialing backend socket", "running but never started listening", "has not called Serve",
		"printed nothing since it started", "/docs/sdk.md#startup"} {
		if !strings.Contains(body, want) {
			t.Errorf("the failure lacks %q: %s", want, body)
		}
	}
	_, logs := get(t, "/api/xbin/logs?component=apps/neverserves")
	if !strings.Contains(logs, "--- gen 1 stopped: timeout dialing backend socket") {
		t.Errorf("bx logs after the marker: %q", logs)
	}
}

// A backend whose first start is stuck past its deadline, and whose next
// would be fine, becomes healthy by itself with no save (D242): xbind
// starts a new generation of the same code 10 s after the failure. The
// first start marks a kv key and blocks before Serve; the retry finds the
// mark and serves. Before, the tile stayed failed until the next save.
func TestPreServeSlowOnceRetries(t *testing.T) {
	writeTiles(t, ws, map[string]string{
		"apps/slowonce/scope.json":     `{"resources":{"kv":{"type":"kv"}}}`,
		"apps/slowonce/tile/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/slowonce/kv","role":"writer"}]}`,
		"apps/slowonce/tile/go.mod":    "module slowonce\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"apps/slowonce/tile/backend/main.go": `package main

import (
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	url := "http://xbin/api/xbin/kv/" + xbin.Resource("kv") + "/started"
	resp, err := xbin.Client().Get(url)
	if err == nil {
		resp.Body.Close()
	}
	if err != nil || resp.StatusCode != 200 { // the first start: marked, then stuck before Serve
		req, _ := http.NewRequest("PUT", url, strings.NewReader(` + "`" + `"1"` + "`" + `))
		req.Header.Set("Content-Type", "application/json")
		if r, err := xbin.Client().Do(req); err == nil {
			r.Body.Close()
		}
		select {}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hi", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) })
	xbin.Serve(mux)
}
`})
	const tile = "apps/slowonce/tile"
	var c int
	var body string
	if !waitFor(func() bool {
		c, body = get(t, "/api/"+tile+"/hi")
		return c == 502 && strings.Contains(body, "never started listening")
	}, 3*time.Minute) {
		t.Fatalf("the first start never failed: %d %s", c, body)
	}
	_, rows := get(t, "/api/xbin/backends")
	var st map[string]struct {
		State string `json:"state"`
		Retry *struct {
			Attempt int `json:"attempt"`
			Of      int `json:"of"`
		} `json:"retry"`
	}
	if err := json.Unmarshal([]byte(rows), &st); err != nil {
		t.Fatalf("/backends: %v: %s", err, rows)
	}
	if r := st[tile]; r.State != "failed" || r.Retry == nil || r.Retry.Attempt != 1 || r.Retry.Of != 3 {
		t.Fatalf("waiting for the retry: %s", rows)
	}
	if !waitFor(func() bool { c, body = get(t, "/api/"+tile+"/hi"); return c == 200 }, time.Minute) {
		t.Fatalf("never became healthy by itself: %d %s", c, body)
	}
	_, logs := get(t, "/api/xbin/logs?component="+tile)
	for _, want := range []string{"--- gen 1 stopped: timeout dialing backend socket", "--- the start failed; retrying in 10s (1 of 3)", "--- gen 2 start"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log lacks %q: %q", want, logs)
		}
	}
}
