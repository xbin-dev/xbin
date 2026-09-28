package deployments

import (
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// protectOf is protect's answer, or the test's failure.
func protectOf(t *testing.T) func(res any, err error) ProtectAnswer {
	return func(res any, err error) ProtectAnswer {
		t.Helper()
		pa, ok := res.(ProtectAnswer)
		if err != nil || !ok {
			t.Fatalf("protect = %T %v", res, err)
		}
		return pa
	}
}

// covers P21 P24 T16 — protecting the primary: on a tile without a record
// it is the opt-in (its dry run captures nothing, creates no store); the
// primary, following the work tree, is pinned in place to a capture (how
// protect) and live reload detaches; the sessions that targeted it are moved
// (P24's default), and no session can target it any more. Resuming or
// attaching live reload onto it is refused for everyone, managers included;
// only a tile manager in a person's own session changes its code (a
// terminal-level person's and the tile's terminal token's deploy, promote,
// roll back and reload now answer the protected 403) while its non-primary
// deployments stay theirs. Unprotecting moves nothing and moves no session;
// protecting a pinned backend primary keeps its checkpoint and rebuilds it
// (Restart).
func TestProtectedPrimary(t *testing.T) {
	f := newGovFx(t, false)
	pa := protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true), DryRun: true}))
	if pa.Impact == nil || pa.Impact.Code != nil || !pa.Impact.PausesLiveReload || f.rec(opSite) != nil || f.st.Exists(opSite) {
		t.Fatalf("the opt-in's dry run = %+v (record %v, store %v)", pa.Impact, f.rec(opSite), f.st.Exists(opSite))
	}
	pa = protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true)}))
	r := f.rec(opSite)
	if r == nil || !r.ProtectedPrimary || r.LiveReload != "" || r.LastLiveReload != "main" || cp(r, "main") == "" || r.Deployments["main"].Created == "" {
		t.Fatalf("record after protecting = %+v", r)
	}
	if d := pa.Deploy; d == nil || d.How != "protect" || d.Deployment != "main" || d.Result != resultOK || d.Checkpoint != "c:"+cp(r, "main")[:7] {
		t.Errorf("protect's deploy = %+v", pa.Deploy)
	}
	if calls := f.took(); !slices.Equal(calls, []string{"sessions-protected apps/site"}) {
		t.Errorf("calls = %q", calls)
	}
	evs := f.evs.take()
	if len(readerForms(evs)) == 0 || !strings.Contains(readerForms(evs)[0], "protectedPrimary") {
		t.Errorf("readers aren't told: %v", readerForms(evs))
	}
	if td := f.p.TileDeployments(opSite); !td.Protected {
		t.Errorf("the terminal manager's view = %+v", td)
	}
	term := terminalP("dev", opSite, users.LevelTerminal)
	if _, err := f.p.Addressed(term, opSite); err == nil || !strings.Contains(err.Error(), "can't target it") {
		t.Errorf("a session following the protected primary = %v", err)
	}

	for _, pr := range []auth.Principal{ownerP, term} {
		_, err := f.do(pr, OpResume, &ResumeRequest{Tile: opSite})
		if bad := azCheckCode(err, azProtected409, opSite); bad != "" {
			t.Errorf("resume onto the protected primary: %s", bad)
		}
	}
	f.add(term, &AddRequest{Tile: opSite, Deployment: "dev"})
	_, err := f.do(term, OpAttach, &AttachRequest{Tile: opSite, Deployment: "main"})
	if bad := azCheckCode(err, azProtected409, opSite); bad != "" {
		t.Errorf("attach onto the protected primary: %s", bad)
	}
	s := ptr(f.rec(opSite).Seq)
	devID := "c:" + cp(f.rec(opSite), "dev")[:12]
	for _, pr := range []auth.Principal{term, userP("dev", opSite, users.LevelTerminal)} {
		for _, c := range []struct {
			op  Op
			req any
		}{
			{OpDeploy, &DeployRequest{Tile: opSite, Deployment: "main", Checkpoint: devID, Seq: s}},
			{OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main", Expect: devID, Seq: s}},
			{OpRollback, &RollbackRequest{Tile: opSite, Deployment: "main", Checkpoint: devID, Seq: s}},
			{OpReloadNow, &ReloadNowRequest{Tile: opSite, Expect: devID, Seq: s}},
		} {
			_, err := f.do(pr, c.op, c.req)
			if bad := azCheckCode(err, azProtected, opSite); bad != "" {
				t.Errorf("%s by %s: %s", c.op, pr.From(), bad)
			}
		}
	}
	f.write(opSite+"/index.html", "<h1>dev 2</h1>")
	f.settle(opSite, f.must(term, OpDeploy, &DeployRequest{Tile: opSite, Deployment: "dev"}))
	f.settle(opSite, f.must(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Deployment: "main", Checkpoint: devID, Seq: ptr(f.rec(opSite).Seq)}))
	if r := f.rec(opSite); "c:"+cp(r, "main")[:12] != devID {
		t.Errorf("the manager's reviewed deploy left main on %s", cp(r, "main"))
	}
	f.took()

	pinned := cp(f.rec(opSite), "main")
	pa = protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(false)}))
	if r := f.rec(opSite); r.ProtectedPrimary || cp(r, "main") != pinned || pa.Deploy != nil || len(pa.Warnings) != 0 {
		t.Errorf("unprotecting: record %+v, answer %+v", r, pa)
	}
	if calls := f.took(); len(calls) != 0 {
		t.Errorf("unprotecting called %q", calls)
	}
	if pa = protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(false)})); !pa.Unchanged {
		t.Error("unprotecting again changed something")
	}

	t.Run("a pinned backend primary", func(t *testing.T) {
		g := newGovFx(t, true)
		g.settle(opAPI, g.must(ownerP, OpPause, &PauseRequest{Tile: opAPI}))
		pinned := cp(g.rec(opAPI), "main")
		g.took()
		pa := protectOf(t)(g.do(ownerP, OpProtect, &ProtectRequest{Tile: opAPI, On: ptr(true), Expect: "c:" + pinned[:9]}))
		if d := pa.Deploy; d == nil || d.How != "protect" || d.Deployment != "main" {
			t.Fatalf("the rebuild = %+v", pa.Deploy)
		}
		if e := g.wait(opAPI, pa.Deploy.ID); e.Result != resultOK || cp(g.rec(opAPI), "main") != pinned || !g.rec(opAPI).ProtectedPrimary {
			t.Errorf("the rebuild = %+v", e)
		}
		if calls := g.waitCall("restart"); !slices.Contains(calls, "restart apps/api/main") || !slices.Contains(calls, "sessions-protected apps/api") {
			t.Errorf("calls = %q", calls)
		}
	})
}

// covers P21 T16 06-security-T16 — onto a protected primary every code move
// names what its manager reviewed: deploy and roll back a checkpoint, reload
// now, promotion and reassignment an expect, each with seq; without them the
// answer is 400 before anything is captured, dry runs included, while a
// restart of the current code is no review. A record that moved since the
// review (seq), or a work tree that moved (expect), answers 409. Authority is
// judged again at the commit: a manager demoted while the reload captures
// commits nothing. A reviewed deploy lands.
func TestProtectedPromoteNeedsReviewedCheckpoint(t *testing.T) {
	var manager atomic.Bool
	manager.Store(true)
	f := newGovFx(t, false)
	f.p.MayManage = func(pr auth.Principal, tile string) bool { return manager.Load() && pr.IsAdmin() }
	f.write(opSite+"/index.html", "<h1>dev</h1>")
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	f.write(opSite+"/index.html", "<h1>v1</h1>")
	protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true)}))
	r0 := f.rec(opSite)
	s, devID := ptr(r0.Seq), "c:"+cp(r0, "dev")[:12]
	captures := f.st.captures
	for _, c := range []struct {
		op    Op
		req   any
		field string
	}{
		{OpDeploy, &DeployRequest{Tile: opSite, Seq: s}, "checkpoint"},
		{OpDeploy, &DeployRequest{Tile: opSite, Checkpoint: devID}, "checkpoint"},
		{OpDeploy, &DeployRequest{Tile: opSite, Expect: devID, Seq: s}, "checkpoint"},
		{OpDeploy, &DeployRequest{Tile: opSite, Seq: s, DryRun: true}, "checkpoint"},
		{OpRollback, &RollbackRequest{Tile: opSite, Deployment: "main", Seq: s}, "checkpoint"},
		{OpReloadNow, &ReloadNowRequest{Tile: opSite, Seq: s}, "expect"},
		{OpReloadNow, &ReloadNowRequest{Tile: opSite, Expect: devID, DryRun: true}, "expect"},
		{OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main", Seq: s}, "expect"},
		{OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Seq: s}, "expect"},
	} {
		_, err := f.do(ownerP, c.op, c.req)
		wantErr(t, string(c.op), err, http.StatusBadRequest, "the primary of apps/site is protected: name the checkpoint you reviewed (send "+c.field+" and seq)")
	}
	if f.st.captures != captures || f.rec(opSite).Seq != r0.Seq {
		t.Fatal("an unreviewed move captured or committed")
	}
	answerOf(t)(f.do(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Restart: true}))

	_, err := f.do(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Checkpoint: devID, Seq: ptr(r0.Seq - 1)})
	wantErr(t, "a moved record", err, http.StatusConflict, "the deployments of apps/site changed (seq")
	reviewed := f.captureNow(opSite)
	f.write(opSite+"/index.html", "<h1>moved</h1>")
	_, err = f.do(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite, Expect: reviewed.ID, Seq: s})
	wantErr(t, "a moved work tree", err, http.StatusConflict, "the code changed since you reviewed "+reviewed.ID)

	// Demoted while the reload captures: the swap's commit refuses it.
	f.st.set(func(st *fakeStore) { st.hook = func(string) { manager.Store(false) } })
	now := f.captureNow(opSite)
	ans := answerOf(t)(f.do(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite, Expect: now.ID, Seq: s}))
	f.st.set(func(st *fakeStore) { st.hook = nil })
	if e := f.wait(opSite, ans.Deploy.ID); e.Result != resultFailed || !strings.HasPrefix(e.Error, "the primary of apps/site (main) is protected") ||
		cp(f.rec(opSite), "main") != cp(r0, "main") {
		t.Errorf("the demoted manager's reload = %+v, main on %s", e, cp(f.rec(opSite), "main"))
	}
	manager.Store(true)
	f.settle(opSite, f.must(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Checkpoint: devID, Seq: ptr(f.rec(opSite).Seq)}))
	if cp(f.rec(opSite), "main") != cp(r0, "dev") {
		t.Error("the reviewed deploy didn't land")
	}
}

// covers T16 NP-06-8 A16 — protecting a primary lists the components nested
// in its tile, whose code its writers change and whose pages it serves, and
// warns for each that isn't protected itself, dry run and commit alike, with
// whether the caller manages it; protecting a child is that child's
// managers' act, never implied; a protected child is listed without a
// warning.
func TestProtectionCoversNestedComponents(t *testing.T) {
	f := newGovFx(t, false)
	f.write(opSite+"/child/xbin.json", "{}")
	f.write(opSite+"/child/index.html", "<p>child</p>")
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	f.p.MayManage = func(pr auth.Principal, tile string) bool {
		return pr.Component == "" && (pr.IsAdmin() || pr.UserID == "mia" && tile == opSite)
	}
	mia := userP("mia", opSite, users.LevelTerminal)
	child := opSite + "/child"
	warn := child + " is nested in apps/site, whose writers change its code and whose pages serve it, but isn't protected: protect it too"
	for _, dry := range []bool{true, false} {
		pa := protectOf(t)(f.do(mia, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true), DryRun: dry}))
		if !slices.Equal(pa.Nested, []NestedProtection{{Tile: child}}) || !slices.Equal(pa.Warnings, []string{warn}) {
			t.Errorf("dry %v: nested %+v, warnings %q", dry, pa.Nested, pa.Warnings)
		}
	}
	if f.rec(child) != nil {
		t.Fatal("protecting the parent protected the child")
	}
	_, err := f.do(mia, OpProtect, &ProtectRequest{Tile: child, On: ptr(true)})
	if bad := azCheckCode(err, azManager, child); bad != "" {
		t.Errorf("mia protecting the child: %s", bad)
	}
	protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: child, On: ptr(true)}))
	protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(false)}))
	pa := protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true)}))
	if !slices.Equal(pa.Nested, []NestedProtection{{Tile: child, Protected: true, Manager: true}}) || len(pa.Warnings) != 0 {
		t.Errorf("with the child protected: nested %+v, warnings %q", pa.Nested, pa.Warnings)
	}
}

// covers T12 — under --no-auth every caller without a credential is the
// owner (Via "dev"): protecting is allowed, and its answers, the dry run
// included, say it is not enforced; a real owner or admin session isn't told
// so.
func TestNoAuthProtectionMarkedUnenforced(t *testing.T) {
	dev := auth.Principal{Owner: true, Via: "dev"}
	if Unenforced(dev) != NotEnforced || Unenforced(ownerP) != "" || Unenforced(azAda) != "" || NotEnforced != "not enforced: authentication is off" {
		t.Fatalf("Unenforced: dev %q, owner %q, admin %q", Unenforced(dev), Unenforced(ownerP), Unenforced(azAda))
	}
	f := newGovFx(t, false)
	for _, dry := range []bool{true, false} {
		pa := protectOf(t)(f.do(dev, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true), DryRun: dry}))
		if !slices.Equal(pa.Warnings, []string{NotEnforced}) {
			t.Errorf("dry %v: warnings %q", dry, pa.Warnings)
		}
	}
	if r := f.rec(opSite); r == nil || !r.ProtectedPrimary {
		t.Fatal("protecting under --no-auth wasn't allowed")
	}
	protectOf(t)(f.do(dev, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(false)}))
	if pa := protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true)})); len(pa.Warnings) != 0 {
		t.Errorf("the owner token was told %q", pa.Warnings)
	}
}
