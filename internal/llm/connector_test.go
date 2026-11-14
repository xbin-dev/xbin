package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// covers D226 — with nothing configured every route says so, and nothing
// reaches an upstream; switched off says that.
func TestNotConfigured(t *testing.T) {
	r := newRig(t, "")
	for _, c := range []struct{ method, path string }{
		{"POST", "/llm/v1/chat/completions"}, {"POST", "/llm/v1/responses"}, {"GET", "/llm/v1/models"}, {"GET", "/llm/preferred"},
	} {
		a := r.must(503, pOwner, c.method, c.path, chatBody("x", false))
		if code := a.errCode(t); code != "xbin_llm_not_configured" {
			t.Errorf("%s %s: code %q", c.method, c.path, code)
		}
	}
	st := r.must(200, pUser("bob"), "GET", "/llm", nil).json(t)
	if st["configured"] != false || st["callable"] != false || st["code"] != "xbin_llm_not_configured" || st["ref"] != "xbin:llm" || st["url"] != "/api/xbin/llm/v1" {
		t.Errorf("status: %v", st)
	}
	up := newUpstream(t)
	r.addUpstream("main", up, nil)
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"enabled": false})
	if code := r.must(503, pOwner, "POST", "/llm/v1/chat/completions", chatBody("main/gpt-5", false)).errCode(t); code != "xbin_llm_disabled" {
		t.Errorf("disabled: code %q", code)
	}
	if up.calls() != 0 {
		t.Errorf("a disabled connector reached the upstream %d times", up.calls())
	}
}

// covers D226 — who may call: a person by the people switch, a component
// by its binding (default-deny), an LLM key by itself; each call is
// attributed to the acting person and the component.
func TestAuthzMatrix(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.bound["apps/agent"] = true
	live, _, _ := r.keys.Mint(KeyGrant{User: "kim", Component: "apps/code", Label: "session s1"}, time.Hour)
	short, _, _ := r.keys.Mint(KeyGrant{User: "kim"}, time.Minute)

	type row struct {
		name      string
		p         auth.Principal
		key       string
		people    string
		want      int
		code      string
		user, cmp string
	}
	rows := []row{
		{"the owner token", pOwner, "", "admins", 200, "", "owner", ""},
		{"an admin", pAdmin, "", "admins", 200, "", "ada", ""},
		{"a person, switch admins", pUser("bob"), "", "admins", 403, "xbin_llm_people_off", "", ""},
		{"a person, switch everyone", pUser("bob"), "", "everyone", 200, "", "bob", ""},
		{"an admin, switch nobody", pAdmin, "", "nobody", 403, "xbin_llm_people_off", "", ""},
		{"a bound tile's frame, driven by a person", pFrame("apps/agent", "bob"), "", "nobody", 200, "", "bob", "apps/agent"},
		{"a bound tile's frame, driven by the owner", pFrame("apps/agent", ""), "", "admins", 200, "", "owner", "apps/agent"},
		{"a bound tile's backend", pInstance("apps/agent"), "", "admins", 200, "", "", "apps/agent"},
		{"a bound tile's backend in a person's partition", pPartition("apps/agent", "cy"), "", "admins", 200, "", "cy", "apps/agent"},
		{"an unbound tile's frame, driven by an admin", auth.Principal{Component: "apps/other", UserID: "ada", User: pAdmin.User, Via: "frame"}, "", "everyone", 403, "xbin_llm_not_bound", "", ""},
		{"an unbound tile's backend", pInstance("apps/other"), "", "everyone", 403, "xbin_llm_not_bound", "", ""},
		{"a cron delivery", auth.Principal{Component: "xbin/cron", Via: "cron", Role: "admin"}, "", "everyone", 403, "xbin_llm_not_bound", "", ""},
		{"an LLM key", auth.Principal{Via: "llm", UserID: "kim"}, live, "nobody", 200, "", "kim", "apps/code"},
		{"the llm marker without a key", auth.Principal{Via: "llm", UserID: "kim"}, "", "everyone", 401, "invalid_api_key", "", ""},
		{"an unknown LLM key", auth.Principal{Via: "llm"}, KeyPrefix + "nope", "everyone", 401, "invalid_api_key", "", ""},
		{"nobody at all", auth.Principal{}, "", "everyone", 403, "xbin_llm_forbidden", "", ""},
	}
	for _, c := range rows {
		r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": c.people})
		before := up.calls()
		req := r.req(c.p, "POST", "/llm/v1/chat/completions", chatBody("main/gpt-5", false))
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		rec := httptest.NewRecorder()
		r.mux.ServeHTTP(rec, req)
		a := answer{rec.Code, rec.Header(), rec.Body.Bytes()}
		if a.code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, a.code, c.want, a.body)
			continue
		}
		if c.want != 200 {
			if code := a.errCode(t); code != c.code {
				t.Errorf("%s: code %q, want %q", c.name, code, c.code)
			}
			if up.calls() != before {
				t.Errorf("%s: a refused call reached the upstream", c.name)
			}
			continue
		}
		// attributed: exactly one more request on (user, component)
		found := false
		for _, row := range r.usageRows("?component=" + c.cmp) {
			found = found || (row.User == c.user && row.Component == c.cmp && row.Requests > 0)
		}
		if !found {
			t.Errorf("%s: no usage row for user %q component %q: %+v", c.name, c.user, c.cmp, r.usageRows(""))
		}
	}
	// an expired key, and a revoked one
	r.clock.add(2 * time.Minute)
	req := r.req(auth.Principal{Via: "llm"}, "GET", "/llm/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+short)
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("an expired key: %d", rec.Code)
	}
	if n := r.keys.RevokeWhere(func(g KeyGrant) bool { return g.User == "kim" }); n != 1 {
		t.Errorf("revoked %d keys, want the one still live", n)
	}
	if _, ok := r.keys.Resolve(live); ok {
		t.Error("a revoked key still resolves")
	}
	// the gate the server calls: a marker principal only
	if p, ok := r.s.ResolveKey(live); ok || p.Via != "" {
		t.Errorf("ResolveKey of a revoked key: %+v, %v", p, ok)
	}
	tok, g, _ := r.keys.Mint(KeyGrant{User: OwnerUser, Sandbox: "sbx1"}, 0)
	if p, ok := r.s.ResolveKey(tok); !ok || p.Via != "llm" || p.UserID != "" || p.Owner || p.Component != "" || !strings.HasPrefix(tok, KeyPrefix) || g.Expires.Sub(r.clock.now()) != MaxKeyTTL {
		t.Errorf("ResolveKey: %+v, %v (grant %+v)", p, ok, g)
	}
	if _, _, err := r.keys.Mint(KeyGrant{}, time.Hour); err == nil {
		t.Error("a key without a person was minted")
	}
}

// covers D226 — a streamed call is relayed as it arrives (the client reads
// the first chunk while the upstream still holds the rest), the request
// reaches the upstream with the workspace's key and the upstream's model
// name, nothing of the caller's credentials, and the usage chunk is asked
// for and metered with its cost.
func TestStreamedCallIsUnbufferedAndMetered(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	up.hold = make(chan struct{})
	up.usage = map[string]any{"prompt_tokens": 1000, "completion_tokens": 200, "total_tokens": 1200,
		"prompt_tokens_details": map[string]any{"cached_tokens": 400}, "completion_tokens_details": map[string]any{"reasoning_tokens": 50}}
	r.addUpstream("main", up, map[string]any{"prices": map[string]any{"gpt-5": map[string]any{"in": 2.0, "out": 10.0, "cachedIn": 0.5}}})
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"aliases": map[string]string{"fast": "main/gpt-5"}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mux.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), pAdmin)))
	}))
	defer srv.Close()
	body, _ := json.Marshal(map[string]any{"model": "fast", "stream": true, "temperature": 0.5, "stream_options": map[string]any{"include_usage": false},
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	req, _ := http.NewRequest("POST", srv.URL+"/llm/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer caller-credential")
	req.Header.Set("Cookie", "xbin_session=caller-cookie")
	req.Header.Set("X-XBin-User", "mallory")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("answer: %d %v", resp.StatusCode, resp.Header)
	}
	br := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first: // the upstream is still holding the rest
		if !strings.Contains(line, `"first"`) {
			t.Fatalf("first line: %q", line)
		}
	case <-time.After(5 * time.Second):
		close(up.hold) // let the call end, so the failure is reported instead of hanging the cleanup
		t.Fatal("the first chunk did not arrive while the upstream held the stream: the proxy buffers")
	}
	close(up.hold)
	rest, _ := readAll(br)
	if !strings.Contains(rest, `" last"`) || !strings.Contains(rest, `"prompt_tokens":1000`) || !strings.HasSuffix(strings.TrimSpace(rest), "data: [DONE]") {
		t.Errorf("the rest of the stream: %q", rest)
	}

	sent := up.lastBody()
	if sent["model"] != "gpt-5" || sent["temperature"] != 0.5 || sent["stream_options"].(map[string]any)["include_usage"] != true {
		t.Errorf("the upstream got: %v", sent)
	}
	up.mu.Lock()
	h := up.headers[len(up.headers)-1]
	up.mu.Unlock()
	if h.Get("Authorization") != "Bearer "+upstreamKey || h.Get("Cookie") != "" || h.Get("X-Xbin-User") != "" {
		t.Errorf("the upstream's request headers: %v", h)
	}
	rows := r.usageRows("")
	if len(rows) != 1 {
		t.Fatalf("usage rows: %+v", rows)
	}
	want := 600*2.0/1e6 + 400*0.5/1e6 + 200*10.0/1e6
	if got := rows[0]; got.User != "ada" || got.Component != "" || got.Model != "main/gpt-5" || got.Requests != 1 || got.InputTokens != 1000 || got.OutputTokens != 200 ||
		got.CachedTokens != 400 || got.ReasoningTokens != 50 || got.Cost < want-1e-12 || got.Cost > want+1e-12 || got.Unpriced != 0 || got.Day != "2026-11-14" {
		t.Errorf("metered: %+v (cost want %v)", got, want)
	}
}

func readAll(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String(), err
		}
	}
}

// covers D226 — a client that goes away ends the upstream call.
func TestCancellationReachesTheUpstream(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	up.hold = make(chan struct{}) // never released
	r.addUpstream("main", up, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mux.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), pOwner)))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	body, _ := json.Marshal(chatBody("gpt-5", true))
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/llm/v1/responses", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, "first") {
			t.Fatalf("first line: %q", line)
		}
	case <-time.After(5 * time.Second):
		close(up.hold)
		t.Fatal("the first event did not arrive while the upstream held the stream")
	}
	cancel()
	resp.Body.Close()
	select {
	case <-up.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream's request was not canceled when the client went away")
	}
	// counted as a request that reported no usage
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := r.usageRows("")
		if len(rows) == 1 && rows[0].Requests == 1 && rows[0].Failed == 1 && rows[0].InputTokens == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the canceled call's row: %+v", rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// covers D226 — a quota stops at its limit with 429 naming the quota and
// its reset; a warn quota lets the call through, marked; the workspace's
// quota holds everyone; the next UTC day starts afresh.
func TestQuotaStopsAndResets(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t) // each call: 120 tokens
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"})
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"default": map[string]any{"tokensPerDay": 200}, "users": map[string]any{"vip": map[string]any{"tokensPerDay": -1}}})

	call := func(p auth.Principal) answer {
		return r.do(p, "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	}
	if a := call(pUser("bob")); a.code != 200 {
		t.Fatalf("first call: %d %s", a.code, a.body)
	}
	if a := call(pUser("bob")); a.code != 200 { // 120 < 200: a call in flight finishes past the limit
		t.Fatalf("second call: %d %s", a.code, a.body)
	}
	a := call(pUser("bob"))
	if a.code != 429 || a.errCode(t) != "xbin_llm_quota_exceeded" {
		t.Fatalf("at the limit: %d %s", a.code, a.body)
	}
	e := a.json(t)["error"].(map[string]any)
	msg := e["message"].(string)
	if e["type"] != "insufficient_quota" || e["quota"] != "default.tokensPerDay" || e["resets"] != "2026-11-15T00:00:00Z" || e["limit"] != 200.0 || e["used"] != 240.0 ||
		!strings.Contains(msg, "your daily LLM token quota of 200 tokens") || !strings.Contains(msg, "2026-11-15T00:00:00Z") || a.h.Get("Retry-After") != "43200" {
		t.Errorf("the refusal: %s (Retry-After %q)", a.body, a.h.Get("Retry-After"))
	}
	if up.calls() != 2 {
		t.Errorf("the upstream saw %d calls, want 2", up.calls())
	}
	// another person is not held by bob's use; a user override lifts the default
	if a := call(pUser("cy")); a.code != 200 {
		t.Errorf("another person: %d", a.code)
	}
	for i := 0; i < 3; i++ {
		if a := call(pUser("vip")); a.code != 200 {
			t.Errorf("an unlimited override, call %d: %d %s", i, a.code, a.body)
		}
	}
	// the person reads the same in their status
	st := r.must(200, pUser("bob"), "GET", "/llm", nil).json(t)
	q := st["quota"].(map[string]any)
	if q["state"] != "stop" || len(q["limits"].([]any)) != 1 {
		t.Errorf("status quota: %v", q)
	}
	// warn: through, with the header
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"users": map[string]any{"bob": map[string]any{"tokensPerDay": 100, "action": "warn"}}})
	a = call(pUser("bob"))
	if a.code != 200 || !strings.HasPrefix(a.h.Get(QuotaHeader), "warn; quota=user.tokensPerDay; resets=2026-11-15T00:00:00Z") {
		t.Errorf("a warn quota: %d, %q", a.code, a.h.Get(QuotaHeader))
	}
	// the workspace's quota holds everyone, the unlimited person and a component without a person included
	r.bound["apps/agent"] = true
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"workspace": map[string]any{"tokensPerMonth": 500}})
	for _, p := range []auth.Principal{pUser("vip"), pInstance("apps/agent"), pOwner} {
		a := call(p)
		if a.code != 429 || a.json(t)["error"].(map[string]any)["quota"] != "workspace.tokensPerMonth" || !strings.Contains(string(a.body), "2026-12-01T00:00:00Z") ||
			!strings.Contains(string(a.body), "the workspace's monthly LLM token quota") {
			t.Errorf("the workspace quota for %+v: %d %s", p, a.code, a.body)
		}
	}
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"workspace": nil, "users": map[string]any{"bob": nil}})
	if a := call(pUser("bob")); a.code != 429 {
		t.Errorf("bob, back on the default: %d", a.code)
	}
	// the next UTC day
	r.clock.set(time.Date(2026, 11, 15, 0, 0, 1, 0, time.UTC))
	if a := call(pUser("bob")); a.code != 200 {
		t.Errorf("after the reset: %d %s", a.code, a.body)
	}
}

// covers D226 — keys and the vault: an admin's key is sealed at rest, never
// answered, never logged; while the vault is sealed its upstream answers
// 503 naming the vault and a host's upstream keeps working.
func TestKeysAndTheSealedVault(t *testing.T) {
	up := newUpstream(t)
	hostDir := t.TempDir()
	host := writeHost(t, hostDir, `{"upstreams": {"hosted": {"baseURL": "`+up.URL+`", "keyFile": "upstream.key", "models": ["gpt-5"]}}}`, "sk-host-file-key-0001")
	r := newRig(t, host)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})

	raw, _ := os.ReadFile(filepath.Join(r.dir, "keys.json"))
	cfg, _ := os.ReadFile(filepath.Join(r.dir, "config.json"))
	r.noSecret("data/llm/keys.json (it is sealed)", raw)
	r.noSecret("data/llm/config.json", cfg)
	if !strings.Contains(string(raw), `"enc": 1`) || !strings.Contains(string(cfg), `"hasKey": true`) {
		t.Errorf("at rest: keys %s, config %s", raw, cfg)
	}
	view := r.must(200, pOwner, "GET", "/llm/config", nil)
	r.noSecret("GET /llm/config", view.body)
	if strings.Contains(string(view.body), "sk-host-file-key") {
		t.Error("the host's key appears in GET /llm/config")
	}
	v := view.json(t)
	ups := v["upstreams"].([]any)
	hosted, main := ups[0].(map[string]any), ups[1].(map[string]any)
	if k := main["key"].(map[string]any); k["set"] != true || k["source"] != "vault" || k["hint"] != "…cdef" || v["vault"] != "open" {
		t.Errorf("the admin key's view: %v (vault %v)", k, v["vault"])
	}
	if k := hosted["key"].(map[string]any); k["set"] != true || k["source"] != "host" || k["hint"] != nil || hosted["locked"] != true || hosted["reason"] != "set by your host" {
		t.Errorf("the host key's view: %v", hosted)
	}

	call := func(model string) answer {
		return r.do(pOwner, "POST", "/llm/v1/chat/completions", chatBody(model, false))
	}
	if a := call("main/gpt-5"); a.code != 200 {
		t.Fatalf("unsealed: %d %s", a.code, a.body)
	}
	r.vault.set(true, true) // sealed
	a := call("main/gpt-5")
	if a.code != 503 || a.errCode(t) != "xbin_vault_sealed" || !strings.Contains(string(a.body), "bx vault unseal") {
		t.Errorf("sealed, the admin's upstream: %d %s", a.code, a.body)
	}
	if a := call("hosted/gpt-5"); a.code != 200 {
		t.Errorf("sealed, the host's upstream: %d %s", a.code, a.body)
	}
	if up.auth[len(up.auth)-1] != "Bearer sk-host-file-key-0001" {
		t.Errorf("the host's upstream was called with %q", up.auth[len(up.auth)-1])
	}
	sv := r.must(200, pOwner, "GET", "/llm/config", nil).json(t)
	if k := sv["upstreams"].([]any)[1].(map[string]any)["key"].(map[string]any); sv["vault"] != "sealed" || k["set"] != true || k["hint"] != nil {
		t.Errorf("the sealed view: vault %v, key %v", sv["vault"], k)
	}
	// a key can't be set while sealed; settings without one can
	if a := r.do(pOwner, "PUT", "/llm/upstreams/other", map[string]any{"baseURL": up.URL, "key": "sk-another-key-000000"}); a.code != 409 || !strings.Contains(string(a.body), "sealed") {
		t.Errorf("setting a key while sealed: %d %s", a.code, a.body)
	}
	r.must(200, pOwner, "PUT", "/llm/upstreams/main", map[string]any{"private": true})
	r.vault.set(true, false)
	if a := call("main/gpt-5"); a.code != 200 {
		t.Errorf("unsealed again: %d %s", a.code, a.body)
	}
	// removing the key: the upstream is called without one
	r.must(200, pOwner, "PUT", "/llm/upstreams/main", map[string]any{"key": ""})
	call("main/gpt-5")
	if got := up.auth[len(up.auth)-1]; got != "" {
		t.Errorf("after removing the key the upstream got Authorization %q", got)
	}
	r.noSecret("the log", r.logs.Bytes())
	if strings.Contains(r.logs.String(), "sk-host-file-key") {
		t.Error("the host's key appears in the log")
	}
}

// covers D226 — the vault's other states: locked refuses to store a key,
// plaintext keeps it in the clear and seals it once a barrier exists.
func TestVaultLockedAndPlaintext(t *testing.T) {
	up := newUpstream(t)
	locked := newRigAt(t, t.TempDir(), "", &fakeVault{})
	if a := locked.do(pOwner, "PUT", "/llm/upstreams/main", map[string]any{"baseURL": up.URL, "key": upstreamKey}); a.code != 409 || !strings.Contains(string(a.body), "isn't set up") {
		t.Errorf("locked: %d %s", a.code, a.body)
	}
	if _, err := os.Stat(filepath.Join(locked.dir, "keys.json")); err == nil {
		t.Error("a key file was written with no vault set up")
	}
	locked.must(200, pOwner, "PUT", "/llm/upstreams/local", map[string]any{"baseURL": up.URL}) // an upstream without a key needs no vault

	dir, v := t.TempDir(), &fakeVault{}
	s, err := New(Options{Dir: dir, Vault: v, AllowPlaintext: true, IsAdmin: func(auth.Principal) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.keys.set("main", upstreamKey); err != nil || s.keys.state() != VaultPlaintext {
		t.Fatalf("plaintext mode: %v, %s", err, s.keys.state())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "keys.json"))
	if !strings.Contains(string(raw), upstreamKey) {
		t.Fatalf("the plaintext mode keeps the map as it is: %s", raw)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "keys.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("keys.json mode %v", fi.Mode().Perm())
	}
	v.set(true, false) // a barrier is set up: the next read seals the file
	s2, _ := New(Options{Dir: dir, Vault: v})
	defer s2.Close()
	if k, err := s2.keys.get("main"); err != nil || k != upstreamKey {
		t.Fatalf("after the barrier: %q, %v", k, err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "keys.json"))
	if strings.Contains(string(raw), upstreamKey) {
		t.Errorf("the key stayed in the clear after a barrier was set up: %s", raw)
	}
}

// covers D226 — what the proxy passes and what it keeps: an upstream's
// error passes with its status, its refusal of the key becomes the
// connector's own 502, the key is cut out of an echo, a redirect is not
// followed, an oversized body is refused before any upstream call.
func TestUpstreamErrorsAndBounds(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, nil)
	other := newUpstream(t)
	call := func() answer { return r.do(pOwner, "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false)) }
	fail := func(f func(w http.ResponseWriter, req *http.Request)) {
		up.mu.Lock()
		up.fail = func(w http.ResponseWriter, req *http.Request) bool { f(w, req); return true }
		up.mu.Unlock()
	}

	fail(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		writeJSON(w, 429, map[string]any{"error": map[string]any{"message": "slow down, key " + upstreamKey, "type": "rate_limit_error", "code": "rate_limit_exceeded"}})
	})
	a := call()
	if a.code != 429 || a.errCode(t) != "rate_limit_exceeded" || a.h.Get("Retry-After") != "7" || !strings.Contains(string(a.body), "slow down, key [key]") {
		t.Errorf("an upstream 429: %d %s %v", a.code, a.body, a.h)
	}
	r.noSecret("a relayed upstream error", a.body)

	for _, status := range []int{401, 403} {
		fail(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, status, map[string]any{"error": map[string]any{"message": "Incorrect API key provided: " + upstreamKey}})
		})
		a = call()
		if a.code != 502 || a.errCode(t) != "xbin_llm_upstream_auth" {
			t.Errorf("an upstream %d: %d %s", status, a.code, a.body)
		}
		r.noSecret("the answer to an upstream key refusal", a.body)
	}

	fail(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	})
	if a = call(); a.code != 502 || a.errCode(t) != "xbin_llm_upstream" || other.calls() != 0 {
		t.Errorf("a redirect: %d %s (the other host saw %d calls)", a.code, a.body, other.calls())
	}

	fail(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(500)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	})
	if a = call(); a.code != 500 || a.errCode(t) != "xbin_llm_upstream" || strings.Contains(string(a.body), "<html>") {
		t.Errorf("an upstream's HTML error: %d %s", a.code, a.body)
	}
	up.mu.Lock()
	up.fail = nil
	up.mu.Unlock()

	// an unreachable upstream: 502, and the address stays out of the answer
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	r.must(200, pOwner, "PUT", "/llm/upstreams/dead", map[string]any{"baseURL": deadURL, "key": upstreamKey})
	a = r.do(pOwner, "POST", "/llm/v1/chat/completions", chatBody("dead/gpt-5", false))
	if a.code != 502 || a.errCode(t) != "xbin_llm_upstream_unreachable" || strings.Contains(string(a.body), strings.TrimPrefix(deadURL, "http://")) {
		t.Errorf("an unreachable upstream: %d %s", a.code, a.body)
	}

	// bodies: not JSON, not an object, too large
	before := up.calls()
	if a := r.do(pOwner, "POST", "/llm/v1/chat/completions", "not json"); a.code != 400 || a.errCode(t) != "xbin_llm_body" {
		t.Errorf("a body that is not JSON: %d %s", a.code, a.body)
	}
	big := `{"model":"gpt-5","messages":[{"role":"user","content":"` + strings.Repeat("x", MaxRequestBody) + `"}]}`
	if a := r.do(pOwner, "POST", "/llm/v1/chat/completions", big); a.code != 413 || a.errCode(t) != "xbin_llm_body" {
		t.Errorf("an oversized body: %d %.200s", a.code, a.body)
	}
	if up.calls() != before {
		t.Error("a refused body reached the upstream")
	}

	// every failure is counted as one, and nothing above leaked into the log
	var failed int64
	for _, row := range r.usageRows("") {
		failed += row.Failed
	}
	if failed != 6 {
		t.Errorf("failed calls metered: %d, want 6", failed)
	}
	r.noSecret("the log", r.logs.Bytes())
	if strings.Contains(r.logs.String(), `"content"`) || strings.Contains(r.logs.String(), "xxxxxxxx") {
		t.Error("a request body appears in the log")
	}
}

// covers D226 — the upstream test an admin runs: a small call that says how
// it went, with the detail an admin needs.
func TestUpstreamTest(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, nil)
	res := r.must(200, pOwner, "POST", "/llm/upstreams/main/test", nil).json(t)
	if res["ok"] != true || res["models"] != 2.0 {
		t.Errorf("a passthrough upstream without a model: %v", res)
	}
	res = r.must(200, pOwner, "POST", "/llm/upstreams/main/test", map[string]any{"model": "gpt-5"}).json(t)
	if res["ok"] != true || res["model"] != "gpt-5" || res["tokens"] != 120.0 || res["status"] != 200.0 {
		t.Errorf("a test call: %v", res)
	}
	up.mu.Lock()
	up.fail = func(w http.ResponseWriter, _ *http.Request) bool {
		writeJSON(w, 404, map[string]any{"error": map[string]any{"message": "The model does not exist"}})
		return true
	}
	up.mu.Unlock()
	res = r.must(200, pOwner, "POST", "/llm/upstreams/main/test", map[string]any{"model": "nope"}).json(t)
	if res["ok"] != false || !strings.Contains(res["error"].(string), "The model does not exist") || res["status"] != 404.0 {
		t.Errorf("a failing test call: %v", res)
	}
	r.must(404, pOwner, "POST", "/llm/upstreams/none/test", nil)
	if a := r.do(pUser("bob"), "POST", "/llm/upstreams/main/test", nil); a.code != 403 {
		t.Errorf("a test by a non-admin: %d", a.code)
	}
}
