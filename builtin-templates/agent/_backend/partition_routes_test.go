package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// partAgent is a test agent installed as the package's agent, with the
// route table mounted (in the mode the test set first).
func partAgent(t *testing.T) (*Agent, http.Handler) {
	t.Helper()
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	ag := newTestAgent(t, newTestDB(t))
	old := agent
	agent = ag
	t.Cleanup(func() { agent = old })
	mux := http.NewServeMux()
	routes(mux)
	return ag, mux
}

// as is a request the way xbind hands it over: headers set, nothing else.
func as(method, path, body string, hdr map[string]string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// alicesFrame: alice through her own partition's frame (the tile acting for
// her, admin role).
func alicesFrame(level string) map[string]string {
	return map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": "admin", "X-XBin-User": "alice",
		"X-XBin-User-Level": level, "X-XBin-Partition": "user:alice", "X-XBin-Partition-Id": "u-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
}

// stubGlobal stands in for the global instance a person's partition calls.
type stubGlobal struct {
	mu    sync.Mutex
	calls []string
	reply func(method, path string, body []byte) (int, string)
}

func stubGlobalCalls(t *testing.T, reply func(method, path string, body []byte) (int, string)) *stubGlobal {
	t.Helper()
	s := &stubGlobal{reply: reply}
	old := callGlobal
	callGlobal = func(_ context.Context, method, path string, body []byte, _ string) (gwResp, error) {
		s.mu.Lock()
		s.calls = append(s.calls, method+" "+path+" "+string(body))
		s.mu.Unlock()
		st, b := 200, `{"ok":true}`
		if s.reply != nil {
			st, b = s.reply(method, path, body)
		}
		return gwResp{Status: st, Type: "application/json", Body: []byte(b)}, nil
	}
	t.Cleanup(func() { callGlobal = old })
	return s
}

func (s *stubGlobal) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// TestUserModeRoutes: in a person's partition a manager's settings writes go
// to the global instance (and conf is read afresh), the person's own skill
// is served here and a shared one there, and sharing and channels answer 409.
func TestUserModeRoutes(t *testing.T) {
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	confIn = newConfReader(kv, nil)
	ag, h := partAgent(t)
	g := stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
		if path == "/config" {
			_ = kv.Put(context.Background(), confSettingsKey, []byte(`{"config":`+strconvQuote(string(body))+`}`))
		}
		return 200, string(body)
	})
	do := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	confIn.setting("config") // cached: empty
	if rec := do(as("PUT", "/config", `{"model":"fake/two"}`, alicesFrame("write"))); rec.Code != 200 {
		t.Fatalf("PUT /config: %d %s", rec.Code, rec.Body)
	}
	if c := g.got(); len(c) != 1 || c[0] != `PUT /config {"model":"fake/two"}` {
		t.Fatalf("forwarded: %v", c)
	}
	if parseConfig(ag.db.getSetting("config")).Model != "fake/two" {
		t.Fatal("conf wasn't read afresh after the forwarded write")
	}
	if rec := do(as("PUT", "/config", `{}`, alicesFrame("read"))); rec.Code != 403 {
		t.Fatalf("a reader's PUT /config: %d (the partition checks too)", rec.Code)
	}
	if len(g.got()) != 1 {
		t.Fatal("a reader's write was forwarded")
	}

	// skills: alice's own here, a shared one at global
	_ = ag.db.upsertSkill(&Skill{Name: "mine", Content: "a", Owner: "alice"})
	if rec := do(as("PUT", "/skills", `{"name":"mine","content":"b"}`, alicesFrame("write"))); rec.Code != 200 {
		t.Fatalf("PUT her own skill: %d %s", rec.Code, rec.Body)
	}
	if s, _ := ag.db.localSkill("mine"); s == nil || s.Content != "b" {
		t.Fatalf("her skill here: %+v", s)
	}
	do(as("PUT", "/skills", `{"name":"team-howto","content":"c"}`, alicesFrame("write")))
	do(as("DELETE", "/skills/other", "", alicesFrame("write")))
	c := g.got()
	if len(c) != 3 || !strings.HasPrefix(c[1], "PUT /skills ") || c[2] != "DELETE /skills/other " {
		t.Fatalf("shared skills forwarded: %v", c)
	}
	if _, err := ag.db.localSkill("team-howto"); err == nil {
		t.Fatal("a shared skill was saved in the partition")
	}

	// sharing and channels
	run, _ := ag.db.createRun("x", "", 0)
	_, _ = ag.db.q.Exec(`UPDATE runs SET owner='alice', visibility='private' WHERE id=?`, run)
	for _, r := range []*http.Request{
		as("POST", "/runs/"+itoa(run)+"/members", `{"user":"bob"}`, alicesFrame("write")),
		as("POST", "/runs/"+itoa(run)+"/links", `{}`, alicesFrame("write")),
		as("POST", "/triggers", `{"name":"x"}`, alicesFrame("write")),
		as("POST", "/channels/1/claim", `{}`, alicesFrame("write")),
	} {
		if rec := do(r); rec.Code != http.StatusConflict {
			t.Errorf("%s %s: %d %s", r.Method, r.URL.Path, rec.Code, rec.Body)
		}
	}
	if rec := do(as("GET", "/me", "", alicesFrame("read"))); !strings.Contains(rec.Body.String(), `"partition":"user:alice"`) {
		t.Fatalf("GET /me: %s", rec.Body)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// TestUserModeHalt: the brake is read from conf; a reader is refused while
// it is on, a manager's request takes it off at the global instance.
func TestUserModeHalt(t *testing.T) {
	setMode(t, modeUser, "alice")
	shorten(t, &confTTL, 0)
	kv := newMemKV()
	_ = kv.Put(context.Background(), confSettingsKey, []byte(`{"halt":"1"}`))
	confIn = newConfReader(kv, nil)
	_, h := partAgent(t)
	g := stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
		if path == "/halt" {
			_ = kv.Put(context.Background(), confSettingsKey, []byte(`{"halt":""}`))
		}
		return 200, `{"on":false}`
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/ask", `{"text":"hi"}`, alicesFrame("read")))
	if rec.Code != http.StatusLocked {
		t.Fatalf("a reader's ask while halted: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/ask", `{"text":"hi"}`, alicesFrame("write")))
	if rec.Code/100 != 2 {
		t.Fatalf("a manager's ask while halted: %d %s", rec.Code, rec.Body)
	}
	if c := g.got(); len(c) != 1 || c[0] != `PUT /halt {"on":false}` {
		t.Fatalf("the halt came off at: %v", c)
	}
}

// TestGlobalTakesPartitionCalls: the global instance takes a person's call
// from their partition — attributed, role clamped to reader/writer — as that
// person with their level; never as the tile itself.
func TestGlobalTakesPartitionCalls(t *testing.T) {
	setMode(t, modeGlobal, "")
	_, h := partAgent(t)
	f5 := func(user, level, role string) map[string]string {
		return map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": role, "X-XBin-User": user,
			"X-XBin-User-Level": level, "X-XBin-Partition": "user:" + user, "X-XBin-Partition-Id": "u-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	}
	var me map[string]any
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as("GET", "/me", "", f5("bob", "read", "reader")))
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &me) != nil || me["kind"] != "user" || me["user"] != "bob" ||
		me["manager"] != false || me["partition"] != "global" {
		t.Fatalf("bob's call from his partition: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("PUT", "/config", `{}`, f5("bob", "read", "reader")))
	if rec.Code != 403 {
		t.Fatalf("a reader changed the settings at global: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("PUT", "/config", `{"model":"m"}`, f5("carol", "write", "writer")))
	if rec.Code != 200 {
		t.Fatalf("a manager's settings through her partition: %d %s", rec.Code, rec.Body)
	}
	// the tile's own path with a person's partition and no person: nobody
	hdr := f5("", "", "admin")
	delete(hdr, "X-XBin-User")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("GET", "/me", "", hdr))
	if rec.Code != 403 {
		t.Fatalf("a partition's call without its person read as %d %s", rec.Code, rec.Body)
	}
	// another tile with the writer role: refused, as ever
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("GET", "/me", "", map[string]string{"X-XBin-From": "apps/other", "X-XBin-Role": "writer"}))
	if rec.Code != 403 {
		t.Fatalf("another tile's writer call: %d", rec.Code)
	}
}

// TestOldManagerDegrade: in a person's partition a manager whose hello lacks
// `partitions` isn't used — every path says why, naming it; global and an
// unpartitioned agent keep using it.
func TestOldManagerDegrade(t *testing.T) {
	ms := bindSbx(t, "apps/old", "apps/new")
	ms["apps/old"].Caps = []string{"exec", "files", "tar", "snapshots", "clone", "archive", "ports"}
	ctx := context.Background()
	old, _ := boundManager("apps/old")
	if _, err := managerHello(ctx, old); err != nil {
		t.Fatalf("legacy: %v", err)
	}
	setMode(t, modeGlobal, "")
	forgetHellos()
	if _, err := managerHello(ctx, old); err != nil {
		t.Fatalf("global: %v", err)
	}
	setMode(t, modeUser, "alice")
	forgetHellos()
	invalidateSandboxCatalog()
	_, err := managerHello(ctx, old)
	if sbxRefusal(err) != "partitions" || !strings.Contains(err.Error(), "apps/old") || !strings.Contains(err.Error(), `"partitions"`) ||
		!strings.Contains(err.Error(), "bx template updates") {
		t.Fatalf("an old manager in a person's partition: %v", err)
	}
	if sbxStatus("partitions") != http.StatusConflict {
		t.Fatal("the refusal's status")
	}
	cat := sandboxCatalog(ctx)
	if len(cat.Managers) != 2 || cat.Managers[0].OK || cat.Managers[0].Refusal != "partitions" || !cat.Managers[1].OK {
		t.Fatalf("the catalog: %+v", cat.Managers)
	}
	if ms["apps/old"].count("GET", "/sbx/sandboxes") != 0 {
		t.Fatal("the old manager was asked for its sandboxes from a person's partition")
	}
	if _, err := sbxDial("apps/old", "alice"); err != nil {
		t.Fatal(err) // dialing is fine; every use goes through hello
	}
}

// TestPartitionSandboxes: a partitioned agent labels its sandboxes with
// their home, and a person's conversation never binds a sandbox that isn't
// homed in their partition.
func TestPartitionSandboxes(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	ag := newTestAgent(t, newTestDB(t))
	old := agent
	agent = ag
	t.Cleanup(func() { agent = old })
	root, _ := ag.db.createRun("x", "", 0)
	label := func() map[string]string {
		var req sbxCreate
		forConversation(&req, who{kind: whoUser, user: "alice"}, root)
		return req.Labels
	}
	if l := label(); l["xbin.agent/home"] != "" || l["xbin.agent/conversation"] == "" {
		t.Fatalf("legacy labels: %v", l)
	}
	setMode(t, modeGlobal, "")
	if l := label(); l["xbin.agent/home"] != "global" {
		t.Fatalf("global's: %v", l)
	}
	setMode(t, modeUser, "alice")
	pidMu.Lock()
	pidSeen = "u-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pidMu.Unlock()
	if l := label(); l["xbin.agent/home"] != "u-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("alice's: %v", l)
	}
	if why := partitionBoxRefusal(&sbxSandbox{Name: "team-box", Shared: true}); !strings.Contains(why, "team-box") {
		t.Fatalf("a shared sandbox in a person's partition: %q", why)
	}
	if why := partitionBoxRefusal(&sbxSandbox{Name: "mine"}); why != "" {
		t.Fatalf("her own sandbox: %q", why)
	}
	setMode(t, modeLegacy, "")
	if why := partitionBoxRefusal(&sbxSandbox{Name: "team-box", Shared: true}); why != "" {
		t.Fatalf("legacy refuses a shared sandbox: %q", why)
	}
}

// fakeMail is a drop box in memory.
type fakeMail struct {
	mu    sync.Mutex
	items []mailItem
	acked []string
}

func (f *fakeMail) Inbox(_ context.Context, after string, limit int) ([]mailItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []mailItem
	for _, it := range f.items {
		if it.ID > after && len(out) < limit {
			out = append(out, it)
		}
	}
	return out, nil
}

func (f *fakeMail) Ack(_ context.Context, ids ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, ids...)
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	var keep []mailItem
	for _, it := range f.items {
		if !gone[it.ID] {
			keep = append(keep, it)
		}
	}
	f.items = keep
	return nil
}

// TestMailboxSkeleton: the doorbell pulls, hands each item to its topic's
// handler once (mail_seen), acks it, and leaves what no handler knows.
func TestMailboxSkeleton(t *testing.T) {
	ag, h := partAgent(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{}`, map[string]string{"X-XBin-From": "xbin/mail", "X-XBin-Role": "writer"}))
	if rec.Code != 404 {
		t.Fatalf("an unpartitioned agent's mailbox: %d", rec.Code)
	}
	setMode(t, modeUser, "alice")
	fm := &fakeMail{items: []mailItem{
		{ID: "001", From: "global", Topic: "test/hello", Data: json.RawMessage(`{"n":1}`)},
		{ID: "002", From: "global", Topic: "later/unknown"},
		{ID: "003", From: "global", Topic: "test/hello", Data: json.RawMessage(`{"n":3}`)},
	}}
	old := partitionMail
	partitionMail = fm
	t.Cleanup(func() { partitionMail = old; delete(mailHandlers, "test/hello") })
	var seen []string
	mailHandlers["test/hello"] = func(_ context.Context, tx *DB, it mailItem) error {
		seen = append(seen, it.ID)
		return tx.putSetting("mail_test_"+it.ID, string(it.Data))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{"partition":"user:alice","pending":3}`, map[string]string{"X-XBin-From": "xbin/mail", "X-XBin-Role": "writer", "X-XBin-Partition": "user:alice"}))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"handled":2`) || !strings.Contains(rec.Body.String(), `"left":1`) {
		t.Fatalf("the doorbell: %d %s", rec.Code, rec.Body)
	}
	if strings.Join(seen, ",") != "001,003" || strings.Join(fm.acked, ",") != "001,003" || ag.db.getSetting("mail_test_003") != `{"n":3}` {
		t.Fatalf("handled %v, acked %v", seen, fm.acked)
	}
	// delivered again (an ack lost): acked, not applied twice
	fm.items = append(fm.items, mailItem{ID: "001", From: "global", Topic: "test/hello"})
	if _, _, err := ag.pullMail(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || len(fm.items) != 1 || fm.items[0].ID != "002" {
		t.Fatalf("a redelivery: handled %v, left %v", seen, fm.items)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, as("POST", "/mailbox", `{}`, map[string]string{"X-XBin-From": "apps/other", "X-XBin-Role": "writer"}))
	if rec.Code != 403 {
		t.Fatalf("another tile rang the doorbell: %d", rec.Code)
	}
}

// TestPersonalProviders: a person's own providers (personal binds) are
// offered in their own conversations only.
func TestPersonalProviders(t *testing.T) {
	t.Setenv("XBIN_IFACE_LLM", `[{"provider":"apps/llm-gw","url":"http://xbin/api/apps/llm-gw"},`+
		`{"provider":"users/alice/gw","url":"http://xbin/api/users/alice/gw","personal":true}]`)
	t.Setenv("XBIN_IFACE_MCP", `[{"provider":"apps/mcp","url":"http://xbin/api/apps/mcp"},`+
		`{"provider":"users/alice/mcp","url":"http://xbin/api/users/alice/mcp","personal":true}]`)
	names := func(ps []llmProvider) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Path)
		}
		return strings.Join(out, ",")
	}
	mcps := func(ctx context.Context) string {
		var out []string
		for _, s := range allMCPServersIn(ctx, Config{}) {
			out = append(out, s.Name)
		}
		return strings.Join(out, ",")
	}
	ctx := context.Background()
	if names(llmProvidersIn(ctx)) != "apps/llm-gw" || mcps(ctx) != "apps/mcp" {
		t.Fatalf("without the person: %s / %s", names(llmProvidersIn(ctx)), mcps(ctx))
	}
	own := withPersonal(ctx, true)
	if names(llmProvidersIn(own)) != "apps/llm-gw,users/alice/gw" || mcps(own) != "apps/mcp,users/alice/mcp" {
		t.Fatalf("in her conversation: %s / %s", names(llmProvidersIn(own)), mcps(own))
	}
	c := &catalog{Models: []catalogModel{{ID: "a", Provider: "apps/llm-gw"}, {ID: "b", Provider: "users/alice/gw"}},
		Providers: []catalogProvider{{Path: "apps/llm-gw"}, {Path: "users/alice/gw"}}}
	if got := catalogIn(ctx, c); len(got.Models) != 1 || len(got.Providers) != 1 || got.Models[0].ID != "a" {
		t.Fatalf("the picker without the person: %+v", got)
	}
	if got := catalogIn(own, c); len(got.Models) != 2 {
		t.Fatal("the picker in her own conversation")
	}

	// whose conversation
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	ag := newTestAgent(t, newTestDB(t))
	hers, _ := ag.db.createRun("hers", "", 0)
	_, _ = ag.db.q.Exec(`UPDATE runs SET owner='alice' WHERE id=?`, hers)
	other, _ := ag.db.createRun("other", "", 0)
	_, _ = ag.db.q.Exec(`UPDATE runs SET owner='el:apps/x' WHERE id=?`, other)
	kid, _ := ag.db.createRun("kid", "", hers)
	h, _ := ag.db.getRun(hers)
	o, _ := ag.db.getRun(other)
	k, _ := ag.db.getRun(kid)
	if personalOK(ag.personalCtx(ctx, h)) {
		t.Fatal("legacy mode offered personal providers")
	}
	setMode(t, modeUser, "alice")
	if !personalOK(ag.personalCtx(ctx, h)) || !personalOK(ag.personalCtx(ctx, k)) || personalOK(ag.personalCtx(ctx, o)) {
		t.Fatal("personal providers: her conversation and its subagent yes, another's no")
	}
	r := as("GET", "/models", "", alicesFrame("read"))
	if !personalOK(callerPersonal(r)) {
		t.Fatal("her picker")
	}
	r = as("GET", "/models", "", map[string]string{"X-XBin-From": "apps/agent", "X-XBin-User": "alice", "X-XBin-Viewed-By": "carol"})
	if personalOK(callerPersonal(r)) {
		t.Fatal("an admin viewing as her")
	}
}
