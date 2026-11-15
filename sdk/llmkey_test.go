package xbin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// LLMKeyFor and RevokeLLMKey against a gateway that answers as xbind does.
func TestLLMKeyFor(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+" "+r.Header.Get("Authorization")+" "+string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			User   string
			TTLSec int64
		}
		_ = json.Unmarshal(body, &req)
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/xbin/llm/keys/lk_1":
			w.WriteHeader(204)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/xbin/llm/keys" && r.URL.Query().Get("label") == "codex run 12":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && r.URL.Path == "/api/xbin/llm/keys/lk_1/renew":
			_, _ = w.Write([]byte(`{"id":"lk_1","expires":"2026-11-16T12:00:00Z"}`))
		case req.User == "many":
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"too many live LLM keys for kim on apps/agent","code":"xbin_llm_keys"}`))
		case r.Method != http.MethodPost || r.URL.Path != "/api/xbin/llm/keys":
			w.WriteHeader(404)
			_, _ = w.Write([]byte("404 page not found\n"))
		case req.User == "mallory":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"an LLM key is for the person this credential acts for — kim — not mallory"}`))
		case req.User == "off":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"this workspace's LLM connector is not configured","code":"xbin_llm_not_configured"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"lk_1","token":"xbl_abc","user":"kim","component":"apps/agent","expires":"2026-11-15T12:00:00Z","url":"/api/xbin/llm/v1"}`))
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	clientOnce = sync.Once{}
	defer func() { clientOnce = sync.Once{} }()
	ctx := context.Background()

	k, err := LLMKeyFor(ctx, LLMKeyOpts{User: "user:kim", TTL: 90 * time.Minute, Label: "codex run 12", Sandbox: "sb-1"})
	if err != nil {
		t.Fatal(err)
	}
	if k.ID != "lk_1" || k.Token != "xbl_abc" || k.User != "kim" || k.Component != "apps/agent" || !k.Expires.Equal(time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("key: %+v", k)
	}
	if _, err := LLMKeyFor(ctx, LLMKeyOpts{User: "mallory"}); !errors.Is(err, ErrLLMKeyForbidden) || !strings.Contains(err.Error(), "not mallory") {
		t.Fatalf("another person's key: %v", err)
	}
	if _, err := LLMKeyFor(ctx, LLMKeyOpts{User: "off"}); !errors.Is(err, ErrLLMUnavailable) {
		t.Fatalf("no connector: %v", err)
	}
	if err := RevokeLLMKey(ctx, "lk_1"); err != nil {
		t.Fatal(err)
	}
	if err := RevokeLLMKey(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := RevokeLLMKey(ctx, "other/../x"); !errors.Is(err, ErrLLMUnavailable) { // escaped: one path segment, which this gateway doesn't know
		t.Fatalf("an odd id: %v", err)
	}
	if err := RevokeLLMKeysLabeled(ctx, "codex run 12"); err != nil {
		t.Fatal(err)
	}
	if err := RevokeLLMKeysLabeled(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// a longer run's key is renewed; one that ended is gone; the cap is its own error
	if exp, err := RenewLLMKey(ctx, "lk_1", 48*time.Hour); err != nil || exp.Format(time.RFC3339) != "2026-11-16T12:00:00Z" {
		t.Fatalf("renew: %v %v", exp, err)
	}
	if _, err := RenewLLMKey(ctx, "lk_gone", 0); !errors.Is(err, ErrLLMKeyGone) {
		t.Fatalf("renewing a key that ended: %v", err)
	}
	if _, err := LLMKeyFor(ctx, LLMKeyOpts{User: "many"}); !errors.Is(err, ErrLLMKeyLimit) || !strings.Contains(err.Error(), "too many live LLM keys") {
		t.Fatalf("at the cap: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen[6] != `POST /api/xbin/llm/keys/lk_1/renew Bearer tok {"ttlSec":172800}` {
		t.Fatalf("the renew: %q", seen[6])
	}
	if len(seen) != 9 || seen[5] != "DELETE /api/xbin/llm/keys Bearer tok " || seen[0] != `POST /api/xbin/llm/keys Bearer tok {"label":"codex run 12","sandbox":"sb-1","ttlSec":5400,"user":"kim"}` ||
		seen[3] != "DELETE /api/xbin/llm/keys/lk_1 Bearer tok " || seen[4] != "DELETE /api/xbin/llm/keys/other%2F..%2Fx Bearer tok " {
		t.Fatalf("requests: %q", seen)
	}
}
