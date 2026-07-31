//go:build integration

package test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIngress verifies the whole publish path end to end against a dedicated
// instance with the builtin ingress listener on (plans/ingress.md): an http
// expose becomes Host-routed public traffic confined to its declared paths
// with the anonymous `ingress` principal injected; a stream expose becomes a
// live host-port relay into the backend; unbinding cuts both.
func TestIngress(t *testing.T) {
	dir := t.TempDir()
	iWS := filepath.Join(dir, "ws")

	freePort := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().String()
	}
	consoleAddr := freePort()
	ingressAddr := freePort()
	echoAddr := freePort()

	// The site tile: one http expose (root + a public API subtree; /secret
	// stays private) and one stream expose (an echo listener on :7777).
	writeWS := func(rel, content string) {
		p := filepath.Join(iWS, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeWS("apps/site/xbin.json", `{
		"runtime": "go",
		"exposes": {
			"web":  {"kind": "http", "paths": ["/", "/api/public/*"]},
			"echo": {"kind": "stream", "port": 7777}
		}
	}`)
	writeWS("apps/site/go.mod", "module site\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n")
	writeWS("apps/site/backend/main.go", `package main

import (
	"fmt"
	"net"
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	go func() { // the exposed stream endpoint: ordinary net.Listen + echo
		ln, err := net.Listen("tcp", ":7777")
		if err != nil {
			return
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); buf := make([]byte, 256); for { n, err := c.Read(buf); if n > 0 { c.Write(buf[:n]) }; if err != nil { return } } }(c)
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "root from=%s host=%s ingress=%v", xbin.Caller(r).From,
			r.Header.Get("X-XBin-Ingress-Host"), xbin.Caller(r).Ingress())
	})
	mux.HandleFunc("GET /api/public/ping", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "pong")
	})
	mux.HandleFunc("GET /secret", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "SECRET-DATA")
	})
	xbin.Serve(mux)
}
`)

	cmd := exec.Command(xbindBin, "--workspace", iWS, "--listen", consoleAddr,
		"--no-auth", "--ingress-listen", ingressAddr)
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	base := "http://" + consoleAddr
	if !waitFor(func() bool {
		r, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	}, 10*time.Second) {
		t.Fatal("ingress instance never healthy")
	}

	do := func(method, path, body string) (int, string) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		rq, _ := http.NewRequest(method, base+path, rd)
		if body != "" {
			rq.Header.Set("Content-Type", "application/json")
		}
		r, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}

	// Nothing is published yet: the ingress listener knows no hosts.
	pub := func(host, path string) (int, string) {
		rq, _ := http.NewRequest("GET", "http://"+ingressAddr+path, nil)
		rq.Host = host
		r, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatalf("ingress GET %s%s: %v", host, path, err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	if c, _ := pub("site.test", "/"); c != 404 {
		t.Fatalf("unbound expose must be unreachable, got %d", c)
	}

	// Publish: bind the http expose to runtime with an exact host, and the
	// stream expose to a host port.
	if c, b := do("POST", "/api/xbin/bindings",
		`{"component":"apps/site","slot":"web","provider":"runtime","host":"site.test"}`); c != 200 {
		t.Fatalf("bind web: %d %s", c, b)
	}
	if c, b := do("POST", "/api/xbin/bindings",
		fmt.Sprintf(`{"component":"apps/site","slot":"echo","provider":"runtime","listen":%q}`, echoAddr)); c != 200 {
		t.Fatalf("bind echo: %d %s", c, b)
	}

	// Public HTTP: the declared paths pass (cold build on first hit), with
	// the ingress principal + public host injected; everything else 404s.
	if !waitFor(func() bool {
		c, b := pub("site.test", "/")
		return c == 200 && strings.Contains(b, "from=ingress")
	}, 120*time.Second) {
		c, b := pub("site.test", "/")
		t.Fatalf("published root never served: %d %s", c, b)
	}
	if _, b := pub("site.test", "/"); !strings.Contains(b, "host=site.test") || !strings.Contains(b, "ingress=true") {
		t.Fatalf("ingress identity not injected: %s", b)
	}
	if c, b := pub("site.test", "/api/public/ping"); c != 200 || b != "pong" {
		t.Fatalf("public subtree: %d %s", c, b)
	}
	if c, b := pub("site.test", "/secret"); c != 404 || strings.Contains(b, "SECRET") {
		t.Fatalf("undeclared path must 404: %d %s", c, b)
	}
	if c, b := pub("site.test", "/api/public/../../secret"); c != 404 || strings.Contains(b, "SECRET") {
		t.Fatalf("traversal must not escape the allowlist: %d %s", c, b)
	}
	if c, _ := pub("other.test", "/"); c != 404 {
		t.Fatalf("unrouted host must 404, got %d", c)
	}
	// The console API surface is structurally unreachable through ingress.
	if c, _ := pub("site.test", "/api/xbin/status"); c != 404 {
		t.Fatalf("/api/xbin must not exist on the ingress listener, got %d", c)
	}

	// Stream plane: the host port relays into the backend's :7777 echo.
	conn, err := net.DialTimeout("tcp", echoAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial stream relay: %v", err)
	}
	if _, err := conn.Write([]byte("ping-thru")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 9)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping-thru" {
		t.Fatalf("stream echo through relay: %q %v", buf, err)
	}
	conn.Close()

	// The admin overview sees both.
	if c, b := do("GET", "/api/xbin/ingress", ""); c != 200 ||
		!strings.Contains(b, `"site.test"`) || !strings.Contains(b, `"echo"`) {
		t.Fatalf("ingress overview: %d %s", c, b)
	}

	// Unpublish both: the host 404s and the port closes.
	if c, _ := do("DELETE", "/api/xbin/bindings", `{"component":"apps/site","slot":"web"}`); c != 200 {
		t.Fatal("unbind web")
	}
	if c, _ := do("DELETE", "/api/xbin/bindings", `{"component":"apps/site","slot":"echo"}`); c != 200 {
		t.Fatal("unbind echo")
	}
	if !waitFor(func() bool { c, _ := pub("site.test", "/"); return c == 404 }, 10*time.Second) {
		t.Fatal("unpublished host still routed")
	}
	if !waitFor(func() bool {
		c, err := net.DialTimeout("tcp", echoAddr, 300*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err != nil
	}, 10*time.Second) {
		t.Fatal("unpublished stream port still open")
	}
}
