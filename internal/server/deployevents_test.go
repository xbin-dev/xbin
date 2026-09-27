package server_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/boot"
)

// evNodeServer is the smallest node backend: it listens, and answers.
const evNodeServer = "require('http').createServer((q, s) => s.end('ok')).listen(process.env.XBIN_SOCKET);\n"

// evDaemon boots an xbind in-process (auth on, no isolation) on a
// zero-state workspace and returns its address and owner token.
func evDaemon(t *testing.T) (ws, addr, owner string) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	ws = filepath.Join(t.TempDir(), "ws")
	for rel, body := range map[string]string{
		"xbin.json":                     `{"schema":1}`,
		"apps/ev/xbin.json":             `{}`,
		"apps/ev/index.html":            "<!doctype html><html><head></head><body>ev</body></html>\n",
		"apps/evnode/xbin.json":         `{"runtime":"node"}`,
		"apps/evnode/backend/server.js": evNodeServer,
		"apps/evbroken/xbin.json":       `{"runtime":"node"}`,
		"apps/evbroken/README.txt":      "no backend/server.js: every build fails\n",
		"notes+ideas/xbin.json":         `{}`,
		"notes+ideas/index.html":        "<!doctype html><html><head></head><body>n</body></html>\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if sdk, err := filepath.Abs("../../sdk"); err == nil {
		t.Setenv("XBIN_SDK_PATH", sdk)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	cfg := &boot.Config{Workspace: ws, Listener: ln, Listen: ln.Addr().String(), InsecureVault: true,
		Privileges: boot.NoPrivileges{}, Stdout: io.Discard, Version: "test", LimitMem: "2G",
		Ready: func(a string) { ready <- a }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the daemon did not stop")
		}
	})
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("boot: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon never served")
	}
	tok, err := os.ReadFile(filepath.Join(ws, ".xbin", "token"))
	if err != nil {
		t.Fatal(err)
	}
	return ws, addr, strings.TrimSpace(string(tok))
}

// evTS masks the one run-to-run value of today's events: a status
// report's unix timestamp.
var evTS = regexp.MustCompile(`"ts":[0-9]+`)

// covers PO-5 Z3 SC-ZERO — the exact bytes a /ws/events subscriber
// receives for a zero-state workspace's events, from their real
// producers: a save's reload (the watch loop), a backend's first start
// and a save's sequence reload, build-start, build-ok, a failing build's
// reload, build-start, build-error, and status reports. No event carries a
// deployment key and no deployments event appears; a tile whose own path
// holds + is named as itself. With no node on PATH the running-backend
// sequences are skipped, said so. Hand-maintained goldens: changing one is
// a compat change (12-compat.md).
func TestEventBytesZeroState(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	_, err := exec.LookPath("node")
	haveNode := err == nil
	ws, addr, owner := evDaemon(t)
	hdr := http.Header{"Authorization": {"Bearer " + owner}}
	conn, resp, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/events", hdr)
	if err != nil {
		t.Fatalf("events socket: %v %v", err, resp)
	}
	defer conn.Close()
	frames := make(chan string, 64)
	go func() {
		for {
			_, b, err := conn.ReadMessage()
			if err != nil {
				close(frames)
				return
			}
			frames <- evTS.ReplaceAllString(string(b), `"ts":<ts>`)
		}
	}()
	// expect reads exactly the frames want names, in order, then checks that
	// nothing else arrives for a bounded while (a negative: no condition to
	// wait for).
	expect := func(step string, want ...string) {
		t.Helper()
		for i, w := range want {
			select {
			case got := <-frames:
				if got != w {
					t.Errorf("%s: frame %d\n  got  %q\n  want %q", step, i+1, got, w)
				}
			case <-time.After(60 * time.Second):
				t.Fatalf("%s: frame %d never arrived (want %q)", step, i+1, w)
			}
		}
		select {
		case got := <-frames:
			t.Errorf("%s: an extra frame %q", step, got)
		case <-time.After(700 * time.Millisecond):
		}
	}
	save := func(rel string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(ws, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("\n")
		f.Close()
	}
	call := func(method, path, body string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+owner)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}

	save("apps/ev/index.html")
	expect("a static tile's save", `{"type":"reload","component":"apps/ev"}`+"\n")
	save("notes+ideas/index.html")
	expect("a + tile's save", `{"type":"reload","component":"notes+ideas"}`+"\n")

	call("GET", "/api/apps/evbroken/ping", "")
	expect("a backend that can't build, first request",
		`{"type":"build-start","component":"apps/evbroken"}`+"\n",
		`{"type":"build-error","component":"apps/evbroken","text":"build failed:\nentry backend/server.js not found (set \"entry\" in xbin.json)"}`+"\n")
	save("apps/evbroken/README.txt")
	expect("a failing build's save",
		`{"type":"reload","component":"apps/evbroken"}`+"\n",
		`{"type":"build-start","component":"apps/evbroken"}`+"\n",
		`{"type":"build-error","component":"apps/evbroken","text":"build failed:\nentry backend/server.js not found (set \"entry\" in xbin.json)"}`+"\n")

	if haveNode {
		call("GET", "/api/apps/evnode/ping", "")
		expect("a backend's first start",
			`{"type":"build-start","component":"apps/evnode"}`+"\n",
			`{"type":"build-ok","component":"apps/evnode"}`+"\n")
		save("apps/evnode/backend/server.js")
		expect("a backend's save and swap",
			`{"type":"reload","component":"apps/evnode"}`+"\n",
			`{"type":"build-start","component":"apps/evnode"}`+"\n",
			`{"type":"build-ok","component":"apps/evnode"}`+"\n")
	} else {
		t.Log("SKIP (partial): no node on PATH — the running-backend sequences are not checked")
	}

	call("POST", "/api/xbin/tile-report", `{"level":"warn","message":"disk low","component":"apps/ev"}`)
	expect("a status report", `{"type":"status","component":"apps/ev","data":{"level":"warn","message":"disk low","ts":<ts>}}`+"\n")
	call("POST", "/api/xbin/tile-report?component=notes%2Bideas", `{"level":"info","message":"hi","transient":true}`)
	expect("a transient status on a + tile", `{"type":"status","component":"notes+ideas","data":{"level":"info","message":"hi","transient":true,"ts":<ts>}}`+"\n")
	call("POST", "/api/xbin/tile-report", `{"level":"ok","component":"apps/ev"}`)
	expect("a status cleared", `{"type":"status","component":"apps/ev","data":{"level":"ok","message":"","ts":<ts>}}`+"\n")
}
