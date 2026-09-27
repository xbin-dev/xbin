package xbin

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// AccessOf asks the gateway with the tile's token; a refusal is an error,
// an unknown person is level none.
func TestAccessOf(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var paths, auths []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch user := strings.TrimPrefix(r.URL.Path, "/api/xbin/access/"); user {
		case "alice":
			_, _ = w.Write([]byte(`{"user":"alice","level":"write","active":true}`))
		case "bob":
			_, _ = w.Write([]byte(`{"user":"bob","level":"none","active":false}`))
		case "a b":
			_, _ = w.Write([]byte(`{"user":"a b","level":"read","active":true}`))
		default:
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"a tile's backend asks (its instance token)"}`))
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	clientOnce = sync.Once{}
	defer func() { clientOnce = sync.Once{} }()

	ctx := context.Background()
	a, err := AccessOf(ctx, "user:alice")
	if err != nil || a != (UserAccess{User: "alice", Level: "write", Active: true}) || !a.CanRead() || !a.CanWrite() {
		t.Fatalf("alice: %+v %v", a, err)
	}
	b, err := AccessOf(ctx, "bob")
	if err != nil || b.CanRead() || b.Level != "none" {
		t.Fatalf("bob: %+v %v", b, err)
	}
	if c, err := AccessOf(ctx, "a b"); err != nil || !c.CanRead() || c.CanWrite() {
		t.Fatalf("a b: %+v %v", c, err)
	}
	if _, err := AccessOf(ctx, "carol"); err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "instance token") {
		t.Fatalf("a refusal: %v", err)
	}
	if _, err := AccessOf(ctx, " "); err == nil {
		t.Fatal("no user asked nothing")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 4 || paths[0] != "GET /api/xbin/access/alice" || paths[2] != "GET /api/xbin/access/a%20b" || auths[0] != "Bearer tok" {
		t.Fatalf("calls: %v %v", paths, auths)
	}
	// an inactive account never reads, whatever its level says
	if (UserAccess{Level: "terminal"}).CanRead() {
		t.Fatal("an inactive account reads")
	}
}
