package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// codingRunAs starts a conversation of the coding class (the sandbox
// toolset) owned as the stamp says, with carol a participant and dave a
// viewer when members is set.
func codingRunAs(t *testing.T, ag *Agent, st runStamp, members bool) int64 {
	t.Helper()
	cfg := defaultConfig()
	cfg.Class = "coding"
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true, Stamp: st})
	if err != nil {
		t.Fatal(err)
	}
	if members {
		for u, role := range map[string]string{"carol": roleParticipant, "dave": roleViewer} {
			if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, ?, ?, 1)`, r.ID, u, role); err != nil {
				t.Fatal(err)
			}
		}
		ag.acl.flush(r.ID)
	}
	return r.ID
}

var (
	alicePrivate = runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}
	aliceTeam    = runStamp{Owner: "alice", Visibility: visTeam, TeamRole: roleParticipant, Origin: "chat"}
)

func bindTo(t *testing.T, mux *http.ServeMux, c caller, run int64, ref, cwd string) int {
	t.Helper()
	w := callAs(t, mux, c, "PATCH", fmt.Sprintf("/runs/%d", run), map[string]any{"sandbox": map[string]string{"ref": ref, "cwd": cwd}})
	if w.Code != 200 && testing.Verbose() {
		t.Logf("bind %s as %s: %d %s", ref, c.user, w.Code, w.Body)
	}
	return w.Code
}

func sandboxOf(t *testing.T, ag *Agent, run int64) (*SandboxBinding, []SandboxBinding) {
	t.Helper()
	cfg, err := ag.db.runConfig(run)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sandbox, cfg.Attached
}

// Who may bind what: participant access to the conversation AND the right to
// use the sandbox; a tile manager may delete anyone's but bind no one's
// private one.
func TestSandboxBindAccess(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := codingRunAs(t, ag, alicePrivate, true)
	team := codingRunAs(t, ag, aliceTeam, false)
	ref := func(b *sbxSandbox) string { return sandboxRef("apps/cs", b.ID) }
	alices := ref(mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "alices"}))
	bobs := ref(mkSandbox(t, "apps/cs", "bob", sbxCreate{Name: "bobs"}))
	teamBox := ref(mkSandbox(t, "apps/cs", "bob", sbxCreate{Name: "shared", Visibility: "team"}))
	carols := ref(mkSandbox(t, "apps/cs", "carol", sbxCreate{Name: "carols"}))
	for _, c := range []struct {
		who  caller
		run  int64
		ref  string
		want int
	}{
		{asAlice, conv, alices, 200},
		{asAlice, conv, bobs, 403},    // not hers to use
		{asAlice, conv, teamBox, 200}, // team: anyone's
		{asCarol, conv, carols, 200},  // a participant brings her own
		{asDave, conv, teamBox, 403},  // a viewer can't steer
		{asBob, conv, teamBox, 404},   // can't see the conversation
		{asMgr, team, bobs, 403},      // a tile manager: not someone else's private one
		{asMgr, team, teamBox, 200},
		{asViewAs, team, teamBox, 403},
		{asAlice, conv, "apps/cs|nope", 404},
		{asAlice, conv, "apps/gone|x", 404},
		{asAlice, conv, "garbage", 400},
	} {
		if got := bindTo(t, mux, c.who, c.run, c.ref, ""); got != c.want {
			t.Errorf("%s binds %s to %d: %d, want %d", c.who.user, c.ref, c.run, got, c.want)
		}
	}
	active, att := sandboxOf(t, ag, conv)
	if active == nil || active.Ref != carols || active.By != "carol" || len(att) != 3 || active.Name != "carols" ||
		active.Manager != "Fake sandboxes (test fixture)" || active.Egress != "none" || active.Image != "base" || active.At == 0 ||
		!strings.HasSuffix(active.Cwd, "/work") {
		t.Fatalf("the last binding wins, all three attached: %+v %+v", active, att)
	}
	if active, _ := sandboxOf(t, ag, team); active.By != "mgr" {
		t.Fatalf("by: %+v", active)
	}
	// a tile manager may delete bob's private sandbox (not seeing it bound anywhere)
	if w := callAs(t, mux, asMgr, "DELETE", "/sandboxes/"+url.PathEscape(bobs), nil); w.Code != 200 {
		t.Fatalf("mgr deletes: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "DELETE", "/sandboxes/"+teamBox, nil); w.Code != 403 {
		t.Fatalf("alice deletes bob's team sandbox: %d %s", w.Code, w.Body)
	}
}

// The class gates binding: the sandbox toolset, and the sandbox's egress.
func TestSandboxBindClass(t *testing.T) {
	ag, mux := accessFixture(t)
	ms := bindSbx(t, "apps/cs")
	private := runAs(t, ag, alicePrivate, false) // the internal class: no sandbox
	coding := codingRunAs(t, ag, alicePrivate, false)
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "b", Egress: "internet"})
	ref := sandboxRef("apps/cs", box.ID)
	w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", private), map[string]any{"sandbox": map[string]string{"ref": ref}})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "no sandbox toolset") {
		t.Fatalf("an internal conversation: %d %s", w.Code, w.Body)
	}
	if got := bindTo(t, mux, asAlice, coding, ref, ""); got != 200 {
		t.Fatalf("coding allows internet egress: %d", got)
	}
	ms["apps/cs"].mu.Lock()
	ms["apps/cs"].boxes[box.ID].Egress = "open"
	ms["apps/cs"].mu.Unlock()
	w = callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", coding), map[string]any{"sandbox": map[string]string{"ref": ref}})
	if w.Code != 403 || !strings.Contains(w.Body.String(), `egress \"open\"`) {
		t.Fatalf("open egress: %d %s", w.Code, w.Body)
	}
	// and the tools' check sees the change too
	cfg, _ := ag.db.runConfig(coding)
	if _, err := ag.sandboxUse(context.Background(), coding, cfg, ""); sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), "changed since it was bound") {
		t.Fatalf("the check after egress changed: %v", err)
	}
}

// cwd: absolute, and it must exist when the sandbox runs.
func TestSandboxBindCwd(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := codingRunAs(t, ag, alicePrivate, false)
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{})
	ref := sandboxRef("apps/cs", box.ID)
	c, _ := sbxDial("apps/cs", "alice")
	_ = c.Mkdir(context.Background(), box.ID, box.Workdir+"/api", false)
	_, _ = c.WriteFile(context.Background(), box.ID, box.Workdir+"/f", strings.NewReader("x"), sbxWrite{})
	for cwd, want := range map[string]int{"relative": 400, box.Workdir + "/nope": 400, box.Workdir + "/f": 400, box.Workdir + "/api/": 200} {
		if got := bindTo(t, mux, asAlice, conv, ref, cwd); got != want {
			t.Errorf("cwd %q: %d, want %d", cwd, got, want)
		}
	}
	if active, _ := sandboxOf(t, ag, conv); active.Cwd != box.Workdir+"/api" {
		t.Fatalf("cleaned: %q", active.Cwd)
	}
	// a stopped sandbox isn't started to check a cwd
	_, _ = c.Lifecycle(context.Background(), box.ID, "stop", 0, false)
	if got := bindTo(t, mux, asAlice, conv, ref, "/anywhere"); got != 200 {
		t.Fatalf("stopped: %d", got)
	}
	if b, _ := theManager(t).Box(box.ID); b.State != "stopped" {
		t.Fatal("binding started it")
	}
}

// theManager is the test's manager bound as apps/cs.
func theManager(t *testing.T) *sbxTestManager {
	t.Helper()
	m, _ := boundManager("apps/cs")
	for _, x := range testManagers {
		if x.srv.URL == m.URL {
			return x
		}
	}
	t.Fatal("no test manager")
	return nil
}

// Rebinding applies from the next turn (the config is read every turn),
// attaches, detaches; the ask binds one from the start.
func TestSandboxRebindAndDetach(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	a := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "a"})
	b := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "b"})
	refA, refB := sandboxRef("apps/cs", a.ID), sandboxRef("apps/cs", b.ID)
	// the class comes from the ask (the classes track) or the defaults
	if err := ag.db.putSetting("config", `{"class":"coding"}`); err != nil {
		t.Fatal(err)
	}
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "hi", "class": "coding", "sandbox": map[string]string{"ref": refA}})
	if w.Code != 200 {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	var run struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	waitQuiet(t, ag)
	if active, att := sandboxOf(t, ag, run.ID); active == nil || active.Ref != refA || active.By != "alice" || len(att) != 1 {
		t.Fatalf("the ask's sandbox: %+v %+v", active, att)
	}
	if w := callAs(t, mux, asBob, "POST", "/ask", map[string]any{"text": "hi", "class": "coding", "sandbox": map[string]string{"ref": refA}}); w.Code != 403 {
		t.Fatalf("bob asks in alice's sandbox: %d %s", w.Code, w.Body)
	}
	if got := bindTo(t, mux, asAlice, run.ID, refB, ""); got != 200 {
		t.Fatal(got)
	}
	var seen string
	f := fakeOf(ag)
	f.on(forRun(run.ID, lastUser("next")), func(req LLMRequest) (LLMReply, error) {
		cfg, _ := ag.db.runConfig(run.ID)
		seen = cfg.Sandbox.Ref
		return say("ok")(req)
	})
	send(t, ag, run.ID, "next")
	waitQuiet(t, ag)
	if seen != refB {
		t.Fatalf("the next turn's sandbox: %q", seen)
	}
	cfg, _ := ag.db.runConfig(run.ID)
	use, err := ag.sandboxUse(context.Background(), run.ID, cfg, "")
	if err != nil || use.ID != b.ID || use.Cwd != b.Workdir || use.Conn.User != "alice" {
		t.Fatalf("the tools resolve the new one: %+v %v", use, err)
	}
	if use, err := ag.sandboxUse(context.Background(), run.ID, cfg, refA); err != nil || use.ID != a.ID {
		t.Fatalf("an attached one by ref: %v", err)
	}
	// null: no active sandbox, both stay attached
	callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", run.ID), map[string]any{"sandbox": nil})
	if active, att := sandboxOf(t, ag, run.ID); active != nil || len(att) != 2 {
		t.Fatalf("unbound: %+v %+v", active, att)
	}
	cfg, _ = ag.db.runConfig(run.ID)
	if _, err := ag.sandboxUse(context.Background(), run.ID, cfg, ""); sbxRefusal(err) != "none" || !strings.Contains(err.Error(), "no sandbox is bound") {
		t.Fatalf("none bound: %v", err)
	}
	// detach one, and bind the other in the same call
	w = callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", run.ID), map[string]any{"detach": refA, "sandbox": map[string]string{"ref": refB}})
	if active, att := sandboxOf(t, ag, run.ID); w.Code != 200 || active == nil || active.Ref != refB || len(att) != 1 || att[0].Ref != refB {
		t.Fatalf("detach + bind: %d %+v %+v", w.Code, active, att)
	}
	cfg, _ = ag.db.runConfig(run.ID)
	if _, err := ag.sandboxUse(context.Background(), run.ID, cfg, refA); sbxRefusal(err) != "not-attached" {
		t.Fatalf("a detached ref: %v", err)
	}
	callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", run.ID), map[string]any{"detach": refB})
	if active, att := sandboxOf(t, ag, run.ID); active != nil || att != nil {
		t.Fatalf("detached all: %+v %+v", active, att)
	}
	if got := callAs(t, mux, asDave, "PATCH", fmt.Sprintf("/runs/%d", run.ID), map[string]any{"detach": refB}).Code; got != 404 {
		t.Fatalf("dave can't see it: %d", got)
	}
}

func TestSandboxAttachLimit(t *testing.T) {
	var cfg Config
	for i := 0; i < maxAttached; i++ {
		if err := attachSandbox(&cfg, SandboxBinding{Ref: fmt.Sprintf("apps/cs|b%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := attachSandbox(&cfg, SandboxBinding{Ref: "apps/cs|b3", Cwd: "/x"}); err != nil || cfg.Sandbox.Cwd != "/x" || len(cfg.Attached) != maxAttached {
		t.Fatalf("rebinding an attached one: %v %+v", err, cfg.Sandbox)
	}
	if err := attachSandbox(&cfg, SandboxBinding{Ref: "apps/cs|one-too-many"}); err == nil || !strings.Contains(err.Error(), "detach one first") {
		t.Fatalf("the ninth: %v", err)
	}
}

// Subagents inherit the root's sandboxes as copies; the defaults never
// carry one.
func TestSandboxInheritance(t *testing.T) {
	parent := Config{Sandbox: &SandboxBinding{Ref: "apps/cs|a", Cwd: "/w"}, Attached: []SandboxBinding{{Ref: "apps/cs|a"}, {Ref: "apps/cs|b"}}}
	kid := childConfig(parent, "")
	if kid.Sandbox == nil || kid.Sandbox.Ref != "apps/cs|a" || len(kid.Attached) != 2 {
		t.Fatalf("inherited: %+v", kid)
	}
	kid.Sandbox.Cwd = "/elsewhere"
	kid.Attached[1].Ref = "apps/cs|c"
	if parent.Sandbox.Cwd != "/w" || parent.Attached[1].Ref != "apps/cs|b" {
		t.Fatalf("a child's change reached its parent: %+v", parent)
	}
	if kid := childConfig(Config{}, ""); kid.Sandbox != nil || kid.Attached != nil {
		t.Fatalf("nothing to inherit: %+v", kid)
	}

	_, mux := accessFixture(t)
	w := callAs(t, mux, asSystem, "PUT", "/config", map[string]any{"sandbox": map[string]string{"ref": "apps/cs|a"}, "attached": []any{map[string]string{"ref": "apps/cs|a"}}})
	if w.Code != 200 || strings.Contains(w.Body.String(), "apps/cs|a") {
		t.Fatalf("the defaults carry no sandbox: %d %s", w.Code, w.Body)
	}
}

// The tools' check, every call: the manager still bound, the sandbox still
// there, the binder still allowed and still in the conversation.
func TestSandboxUseRechecks(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := codingRunAs(t, ag, alicePrivate, true)
	carols := mkSandbox(t, "apps/cs", "carol", sbxCreate{Name: "carols", Members: []string{"alice"}})
	ref := sandboxRef("apps/cs", carols.ID)
	if got := bindTo(t, mux, asCarol, conv, ref, ""); got != 200 {
		t.Fatal(got)
	}
	ctx := context.Background()
	use := func() error {
		cfg, _ := ag.db.runConfig(conv)
		_, err := ag.sandboxUse(ctx, conv, cfg, "")
		return err
	}
	if err := use(); err != nil {
		t.Fatal(err)
	}
	calls := theManager(t).Calls()
	if last := calls[len(calls)-1]; last.Method != "GET" || last.SbxUser != "carol" {
		t.Fatalf("the check asks the manager as the binder: %+v", last)
	}
	// carol leaves: her sandbox leaves with her
	if w := callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d/members/carol", conv), nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := use(); sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), "no longer takes part") {
		t.Fatalf("the binder left: %v", err)
	}
	// alice (a member of carol's sandbox) binds it herself; then carol drops her
	if got := bindTo(t, mux, asAlice, conv, ref, ""); got != 200 {
		t.Fatal(got)
	}
	if err := use(); err != nil {
		t.Fatal(err)
	}
	if c, _ := sbxDial("apps/cs", "carol"); c != nil {
		none := []string{}
		_, _ = c.Patch(ctx, carols.ID, sbxPatch{Members: &none})
	}
	if err := use(); sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), "may no longer use it") {
		t.Fatalf("the binder was dropped: %v", err)
	}
	// deleted at the manager
	team := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "gone"})
	if got := bindTo(t, mux, asAlice, conv, sandboxRef("apps/cs", team.ID), ""); got != 200 {
		t.Fatal(got)
	}
	c, _ := sbxDial("apps/cs", "alice")
	_ = c.Delete(ctx, team.ID)
	if err := use(); sbxRefusal(err) != "not-found" || !strings.Contains(err.Error(), "is gone") {
		t.Fatalf("deleted: %v", err)
	}
	// the manager unbound
	t.Setenv("XBIN_IFACE_SANDBOXES", "[]")
	if err := use(); sbxRefusal(err) != "unbound" {
		t.Fatalf("unbound: %v", err)
	}
	// a class without the sandbox toolset
	cfg, _ := ag.db.runConfig(conv)
	cfg.Class = "web"
	if _, err := ag.sandboxUse(ctx, conv, cfg, ""); sbxRefusal(err) != "not-allowed" || !strings.Contains(err.Error(), "no sandbox toolset") {
		t.Fatalf("the class: %v", err)
	}
}

// A sandbox made for a conversation: owned by the caller, a team one for a
// team conversation, its participants members, labeled, and bound.
func TestNewSandboxForConversation(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	team := codingRunAs(t, ag, aliceTeam, true)
	private := codingRunAs(t, ag, alicePrivate, true)
	create := func(c caller, body map[string]any) (int, map[string]any) {
		w := callAs(t, mux, c, "POST", "/sandboxes", body)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 201 && testing.Verbose() {
			t.Logf("create: %d %s", w.Code, w.Body)
		}
		return w.Code, out
	}
	code, out := create(asBob, map[string]any{"name": "for the team", "conversation": team, "clientId": "k1"})
	if code != 201 || out["ref"] == nil || out["binding"] == nil {
		t.Fatalf("bob creates for the team conversation: %d %v", code, out)
	}
	id := out["id"].(string)
	box, _ := theManager(t).Box(id)
	if box.Owner.User != "bob" || !box.Owner.Asserted || box.Visibility != "team" || strings.Join(box.Members, ",") != "alice,carol" ||
		box.Labels["xbin.agent/conversation"] != fmt.Sprint(team) {
		t.Fatalf("the team sandbox: %+v", box)
	}
	if active, _ := sandboxOf(t, ag, team); active == nil || active.Ref != out["ref"] || active.By != "bob" {
		t.Fatalf("bound: %+v", active)
	}
	if again, out2 := create(asBob, map[string]any{"name": "for the team", "conversation": team, "clientId": "k1"}); again != 201 || out2["id"] != id {
		t.Fatalf("a retried create: %d %v", again, out2)
	}
	for _, call := range theManager(t).Calls() {
		if call.Method == "POST" && call.Path == "/sbx/sandboxes" && (call.SbxUser != "bob" || !strings.Contains(call.Body, `"clientId":"bob:k1"`)) {
			t.Fatalf("the create names bob: %+v", call)
		}
	}
	code, out = create(asAlice, map[string]any{"name": "mine", "conversation": private, "bind": false})
	if code != 201 || out["binding"] != nil {
		t.Fatalf("alice, unbound: %d %v", code, out)
	}
	if box, _ := theManager(t).Box(out["id"].(string)); box.Visibility != "private" || strings.Join(box.Members, ",") != "carol" {
		t.Fatalf("a private conversation's: %+v", box)
	}
	if active, _ := sandboxOf(t, ag, private); active != nil {
		t.Fatal("bind:false binds nothing")
	}
	if code, _ := create(asDave, map[string]any{"name": "x", "conversation": private}); code != 403 {
		t.Fatalf("a viewer: %d", code)
	}
	internal := runAs(t, ag, alicePrivate, false)
	before := theManager(t).count("POST", "/sbx/sandboxes")
	if code, _ := create(asAlice, map[string]any{"name": "x", "conversation": internal}); code != 403 || theManager(t).count("POST", "/sbx/sandboxes") != before {
		t.Fatalf("an internal conversation: %d", code)
	}
	if code, _ := create(asAlice, map[string]any{"name": "x", "conversation": private, "egress": "open"}); code != 403 {
		t.Fatalf("egress the class refuses: %d", code)
	}
	if code, _ := create(asAlice, map[string]any{"name": ""}); code != 400 {
		t.Fatalf("no name: %d", code)
	}
	if code, out := create(asAlice, map[string]any{"name": "plain"}); code != 201 || out["mine"] != true || out["canEdit"] != true {
		t.Fatalf("a plain create: %d %v", code, out)
	}
}

// Lifecycle, patching and deleting; a participant acts through a
// conversation on what it has bound; deleting detaches everywhere.
func TestSandboxLifecycleRoutes(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := codingRunAs(t, ag, alicePrivate, true)
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "alices"})
	ref := sandboxRef("apps/cs", box.ID)
	esc := url.PathEscape(ref)
	if got := bindTo(t, mux, asAlice, conv, ref, ""); got != 200 {
		t.Fatal(got)
	}
	act := func(c caller, target string) (int, string) {
		w := callAs(t, mux, c, "POST", target, map[string]any{})
		var out struct{ State string }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.State
	}
	if code, st := act(asAlice, "/sandboxes/"+esc+"/stop?wait=5"); code != 200 || st != "stopped" {
		t.Fatalf("alice stops hers: %d %s", code, st)
	}
	if code, st := act(asAlice, "/sandboxes/"+ref+"/start"); code != 200 || st != "running" {
		t.Fatalf("alice starts it (an unescaped ref): %d %s", code, st)
	}
	if code, _ := act(asCarol, "/sandboxes/"+esc+"/stop"); code != 403 {
		t.Fatalf("carol, not a member of it: %d", code)
	}
	if code, st := act(asCarol, fmt.Sprintf("/sandboxes/%s/stop?conversation=%d", esc, conv)); code != 200 || st != "stopped" {
		t.Fatalf("carol, through the conversation: %d %s", code, st)
	}
	if code, _ := act(asDave, fmt.Sprintf("/sandboxes/%s/start?conversation=%d", esc, conv)); code != 403 {
		t.Fatalf("dave, a viewer: %d", code)
	}
	if code, _ := act(asBob, "/sandboxes/"+esc+"/start"); code != 404 {
		t.Fatalf("bob can't see it: %d", code)
	}
	if code, _ := act(asAlice, "/sandboxes/"+esc+"/explode"); code != 404 {
		t.Fatalf("an unknown action: %d", code)
	}
	if code, st := act(asAlice, "/sandboxes/"+esc+"/archive"); code != 200 || st != "archived" {
		t.Fatalf("archive: %d %s", code, st)
	}
	if code, st := act(asAlice, "/sandboxes/"+esc+"/thaw"); code != 200 || st != "stopped" {
		t.Fatalf("thaw: %d %s", code, st)
	}

	// carol sees it through the conversation, without the right to use it herself
	w := callAs(t, mux, asCarol, "GET", "/sandboxes", nil)
	var list struct{ Sandboxes []map[string]any }
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Sandboxes) != 1 || list.Sandboxes[0]["canUse"] != false || fmt.Sprint(list.Sandboxes[0]["boundTo"]) != fmt.Sprintf("[%d]", conv) {
		t.Fatalf("carol's list: %s", w.Body)
	}
	if w := callAs(t, mux, asCarol, "GET", "/sandboxes/"+esc, nil); w.Code != 200 {
		t.Fatalf("carol reads it: %d", w.Code)
	}
	if w := callAs(t, mux, asCarol, "PATCH", "/sandboxes/"+esc, map[string]any{"name": "mine now"}); w.Code != 403 {
		t.Fatalf("carol renames it: %d", w.Code)
	}
	w = callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+esc, map[string]any{"name": "renamed", "members": []string{"carol"}, "visibility": "private"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"renamed"`) {
		t.Fatalf("alice renames it: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+esc, map[string]any{"visibility": "world"}); w.Code != 400 {
		t.Fatalf("a bad visibility: %d", w.Code)
	}
	if w := callAs(t, mux, asCarol, "DELETE", "/sandboxes/"+esc, nil); w.Code != 403 {
		t.Fatalf("carol, a member, deletes it: %d", w.Code)
	}
	w = callAs(t, mux, asAlice, "DELETE", "/sandboxes/"+esc, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"detached":1`) {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if active, att := sandboxOf(t, ag, conv); active != nil || att != nil {
		t.Fatalf("detached on delete: %+v %+v", active, att)
	}
	if w := callAs(t, mux, asAlice, "GET", "/sandboxes/"+esc, nil); w.Code != 404 {
		t.Fatalf("gone: %d", w.Code)
	}
}
