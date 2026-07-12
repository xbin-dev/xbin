package ingress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The public-path allowlist is THE security boundary for anonymous traffic
// (ING-5) — exhaustive table, including the traversal shapes that must not
// slip past it.
func TestPathAllowed(t *testing.T) {
	cases := []struct {
		patterns []string
		path     string
		want     bool
	}{
		{[]string{"/*"}, "/", true},
		{[]string{"/*"}, "/anything/at/all", true},
		{[]string{"/"}, "/", true},
		{[]string{"/"}, "/other", false},
		{[]string{"/api/public/*"}, "/api/public", true},
		{[]string{"/api/public/*"}, "/api/public/x/y", true},
		{[]string{"/api/public/*"}, "/api/publicity", false}, // prefix must be segment-aligned
		{[]string{"/api/public/*"}, "/api", false},
		{[]string{"/docs"}, "/docs", true},
		{[]string{"/docs"}, "/docs/", true}, // trailing slash matches the exact path
		{[]string{"/docs"}, "/docs/x", false},
		{[]string{}, "/", false}, // no declaration = nothing public
		{[]string{"/", "/api/public/*"}, "/api/public/feed", true},
		{[]string{"/", "/api/public/*"}, "/api/admin", false},
	}
	for _, c := range cases {
		if got := PathAllowed(c.patterns, c.path); got != c.want {
			t.Errorf("PathAllowed(%v, %q) = %v, want %v", c.patterns, c.path, got, c.want)
		}
	}
}

func TestCleanPath(t *testing.T) {
	cases := map[string]string{
		"":                          "/",
		"/":                         "/",
		"/a/b":                      "/a/b",
		"/a/b/":                     "/a/b/",
		"/a/../b":                   "/b",
		"/api/public/../../secret":  "/secret",                   // traversal resolves BEFORE matching
		"/api/public/%2e%2e/secret": "/api/public/%2e%2e/secret", // already-decoded input only
		"//a///b":                   "/a/b",
		"/.":                        "/",
	}
	for in, want := range cases {
		if got := CleanPath(in); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostOnlyAndZones(t *testing.T) {
	for in, want := range map[string]string{
		"Blog.Example.com":      "blog.example.com",
		"blog.example.com:8443": "blog.example.com",
		"blog.example.com.":     "blog.example.com",
		"[2001:db8::1]:443":     "2001:db8::1",
	} {
		if got := HostOnly(in); got != want {
			t.Errorf("HostOnly(%q) = %q, want %q", in, got, want)
		}
	}
	if !HostInZone("a.sites.example.com", "*.sites.example.com") {
		t.Error("one-label zone member rejected")
	}
	if !HostInZone("a.b.sites.example.com", "*.sites.example.com") {
		t.Error("deep zone member rejected")
	}
	if HostInZone("sites.example.com", "*.sites.example.com") {
		t.Error("zone apex must not match")
	}
	if HostInZone("evilsites.example.com", "*.sites.example.com") {
		t.Error("suffix must be label-aligned")
	}
	if HostInZone("bank.example.com", "*.sites.example.com") {
		t.Error("outside the zone")
	}
	if !ValidHost("a-b.example.com") || ValidHost("*.x.com") || ValidHost("-a.com") || ValidHost("") {
		t.Error("ValidHost")
	}
	if !ValidZone("*.sites.example.com") || ValidZone("sites.example.com") || ValidZone("*.") {
		t.Error("ValidZone")
	}
}

// The HTTP entry: host routing, allowlist gating, and path cleaning before
// the forward — anonymous requests reach exactly the declared surface.
func TestHTTPHandler(t *testing.T) {
	routes := map[string]Route{
		"blog.example.com": {Component: "apps/blog", Slot: "web", Paths: []string{"/", "/posts/*"}, Source: "runtime", Host: "blog.example.com"},
	}
	var forwarded []string
	h := &HTTPHandler{
		Source: "runtime",
		Lookup: func(source, host string) (Route, bool) {
			if source != "runtime" {
				t.Fatalf("lookup source %q", source)
			}
			rt, ok := routes[host]
			return rt, ok
		},
		Forward: func(w http.ResponseWriter, r *http.Request, rt Route) {
			forwarded = append(forwarded, rt.Component+r.URL.Path)
			w.WriteHeader(http.StatusOK)
		},
	}
	req := func(host, path string) int {
		r := httptest.NewRequest("GET", "http://"+host+path, nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if req("blog.example.com", "/") != 200 || req("blog.example.com:8080", "/posts/1") != 200 {
		t.Fatal("published paths must pass")
	}
	if req("blog.example.com", "/admin") != 404 {
		t.Fatal("undeclared path must 404")
	}
	if req("other.example.com", "/") != 404 {
		t.Fatal("unrouted host must 404")
	}
	// Traversal cannot escape the allowlist — and what forwards is the clean path.
	if req("blog.example.com", "/posts/../admin") != 404 {
		t.Fatal("cleaned traversal must be gated")
	}
	if code := req("blog.example.com", "/posts/x/../y"); code != 200 {
		t.Fatal("in-subtree traversal is fine after cleaning")
	}
	want := []string{"apps/blog/", "apps/blog/posts/1", "apps/blog/posts/y"}
	if fmt.Sprint(forwarded) != fmt.Sprint(want) {
		t.Fatalf("forwarded %v, want %v", forwarded, want)
	}
}

// echoDialer fakes runner.DialInto with an in-process echo service.
func echoDialer(t *testing.T) func(ctx context.Context, comp, proto string, port int) (net.Conn, error) {
	return func(ctx context.Context, comp, proto string, port int) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { // echo server on the "netns" side
			buf := make([]byte, 4096)
			for {
				n, err := b.Read(buf)
				if n > 0 {
					if _, err := b.Write(buf[:n]); err != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		return a, nil
	}
}

func TestStreamsTCP(t *testing.T) {
	port := freePort(t)
	spec := StreamSpec{Component: "apps/game", Slot: "game", Proto: "tcp", Listen: fmt.Sprintf("127.0.0.1:%d", port), Port: 2456}
	s2 := &Streams{Dial: echoDialer(t)}
	defer s2.Close()
	s2.Reconcile([]StreamSpec{spec})
	if st := s2.Status(); len(st) != 1 || st[0].Error != "" {
		t.Fatalf("status: %+v", st)
	}
	conn, err := net.DialTimeout("tcp", spec.Listen, 2*time.Second)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo through relay: %q %v", buf, err)
	}
	// Unbinding severs live flows and closes the port.
	s2.Reconcile(nil)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("live conn must be severed on unbind")
	}
	if c, err := net.DialTimeout("tcp", spec.Listen, 500*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("listener must be closed on unbind")
	}
}

func TestStreamsUDP(t *testing.T) {
	port := freePort(t)
	spec := StreamSpec{Component: "apps/game", Slot: "game", Proto: "udp", Listen: fmt.Sprintf("127.0.0.1:%d", port), Port: 2456}
	s := &Streams{Dial: echoDialer(t)}
	defer s.Close()
	s.Reconcile([]StreamSpec{spec})
	conn, err := net.Dial("udp", spec.Listen)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("dgram")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "dgram" {
		t.Fatalf("udp echo: %q %v", buf[:n], err)
	}
}

func TestStreamsListenErrorSurfaces(t *testing.T) {
	s := &Streams{Dial: echoDialer(t)}
	defer s.Close()
	s.Reconcile([]StreamSpec{{Component: "a", Slot: "x", Proto: "tcp", Listen: "256.0.0.1:1", Port: 1}})
	st := s.Status()
	if len(st) != 1 || st[0].Error == "" {
		t.Fatalf("listen failure must surface in status: %+v", st)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
