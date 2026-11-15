package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// A tile agent session started by a person's agent: with a ticket the
// person approved for exactly that start, or on their preference — and
// never with a mode or pickers of the agent's own choosing. (The create
// stops at the missing bx, after every gate: 503. The session itself runs
// in internal/term's TestAgentSessionAsDelegate.)
func TestDelegateSessionStart(t *testing.T) {
	w := delegateWorld(t)
	alice, bob, person := delegateCred("d-alice"), delegateCred("d-bob"), w.cookie("alice")
	create := func(path, body string, c ...cred) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/term/sessions"+path, body, c...)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	const plain = `{"cwd":"apps/x","kind":"agent","provider":"claude"}`
	// neither a ticket nor a preference: nothing starts
	if c, b := create("", plain, alice); c != 403 || !strings.Contains(b, `"refusal":"needs-approval"`) {
		t.Fatalf("unasked: %d %s", c, b)
	}

	// --- with a ticket ---
	const bypass = `{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"bypassPermissions","net":"internet"}`
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", bypass)
	sess, _ := req.Preview["session"].(map[string]any)
	if req.Kind != delegation.KindSession || req.Tile != "apps/x" || req.State != delegation.Pending || sess["mode"] != "bypassPermissions" ||
		sess["asks"] != false || sess["net"] != "internet" || sess["provider"] != "claude" || req.Summary != "the agent's words" || req.Body != bypass {
		t.Fatalf("the request (its card says this mode doesn't ask): %+v", req)
	}
	// pending: not a ticket yet
	if c, b := create("", bypass, alice, header(HeaderTicket, req.ID)); c != 403 || !strings.Contains(b, `"refusal":"ticket"`) {
		t.Fatalf("a pending request used as a ticket: %d %s", c, b)
	}
	// the agent can't approve its own request; nor can anyone but its person
	for name, c := range map[string]cred{"the agent": alice, "bob": w.cookie("bob"), "a tile's frame": w.loginFrame("apps/x", "alice"),
		"the holder's backend": bearer("d-alice"), "view-as": w.viewAs("dave", "alice"), "a frame of the holder no login minted": w.frame(dHolder, "alice"),
		"the agent's own page, under her login": w.loginFrame(dHolder, "alice")} {
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", "", c); rec.Code != 403 && rec.Code != 404 {
			t.Errorf("%s approved alice's request: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Pending {
		t.Fatalf("the request is %s", got.State)
	}
	w.approve(person, req.ID, "")
	// bound: another body is another start
	if c, b := create("", plain, alice, header(HeaderTicket, req.ID)); c != 403 || !strings.Contains(b, "exactly what the person saw") {
		t.Fatalf("the ticket, another body: %d %s", c, b)
	}
	// bob's agent can't use alice's ticket
	if c, _ := create("", bypass, bob, header(HeaderTicket, req.ID)); c != 403 {
		t.Fatalf("bob's agent with alice's ticket: %d", c)
	}
	if c, b := create("", bypass, alice, header(HeaderTicket, req.ID)); c != 503 || !strings.Contains(b, "bx binary") {
		t.Fatalf("the approved start, an explicit mode and all: %d %s, want every gate passed (503: no bx here)", c, b)
	}
	if c, b := create("", bypass, alice, header(HeaderTicket, req.ID)); c != 403 || !strings.Contains(b, `"refusal":"ticket"`) {
		t.Fatalf("the ticket a second time: %d %s", c, b)
	}
	// a resume: the card shows what the session AMOUNTS TO — the history
	// entry's harness and mode, not the body's — and only a session the
	// agent started can be resumed (security review, finding 1)
	seed := func(id, mode, by string) {
		dir := filepath.Join(w.s.Term.Root, "data", "agent-history", "alice", util.CompKey("apps/x"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		f := fmt.Sprintf(`{"meta":{"id":%q,"cwd":"apps/x","provider":"claude","mode":%q,"created":"2026-01-01T00:00:00Z","ended":"2026-01-01T00:01:00Z","turns":2,"acpSessionId":"c-1","loadable":true,"agent":%q},"events":[]}`, id, mode, by)
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seed("mine", "bypassPermissions", dHolder)
	seed("hers", "bypassPermissions", "")
	const resume = `{"cwd":"apps/x","kind":"agent","provider":"codex","resume":"mine","options":{"effort":"high"}}`
	rr := w.ask(alice, "POST", "/api/xbin/term/sessions", resume)
	shown, _ := rr.Preview["session"].(map[string]any)
	if shown["provider"] != "claude" || shown["mode"] != "bypassPermissions" || shown["asks"] != false || shown["resume"] != true || fmt.Sprint(shown["options"]) != "map[effort:high]" {
		t.Fatalf("a resume's card must show the history entry's harness and mode, and every option: %v", shown)
	}
	in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/term/sessions", "body": `{"cwd":"apps/x","kind":"agent","provider":"claude","resume":"hers"}`})
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice); rec.Code != 403 || !strings.Contains(rec.Body.String(), "resumes only the sessions it started") {
		t.Fatalf("asking to resume the person's own bypass session: %d %s", rec.Code, rec.Body.String())
	}
	// the history entry changes between the approval and the start: no longer what was approved
	seed("mine", "default", dHolder)
	rr2 := w.ask(alice, "POST", "/api/xbin/term/sessions", resume) // its card says: default, asks
	if sh, _ := rr2.Preview["session"].(map[string]any); sh["mode"] != "default" || sh["asks"] != true {
		t.Fatalf("the card: %v", sh)
	}
	w.approve(person, rr2.ID, "")
	seed("mine", "bypassPermissions", dHolder)
	if c, b := create("", resume, alice, header(HeaderTicket, rr2.ID)); c != 403 || !strings.Contains(b, "exactly the session its person approved") {
		t.Fatalf("a start that is no longer what the card said: %d %s", c, b)
	}

	// a deployment is never the agent's pick, approved or not
	req2 := w.ask(alice, "POST", "/api/xbin/term/sessions?deployment=dev", plain)
	w.approve(person, req2.ID, "")
	if c, b := create("?deployment=dev", plain, alice, header(HeaderTicket, req2.ID)); c != 403 || !strings.Contains(b, "default deployment") {
		t.Fatalf("an approved start on a deployment: %d %s", c, b)
	}
	// a standing approval never covers a start
	req3 := w.ask(alice, "POST", "/api/xbin/term/sessions", plain)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req3.ID+"/approve", `{"grant":"always"}`, person); rec.Code != 400 {
		t.Fatalf("always, for a session start: %d %s", rec.Code, rec.Body.String())
	}
	// bob holds no terminal level on apps/x: his agent can't even ask
	in, _ = json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/term/sessions", "body": plain})
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), "terminal access") {
		t.Fatalf("bob's agent asks: %d %s", rec.Code, rec.Body.String())
	}
	// the tile's org keeps agents out: no asking either
	w.policy.set(func(d *dPolicy) { d.deny["apps/x"] = errors.New("Finance has switched the workspace agent off") })
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice); rec.Code != 403 || !strings.Contains(rec.Body.String(), "Finance") {
		t.Fatalf("asking on a tile whose org refuses: %d %s", rec.Code, rec.Body.String())
	}
	w.policy.set(func(d *dPolicy) { delete(d.deny, "apps/x") })

	// --- on the person's preference ---
	w.policy.set(func(d *dPolicy) { d.prefs["alice"] = delegation.StartPrefs{} })
	if c, b := create("", plain, alice); c != 503 || !strings.Contains(b, "bx binary") {
		t.Fatalf("on a preference: %d %s", c, b)
	}
	if c, b := create("", `{"cwd":"apps/x","kind":"agent","provider":"claude","model":"sonnet","name":"n"}`, alice); c != 503 {
		t.Fatalf("on a preference, a model and a name: %d %s", c, b)
	}
	for _, body := range []string{
		`{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"bypassPermissions"}`,
		`{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"default"}`,
		`{"cwd":"apps/x","kind":"agent","provider":"claude","net":"internet"}`,
		`{"cwd":"apps/x","kind":"agent","provider":"claude","api":true}`,
		`{"cwd":"apps/x","kind":"agent","provider":"claude","options":{"mode":"bypassPermissions"}}`,
		`{"cwd":"apps/x","kind":"agent","provider":"claude","vm":true}`,
	} {
		if c, b := create("", body, alice); c != 403 || !strings.Contains(b, "workspace agent") {
			t.Errorf("on a preference, %s: %d %s", body, c, b)
		}
		if c, b := create("", body, person); c != 503 { // alice herself picks freely
			t.Errorf("alice %s: %d %s", body, c, b)
		}
	}
	if c, b := create("?deployment=dev", plain, alice); c != 403 || !strings.Contains(b, "default deployment") {
		t.Errorf("on a preference, a deployment: %d %s", c, b)
	}
	// the person's own gates still decide: bob has no terminal level
	w.policy.set(func(d *dPolicy) { d.prefs["bob"] = delegation.StartPrefs{} })
	if c, b := create("", plain, bob); c != 403 || !strings.Contains(b, "terminal access") {
		t.Errorf("bob's agent: %d %s", c, b)
	}
	// never a shell
	if c, _ := create("", `{"cwd":"apps/x","kind":"shell"}`, alice); c != 400 {
		t.Errorf("a shell: %d", c)
	}
	// the audit rows say how each start was authorised
	rows, _ := w.store.Audit.Read("alice", time.Time{}, 0)
	var ticketed, preferred, refused int
	for _, c := range rows {
		if c.Method == "POST" && c.Path == "/api/xbin/term/sessions" {
			switch {
			case c.Ticket == req.ID && c.Status == 503:
				ticketed++
			case c.Under == "preference" && c.Tile == "apps/x":
				preferred++
			case c.Refused != "":
				refused++
			}
		}
	}
	if ticketed != 1 || preferred < 2 || refused < 3 {
		t.Errorf("audit: %d ticketed, %d on a preference, %d refused", ticketed, preferred, refused)
	}
}

// Tile API calls through the gate: reads at the person's level, writes
// only with the person's approval — a ticket bound to the call, or a
// standing approval — the dry run, and the audit rows.
func TestDelegateTileCalls(t *testing.T) {
	w := delegateWorld(t)
	bob, person := delegateCred("d-bob"), w.cookie("bob")

	// a read: free, as the person, on the model of its agent session —
	// never the one a header says
	rec := w.do(w.inv, "GET", "/api/apps/x/notes?q=secret", "", bob, header(HeaderAgentModel, "private-1"))
	calls := w.tileCalls()
	if rec.Code != 200 || rec.Header().Get("X-Ran") != "tile" || len(calls) != 1 {
		t.Fatalf("the tile read: %d ran %q (%d calls)", rec.Code, rec.Header().Get("X-Ran"), len(calls))
	}
	if p := calls[0].p; p.UserID != "bob" || p.Via != "delegate" || p.Agent != dHolder || p.Component != "" || p.IsAdmin() || p.User == nil || p.AgentModel != "local/test" || p.AgentSession == "" {
		t.Fatalf("the proxy saw %+v", p)
	}
	if last := w.policy.last(); last != (delegation.Subject{Holder: dHolder, User: "bob", Tile: "apps/x", Model: "local/test"}) {
		t.Fatalf("the policy was asked %+v", last)
	}
	// a model header that isn't one is dropped, not passed on
	w.do(w.inv, "GET", "/api/apps/x/notes", "", bob, header(HeaderAgentModel, "a b\tc"))
	if p := w.tileCalls()[1].p; p.AgentModel != "local/test" {
		t.Fatalf("a model header reached the principal: %q", p.AgentModel)
	}

	// a write: needs the person's approval
	const send = `{"to":"x@example.com","text":"hi"}`
	if rec := w.do(w.inv, "POST", "/api/apps/mail/send", send, bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"needs-approval"`) || len(w.tileCalls()) != 2 {
		t.Fatalf("an unapproved write: %d %s", rec.Code, rec.Body.String())
	}
	// asking for something that needs no approval, or isn't delegated, is refused
	for _, c := range [][2]string{{"GET", "/api/apps/mail/inbox"}, {"POST", "/api/xbin/grants"}, {"POST", "/api/xbin/term/sessions/x/prompt"},
		{"POST", "/c/apps/x/"}, {"POST", "http://evil.example/api/apps/mail/send"}, {"POST", "/api/xbin/delegate/requests"}} {
		in, _ := json.Marshal(map[string]string{"method": c[0], "path": c[1]})
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), bob); rec.Code != 400 {
			t.Errorf("asking for %s %s: %d %s, want 400", c[0], c[1], rec.Code, rec.Body.String())
		}
	}
	// a person can't make a request for their agent, nor a tile
	in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/apps/mail/send", "body": send})
	for name, c := range map[string]cred{"the person": person, "the page": w.loginFrame(dHolder, "bob"), "the holder's backend": bearer("d-bob")} {
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), c); rec.Code != 403 {
			t.Errorf("%s made a request: %d", name, rec.Code)
		}
	}

	req := w.ask(bob, "POST", "/api/apps/mail/send?draft=1", send)
	if req.Kind != delegation.KindCall || req.Tile != "apps/mail" || req.Preview["role"] != "writer" || req.Target != "/api/apps/mail/send?draft=1" || req.Body != send {
		t.Fatalf("the request: %+v", req)
	}
	w.approve(person, req.ID, "once")
	for name, c := range map[string][3]string{
		"another body":   {"POST", "/api/apps/mail/send?draft=1", `{"to":"evil@example.com","text":"hi"}`},
		"another query":  {"POST", "/api/apps/mail/send?draft=0", send},
		"another path":   {"POST", "/api/apps/mail/delete?draft=1", send},
		"another method": {"PUT", "/api/apps/mail/send?draft=1", send},
	} {
		if rec := w.do(w.inv, c[0], c[1], c[2], bob, header(HeaderTicket, req.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"ticket"`) {
			t.Errorf("the ticket, %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := w.do(w.inv, "POST", "/api/apps/mail/send?draft=1", send, delegateCred("d-alice"), header(HeaderTicket, req.ID)); rec.Code != 403 {
		t.Errorf("alice's agent with bob's ticket: %d", rec.Code)
	}
	if n := len(w.tileCalls()); n != 2 {
		t.Fatalf("%d calls reached the tile before the ticket matched", n)
	}
	if rec := w.do(w.inv, "POST", "/api/apps/mail/send?draft=1", send, bob, header(HeaderTicket, req.ID)); rec.Code != 200 {
		t.Fatalf("the approved write: %d %s", rec.Code, rec.Body.String())
	}
	if c := w.tileCalls()[2]; c.method != "POST" || c.body != send || c.p.UserID != "bob" {
		t.Fatalf("the tile got %+v", c)
	}
	if rec := w.do(w.inv, "POST", "/api/apps/mail/send?draft=1", send, bob, header(HeaderTicket, req.ID)); rec.Code != 403 {
		t.Fatalf("the ticket twice: %d", rec.Code)
	}

	// a standing approval is of THIS CALL by default — the method and the
	// path, whatever the body — and of nothing else on the tile (review 5:
	// "always for this tile" let the agent call every route of the tile)
	req = w.ask(bob, "POST", "/api/apps/mail/send?draft=1", send)
	if st, _ := req.Preview["standing"].(map[string]any); !strings.Contains(fmt.Sprint(st["tile"]), "EVERY write to apps/mail") || !strings.Contains(fmt.Sprint(st["call"]), "POST /api/apps/mail/send") {
		t.Fatalf("the card says what each standing choice is: %v", req.Preview["standing"])
	}
	w.approve(person, req.ID, "hour")
	if rec := w.do(w.inv, "POST", "/api/apps/mail/send", `{"to":"someone else"}`, bob); rec.Code != 200 {
		t.Fatalf("the approved call again: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range [][2]string{{"DELETE", "/api/apps/mail/drafts/3"}, {"POST", "/api/apps/mail/permissions/p1"}, {"PUT", "/api/apps/mail/send"}, {"POST", "/api/apps/x/notes"}} {
		if rec := w.do(w.inv, c[0], c[1], "{}", bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"needs-approval"`) {
			t.Fatalf("%s %s under an approval of POST …/send: %d %s", c[0], c[1], rec.Code, rec.Body.String())
		}
	}
	// the wider choice is made explicitly
	req = w.ask(bob, "DELETE", "/api/apps/mail/drafts/3", "")
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"hour","scope":"all"}`, person); rec.Code != 400 {
		t.Fatalf("a scope that isn't one: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"hour","scope":"tile"}`, person); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"scope":"tile"`) {
		t.Fatalf("the tile-wide approval: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "DELETE", "/api/apps/mail/drafts/3", "", bob); rec.Code != 200 {
		t.Fatalf("under the tile-wide approval: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "POST", "/api/apps/x/notes", "{}", bob); rec.Code != 403 {
		t.Fatalf("another tile under mail's approval: %d", rec.Code)
	}
	aps, _ := w.store.Approvals("bob")
	if len(aps) != 2 {
		t.Fatalf("approvals: %+v", aps)
	}
	for _, a := range aps {
		if rec := w.do(w.full, "DELETE", "/api/xbin/delegate/approvals/"+a.ID, "", person); rec.Code != 204 {
			t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := w.do(w.inv, "DELETE", "/api/apps/mail/drafts/3", "", bob); rec.Code != 403 {
		t.Fatalf("after the revoke: %d", rec.Code)
	}

	// no socket, no path ticket, no tile that isn't one
	if rec := w.do(w.inv, "GET", "/api/apps/x/ws", "", bob, header("Upgrade", "websocket")); rec.Code != 403 || !strings.Contains(rec.Body.String(), "plain requests") {
		t.Errorf("an upgrade: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "GET", "/api/~ticket/x", "", bob); rec.Code != 403 {
		t.Errorf("a path ticket: %d", rec.Code)
	}
	if rec := w.do(w.inv, "GET", "/api/apps/nope/x", "", bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), noTileFor("bob")) || !strings.Contains(rec.Body.String(), `"refusal":"tile"`) {
		t.Errorf("no such tile: %d %s", rec.Code, rec.Body.String())
	}

	// the dry run: what the call would be; nothing runs
	n := len(w.tileCalls())
	dry := header(HeaderDryRun, "1")
	out := decode[map[string]any](t, w.do(w.inv, "POST", "/api/apps/x/send", `{}`, bob, dry))
	if out["dryRun"] != true || out["allowed"] != true || out["sideEffect"] != true || out["reversible"] != false || out["needsApproval"] != true ||
		out["tile"] != "apps/x" || out["role"] != "writer" || out["level"] != "write" || out["as"] != "bob" || out["class"] != "tile-write" {
		t.Errorf("the dry run of a write: %v", out)
	}
	if _, err := w.store.AddApproval(delegation.Approval{User: "bob", Tile: "apps/x", Scope: delegation.ScopeTile}, delegation.GrantAlways, "session"); err != nil {
		t.Fatal(err)
	}
	if out = decode[map[string]any](t, w.do(w.inv, "POST", "/api/apps/x/send", `{}`, bob, dry)); out["needsApproval"] != false || !strings.HasPrefix(out["under"].(string), "approval:") {
		t.Errorf("the dry run under a standing approval: %v", out)
	}
	w.policy.set(func(d *dPolicy) { d.deny["apps/x"] = errors.New("Finance allows private models only") })
	if out = decode[map[string]any](t, w.do(w.inv, "GET", "/api/apps/x/notes", "", bob, dry)); out["allowed"] != false || !strings.Contains(out["refusal"].(string), "Finance") || out["sideEffect"] != false {
		t.Errorf("a dry run of a call the tile's org blocks: %v", out)
	}
	// …and the call itself, the standing approval notwithstanding; xbind's own reads about that tile too
	if rec := w.do(w.inv, "POST", "/api/apps/x/send", `{}`, bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), "Finance") {
		t.Errorf("a write the tile's org blocks: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/xbin/code/tree?component=apps/x", "/api/xbin/logs?component=apps/x", "/api/xbin/tile-status?component=apps/x", "/api/xbin/components/apps/x"} {
		if rec := w.do(w.inv, "GET", path, "", bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"tile"`) || !strings.Contains(rec.Body.String(), "Finance") || rec.Header().Get("X-Ran") != "" {
			t.Errorf("%s on a tile whose org blocks agents: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	w.policy.set(func(d *dPolicy) { delete(d.deny, "apps/x") })
	// at the person's level, whatever the handler asks: a tile bob can't read
	if rec := w.do(w.inv, "GET", "/api/xbin/components/apps/secret", "", bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), noTileFor("bob")) {
		t.Errorf("a component bob can't read: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "POST", "/api/xbin/grants", `{}`, bob, dry); rec.Code != 403 { // a dry run is no way round the cap
		t.Errorf("a dry run of an undelegated route: %d", rec.Code)
	}
	if out = decode[map[string]any](t, w.do(w.inv, "POST", "/api/xbin/term/sessions", `{}`, bob, dry)); out["needsApproval"] != true || out["class"] != "act" {
		t.Errorf("a dry run of a session start: %v", out)
	}
	if len(w.tileCalls()) != n {
		t.Fatal("a dry run reached the tile")
	}

	// the audit log: every tile call, act and refusal; no query, no body
	rows, err := w.store.Audit.Read("bob", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		method, path, class string
		status              int
	}
	seen := map[key]delegation.Call{}
	for _, c := range rows {
		if c.User != "bob" || c.Time.IsZero() || strings.Contains(c.Path, "?") || strings.Contains(c.Path, "secret") && c.Tile == "" && c.Class == "tile-read" {
			t.Errorf("row %+v", c)
		}
		seen[key{c.Method, c.Path, c.Class, c.Status}] = c
	}
	for _, k := range []struct {
		key
		refused, under string
		ticket         bool
	}{
		{key{"GET", "/api/apps/x/notes", "tile-read", 200}, "", "", false},
		{key{"POST", "/api/apps/mail/send", "tile-write", 200}, "", "", true},
		{key{"POST", "/api/apps/mail/send", "tile-write", 403}, "", "", false},
		{key{"DELETE", "/api/apps/mail/drafts/3", "tile-write", 200}, "", "approval:", false},
		{key{"GET", "/api/apps/x/ws", "unlisted", 403}, "plain requests", "", false},
		{key{"POST", "/api/xbin/grants", "never", 403}, "admin API is never delegated", "", false},
		{key{"GET", "/api/xbin/code/tree", "read", 403}, "Finance", "", false},
		{key{"POST", "/api/apps/mail/send", "decision:approved", 200}, "", "", true},
	} {
		c, ok := seen[k.key]
		if !ok {
			t.Errorf("no audit row for %+v", k.key)
			continue
		}
		if k.refused != "" && !strings.Contains(c.Refused, k.refused) || k.under != "" && !strings.HasPrefix(c.Under, k.under) || k.ticket && c.Ticket == "" {
			t.Errorf("%+v: %+v", k.key, c)
		}
	}
	// the listing reads a UI makes are not rows
	w.do(w.inv, "GET", "/api/xbin/components", "", bob)
	w.do(w.inv, "GET", "/api/xbin/term/sessions", "", bob)
	after, _ := w.store.Audit.Read("bob", time.Time{}, 0)
	if len(after) != len(rows) {
		t.Errorf("listing reads wrote %d rows", len(after)-len(rows))
	}
	// …and reads of code are
	w.do(w.inv, "GET", "/api/xbin/code/file?component=apps/x&file=main.go", "", bob)
	if after, _ = w.store.Audit.Read("bob", time.Time{}, 0); len(after) != len(rows)+1 || after[0].Tile != "apps/x" || after[0].Class != "read" {
		t.Errorf("a code read: %+v", after[0])
	}
}

// The rate limit on acts, and the off switches: the person's pause — which
// cuts a call that is running — the workspace's switch, and sandboxes off.
func TestDelegateRateLimitAndOffSwitches(t *testing.T) {
	w := delegateWorld(t)
	bob, alice := delegateCred("d-bob"), delegateCred("d-alice")
	now := time.Now()
	w.invS.delegateRT.setClock(func() time.Time { return now })
	for i := 0; i < delegateActBurst; i++ {
		if rec := w.do(w.inv, "POST", "/api/xbin/term/sessions/x/cancel", "", bob); rec.Code != 200 {
			t.Fatalf("act %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := w.do(w.inv, "POST", "/api/xbin/term/sessions/x/cancel", "", bob)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"refusal":"rate-limited"`) || rec.Header().Get("X-Ran") != "" {
		t.Fatalf("past the limit: %d %s", rec.Code, rec.Body.String())
	}
	// reads and dry runs aren't acts; another person's agent has its own count
	if rec := w.do(w.inv, "GET", "/api/apps/x/notes", "", bob); rec.Code != 200 {
		t.Errorf("a read while limited: %d", rec.Code)
	}
	if rec := w.do(w.inv, "POST", "/api/xbin/term/sessions/x/cancel", "", bob, header(HeaderDryRun, "1")); rec.Code != 200 {
		t.Errorf("a dry run while limited: %d", rec.Code)
	}
	if rec := w.do(w.inv, "POST", "/api/xbin/term/sessions/x/cancel", "", alice); rec.Code != 200 {
		t.Errorf("alice's agent while bob's is limited: %d", rec.Code)
	}
	now = now.Add(delegateActRefill)
	if rec := w.do(w.inv, "POST", "/api/xbin/term/sessions/x/cancel", "", bob); rec.Code != 200 {
		t.Errorf("after the refill: %d", rec.Code)
	}

	// a call that is running when its person pauses is cut: the pause is
	// made on the server the call runs on
	started, cut := make(chan struct{}), make(chan error, 1)
	w.invS.ComponentAPI = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			cut <- r.Context().Err()
		case <-time.After(5 * time.Second):
			cut <- nil
		}
	})
	go w.do(w.inv, "GET", "/api/apps/x/stream", "", bob)
	<-started
	pause := http.HandlerFunc(w.invS.apiDelegateStatePut)
	if rec := w.do(w.invS.authed(pause), "PUT", "/api/xbin/delegate/state", `{"paused":true}`, w.cookie("bob")); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"paused":true`) {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body.String())
	}
	if err := <-cut; err == nil {
		t.Fatal("the running call outlived the pause")
	}
	for _, c := range [][2]string{{"GET", "/api/apps/x/notes"}, {"GET", "/api/xbin/components"}, {"POST", "/api/xbin/delegate/requests"}} {
		rec := w.do(w.inv, c[0], c[1], "{}", bob)
		if rec.Code != 403 || rec.Header().Get("X-Ran") != "" || !strings.Contains(rec.Body.String(), "you paused it") || !strings.Contains(rec.Body.String(), `"refusal":"paused"`) {
			t.Errorf("paused, %s %s: %d ran %q %s", c[0], c[1], rec.Code, rec.Header().Get("X-Ran"), rec.Body.String())
		}
	}
	if rec := w.do(w.inv, "GET", "/api/xbin/components", "", alice); rec.Code != 200 {
		t.Errorf("alice's agent while bob's is paused: %d", rec.Code)
	}
	if rec := w.do(w.inv, "GET", "/api/xbin/components", "", delegateCred("nope")); rec.Code != 401 {
		t.Errorf("an unknown token: %d", rec.Code)
	}
	// the agent's own page may pause — never resume: that widens what the agent does
	page := w.loginFrame(dHolder, "bob")
	for _, body := range []string{`{"paused":false}`, `{"paused":true,"adminChanges":false}`, `{"adminChanges":true}`} {
		if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", body, page); rec.Code != 403 {
			t.Fatalf("the page PUT %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", `{"paused":true}`, page); rec.Code != 200 || !w.store.State("bob").Paused {
		t.Fatalf("pause from the page: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", `{"paused":false}`, w.cookie("bob")); rec.Code != 200 || w.store.State("bob").Paused {
		t.Fatalf("resume by the person: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.inv, "GET", "/api/xbin/components", "", bob); rec.Code != 200 {
		t.Errorf("resumed: %d", rec.Code)
	}

	// the workspace's switch: an admin's; off is everyone's agent, at once
	for name, c := range map[string]cred{"bob": w.cookie("bob"), "bob's agent's page": page, "view-as": w.viewAs("alice", "bob")} {
		if rec := w.do(w.full, "PUT", "/api/xbin/delegate/workspace", `{"enabled":false}`, c); rec.Code != 403 {
			t.Errorf("%s turned the workspace's switch: %d", name, rec.Code)
		}
	}
	// …and every person with an agent hears it on their own sockets (a
	// `delegate` state event), as does the admin who turned it: the chrome
	// and the app follow the switch without a read of their own
	swCh, swCancel := w.s.Hub.Subscribe(func(e events.Event) bool { return e.Type == "delegate" })
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/workspace", `{"enabled":false}`, w.cookie("alice")); rec.Code != 200 {
		t.Fatalf("off: %d %s", rec.Code, rec.Body.String())
	}
	heard := map[string]bool{}
drain:
	for {
		select {
		case ev := <-swCh:
			if c, ok := ev.Data.(delegateChange); ok && c.Op == "state" {
				heard[c.User] = true
			}
		default:
			break drain
		}
	}
	swCancel()
	if !heard["bob"] || !heard["alice"] {
		t.Errorf("the switch's state event reached %v, want bob (who has an agent) and alice (who turned it)", heard)
	}
	for _, c := range []cred{bob, alice} {
		if rec := w.do(w.inv, "GET", "/api/xbin/components", "", c); rec.Code != 403 || !strings.Contains(rec.Body.String(), "hasn't turned workspace agents on") || !strings.Contains(rec.Body.String(), `"refusal":"workspace-off"`) {
			t.Errorf("the workspace off: %d %s", rec.Code, rec.Body.String())
		}
	}
	// without sandboxes (and not in dev) nothing delegates
	_, _ = w.store.SetWorkspace(true, "test")
	w.s.Auth.SetDelegationGate(DelegationGate(w.store, false, false))
	if rec := w.do(w.inv, "GET", "/api/xbin/components", "", bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), "without sandboxes") || !strings.Contains(rec.Body.String(), `"refusal":"no-sandbox"`) {
		t.Errorf("isolation off: %d %s", rec.Code, rec.Body.String())
	}
	w.s.Auth.SetDelegationGate(DelegationGate(w.store, false, true)) // --dev / --no-auth
	if rec := w.do(w.inv, "GET", "/api/xbin/components", "", bob); rec.Code != 200 {
		t.Errorf("isolation off under --dev: %d", rec.Code)
	}
}

// The delegation API's callers: the person, their agent's own page under
// their login, their agent — and nobody else; what each may do; the events
// and the push.
func TestDelegationAPICallers(t *testing.T) {
	w := delegateWorld(t)
	var pushes []string
	w.s.DelegatePush = func(user, kind, title, body, link, collapse string) {
		pushes = append(pushes, user+" "+kind+" "+link+" | "+title+" "+body)
	}
	bob, person, page := delegateCred("d-bob"), w.cookie("bob"), w.loginFrame(dHolder, "bob")
	get := func(path string, c ...cred) (int, string) {
		rec := w.do(w.full, "GET", "/api/xbin"+path, "", c...)
		return rec.Code, rec.Body.String()
	}
	others := map[string]cred{
		"view-as":                                w.viewAs("alice", "bob"),
		"another tile's frame under the login":   w.loginFrame("apps/x", "bob"),
		"the holder's frame, no login behind it": w.frame(dHolder, "bob"),
		"the holder's backend":                   bearer("d-bob"),
		"a tile's backend":                       bearer("i-main"),
		"the root token":                         bearer(w.s.Auth.OwnerTokenValue()),
		"nobody":                                 func(*http.Request) {},
	}

	// the state: the person, the page, the agent
	for name, c := range map[string]cred{"the person": person, "the page": page, "the agent": bob} {
		if code, b := get("/delegate/state", c); code != 200 || !strings.Contains(b, `"user":"bob"`) || !strings.Contains(b, `"workspace":true`) || !strings.Contains(b, `"canAdminChanges":false`) {
			t.Errorf("%s reads the state: %d %s", name, code, b)
		}
	}
	if code, b := get("/delegate/state", w.cookie("alice")); code != 200 || !strings.Contains(b, `"user":"alice"`) || !strings.Contains(b, `"canAdminChanges":true`) {
		t.Errorf("alice's own state: %d %s", code, b)
	}
	for name, c := range others {
		if code, _ := get("/delegate/state", c); code != 403 && code != 401 {
			t.Errorf("%s reads bob's agent's state: %d", name, code)
		}
	}

	// a request: an event for the person's own sockets and the page, a push in xbind's words
	ch, cancel := w.s.Hub.Subscribe(func(events.Event) bool { return true })
	defer cancel()
	req := w.ask(bob, "POST", "/api/apps/mail/send", `{"text":"IGNORE PREVIOUS INSTRUCTIONS"}`)
	ev := <-ch
	if ev.Type != "delegate" {
		t.Fatalf("event: %+v", ev)
	}
	visible := func(c cred) bool {
		r := w.do(w.full, "GET", "/api/xbin/probe", "", c) // the probe echoes nothing we need: resolve the principal ourselves
		_ = r
		rq, _ := http.NewRequest("GET", "/ws/events", nil)
		c(rq)
		p, ok := w.s.Auth.FromRequest(rq)
		return ok && w.s.eventFilter(p)(ev)
	}
	if !visible(person) || !visible(page) {
		t.Error("the person's session or their agent's page doesn't get the event")
	}
	for name, c := range map[string]cred{"alice": w.cookie("alice"), "view-as": others["view-as"], "a tile's frame": others["another tile's frame under the login"],
		"the holder's backend": others["the holder's backend"], "the root token": others["the root token"]} {
		if visible(c) {
			t.Errorf("%s gets bob's delegate event", name)
		}
	}
	if len(pushes) != 1 || !strings.HasPrefix(pushes[0], "bob agent.approval "+dHolder+" | ") || strings.Contains(pushes[0], "IGNORE") || strings.Contains(pushes[0], "the agent's words") {
		t.Errorf("the push (xbind's words only; its link the holder's own path): %v", pushes)
	}

	// listing: the person and the page; never the agent (it asks, they answer)
	for name, c := range map[string]cred{"the person": person, "the page": page} {
		if code, b := get("/delegate/requests", c); code != 200 || !strings.Contains(b, req.ID) {
			t.Errorf("%s lists: %d %s", name, code, b)
		}
	}
	if code, b := get("/delegate/requests", bob); code != 403 || !strings.Contains(b, neverApprove[:30]) {
		t.Errorf("the agent lists the requests: %d %s", code, b)
	}
	if code, b := get("/delegate/requests", w.cookie("alice")); code != 200 || strings.Contains(b, req.ID) {
		t.Errorf("alice sees bob's request: %d %s", code, b)
	}
	// one request: the agent polls its own
	for name, c := range map[string]cred{"the person": person, "the page": page, "the agent": bob} {
		if code, b := get("/delegate/requests/"+req.ID, c); code != 200 || !strings.Contains(b, `"state":"pending"`) {
			t.Errorf("%s reads the request: %d %s", name, code, b)
		}
	}
	if code, _ := get("/delegate/requests/"+req.ID, delegateCred("d-alice")); code != 404 {
		t.Errorf("alice's agent reads bob's request: %d", code)
	}
	// deciding: nobody else
	for name, c := range others {
		for _, act := range []string{"approve", "deny"} {
			if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/"+act, "", c); rec.Code != 403 && rec.Code != 401 {
				t.Errorf("%s %ss: %d %s", name, act, rec.Code, rec.Body.String())
			}
		}
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", "", w.cookie("alice")); rec.Code != 404 {
		t.Errorf("alice approves bob's request: %d", rec.Code)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", "", bob); rec.Code != 403 {
		t.Errorf("the agent approves itself: %d", rec.Code)
	}
	// the agent's own page shows the card and renders what the agent
	// wrote: it never approves, denies or withdraws
	for _, c := range [][2]string{{"POST", "/approve"}, {"POST", "/deny"}, {"DELETE", ""}} {
		if rec := w.do(w.full, c[0], "/api/xbin/delegate/requests/"+req.ID+c[1], `{"grant":"always"}`, page); rec.Code != 403 {
			t.Errorf("the page %s %s: %d %s", c[0], c[1], rec.Code, rec.Body.String())
		}
	}
	if got, _ := w.store.Request("bob", req.ID); got.State != delegation.Pending {
		t.Fatalf("the request after the page tried: %s", got.State)
	}
	// the person approves, with a standing approval
	rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"always"}`, person)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"decidedVia":"session"`) || !strings.Contains(rec.Body.String(), `"approval":{`) {
		t.Fatalf("the person approves: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/deny", "", person); rec.Code != 409 {
		t.Errorf("decided twice: %d", rec.Code)
	}
	// the agent withdraws what it no longer wants
	req2 := w.ask(bob, "POST", "/api/apps/x/a", "")
	if rec := w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+req2.ID, "", bob); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"withdrawn"`) {
		t.Errorf("withdraw: %d %s", rec.Code, rec.Body.String())
	}

	// standing approvals: read by all three, given and revoked by the person alone
	for name, c := range map[string]cred{"the person": person, "the page": page, "the agent": bob} {
		if code, b := get("/delegate/approvals", c); code != 200 || !strings.Contains(b, `"tile":"apps/mail"`) {
			t.Errorf("%s reads the approvals: %d %s", name, code, b)
		}
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/approvals", `{"tile":"apps/x","grant":"hour","scope":"tile"}`, bob); rec.Code != 403 {
		t.Errorf("the agent gives itself an approval: %d", rec.Code)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/approvals", `{"tile":"apps/x","grant":"hour","scope":"tile"}`, page); rec.Code != 403 {
		t.Errorf("the page gives itself one: %d %s", rec.Code, rec.Body.String())
	}
	aps, _ := w.store.Approvals("bob")
	if len(aps) != 1 {
		t.Fatalf("approvals: %+v", aps)
	}
	if rec := w.do(w.full, "DELETE", "/api/xbin/delegate/approvals/"+aps[0].ID, "", page); rec.Code != 403 {
		t.Errorf("the page revokes one: %d", rec.Code)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/approvals", `{"tile":"apps/x","grant":"hour","scope":"call","method":"post","path":"/api/apps/x/notes"}`, person); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"method":"POST"`) {
		t.Errorf("the person gives one, of a call: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/approvals", `{"tile":"apps/x","grant":"hour","scope":"tile"}`, person); rec.Code != 200 {
		t.Errorf("the person gives one, of the tile: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"tile":"apps/secret","grant":"hour","scope":"tile"}`, `{"tile":"apps/x","grant":"once","scope":"tile"}`, `{"tile":"apps/x"}`,
		`{"tile":"apps/x","grant":"always"}`, // the scope is said, never guessed
		`{"tile":"apps/x","grant":"always","scope":"call"}`, `{"tile":"apps/x","grant":"always","scope":"call","method":"GET","path":"/api/apps/x/a"}`,
		`{"tile":"apps/x","grant":"always","scope":"call","method":"POST","path":"/api/apps/mail/send"}`, `{"tile":"apps/x","grant":"always","scope":"call","method":"POST","path":"/api/apps/x/a?b=1"}`} {
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/approvals", body, person); rec.Code != 404 && rec.Code != 400 {
			t.Errorf("an approval %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	for name, c := range others {
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/approvals", `{"tile":"apps/x","grant":"always","scope":"tile"}`, c); rec.Code != 403 && rec.Code != 401 {
			t.Errorf("%s gives an approval: %d", name, rec.Code)
		}
	}

	// the switches: the page pauses; admin changes are the person's own, and an admin's
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", `{"adminChanges":true}`, person); rec.Code != 403 {
		t.Errorf("bob, no admin, opts in to admin changes: %d", rec.Code)
	}
	alicePage := w.loginFrame(dHolder, "alice")
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", `{"adminChanges":true}`, alicePage); rec.Code != 403 {
		t.Errorf("alice's agent's page opts her in: %d", rec.Code)
	}
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", `{"adminChanges":true}`, delegateCred("d-alice")); rec.Code != 403 {
		t.Errorf("alice's agent opts her in: %d", rec.Code)
	}
	if w.store.State("alice").AdminChanges {
		t.Fatal("alice was opted in")
	}
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", `{"adminChanges":true}`, w.cookie("alice")); rec.Code != 200 || !w.store.State("alice").AdminChanges {
		t.Errorf("alice opts in: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{}`, `{"paused":"yes"}`, `{"nope":true}`} {
		if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", body, person); rec.Code != 400 {
			t.Errorf("PUT state %s: %d", body, rec.Code)
		}
	}

	// the log: the person's own; an admin with ?user=; never the agent, its page, or anyone else
	if code, b := get("/delegate/audit", person); code != 200 || !strings.Contains(b, `"class":"decision:approved"`) || !strings.Contains(b, `"user":"bob"`) {
		t.Errorf("bob reads his log: %d %s", code, b)
	}
	if code, b := get("/delegate/audit?limit=1", person); code != 200 || strings.Count(b, `"ts"`) != 1 {
		t.Errorf("limit: %d %s", code, b)
	}
	if code, b := get("/delegate/audit?user=bob", w.cookie("alice")); code != 200 || !strings.Contains(b, `"decision:approved"`) {
		t.Errorf("an admin reads bob's: %d %s", code, b)
	}
	if code, _ := get("/delegate/audit?user=alice", person); code != 403 {
		t.Errorf("bob reads alice's: %d", code)
	}
	for name, c := range map[string]cred{"the agent": bob, "the page": page} {
		if code, _ := get("/delegate/audit", c); code != 403 {
			t.Errorf("%s reads the log: %d", name, code)
		}
	}
	for name, c := range others {
		if code, _ := get("/delegate/audit", c); code != 403 && code != 401 {
			t.Errorf("%s reads the log: %d", name, code)
		}
	}
	// the workspace's switch is read by admins
	if code, _ := get("/delegate/workspace", person); code != 403 {
		t.Errorf("bob reads the workspace's switch: %d", code)
	}
	if code, b := get("/delegate/workspace", w.cookie("alice")); code != 200 || !strings.Contains(b, `"enabled":true`) {
		t.Errorf("alice reads it: %d %s", code, b)
	}
	// a delegate actor is exactly a delegate: the principal an admin read is run as never passes for the person
	if _, ok := delegateActorOf(auth.Principal{UserID: "alice", Via: "delegate"}); ok {
		t.Error("a delegate principal without its agent passes for a person")
	}
}

// Developing an agent (--no-auth, Server.DelegationDev): nobody signs in,
// so the implicit owner answers as the person — for the person ?user=
// names. Without the dev mark, or with auth on, the same request is nobody
// or the root token: refused.
func TestDelegationDevOwner(t *testing.T) {
	a, err := auth.Load(t.TempDir(), true) // --no-auth
	if err != nil {
		t.Fatal(err)
	}
	store := delegation.OpenStore(t.TempDir())
	if _, err := store.SetWorkspace(true, "test"); err != nil { // nothing is allowed while workspace agents are off
		t.Fatal(err)
	}
	s := &Server{Auth: a, Delegation: store, DelegationDev: true, Hub: events.NewHub()}
	h := s.Handler()
	do := func(method, path, body string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/api/xbin"+path, strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	mk := func(user string) delegation.Request {
		r, err := store.NewRequest(delegation.Request{User: user, Agent: dHolder, Kind: delegation.KindCall, Method: "POST", Target: "/api/apps/mail/send", Tile: "apps/mail"}, []byte("{}"))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	own, bobs := mk("owner"), mk("bob")
	// the owner's own, by default
	if code, body := do("GET", "/delegate/requests", ""); code != 200 || !strings.Contains(body, own.ID) || strings.Contains(body, bobs.ID) {
		t.Fatalf("the owner's requests: %d %s", code, body)
	}
	if code, body := do("POST", "/delegate/requests/"+own.ID+"/approve", ""); code != 200 || !strings.Contains(body, `"decidedVia":"dev"`) {
		t.Fatalf("the owner approves: %d %s", code, body)
	}
	// a named person's
	if code, _ := do("POST", "/delegate/requests/"+bobs.ID+"/approve", ""); code != 404 {
		t.Fatalf("bob's request without naming bob: %d", code)
	}
	if code, body := do("POST", "/delegate/requests/"+bobs.ID+"/approve?user=bob", `{"grant":"hour"}`); code != 200 || !strings.Contains(body, `"approval":{`) {
		t.Fatalf("the owner approves for bob: %d %s", code, body)
	}
	if _, ok := store.Covers("bob", "apps/mail", "POST", "/api/apps/mail/send"); !ok {
		t.Fatal("no standing approval for bob")
	}
	if code, body := do("PUT", "/delegate/state?user=bob", `{"adminChanges":true,"paused":true}`); code != 200 || !store.State("bob").AdminChanges || !store.State("bob").Paused {
		t.Fatalf("the owner sets bob's switches: %d %s", code, body)
	}
	if code, body := do("GET", "/delegate/audit?user=bob", ""); code != 200 || !strings.Contains(body, `"decision:approved"`) {
		t.Fatalf("the owner reads bob's log: %d %s", code, body)
	}
	// without the dev mark the implicit owner is the root: names no person
	s.DelegationDev = false
	r3 := mk("owner")
	for _, c := range [][2]string{{"POST", "/delegate/requests/" + r3.ID + "/approve"}, {"GET", "/delegate/requests"}, {"GET", "/delegate/audit"}, {"POST", "/delegate/approvals"}} {
		if code, _ := do(c[0], c[1], `{"tile":"apps/mail","grant":"always"}`); code != 403 {
			t.Errorf("%s %s without the dev mark: %d", c[0], c[1], code)
		}
	}
	// with auth on the dev mark changes nothing for the root token (the others' refusals: TestDelegationAPICallers)
	w := delegateWorld(t)
	w.s.DelegationDev = true
	r4, _ := w.store.NewRequest(delegation.Request{User: "owner", Kind: delegation.KindCall, Method: "POST", Target: "/api/apps/mail/send", Tile: "apps/mail"}, nil)
	for _, c := range []cred{bearer(w.s.Auth.OwnerTokenValue()), func(*http.Request) {}} {
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r4.ID+"/approve", "", c); rec.Code != 403 && rec.Code != 401 {
			t.Errorf("with auth on: %d %s", rec.Code, rec.Body.String())
		}
	}
}

// The security review's remaining findings at the gate, each as the case
// that passed before its fix.
func TestDelegateReviewHardening(t *testing.T) {
	w := delegateWorld(t)
	bob, person := delegateCred("d-bob"), w.cookie("bob")
	const send = `{"to":"x"}`
	ticket := func(method, path, body string) string {
		t.Helper()
		req := w.ask(bob, method, path, body)
		w.approve(person, req.ID, "once")
		return req.ID
	}

	t.Run("the method judged is the method made", func(t *testing.T) {
		// a GET that says it is a DELETE is a write the gate judged as a read
		n := len(w.tileCalls())
		rec := w.do(w.inv, "GET", "/api/apps/mail/drafts/3", "", bob, header("X-HTTP-Method-Override", "DELETE"), header("X-Method-Override", "DELETE"), header("X-HTTP-Method", "DELETE"))
		calls := w.tileCalls()
		if rec.Code != 200 || len(calls) != n+1 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		if rec := w.do(w.inv, "GET", "/api/apps/mail/drafts/3?_method=DELETE", "", bob); rec.Code != 403 || len(w.tileCalls()) != n+1 {
			t.Errorf("?_method=: %d %s", rec.Code, rec.Body.String())
		}
		in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/apps/mail/send?_method=DELETE"})
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), bob); rec.Code != 400 {
			t.Errorf("asking for a call with ?_method=: %d", rec.Code)
		}
	})
	t.Run("a ticket binds the content type", func(t *testing.T) {
		id := ticket("POST", "/api/apps/mail/send", send) // approved as JSON
		form := header("Content-Type", "application/x-www-form-urlencoded")
		if rec := w.do(w.inv, "POST", "/api/apps/mail/send", send, bob, header(HeaderTicket, id), form); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"ticket"`) {
			t.Fatalf("the same bytes as a form: %d %s", rec.Code, rec.Body.String())
		}
		if rec := w.do(w.inv, "POST", "/api/apps/mail/send", send, bob, header(HeaderTicket, id), header("Content-Type", "Application/JSON")); rec.Code != 200 {
			t.Fatalf("as JSON, in another case: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("an agent in a loop grows no log", func(t *testing.T) {
		now := time.Now()
		w.invS.delegateRT.setClock(func() time.Time { return now })
		before, _ := w.store.Audit.Read("bob", time.Time{}, 1000)
		for i := 0; i < 500; i++ {
			w.do(w.inv, "POST", "/api/xbin/grants", "{}", bob)                         // refused
			w.do(w.inv, "GET", "/api/apps/x/"+strings.Repeat("a", 2000), "", bob)      // a tile read, a long path
			w.do(w.inv, "POST", "/api/apps/x/a", "{}", bob, header(HeaderDryRun, "1")) // a dry run
		}
		after, _ := w.store.Audit.Read("bob", time.Time{}, 1000)
		if n := len(after) - len(before); n > delegateNoiseBurst {
			t.Fatalf("1500 refusals, reads and dry runs wrote %d rows, want at most %d", n, delegateNoiseBurst)
		}
		for _, c := range after {
			if len(c.Path) > delegation.MaxPath+4 {
				t.Fatalf("a row's path is %d bytes", len(c.Path))
			}
		}
		now = now.Add(time.Minute)
		w.do(w.inv, "POST", "/api/xbin/grants", "{}", bob)
		if last, _ := w.store.Audit.Read("bob", time.Time{}, 1); len(last) != 1 || last[0].N < 1400 {
			t.Fatalf("the next row doesn't say how many were left out: %+v", last)
		}
		// an act that ran is always a row
		id := ticket("POST", "/api/apps/mail/send", send)
		for i := 0; i < 200; i++ {
			w.do(w.inv, "POST", "/api/xbin/grants", "{}", bob)
		}
		if rec := w.do(w.inv, "POST", "/api/apps/mail/send", send, bob, header(HeaderTicket, id)); rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if last, _ := w.store.Audit.Read("bob", time.Time{}, 1); last[0].Ticket != id {
			t.Fatalf("the act isn't the last row: %+v", last[0])
		}
		w.invS.delegateRT.setClock(nil)
	})
	t.Run("a non-primary deployment has no part in this plane", func(t *testing.T) {
		tok := w.s.Auth.MintFrameTokenDeployment(dHolder, "bob", "dev", time.Minute)
		w.s.Auth.RegisterInstanceDeployment("h-dev", dHolder, "dev")
		for name, c := range map[string]cred{"a frame of the holder's dev deployment": header(auth.FrameTokenHeader, tok), "its backend": bearer("h-dev"), "its backend as a delegate": delegateCred("h-dev")} {
			for _, path := range []string{"/delegate/state", "/delegate/requests", "/delegate/approvals", "/delegate/changesets"} {
				if rec := w.do(w.full, "GET", "/api/xbin"+path, "", c); rec.Code == 200 {
					t.Errorf("%s reads %s: %d %s", name, path, rec.Code, rec.Body.String())
				}
			}
		}
		for p, c := range RouteClasses() {
			if strings.Contains(p, " /delegate/") && c != PrimaryOnly {
				t.Errorf("%s is %s for non-primary deployments, want primary-only", p, c)
			}
		}
	})
}

// Calls in flight end, and standing approvals go, when what allowed them
// does: the person is disabled, loses the tile, revokes the approval, or
// the tile is deleted. Each change is driven in the three orders it can
// land in, explicitly — never by timing:
//
//	before the gate judges the call:  the call is refused; the tile never sees it
//	while the gate is judging it:     the gate's last check, made after the call is
//	                                  registered, refuses it (the policy's onAsk hook
//	                                  makes the change at exactly that point)
//	after the call reached the tile:  the sweep cuts it at once, the recheck within
//	                                  its interval
//
// Every wait is bounded and fails saying what it waited for. (An earlier
// version polled "some call of bob's is registered" to mean "this call
// runs": a previous call not yet unregistered satisfied it, the change then
// landed before the new call was judged, the gate — rightly — refused it,
// and the test waited without bound for a tile handler that never ran.)
func TestDelegateCallsEndWithWhatAllowedThem(t *testing.T) {
	const wait = 30 * time.Second
	w := delegateWorld(t)
	w.invS.delegateRT.setRecheck(20 * time.Millisecond) // this server's own, before its first call
	bob := delegateCred("d-bob")

	// one call, from request to answer
	type call struct {
		name    string
		started chan struct{}                   // the tile's handler has it
		cut     chan error                      // how the handler's context ended (nil: it wasn't cut)
		done    chan *httptest.ResponseRecorder // the request returned
	}
	start := func(name, method, path string) *call {
		t.Helper()
		c := &call{name: name, started: make(chan struct{}), cut: make(chan error, 1), done: make(chan *httptest.ResponseRecorder, 1)}
		w.invS.ComponentAPI = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			close(c.started)
			select {
			case <-r.Context().Done():
				c.cut <- r.Context().Err()
			case <-time.After(2 * wait):
				c.cut <- nil
			}
		})
		go func() { c.done <- w.do(w.inv, method, path, "{}", bob) }()
		return c
	}
	// running: the call reached the tile and is registered — this call, not
	// "a call"
	running := func(c *call) {
		t.Helper()
		select {
		case <-c.started:
		case rec := <-c.done:
			t.Fatalf("%s: the call never reached the tile: %d %s", c.name, rec.Code, rec.Body.String())
		case <-time.After(wait):
			t.Fatalf("%s: after %s the call neither reached the tile nor was answered (%d calls registered)", c.name, wait, len(w.invS.delegateRT.running("bob")))
		}
		if n := len(w.invS.delegateRT.running("bob")); n != 1 {
			t.Fatalf("%s: %d calls registered while it runs, want 1", c.name, n)
		}
	}
	// ended: it was cut, its request returned, and nothing is left registered
	ended := func(c *call) {
		t.Helper()
		select {
		case err := <-c.cut:
			if err == nil {
				t.Fatalf("%s: the running call outlived it", c.name)
			}
		case <-time.After(wait):
			t.Fatalf("%s: after %s the running call wasn't cut (%d calls registered)", c.name, wait, len(w.invS.delegateRT.running("bob")))
		}
		select {
		case <-c.done:
		case <-time.After(wait):
			t.Fatalf("%s: cut, but the request didn't return in %s", c.name, wait)
		}
		for deadline := time.Now().Add(wait); len(w.invS.delegateRT.running("bob")) != 0; {
			if time.Now().After(deadline) {
				t.Fatalf("%s: its call is still registered %s after it returned", c.name, wait)
			}
			time.Sleep(time.Millisecond)
		}
	}
	// refused: judged after the change — it never reaches the tile
	refused := func(name, method, path, want string) {
		t.Helper()
		c := start(name, method, path)
		select {
		case rec := <-c.done:
			if rec.Code != 403 || !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("%s: %d %s, want a 403 with %q", name, rec.Code, rec.Body.String(), want)
			}
		case <-c.started:
			t.Fatalf("%s: the call reached the tile", name)
		case <-time.After(wait):
			t.Fatalf("%s: no answer in %s", name, wait)
		}
	}
	setBob := func(u users.User) {
		t.Helper()
		u.ID = "bob"
		if _, err := w.s.Auth.Users.Upsert(u, ""); err != nil {
			t.Fatal(err)
		}
	}
	approve := func(tile string) delegation.Approval {
		t.Helper()
		ap, err := w.store.AddApproval(delegation.Approval{User: "bob", Tile: tile, Scope: delegation.ScopeTile}, delegation.GrantAlways, "session")
		if err != nil {
			t.Fatal(err)
		}
		return ap
	}
	atJudging := func(f func()) { // run f when the gate asks the tile's rules for the next call, once
		w.policy.set(func(d *dPolicy) {
			d.onAsk = func() {
				d.onAsk = nil
				f()
			}
		})
	}
	reads := map[string]string{"apps/x": users.LevelRead, "apps/mail": users.LevelRead}
	w.policy.set(func(d *dPolicy) { d.readCheck = true })

	// ---- a standing approval is revoked ----
	ap := approve("apps/mail")
	c := start("revoked while running", "POST", "/api/apps/mail/import")
	running(c)
	if _, err := w.store.RevokeApproval("bob", ap.ID); err != nil {
		t.Fatal(err)
	}
	ended(c) // no sweep: the recheck
	refused("revoked before the call", "POST", "/api/apps/mail/import", `"refusal":"needs-approval"`)
	ap = approve("apps/mail")
	atJudging(func() { _, _ = w.store.RevokeApproval("bob", ap.ID) })
	refused("revoked while the gate judged the call", "POST", "/api/apps/mail/import", "approval")

	// ---- the person loses the tile ----
	approve("apps/mail")
	approve("apps/x")
	c = start("losing the tile while running", "GET", "/api/apps/mail/stream")
	running(c)
	setBob(users.User{Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead}})
	w.invS.SweepDelegation()
	ended(c)
	if aps, _ := w.store.Approvals("bob"); len(aps) != 1 || aps[0].Tile != "apps/x" {
		t.Fatalf("approvals after bob lost apps/mail: %+v", aps)
	}
	refused("the tile lost before the call", "GET", "/api/apps/mail/stream", noTileFor("bob"))
	setBob(users.User{Role: users.RoleUser, Tiles: reads}) // access given again: the approval doesn't come back
	w.invS.SweepDelegation()
	if _, ok := w.store.Covers("bob", "apps/mail", "POST", "/api/apps/mail/send"); ok {
		t.Fatal("an approval came back with the access")
	}
	atJudging(func() { setBob(users.User{Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead}}) })
	refused("the tile lost while the gate judged the call", "GET", "/api/apps/mail/stream", "")
	setBob(users.User{Role: users.RoleUser, Tiles: reads})

	// ---- the tile is deleted: its approvals go ----
	if err := os.RemoveAll(filepath.Join(w.invS.Reg.Root, "apps", "x")); err != nil {
		t.Fatal(err)
	}
	if err := w.invS.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	w.invS.SweepDelegation()
	if aps, _ := w.store.Approvals("bob"); len(aps) != 0 {
		t.Fatalf("approvals after apps/x was deleted: %+v", aps)
	}

	// ---- the person is disabled ----
	c = start("disabled while running", "GET", "/api/apps/mail/stream")
	running(c)
	setBob(users.User{Role: users.RoleUser, Tiles: reads, Disabled: true})
	ended(c) // no sweep: the recheck
	setBob(users.User{Role: users.RoleUser, Tiles: reads})

	// ---- the person pauses ----
	c = start("paused while running", "GET", "/api/apps/mail/stream")
	running(c)
	if _, err := w.store.SetState("bob", func(st *delegation.State) { st.Paused = true }); err != nil {
		t.Fatal(err)
	}
	if n := w.invS.CutDelegateStreams("bob"); n != 1 { // what PUT /delegate/state does
		t.Fatalf("pausing cut %d calls, want 1", n)
	}
	ended(c)
	refused("paused before the call", "GET", "/api/apps/mail/inbox", "paused")
	if _, err := w.store.SetState("bob", func(st *delegation.State) { st.Paused = false }); err != nil {
		t.Fatal(err)
	}
	atJudging(func() { _, _ = w.store.SetState("bob", func(st *delegation.State) { st.Paused = true }) })
	refused("paused while the gate judged the call", "GET", "/api/apps/mail/inbox", "")
}

// The second review, at the gate: what a tile sees of a delegate's call is
// what its person approved and nothing else — the full Content-Type, an
// allowlist of request headers — and what a tile answers can't send the
// call anywhere else.
func TestDelegateTileCallIsExactlyWhatWasApproved(t *testing.T) {
	w := delegateWorld(t)
	bob, person := delegateCred("d-bob"), w.cookie("bob")
	var seen http.Header
	w.invS.ComponentAPI = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		if strings.HasSuffix(r.URL.Path, "/hop") { // "do it over there": another tile's write, a query of this tile's choosing
			rw.Header().Set("Location", "/api/apps/x/notes?to=evil")
			rw.Header().Set("Refresh", "0; url=/api/apps/x/notes")
			rw.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		rw.WriteHeader(200)
	})

	t.Run("the request headers a tile sees are a list", func(t *testing.T) {
		rec := w.do(w.inv, "GET", "/api/apps/mail/inbox", "", bob,
			header("Accept", "application/json"), header("If-None-Match", `"v1"`), header("User-Agent", "agent/1"),
			header("X-Confirm", "yes"), header("X-Admin", "1"), header("Cookie", "sid=1"), header("X-Forwarded-For", "10.0.0.1"),
			header("Origin", "https://evil.example"), header("Content-Encoding", "gzip"), header("Prefer", "handling=lenient"), header("X-Requested-With", "x"))
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		for _, h := range []string{"X-Confirm", "X-Admin", "Cookie", "X-Forwarded-For", "Origin", "Content-Encoding", "Prefer", "X-Requested-With", "Authorization", HeaderTicket, HeaderDryRun, HeaderAgentModel} {
			if v := seen.Values(h); len(v) > 0 {
				t.Errorf("the tile was sent %s: %q", h, v)
			}
		}
		for h, want := range map[string]string{"Accept": "application/json", "If-None-Match": `"v1"`, "User-Agent": "agent/1"} {
			if seen.Get(h) != want {
				t.Errorf("the tile's %s: %q, want %q", h, seen.Get(h), want)
			}
		}
		for h := range seen {
			if !delegateForwards[h] && !strings.HasPrefix(h, "X-Xbin-") {
				t.Errorf("the tile was sent %s, which isn't in delegateForwards", h)
			}
		}
	})
	t.Run("a ticket binds the whole Content-Type", func(t *testing.T) {
		const body = `{"to":"x"}`
		ask := func(ctype string) string {
			t.Helper()
			in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/apps/mail/send", "body": body, "contentType": ctype})
			rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), bob)
			var req delegation.Request
			if json.Unmarshal(rec.Body.Bytes(), &req); rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			w.approve(person, req.ID, "once")
			return req.ID
		}
		id := ask("application/json")
		for _, sent := range []string{"application/json; charset=utf-7", "application/json; profile=other", "application/json;boundary=x"} {
			if rec := w.do(w.inv, "POST", "/api/apps/mail/send", body, bob, header(HeaderTicket, id), header("Content-Type", sent)); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"ticket"`) {
				t.Errorf("approved as application/json, sent as %q: %d %s", sent, rec.Code, rec.Body.String())
			}
		}
		if rec := w.do(w.inv, "POST", "/api/apps/mail/send", body, bob, header(HeaderTicket, id), header("Content-Type", "Application/JSON")); rec.Code != 200 {
			t.Errorf("the same type, another case: %d %s", rec.Code, rec.Body.String())
		}
		id = ask("text/plain; charset=utf-8")
		if rec := w.do(w.inv, "POST", "/api/apps/mail/send", body, bob, header(HeaderTicket, id), header("Content-Type", "text/plain")); rec.Code != 403 {
			t.Errorf("approved with a charset, sent without: %d", rec.Code)
		}
		if rec := w.do(w.inv, "POST", "/api/apps/mail/send", body, bob, header(HeaderTicket, id), header("Content-Type", "text/plain;charset=utf-8")); rec.Code != 200 {
			t.Errorf("the same parameters, spaced otherwise: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a tile's redirect goes nowhere", func(t *testing.T) {
		req := w.ask(bob, "POST", "/api/apps/mail/hop", `{}`)
		w.approve(person, req.ID, "once")
		rec := w.do(w.inv, "POST", "/api/apps/mail/hop", `{}`, bob, header(HeaderTicket, req.ID))
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "" || rec.Header().Get("Refresh") != "" {
			t.Errorf("a tile's 307 to a delegate: %d Location %q Refresh %q", rec.Code, rec.Header().Get("Location"), rec.Header().Get("Refresh"))
		}
		// and the place it pointed at is judged as any request that arrives: it needs its own approval
		if rec := w.do(w.inv, "POST", "/api/apps/x/notes?to=evil", `{}`, bob, header(HeaderTicket, req.ID)); rec.Code != 403 {
			t.Errorf("the redirect's target under the first call's ticket: %d", rec.Code)
		}
	})
}

// A tile the person can't read and a tile that isn't there answer a
// delegate the same, byte for byte (the second review's N7): otherwise the
// agent maps the workspace's tiles by asking.
func TestDelegateTileRefusalIsNoOracle(t *testing.T) {
	w := delegateWorld(t)
	bob := delegateCred("d-bob")
	if _, ok := w.invS.Reg.Component("apps/secret"); !ok {
		t.Fatal("the fixture has no apps/secret")
	}
	same := func(name string, call func(tile string) *httptest.ResponseRecorder) {
		t.Helper()
		there, gone := call("apps/secret"), call("apps/nosuch")
		norm := func(rec *httptest.ResponseRecorder, tile string) string {
			return fmt.Sprint(rec.Code, " ", strings.ReplaceAll(rec.Body.String(), tile, "<tile>"))
		}
		if a, b := norm(there, "apps/secret"), norm(gone, "apps/nosuch"); a != b {
			t.Errorf("%s tells a tile bob can't read from one that isn't there:\n  %s\n  %s", name, a, b)
		}
		if there.Code/100 == 2 {
			t.Errorf("%s on a tile bob can't read: %d", name, there.Code)
		}
	}
	same("a read", func(tile string) *httptest.ResponseRecorder { return w.do(w.inv, "GET", "/api/"+tile+"/x", "", bob) })
	same("a write", func(tile string) *httptest.ResponseRecorder { return w.do(w.inv, "POST", "/api/"+tile+"/x", "{}", bob) })
	same("a dry run", func(tile string) *httptest.ResponseRecorder {
		return w.do(w.inv, "POST", "/api/"+tile+"/x", "{}", bob, header(HeaderDryRun, "1"))
	})
	same("asking for approval", func(tile string) *httptest.ResponseRecorder {
		in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/" + tile + "/x", "body": "{}"})
		return w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), bob)
	})
	same("a tile's status", func(tile string) *httptest.ResponseRecorder {
		return w.do(w.full, "GET", "/api/xbin/components/"+tile, "", bob)
	})
}

// A body the person can't read is never approved as if they had (the
// second review's N7): the request says it is binary, with its size and
// hash in xbind's words; approving it takes saying so, and the approval
// records that it was given unseen.
func TestDelegateBinaryBodyIsApprovedKnowingly(t *testing.T) {
	w := delegateWorld(t)
	bob, person := delegateCred("d-bob"), w.cookie("bob")
	for name, raw := range map[string][]byte{
		"not UTF-8":            {0xff, 0xfe, 0x00, 'P', 'K'},
		"UTF-8 with a NUL":     []byte("{\"to\":\"x\"}\x00rm -rf"),
		"UTF-8 with an escape": []byte("ok\x1b[2J\x1b[Hall fine"),
	} {
		in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/apps/mail/import", "bodyBase64": base64.StdEncoding.EncodeToString(raw), "contentType": "application/octet-stream"})
		rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), bob)
		req := decode[delegation.Request](t, rec)
		body, _ := req.Preview["body"].(map[string]any)
		if rec.Code != 200 || !req.BodyBinary || req.BodyShown || req.Body != "" || body["binary"] != true || body["size"] != float64(len(raw)) || body["sha256"] != req.BodySHA256 {
			t.Fatalf("%s: the request %d %+v (preview.body %v)", name, rec.Code, req, body)
		}
		// approving it like any other is refused, and says why
		rec = w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"once"}`, person)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"unseen":true`) || !strings.Contains(rec.Body.String(), req.BodySHA256) {
			t.Fatalf("%s: approved without saying it is unseen: %d %s", name, rec.Code, rec.Body.String())
		}
		if got, _ := w.store.Request("bob", req.ID); got.State != delegation.Pending {
			t.Fatalf("%s: the request is %s", name, got.State)
		}
		rec = w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"once","unseen":true}`, person)
		out := decode[struct{ Request delegation.Request }](t, rec)
		if rec.Code != 200 || !out.Request.ApprovedUnseen {
			t.Fatalf("%s: approved knowingly: %d %s", name, rec.Code, rec.Body.String())
		}
		if rec := w.do(w.inv, "POST", "/api/apps/mail/import", string(raw), bob, header(HeaderTicket, req.ID), header("Content-Type", "application/octet-stream")); rec.Code != 200 {
			t.Fatalf("%s: the ticketed call: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// a text body is shown, and "unseen" is nothing to say about it
	req := w.ask(bob, "POST", "/api/apps/mail/send", "{\"to\":\"x\"}\n\tok")
	if req.BodyBinary || !req.BodyShown || req.Preview["body"] != nil {
		t.Fatalf("a text body: %+v", req)
	}
	w.approve(person, req.ID, "once")
	if got, _ := w.store.Request("bob", req.ID); got.ApprovedUnseen {
		t.Fatal("a text body approved 'unseen'")
	}
}

// Requests that pile up are one notification, not twenty (N7).
func TestDelegateRequestPushesCollapse(t *testing.T) {
	w := delegateWorld(t)
	bob := delegateCred("d-bob")
	now := time.Now()
	w.s.delegateRT.setClock(func() time.Time { return now })
	type push struct{ body, collapse string }
	var pushes []push
	w.s.DelegatePush = func(user, kind, title, body, link, collapse string) { pushes = append(pushes, push{body, collapse}) }
	for i := 0; i < 5; i++ {
		w.ask(bob, "POST", "/api/apps/mail/send", fmt.Sprintf(`{"n":%d}`, i))
		now = now.Add(2 * time.Second)
	}
	if len(pushes) != 1 {
		t.Fatalf("5 requests in 10 s pushed %d times", len(pushes))
	}
	now = now.Add(2 * time.Minute)
	w.ask(bob, "POST", "/api/apps/mail/send", `{"n":6}`)
	if len(pushes) != 2 || pushes[0].collapse != pushes[1].collapse || !strings.Contains(pushes[1].body, "6 requests") {
		t.Fatalf("the later push: %+v", pushes)
	}
}
