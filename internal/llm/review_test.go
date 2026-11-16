package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// covers D226 — a caller chooses the model name it sends, never how many
// rows the usage store keeps or what the log says: a name is bounded, and a
// name neither configuration nor the upstream's own list has is metered and
// logged as "<upstream>/(unknown)".
func TestModelNamesAreBounded(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t) // lists gpt-5 and gpt-5-mini; answers any name
	r.addUpstream("main", up, nil)
	call := func(model any) answer {
		b := chatBody("", false)
		b["model"] = model
		return r.do(pOwner, "POST", "/llm/v1/chat/completions", b)
	}
	before := up.calls()
	for what, model := range map[string]any{
		"a name past the cap": strings.Repeat("m", maxModelName+1),
		"a name with a space": "gpt 5",
		"a name with a line":  "gpt-5\nlevel=ERROR msg=forged",
		"a number":            5,
		"a list":              []string{"gpt-5"},
	} {
		if a := call(model); a.code != 400 || a.errCode(t) != "xbin_llm_model" {
			t.Errorf("%s: %d %s", what, a.code, a.body)
		}
	}
	if up.calls() != before {
		t.Error("a refused model name reached the upstream")
	}
	// a hundred names of the caller's own, which the upstream happens to answer
	for i := 0; i < 100; i++ {
		if a := call(fmt.Sprintf("made-up-%d", i)); a.code != 200 {
			t.Fatalf("call %d: %d %s", i, a.code, a.body)
		}
	}
	// …and some the upstream refuses, with the caller's name in its answer
	up.mu.Lock()
	up.fail = func(w http.ResponseWriter, _ *http.Request) bool {
		writeJSON(w, 404, map[string]any{"error": map[string]any{"message": "no such model"}})
		return true
	}
	up.mu.Unlock()
	for i := 0; i < 20; i++ {
		call(fmt.Sprintf("refused-name-%d", i))
	}
	up.mu.Lock()
	up.fail = nil
	up.mu.Unlock()
	if a := call("gpt-5"); a.code != 200 { // a model the upstream lists keeps its name
		t.Fatalf("a listed model: %d %s", a.code, a.body)
	}
	if a := call("main/gpt-5-mini"); a.code != 200 {
		t.Fatalf("a listed model, prefixed: %d %s", a.code, a.body)
	}
	var names []string
	total := int64(0)
	for _, row := range r.usageRows("") {
		names = append(names, row.Model)
		total += row.Requests
	}
	if !slices.Equal(names, []string{"main/(unknown)", "main/gpt-5", "main/gpt-5-mini"}) || total != 122 {
		t.Errorf("usage rows after 120 made-up names: %v (%d requests)", names, total)
	}
	r.s.meter.flush()
	raw, _ := os.ReadFile(filepath.Join(r.dir, "usage", "2026-11.json"))
	if strings.Contains(string(raw), "made-up") || strings.Contains(string(raw), "refused-name") {
		t.Error("a caller's own model name is in the usage file")
	}
	if strings.Contains(r.logs.String(), "refused-name") || strings.Contains(r.logs.String(), "made-up") || strings.Contains(r.logs.String(), "forged") {
		t.Errorf("a caller's own model name is in the log:\n%.600s", r.logs.String())
	}
	if !strings.Contains(r.logs.String(), `model="main/(unknown)"`) && !strings.Contains(r.logs.String(), "model=main/(unknown)") {
		t.Errorf("the failed calls are not logged under the fixed name:\n%.600s", r.logs.String())
	}
	if up.modelsN != 1 {
		t.Errorf("the upstream's model list was asked for %d times for %d calls", up.modelsN, total)
	}
}

// covers D226 — what the relay reads is what the upstream reads: "stream"
// is a boolean or the call is refused, a member sent twice goes out once,
// and a background response (never metered) is refused.
func TestRequestIsReadStrictly(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	post := func(path, body string) answer { return r.do(pOwner, "POST", path, body) }
	before := up.calls()
	for _, bad := range []string{`"true"`, `1`, `"yes"`, `[true]`} {
		a := post("/llm/v1/chat/completions", `{"model":"gpt-5","messages":[],"stream":`+bad+`}`)
		if a.code != 400 || a.errCode(t) != "xbin_llm_body" || !strings.Contains(string(a.body), "stream") {
			t.Errorf("stream %s: %d %s", bad, a.code, a.body)
		}
	}
	for _, bg := range []string{`true`, `"true"`, `1`} {
		a := post("/llm/v1/responses", `{"model":"gpt-5","input":"hi","background":`+bg+`}`)
		if a.code != 400 || a.errCode(t) != "xbin_llm_unsupported" || !strings.Contains(string(a.body), "background") {
			t.Errorf("background %s: %d %s", bg, a.code, a.body)
		}
	}
	if up.calls() != before {
		t.Error("a refused request reached the upstream")
	}
	if a := post("/llm/v1/responses", `{"model":"gpt-5","input":"hi","background":false}`); a.code != 200 {
		t.Errorf("background false: %d %s", a.code, a.body)
	}
	// "stream" twice: the relay reads the last, and the upstream gets only that one — with its usage chunk asked for
	raw := &rawUpstream{}
	rawSrv := httptest.NewServer(raw)
	defer rawSrv.Close()
	r.must(200, pOwner, "PUT", "/llm/upstreams/raw", map[string]any{"baseURL": rawSrv.URL, "models": []string{"m"}})
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, r.req(pOwner, "POST", "/llm/v1/chat/completions", `{"stream":false,"model":"raw/m","messages":[],"stream":true}`))
	if n := strings.Count(raw.body, `"stream"`); rec.Code != 200 || n != 1 || !strings.Contains(raw.body, `"stream":true`) || !strings.Contains(raw.body, `"include_usage":true`) || !strings.Contains(raw.body, `"model":"m"`) {
		t.Errorf("the upstream got (%d): %s", rec.Code, raw.body)
	}
}

// rawUpstream keeps the bytes of the last request it got.
type rawUpstream struct{ body string }

func (u *rawUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.body = string(b)
	writeJSON(w, 200, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
}

// covers D226 — an alias adds a name; it never takes over what
// configuration names. An admin's alias can't redirect a model of the
// host's upstream, the host's locked default, or a model an upstream lists
// — neither through the API nor from the admin's stored settings.
func TestAliasNeverShadowsConfiguration(t *testing.T) {
	house, mine := newUpstream(t), newUpstream(t)
	dir := t.TempDir()
	host := writeHost(t, dir, `{"people": "everyone", "upstreams": {"house": {"baseURL": "`+house.URL+`", "models": ["gpt-5"]}}, "defaults": {"model": "house/gpt-5"}}`, "")
	// an admin store written before the host's file came (or by hand): aliases over the host's names
	data := t.TempDir()
	stored := `{"schema": 1, "upstreams": {"mine": {"baseURL": "` + mine.URL + `"}},
		"aliases": {"house/gpt-5": "mine/other", "gpt-5": "mine/other", "extra": "mine/other"}, "defaults": {}, "quotas": {}}`
	if err := os.WriteFile(filepath.Join(data, "config.json"), []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigAt(t, data, host, &fakeVault{inited: true})
	for _, model := range []string{"", "house/gpt-5", "gpt-5", "xbin/default"} {
		if a := r.do(pUser("bob"), "POST", "/llm/v1/chat/completions", chatBody(model, false)); a.code != 200 {
			t.Fatalf("model %q: %d %s", model, a.code, a.body)
		}
	}
	if house.calls() != 4 || mine.calls() != 0 {
		t.Errorf("the host's model was redirected by the admin's aliases: house saw %d calls, the admin's upstream %d", house.calls(), mine.calls())
	}
	r.must(200, pUser("bob"), "POST", "/llm/v1/chat/completions", chatBody("extra", false)) // an alias for a name nothing else has still works
	if mine.calls() != 1 || mine.lastBody()["model"] != "other" {
		t.Errorf("a plain alias: %d calls, %v", mine.calls(), mine.lastBody())
	}
	// the admin's view says which aliases are dead
	problems := fmt.Sprint(r.must(200, pOwner, "GET", "/llm/config", nil).json(t)["problems"])
	if !strings.Contains(problems, "alias house/gpt-5: never used") || !strings.Contains(problems, "alias gpt-5: never used") {
		t.Errorf("problems: %s", problems)
	}
	// through the API such an alias is refused
	for what, alias := range map[string]string{"a model of the host's upstream": "house/gpt-5", "any name under an upstream": "mine/whatever", "a listed model's bare name": "gpt-5"} {
		a := r.do(pOwner, "PUT", "/llm/config", map[string]any{"aliases": map[string]string{alias: "mine/other"}})
		if a.code != 400 || !strings.Contains(string(a.body), "alias") {
			t.Errorf("an alias over %s: %d %s", what, a.code, a.body)
		}
	}
	// and an upstream can't be added under existing aliases' names
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"aliases": map[string]string{"later/model": "mine/other"}})
	if a := r.do(pOwner, "PUT", "/llm/upstreams/later", map[string]any{"baseURL": mine.URL}); a.code != 400 || !strings.Contains(string(a.body), "later/model") {
		t.Errorf("an upstream named over an alias: %d %s", a.code, a.body)
	}
}

// covers D226 — a key is only ever sent to the address it was set for:
// changing an upstream's scheme, host or port drops its key unless the same
// request brings one.
func TestKeyDoesNotFollowAMovedUpstream(t *testing.T) {
	r := newRig(t, "")
	up, elsewhere := newUpstream(t), newUpstream(t)
	r.addUpstream("main", up, nil)
	keyOf := func(a answer) (set bool, notices string) {
		v := a.json(t)
		for _, u := range v["upstreams"].([]any) {
			if um := u.(map[string]any); um["name"] == "main" {
				set = um["key"].(map[string]any)["set"] == true
			}
		}
		return set, fmt.Sprint(v["notices"])
	}
	// the same address (another path, a trailing slash): the key stays
	if set, notes := keyOf(r.must(200, pOwner, "PUT", "/llm/upstreams/main", map[string]any{"baseURL": up.URL + "/v1/"})); !set || notes != "<nil>" {
		t.Errorf("the same address: key set %v, notices %s", set, notes)
	}
	// another address, no key in the request: the key is gone, and said so
	a := r.must(200, pOwner, "PUT", "/llm/upstreams/main", map[string]any{"baseURL": elsewhere.URL})
	if set, notes := keyOf(a); set || !strings.Contains(notes, "the key of upstream main was removed") {
		t.Errorf("a moved upstream: key set %v, notices %s", set, notes)
	}
	r.must(200, pOwner, "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	elsewhere.mu.Lock()
	got := slices.Clone(elsewhere.auth)
	elsewhere.mu.Unlock()
	for _, h := range got {
		if h != "" {
			t.Errorf("the new address got the old key: Authorization %q", h)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(r.dir, "keys.json")); len(raw) > 0 {
		if k, _ := r.s.keys.get("main"); k != "" {
			t.Error("the dropped key is still in the vault")
		}
	}
	// moving with a key in the same request keeps that key
	if set, _ := keyOf(r.must(200, pOwner, "PUT", "/llm/upstreams/main", map[string]any{"baseURL": up.URL, "key": upstreamKey})); !set {
		t.Error("a move that brings its key lost it")
	}
	// while sealed the key can't be cut out of the vault, but it is not used again
	r.vault.set(true, true)
	if set, _ := keyOf(r.must(200, pOwner, "PUT", "/llm/upstreams/main", map[string]any{"baseURL": elsewhere.URL})); set {
		t.Error("a move while the vault is sealed kept the key marked")
	}
	r.vault.set(true, false)
	r.must(200, pOwner, "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	elsewhere.mu.Lock()
	last := elsewhere.auth[len(elsewhere.auth)-1]
	elsewhere.mu.Unlock()
	if last != "" {
		t.Errorf("after a sealed move the new address got Authorization %q", last)
	}
}

// covers D226 — the upstream test is a call like any other: it needs the
// connector on, stops at the quotas, sends the smallest request and is
// metered to the admin who ran it.
func TestUpstreamTestHonoursSwitchesAndQuotas(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.must(200, pAdmin, "POST", "/llm/upstreams/main/test", nil)
	sent := up.lastBody()
	if sent["max_tokens"] != 1.0 || len(sent["messages"].([]any)) != 1 || sent["messages"].([]any)[0].(map[string]any)["content"] != "ping" || sent["stream"] != nil {
		t.Errorf("the test call's request: %v", sent)
	}
	rows := r.usageRows("")
	if len(rows) != 1 || rows[0].User != "ada" || rows[0].Requests != 1 || rows[0].Model != "main/gpt-5" {
		t.Errorf("metered: %+v", rows)
	}
	// the admin console (a tile holding xbin:admin), driven by ada: hers too
	r.must(200, pFrame("apps/admin", "ada"), "POST", "/llm/upstreams/main/test", nil)
	if rows := r.usageRows("?component=apps/admin"); len(rows) != 1 || rows[0].User != "ada" {
		t.Errorf("a test from the admin console: %+v", rows)
	}
	// a model that takes max_completion_tokens is asked again that way
	up.mu.Lock()
	up.fail = func(w http.ResponseWriter, req *http.Request) bool {
		up.mu.Lock()
		last := up.bodies[len(up.bodies)-1]
		up.mu.Unlock()
		if last["max_tokens"] != nil {
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."}})
			return true
		}
		return false
	}
	up.mu.Unlock()
	if res := r.must(200, pAdmin, "POST", "/llm/upstreams/main/test", nil).json(t); res["ok"] != true || up.lastBody()["max_completion_tokens"] == nil {
		t.Errorf("a model that wants max_completion_tokens: %v, sent %v", res, up.lastBody())
	}
	up.mu.Lock()
	up.fail = nil
	up.mu.Unlock()

	// a model off the upstream's list is refused before any call
	if a := r.do(pAdmin, "POST", "/llm/upstreams/main/test", map[string]any{"model": "other"}); a.code != 400 {
		t.Errorf("a model off the list: %d %s", a.code, a.body)
	}
	// at the quota: refused, nothing sent
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"workspace": map[string]any{"tokensPerMonth": 1}})
	before := up.calls()
	if a := r.do(pAdmin, "POST", "/llm/upstreams/main/test", nil); a.code != 429 || !strings.Contains(string(a.body), "the workspace's monthly LLM token quota") {
		t.Errorf("a test past the workspace quota: %d %s", a.code, a.body)
	}
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"workspace": nil})
	// switched off: refused
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"enabled": false})
	if a := r.do(pAdmin, "POST", "/llm/upstreams/main/test", nil); a.code != 409 || !strings.Contains(string(a.body), "off") {
		t.Errorf("a test while the connector is off: %d %s", a.code, a.body)
	}
	if up.calls() != before {
		t.Error("a refused test reached the upstream")
	}
	// …and when the host switched it off, an admin's test is no way around
	hosted := newRigAt(t, t.TempDir(), writeHost(t, t.TempDir(), `{"enabled": false, "upstreams": {"house": {"baseURL": "`+up.URL+`", "models": ["gpt-5"]}}}`, ""), &fakeVault{inited: true})
	if a := hosted.do(pOwner, "POST", "/llm/upstreams/house/test", nil); a.code != 409 || !strings.Contains(string(a.body), "your host") || up.calls() != before {
		t.Errorf("a test of the host's upstream while the host has it off: %d %s", a.code, a.body)
	}
}

// covers D226 — the deployment file is read again when it changed after
// the read began: a file caught half-written is picked up once its writer
// is done.
func TestHostFileCaughtMidWrite(t *testing.T) {
	up := newUpstream(t)
	dir := t.TempDir()
	cfg := func(limit int) string {
		return fmt.Sprintf(`{"upstreams": {"hosted": {"baseURL": "%s"}}, "quotas": {"default": {"tokensPerDay": %d}}}`, up.URL, limit)
	}
	host := writeHost(t, dir, cfg(1000), "")
	r := newRig(t, host)
	limit := func() float64 {
		t.Helper()
		r.clock.add(2 * hostCheckGap)
		st := r.must(200, pOwner, "GET", "/llm", nil).json(t)
		return st["quota"].(map[string]any)["limits"].([]any)[0].(map[string]any)["value"].(float64)
	}
	if limit() != 1000 {
		t.Fatal("the file at boot is not in force")
	}
	// a writer is half-way: the reader gets the first half, and the writer finishes right after the read
	final := cfg(2000)
	if err := os.WriteFile(host, []byte(final[:len(final)/2]), 0o600); err != nil {
		t.Fatal(err)
	}
	afterHostRead = func(string) {
		afterHostRead = nil
		if err := os.WriteFile(host, []byte(final), 0o600); err != nil {
			t.Error(err)
		}
	}
	defer func() { afterHostRead = nil }()
	if limit() != 1000 {
		t.Error("a half-written file was put in force")
	}
	if limit() != 2000 {
		t.Error("the file its writer finished during the read was never read again")
	}
}

// covers D226 — one caller holds a bounded number of calls in flight; past
// it the answer is a clear 429, and nobody else is held.
func TestConcurrencyCap(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	up.hold = make(chan struct{})
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	v := r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone", "concurrency": 2}).json(t)
	if v["concurrency"] != 2.0 {
		t.Fatalf("concurrency in the view: %v", v["concurrency"])
	}
	if a := r.do(pOwner, "PUT", "/llm/config", map[string]any{"concurrency": MaxConcurrency + 1}); a.code != 400 {
		t.Errorf("a concurrency past the maximum: %d", a.code)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := pUser(req.Header.Get("X-Test-User"))
		r.mux.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
	}))
	defer srv.Close()
	start := func(user string) *http.Response {
		t.Helper()
		body, _ := json.Marshal(chatBody("gpt-5", true))
		req, _ := http.NewRequest("POST", srv.URL+"/llm/v1/chat/completions", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", user)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	one, two := start("bob"), start("bob") // both held open by the upstream
	third := start("bob")
	body, _ := io.ReadAll(third.Body)
	third.Body.Close()
	a := answer{third.StatusCode, third.Header, body}
	if a.code != 429 || a.errCode(t) != "xbin_llm_busy" || a.h.Get("Retry-After") != "1" || !strings.Contains(string(body), "2 of yours are in flight") {
		t.Errorf("a third call at a cap of two: %d %s %v", a.code, body, a.h)
	}
	other := start("cy") // another person has slots of their own
	if other.StatusCode != 200 {
		t.Errorf("another caller: %d", other.StatusCode)
	}
	close(up.hold)
	for _, resp := range []*http.Response{one, two, other} {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	// the slots come back
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := start("bob")
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bob's slots never came back: %d", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// covers D226 — many callers at once: every call is metered exactly once
// and the per-caller cap holds (run under -race).
func TestConcurrentCalls(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t) // 120 tokens a call
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone", "concurrency": 4})
	const users, each = 8, 25
	var ok, busy, other atomic.Int64
	var wg sync.WaitGroup
	for u := 0; u < users; u++ {
		for i := 0; i < each; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				path, body := "/llm/v1/chat/completions", chatBody("gpt-5", i%2 == 0)
				if i%3 == 0 {
					path = "/llm/v1/responses"
				}
				for {
					switch a := r.do(pUser(fmt.Sprintf("user%d", u)), "POST", path, body); a.code {
					case 200:
						ok.Add(1)
						return
					case 429: // over the cap of 4: the caller waits and comes back
						busy.Add(1)
						time.Sleep(time.Millisecond)
					default:
						other.Add(1)
						return
					}
				}
			}()
		}
	}
	// readers and writers of the same state, meanwhile
	stop := make(chan struct{})
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			r.do(pOwner, "GET", "/llm/usage", nil)
			r.do(pOwner, "GET", "/llm/config", nil)
			r.do(pUser("user0"), "GET", "/llm", nil)
			r.do(pOwner, "PUT", "/llm/quotas", map[string]any{"users": map[string]any{"nobody": map[string]any{"tokensPerDay": i + 1}}})
			r.s.meter.flush()
		}
	}()
	wg.Wait()
	close(stop)
	bg.Wait()
	if ok.Load() != users*each || other.Load() != 0 {
		t.Fatalf("%d calls succeeded, %d failed otherwise (%d were told to wait)", ok.Load(), other.Load(), busy.Load())
	}
	var total Counters
	perUser := map[string]int64{}
	for _, row := range r.usageRows("") {
		total.add(row.Counters)
		perUser[row.User] += row.Requests
	}
	if total.Requests != users*each || total.InputTokens != 100*users*each || total.OutputTokens != 20*users*each || total.Estimated != 0 || total.Failed != 0 {
		t.Errorf("metered: %+v, want %d requests", total, users*each)
	}
	for u := 0; u < users; u++ {
		if n := perUser[fmt.Sprintf("user%d", u)]; n != each {
			t.Errorf("user%d: %d requests metered, want %d", u, n, each)
		}
	}
	r.s.flightMu.Lock()
	left := len(r.s.flight)
	r.s.flightMu.Unlock()
	if left != 0 {
		t.Errorf("%d callers still hold slots after every call ended", left)
	}
}

// covers D226 — what costs nothing is checked before the body is read: a
// caller past its quota, or not admitted at all, never makes xbind read 32
// MiB.
func TestCheapChecksComeBeforeTheBody(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"users": map[string]any{"bob": map[string]any{"tokensPerDay": 1}}})
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"})
	r.must(200, pUser("bob"), "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false)) // fills the quota
	for what, c := range map[string]struct {
		p    auth.Principal
		want int
	}{"a caller past its quota": {pUser("bob"), 429}, "an unbound tile": {pInstance("apps/other"), 403}} {
		body := &countingReader{}
		req := httptest.NewRequest("POST", "/llm/v1/chat/completions", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.mux.ServeHTTP(rec, req.WithContext(auth.WithPrincipal(req.Context(), c.p)))
		if rec.Code != c.want || body.n != 0 {
			t.Errorf("%s: %d, after reading %d bytes of the body", what, rec.Code, body.n)
		}
	}
}

// countingReader is an endless request body that counts what is read of it.
type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

// stalledWriter is a client that stops reading: a write fails once its
// deadline is over. It records the deadlines it was given.
type stalledWriter struct {
	h         http.Header
	mu        sync.Mutex
	deadlines []time.Time
	writes    int
}

func (s *stalledWriter) Header() http.Header { return s.h }
func (s *stalledWriter) WriteHeader(int)     {}
func (s *stalledWriter) Flush()              {}
func (s *stalledWriter) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.deadlines = append(s.deadlines, t)
	s.mu.Unlock()
	return nil
}
func (s *stalledWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	n := s.writes
	s.mu.Unlock()
	if n > 1 { // the first chunk went out; then the client's window is full
		return 0, os.ErrDeadlineExceeded
	}
	return len(p), nil
}

// covers D226 — a client that stays connected but stops reading doesn't pin
// the call: every write has a deadline, a write that misses it ends the
// client's part, the upstream is cut after the grace, and no deadline is
// left on the connection.
func TestStalledClientDoesNotPinTheCall(t *testing.T) {
	up := newUpstream(t)
	up.hold = make(chan struct{})
	p := NewProxy(nil)
	p.Grace, p.WriteTimeout = 100*time.Millisecond, 50*time.Millisecond
	// an upstream that keeps talking: the proxy must give up on the client, not wait for the stream to end
	chatty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; ; i++ {
			if _, err := fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%d\"}}]}\n\n", i); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-req.Context().Done():
				close(up.canceled)
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	defer chatty.Close()
	w := &stalledWriter{h: http.Header{}}
	done := make(chan Outcome, 1)
	go func() {
		done <- p.Relay(context.Background(), w, Call{Target: Target{Name: "main", BaseURL: chatty.URL}, Path: "/v1/chat/completions", Body: []byte(`{}`), Sink: usageSink})
	}()
	select {
	case out := <-done:
		if !out.Canceled || out.HasUsage || out.Events < 2 || !out.Stream {
			t.Errorf("outcome: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a client that stopped reading pinned the call")
	}
	select {
	case <-up.canceled:
	case <-time.After(5 * time.Second):
		t.Error("the upstream call went on after its client stalled and the grace passed")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.deadlines) < 3 || !w.deadlines[len(w.deadlines)-1].IsZero() || w.deadlines[0].IsZero() {
		t.Errorf("write deadlines: %v (want one per write, and none left at the end)", w.deadlines)
	}
}

// covers D226 — an LLM key stands only as long as what it was minted under:
// its person exists, is enabled and can open its tile, and — a key minted
// under a binding — the tile is still bound. The hooks end such keys too.
func TestLLMKeyFollowsItsOwner(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	active := map[string]bool{"kim": true, "lee": true}
	reads := map[string]bool{"kim@apps/code": true, "lee@apps/agent": true}
	r.s.opt.KeyOwner = func(user, component string) bool {
		return active[user] && (component == "" || reads[user+"@"+component])
	}
	r.bound["apps/agent"] = true
	session, _, _ := r.keys.Mint(KeyGrant{User: "kim", Component: "apps/code", Label: "terminal t1"}, time.Hour)
	personal, _, _ := r.keys.Mint(KeyGrant{User: "kim"}, time.Hour)
	sandbox, _, _ := r.keys.Mint(KeyGrant{User: "lee", Component: "apps/agent", Sandbox: "sbx1", Bound: true}, time.Hour)
	owner, _, _ := r.keys.Mint(KeyGrant{User: OwnerUser}, time.Hour)
	call := func(key string) int {
		req := r.req(auth.Principal{Via: "llm"}, "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		r.mux.ServeHTTP(rec, req)
		return rec.Code
	}
	for what, key := range map[string]string{"a session's": session, "a person's": personal, "a sandbox's": sandbox, "the owner's": owner} {
		if code := call(key); code != 200 {
			t.Fatalf("%s key: %d", what, code)
		}
	}
	// kim loses the tile the session key was for: that key stops, her other one doesn't
	reads["kim@apps/code"] = false
	if a, b := call(session), call(personal); a != 401 || b != 200 {
		t.Errorf("after kim lost apps/code: session key %d, personal key %d", a, b)
	}
	// kim is disabled: every key of hers stops, at once, without anyone revoking
	active["kim"] = false
	if a, b := call(session), call(personal); a != 401 || b != 401 {
		t.Errorf("after kim was disabled: %d, %d", a, b)
	}
	if n := r.s.UserGone("kim"); n != 2 {
		t.Errorf("UserGone ended %d keys, want 2", n)
	}
	active["kim"], reads["kim@apps/code"] = true, true
	if a := call(personal); a != 401 {
		t.Errorf("a revoked key works again once its person is back: %d", a)
	}
	// the binding the sandbox key was minted under goes
	if n := r.s.BindingChanged("apps/agent"); n != 0 {
		t.Errorf("BindingChanged while still bound ended %d keys", n)
	}
	r.bound["apps/agent"] = false
	if a := call(sandbox); a != 401 {
		t.Errorf("a key minted under a binding, after the unbind: %d", a)
	}
	if n := r.s.BindingChanged("apps/agent"); n != 1 {
		t.Errorf("BindingChanged ended %d keys, want 1", n)
	}
	if a := call(owner); a != 200 {
		t.Errorf("the owner's key: %d", a)
	}
}

// covers D226 — a tile's non-primary deployment uses its binding by the
// tile's edge policy: read (the default) lists models and can't run a call,
// block is refused with the policy's text, the primary is untouched.
func TestNonPrimaryDeploymentIsClamped(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5"}})
	r.bound["apps/agent"] = true
	mode := map[string]string{"dev": EdgeRead, "locked": EdgeBlock}
	r.s.opt.Edge = func(p auth.Principal) (string, string) {
		switch m := mode[p.Deployment]; m {
		case EdgeRead:
			return m, `apps/agent's non-primary deployment "` + p.Deployment + `" reaches the LLM connector read-only (edge policy "read")`
		case EdgeBlock:
			return m, `apps/agent's non-primary deployment "` + p.Deployment + `" may not use edge slot:llm to xbin:llm: the tile's edge policy for it is "block"`
		}
		return EdgeFull, ""
	}
	in := func(dep string) auth.Principal {
		p := pInstance("apps/agent")
		p.Deployment = dep
		return p
	}
	before := up.calls()
	for _, path := range []string{"/llm/v1/chat/completions", "/llm/v1/responses"} {
		a := r.do(in("dev"), "POST", path, chatBody("gpt-5", false))
		if a.code != 403 || a.errCode(t) != "xbin_llm_edge" || !strings.Contains(string(a.body), `read-only (edge policy \"read\")`) {
			t.Errorf("a read-clamped deployment's call on %s: %d %s", path, a.code, a.body)
		}
	}
	if up.calls() != before || len(r.usageRows("")) != 0 {
		t.Error("a read-clamped deployment's call reached the upstream or the meter")
	}
	for _, path := range []string{"/llm/v1/models", "/llm/preferred", "/llm"} {
		if a := r.do(in("dev"), "GET", path, nil); a.code != 200 {
			t.Errorf("a read-clamped deployment's GET %s: %d %s", path, a.code, a.body)
		}
	}
	if st := r.must(200, in("dev"), "GET", "/llm", nil).json(t); st["callable"] != false || st["code"] != "xbin_llm_edge" {
		t.Errorf("a read-clamped deployment's status: %v", st)
	}
	for _, c := range []struct{ method, path string }{{"POST", "/llm/v1/chat/completions"}, {"GET", "/llm/v1/models"}, {"GET", "/llm/preferred"}} {
		a := r.do(in("locked"), c.method, c.path, chatBody("gpt-5", false))
		if a.code != 403 || a.errCode(t) != "xbin_llm_edge" || !strings.Contains(string(a.body), "block") {
			t.Errorf("a blocked deployment's %s %s: %d %s", c.method, c.path, a.code, a.body)
		}
	}
	if a := r.do(in(""), "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false)); a.code != 200 {
		t.Errorf("the primary: %d %s", a.code, a.body)
	}
}

// covers D226 — the host's default quota is a ceiling: an admin's per-user
// and per-org entries narrow it, never widen it, and the host need not lock
// the users part for that. The host's own entries are the host's to give.
func TestHostQuotaIsACeiling(t *testing.T) {
	ceiling := &Quota{TokensPerDay: 1000, CostPerMonth: 10}
	admin := QuotaRules{Default: ceiling, ceiling: ceiling,
		Users: map[string]Quota{"wide": {TokensPerDay: 5000}, "free": {TokensPerDay: Unlimited, CostPerMonth: Unlimited}, "narrow": {TokensPerDay: 200, Action: "warn"}},
		Orgs:  map[string]Quota{"eng": {TokensPerDay: 9000}, "interns": {TokensPerDay: 100}, "labs": {TokensPerDay: Unlimited}}}
	show := func(limits []Limit) string {
		var out []string
		for _, l := range limits {
			out = append(out, fmt.Sprintf("%s=%v/%s/%s", l.Name, l.Value, l.Source, l.Action))
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		what, user string
		orgs       []string
		rules      QuotaRules
		want       string
	}{
		{"an admin's wider user entry gives nothing", "wide", nil, admin, "tokensPerDay=1000/default/stop costPerMonth=10/default/stop"},
		{"nor does unlimited", "free", nil, admin, "tokensPerDay=1000/default/stop costPerMonth=10/default/stop"},
		{"a narrower one narrows, warning at its own value; the ceiling still stops", "narrow", nil, admin,
			"tokensPerDay=200/user/warn tokensPerDay=1000/default/stop costPerMonth=10/default/stop"},
		{"an admin's wider org entry gives nothing", "ann", []string{"eng"}, admin, "tokensPerDay=1000/default/stop costPerMonth=10/default/stop"},
		{"nor an org's unlimited", "ann", []string{"labs", "interns"}, admin, "tokensPerDay=1000/default/stop costPerMonth=10/default/stop"},
		{"a narrower org entry narrows", "ann", []string{"interns"}, admin, "tokensPerDay=100/org:interns/stop tokensPerDay=1000/default/stop costPerMonth=10/default/stop"},
		{"the host's own user entry is the host's to give", "wide", nil,
			QuotaRules{Default: ceiling, ceiling: ceiling, hostUsers: true, Users: map[string]Quota{"wide": {TokensPerDay: 5000}}}, "tokensPerDay=5000/user/stop costPerMonth=10/default/stop"},
		{"and the host's own org entry", "ann", []string{"eng"},
			QuotaRules{Default: ceiling, ceiling: ceiling, hostOrgs: true, Orgs: map[string]Quota{"eng": {TokensPerDay: Unlimited}}}, "costPerMonth=10/default/stop"},
	} {
		if got := show(c.rules.resolve(c.user, c.orgs)); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.what, got, c.want)
		}
	}

	// end to end: the host sets only the default; the admin's override is accepted, and holds nothing open
	up := newUpstream(t) // 120 tokens a call
	host := writeHost(t, t.TempDir(), `{"people": "everyone", "upstreams": {"house": {"baseURL": "`+up.URL+`", "models": ["gpt-5"]}}, "quotas": {"default": {"tokensPerDay": 100}}}`, "")
	r := newRig(t, host)
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"users": map[string]any{"bob": map[string]any{"tokensPerDay": -1}}, "orgs": map[string]any{"eng": map[string]any{"tokensPerDay": 1000000}}})
	r.orgs["bob"] = []string{"eng"}
	r.must(200, pUser("bob"), "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	a := r.do(pUser("bob"), "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false))
	if a.code != 429 || a.json(t)["error"].(map[string]any)["quota"] != "default.tokensPerDay" {
		t.Errorf("bob, whom the admin gave unlimited under the host's 100 a day: %d %s", a.code, a.body)
	}
}

// covers D226 — a model without a price: a "*" price of its upstream
// prices it; without one its calls are marked unpriced, token quotas still
// hold them, and admins get one alert while a cost quota is in force.
func TestUnpricedModelsAndCostQuotas(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t) // 100 in, 20 out
	r.addUpstream("main", up, map[string]any{"models": []string{"gpt-5", "odd", "odder"}, "prices": map[string]any{"gpt-5": map[string]any{"in": 1.0, "out": 1.0}}})
	r.addUpstream("flat", up, map[string]any{"models": []string{"anything"}, "prices": map[string]any{"*": map[string]any{"in": 2.0, "out": 4.0}}})
	call := func(model string) int {
		return r.do(pOwner, "POST", "/llm/v1/chat/completions", chatBody(model, false)).code
	}
	call("main/gpt-5")
	call("main/odd")
	call("main/odd")
	call("main/odder")
	call("flat/anything")
	by := map[string]UsageRow{}
	for _, row := range r.usageRows("") {
		by[row.Model] = row
	}
	if by["main/odd"].Unpriced != 2 || by["main/odd"].Cost != 0 || by["main/gpt-5"].Unpriced != 0 || by["main/gpt-5"].Cost == 0 {
		t.Errorf("priced and unpriced rows: %+v", by)
	}
	if got, want := by["flat/anything"], (100*2.0+20*4.0)/1e6; got.Unpriced != 0 || got.Cost < want-1e-12 || got.Cost > want+1e-12 {
		t.Errorf("a model priced by its upstream's \"*\": %+v, want cost %v", got, want)
	}
	// no cost quota: nothing to tell
	if a := r.s.Alerts(); len(a) != 0 {
		t.Errorf("alerts without a cost quota: %+v", a)
	}
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"default": map[string]any{"costPerMonth": 0.000001, "tokensPerDay": 1000}})
	alerts := r.s.Alerts()
	if len(alerts) != 1 || alerts[0].Level != "warn" || !strings.Contains(alerts[0].Message, "3 call(s)") || !strings.Contains(alerts[0].Message, "main/odd, main/odder") {
		t.Errorf("alerts with a cost quota and unpriced calls: %+v", alerts)
	}
	// the cost quota is met (the priced calls passed it): it stops everything, unpriced models included
	if code := call("main/odd"); code != 429 {
		t.Errorf("past the cost quota: %d", code)
	}
	// with only unpriced use a cost quota can't count — the token quota still stops
	other := pUser("bob")
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"})
	for i, want := range []int{200, 200, 200, 200, 200, 200, 200, 200, 200, 429} { // 9 × 120 tokens pass 1000
		if a := r.do(other, "POST", "/llm/v1/chat/completions", chatBody("main/odd", false)); a.code != want {
			t.Fatalf("bob's unpriced call %d: %d, want %d: %s", i+1, a.code, want, a.body)
		} else if want == 429 && a.json(t)["error"].(map[string]any)["quota"] != "default.tokensPerDay" {
			t.Errorf("what stopped bob: %s", a.body)
		}
	}
	// priced at last: the alert stays for this month's old calls, and goes with the cost quota
	r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"default": map[string]any{"tokensPerDay": 1000}})
	if a := r.s.Alerts(); len(a) != 0 {
		t.Errorf("alerts once no cost quota is in force: %+v", a)
	}
}

// what http.ResponseController looks for on a writer
var _ interface {
	SetWriteDeadline(time.Time) error
} = (*stalledWriter)(nil)
