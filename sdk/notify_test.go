package xbin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// NotifyUser posts the notification through the gateway with the tile's
// token; refusals come back as errors, the rate limit as a typed one.
func TestNotifyUser(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var bodies []map[string]string
	var auths []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/xbin/notify" {
			http.Error(w, "bad route", 404)
			return
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies = append(bodies, b)
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		switch b["user"] {
		case "bob":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"that user cannot read this tile"}`))
		case "busy":
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"too many notifications for this user; try later"}`))
		default:
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	clientOnce = sync.Once{}
	defer func() { clientOnce = sync.Once{} }()

	ctx := context.Background()
	if err := NotifyUser(ctx, "alice", "Approval needed", "Deploy?", "#a/17"); err != nil {
		t.Fatal(err)
	}
	if err := NotifyUserWith(ctx, UserNotification{User: "alice", Title: "t", Kind: "approval", CollapseID: "a17"}); err != nil {
		t.Fatal(err)
	}
	err = NotifyUser(ctx, "bob", "x", "", "")
	if err == nil || errors.Is(err, ErrNotifyRateLimited) || !strings.Contains(err.Error(), "cannot read this tile") {
		t.Fatalf("403: %v", err)
	}
	if err := NotifyUser(ctx, "busy", "x", "", ""); !errors.Is(err, ErrNotifyRateLimited) {
		t.Fatalf("429: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := map[string]string{"user": "alice", "title": "Approval needed", "body": "Deploy?", "link": "#a/17", "kind": "", "collapseId": ""}
	for k, v := range want {
		if bodies[0][k] != v {
			t.Fatalf("body[%s] = %q, want %q (%v)", k, bodies[0][k], v, bodies[0])
		}
	}
	if bodies[1]["kind"] != "approval" || bodies[1]["collapseId"] != "a17" || auths[0] != "Bearer tok" {
		t.Fatalf("second call %v, auth %q", bodies[1], auths[0])
	}
}
