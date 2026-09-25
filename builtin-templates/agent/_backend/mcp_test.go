package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMCP is a streamable-HTTP MCP server with one tool. hang makes it accept
// requests and never answer — a provider that is cold, rebuilding or wedged.
type fakeMCP struct {
	*httptest.Server
	requests atomic.Int64
}

func newFakeMCP(t *testing.T, tool string, hang bool) *fakeMCP {
	t.Helper()
	f := &fakeMCP{}
	stop := make(chan struct{})
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if hang {
			select {
			case <-r.Context().Done():
			case <-stop:
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		var result any = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}}
		if req.Method == "tools/list" {
			result = map[string]any{"tools": []map[string]any{{"name": tool, "description": "a tool"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(func() { close(stop); f.Close() })
	return f
}

func toolNamesOf(specs []toolSpec) map[string]bool {
	out := map[string]bool{}
	for _, s := range specs {
		out[s.Function.Name] = true
	}
	return out
}

// Discovery used to walk the servers one after another with no deadline but
// the drive's own, so one wedged provider held every prompt hostage. Now each
// server gets mcpDiscoverTimeout, and they are asked at the same time.
func TestMCPDiscoveryIsConcurrentAndBounded(t *testing.T) {
	shorten(t, &mcpDiscoverTimeout, 300*time.Millisecond)
	ag := newTestAgent(t, newTestDB(t))
	slowA, slowB := newFakeMCP(t, "a", true), newFakeMCP(t, "b", true)
	fast := newFakeMCP(t, "search", false)
	cfg := Config{MCP: []MCPServer{{Name: "slowA", URL: slowA.URL}, {Name: "slowB", URL: slowB.URL}, {Name: "fast", URL: fast.URL}}}

	start := time.Now()
	var got map[string]bool
	within(t, "mcpTools", 3*time.Second, func() { got = toolNamesOf(ag.mcpTools(context.Background(), cfg)) })
	if took := time.Since(start); took > 550*time.Millisecond {
		t.Fatalf("discovery took %v — two wedged servers were waited for one after the other", took)
	}
	if !got["mcp:fast:search"] || len(got) != 1 {
		t.Fatalf("tools = %v, want only the answering server's", got)
	}
}

// A tool list almost never changes, so a prompt must not pay the handshake
// every time — nor again after the backend wakes from an idle reap.
func TestMCPToolsAreCachedAndPersisted(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	srv := newFakeMCP(t, "search", false)
	cfg := Config{MCP: []MCPServer{{Name: "s", URL: srv.URL}}}

	if got := toolNamesOf(ag.mcpTools(context.Background(), cfg)); !got["mcp:s:search"] {
		t.Fatalf("first discovery: %v", got)
	}
	first := srv.requests.Load()
	ag.mcpTools(context.Background(), cfg)
	if n := srv.requests.Load(); n != first {
		t.Fatalf("a fresh list was fetched again (%d → %d requests)", first, n)
	}
	// A new process (a save, a swap, an idle reap) on the same database.
	if got := toolNamesOf(newTestAgent(t, db).mcpTools(context.Background(), cfg)); !got["mcp:s:search"] || srv.requests.Load() != first {
		t.Fatalf("the persisted list was not used: tools %v, requests %d → %d", got, first, srv.requests.Load())
	}

	// Stale: served at once, refreshed behind the caller's back.
	key := mcpCacheKey(cfg.MCP[0])
	var c mcpToolList
	_ = json.Unmarshal([]byte(db.getSetting(key)), &c)
	c.At = time.Now().Add(-time.Hour).Unix()
	b, _ := json.Marshal(c)
	_ = db.putSetting(key, string(b))
	if got := toolNamesOf(ag.mcpTools(context.Background(), cfg)); !got["mcp:s:search"] {
		t.Fatalf("a stale list should still be served: %v", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for srv.requests.Load() == first && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.requests.Load() == first {
		t.Fatal("a stale list was never refreshed")
	}
}

// A refresh that fails keeps the last good list rather than dropping the
// server's tools from every run until it recovers.
func TestMCPFailedRefreshKeepsTheList(t *testing.T) {
	shorten(t, &mcpDiscoverTimeout, 200*time.Millisecond)
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	srv := newFakeMCP(t, "search", false)
	cfg := Config{MCP: []MCPServer{{Name: "s", URL: srv.URL}}}
	ag.mcpTools(context.Background(), cfg)
	srv.Close() // the provider goes away

	if _, err := ag.discoverMCPTools(context.Background(), cfg.MCP[0]); err == nil {
		t.Fatal("setup: discovery against a closed server should fail")
	}
	if got := toolNamesOf(ag.mcpTools(context.Background(), cfg)); !got["mcp:s:search"] {
		t.Fatalf("the last good list was lost: %v", got)
	}
}

// The web lane never gets MCP tools, so discovering them only woke servers.
func TestWebLaneDoesNotWakeMCPServers(t *testing.T) {
	ag := newTestAgent(t, newTestDB(t))
	srv := newFakeMCP(t, "search", false)
	if got := ag.mcpTools(context.Background(), Config{Toolset: "web", MCP: []MCPServer{{Name: "s", URL: srv.URL}}}); len(got) != 0 {
		t.Fatalf("web lane got MCP tools: %v", toolNamesOf(got))
	}
	if n := srv.requests.Load(); n != 0 {
		t.Fatalf("web lane made %d MCP request(s)", n)
	}
}

// A run shows as running the moment its turn starts. Tool discovery is
// network work, and doing it first left the run showing its previous status
// for as long as that took.
func TestRunShowsRunningDuringDiscovery(t *testing.T) {
	shorten(t, &mcpDiscoverTimeout, time.Second)
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	slow := newFakeMCP(t, "a", true)
	id := newRun(t, ag, Config{MCP: []MCPServer{{Name: "slow", URL: slow.URL}}}, "hi")
	deadline := time.Now().Add(700 * time.Millisecond)
	for slow.requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if slow.requests.Load() == 0 {
		t.Fatal("setup: discovery never started")
	}
	if r, _ := db.getRun(id); r.Status != statusRunning {
		t.Fatalf("while discovery was waiting, the run showed %q", r.Status)
	}
	waitStatus(t, db, id, statusIdle)
}
