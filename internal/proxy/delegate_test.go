package proxy

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// One tile API read as the person, through delegation, all the way to a
// backend: a delegate principal (auth/delegate.go) with the decision the
// broker's routeDelegate gives it reaches the person's partition of the
// tile, and the backend sees who it is — X-XBin-From user:<id>, the
// person and their level, the role clamped to it, X-XBin-Agent — and none
// of what the caller sent: not its X-XBin-* headers, and not the delegate
// credential, which it could replay as the person's agent.
func TestDelegateCallReachesBackend(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "apps", "pg", "xbin.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"runtime":"go","partition":["user","global"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{Root: root, PartitionModes: func(a registry.PartitionAsk) registry.PartitionMode {
		if a.Requested == nil {
			return registry.PartitionMode{}
		}
		return registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: *a.Requested}
	}}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := map[string]string{"path": r.URL.Path, "authorization": r.Header.Get("Authorization")}
		for k := range r.Header {
			if len(k) > 7 && k[:7] == "X-Xbin-" {
				seen[k] = r.Header.Get(k)
			}
		}
		_ = json.NewEncoder(w).Encode(seen)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	fp := &fakeParts{sock: sock, holds: map[string]int{}}
	px := &Proxy{Reg: reg, Partitions: fp, Hub: events.NewHub(), UserLevel: func(uid, tile string) string { return "write" }}
	var asked auth.Principal
	px.Route = func(p auth.Principal, c *registry.Component, q string) Decision {
		asked = p
		return Decision{Deployment: "main", Role: "writer", Partition: "user:alice", CallerPartition: "user:alice", CallerPartitionID: alicePKey,
			Attribute: &auth.Attribution{UserID: "alice", Level: "write", Role: "writer", Agent: "xbin/agent"}}
	}

	delegate := auth.Principal{UserID: "alice", Via: "delegate", Agent: "xbin/agent"}
	r := httptest.NewRequest("GET", "/api/apps/pg/notes?q=1", nil)
	r.Header.Set("Authorization", auth.DelegateScheme+" the-instance-token")
	r.Header.Set("X-XBin-Agent", "spoofed")
	r.Header.Set("X-XBin-User", "mallory")
	r.Header.Set("X-XBin-Dry-Run", "1")
	rec := httptest.NewRecorder()
	px.ServeHTTP(rec, r.WithContext(auth.WithPrincipal(r.Context(), delegate)))
	if rec.Code != 200 {
		t.Fatalf("the call: %d %s", rec.Code, rec.Body.String())
	}
	var seen map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &seen); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"path": "/notes", "authorization": "",
		"X-Xbin-From": "user:alice", "X-Xbin-Role": "writer", "X-Xbin-User": "alice", "X-Xbin-User-Level": "write",
		"X-Xbin-Agent": "xbin/agent", "X-Xbin-Partition": "user:alice", "X-Xbin-Partition-Id": alicePKey,
	}
	for k, v := range want {
		if seen[k] != v {
			t.Errorf("the backend saw %s = %q, want %q", k, seen[k], v)
		}
	}
	for k := range seen {
		if _, ok := want[k]; !ok {
			t.Errorf("the backend saw %s = %q, which xbind didn't set", k, seen[k])
		}
	}
	if asked.Agent != "xbin/agent" || asked.UserID != "alice" || asked.Component != "" {
		t.Errorf("Route was asked about %+v", asked)
	}
	if got := fp.asked; len(got) != 1 || got[0] != "apps/pg main user:alice interactive" {
		t.Errorf("the partition runner was asked %v, want alice's partition", got)
	}

	// a call a person or a tile makes themselves never carries X-XBin-Agent
	px.Route = func(auth.Principal, *registry.Component, string) Decision {
		return Decision{Deployment: "main", Role: "admin", Partition: "user:alice", CallerPartition: "user:alice", CallerPartitionID: alicePKey}
	}
	r = httptest.NewRequest("GET", "/api/apps/pg/notes", nil)
	r.Header.Set("X-XBin-Agent", "spoofed")
	rec = httptest.NewRecorder()
	px.ServeHTTP(rec, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame"})))
	seen = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &seen)
	if rec.Code != 200 || seen["X-Xbin-Agent"] != "" {
		t.Errorf("alice's own frame: %d, X-XBin-Agent %q", rec.Code, seen["X-Xbin-Agent"])
	}
}
