package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// oauthMCP is a streamable-HTTP MCP server behind its own OAuth
// authorization server, as the MCP authorization spec has it: a 401 with
// resource_metadata, protected resource and authorization server metadata,
// dynamic registration, the code grant with PKCE S256 and the resource
// indicator, refresh with rotation. "static-1" is a token it also takes.
type oauthMCP struct {
	*httptest.Server
	mu         sync.Mutex
	challenge  string // the authorization request's, as the test hands it over
	redirect   string
	access     map[string]bool
	refresh    map[string]bool
	n          int
	registered int
	refreshed  int
	calls      []string // tools/call arguments' text
}

func newOAuthMCP(t *testing.T) *oauthMCP {
	t.Helper()
	f := &oauthMCP{access: map[string]bool{}, refresh: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		ok := f.access[tok] || tok == "static-1"
		f.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp", scope="mcp:read"`, f.URL))
			w.WriteHeader(401)
			return
		}
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "Fake Travel"}}
		switch req.Method {
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "echo", "description": "says it back"}}}
		case "tools/call":
			var p struct {
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.mu.Lock()
			f.calls = append(f.calls, fmt.Sprint(p.Arguments["text"]))
			f.mu.Unlock()
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "echo: " + fmt.Sprint(p.Arguments["text"])}}}
		}
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": f.URL + "/mcp", "authorization_servers": []string{f.URL},
			"scopes_supported": []string{"mcp:read", "mcp:write"}, "resource_name": "Fake Travel MCP"})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.URL, "authorization_endpoint": f.URL + "/oauth/authorize",
			"token_endpoint": f.URL + "/oauth/token", "registration_endpoint": f.URL + "/oauth/register",
			"code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true})
	})
	mux.HandleFunc("POST /oauth/register", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			RedirectURIs []string `json:"redirect_uris"`
			Auth         string   `json:"token_endpoint_auth_method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.registered++
		f.redirect = b.RedirectURIs[0]
		f.mu.Unlock()
		if b.Auth != "none" {
			http.Error(w, `{"error":"invalid_client_metadata"}`, 400)
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "cid-1"})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		bad := func(code string) { w.WriteHeader(400); _ = json.NewEncoder(w).Encode(map[string]any{"error": code}) }
		if r.Form.Get("client_id") != "cid-1" || r.Form.Get("resource") != f.URL+"/mcp" {
			bad("invalid_client")
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "code-1" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || r.Form.Get("redirect_uri") != f.redirect {
				bad("invalid_grant")
				return
			}
		case "refresh_token":
			if !f.refresh[r.Form.Get("refresh_token")] {
				bad("invalid_grant")
				return
			}
			delete(f.refresh, r.Form.Get("refresh_token")) // rotated
			f.refreshed++
		default:
			bad("unsupported_grant_type")
			return
		}
		f.n++
		acc, ref := fmt.Sprintf("acc-%d", f.n), fmt.Sprintf("ref-%d", f.n)
		f.access[acc], f.refresh[ref] = true, true
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": acc, "refresh_token": ref, "expires_in": 3600, "token_type": "Bearer"})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// revokeAll: every token it handed out stops working (a revoked grant).
func (f *oauthMCP) revokeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access, f.refresh = map[string]bool{}, map[string]bool{}
}

func decodeServer(t *testing.T, w *httptest.ResponseRecorder) directView {
	t.Helper()
	var out struct {
		Server directView `json:"server"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v", w.Body, err)
	}
	return out.Server
}

const testRedirect = "https://xbin.test/c/apps/agent/mcp-callback.html"

// signIn runs the browser's part of a sign-in as c: the authorization URL,
// the server's consent (the fake takes the challenge), the callback.
func signIn(t *testing.T, mux *http.ServeMux, f *oauthMCP, c caller, id string) directView {
	v, _ := signInState(t, mux, f, c, id)
	return v
}

func signInState(t *testing.T, mux *http.ServeMux, f *oauthMCP, c caller, id string) (directView, string) {
	t.Helper()
	w := callAs(t, mux, c, "POST", "/mcp/servers/"+id+"/signin", map[string]any{"redirectUri": testRedirect})
	if w.Code != 200 {
		t.Fatalf("signin: %d %s", w.Code, w.Body)
	}
	var s struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	au, _ := url.Parse(s.URL)
	q := au.Query()
	if au.Path != "/oauth/authorize" || q.Get("client_id") != "cid-1" || q.Get("redirect_uri") != testRedirect || q.Get("code_challenge_method") != "S256" ||
		q.Get("resource") != f.URL+"/mcp" || q.Get("scope") != "mcp:read mcp:write" || q.Get("state") == "" || q.Get("response_type") != "code" {
		t.Fatalf("the authorization URL: %s", s.URL)
	}
	f.mu.Lock()
	f.challenge = q.Get("code_challenge")
	f.mu.Unlock()
	w = callAs(t, mux, c, "POST", "/mcp/oauth/callback", map[string]any{"state": q.Get("state"), "code": "code-1", "iss": f.URL})
	if w.Code != 200 {
		t.Fatalf("callback: %d %s", w.Code, w.Body)
	}
	return decodeServer(t, w), q.Get("state")
}

func TestDirectMCPOAuth(t *testing.T) {
	allowLocalMCP(t)
	ag, mux := accessFixture(t)
	vault := memVault(t)
	f := newOAuthMCP(t)

	// a plain-http address off this machine, and one with credentials, are refused
	for _, u := range []string{"http://example.com/mcp", "https://u:p@example.com/mcp", "ftp://x/mcp"} {
		if w := callAs(t, mux, asAlice, "POST", "/mcp/servers", map[string]any{"url": u}); w.Code != 400 {
			t.Fatalf("%s: %d %s", u, w.Code, w.Body)
		}
	}
	w := callAs(t, mux, asAlice, "POST", "/mcp/servers", map[string]any{"url": f.URL + "/mcp", "name": "travel", "scope": "personal"})
	if w.Code != 201 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	s := decodeServer(t, w)
	if s.Name != "my-travel" || s.Auth != mcpAuthOAuth || s.State != mcpSignin || s.Owner != "alice" || s.Title != "Fake Travel MCP" || s.Issuer != f.URL || !s.CanEdit {
		t.Fatalf("the server: %+v", s)
	}
	// alice's own: bob neither sees nor changes it
	w = callAs(t, mux, asBob, "GET", "/mcp/servers", nil)
	if strings.Contains(w.Body.String(), s.ID) {
		t.Fatalf("bob sees alice's server: %s", w.Body)
	}
	if w := callAs(t, mux, asBob, "POST", "/mcp/servers/"+s.ID+"/signin", map[string]any{"redirectUri": testRedirect}); w.Code != 404 {
		t.Fatalf("bob's signin: %d", w.Code)
	}
	// not signed in: offered nowhere
	aliceCtx := withMCPOwner(context.Background(), "alice")
	if got := toolNamesOf(ag.mcpTools(aliceCtx, Config{})); len(got) != 0 {
		t.Fatalf("tools before the sign-in: %v", got)
	}

	// a callback with someone else's session is refused; alice's goes through
	w = callAs(t, mux, asAlice, "POST", "/mcp/servers/"+s.ID+"/signin", map[string]any{"redirectUri": "https://evil.test/cb"})
	if w.Code != 400 {
		t.Fatalf("a foreign redirect: %d %s", w.Code, w.Body)
	}
	w = callAs(t, mux, asAlice, "POST", "/mcp/servers/"+s.ID+"/signin", map[string]any{"redirectUri": testRedirect})
	var st struct{ URL string }
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	au, _ := url.Parse(st.URL)
	if w := callAs(t, mux, asBob, "POST", "/mcp/oauth/callback", map[string]any{"state": au.Query().Get("state"), "code": "code-1"}); w.Code != 403 {
		t.Fatalf("bob's callback: %d %s", w.Code, w.Body)
	}
	s, used := signInState(t, mux, f, asAlice, s.ID)
	if s.State != mcpReady || !s.SignedIn || len(s.Tools) != 1 || s.Tools[0] != "echo" {
		t.Fatalf("after the sign-in: %+v", s)
	}
	if f.registered != 1 {
		t.Fatalf("registered %d times (once for one redirect)", f.registered)
	}
	if raw := vault.m[directVaultKey(s.ID)]; !strings.Contains(raw, "acc-") || strings.Contains(fmt.Sprint(ag.db.getSetting(mcpCacheKey(MCPServer{Name: "my-travel", URL: "direct:" + s.ID}))), "acc-") {
		t.Fatalf("the tokens' place: vault %q", raw)
	}
	var meta string
	_ = ag.db.q.QueryRow(`SELECT meta || client_id || redirect FROM mcp_servers WHERE id=?`, s.ID).Scan(&meta)
	if strings.Contains(meta, "acc-") || strings.Contains(meta, "ref-") {
		t.Fatalf("a token in the row: %s", meta)
	}
	// the same state twice is refused
	if w := callAs(t, mux, asAlice, "POST", "/mcp/oauth/callback", map[string]any{"state": used, "code": "code-1"}); w.Code != 400 {
		t.Fatalf("a used state: %d %s", w.Code, w.Body)
	}

	// its tools: in alice's conversations, never bob's
	if got := toolNamesOf(ag.mcpTools(aliceCtx, Config{})); !got["mcp:my-travel:echo"] {
		t.Fatalf("alice's tools: %v", got)
	}
	if got := toolNamesOf(ag.mcpTools(withMCPOwner(context.Background(), "bob"), Config{})); got["mcp:my-travel:echo"] {
		t.Fatalf("bob's conversation got alice's server: %v", got)
	}
	out, err := ag.mcpCall(aliceCtx, Config{}, "mcp:my-travel:echo", map[string]any{"text": "hi"})
	if err != nil || out != "echo: hi" {
		t.Fatalf("a call: %q %v", out, err)
	}
	if _, err := ag.mcpCall(withMCPOwner(context.Background(), "bob"), Config{}, "mcp:my-travel:echo", map[string]any{"text": "x"}); err == nil {
		t.Fatal("bob's conversation called alice's server")
	}

	// an access token near its end is refreshed first (the refresh token rotates)
	tk := directTokens(s.ID)
	tk.ExpiresMs = nowMs() + 1000
	_ = putDirectTokens(s.ID, tk)
	if out, err := ag.mcpCall(aliceCtx, Config{}, "mcp:my-travel:echo", map[string]any{"text": "again"}); err != nil || out != "echo: again" || f.refreshed != 1 {
		t.Fatalf("after a refresh: %q %v (refreshed %d)", out, err, f.refreshed)
	}
	// an access token the server stopped taking is refreshed on its 401
	f.mu.Lock()
	delete(f.access, directTokens(s.ID).Access)
	f.mu.Unlock()
	if out, err := ag.mcpCall(aliceCtx, Config{}, "mcp:my-travel:echo", map[string]any{"text": "401"}); err != nil || out != "echo: 401" || f.refreshed != 2 {
		t.Fatalf("after a 401: %q %v (refreshed %d)", out, err, f.refreshed)
	}
	// a revoked grant: the call says sign in again, and the server waits for it
	f.revokeAll()
	if _, err := ag.mcpCall(aliceCtx, Config{}, "mcp:my-travel:echo", map[string]any{"text": "x"}); err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("a revoked grant: %v", err)
	}
	if row, _ := ag.db.directServer(s.ID); row.State != mcpSignin {
		t.Fatalf("the state after a revoke: %s", row.State)
	}
	s = signIn(t, mux, f, asAlice, s.ID)
	if s.State != mcpReady || f.registered != 1 {
		t.Fatalf("signed in again: %+v (registered %d)", s, f.registered)
	}
	// sign out, remove: the vault entry goes
	w = callAs(t, mux, asAlice, "POST", "/mcp/servers/"+s.ID+"/signout", nil)
	if w.Code != 200 || decodeServer(t, w).State != mcpSignin || directTokens(s.ID).Access != "" {
		t.Fatalf("sign out: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "DELETE", "/mcp/servers/"+s.ID, nil); w.Code != 204 {
		t.Fatalf("remove: %d", w.Code)
	}
	if _, ok := vault.m[directVaultKey(s.ID)]; ok {
		t.Fatal("the vault entry outlived its server")
	}
}

func TestDirectMCPGlobalAndToken(t *testing.T) {
	allowLocalMCP(t)
	ag, mux := accessFixture(t)
	memVault(t)
	open := newFakeMCP(t, "search", false)
	f := newOAuthMCP(t)

	// global: a manager's to add
	if w := callAs(t, mux, asAlice, "POST", "/mcp/servers", map[string]any{"url": open.URL, "name": "docs", "scope": "global"}); w.Code != 403 {
		t.Fatalf("alice adds a global server: %d %s", w.Code, w.Body)
	}
	w := callAs(t, mux, asMgr, "POST", "/mcp/servers", map[string]any{"url": open.URL, "name": "docs", "scope": "global"})
	if w.Code != 201 {
		t.Fatalf("a global server: %d %s", w.Code, w.Body)
	}
	g := decodeServer(t, w)
	if g.Name != "docs" || g.Auth != mcpAuthNone || g.State != mcpReady || len(g.Tools) != 1 {
		t.Fatalf("the global server: %+v", g)
	}
	if w := callAs(t, mux, asMgr, "POST", "/mcp/servers", map[string]any{"url": open.URL, "name": "docs", "scope": "global"}); w.Code != 409 {
		t.Fatalf("a second docs: %d", w.Code)
	}
	if w := callAs(t, mux, asMgr, "POST", "/mcp/servers", map[string]any{"url": open.URL, "name": "my-docs", "scope": "global"}); w.Code != 400 {
		t.Fatalf("a global my-: %d", w.Code)
	}
	// everyone's conversations get it; only a manager removes it
	for _, owner := range []string{"alice", "bob", ""} {
		if got := toolNamesOf(ag.mcpTools(withMCPOwner(context.Background(), owner), Config{})); !got["mcp:docs:search"] {
			t.Fatalf("%q's tools: %v", owner, got)
		}
	}
	w = callAs(t, mux, asAlice, "GET", "/mcp/servers", nil)
	var list struct {
		Servers   []directView `json:"servers"`
		CanGlobal bool         `json:"canGlobal"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Servers) != 1 || list.Servers[0].CanEdit || list.CanGlobal {
		t.Fatalf("alice's list: %s", w.Body)
	}
	if w := callAs(t, mux, asAlice, "DELETE", "/mcp/servers/"+g.ID, nil); w.Code != 403 {
		t.Fatalf("alice removes it: %d", w.Code)
	}

	// a static token: refused when wrong, kept in the vault when right
	if w := callAs(t, mux, asBob, "POST", "/mcp/servers", map[string]any{"url": f.URL + "/mcp", "name": "tok", "token": "nope"}); w.Code != 400 {
		t.Fatalf("a wrong token: %d %s", w.Code, w.Body)
	}
	w = callAs(t, mux, asBob, "POST", "/mcp/servers", map[string]any{"url": f.URL + "/mcp", "name": "tok", "token": "static-1"})
	if w.Code != 201 {
		t.Fatalf("a token: %d %s", w.Code, w.Body)
	}
	b := decodeServer(t, w)
	if b.Auth != mcpAuthBearer || b.State != mcpReady || !b.SignedIn || len(b.Tools) != 1 || strings.Contains(w.Body.String(), "static-1") {
		t.Fatalf("the token's server: %s", w.Body)
	}
	if out, err := ag.mcpCall(withMCPOwner(context.Background(), "bob"), Config{}, "mcp:my-tok:echo", map[string]any{"text": "t"}); err != nil || out != "echo: t" {
		t.Fatalf("a call with the token: %q %v", out, err)
	}
}

// A person's partition reaches a global server through the global instance
// (its tokens stay there); their own servers are theirs, in their
// conversations only.
func TestDirectMCPPartitions(t *testing.T) {
	allowLocalMCP(t)
	ag, mux := accessFixture(t)
	memVault(t)
	open := newFakeMCP(t, "search", false)

	// at the global instance: a global server, used over its rpc route
	setMode(t, modeGlobal, "")
	gMux := http.NewServeMux() // built at global: a person's partition's calls pass agentRole there
	routes(gMux)
	if w := callAs(t, mux, asMgr, "POST", "/mcp/servers", map[string]any{"url": open.URL, "name": "docs", "scope": "personal"}); w.Code != 409 {
		t.Fatalf("a personal server at global: %d %s", w.Code, w.Body)
	}
	w := callAs(t, mux, asMgr, "POST", "/mcp/servers", map[string]any{"url": open.URL, "name": "docs", "scope": "global"})
	if w.Code != 201 {
		t.Fatalf("add at global: %d %s", w.Code, w.Body)
	}
	g := decodeServer(t, w)
	// her page can't call it (only her partition's backend, which offers it
	// where her class lists it), nor can a manager's
	for _, c := range []caller{asAlice, asMgr} {
		if w := callAs(t, mux, c, "POST", "/mcp/servers/"+g.ID+"/rpc", map[string]any{"method": "tools/list"}); w.Code != 404 {
			t.Fatalf("rpc at global from a page: %d %s", w.Code, w.Body)
		}
	}
	w = serveAs(gMux, "POST", "/mcp/servers/"+g.ID+"/rpc", `{"method":"tools/list"}`, asPartition("alice"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "search") {
		t.Fatalf("rpc at global: %d %s", w.Code, w.Body)
	}
	if w := serveAs(gMux, "POST", "/mcp/servers/"+g.ID+"/rpc", `{"method":"resources/read"}`, asPartition("alice")); w.Code != 400 {
		t.Fatalf("another method: %d", w.Code)
	}
	globalList := callAs(t, mux, asAlice, "GET", "/mcp/servers", nil).Body.Bytes()

	// in alice's partition: the global one through callGlobal, her own here
	setMode(t, modeUser, "alice")
	var paths []string
	old := callGlobal
	callGlobal = func(_ context.Context, method, path string, body []byte, _ string) (gwResp, error) {
		paths = append(paths, method+" "+path)
		setMode(t, modeGlobal, "")
		defer setMode(t, modeUser, "alice")
		if method == http.MethodGet {
			return gwResp{Status: 200, Body: globalList}, nil
		}
		w := serveAs(gMux, method, path, string(body), asPartition("alice"))
		b, _ := io.ReadAll(w.Body)
		return gwResp{Status: w.Code, Body: b}, nil
	}
	resetGlobalDirect := func() {
		globalDirectMu.Lock()
		globalDirectAt, globalDirectList = time.Time{}, nil
		globalDirectMu.Unlock()
	}
	resetGlobalDirect()
	t.Cleanup(func() { callGlobal = old; resetGlobalDirect() }) // the cache is the process's: no later test in a person's mode may see these
	if w := callAs(t, mux, asAlice, "POST", "/mcp/servers", map[string]any{"url": open.URL, "name": "docs", "scope": "global"}); w.Code != 409 {
		t.Fatalf("a global server from a partition: %d %s", w.Code, w.Body)
	}
	w = callAs(t, mux, asAlice, "POST", "/mcp/servers", map[string]any{"url": open.URL, "name": "mine"})
	if w.Code != 201 {
		t.Fatalf("alice's own: %d %s", w.Code, w.Body)
	}
	if got := toolNamesOf(ag.mcpTools(withPersonal(context.Background(), true), Config{})); !got["mcp:docs:search"] || !got["mcp:my-mine:search"] {
		t.Fatalf("alice's conversation: %v", got)
	}
	if got := toolNamesOf(ag.mcpTools(context.Background(), Config{})); got["mcp:my-mine:search"] || !got["mcp:docs:search"] {
		t.Fatalf("a conversation not hers: %v", got)
	}
	n := len(paths)
	if _, err := ag.mcpCall(context.Background(), Config{}, "mcp:docs:search", map[string]any{}); err != nil {
		t.Fatalf("a global call from the partition: %v", err)
	}
	if len(paths) == n || !strings.HasPrefix(paths[len(paths)-1], "POST /mcp/servers/"+g.ID+"/rpc") {
		t.Fatalf("the call didn't go through global: %v", paths)
	}
}

func TestWWWAuthParams(t *testing.T) {
	got := wwwAuthParams(`Bearer resource_metadata="https://a.example/.well-known/x", scope="mcp:read mcp:write", error=invalid_token`)
	if got["resource_metadata"] != "https://a.example/.well-known/x" || got["scope"] != "mcp:read mcp:write" || got["error"] != "invalid_token" {
		t.Fatalf("%v", got)
	}
	if len(wwwAuthParams(`Basic realm="x"`)) != 0 {
		t.Fatal("a Basic challenge")
	}
	if s := slugFromURL("https://api.ottotheagent.com/mcp"); s != "ottotheagent" {
		t.Fatalf("slug %q", s)
	}
}

// allowLocalMCP lets t's personal servers be httptest's (http, loopback).
func allowLocalMCP(t *testing.T) {
	directLocalOK = true
	t.Cleanup(func() { directLocalOK = false })
}

// A person's own server reaches public addresses only, over https — not the
// LAN, a cloud's metadata endpoint or the backend's loopback, whatever its
// URL, its metadata or a redirect says; a global one (a manager's) keeps
// what the agent's network reaches (mcp_reach.go).
func TestDirectMCPReach(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700:4700::1111": true, "1.1.1.1": true,
		"10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "127.0.0.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "0.1.2.3": false, "198.18.0.1": false, "240.0.0.1": false, "255.255.255.255": false,
		"::1": false, "fd00::1": false, "fe80::1": false, "::ffff:10.0.0.1": false, "64:ff9b::a00:1": false, "224.0.0.1": false,
	} {
		if got := publicAddr(netip.MustParseAddr(ip)); got != want {
			t.Errorf("publicAddr(%s) = %v", ip, got)
		}
	}
	if _, err := checkDirectURL("http://127.0.0.1:8080/mcp", mcpPersonal); err == nil {
		t.Fatal("a personal server over http on loopback")
	}
	if _, err := checkDirectURL("http://127.0.0.1:8080/mcp", mcpGlobal); err != nil {
		t.Fatalf("a global one on this machine: %v", err)
	}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	t.Cleanup(tls.Close)
	for _, u := range []string{tls.URL, strings.Replace(tls.URL, "https://", "http://", 1)} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
		resp, err := directHTTP.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("a personal server's request reached %s", u)
		}
		if !strings.Contains(err.Error(), "not a public address") && !strings.Contains(err.Error(), "https only") {
			t.Fatalf("%s: %v", u, err)
		}
	}
	// the open (global) transport dials it: the TLS handshake is what fails here (httptest's own CA)
	req, _ := http.NewRequestWithContext(withDirectReach(context.Background(), true), http.MethodGet, tls.URL, nil)
	if _, err := directHTTP.Do(req); err == nil || strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("a global server's request: %v", err)
	}
	for _, c := range []struct {
		res, server string
		ok          bool
	}{
		{"https://a.example/mcp", "https://a.example/mcp", true},
		{"https://a.example", "https://a.example/mcp", true},
		{"https://a.example/mcp/", "https://a.example/mcp/v1", true},
		{"https://a.example/other", "https://a.example/mcp", false},
		{"https://a.example/mc", "https://a.example/mcp", false},
		{"https://b.example/mcp", "https://a.example/mcp", false},
		{"http://a.example/mcp", "https://a.example/mcp", false},
	} {
		if got := resourceCovers(c.res, c.server); got != c.ok {
			t.Errorf("resourceCovers(%s, %s) = %v", c.res, c.server, got)
		}
	}
}
