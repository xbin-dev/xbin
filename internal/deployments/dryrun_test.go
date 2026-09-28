package deployments

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covers D119c PO-7 T10 L20 — dry runs never create a checkpoint store on a
// zero-state tile (05-model §5, 07-runtime §2.11): a dry run of pausing live
// reload computes the impact from a stat-only walk (impact.code null, the
// caps checked) and captures nothing; one refused by the caps, by isolation
// (D119h) or by authority is judged exactly as for real; the operations that
// aren't opt-ins answer 409 on such a tile. Afterwards the workspace has no
// data/checkpoints, data/deployments or .xbin/deploy. The store appears
// only with a committed opt-in; a dry run on a tile with a record takes its
// checkpoint and changes no record.
func TestDryRunsCreateNoStore(t *testing.T) {
	f := newGitOpsFx(t, true)
	noState := func(step string) {
		t.Helper()
		for _, rel := range []string{"data/checkpoints", "data/deployments", ".xbin/deploy"} {
			if _, err := os.Lstat(filepath.Join(f.root, rel)); err == nil {
				t.Fatalf("%s: %s exists", step, rel)
			}
		}
	}

	for _, tile := range []string{opSite, opAPI} {
		res, err := f.do(ownerP, OpPause, &PauseRequest{Tile: tile, DryRun: true})
		dr, ok := res.(DryRunAnswer)
		if err != nil || !ok {
			t.Fatalf("dry pause of %s = %#v, %v", tile, res, err)
		}
		if dr.Impact.Code != nil || !dr.Impact.PausesLiveReload || dr.Impact.Data != "none" || f.rec(tile) != nil {
			t.Errorf("dry pause of %s: impact %+v, record %+v", tile, dr.Impact, f.rec(tile))
		}
		noState("a dry pause of " + tile)
	}

	// Refused dry runs: the caps (a stat-only walk), isolation, authority,
	// and the operations a tile without a record doesn't take.
	f.st.real.Caps.Entries = 1
	_, err := f.do(ownerP, OpPause, &PauseRequest{Tile: opAPI, DryRun: true})
	if code, msg := errStatus(err); code != http.StatusConflict || !strings.Contains(msg, "refused") {
		t.Errorf("a dry pause over the caps: %d %s", code, msg)
	}
	f.st.real.Caps.Entries = 200_000
	f.iso = false
	_, err = f.do(ownerP, OpPause, &PauseRequest{Tile: opNode, DryRun: true})
	if code, msg := errStatus(err); code != http.StatusConflict || msg != isolationMsg {
		t.Errorf("a dry pause of a backend without isolation: %d %s", code, msg)
	}
	f.iso = true
	_, err = f.do(userP("rita", opSite, "read"), OpPause, &PauseRequest{Tile: opSite, DryRun: true})
	if code, _ := errStatus(err); code != http.StatusForbidden {
		t.Errorf("a reader's dry pause: %v", err)
	}
	for _, c := range []struct {
		op  Op
		req any
	}{
		{OpResume, &ResumeRequest{Tile: opSite, DryRun: true}},
		{OpReloadNow, &ReloadNowRequest{Tile: opSite, DryRun: true}},
		{OpDeploy, &DeployRequest{Tile: opSite, DryRun: true}},
		{OpRollback, &RollbackRequest{Tile: opSite, DryRun: true}},
	} {
		_, err := f.do(ownerP, c.op, c.req)
		if code, msg := errStatus(err); code != http.StatusConflict || !strings.HasSuffix(msg, "has no deployments yet: pause live reload or add a deployment first") {
			t.Errorf("dry %s on a zero-state tile: %d %s", c.op, code, msg)
		}
	}
	noState("the refused dry runs")

	// The committed opt-in creates the store; a dry run then captures into
	// it (content-addressed) and commits nothing.
	f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
	if !f.st.real.Exists(opSite) || f.st.real.Exists(opAPI) {
		t.Fatalf("stores after one committed pause: site %v, api %v", f.st.real.Exists(opSite), f.st.real.Exists(opAPI))
	}
	seq := f.rec(opSite).Seq
	f.write(opSite+"/index.html", "<h1>dry</h1>")
	res, err := f.do(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite, DryRun: true})
	dr, _ := res.(DryRunAnswer)
	if err != nil || dr.Impact.Code == nil || !strings.HasPrefix(dr.Impact.Code.To, "c:") {
		t.Fatalf("dry reload now = %+v, %v", dr.Impact, err)
	}
	if _, err := f.st.real.Resolve(t.Context(), opSite, dr.Impact.Code.To); err != nil {
		t.Errorf("the dry run's checkpoint %s isn't in the store: %v", dr.Impact.Code.To, err)
	}
	if f.rec(opSite).Seq != seq {
		t.Errorf("a dry run moved seq %d → %d", seq, f.rec(opSite).Seq)
	}
	// The reviewed checkpoint is then exactly what reload now ships.
	ans := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opSite, Expect: dr.Impact.Code.To})
	if ans.Deploy == nil || ans.Deploy.Checkpoint != dr.Impact.Code.To {
		t.Errorf("reload now with the dry run's expect shipped %+v, want %s", ans.Deploy, dr.Impact.Code.To)
	}
}
