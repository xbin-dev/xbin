package deployments

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers D119h T12 SC-FAIL-CLOSED — the plane half of
// TestNonIsolatedRefusesBackendDeployments: without isolation, pausing live
// reload on a go, node or python tile, and deploying, reloading now or
// rolling back onto its pinned primary, is refused with 409 naming
// --isolate before anything changes (no capture, no store, no record, no
// deploy), dry runs included, and the state's allowed entries say so; a
// static tile pauses and reloads everywhere; resuming live reload, which
// pins nothing, stays open, so a tile pinned under isolation returns to its
// work tree after xbind restarts without it.
func TestNonIsolatedRefusesBackendDeployments(t *testing.T) {
	f := newOpsFx(t, false)
	for _, tile := range []string{opAPI, opNode, opPy} {
		for _, dry := range []bool{false, true} {
			_, err := f.do(ownerP, OpPause, &PauseRequest{Tile: tile, DryRun: dry})
			if code, msg := errStatus(err); code != http.StatusConflict || msg != isolationMsg {
				t.Errorf("pause %s (dry %v): %d %q", tile, dry, code, msg)
			}
		}
		var e *Error
		_, err := f.do(ownerP, OpPause, &PauseRequest{Tile: tile})
		if !errors.As(err, &e) || e.Kind != KindPolicy {
			t.Errorf("pause %s: kind %+v, want policy", tile, e)
		}
		s := f.p.subjectOf(tile, util.MainDeployment)
		for _, op := range []Op{OpPause, OpReloadNow, OpDeploy, OpRollback} {
			if c := f.p.Policy(op, s); c.OK || c.Kind != KindPolicy || c.Why != isolationMsg {
				t.Errorf("%s Policy(%s) = %+v", tile, op, c)
			}
		}
		if c := f.p.Policy(OpResume, s); !c.OK {
			t.Errorf("%s Policy(resume) = %+v", tile, c)
		}
	}
	if f.st.captures != 0 {
		t.Errorf("refused pauses captured %d times", f.st.captures)
	}
	for _, rel := range []string{"data/checkpoints", "data/deployments", ".xbin/deploy"} {
		if _, err := os.Lstat(filepath.Join(f.root, rel)); err == nil {
			t.Errorf("a refused pause created %s", rel)
		}
	}
	if n, _ := f.run.counts(); n != 0 {
		t.Errorf("the runner deployed %d times", n)
	}

	// A static tile pauses and reloads without isolation.
	ans := f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
	f.write(opSite+"/index.html", "<h1>static v2</h1>")
	if ans.Deploy == nil || ans.Deploy.Result != resultOK || f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite}).Deploy == nil {
		t.Errorf("a static tile without isolation: %+v", ans.Deploy)
	}

	// A Go tile pinned under isolation, then xbind restarted without it.
	f.iso = true
	a := f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
	f.wait(opAPI, a.Deploy.ID)
	f.iso = false
	f.boot()
	seq := f.rec(opAPI).Seq
	f.write(opAPI+"/main.go", "package main // v2\n")
	for _, c := range []struct {
		op  Op
		req any
	}{
		{OpReloadNow, &ReloadNowRequest{Tile: opAPI}},
		{OpDeploy, &DeployRequest{Tile: opAPI}},
		{OpRollback, &RollbackRequest{Tile: opAPI, Checkpoint: "c:" + *f.rec(opAPI).Deployments[util.MainDeployment].Checkpoint}},
	} {
		_, err := f.do(ownerP, c.op, c.req)
		if code, msg := errStatus(err); code != http.StatusConflict || msg != isolationMsg {
			t.Errorf("%s of a pinned Go tile without isolation: %d %q", c.op, code, msg)
		}
	}
	if f.rec(opAPI).Seq != seq {
		t.Error("refused operations moved the record")
	}
	f.must(ownerP, OpResume, &ResumeRequest{Tile: opAPI})
	if f.rec(opAPI) != nil || !strings.Contains(strings.Join(f.run.changed, " "), opAPI+"/main") {
		t.Errorf("resume without isolation: record %+v, changed %v", f.rec(opAPI), f.run.changed)
	}
}
