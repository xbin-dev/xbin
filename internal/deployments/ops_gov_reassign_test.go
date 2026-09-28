package deployments

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/checkpoint"
)

// captureNow is the checkpoint a capture of tile's work tree takes now, as a
// reviewer's diff names it: content-addressed, so the plane's capture of the
// same files is the same checkpoint.
func (f *govFx) captureNow(tile string) checkpoint.Result {
	f.t.Helper()
	c, _ := f.p.component(tile)
	res, err := f.st.Capture(context.Background(), checkpoint.CaptureRequest{Source: f.p.source(c), By: "owner"})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// covers D127d D127t T6 09-fabric-§8 — TestReassignMovesActiveRegistrations:
// reassigning the primary is a tile manager's act with the loud
// confirmation, routing only. The dry run shows the primary's code moving
// and the vault keys the new primary lacks, changing nothing; the commit
// moves the primary in memory at once (Primary, PinnedPrimary composing the
// tile from its code, the active registrations: the old primary's go dormant,
// the new one's activate), announces it to readers too, moves the sessions
// named after either, hands the broker the moved registrations (its
// consumers and ingress), and restarts the new primary first, then the old
// one, through the runner, then wakes alwaysOn; no data act runs, and neither
// the root xbin.json nor the tile's files are written. Again is unchanged; back
// to main follows the work tree. Refused: a target that isn't healthy
// (the runner's state, or a failed move), and a tile that isn't alone in the
// scope it roots or in the workspace scope, which the state's allowed says.
func TestReassignPrimaryAtomic(t *testing.T) {
	f := newGovFx(t, false)
	f.write("xbin.json", `{"schema":1}`)
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	f.p.VaultPlaceholders = func(tile, dep string) ([]string, error) { return []string{"STRIPE_KEY"}, nil }
	f.took()
	f.evs.take()
	root, _ := os.ReadFile(filepath.Join(f.root, "xbin.json"))
	files := treeHash(t, filepath.Join(f.root, opSite))
	r0 := f.rec(opSite)
	devTree := cp(r0, "dev")

	_, err := f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev"})
	wantErr(t, "without the confirmation", err, http.StatusBadRequest,
		`making dev the primary sends everyone to dev's data, and main's data stays behind: send confirm:"data-stays"`)
	im := f.dry(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, DryRun: true})
	if c := im.Code; c == nil || c.Deployment != "dev" || c.From != "work-tree" || c.To != "c:"+devTree[:7] || im.Affects != "everyone" ||
		!slices.Equal(im.Placeholders, []string{"STRIPE_KEY"}) || !slices.Equal(im.Reloads, []string{"main"}) || im.PausesLiveReload {
		t.Errorf("dry run = %+v, code %+v", im, im.Code)
	}
	if f.rec(opSite).Seq != r0.Seq {
		t.Fatal("the dry run changed the record")
	}

	ans := answerOf(t)(f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Seq: ptr(r0.Seq)}))
	r := f.rec(opSite)
	if ans.Deploy != nil || ans.Unchanged || r.Primary != "dev" || r.Seq != r0.Seq+1 || cp(r, "dev") != devTree || r.LiveReload != "main" {
		t.Fatalf("answer %+v, record %+v", ans, r)
	}
	if pc, ok := f.p.PinnedPrimary(opSite); !ok || pc == nil || pc.ManifestErr != "" || f.p.Primary(opSite) != "dev" {
		t.Errorf("the tile isn't composed from dev's code: %+v %v", pc, ok)
	}
	for dep, want := range map[string][2]bool{"dev": {true, true}, "main": {true, false}} { // main's cron and bus keep firing for main (D127h)
		if fires, routes := f.p.RegistrationsActive(opSite, dep); fires != want[0] || routes != want[1] {
			t.Errorf("%s's registrations: fires %v routes %v, want %v", dep, fires, routes, want)
		}
	}
	evs := f.evs.take()
	if len(evs) == 0 || !slices.Equal(evs[0].Data.(recordEvent).What, []string{"primary", "deployments"}) ||
		len(readerForms(evs)) != 1 || !strings.Contains(readerForms(evs)[0], `"primary"`) {
		t.Errorf("events = %+v", evs)
	}
	calls := f.waitCall("wake")
	if !slices.Equal(calls, []string{"sessions-reassigned apps/site main→dev", "routes-reassigned apps/site main→dev", "reassign apps/site main→dev", "wake"}) {
		t.Errorf("calls = %q (sessions, routes, then y first then x through the runner, then alwaysOn)", calls)
	}
	if b, _ := os.ReadFile(filepath.Join(f.root, "xbin.json")); !bytes.Equal(b, root) || treeHash(t, filepath.Join(f.root, opSite)) != files {
		t.Error("the reassignment wrote the root xbin.json or the tile's files")
	}

	if a := answerOf(t)(f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev"})); !a.Unchanged || f.rec(opSite).Seq != r.Seq {
		t.Errorf("reassigning to the primary again = %+v", a)
	}
	answerOf(t)(f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "main", Confirm: ConfirmDataStays}))
	if _, ok := f.p.PinnedPrimary(opSite); ok || f.p.Primary(opSite) != "main" {
		t.Error("main, following the work tree, isn't the primary again")
	}
	f.waitCall("wake")

	t.Run("healthy", func(t *testing.T) {
		g := newGovFx(t, true)
		g.add(ownerP, &AddRequest{Tile: opAPI, Deployment: "dev"})
		for _, st := range []string{"failed", "idle"} {
			g.gr.setStatus(opAPI+"/dev", st)
			_, err := g.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opAPI, Deployment: "dev", Confirm: ConfirmDataStays})
			wantErr(t, st, err, http.StatusConflict, "dev isn't healthy ("+st+"); only a healthy deployment can become the primary")
		}
		g.gr.setStatus(opAPI+"/dev", "healthy")
		if _, err := g.p.idx.commit(opAPI, -1, func(r *Record) error { r.Deployments["dev"].State = "failed"; return nil }); err != nil {
			t.Fatal(err)
		}
		_, err := g.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opAPI, Deployment: "dev", Confirm: ConfirmDataStays})
		wantErr(t, "a failed move", err, http.StatusConflict, "dev isn't healthy (failed)")
		if g.rec(opAPI).Primary != "main" {
			t.Error("a refused reassignment moved the primary")
		}
	})

	t.Run("scopes", func(t *testing.T) {
		g := newGovFx(t, false)
		for rel, body := range map[string]string{"apps/shared/scope.json": "{}", "apps/shared/a/xbin.json": "{}",
			"apps/shared/b/xbin.json": "{}", "apps/solo/scope.json": "{}", "apps/solo/xbin.json": "{}"} {
			g.write(rel, body)
		}
		if err := g.reg.Rescan(); err != nil {
			t.Fatal(err)
		}
		for _, tile := range []string{"apps/shared/a", "apps/solo"} {
			g.add(ownerP, &AddRequest{Tile: tile, Deployment: "dev"})
		}
		g.took()
		split := "reassigning the primary of apps/shared/a would split apps/shared's data: only a tile alone in its scope, or in the workspace scope, can change its primary in this release"
		_, err := g.do(ownerP, OpPrimary, &PrimaryRequest{Tile: "apps/shared/a", Deployment: "dev", Confirm: ConfirmDataStays})
		wantErr(t, "a shared scope", err, http.StatusConflict, split)
		if c := g.p.Allowed(OpPrimary, g.p.subjectOf("apps/shared/a", "dev")); c.OK || c.Kind != KindPolicy || c.Why != split {
			t.Errorf("allowed = %+v", c)
		}
		answerOf(t)(g.do(ownerP, OpPrimary, &PrimaryRequest{Tile: "apps/solo", Deployment: "dev", Confirm: ConfirmDataStays}))
		if c := g.p.Allowed(OpPrimary, g.p.subjectOf(opSite, "")); !c.OK {
			t.Errorf("the workspace scope's allowed = %+v", c)
		}
		for _, c := range g.waitCall("wake") {
			if strings.HasPrefix(c, "reset") || strings.HasPrefix(c, "seed") || strings.HasPrefix(c, "drop") {
				t.Errorf("a reassignment moved data: %s", c)
			}
		}
	})
}

// covers D127m T16 06-security-T16 — reassigning a protected primary names the
// checkpoint its manager reviewed, with seq: without either it is refused
// with 400 before anything is captured; a target that follows the work tree
// is pinned to exactly the reviewed capture in the same commit (how
// reassign, live reload detached, the protection kept), and a work tree that
// moved since the review, a pinned target running other code, or a moved
// record answers 409 and changes nothing.
func TestReassignPinsReviewedCheckpoint(t *testing.T) {
	setup := func(t *testing.T, attach bool) *govFx {
		f := newGovFx(t, false)
		f.write(opSite+"/index.html", "<h1>dev</h1>") // dev's code differs from main's
		f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev", Attach: attach})
		f.write(opSite+"/index.html", "<h1>v1</h1>")
		answerOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true)}))
		f.took()
		f.evs.take()
		return f
	}

	f := setup(t, true)
	r0 := f.rec(opSite)
	if r0.LiveReload != "dev" || !r0.ProtectedPrimary || cp(r0, "main") == "" {
		t.Fatalf("setup = %+v", r0)
	}
	captures := f.st.captures
	for _, req := range []*PrimaryRequest{
		{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Seq: ptr(r0.Seq)},
		{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Expect: "c:0123456"},
		{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, DryRun: true},
	} {
		_, err := f.do(ownerP, OpPrimary, req)
		wantErr(t, "unreviewed", err, http.StatusBadRequest, "the primary of apps/site is protected: name the checkpoint you reviewed (send expect and seq)")
	}
	if f.st.captures != captures || f.rec(opSite).Seq != r0.Seq {
		t.Fatal("an unreviewed reassignment captured or committed")
	}
	reviewed := f.captureNow(opSite)
	req := &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Expect: reviewed.ID, Seq: ptr(r0.Seq)}
	stale := *req
	stale.Seq = ptr(r0.Seq - 1)
	_, err := f.do(ownerP, OpPrimary, &stale)
	wantErr(t, "a moved record", err, http.StatusConflict, "the deployments of apps/site changed (seq")

	ans := answerOf(t)(f.do(ownerP, OpPrimary, req))
	r := f.rec(opSite)
	if r.Primary != "dev" || cp(r, "dev") != reviewed.Hash || r.LiveReload != "" || r.LastLiveReload != "dev" || !r.ProtectedPrimary {
		t.Fatalf("record after the reviewed reassignment = %+v", r)
	}
	if d := ans.Deploy; d == nil || d.How != "reassign" || d.Deployment != "dev" || d.Checkpoint != reviewed.ID || d.FollowsWorkTree || d.Previous != "" {
		t.Fatalf("its deploy = %+v", ans.Deploy)
	}
	if e := f.wait(opSite, ans.Deploy.ID); e.Result != resultOK {
		t.Errorf("the pin's attempt = %+v", e)
	}
	if calls := f.waitCall("wake"); !slices.Equal(calls, []string{"sessions-reassigned apps/site main→dev", "routes-reassigned apps/site main→dev", "reassign apps/site main→dev", "wake"}) {
		t.Errorf("calls = %q", calls)
	}
	if evs := f.evs.take(); len(evs) == 0 || !slices.Contains(evs[0].Data.(recordEvent).What, "liveReload") {
		t.Errorf("events = %+v", evs)
	}

	t.Run("the work tree moved since the review", func(t *testing.T) {
		g := setup(t, true)
		reviewed := g.captureNow(opSite)
		g.write(opSite+"/index.html", "<h1>moved</h1>")
		before := g.rec(opSite)
		_, err := g.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Expect: reviewed.ID, Seq: ptr(before.Seq)})
		wantErr(t, "a moved work tree", err, http.StatusConflict, "the code changed since you reviewed "+reviewed.ID+" (now c:")
		if r := g.rec(opSite); r.Seq != before.Seq || r.Primary != "main" || r.LiveReload != "dev" || g.p.pausing.has(opSite) {
			t.Errorf("record after the refusal = %+v", r)
		}
	})

	t.Run("a pinned target running other code", func(t *testing.T) {
		g := setup(t, false)
		before := g.rec(opSite)
		wrong := "c:" + cp(before, "main")[:12]
		_, err := g.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Expect: wrong, Seq: ptr(before.Seq)})
		wantErr(t, "another checkpoint", err, http.StatusConflict, "the code changed since you reviewed "+wrong+" (now c:"+cp(before, "dev")[:7])
		ok := "c:" + cp(before, "dev")[:12]
		answerOf(t)(g.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Expect: ok, Seq: ptr(before.Seq)}))
		if r := g.rec(opSite); r.Primary != "dev" || cp(r, "dev") != cp(before, "dev") {
			t.Errorf("record = %+v", r)
		}
		g.waitCall("wake")
	})
}
