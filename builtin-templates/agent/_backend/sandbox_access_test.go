package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// Classes a manager may define: internal reach with a sandbox that has no
// network (not mixed), a confirmed mixed one, and one with no reach either
// way.
var (
	intSbxClass = agentClass{ID: "intsbx", Name: "Internal coding", Toolsets: []string{tsInternal, tsSandbox},
		Managers: classSet{All: true}, SandboxEgress: []string{"none"}}
	mixedSbxClass = agentClass{ID: "mixed", Name: "Mixed", Toolsets: []string{tsInternal, tsWeb, tsSandbox},
		Managers: classSet{All: true}, SandboxEgress: []string{"none"}}
	quietSbxClass = agentClass{ID: "quiet", Name: "Quiet", Toolsets: []string{tsSandbox},
		Managers: classSet{All: true}, SandboxEgress: []string{"none"}}
)

// classRunAs starts a held conversation of class, owned as the stamp says.
func classRunAs(t *testing.T, ag *Agent, class string, st runStamp) int64 {
	t.Helper()
	cfg := defaultConfig()
	cfg.Class = class
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true, Stamp: st})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// setBox changes a sandbox at the test's manager behind the agent's back.
func setBox(t *testing.T, id string, f func(b *fsbBox)) {
	t.Helper()
	m := theManager(t)
	m.mu.Lock()
	f(m.boxes[id])
	m.mu.Unlock()
	invalidateSandboxCatalog()
}

func useOf(t *testing.T, ag *Agent, run int64, ref string) error {
	t.Helper()
	cfg, err := ag.db.runConfig(run)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ag.sandboxUse(context.Background(), run, cfg, ref)
	return err
}

// An egress PATCHed on a running sandbox applies at its next start
// (egressNext): the firewall checks the less restrictive of the two, so a
// lowered egress isn't trusted before the restart, and a raised one counts
// at once (a stopped sandbox starts on an exec).
func TestSandboxEgressPendingRestart(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	useClasses(t, intSbxClass)
	internal := classRunAs(t, ag, "intsbx", alicePrivate)

	// lowered: it still has the internet until it restarts
	net := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "net", Egress: "internet"})
	netRef := sandboxRef("apps/cs", net.ID)
	w := callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+url.PathEscape(netRef), map[string]any{"egress": "none"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"egressNext":"none"`) || !strings.Contains(w.Body.String(), `"restartNeeded":true`) {
		t.Fatalf("lowering a running sandbox's egress: %d %s", w.Code, w.Body)
	}
	if got := bindTo(t, mux, asAlice, internal, netRef, ""); got != 403 {
		t.Fatalf("an internal-reach class binds a sandbox that still has the internet: %d", got)
	}
	// restarted: now it has none
	c, _ := sbxDial("apps/cs", "alice")
	_, _ = c.Lifecycle(context.Background(), net.ID, "stop", 5, false)
	_, _ = c.Lifecycle(context.Background(), net.ID, "start", 5, false)
	if got := bindTo(t, mux, asAlice, internal, netRef, ""); got != 200 {
		t.Fatalf("after the restart: %d", got)
	}

	// raised: bound with none, the next start takes the internet
	quiet := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "quiet"})
	ref := sandboxRef("apps/cs", quiet.ID)
	if got := bindTo(t, mux, asAlice, internal, ref, ""); got != 200 {
		t.Fatal(got)
	}
	if w := callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+url.PathEscape(ref), map[string]any{"egress": "internet"}); w.Code != 200 {
		t.Fatalf("raising: %d %s", w.Code, w.Body)
	}
	if err := useOf(t, ag, internal, ref); sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), `egress "internet"`) {
		t.Fatalf("the tools' check with the internet pending: %v", err)
	}
	if got := bindTo(t, mux, asAlice, internal, ref, ""); got != 403 {
		t.Fatalf("binding with the internet pending: %d", got)
	}

	// Approve mode: a pending internet is an egress, so bash parks
	coding := codingRunAs(t, ag, alicePrivate, false)
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "code"})
	cref := sandboxRef("apps/cs", box.ID)
	if got := bindTo(t, mux, asAlice, coding, cref, ""); got != 200 {
		t.Fatal(got)
	}
	inet := "internet"
	_, _ = c.Patch(context.Background(), box.ID, sbxPatch{Egress: &inet})
	cfg, _ := ag.db.runConfig(coding)
	cfg.Approve = true
	ctx := withSbxCall(context.Background(), sbxCall{run: coding, name: "bash", approve: true})
	if _, err := ag.sandboxUse(ctx, coding, cfg, ""); sbxRefusal(err) != "egress-changed" {
		t.Fatalf("bash on a pending internet in Approve mode: %v", err)
	}
	if cfg, _ := ag.db.runConfig(coding); cfg.Sandbox.Egress != "internet" || !sandboxSideEffect("bash", cfg) {
		t.Fatalf("the binding records the pending internet: %+v", cfg.Sandbox)
	}
}

// A sandbox whose manager reports no egress, or one this agent doesn't
// know, counts as open: it binds only where the class allows open.
func TestSandboxUnknownEgressIsOpen(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	coding := codingRunAs(t, ag, alicePrivate, false) // none or internet
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "odd"})
	ref := sandboxRef("apps/cs", box.ID)
	for _, e := range []string{"", "lan"} {
		setBox(t, box.ID, func(b *fsbBox) { b.Egress = e })
		w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", coding), map[string]any{"sandbox": map[string]string{"ref": ref}})
		if w.Code != 403 || !strings.Contains(w.Body.String(), `egress \"open\"`) {
			t.Fatalf("egress %q binds: %d %s", e, w.Code, w.Body)
		}
	}
	setBox(t, box.ID, func(b *fsbBox) { b.Egress = "none" })
	if got := bindTo(t, mux, asAlice, coding, ref, ""); got != 200 {
		t.Fatal(got)
	}
	setBox(t, box.ID, func(b *fsbBox) { b.Egress = "" })
	if err := useOf(t, ag, coding, ""); sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), `egress "open"`) {
		t.Fatalf("the tools' check on a missing egress: %v", err)
	}
	for _, c := range []struct {
		now, next, want string
	}{
		{"none", "", "none"}, {"none", "internet", "internet"}, {"internet", "none", "internet"},
		{"open", "none", "open"}, {"", "", "open"}, {"none", "lan", "open"}, {"weird", "", "open"},
	} {
		if got := (&sbxSandbox{Egress: c.now, EgressNext: c.next}).effectiveEgress(); got != c.want {
			t.Errorf("egress %q next %q: %q, want %q", c.now, c.next, got, c.want)
		}
	}
}

// A detach reaches the subagents' copies of the binding at once, and so
// does a rebind by someone else: a subagent works only in what its root
// still has, as it was bound.
func TestSandboxDetachReachesSubagents(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := codingRunAs(t, ag, alicePrivate, true)
	carols := mkSandbox(t, "apps/cs", "carol", sbxCreate{Name: "carols", Members: []string{"alice"}})
	ref := sandboxRef("apps/cs", carols.ID)
	if got := bindTo(t, mux, asCarol, conv, ref, ""); got != 200 {
		t.Fatal(got)
	}
	spawn := func() (*Run, Config) {
		t.Helper()
		rootCfg, _ := ag.db.runConfig(conv)
		id, err := ag.db.createRun("kid", "{}", conv)
		if err != nil {
			t.Fatal(err)
		}
		kid, _ := ag.db.getRun(id)
		return kid, childConfig(rootCfg, "")
	}
	kid, kidCfg := spawn()
	if out := mustTool(t, ag, kid, kidCfg, "k1", "bash", map[string]any{"command": "echo in-carols"}); !strings.Contains(out, "in-carols") {
		t.Fatalf("the subagent works in the root's sandbox: %s", out)
	}
	// carol detaches her sandbox: the subagent's copy stops working with it
	if w := callAs(t, mux, asCarol, "PATCH", fmt.Sprintf("/runs/%d", conv), map[string]any{"detach": ref}); w.Code != 200 {
		t.Fatalf("detach: %d %s", w.Code, w.Body)
	}
	if err := useOf(t, ag, conv, ref); sbxRefusal(err) != "not-attached" {
		t.Fatalf("the root: %v", err)
	}
	if _, err := tool(t, ag, kid, kidCfg, "k2", "bash", map[string]any{"command": "echo still"}); err == nil || !strings.Contains(err.Error(), "no longer attached") {
		t.Fatalf("the subagent after the detach: %v", err)
	}
	// alice binds it again, herself: carol's copy stays refused, a new subagent works
	if got := bindTo(t, mux, asAlice, conv, ref, ""); got != 200 {
		t.Fatal(got)
	}
	if _, err := tool(t, ag, kid, kidCfg, "k3", "read", map[string]any{"path": "."}); err == nil || !strings.Contains(err.Error(), "no longer attached") {
		t.Fatalf("the old copy after a rebind by someone else: %v", err)
	}
	kid2, kidCfg2 := spawn()
	if _, err := tool(t, ag, kid2, kidCfg2, "k4", "bash", map[string]any{"command": "true"}); err != nil {
		t.Fatalf("a subagent of the new binding: %v", err)
	}
}

// A sandbox another consumer shared with this tile is people's only as far
// as the share names them.
func TestSandboxShareUsers(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := codingRunAs(t, ag, aliceTeam, false) // the team may talk in it
	box := mkSandbox(t, "apps/cs", "dave", sbxCreate{Name: "ci's", Visibility: "team"})
	ref := sandboxRef("apps/cs", box.ID)
	share := func(users fsbUsers) {
		setBox(t, box.ID, func(b *fsbBox) {
			b.Owner.Via = "apps/ci"
			b.Shares = []fsbShare{{Consumer: "apps/other", Users: fsbUsers{All: true}}, {Consumer: "apps/agent", Users: users}}
		})
	}
	share(fsbUsers{List: []string{"bob"}})
	if got := bindTo(t, mux, asCarol, conv, ref, ""); got != 403 {
		t.Fatalf("carol, not in the share, binds a team sandbox: %d", got)
	}
	if got := bindTo(t, mux, asAlice, conv, ref, ""); got != 403 {
		t.Fatalf("alice, not in the share: %d", got)
	}
	if got := bindTo(t, mux, asBob, conv, ref, ""); got != 200 {
		t.Fatalf("bob, in the share: %d", got)
	}
	canUse := func(c caller) any {
		w := callAs(t, mux, c, "GET", "/sandboxes/"+url.PathEscape(ref), nil)
		var v map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return v["canUse"]
	}
	if canUse(asCarol) != false || canUse(asBob) != true {
		t.Fatalf("canUse: carol %v, bob %v", canUse(asCarol), canUse(asBob))
	}
	if err := useOf(t, ag, conv, ""); err != nil {
		t.Fatal(err)
	}
	// the share drops bob: his binding goes with it
	share(fsbUsers{List: []string{"carol"}})
	if err := useOf(t, ag, conv, ""); sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), "may no longer use it") {
		t.Fatalf("bob dropped from the share: %v", err)
	}
	share(fsbUsers{All: true})
	if got := bindTo(t, mux, asCarol, conv, ref, ""); got != 200 {
		t.Fatalf("a share for everyone: %d", got)
	}
	// no share for this tile: nobody
	t.Setenv("XBIN_COMPONENT", "apps/agent#2")
	if a := sandboxAccess(who{kind: whoUser, user: "dave", level: "write"}, &sbxSandbox{Shared: true, Visibility: visTeam,
		Shares: json.RawMessage(`[{"consumer":"apps/agent","users":"*"}]`)}); a.seen() {
		t.Fatalf("a share for another consumer: %+v", a)
	}
}

// The class firewall across a sandbox that conversations share: one with
// internal reach marks it; a class that reaches outside without internal
// reach may then neither bind it nor keep working in it.
func TestSandboxInternalTaint(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	useClasses(t, intSbxClass, mixedSbxClass, quietSbxClass)
	coding := codingRunAs(t, ag, alicePrivate, false)
	internal := classRunAs(t, ag, "intsbx", alicePrivate)
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "scratch", Labels: map[string]string{"k": "v"}})
	ref := sandboxRef("apps/cs", box.ID)
	if got := bindTo(t, mux, asAlice, coding, ref, ""); got != 200 {
		t.Fatal(got)
	}
	if err := useOf(t, ag, coding, ""); err != nil {
		t.Fatal(err)
	}
	if got := bindTo(t, mux, asAlice, internal, ref, ""); got != 200 {
		t.Fatal(got)
	}
	if b, _ := theManager(t).Box(box.ID); b.Labels[sbxInternalLabel] != "1" || b.Labels["k"] != "v" {
		t.Fatalf("marked, its labels kept: %+v", b.Labels)
	}
	// the coding conversation that had it first can't work in it any more
	if err := useOf(t, ag, coding, ""); sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), "held data from an internal-reach conversation") {
		t.Fatalf("a web-lane conversation after the mark: %v", err)
	}
	coding2 := codingRunAs(t, ag, alicePrivate, false)
	w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", coding2), map[string]any{"sandbox": map[string]string{"ref": ref}})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "held data from an internal-reach conversation") {
		t.Fatalf("binding it to a web-lane conversation: %d %s", w.Code, w.Body)
	}
	// no egress, or a confirmed mixed class: they may
	for _, cls := range []string{"quiet", "mixed"} {
		if got := bindTo(t, mux, asAlice, classRunAs(t, ag, cls, alicePrivate), ref, ""); got != 200 {
			t.Fatalf("class %s: %d", cls, got)
		}
	}
	// the owner's label edit keeps the mark
	if w := callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+url.PathEscape(ref), map[string]any{"labels": map[string]string{"k": "w"}}); w.Code != 200 {
		t.Fatalf("relabel: %d %s", w.Code, w.Body)
	}
	if b, _ := theManager(t).Box(box.ID); b.Labels[sbxInternalLabel] != "1" || b.Labels["k"] != "w" {
		t.Fatalf("relabeled: %+v", b.Labels)
	}
	// a mark lost at the manager is made again by the next tool call
	setBox(t, box.ID, func(b *fsbBox) { b.Labels = map[string]string{} })
	if err := useOf(t, ag, internal, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := theManager(t).Box(box.ID); b.Labels[sbxInternalLabel] != "1" {
		t.Fatalf("marked again: %+v", b.Labels)
	}
	// a mark the manager refuses: no binding
	other := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "other"})
	theManager(t).FailNext("patch", 503, "unavailable", "down")
	w = callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", internal), map[string]any{"sandbox": map[string]string{"ref": sandboxRef("apps/cs", other.ID)}})
	if w.Code != 502 || !strings.Contains(w.Body.String(), "marking it failed") {
		t.Fatalf("an unmarkable sandbox: %d %s", w.Code, w.Body)
	}
	if active, _ := sandboxOf(t, ag, internal); active == nil || active.Ref != ref {
		t.Fatalf("still bound to the first: %+v", active)
	}
	// a label change racing the mark: read again, marked
	theManager(t).FailNext("patch", 412, "precondition", "the sandbox changed")
	if got := bindTo(t, mux, asAlice, internal, sandboxRef("apps/cs", other.ID), ""); got != 200 {
		t.Fatalf("after a lost update: %d", got)
	}
	if b, _ := theManager(t).Box(other.ID); b.Labels[sbxInternalLabel] != "1" {
		t.Fatalf("marked after a retry: %+v", b.Labels)
	}
	// one made for an internal-reach conversation is marked as it is bound
	w = callAs(t, mux, asAlice, "POST", "/sandboxes", map[string]any{"name": "made", "conversation": internal})
	var made struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &made)
	if b, _ := theManager(t).Box(made.ID); w.Code != 201 || b.Labels[sbxInternalLabel] != "1" {
		t.Fatalf("made for it: %d %s %+v", w.Code, w.Body, b.Labels)
	}
	// the label present but empty (unmarked): marking sets it, never copies the blank back
	blank := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "blank", Labels: map[string]string{sbxInternalLabel: "", "k": "v"}})
	if got := bindTo(t, mux, asAlice, internal, sandboxRef("apps/cs", blank.ID), ""); got != 200 {
		t.Fatalf("a blank label: %d", got)
	}
	if b, _ := theManager(t).Box(blank.ID); b.Labels[sbxInternalLabel] != "1" || b.Labels["k"] != "v" {
		t.Fatalf("marked over a blank label: %+v", b.Labels)
	}
}

// The mark spreads within a conversation (review): a class with a sandbox
// but neither internal reach nor egress could otherwise carry what a marked
// sandbox holds into a clean one — sandbox_copy, or a read then a write, or
// through the session files after a detach — and a web-lane conversation
// could then bind that one.
func TestSandboxMarkSpreads(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	useClasses(t, intSbxClass, mixedSbxClass, quietSbxClass)
	marked := func(box *sbxSandbox) bool {
		t.Helper()
		b, _ := theManager(t).Box(box.ID)
		return b.Labels[sbxInternalLabel] != ""
	}
	held := func(run int64) bool {
		t.Helper()
		cfg, err := ag.db.runConfig(run)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.HeldInternal
	}
	mk := func(name string, labels map[string]string) (*sbxSandbox, string) {
		box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: name, Labels: labels})
		return box, sandboxRef("apps/cs", box.ID)
	}
	webRefuses := func(ref string) {
		t.Helper()
		w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", codingRunAs(t, ag, alicePrivate, false)), map[string]any{"sandbox": map[string]string{"ref": ref}})
		if w.Code != 403 || !strings.Contains(w.Body.String(), "held data from an internal-reach conversation") {
			t.Fatalf("a web-lane conversation binding %s: %d %s", ref, w.Code, w.Body)
		}
	}

	// binding a marked sandbox marks what the conversation has attached
	s, refS := mk("s", map[string]string{sbxInternalLabel: "1"})
	tt, refT := mk("t", nil)
	quiet := classRunAs(t, ag, "quiet", alicePrivate)
	if got := bindTo(t, mux, asAlice, quiet, refT, ""); got != 200 || marked(tt) || held(quiet) {
		t.Fatalf("a clean one into a quiet conversation: %d marked %v", got, marked(tt))
	}
	if got := bindTo(t, mux, asAlice, quiet, refS, ""); got != 200 {
		t.Fatal(got)
	}
	if !marked(tt) || !held(quiet) {
		t.Fatalf("after the marked one: t marked %v, held %v", marked(tt), held(quiet))
	}
	webRefuses(refT)
	// …and whatever it binds after — even once the marked one is detached
	if w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", quiet), map[string]any{"detach": refS}); w.Code != 200 {
		t.Fatalf("detach: %d %s", w.Code, w.Body)
	}
	u, refU := mk("u", nil)
	if got := bindTo(t, mux, asAlice, quiet, refU, ""); got != 200 || !marked(u) {
		t.Fatalf("bound after the detach: %d marked %v", got, marked(u))
	}
	webRefuses(refU)
	_ = s

	// a sandbox marked after it was bound: working in it marks the rest
	a, refA := mk("a", nil)
	b, refB := mk("b", nil)
	late := classRunAs(t, ag, "quiet", alicePrivate)
	for _, ref := range []string{refB, refA} {
		if got := bindTo(t, mux, asAlice, late, ref, ""); got != 200 {
			t.Fatal(got)
		}
	}
	setBox(t, a.ID, func(x *fsbBox) { x.Labels = map[string]string{sbxInternalLabel: "1"} })
	if err := useOf(t, ag, late, refA); err != nil {
		t.Fatal(err)
	}
	if !marked(b) || !held(late) {
		t.Fatalf("after working in a: b marked %v, held %v", marked(b), held(late))
	}
	// a mark lost at the manager is made again before the conversation works there
	setBox(t, b.ID, func(x *fsbBox) { x.Labels = nil })
	if err := useOf(t, ag, late, refB); err != nil || !marked(b) {
		t.Fatalf("working in b: %v, marked %v", err, marked(b))
	}

	// sandbox_copy from a marked source marks the target first
	c, refC := mk("c", nil)
	d, refD := mk("d", nil)
	cp := classRunAs(t, ag, "quiet", alicePrivate)
	for _, ref := range []string{refD, refC} {
		if got := bindTo(t, mux, asAlice, cp, ref, ""); got != 200 {
			t.Fatal(got)
		}
	}
	put(t, c, "secret.txt", "internal\n")
	setBox(t, c.ID, func(x *fsbBox) { x.Labels = map[string]string{sbxInternalLabel: "1"} })
	run, _ := ag.db.getRun(cp)
	cfg, _ := ag.db.runConfig(cp)
	mustTool(t, ag, run, cfg, "cp", "sandbox_copy", map[string]any{"from": map[string]any{"path": "secret.txt"}, "to": map[string]any{"sandbox": refD, "path": "x.txt"}})
	if !marked(d) {
		t.Fatal("the copy's target is not marked")
	}
	webRefuses(refD)

	// an internal-reach conversation: every sandbox it binds is marked, as before
	e, refE := mk("e", nil)
	if got := bindTo(t, mux, asAlice, classRunAs(t, ag, "intsbx", alicePrivate), refE, ""); got != 200 || !marked(e) {
		t.Fatalf("internal: %d marked %v", got, marked(e))
	}
	// a web-lane conversation never holds it: a clean sandbox stays clean there
	f, refF := mk("f", nil)
	coding := codingRunAs(t, ag, alicePrivate, false)
	if got := bindTo(t, mux, asAlice, coding, refF, ""); got != 200 || useOf(t, ag, coding, "") != nil || marked(f) || held(coding) {
		t.Fatalf("web lane: %d marked %v held %v", got, marked(f), held(coding))
	}
}

// A relabel racing a mark (review): PATCH /sandboxes/{ref} {labels} goes
// with the version it merged against, so a mark set between its read and
// its write fails it — read again, merged, and the mark stays.
func TestSandboxRelabelRacesTheMark(t *testing.T) {
	_, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "r", Labels: map[string]string{"k": "v"}})
	ref := sandboxRef("apps/cs", box.ID)
	var patches atomic.Int32
	sbxTransport(t, func(r *http.Request, next http.RoundTripper) (*http.Response, error) {
		if r.Method == "PATCH" && patches.Add(1) == 1 { // an internal conversation marks it just now
			setBox(t, box.ID, func(x *fsbBox) {
				x.Labels = map[string]string{"k": "v", sbxInternalLabel: "1"}
				x.Version++
			})
		}
		return next.RoundTrip(r)
	})
	w := callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+url.PathEscape(ref), map[string]any{"labels": map[string]string{"k": "w"}})
	if w.Code != 200 {
		t.Fatalf("relabel: %d %s", w.Code, w.Body)
	}
	if b, _ := theManager(t).Box(box.ID); b.Labels[sbxInternalLabel] != "1" || b.Labels["k"] != "w" || patches.Load() != 2 {
		t.Fatalf("after the race: %+v (%d patches)", b.Labels, patches.Load())
	}
	// the caller's own version is theirs: a stale one is refused, not retried
	w = callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+url.PathEscape(ref), map[string]any{"labels": map[string]string{"k": "x"}, "version": 1})
	if w.Code != 412 || patches.Load() != 3 {
		t.Fatalf("a stale version of the caller's: %d %s (%d patches)", w.Code, w.Body, patches.Load())
	}
	// a change that doesn't touch the labels sends no version of ours
	if w := callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+url.PathEscape(ref), map[string]any{"name": "renamed"}); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
}

// A share PATCH goes with the version the page read (D121 follow-up): the
// agent passes it to the manager, a stale one comes back 412 precondition —
// what the page's store re-reads on and retries once — and GET
// /sandboxes/{ref} gives the version to retry with.
func TestSandboxSharesWithVersion(t *testing.T) {
	_, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "s"})
	ref := sandboxRef("apps/cs", box.ID)
	path := "/sandboxes/" + url.PathEscape(ref)
	read := func() (int, []any) {
		t.Helper()
		w := callAs(t, mux, asAlice, "GET", path, nil)
		var v struct {
			Version int   `json:"version"`
			Shares  []any `json:"shares"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Version == 0 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		return v.Version, v.Shares
	}
	v0, _ := read()
	share := func(consumer string) map[string]any {
		return map[string]any{"consumer": consumer, "users": []string{"alice"}}
	}
	w := callAs(t, mux, asAlice, "PATCH", path, map[string]any{"shares": []any{share("apps/term")}, "version": v0})
	if w.Code != 200 {
		t.Fatalf("a share at the version read: %d %s", w.Code, w.Body)
	}
	v1, shares := read()
	if v1 <= v0 || len(shares) != 1 {
		t.Fatalf("after the share: version %d → %d, shares %v", v0, v1, shares)
	}
	// the list read at v0 is stale now: refused, nothing lost
	w = callAs(t, mux, asAlice, "PATCH", path, map[string]any{"shares": []any{share("apps/other")}, "version": v0})
	var refusal struct{ Refusal string }
	if w.Code != 412 || json.Unmarshal(w.Body.Bytes(), &refusal) != nil || refusal.Refusal != "precondition" {
		t.Fatalf("a stale share list: %d %s", w.Code, w.Body)
	}
	if v, shares := read(); v != v1 || len(shares) != 1 {
		t.Fatalf("a refused PATCH changed it: version %d, shares %v", v, shares)
	}
	// re-read, retried at the new version: both shares
	w = callAs(t, mux, asAlice, "PATCH", path, map[string]any{"shares": []any{share("apps/term"), share("apps/other")}, "version": v1})
	if w.Code != 200 {
		t.Fatalf("the retry: %d %s", w.Code, w.Body)
	}
	if _, shares := read(); len(shares) != 2 {
		t.Fatalf("shares after the retry: %v", shares)
	}
}

// Picking a sandbox the conversation has attached again, with no cwd, keeps
// the cwd it is attached at.
func TestSandboxRepickKeepsCwd(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := codingRunAs(t, ag, alicePrivate, false)
	a := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "a"})
	b := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "b"})
	refA, refB := sandboxRef("apps/cs", a.ID), sandboxRef("apps/cs", b.ID)
	c, _ := sbxDial("apps/cs", "alice")
	_ = c.Mkdir(context.Background(), a.ID, a.Workdir+"/api", false)
	if got := bindTo(t, mux, asAlice, conv, refA, a.Workdir+"/api"); got != 200 {
		t.Fatal(got)
	}
	if got := bindTo(t, mux, asAlice, conv, refA, ""); got != 200 {
		t.Fatal(got)
	}
	if active, _ := sandboxOf(t, ag, conv); active.Cwd != a.Workdir+"/api" {
		t.Fatalf("re-picked: %q", active.Cwd)
	}
	for _, ref := range []string{refB, refA} {
		if got := bindTo(t, mux, asAlice, conv, ref, ""); got != 200 {
			t.Fatal(got)
		}
	}
	if active, att := sandboxOf(t, ag, conv); active.Ref != refA || active.Cwd != a.Workdir+"/api" || len(att) != 2 || att[1].Cwd != b.Workdir {
		t.Fatalf("switched back: %+v %+v", active, att)
	}
}

// The owner's grant card says when the sandbox will be the team's: made for
// a team conversation, it takes the conversation's audience.
func TestSandboxCreateGrantSaysTeam(t *testing.T) {
	ag, _ := accessFixture(t)
	bindSbx(t, "apps/cs")
	calls := []toolCall{tc("c1", "sandbox_create", `{"name":"x"}`)}
	for _, c := range []struct {
		st   runStamp
		team bool
	}{{alicePrivate, false}, {aliceTeam, true}} {
		id := codingRunAs(t, ag, c.st, false)
		cfg, _ := ag.db.runConfig(id)
		ask := sandboxesGrantAsk(ag, mustRun(t, ag, id), cfg, calls, nil)
		if !strings.Contains(ask, `"x"`) || strings.Contains(ask, "anyone on the team may use it") != c.team {
			t.Errorf("%s conversation: %q", c.st.Visibility, ask)
		}
	}
}
