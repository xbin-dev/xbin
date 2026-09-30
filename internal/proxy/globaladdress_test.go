package proxy

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// globalWorld: apps/pg (user + global) and apps/pu (user) recorded
// partitioned, apps/pz whose mode record can't be read (Invalid, R
// unknown), apps/x unpartitioned; Route and RouteGlobal record which one
// was asked and answer a user partition of the fake runner (whatever the
// tile), whose backend echoes the URL it got and the identity headers.
func globalWorld(t *testing.T) (*Proxy, *[]string, *Decision) {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"apps/pg/xbin.json": `{"runtime":"go","partition":["user","global"]}`,
		"apps/pu/xbin.json": `{"runtime":"go","partition":["user"]}`,
		"apps/pz/xbin.json": `{"runtime":"go","partition":["user"]}`,
		"apps/x/xbin.json":  `{"runtime":"go"}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := &registry.Registry{Root: root, PartitionModes: func(a registry.PartitionAsk) registry.PartitionMode {
		switch {
		case a.Tile == "apps/pz":
			return registry.PartitionMode{State: registry.PartitionInvalid, Unknown: true, Err: "partition: the mode record can't be read"}
		case a.Requested == nil:
			return registry.PartitionMode{}
		}
		return registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: *a.Requested}
	}}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"url": r.URL.RequestURI(), "user": r.Header.Get(HeaderUser),
			"role": r.Header.Get(HeaderRole), "level": r.Header.Get(HeaderUserLevel), "partition": r.Header.Get(HeaderPartition)})
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	asked := &[]string{}
	d := &Decision{Deployment: "main", Role: "admin", Partition: "user:alice", CallerPartition: "user:alice", CallerPartitionID: alicePKey}
	px := &Proxy{Reg: reg, Partitions: &fakeParts{sock: sock, holds: map[string]int{}}, Hub: events.NewHub()}
	px.Route = func(_ auth.Principal, c *registry.Component, _ string) Decision {
		*asked = append(*asked, "Route "+c.Path)
		return *d
	}
	px.RouteGlobal = func(_ auth.Principal, c *registry.Component, _ string) Decision {
		*asked = append(*asked, "RouteGlobal "+c.Path)
		return *d
	}
	return px, asked, d
}

// covers PD-16 S3 — ?xbin-partition=global in the proxy (plans/partitions/05
// §6, 10 §A.6): on a partitioned target (or one whose mode can't be read)
// it is consumed — the backend never sees it, the rest of the query passes
// — and RouteGlobal decides the call instead of Route; on an unpartitioned
// target the proxy reads nothing and the backend gets it as today. A value
// other than one "global" is 400, a path ticket's call 403, a proxy
// without RouteGlobal refuses (403), and RouteGlobal's "no global
// instance" is a 404.
func TestGlobalAddressProxy(t *testing.T) {
	px, asked, d := globalWorld(t)
	alice := auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame"}
	call := func(r *http.Request, p auth.Principal) (int, map[string]string, string) {
		t.Helper()
		*asked = nil
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, r)
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body, fmt.Sprint(*asked)
	}
	get := func(path string) *http.Request { return httptest.NewRequest("GET", path, nil) }
	for _, c := range []struct{ path, url, asked string }{
		// consumed on partitioned targets: RouteGlobal, and the backend never sees it
		{"/api/apps/pg/runs/42?xbin-partition=global", "/runs/42", "[RouteGlobal apps/pg]"},
		{"/api/apps/pg/runs?q=1&xbin-partition=global&b=%2F", "/runs?b=%2F&q=1", "[RouteGlobal apps/pg]"},
		{"/api/apps/pg/runs?xbin%2Dpartition=global", "/runs", "[RouteGlobal apps/pg]"},
		{"/api/apps/pu/runs?xbin-partition=global", "/runs", "[RouteGlobal apps/pu]"},
		// without it: Route, as before
		{"/api/apps/pg/runs?q=1", "/runs?q=1", "[Route apps/pg]"},
		// an unpartitioned target: read nothing, passed on as any parameter
		{"/api/apps/x/runs?xbin-partition=global&q=1", "/runs?q=1&xbin-partition=global", "[Route apps/x]"},
		{"/api/apps/x/runs?xbin-partition=user:bob", "/runs?xbin-partition=user%3Abob", "[Route apps/x]"},
	} {
		code, body, got := call(get(c.path), alice)
		if code != 200 || body["url"] != c.url || got != c.asked {
			t.Errorf("%s: %d %v, asked %s; want 200 %q, asked %s", c.path, code, body, got, c.url, c.asked)
		}
	}
	// a mode record that can't be read: consumed, then the partition gate's 409
	if code, body, got := call(get("/api/apps/pz/runs?xbin-partition=global"), alice); code != http.StatusConflict || got != "[RouteGlobal apps/pz]" {
		t.Errorf("apps/pz: %d %v, asked %s", code, body, got)
	}
	// refusals, answered by the proxy before any route is asked
	for _, c := range []struct {
		name string
		r    *http.Request
		code int
		msg  string
	}{
		{"another value", get("/api/apps/pg/runs?xbin-partition=user:bob"), 400, errGlobalAddressValue.Error()},
		{"an empty value", get("/api/apps/pg/runs?xbin-partition="), 400, errGlobalAddressValue.Error()},
		{"twice", get("/api/apps/pg/runs?xbin-partition=global&xbin-partition=global"), 400, errGlobalAddressValue.Error()},
		{"a path ticket", get("/api/apps/pg/preview/x?xbin-partition=global").WithContext(auth.WithPathTicket(t.Context())), 403, errGlobalAddressTicket.Error()},
	} {
		code, body, got := call(c.r, alice)
		if code != c.code || body["error"] != c.msg || got != "[]" {
			t.Errorf("%s: %d %v, asked %s; want %d %q", c.name, code, body, got, c.code, c.msg)
		}
	}
	// a path ticket without the parameter is served as before
	if code, _, got := call(get("/api/apps/pg/preview/x").WithContext(auth.WithPathTicket(t.Context())), alice); code != 200 || got != "[Route apps/pg]" {
		t.Errorf("a path ticket without the parameter: %d, asked %s", code, got)
	}
	// RouteGlobal's refusals keep their status
	*d = Decision{Deny: util.NoGlobalInstance("apps/pu")}
	if code, body, _ := call(get("/api/apps/pu/runs?xbin-partition=global"), alice); code != 404 || body["error"] != "apps/pu has no global instance" {
		t.Errorf("no global instance: %d %v", code, body)
	}
	*d = Decision{Deny: fmt.Errorf("?xbin-partition=global addresses a tile's own global instance: apps/q can't use it on apps/pg")}
	if code, body, _ := call(get("/api/apps/pg/runs?xbin-partition=global"), auth.Principal{Component: "apps/q", Via: "instance"}); code != 403 || body["docs"] != "/docs/auth.md" {
		t.Errorf("another tile: %d %v", code, body)
	}
	// no RouteGlobal installed: refused, never routed as a plain call
	px.RouteGlobal = nil
	if code, body, got := call(get("/api/apps/pg/runs?xbin-partition=global"), alice); code != 403 || got != "[]" ||
		body["error"] != "this xbind can't address apps/pg's global instance" {
		t.Errorf("no RouteGlobal: %d %v, asked %s", code, body, got)
	}
}

// covers S3 — what the global instance receives for an attributed F5 call
// (05 §6): the person, their level and the clamped role, whatever the
// credential sent — a user partition's instance token names no person, and
// a forged X-XBin-User never survives — and X-XBin-Partition names the
// caller's partition.
func TestGlobalAddressAttribution(t *testing.T) {
	px, _, d := globalWorld(t)
	px.UserLevel = func(uid, tile string) string { return "write" } // what identify would say for a person on the credential
	*d = Decision{Deployment: "main", Role: "reader", Partition: "user:alice", CallerPartition: "user:alice", CallerPartitionID: alicePKey,
		Attribute: &auth.Attribution{UserID: "alice", Level: "read", Role: "reader"}}
	for _, p := range []auth.Principal{
		{Component: "apps/pg", Via: "instance", Partition: "user:alice"},
		{Component: "apps/pg", UserID: "alice", Via: "frame"},
	} {
		r := httptest.NewRequest("GET", "/api/apps/pg/runs?xbin-partition=global", nil)
		r.Header.Set(HeaderUser, "mallory")
		r.Header.Set(HeaderRole, "admin")
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, r)
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != 200 || body["user"] != "alice" || body["level"] != "read" || body["role"] != "reader" || body["partition"] != "user:alice" || body["url"] != "/runs" {
			t.Errorf("%s: %d %v", p.Via, rec.Code, body)
		}
	}
}
