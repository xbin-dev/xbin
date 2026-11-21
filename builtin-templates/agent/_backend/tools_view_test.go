package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// GET /tools: what a conversation would be offered now — the tools by class
// with their source, and every MCP server with whether it answers — read
// by anyone who can read the tile, its person's workspace agent too, with
// no secret in it and nothing stored.
func TestToolsView(t *testing.T) {
	shorten(t, &mcpDiscoverTimeout, 2*time.Second)
	toolsProbeMu.Lock()
	toolsProbes = map[string]toolsProbe{}
	toolsProbeMu.Unlock()
	// what the gateway was asked: the probes of several servers run at once, so every count is under askedMu
	// (a bare map here was "concurrent map writes" under load)
	var askedMu sync.Mutex
	asked := map[string]int{}
	count := func(k string) int { askedMu.Lock(); defer askedMu.Unlock(); return asked[k] }
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		askedMu.Lock()
		asked[r.URL.Path+" "+req.Method]++
		askedMu.Unlock()
		if r.URL.Path != "/api/apps/dice/mcp" {
			http.Error(w, "the provider's backend is not running", http.StatusBadGateway)
			return
		}
		var result any = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}}
		if req.Method == "tools/list" {
			result = map[string]any{"tools": []map[string]any{{"name": "roll_dice", "description": "Roll dice",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"sides": map[string]any{"type": "integer"}}}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Setenv("XBIN_IFACE_MCP", `[{"provider":"apps/dice","url":"http://xbin/api/apps/dice"},{"provider":"apps/down","url":"http://xbin/api/apps/down"}]`)
	ag, mux := accessFixture(t)
	// a legacy static server whose address and headers carry credentials
	cfg := defaultConfig()
	cfg.MCP = []MCPServer{{Name: "ext", URL: "http://bob:hunter2@127.0.0.1:1/mcp?token=SECRET-QUERY", Headers: map[string]string{"Authorization": "Bearer SECRET-HEADER"}}}
	b, _ := json.Marshal(cfg)
	if err := ag.db.putSetting("config", string(b)); err != nil {
		t.Fatal(err)
	}

	type view struct {
		Default string
		Classes []struct {
			ID, Note string
			Tools    []toolView
			MCP      []string
		}
		MCP []mcpServerView
	}
	get := func(c caller, agentCall bool) (int, view, string) {
		t.Helper()
		if agentCall {
			c = caller{from: c.from, user: c.user, level: c.level}
		}
		w := callAsAgent(t, mux, c, agentCall, "/tools")
		var v view
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return w.Code, v, w.Body.String()
	}
	code, v, raw := get(asAlice, false)
	if code != 200 {
		t.Fatalf("GET /tools as alice: %d %s", code, raw)
	}
	for _, secret := range []string{"hunter2", "SECRET-QUERY", "SECRET-HEADER", "bob:"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the answer holds a credential (%s): %s", secret, raw)
		}
	}
	srv := map[string]mcpServerView{}
	for _, s := range v.MCP {
		srv[s.Name] = s
	}
	if d := srv["apps/dice"]; d.Via != "slot" || d.Slot != "mcp" || d.Tile != "apps/dice" || !d.Probed || !d.OK || d.Error != "" || len(d.Tools) != 1 || d.Tools[0] != "roll_dice" {
		t.Fatalf("the bound dice tile: %+v", d)
	}
	if d := srv["apps/down"]; d.Via != "slot" || !d.Probed || d.OK || !strings.Contains(d.Error, "502") || len(d.Tools) != 0 {
		t.Fatalf("a bound tile whose backend is down: %+v", d)
	}
	if e := srv["ext"]; e.Via != "config" || e.URL != "http://127.0.0.1:1/mcp" || e.OK || e.Error == "" {
		t.Fatalf("the static server: %+v", e)
	}
	classes := map[string]int{}
	for i, c := range v.Classes {
		classes[c.ID] = i
	}
	internal := v.Classes[classes[classInternal]]
	var roll *toolView
	builtins := map[string]bool{}
	for i, tl := range internal.Tools {
		if tl.Source == "builtin" {
			builtins[tl.Name] = true
		}
		if tl.Name == "roll_dice" {
			roll = &internal.Tools[i]
		}
	}
	if roll == nil || roll.Source != "mcp" || roll.Server != "apps/dice" || roll.Call != "mcp:apps/dice:roll_dice" {
		t.Fatalf("the internal class offers roll_dice from apps/dice: %+v", internal.Tools)
	}
	if !builtins["memory_set"] || !builtins["finish"] || !builtins["xbin_call"] || len(internal.MCP) != 3 {
		t.Fatalf("the internal class's built-ins and servers: %v %v", builtins, internal.MCP)
	}
	web := v.Classes[classes[classWeb]]
	for _, tl := range web.Tools {
		if tl.Source == "mcp" {
			t.Fatalf("the web class offers an MCP tool: %+v", tl)
		}
	}
	if web.Note == "" || v.Default == "" {
		t.Fatalf("the web class says why it has no MCP tools: %+v (default %q)", web, v.Default)
	}

	// a read: nothing was stored — a conversation's own discovery still runs
	if raw := ag.db.getSetting(mcpCacheKey(MCPServer{Name: "apps/dice", URL: "http://xbin/api/apps/dice/mcp"})); raw != "" {
		t.Fatalf("GET /tools stored a tool list: %s", raw)
	}
	// asked again at once: the probe's answer is reused, the servers aren't asked again
	before := count("/api/apps/dice/mcp tools/list")
	if code, _, _ := get(asBob, false); code != 200 || count("/api/apps/dice/mcp tools/list") != before {
		t.Fatalf("a second read within %v asked the server again (%d → %d)", toolsProbeFresh, before, count("/api/apps/dice/mcp tools/list"))
	}

	// alice's workspace agent reads it as she would (agentMay)
	if code, v2, raw := get(asAlice, true); code != 200 || len(v2.MCP) != 3 {
		t.Fatalf("alice's workspace agent: %d %s", code, raw)
	}
	// nobody without an identity
	if code, _, _ := get(asNobody, false); code != 403 {
		t.Fatalf("no identity: %d", code)
	}
}

// callAsAgent is callAs for a GET, as the person's workspace agent when
// byAgent (xbind's X-XBin-Agent beside the person's identity).
func callAsAgent(t *testing.T, mux *http.ServeMux, c caller, byAgent bool, target string) *httptest.ResponseRecorder {
	t.Helper()
	if !byAgent {
		return callAs(t, mux, c, "GET", target, nil)
	}
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("X-XBin-From", "user:"+c.user)
	r.Header.Set("X-XBin-Role", "reader")
	r.Header.Set("X-XBin-User", c.user)
	r.Header.Set("X-XBin-User-Level", c.level)
	r.Header.Set("X-XBin-Agent", "users/"+c.user+"/agent")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
