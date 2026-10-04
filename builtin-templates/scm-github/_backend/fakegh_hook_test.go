// fakegh_hook_test.go — the fake GitHub's webhook side: deliveries signed
// with the secret setup gave the fake's hook config, sent to this tile's
// POST /hook/github as ingress; the fixtures in testdata/; and the fake
// consumers events are delivered to (an agent's /adapter/scm/event that
// answers what the test says).
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func init() {
	// No test reaches xbind: no bindings, and no cron to register.
	defaultAgents = func() []agentEndpoint { return nil }
	defaultCron = func(bool) error { return nil }
}

// fakeAgent is a consumer's /adapter/scm/event.
type fakeAgent struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []map[string]any
	status []int // the next answers (then 200)
	hits   int
}

func newFakeAgent(t *testing.T) *fakeAgent {
	a := &fakeAgent{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.hits++
		if r.URL.Path != "/adapter/scm/event" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		st := http.StatusOK
		if len(a.status) > 0 {
			st, a.status = a.status[0], a.status[1:]
		}
		if st == http.StatusOK {
			var ev map[string]any
			if err := json.Unmarshal(b, &ev); err != nil {
				t.Errorf("event: %v", err)
			}
			a.got = append(a.got, ev)
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

// answer queues the next answers.
func (a *fakeAgent) answer(st ...int) {
	a.mu.Lock()
	a.status = append(a.status, st...)
	a.mu.Unlock()
}

// take returns the events taken so far and forgets them.
func (a *fakeAgent) take() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.got
	a.got = nil
	return g
}

// evEnv is a set-up global instance with two bound consumers.
type evEnv struct {
	*env
	agent, other *fakeAgent
	cron         []bool // every cron change, in order
	cronMu       sync.Mutex
	n            int // deliveries sent
}

func newEvEnv(t *testing.T) *evEnv {
	t.Helper()
	ee := &evEnv{env: newEnv(t), agent: newFakeAgent(t), other: newFakeAgent(t)}
	ee.setup()
	ee.wire(ee.global)
	return ee
}

// wire points an instance's hub at the fake consumers and the cron log.
func (ee *evEnv) wire(s *srv) {
	h := s.ev()
	h.mu.Lock()
	h.agents = func() []agentEndpoint {
		return []agentEndpoint{{Provider: "apps/agent", URL: ee.agent.srv.URL}, {Provider: "apps/other-agent", URL: ee.other.srv.URL},
			{Provider: "apps/mine", URL: ee.other.srv.URL, Personal: true}}
	}
	h.post = func(ctx context.Context, u string, body []byte) (int, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	h.cron = func(on bool) error {
		ee.cronMu.Lock()
		ee.cron = append(ee.cron, on)
		ee.cronMu.Unlock()
		return nil
	}
	h.mu.Unlock()
	s.startEvents(false)
}

func (ee *evEnv) cronLog() []bool {
	ee.cronMu.Lock()
	defer ee.cronMu.Unlock()
	return append([]bool{}, ee.cron...)
}

// hookSecret is the secret setup gave the fake's hook config.
func (f *fakeGH) hookSecretNow() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hookSecret
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// sendHook delivers body as GitHub would, signed with secret, to h.
func (e *env) sendHook(h http.Handler, c caller, ghEvent, delivery, secret string, body []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	sig := ""
	if secret != "" {
		sig = sign(secret, body)
	}
	return e.sendHookSig(h, c, ghEvent, delivery, sig, body)
}

// sendHookSig delivers body with X-Hub-Signature-256 as given.
func (e *env) sendHookSig(h http.Handler, c caller, ghEvent, delivery, sig string, body []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest("POST", "/hook/github", bytes.NewReader(body))
	c.set(r)
	r.Header.Set("X-GitHub-Event", ghEvent)
	r.Header.Set("X-GitHub-Delivery", delivery)
	if sig != "" {
		r.Header.Set("X-Hub-Signature-256", sig)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	e.seen = append(e.seen, seenResp{"POST", "/hook/github", w.Code, w.Body.String()})
	return w
}

// hook delivers a fixture (or a body) with a new delivery id, signed with
// the fake's current secret, through ingress.
func (ee *evEnv) hook(ghEvent string, body []byte) *httptest.ResponseRecorder {
	ee.t.Helper()
	ee.n++
	return ee.sendHook(ee.gH, ingress, ghEvent, fmt.Sprintf("d0000000-0000-0000-0000-%012d", ee.n), ee.gh.hookSecretNow(), body)
}

// deliver runs one delivery pass at global.
func (ee *evEnv) deliver() { ee.global.ev().deliverDue(context.Background()) }

// subscribe posts a tile's subscription at global.
func (ee *evEnv) subscribe(c caller, sub map[string]any) subView {
	ee.t.Helper()
	r := ee.call(ee.gH, c, "POST", "/scm/subscriptions", sub)
	if r.Code != 201 && r.Code != 200 {
		ee.t.Fatalf("subscribe: %d %s", r.Code, r.Body)
	}
	var v subView
	decode(ee.t, r, &v)
	return v
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixtureWith is a fixture with fields changed (a path of keys → value).
func fixtureWith(t *testing.T, name string, set map[string]any) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(fixture(t, name), &m); err != nil {
		t.Fatal(err)
	}
	for path, v := range set {
		cur := m
		keys := splitPath(path)
		for _, k := range keys[:len(keys)-1] {
			cur = cur[k].(map[string]any)
		}
		cur[keys[len(keys)-1]] = v
	}
	b, _ := json.Marshal(m)
	return b
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, c := range p {
		if c == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	return append(out, cur)
}

// get reads a path of keys out of a decoded event.
func get(m map[string]any, path string) any {
	var cur any = m
	for _, k := range splitPath(path) {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}
