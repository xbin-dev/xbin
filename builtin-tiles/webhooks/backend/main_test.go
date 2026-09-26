package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type memStore struct{ m map[string][]byte }

func (s *memStore) Get(k string) ([]byte, error) {
	if v, ok := s.m[k]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("not found")
}
func (s *memStore) Put(k string, v []byte) error { s.m[k] = v; return nil }

// fakeAgent answers /adapter/event with status (and records the events).
type fakeAgent struct {
	mu     sync.Mutex
	status int
	events []map[string]any
	srv    *httptest.Server
}

func newFakeAgent(t *testing.T, status int) *fakeAgent {
	a := &fakeAgent{status: status}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev map[string]any
		_ = json.NewDecoder(r.Body).Decode(&ev)
		a.mu.Lock()
		a.events = append(a.events, ev)
		st := a.status
		a.mu.Unlock()
		w.WriteHeader(st)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func fixture(t *testing.T, agents ...*fakeAgent) (*Tile, *http.ServeMux, map[string]string) {
	secrets := map[string]string{}
	var eps []agentEndpoint
	for i, a := range agents {
		eps = append(eps, agentEndpoint{Provider: fmt.Sprintf("apps/agent%d", i), URL: a.srv.URL})
	}
	tile := newTile(&memStore{m: map[string][]byte{}}, func(n string) (string, error) { return secrets[n], nil },
		func(n, v string) error {
			if v == "" {
				delete(secrets, n)
			} else {
				secrets[n] = v
			}
			return nil
		}, func() []agentEndpoint { return eps }, http.DefaultClient)
	mux := http.NewServeMux()
	tile.routes(mux)
	return tile, mux, secrets
}

func call(mux *http.ServeMux, method, target, from, role string, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("X-XBin-From", from)
	if role != "" {
		r.Header.Set("X-XBin-Role", role)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func create(t *testing.T, mux *http.ServeMux, body string) (hook, string) {
	t.Helper()
	w := call(mux, "POST", "/hooks", "apps/webhooks", "admin", body, nil)
	var out struct {
		Hook   hook
		Secret string
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Secret == "" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	return out.Hook, out.Secret
}

// A token hook: only from outside (or the owner testing), only with its
// token; the event goes to the agent as public data, deduped by the id the
// hook reads, its topic built from the hook's name.
func TestTokenHook(t *testing.T) {
	agent := newFakeAgent(t, 200)
	_, mux, _ := fixture(t, agent)
	h, tok := create(t, mux, `{"name":"deploy","auth":"token","eventIdFrom":"header:X-Delivery","topicFrom":"json:env"}`)
	body := `{"env":"prod","sha":"abc"}`
	if w := call(mux, "POST", "/hook/"+h.ID, "apps/other", "writer", body, nil); w.Code != 403 {
		t.Fatalf("another tile: %d", w.Code)
	}
	if w := call(mux, "POST", "/hook/"+h.ID+"?token=nope", "ingress", "", body, nil); w.Code != 401 {
		t.Fatalf("a bad token: %d", w.Code)
	}
	if w := call(mux, "POST", "/hook/zzz?token="+tok, "ingress", "", body, nil); w.Code != 404 {
		t.Fatalf("an unknown hook: %d", w.Code)
	}
	w := call(mux, "POST", "/hook/"+h.ID+"?token="+tok, "ingress", "", body, map[string]string{"X-Delivery": "d-1"})
	if w.Code != 202 {
		t.Fatalf("delivery: %d %s", w.Code, w.Body)
	}
	ev := agent.events[0]
	if ev["eventId"] != h.ID+":d-1" || ev["topic"] != "deploy/prod" || ev["dataClass"] != "public" || ev["data"].(map[string]any)["sha"] != "abc" {
		t.Fatalf("forwarded: %+v", ev)
	}
	if w := call(mux, "POST", "/hook/"+h.ID, "ingress", "", "not json", map[string]string{"Authorization": "Bearer " + tok}); w.Code != 202 {
		t.Fatalf("bearer + text: %d", w.Code)
	}
	if ev := agent.events[1]; ev["text"] != "not json" || !strings.HasPrefix(ev["eventId"].(string), h.ID+":") {
		t.Fatalf("text body: %+v", ev)
	}
	big := strings.Repeat("x", maxBody+10)
	if w := call(mux, "POST", "/hook/"+h.ID+"?token="+tok, "ingress", "", big, nil); w.Code != 413 {
		t.Fatalf("an oversized body: %d", w.Code)
	}
}

// An HMAC hook checks GitHub's signature over the raw body.
func TestHMACHook(t *testing.T) {
	agent := newFakeAgent(t, 200)
	_, mux, _ := fixture(t, agent)
	h, sec := create(t, mux, `{"name":"gh","auth":"hmac","eventIdFrom":"header:X-GitHub-Delivery","topicFrom":"header:X-GitHub-Event"}`)
	body := `{"ref":"refs/heads/main"}`
	mac := hmac.New(sha256.New, []byte(sec))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	hdr := map[string]string{"X-Hub-Signature-256": sig, "X-GitHub-Delivery": "g1", "X-GitHub-Event": "push"}
	if w := call(mux, "POST", "/hook/"+h.ID, "ingress", "", body, hdr); w.Code != 202 {
		t.Fatalf("signed: %d %s", w.Code, w.Body)
	}
	if agent.events[0]["topic"] != "gh/push" {
		t.Fatalf("topic: %+v", agent.events[0])
	}
	hdr["X-Hub-Signature-256"] = "sha256=" + strings.Repeat("0", 64)
	if w := call(mux, "POST", "/hook/"+h.ID, "ingress", "", body, hdr); w.Code != 401 {
		t.Fatalf("a forged signature: %d", w.Code)
	}
}

// The sender learns what happened: 404 when no trigger takes it, 503 when
// the agent is halted (so it retries); rotating the secret retires the old.
func TestForwardOutcomes(t *testing.T) {
	agent := newFakeAgent(t, 404)
	_, mux, _ := fixture(t, agent)
	h, tok := create(t, mux, `{"name":"x","auth":"token"}`)
	if w := call(mux, "POST", "/hook/"+h.ID+"?token="+tok, "ingress", "", `{}`, nil); w.Code != 404 {
		t.Fatalf("no trigger: %d", w.Code)
	}
	agent.status = 503
	if w := call(mux, "POST", "/hook/"+h.ID+"?token="+tok, "ingress", "", `{}`, nil); w.Code != 503 {
		t.Fatalf("halted: %d", w.Code)
	}
	w := call(mux, "POST", "/hooks/"+h.ID+"/rotate", "apps/webhooks", "admin", ``, nil)
	var r struct{ Secret string }
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	agent.status = 200
	if w := call(mux, "POST", "/hook/"+h.ID+"?token="+tok, "ingress", "", `{}`, nil); w.Code != 401 {
		t.Fatalf("the old token after a rotate: %d", w.Code)
	}
	if w := call(mux, "POST", "/hook/"+h.ID+"?token="+r.Secret, "ingress", "", `{}`, nil); w.Code != 202 {
		t.Fatalf("the new token: %d", w.Code)
	}
	_, unbound, _ := fixture(t)
	h2, tok2 := create(t, unbound, `{"name":"y","auth":"token"}`)
	if w := call(unbound, "POST", "/hook/"+h2.ID+"?token="+tok2, "ingress", "", `{}`, nil); w.Code != 503 {
		t.Fatalf("not bound: %d", w.Code)
	}
}

// Only a user with write access changes hooks.
func TestHookAdmin(t *testing.T) {
	_, mux, secrets := fixture(t)
	w := call(mux, "POST", "/hooks", "apps/webhooks", "admin", `{"name":"a","auth":"token"}`, map[string]string{"X-XBin-User": "bob", "X-XBin-User-Level": "read"})
	if w.Code != 403 {
		t.Fatalf("a reader made a hook: %d", w.Code)
	}
	h, _ := create(t, mux, `{"name":"a","auth":"token","id":"my-hook"}`)
	if h.ID != "my-hook" || secrets["hook-token-my-hook"] == "" {
		t.Fatalf("chosen id and vault: %+v %v", h, secrets)
	}
	if w := call(mux, "POST", "/hooks", "apps/webhooks", "admin", `{"name":"a b","auth":"token"}`, nil); w.Code != 400 {
		t.Fatalf("a name with a space: %d", w.Code)
	}
	call(mux, "DELETE", "/hooks/my-hook", "apps/webhooks", "admin", ``, nil)
	if _, ok := secrets["hook-token-my-hook"]; ok {
		t.Fatal("a deleted hook kept its secret")
	}
}
