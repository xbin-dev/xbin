package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// fakeWorld is a server.AgentWorld: apps/mail is Finance's (which allows
// local models only), everything else the workspace's.
type fakeWorld struct{ view agentpolicy.View }

func (fakeWorld) AgentOrgsOf(tile string) []string {
	if tile == "apps/mail" {
		return []string{"fin"}
	}
	return []string{agentpolicy.WorkspaceID}
}
func (fakeWorld) AgentModelAllowedIn(org, model string) error {
	if org == "fin" && !strings.HasPrefix(model, "local/") {
		return errors.New("Finance allows private models only")
	}
	return nil
}
func (f fakeWorld) AgentConnectionFor(p auth.Principal, orgs, tiles []string, model, whatIf string) (agentpolicy.View, *agentpolicy.Change) {
	if whatIf != "" {
		return f.view, &agentpolicy.Change{Connect: []agentpolicy.TileState{{Tile: "apps/secret", Name: "Payroll"}}}
	}
	return f.view, nil
}
func (fakeWorld) AgentScreens(p auth.Principal, hide func(string) bool) []map[string]any {
	tiles := []string{}
	for _, t := range []string{"apps/x", "apps/mail", "apps/secret"} {
		if !hide(t) {
			tiles = append(tiles, t)
		}
	}
	return []map[string]any{{"id": "default", "name": "Default", "tiles": tiles}}
}
func (fakeWorld) AgentPersonOrgs(auth.Principal) []map[string]any {
	return []map[string]any{{"id": "fin", "name": "Finance"}}
}

// An agent session is xbind's record of a conversation: every delegate
// call runs under one, the orgs' rules are asked with ITS model, its scope
// confines the call, the orgs whose tiles a call touched taint it — reads
// and writes — and the model changes only to one every tainted org allows.
func TestAgentSessions(t *testing.T) {
	w := delegateWorld(t)
	world := fakeWorld{view: agentpolicy.View{
		Tiles: []agentpolicy.TileState{
			{Tile: "apps/x", Name: "Notes", Org: "workspace", State: agentpolicy.Connected},
			{Tile: "apps/secret", Name: "Payroll", Org: "fin", State: agentpolicy.Blocked, By: "fin", Reason: "Finance allows private models only", Rule: "models"},
			{Tile: "apps/ghost", Name: "Board minutes", Org: "fin", State: agentpolicy.NoAccess},
			{Tile: "apps/mail", Name: "Mail", Org: "fin", State: agentpolicy.Hidden},
		},
		Counts: agentpolicy.Counts{Tiles: 1, NotConnected: 3, Blocked: 1, NoAccess: 1, Hidden: 1},
	}}
	w.s.AgentWorld, w.invS.AgentWorld = world, world
	bare, person := delegateCredBare("d-bob"), w.cookie("bob")
	under := func(id string) cred {
		return func(r *http.Request) { bare(r); r.Header.Set(HeaderAgentSession, id) }
	}
	open := func(c cred, body string) (delegation.AgentSession, *httptest.ResponseRecorder) {
		rec := w.do(w.full, "POST", "/api/xbin/delegate/sessions", body, c)
		var h delegation.AgentSession
		_ = json.Unmarshal(rec.Body.Bytes(), &h)
		return h, rec
	}
	get := func(id string) delegation.AgentSession {
		h, ok := w.store.AgentSession("bob", dHolder, id)
		if !ok {
			t.Fatalf("no session %s", id)
		}
		return h
	}
	refusal := func(rec *httptest.ResponseRecorder) string {
		var b struct{ Refusal, Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return b.Refusal + b.Code
	}

	// without one: nothing but opening one and the bootstrap reads
	if rec := w.do(w.inv, "GET", "/api/apps/x/notes", "", bare); rec.Code != 403 || refusal(rec) != "no-session" {
		t.Fatalf("a call without a session: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/delegate/state", "/whoami", "/docs/search?q=x"} {
		if rec := w.do(w.inv, "GET", "/api/xbin"+path, "", bare); rec.Code != 200 {
			t.Errorf("the bootstrap read %s without a session: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	// only the agent opens one; a model is "<upstream>/<model>"
	if _, rec := open(person, `{"scope":null,"model":"local/a"}`); rec.Code != 403 {
		t.Errorf("the person opened an agent session: %d", rec.Code)
	}
	for _, bad := range []string{`{"scope":null}`, `{"model":"nomodel"}`, `{"model":"a/b c"}`, `{"model":"local/a","scope":{"orgs":["a b"],"tiles":[]}}`} {
		if _, rec := open(bare, bad); rec.Code != 400 {
			t.Errorf("open %s: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	h, rec := open(bare, `{"scope":null,"model":"local/a"}`)
	if rec.Code != 200 || h.ID == "" || h.Model != "local/a" || len(h.Tainted) != 0 || !strings.Contains(rec.Body.String(), `"connection":{"counts"`) {
		t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
	}
	bob := under(h.ID)
	// another person's id, or another holder's, is no session
	if rec := w.do(w.inv, "GET", "/api/apps/x/notes", "", func(r *http.Request) { delegateCredBare("d-alice")(r); r.Header.Set(HeaderAgentSession, h.ID) }); refusal(rec) != "no-session" {
		t.Errorf("alice's agent under bob's session: %d %s", rec.Code, rec.Body.String())
	}

	// the orgs' rules are asked with the SESSION's model, whatever a header says
	w.do(w.inv, "GET", "/api/apps/x/notes", "", bob, header(HeaderAgentModel, "hosted/big"))
	if last := w.policy.last(); last.Model != "local/a" || last.Tile != "apps/x" {
		t.Fatalf("the policy was asked %+v", last)
	}
	// taint: a read…
	if got := get(h.ID).Tainted; !slices.Equal(got, []string{"workspace"}) {
		t.Fatalf("after a read of apps/x: tainted %v", got)
	}
	// …and a write, once it is allowed (a refused one touches nothing)
	if rec := w.do(w.inv, "POST", "/api/apps/mail/send", "{}", bob); rec.Code != 403 || slices.Contains(get(h.ID).Tainted, "fin") {
		t.Fatalf("an unapproved write tainted the session: %d %v", rec.Code, get(h.ID).Tainted)
	}
	if _, err := w.store.AddApproval(delegation.Approval{User: "bob", Tile: "apps/mail", Scope: delegation.ScopeTile}, delegation.GrantAlways, "session"); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(w.inv, "POST", "/api/apps/mail/send", "{}", bob); rec.Code != 200 || !slices.Equal(get(h.ID).Tainted, []string{"fin", "workspace"}) {
		t.Fatalf("after a write to apps/mail: %d tainted %v", rec.Code, get(h.ID).Tainted)
	}
	// the audit row names the session
	if rows, _ := w.store.Audit.Read("bob", time.Time{}, 5); len(rows) == 0 || rows[0].Session != h.ID {
		t.Errorf("the audit row's session: %+v", rows)
	}

	// the model: to one every tainted org allows, yes; to another, 409 model_taint
	patch := func(body string) *httptest.ResponseRecorder {
		return w.do(w.full, "PATCH", "/api/xbin/delegate/sessions/"+h.ID, body, bob)
	}
	if rec := patch(`{"model":"local/b"}`); rec.Code != 200 || get(h.ID).Model != "local/b" {
		t.Fatalf("to another private model: %d %s", rec.Code, rec.Body.String())
	}
	rec = patch(`{"model":"hosted/big"}`)
	var taint struct {
		Code   string
		Orgs   []string
		Reason string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &taint)
	if rec.Code != 409 || taint.Code != "model_taint" || !slices.Equal(taint.Orgs, []string{"fin"}) || taint.Reason != "Finance allows private models only" || get(h.ID).Model != "local/b" {
		t.Fatalf("to a hosted model, tainted by Finance: %d %s (model %s)", rec.Code, rec.Body.String(), get(h.ID).Model)
	}
	// a session that never touched Finance may be on it — and a new one is the way forward
	fresh, _ := open(bare, `{"scope":{"orgs":["workspace"],"tiles":[]},"model":"local/a"}`)
	if rec := w.do(w.full, "PATCH", "/api/xbin/delegate/sessions/"+fresh.ID, `{"model":"hosted/big"}`, under(fresh.ID)); rec.Code != 200 {
		t.Fatalf("an untainted session to a hosted model: %d %s", rec.Code, rec.Body.String())
	}
	// taint never shrinks, whatever a PATCH says
	if patch(`{"tainted":[]}`); len(get(h.ID).Tainted) != 2 {
		t.Fatalf("a PATCH cleared the taint: %v", get(h.ID).Tainted)
	}

	// scope: narrowing hides ("hidden by you"); the policy's refusal keeps its reason
	if rec := patch(`{"scope":{"orgs":[],"tiles":["apps/x"]}}`); rec.Code != 200 {
		t.Fatalf("narrow: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "GET", "/api/apps/mail/inbox", "", bob); rec.Code != 403 || refusal(rec) != "hidden" || !strings.Contains(rec.Body.String(), "hidden by you") {
		t.Fatalf("a tile outside the scope: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "GET", "/api/apps/x/notes", "", bob); rec.Code != 200 {
		t.Fatalf("a tile inside the scope: %d", rec.Code)
	}
	if !w.invS.DelegateHides(auth.Principal{UserID: "bob", Agent: dHolder, Via: "delegate", AgentSession: h.ID, AgentModel: "local/b"}, "apps/mail") {
		t.Error("a listing shows a tile outside the scope")
	}
	if rec := patch(`{"scope":{"orgs":["fin"],"tiles":["apps/x"]}}`); rec.Code != 200 { // by org, widened again
		t.Fatalf("widen: %d", rec.Code)
	}
	if rec := w.do(w.inv, "GET", "/api/apps/mail/inbox", "", bob); rec.Code != 200 {
		t.Fatalf("a tile of an org in the scope: %d %s", rec.Code, rec.Body.String())
	}
	w.policy.set(func(d *dPolicy) { d.deny["apps/mail"] = errors.New("Finance has the workspace agent turned off") })
	if rec := w.do(w.inv, "GET", "/api/apps/mail/inbox", "", bob); refusal(rec) != "tile" || !strings.Contains(rec.Body.String(), "Finance has the workspace agent turned off") {
		t.Fatalf("blocked by policy: %d %s", rec.Code, rec.Body.String())
	}
	w.policy.set(func(d *dPolicy) { delete(d.deny, "apps/mail") })

	// the connection view an agent reads names no blocked, no-access or hidden tile
	rec = w.do(w.full, "GET", "/api/xbin/delegate/sessions/"+h.ID+"/connection?whatIf=hosted/big", "", bob)
	body := rec.Body.String()
	for _, secret := range []string{"apps/secret", "Payroll", "apps/ghost", "Board minutes", `"apps/mail"`} {
		if strings.Contains(body, secret) {
			t.Errorf("the agent's connection view names %s: %s", secret, body)
		}
	}
	if rec.Code != 200 || !strings.Contains(body, `"tile":"apps/x"`) || !strings.Contains(body, `"blocked":[{"blocked":1,"org":"fin","reason":"Finance allows private models only","rule":"models"}]`) ||
		!strings.Contains(body, `"allowed":false`) || !strings.Contains(body, `"taintedOrgs":["fin"]`) || !strings.Contains(body, `"connects":1`) {
		t.Errorf("the agent's connection view: %d %s", rec.Code, body)
	}

	// the map: within the scope, what is left out counted, never named
	rec = patch(`{"scope":{"orgs":[],"tiles":["apps/x"]}}`)
	rec = w.do(w.full, "GET", "/api/xbin/delegate/map", "", bob)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tile":"apps/x"`) || strings.Contains(rec.Body.String(), "apps/mail") || !strings.Contains(rec.Body.String(), `"hiddenByYou":1`) {
		t.Errorf("the map: %d %s", rec.Code, rec.Body.String())
	}

	// ended: gone
	if rec := w.do(w.full, "DELETE", "/api/xbin/delegate/sessions/"+h.ID, "", bob); rec.Code != 204 {
		t.Fatalf("end: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "GET", "/api/apps/x/notes", "", bob); refusal(rec) != "no-session" {
		t.Errorf("a call under an ended session: %d", rec.Code)
	}
	// bounded: the least recently used go past the cap
	for i := 0; i < delegation.MaxAgentSessions+5; i++ {
		if _, err := w.store.NewAgentSession("bob", dHolder, nil, "local/a"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(w.store.AgentSessions("bob", dHolder)); n != delegation.MaxAgentSessions {
		t.Errorf("%d sessions kept, want %d", n, delegation.MaxAgentSessions)
	}
}

// The docs on /api/: ranked, bounded, and only what is in the index.
func TestDocsSearchAndPage(t *testing.T) {
	w := delegateWorld(t)
	big := strings.Repeat("filler words go here. ", 5000)
	w.s.DocsFS = fstest.MapFS{
		"auth.md":           {Data: []byte("# Auth\n\nWho is calling.\n\n## Delegation\n\nA workspace agent acts as its person. Tickets, tickets, tickets.\n\n## Vault\n\nSecrets.\n")},
		"protocol/core.md":  {Data: []byte("# Core protocol\n\nRoutes.\n\n## Tickets\n\nOne-shot.\n")},
		"big.md":            {Data: []byte("# Big\n\n" + big)},
		"notes.txt":         {Data: []byte("# not a page: tickets")},
		"../outside/sec.md": {Data: []byte("# outside")},
	}
	bob := delegateCredBare("d-bob")
	get := func(path string, c cred) (int, map[string]any) {
		rec := w.do(w.full, "GET", "/api/xbin"+path, "", c)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := get("/docs/search?q=tickets", bob)
	res, _ := out["results"].([]any)
	if code != 200 || len(res) != 2 {
		t.Fatalf("search: %d %v", code, out)
	}
	first := res[0].(map[string]any)
	if first["path"] != "protocol/core.md" || first["section"] != "tickets" || first["heading"] != "Tickets" || first["snippet"] == "" { // a heading match outranks body matches
		t.Errorf("the first result: %v", first)
	}
	if code, out := get("/docs/search?q=tickets+delegation&limit=1", bob); code != 200 || len(out["results"].([]any)) != 1 || out["results"].([]any)[0].(map[string]any)["path"] != "auth.md" {
		t.Errorf("two words, limit 1: %d %v", code, out)
	}
	// bounds: the query, the limit, the page
	if code, _ := get("/docs/search?q="+strings.Repeat("a", docsMaxQuery+1), bob); code != 400 {
		t.Errorf("an over-long query: %d", code)
	}
	if code, out := get("/docs/search?q=filler&limit=500", bob); code != 200 || len(out["results"].([]any)) > docsMaxResults {
		t.Errorf("limit 500: %d, %d results", code, len(out["results"].([]any)))
	}
	if code, out := get("/docs/search?q=", bob); code != 200 || len(out["results"].([]any)) != 0 {
		t.Errorf("an empty query: %d %v", code, out)
	}
	code, out = get("/docs/page?path=big.md", bob)
	if md, _ := out["markdown"].(string); code != 200 || len(md) > docsMaxPage || out["truncated"] != true {
		t.Errorf("a big page: %d, %d bytes, truncated %v", code, len(md), out["truncated"])
	}
	code, out = get("/docs/page?path=auth.md&section=delegation", bob)
	if md, _ := out["markdown"].(string); code != 200 || !strings.Contains(md, "acts as its person") || strings.Contains(md, "Secrets") || out["title"] != "Auth" {
		t.Errorf("one section: %d %v", code, out)
	}
	for _, path := range []string{"/docs/page?path=notes.txt", "/docs/page?path=../outside/sec.md", "/docs/page?path=/etc/passwd", "/docs/page?path=auth.md&section=nosuch", "/docs/page?path=nosuch.md"} {
		if code, _ := get(path, bob); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
	// a person reads them too; nobody signed in doesn't
	if code, _ := get("/docs/search?q=tickets", w.cookie("bob")); code != 200 {
		t.Errorf("a person: %d", code)
	}
	if code, _ := get("/docs/search?q=tickets", func(*http.Request) {}); code == 200 {
		t.Error("nobody searched the docs")
	}
}

// A workspace agent previews as its person: one tile, under an agent
// session, inside its scope and the orgs' rules — tainting the session and
// leaving a row with the target, like any other read of the tile.
func TestDelegatePreview(t *testing.T) {
	w := delegateWorld(t)
	world := fakeWorld{}
	w.s.AgentWorld = world
	w.s.Reg = w.invS.Reg
	bare := delegateCredBare("d-bob")
	h, err := w.store.NewAgentSession("bob", dHolder, &delegation.SessionScope{Tiles: []string{"apps/x", "apps/secret"}, Orgs: []string{}}, "local/a")
	if err != nil {
		t.Fatal(err)
	}
	bob := func(r *http.Request) { bare(r); r.Header.Set(HeaderAgentSession, h.ID) }
	post := func(body string, c cred) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/preview", body, c)
		return rec.Code, rec.Body.String()
	}
	// who the preview is rendered as: the person, for one tile alone
	agent := auth.Principal{UserID: "bob", Via: "delegate", Agent: dHolder, AgentSession: h.ID}
	if got, err := previewPerson(agent, PreviewTarget{Tile: "apps/x"}); got != "bob" || err != nil {
		t.Fatalf("previewPerson for bob's agent: %q %v", got, err)
	}
	noSession := agent
	noSession.AgentSession = ""
	for name, c := range map[string]struct {
		p auth.Principal
		t PreviewTarget
	}{"a screen": {agent, PreviewTarget{Screen: "default"}}, "a tile in the shell": {agent, PreviewTarget{Tile: "apps/x", shell: true}},
		"no target": {agent, PreviewTarget{}}, "no session": {noSession, PreviewTarget{Tile: "apps/x"}}} {
		if got, err := previewPerson(c.p, c.t); got != "" || err == nil {
			t.Errorf("previewPerson, %s: %q %v", name, got, err)
		}
	}
	// through the gate: a session is needed; then the tile is judged
	if code, body := post(`{"tile":"apps/x"}`, bare); code != 403 || !strings.Contains(body, "no-session") {
		t.Errorf("without a session: %d %s", code, body)
	}
	if code, body := post(`{"tile":"apps/x"}`, bob); code != http.StatusServiceUnavailable { // allowed: it reached the renderer, which this test has none of
		t.Fatalf("a tile in scope: %d %s", code, body)
	}
	if got, _ := w.store.AgentSession("bob", dHolder, h.ID); !slices.Equal(got.Tainted, []string{"workspace"}) {
		t.Errorf("after a preview of apps/x: tainted %v", got.Tainted)
	}
	rows, _ := w.store.Audit.Read("bob", time.Time{}, 10)
	found := false
	for _, r := range rows {
		found = found || r.Class == "preview" && r.Tile == "apps/x" && r.Session == h.ID && r.Path == "/api/xbin/preview"
	}
	if !found {
		t.Errorf("no audit row names the preview's target: %+v", rows)
	}
	for name, c := range map[string][2]string{
		"outside the scope":         {`{"tile":"apps/mail"}`, "hidden by you"},
		"a tile bob can't read":     {`{"tile":"apps/secret"}`, noTileFor("bob")},
		"a tile that isn't there":   {`{"tile":"apps/nosuch"}`, noTileFor("bob")},
		"a screen":                  {`{"screen":"default"}`, "one tile, alone"},
		"the tile shown in a shell": {`{"tile":"apps/x","shell":true}`, "one tile, alone"},
	} {
		if code, body := post(c[0], bob); code != 403 || !strings.Contains(body, c[1]) {
			t.Errorf("%s: %d %s, want a 403 with %q", name, code, body, c[1])
		}
	}
	// blocked by an org's rule: its reason
	w.policy.set(func(d *dPolicy) { d.deny["apps/x"] = errors.New("Finance allows private models only") })
	if code, body := post(`{"tile":"apps/x"}`, bob); code != 403 || !strings.Contains(body, "Finance allows private models only") {
		t.Errorf("blocked by policy: %d %s", code, body)
	}
}
