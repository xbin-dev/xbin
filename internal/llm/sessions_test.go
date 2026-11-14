package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// covers D226 — the switches for coding tools: their defaults, an admin's
// changes, the host's file locking each one it sets.
func TestSessionsSwitches(t *testing.T) {
	r := newRig(t, "")
	sessions := func(p auth.Principal, path string) map[string]any {
		t.Helper()
		s, _ := r.must(200, p, "GET", path, nil).json(t)["sessions"].(map[string]any)
		return s
	}
	want := func(got map[string]any, harnesses string, terminals, openai bool) {
		t.Helper()
		if got["harnesses"] != harnesses || got["terminals"] != terminals || got["openaiEnv"] != openai {
			t.Fatalf("sessions %v, want %s %v %v", got, harnesses, terminals, openai)
		}
	}
	want(sessions(pOwner, "/llm/config"), HarnessOwnFirst, true, false)
	want(sessions(pUser("bob"), "/llm"), HarnessOwnFirst, true, false) // anyone reads what is in force
	r.must(200, pAdmin, "PUT", "/llm/config", map[string]any{"sessions": map[string]any{"harnesses": "connector", "openaiEnv": true}})
	want(sessions(pOwner, "/llm/config"), HarnessConnector, true, true)
	r.must(200, pAdmin, "PUT", "/llm/config", map[string]any{"sessions": map[string]any{"terminals": false}})
	want(sessions(pOwner, "/llm/config"), HarnessConnector, false, true) // only what is named changes
	for _, bad := range []any{map[string]any{"harnesses": "sometimes"}, map[string]any{"harnesses": ""}} {
		if a := r.do(pAdmin, "PUT", "/llm/config", map[string]any{"sessions": bad}); a.code != 400 {
			t.Errorf("sessions %v: %d %s", bad, a.code, a.body)
		}
	}
	if a := r.do(pUser("bob"), "PUT", "/llm/config", map[string]any{"sessions": map[string]any{"harnesses": "off"}}); a.code != 403 {
		t.Errorf("a non-admin changing the switches: %d", a.code)
	}

	// the host's file: what it sets is in force and locked, the rest stays the admin's
	host := writeHost(t, t.TempDir(), `{"sessions": {"harnesses": "off", "openaiEnv": false}}`, "")
	h := newRigAt(t, r.dir, host, r.vault)
	v := h.must(200, pOwner, "GET", "/llm/config", nil).json(t)
	want(v["sessions"].(map[string]any), HarnessOff, false, false) // terminals: the admin's, kept
	locks, _ := v["locks"].(map[string]any)
	if locks["sessions.harnesses"] == nil || locks["sessions.openaiEnv"] == nil || locks["sessions.terminals"] != nil {
		t.Fatalf("locks: %v", locks)
	}
	a := h.do(pAdmin, "PUT", "/llm/config", map[string]any{"sessions": map[string]any{"harnesses": "own-first", "terminals": true}})
	if a.code != 409 || !strings.Contains(string(a.body), "sessions.harnesses") {
		t.Fatalf("changing a locked switch: %d %s", a.code, a.body)
	}
	h.must(200, pAdmin, "PUT", "/llm/config", map[string]any{"sessions": map[string]any{"harnesses": "off", "terminals": true}}) // a locked value sent back unchanged passes
	want(h.must(200, pOwner, "GET", "/llm/config", nil).json(t)["sessions"].(map[string]any), HarnessOff, true, false)
}

// covers D226 — what a new session is offered: nothing while the connector
// serves no call; the harness model (the role, else the default, else the
// first listed); the models with the context sizes an upstream reports; a
// listing that asks no upstream when it needn't.
func TestSessionOffer(t *testing.T) {
	r := newRig(t, "")
	ctx := context.Background()
	if _, ok := r.s.Offer(ctx, "bob", "apps/x", true); ok {
		t.Fatal("an offer with nothing configured")
	}
	up := newUpstream(t)
	r.addUpstream("main", up, nil)
	o, ok := r.s.Offer(ctx, "bob", "apps/x", true)
	if !ok || o.Harnesses != HarnessOwnFirst || !o.Terminals || o.OpenAIEnv {
		t.Fatalf("offer: %+v %v", o, ok)
	}
	// no default anywhere: the first listed model, with its context size
	if o.Model != "main/gpt-5" || len(o.Models) != 2 || o.Models[0] != (OfferModel{ID: "main/gpt-5", Context: 400000}) || o.Models[1].ID != "main/gpt-5-mini" {
		t.Fatalf("models: %+v", o)
	}
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"defaults": map[string]any{"model": "main/gpt-5-mini"}, "aliases": map[string]string{"fast": "main/gpt-5-mini"}})
	if o, _ = r.s.Offer(ctx, "bob", "apps/x", true); o.Model != "main/gpt-5-mini" || len(o.Models) != 3 || o.Models[2].ID != "fast" {
		t.Fatalf("with a default and an alias (xbin/… left out): %+v", o)
	}
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"defaults": map[string]any{"roles": map[string]string{RoleHarness: "main/gpt-5"}}})
	up.mu.Lock()
	before := up.modelsN
	up.mu.Unlock()
	r.clock.add(2 * time.Minute) // the model cache is stale: a listing would ask the upstream again
	if o, _ = r.s.Offer(ctx, "bob", "apps/x", false); o.Model != "main/gpt-5" || o.Models != nil {
		t.Fatalf("the harness role, no models asked for: %+v", o)
	}
	up.mu.Lock()
	asked := up.modelsN - before
	up.mu.Unlock()
	if asked != 0 {
		t.Errorf("a launcher's offer asked the upstream for its models %d time(s)", asked)
	}
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"enabled": false})
	if _, ok := r.s.Offer(ctx, "bob", "apps/x", true); ok {
		t.Fatal("an offer while the connector is switched off")
	}
}

// covers D226 — POST /llm/keys: a bound component gets a narrow key for the
// person its credential acts for, never for another; people, LLM keys and
// unbound tiles get none; the key calls the connector and is metered to
// that person and the tile; DELETE ends it, and only its own component's.
func TestComponentKeys(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.bound["apps/agent"], r.bound["apps/other"] = true, true
	mint := func(p auth.Principal, body any) answer { return r.do(p, "POST", "/llm/keys", body) }

	for what, a := range map[string]answer{
		"a person":                         mint(pAdmin, nil),
		"the owner":                        mint(pOwner, nil),
		"an unbound tile":                  mint(pPartition("apps/unbound", "kim"), nil),
		"another person":                   mint(pPartition("apps/agent", "kim"), map[string]any{"user": "bob"}),
		"nobody's backend naming a person": mint(pInstance("apps/agent"), map[string]any{"user": "kim"}),
		"an LLM key":                       mint(auth.Principal{Via: "llm", UserID: "kim"}, nil),
	} {
		if a.code != 403 {
			t.Errorf("%s asking for a key: %d %s", what, a.code, a.body)
		}
	}
	if a := mint(pPartition("apps/agent", "kim"), map[string]any{"label": strings.Repeat("x", 201)}); a.code != 400 {
		t.Errorf("an overlong label: %d", a.code)
	}

	a := mint(pPartition("apps/agent", "kim"), map[string]any{"user": "kim", "ttlSec": 600, "label": "run 7", "sandbox": "sb-1"})
	if a.code != 200 || a.h.Get("Cache-Control") != "no-store" {
		t.Fatalf("mint: %d %s", a.code, a.body)
	}
	k := a.json(t)
	tok, id := k["token"].(string), k["id"].(string)
	exp, _ := time.Parse(time.RFC3339, k["expires"].(string))
	if !strings.HasPrefix(tok, KeyPrefix) || k["user"] != "kim" || k["component"] != "apps/agent" || !exp.Equal(r.clock.now().Add(10*time.Minute)) || k["url"] != BaseURL {
		t.Fatalf("the key: %v", k)
	}
	if g, ok := r.keys.Resolve(tok); !ok || g.Label != "run 7" || g.Sandbox != "sb-1" {
		t.Fatalf("grant: %+v %v", g, ok)
	}
	// a frame's driver, and a backend that serves everyone (metered to the tile alone)
	if k := mint(pFrame("apps/agent", "bob"), nil).json(t); k["user"] != "bob" {
		t.Errorf("a frame's key: %v", k)
	}
	nobody := mint(pInstance("apps/agent"), nil).json(t)
	if nobody["user"] != "" || nobody["component"] != "apps/agent" {
		t.Errorf("a key for nobody: %v", nobody)
	}
	if exp, _ := time.Parse(time.RFC3339, nobody["expires"].(string)); !exp.Equal(r.clock.now().Add(DefaultComponentKeyTTL)) {
		t.Errorf("the default life: %v", nobody["expires"])
	}

	// the key calls, as kim through apps/agent
	call := func(tok string) int {
		req := r.req(auth.Principal{Via: "llm"}, "POST", "/llm/v1/chat/completions", chatBody("main/gpt-5", false))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		r.mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := call(tok); c != 200 {
		t.Fatalf("a call with the key: %d", c)
	}
	if c := call(nobody["token"].(string)); c != 200 {
		t.Fatalf("a call with nobody's key: %d", c)
	}
	rows := r.usageRows("")
	if len(rows) != 2 || rows[0].User != "" || rows[0].Component != "apps/agent" || rows[1].User != "kim" || rows[1].Component != "apps/agent" || rows[1].Requests != 1 {
		t.Fatalf("usage: %+v", rows)
	}

	// revoke: another component can't, its own can, twice is fine
	if a := r.do(pPartition("apps/other", "kim"), "DELETE", "/llm/keys/"+id, nil); a.code != 204 {
		t.Errorf("another component's DELETE: %d", a.code)
	}
	if c := call(tok); c != 200 {
		t.Fatalf("another component ended the key: %d", c)
	}
	for i := 0; i < 2; i++ {
		if a := r.do(pPartition("apps/agent", "kim"), "DELETE", "/llm/keys/"+id, nil); a.code != 204 {
			t.Errorf("DELETE: %d", a.code)
		}
	}
	if c := call(tok); c != 401 {
		t.Fatalf("a revoked key still calls: %d", c)
	}
	if a := r.do(pAdmin, "DELETE", "/llm/keys/"+id, nil); a.code != 403 {
		t.Errorf("a person's DELETE: %d", a.code)
	}
	// Close ends every key
	r.s.Close()
	if _, ok := r.keys.Resolve(nobody["token"].(string)); ok {
		t.Error("a key outlived the connector's Close")
	}
	r.noSecret("the key answers", a.body)
}

// covers D226 — the LLM-only listener's handler: the three model routes at
// /v1 for an LLM key, and nothing for anything else — no other path, no
// other credential, whatever principal the request claims.
func TestKeyOnlyHandler(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	tok, _, revoke, err := r.s.MintKey(KeyGrant{User: "kim", Component: "apps/agent", Sandbox: "sb-1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := r.s.KeyOnlyHandler()
	do := func(method, path, bearer string, body any, as *auth.Principal) answer {
		req := r.req(auth.Principal{}, method, path, body)
		if as != nil {
			req = r.req(*as, method, path, body)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return answer{rec.Code, rec.Header(), rec.Body.Bytes()}
	}
	if a := do("POST", "/v1/chat/completions", tok, chatBody("main/gpt-5", false), nil); a.code != 200 {
		t.Fatalf("a call: %d %s", a.code, a.body)
	}
	if a := do("POST", "/v1/responses", tok, map[string]any{"model": "main/gpt-5", "input": "hi"}, nil); a.code != 200 {
		t.Fatalf("a Responses call: %d %s", a.code, a.body)
	}
	var list struct{ Data []struct{ ID string } }
	if a := do("GET", "/v1/models", tok, nil, nil); a.code != 200 || json.Unmarshal(a.body, &list) != nil || len(list.Data) != 1 || list.Data[0].ID != "main/gpt-5" {
		t.Fatalf("models: %d %s", a.code, a.body)
	}
	if rows := r.usageRows(""); len(rows) != 1 || rows[0].User != "kim" || rows[0].Component != "apps/agent" || rows[0].Requests != 2 {
		t.Fatalf("usage: %+v", rows)
	}
	// no key: 401, whatever else the request is — an admin's principal included
	for _, bearer := range []string{"", "sk-something", "xbl_not-a-key"} {
		if a := do("POST", "/v1/chat/completions", bearer, chatBody("main/gpt-5", false), &pAdmin); a.code != 401 || a.errCode(t) != "invalid_api_key" {
			t.Errorf("bearer %q: %d %s", bearer, a.code, a.body)
		}
	}
	// nothing but the three routes
	for _, c := range [][2]string{{"GET", "/"}, {"GET", "/v1/"}, {"GET", "/api/xbin/llm/config"}, {"GET", "/api/xbin/whoami"}, {"PUT", "/v1/config"},
		{"GET", "/v1/chat/completions"}, {"POST", "/v1/models"}, {"POST", "/llm/keys"}, {"GET", "/healthz"}, {"POST", "/v1/../api/xbin/llm/keys"}} {
		if a := do(c[0], c[1], tok, nil, &pAdmin); a.code != http.StatusNotFound && a.code != http.StatusMovedPermanently {
			t.Errorf("%s %s with a key: %d %s", c[0], c[1], a.code, a.body)
		}
	}
	revoke()
	if a := do("GET", "/v1/models", tok, nil, nil); a.code != 401 {
		t.Errorf("a revoked key: %d", a.code)
	}
}
