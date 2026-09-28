package deployments

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers D119d D119f D119c — the plane half of TestRecordInvariantsRandomized:
// random sequences of the M1 operations (pause, resume, reload now, deploy
// a fresh capture or a named checkpoint, roll back, restart, their dry
// runs), mixed with edits of the work tree and runner failures, keep 05-model
// §4's invariants on every record the operations commit — its binding, seq,
// main existing as the primary, live reload driving "" or exactly its one
// unpinned deployment, full checkpoint ids, states — seq grows on every
// committed change and on nothing else, the plane's live reload answer
// follows the record, and a tile without a record has no file.
func TestOperationsKeepRecordInvariants(t *testing.T) {
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewSource(seed))
	for _, tile := range []string{opSite, opAPI} {
		t.Run(tile, func(t *testing.T) {
			f := newOpsFx(t, true)
			created := ""
			var lastFile []byte
			var lastSeq int64
			var trees []string
			for step := 0; step < 150; step++ {
				if rng.Intn(3) == 0 {
					f.write(tile+"/file.txt", fmt.Sprintf("edit %d\n", rng.Intn(4)))
				}
				fail := rng.Intn(6) == 0
				f.run.set(func(r *fakeRunner) {
					r.fail = nil
					if fail {
						r.fail = errors.New("build failed")
					}
				})
				dry := rng.Intn(5) == 0
				var op Op
				var req any
				switch rng.Intn(7) {
				case 0:
					op, req = OpPause, &PauseRequest{Tile: tile, DryRun: dry}
				case 1:
					op, req = OpResume, &ResumeRequest{Tile: tile, DryRun: dry}
				case 2:
					op, req = OpReloadNow, &ReloadNowRequest{Tile: tile, DryRun: dry}
				case 3:
					op, req = OpDeploy, &DeployRequest{Tile: tile, DryRun: dry}
				case 4:
					cp := "c:0000000"
					if len(trees) > 0 {
						cp = "c:" + trees[rng.Intn(len(trees))][:12]
					}
					op, req = OpDeploy, &DeployRequest{Tile: tile, Checkpoint: cp, DryRun: dry}
				case 5:
					op, req = OpRollback, &RollbackRequest{Tile: tile, DryRun: dry}
				default:
					op, req = OpDeploy, &DeployRequest{Tile: tile, Restart: true, DryRun: dry}
				}
				res, err := f.do(ownerP, op, req)
				if a, ok := res.(Answer); ok && err == nil {
					f.settle(tile, a)
				}
				if code, _ := errStatus(err); code != 0 && code != 404 && code != 409 {
					t.Fatalf("step %d: %s %+v: %v", step, op, req, err)
				}

				r := f.rec(tile)
				file, ferr := os.ReadFile(recordPath(f.root, tile))
				if r == nil {
					if ferr == nil {
						t.Fatalf("step %d: a tile without a record has a record file", step)
					}
					if dep, on := f.p.LiveReload(tile); dep != util.MainDeployment || !on {
						t.Fatalf("step %d: the zero state's live reload = %q %v", step, dep, on)
					}
					created, lastFile, lastSeq = "", nil, 0
					continue
				}
				if created == "" {
					created = r.Created
				}
				if err := opsInvariants(r, tile, created); err != nil {
					t.Fatalf("step %d (%s): %v\n%s", step, op, err, file)
				}
				if r.Primary != util.MainDeployment || len(r.Deployments) != 1 {
					t.Fatalf("step %d: an M1 operation made %+v", step, r)
				}
				if lastFile != nil {
					changed := !bytes.Equal(file, lastFile)
					switch {
					case changed && r.Seq <= lastSeq:
						t.Fatalf("step %d: the record changed and seq stayed %d", step, r.Seq)
					case !changed && r.Seq != lastSeq:
						t.Fatalf("step %d: seq moved %d → %d with no change", step, lastSeq, r.Seq)
					}
				}
				lastFile, lastSeq = file, r.Seq
				if dep, on := f.p.LiveReload(tile); dep != r.LiveReload || on != (r.LiveReload != "") {
					t.Fatalf("step %d: LiveReload = %q %v, the record %q", step, dep, on, r.LiveReload)
				}
				if cp := r.Deployments[util.MainDeployment].Checkpoint; cp != nil {
					trees = append(trees, *cp)
				}
			}
		})
	}
}

// opsInvariants is 05-model §4 over a record the operations committed,
// written out again without validate: the binding, seq, main and the
// primary existing, the grammar, live reload driving "" or exactly its one
// unpinned deployment, full checkpoint ids, states, D127m.
func opsInvariants(r *Record, tile, created string) error {
	switch {
	case r.Schema != 1 || r.Tile != tile || r.Owner != "" || r.Created != created:
		return fmt.Errorf("binding %d %q %q %q", r.Schema, r.Tile, r.Owner, r.Created)
	case r.Seq < 1 || r.NextDeploy < 0:
		return fmt.Errorf("seq %d, nextDeploy %d", r.Seq, r.NextDeploy)
	case r.Deployments[util.MainDeployment] == nil || r.Deployments[r.Primary] == nil:
		return fmt.Errorf("main or the primary %q is gone", r.Primary)
	case r.LiveReload != "" && r.Deployments[r.LiveReload] == nil:
		return fmt.Errorf("live reload drives %q, which is gone", r.LiveReload)
	case r.ProtectedPrimary && r.LiveReload == r.Primary:
		return fmt.Errorf("live reload drives the protected primary")
	}
	unpinned := 0
	for name, d := range r.Deployments {
		switch {
		case !invNameRE.MatchString(name):
			return fmt.Errorf("name %q", name)
		case d.Checkpoint == nil && name != r.LiveReload:
			return fmt.Errorf("%s is unpinned and not driven", name)
		case d.Checkpoint == nil:
			unpinned++
		case name == r.LiveReload:
			return fmt.Errorf("live reload drives pinned %s", name)
		case !fullTreeID(*d.Checkpoint):
			return fmt.Errorf("%s's checkpoint %q isn't a full id", name, *d.Checkpoint)
		case d.State != "" && d.State != "failed":
			return fmt.Errorf("%s's state %q", name, d.State)
		}
	}
	if (r.LiveReload == "") != (unpinned == 0) || unpinned > 1 {
		return fmt.Errorf("%d unpinned deployments, live reload %q", unpinned, r.LiveReload)
	}
	return nil
}
