package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// Classes a manager may define: internal reach with a sandbox that has no
// network (not mixed).
var (
	intSbxClass = agentClass{ID: "intsbx", Name: "Internal coding", Toolsets: []string{tsInternal, tsSandbox},
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
