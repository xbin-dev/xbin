package deployments

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/registry"
)

// covers PD-17 — POST /deployments/primary is refused (409) for a tile
// whose recorded partition mode has user partitions, dry run included: the
// people's data stays keyed by the old primary. A tile without partitions
// reassigns as today.
func TestPrimaryRefusedWhenPartitioned(t *testing.T) {
	f := newGovFx(t, false)
	f.write("xbin.json", `{"schema":1}`)
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	r0 := f.rec(opSite)
	recorded := registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: registry.PartitionSpec{User: true}}
	f.p.Reg.PartitionModes = func(a registry.PartitionAsk) registry.PartitionMode {
		if a.Tile == opSite {
			return recorded
		}
		return registry.PartitionMode{}
	}
	if err := f.p.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	want := opSite + " is partitioned: switching the primary would leave every person's data with main: promote instead"
	for _, dry := range []bool{true, false} {
		_, err := f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, DryRun: dry, Seq: ptr(r0.Seq)})
		wantErr(t, "reassigning a partitioned tile's primary", err, http.StatusConflict, want)
	}
	// Pending (or declined) is still partitioned data.
	recorded.State = registry.PartitionPending
	if err := f.p.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	_, err := f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Seq: ptr(r0.Seq)})
	wantErr(t, "reassigning a pending partitioned tile's primary", err, http.StatusConflict, want)
	// A record xbind can't read: R is unknown, so it may be partitioned.
	recorded = registry.PartitionMode{State: registry.PartitionInvalid, Unknown: true}
	if err := f.p.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	_, err = f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Seq: ptr(r0.Seq)})
	wantErr(t, "reassigning the primary of a tile whose mode record can't be read", err, http.StatusConflict, want)
	if f.rec(opSite).Primary != "main" {
		t.Fatal("a refused reassignment moved the primary")
	}
	// Unpartitioned: today's reassignment.
	f.p.Reg.PartitionModes = nil
	if err := f.p.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.do(ownerP, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays, Seq: ptr(r0.Seq)}); err != nil {
		t.Fatalf("an unpartitioned tile's reassignment: %v", err)
	}
	if f.rec(opSite).Primary != "dev" {
		t.Error("the unpartitioned reassignment didn't move the primary")
	}
}

// covers PD-44 01§2.7 01§2.5 — a deploy, promote or roll back onto the
// primary of code asking for another partition mode says in its dry run
// what follows: a tile that holds data pauses for a tile manager's
// decision, one that holds none takes the mode at once, a declined request
// keeps running; code that asks nothing new says nothing. A manager's
// switch writes one deploy-log line on the primary, with no checkpoint; a
// tile without a record writes none.
func TestPartitionPreflightAndLog(t *testing.T) {
	f := newOpsFx(t, true)
	if err := f.p.LogPartitionSwitch(opAPI, "user:ana", "session"); err != nil || len(f.st.logged(opAPI)) != 0 {
		t.Fatalf("a tile without a record logged a switch: %v %v", err, f.st.logged(opAPI))
	}
	f.settle(opAPI, f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI}))
	f.write(opAPI+"/xbin.json", `{"runtime":"go","partition":["user"]}`)
	holds := true
	f.p.PartitionHolds = func(string) bool { return holds }
	warn := func() string {
		t.Helper()
		res, err := f.do(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, DryRun: true})
		dr, ok := res.(DryRunAnswer)
		if err != nil || !ok {
			t.Fatalf("dry deploy: %#v %v", res, err)
		}
		return dr.Impact.Partition
	}
	if w := warn(); !strings.Contains(w, "apps/api will pause for a partition-mode decision") || !strings.Contains(w, "asks for user, it runs unpartitioned") {
		t.Errorf("a tile with data: %q", w)
	}
	holds = false
	if w := warn(); !strings.Contains(w, "holds no data, so the mode follows at once") {
		t.Errorf("a tile without data: %q", w)
	}
	f.reg.PartitionModes = func(a registry.PartitionAsk) registry.PartitionMode {
		if a.Tile == opAPI {
			return registry.PartitionMode{Request: &registry.PartitionRequest{Spec: &registry.PartitionSpec{User: true}, Declined: true}}
		}
		return registry.PartitionMode{}
	}
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if w := warn(); !strings.Contains(w, "which a manager of apps/api declined") {
		t.Errorf("a declined request: %q", w)
	}
	f.reg.PartitionModes = nil
	f.write(opAPI+"/xbin.json", `{"runtime":"go"}`)
	f.write(opAPI+"/main.go", "package main // v2\n")
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if w := warn(); w != "" {
		t.Errorf("code asking nothing new: %q", w)
	}

	if err := f.p.LogPartitionSwitch(opAPI, "user:ana", "session"); err != nil {
		t.Fatal(err)
	}
	logged := f.st.logged(opAPI)
	last := logged[len(logged)-1]
	if last.How != "partition-switch" || last.By != "user:ana" || last.Via != "session" || last.Result != resultOK ||
		last.Tree != "" || last.Deployment != "main" {
		t.Errorf("the switch's entry: %+v", last)
	}
}

// covers PD-44 01§2.7 (LAND: the latency budget) — a dry run's partition
// preflight reads the moving code's manifest without extracting its tree
// when the move's diff shows xbin.json unchanged: from the tree the primary
// runs, the same file. Its answer is the one the moving tree gives. A diff
// that names the manifest, or is cut short, reads the moving tree as before
// — and a manifest asking for a mode then warns.
func TestPartitionPreflightReadsNoNewTree(t *testing.T) {
	f := newOpsFx(t, true)
	f.settle(opAPI, f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI}))
	running := *f.rec(opAPI).Deployments["main"].Checkpoint
	f.p.PartitionHolds = func(string) bool { return true }
	var files []checkpoint.DiffFile
	truncated := false
	f.st.set(func(s *fakeStore) {
		s.diff = func(checkpoint.DiffRequest) (checkpoint.DiffResult, error) {
			return checkpoint.DiffResult{Files: files, Truncated: truncated}, nil
		}
	})
	extracted := func() map[string]int {
		f.st.mu.Lock()
		defer f.st.mu.Unlock()
		return maps.Clone(f.st.extracted)
	}
	dry := func() (warning string, newTrees int) {
		t.Helper()
		before := extracted()
		res, err := f.do(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, DryRun: true})
		dr, ok := res.(DryRunAnswer)
		if err != nil || !ok {
			t.Fatalf("dry deploy: %#v %v", res, err)
		}
		for tree, n := range extracted() {
			if tree != running && n > before[tree] {
				newTrees++
			}
		}
		return dr.Impact.Partition, newTrees
	}

	f.write(opAPI+"/main.go", "package main // v2\n")
	files = []checkpoint.DiffFile{{Path: "main.go", Status: "M", Added: 1, Removed: 1}}
	if w, n := dry(); w != "" || n != 0 {
		t.Errorf("code whose manifest stays: warning %q, %d new trees extracted (want none)", w, n)
	}
	// the manifest asks for a mode: the diff names it, the new tree is read
	f.write(opAPI+"/xbin.json", `{"runtime":"go","partition":["user"]}`)
	files = append(files, checkpoint.DiffFile{Path: "xbin.json", Status: "M", Added: 1, Removed: 1})
	if w, n := dry(); !strings.Contains(w, "will pause for a partition-mode decision") || n != 1 {
		t.Errorf("code asking for a mode: warning %q, %d new trees extracted", w, n)
	}
	// a summary cut short can't tell: the new tree is read
	files, truncated = files[:1], true
	f.write(opAPI+"/main.go", "package main // v3\n")
	if w, n := dry(); !strings.Contains(w, "will pause") || n != 1 {
		t.Errorf("a truncated diff: warning %q, %d new trees extracted", w, n)
	}
}
