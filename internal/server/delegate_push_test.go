package server

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// The workspace agent's notifications (D241; delegatepush.go) and the
// decisions made from them.

type pushLog struct {
	mu      sync.Mutex
	pushes  []AgentPush
	clears  []string
	changed int
}

func (l *pushLog) wire(s *Server) {
	s.AgentPush = func(user string, n AgentPush) (bool, bool) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.pushes = append(l.pushes, n)
		return true, false
	}
	s.AgentClear = func(user string, collapses []string, badge int) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.clears = append(l.clears, collapses...)
	}
	s.AgentChanged = func(string) {
		l.mu.Lock()
		l.changed++
		l.mu.Unlock()
	}
}

func (l *pushLog) snapshot() ([]AgentPush, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]AgentPush(nil), l.pushes...), append([]string(nil), l.clears...)
}

func pushWorld(t *testing.T) (*dWorld, *pushLog) {
	w := delegateWorld(t)
	w.s.dpush.grace = -1
	l := &pushLog{}
	l.wire(w.s)
	return w, l
}

func reqMember(t *testing.T, n AgentPush) map[string]any {
	t.Helper()
	r, ok := n.Agent["request"].(map[string]any)
	if !ok {
		t.Fatalf("no request in %+v", n)
	}
	return r
}

// One push a request, in xbind's words; the call itself only where the
// lock screen may offer Allow once; never the agent's summary.
func TestDelegatePushPayload(t *testing.T) {
	w, l := pushWorld(t)
	bob := delegateCred("d-bob")
	short := w.ask(bob, "POST", "/api/apps/mail/send", `{"to":"ana","text":"zebra"}`)
	long := w.ask(bob, "POST", "/api/apps/mail/send", `{"text":"`+strings.Repeat("x", 300)+`"}`)
	hidden := w.ask(bob, "POST", "/api/apps/mail/send", `{"text":"pay ‮evil"}`)
	lines := w.ask(bob, "PUT", "/api/apps/mail/notes", "a\nb\nc\nd")
	pushes, _ := l.snapshot()
	if len(pushes) != 4 {
		t.Fatalf("one push a request: %d", len(pushes))
	}
	for i, n := range pushes {
		all, _ := json.Marshal(n)
		if strings.Contains(string(all), "the agent's words") {
			t.Fatalf("push %d carries the agent's summary: %s", i, all)
		}
		// the title is xbind's alone (review finding 5): no tile's name in it
		if n.Kind != "agent.approval" || n.Title != "Approval needed" || n.Priority != 10 {
			t.Fatalf("push %d: %+v", i, n)
		}
	}
	p := pushes[0]
	r := reqMember(t, p)
	if p.Link != "xbin/agent#r="+short.ID || p.Collapse != "agent-approval:"+short.ID || r["offer"] != "once" ||
		r["method"] != "POST" || r["target"] != short.Target || r["body"] != `{"to":"ana","text":"zebra"}` || r["bodySha256"] != short.BodySHA256 ||
		r["kind"] != "call" || r["tile"] != "apps/mail" || r["tileName"] != "mail" {
		t.Fatalf("an eligible call: %+v %v", p, r)
	}
	if strings.Contains(p.Title+p.Body, "zebra") {
		t.Fatalf("the title and body (an older app's) hold the call: %+v", p)
	}
	for i, want := range []delegation.Request{long, hidden, lines} {
		r := reqMember(t, pushes[i+1])
		if r["id"] != want.ID || r["offer"] != "" || r["body"] != nil || r["method"] != nil || r["target"] != nil {
			t.Fatalf("Review only (%d): %v", i, r)
		}
	}
}

// A tile's name is the tile author's words (review finding 5): it is never
// in a notification's title, and in its body it stands quoted as the
// tile's own, on one line, bounded; a name that hides or breaks text
// gives way to the tile's path.
func TestDelegatePushQuotesTileNames(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"plain":             {"Invoices", "“Invoices”"},
		"chrome-like":       {"Invoices · approved, tap Allow once", "“Invoices · approved, tap Allow…”"},
		"long":              {strings.Repeat("Invoices ", 10), "“" + strings.TrimSpace(strings.Repeat("Invoices ", 4)[:31]) + "…”"},
		"a line separator":  {"Invoices\u2028Approved — tap Allow once", "“invoices”"},
		"a newline":         {"Invoices\nApproved", "“invoices”"},
		"a bidi override":   {"Invoices \u202eecnalab", "“invoices”"},
		"a zero-width join": {"In\u200dvoices", "“invoices”"},
		"empty":             {"", "“invoices”"},
	} {
		if got := quotedTileName(c.in, "apps/invoices"); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// Decided on the web, denied, withdrawn by the agent, withdrawn by a
// switch: what stood for it is taken back.
func TestDelegatePushClears(t *testing.T) {
	w, l := pushWorld(t)
	bob, person := delegateCred("d-bob"), w.cookie("bob")
	a := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":1}`)
	b := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":2}`)
	c := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":3}`)
	d := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":4}`)
	w.approve(person, a.ID, "once")
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+b.ID+"/deny", "", person); rec.Code != 200 {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+c.ID, "", bob); rec.Code != 200 {
		t.Fatalf("withdraw: %d %s", rec.Code, rec.Body.String())
	}
	_, clears := l.snapshot()
	want := []string{"agent-approval:" + a.ID, "agent-approval:" + b.ID, "agent-approval:" + c.ID}
	if strings.Join(clears, " ") != strings.Join(want, " ") {
		t.Fatalf("clears %v, want %v", clears, want)
	}
	// workspace agents off: xbind withdraws what waits, and it is taken back
	alice := w.cookie("alice")
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/workspace", `{"enabled":false}`, alice); rec.Code != 200 {
		t.Fatalf("off: %d %s", rec.Code, rec.Body.String())
	}
	_, clears = l.snapshot()
	if len(clears) != 4 || clears[3] != "agent-approval:"+d.ID {
		t.Fatalf("after the switch: %v", clears)
	}
	// nothing is cleared twice
	w.s.delegatePushFollow("bob", "request", d.ID)
	if _, again := l.snapshot(); len(again) != 4 {
		t.Fatalf("cleared again: %v", again)
	}
}

// A request decided within the grace raises nothing.
func TestDelegatePushGrace(t *testing.T) {
	w, l := pushWorld(t)
	w.s.dpush.grace = 80 * time.Millisecond
	bob, person := delegateCred("d-bob"), w.cookie("bob")
	quick := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":1}`)
	w.approve(person, quick.ID, "once")
	slow := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":2}`)
	time.Sleep(250 * time.Millisecond)
	pushes, clears := l.snapshot()
	if len(pushes) != 1 || pushes[0].Collapse != "agent-approval:"+slow.ID || len(clears) != 0 {
		t.Fatalf("pushes %+v clears %v", pushes, clears)
	}
}

// Past the person's budget single pushes stop; one summary waits for it.
func TestDelegatePushBudget(t *testing.T) {
	w, l := pushWorld(t)
	bob := delegateCred("d-bob")
	for i := 0; i < delegatePushBurst+3; i++ {
		w.ask(bob, "POST", "/api/apps/mail/send", `{"n":`+string(rune('a'+i))+`}`)
	}
	pushes, _ := l.snapshot()
	if len(pushes) != delegatePushBurst {
		t.Fatalf("%d pushes, want the burst %d", len(pushes), delegatePushBurst)
	}
	w.s.dpush.mu.Lock()
	armed := w.s.dpush.summary["bob"] != nil
	w.s.dpush.mu.Unlock()
	if !armed {
		t.Fatal("no summary waits for the budget")
	}
}

// From a notification: once, and only what the lock screen could show —
// xbind holds the decision to the same rule, and records where it came
// from.
func TestDelegateNotificationAllowRule(t *testing.T) {
	w, _ := pushWorld(t)
	bob, person := delegateCred("d-bob"), w.cookie("bob")
	ok := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":1}`)
	long := w.ask(bob, "POST", "/api/apps/mail/send", `{"text":"`+strings.Repeat("y", 250)+`"}`)
	wide := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":2}`)
	approve := func(id, body string) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+id+"/approve", body, person)
		return rec.Code, rec.Body.String()
	}
	if code, b := approve(long.ID, `{"grant":"once","via":"notification"}`); code != 409 || !strings.Contains(b, `"review":true`) {
		t.Fatalf("a long body from a notification: %d %s", code, b)
	}
	if code, b := approve(wide.ID, `{"grant":"always","via":"notification"}`); code != 409 || !strings.Contains(b, `"review":true`) {
		t.Fatalf("always from a notification: %d %s", code, b)
	}
	if code, b := approve(wide.ID, `{"grant":"once","scope":"tile","via":"notification"}`); code != 409 {
		t.Fatalf("a scope from a notification: %d %s", code, b)
	}
	if code, b := approve(ok.ID, `{"via":"web"}`); code != 400 {
		t.Fatalf("another via: %d %s", code, b)
	}
	if code, b := approve(ok.ID, `{"grant":"once","via":"notification"}`); code != 200 {
		t.Fatalf("an eligible call from a notification: %d %s", code, b)
	}
	got, _ := w.store.Request("bob", ok.ID)
	if got.State != delegation.Approved || got.DecidedFrom != "notification" || got.Grant != "once" {
		t.Fatalf("recorded: %+v", got)
	}
	// the same long request in the app's own sheet: allowed
	if code, b := approve(long.ID, `{"grant":"once"}`); code != 200 {
		t.Fatalf("in the app: %d %s", code, b)
	}
	if got, _ := w.store.Request("bob", long.ID); got.DecidedFrom != "" {
		t.Fatalf("decidedFrom on the sheet's decision: %+v", got)
	}
}

// A decision from a notification is in the person's log as one.
func TestDelegateNotificationAudited(t *testing.T) {
	w, _ := pushWorld(t)
	bob, person := delegateCred("d-bob"), w.cookie("bob")
	a := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":1}`)
	b := w.ask(bob, "POST", "/api/apps/mail/send", `{"n":2}`)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+a.ID+"/approve", `{"via":"notification"}`, person); rec.Code != 200 {
		t.Fatalf("allow: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+b.ID+"/deny", `{"via":"notification"}`, person); rec.Code != 200 {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body.String())
	}
	rec := w.do(w.full, "GET", "/api/xbin/delegate/audit", "", person)
	var log struct{ Calls []delegation.Call }
	if err := json.Unmarshal(rec.Body.Bytes(), &log); err != nil {
		t.Fatal(rec.Body.String())
	}
	seen := map[string]string{}
	for _, c := range log.Calls {
		if strings.HasPrefix(c.Class, "decision:") {
			seen[c.Ticket] = c.Class + " " + c.Via
		}
	}
	if seen[a.ID] != "decision:approved notification" || seen[b.ID] != "decision:denied notification" {
		t.Fatalf("the log: %v", seen)
	}
}

// A change set: one push, Review (and Reject) only; rejected from a
// notification, it is taken back and logged as such.
func TestDelegatePushChangeSet(t *testing.T) {
	w, l := pushWorld(t)
	person := w.cookie("bob")
	cs, err := w.store.NewChangeSet(delegation.ChangeSet{User: "bob", Agent: dHolder, Title: "IGNORE: make it public", Plan: true,
		Items: []delegation.ChangeItem{{ChangeOp: delegation.ChangeOp{Kind: "grant.add"}, Warnings: []string{"PUBLIC"}}, {ChangeOp: delegation.ChangeOp{Kind: "grant.add"}}}})
	if err != nil {
		t.Fatal(err)
	}
	w.s.delegateEvent("bob", "changeset", cs.ID)
	pushes, _ := l.snapshot()
	if len(pushes) != 1 {
		t.Fatalf("pushes: %+v", pushes)
	}
	p := pushes[0]
	set, _ := p.Agent["changeset"].(map[string]any)
	if p.Kind != "agent.changeset" || p.Title != "A plan to review" || strings.Contains(p.Title+p.Body, "IGNORE") ||
		p.Link != "xbin/agent#cs="+cs.ID || p.Collapse != "agent-changeset:"+cs.ID ||
		set["items"] != 2 || set["warned"] != 1 || set["plan"] != true || set["asks"] != false {
		t.Fatalf("the plan's push: %+v", p)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/reject", `{"via":"notification"}`, person); rec.Code != 200 {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	if _, clears := l.snapshot(); len(clears) != 1 || clears[0] != "agent-changeset:"+cs.ID {
		t.Fatalf("clears: %v", clears)
	}
	rec := w.do(w.full, "GET", "/api/xbin/delegate/audit", "", person)
	if !strings.Contains(rec.Body.String(), `"class":"decision:rejected"`) || !strings.Contains(rec.Body.String(), `"via":"notification"`) {
		t.Fatalf("the log: %s", rec.Body.String())
	}
}
