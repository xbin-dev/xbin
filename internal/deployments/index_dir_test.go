package deployments

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// covers P5 SC-OPT-OUT — tiles opt in and out at once: every record write
// succeeds while other tiles' opt-outs remove the records directory once it
// is empty (the per-tile locks alone let an opt-out remove the directory
// between another tile's creating it and its write: ENOENT, seen as a 500
// from a pause in TestLiveReloadPauseRace), and the last opt-out still leaves
// no directory behind.
func TestRecordWritesSurviveConcurrentOptOuts(t *testing.T) {
	root := t.TempDir()
	idx := newIndex(root, func(string) string { return "" })
	if err := idx.load(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for _, tile := range []string{"apps/a", "apps/b", "apps/c", "apps/d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				rec, err := idx.commit(tile, -1, pinMain(treeA))
				if err == nil {
					err = idx.remove(tile, rec.Seq)
				}
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "data", "deployments")); !os.IsNotExist(err) {
		t.Errorf("after every tile opted out the records directory is still there (%v)", err)
	}
}
