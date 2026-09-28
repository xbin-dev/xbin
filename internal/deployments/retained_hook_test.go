package deployments

import (
	"reflect"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers D119e SC-ROLLBACK — the runner's Retained hook, which the env-layer GC
// reads (WP-67's A4): a tile no record governs retains nothing and answers
// ok; once a record governs it, RetainedTrees answers exactly what the
// artifact pruning keeps, each deployment's current checkpoint and its three
// newest roll-back targets; a deploy log that can't be read answers not ok,
// so the GC keeps every layer rather than guess.
func TestRetainedTreesHook(t *testing.T) {
	f, st, _ := newRetainFx(t)
	if trees, ok := f.p.RetainedTrees(opAPI); trees != nil || !ok {
		t.Fatalf("zero state: RetainedTrees = %v, %v; want nil, true", trees, ok)
	}
	var deployed []string
	for i, v := range []string{"v1", "v2", "v3", "v4", "v5"} {
		f.write(opAPI+"/main.go", "package main // "+v+"\n")
		op, req := OpReloadNow, any(&ReloadNowRequest{Tile: opAPI})
		if i == 0 {
			op, req = OpPause, &PauseRequest{Tile: opAPI}
		}
		if e := f.wait(opAPI, f.must(ownerP, op, req).Deploy.ID); e.Result != resultOK {
			t.Fatalf("deploy %s: %+v", v, e)
		}
		deployed = append(deployed, f.current(opAPI, util.MainDeployment))
	}
	trees, ok := f.p.RetainedTrees(opAPI)
	if want := sortedTrees(deployed[1:]); !ok || !reflect.DeepEqual(trees, want) {
		t.Errorf("RetainedTrees = %v, %v; want the current and three roll-back targets %v, true", trees, ok, want)
	}
	st.fakeStore.mu.Lock()
	st.noLog = true
	st.fakeStore.mu.Unlock()
	if trees, ok := f.p.RetainedTrees(opAPI); trees != nil || ok {
		t.Errorf("unreadable deploy log: RetainedTrees = %v, %v; want nil, false", trees, ok)
	}
}
