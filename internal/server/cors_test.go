package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A sandboxed tile page (Origin: null) gets its preflights answered, MCP's
// streamable-HTTP headers included — the builtin chat tile sends
// Mcp-Session-Id and MCP-Protocol-Version on every call after initialize —
// and can read the session id a provider answers with. Other origins pass
// through untouched.
func TestNullOriginCORS(t *testing.T) {
	served := false
	h := nullOriginCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.Header().Set("Mcp-Session-Id", "s-1")
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/apps/crm/mcp", nil)
	req.Header.Set("Origin", "null")
	req.Header.Set("Access-Control-Request-Headers", "content-type,mcp-protocol-version,mcp-session-id,x-xbin-frame-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if served || w.Code != http.StatusNoContent {
		t.Fatalf("preflight: served=%v code=%d", served, w.Code)
	}
	allowed := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers"))
	for _, hd := range []string{"content-type", "x-xbin-frame-token", "mcp-session-id", "mcp-protocol-version", "last-event-id"} {
		if !strings.Contains(allowed, hd) {
			t.Errorf("preflight doesn't allow %s: %q", hd, allowed)
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/api/apps/crm/mcp", nil)
	req.Header.Set("Origin", "null")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !served || w.Header().Get("Access-Control-Allow-Origin") != "null" || w.Header().Get("Access-Control-Expose-Headers") != "Mcp-Session-Id" {
		t.Fatalf("answer: served=%v headers=%v", served, w.Header())
	}

	served = false
	req = httptest.NewRequest(http.MethodPost, "/api/apps/crm/mcp", nil)
	req.Header.Set("Origin", "https://elsewhere.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !served || w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Expose-Headers") != "" {
		t.Fatalf("another origin: served=%v headers=%v", served, w.Header())
	}
}
