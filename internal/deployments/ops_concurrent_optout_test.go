package deployments

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// covers P5 SC-OPT-OUT SC-LIVE-RELOAD-PAUSE — tiles pause and resume live
// reload at once through the plane: every operation succeeds, since no
// record or deploy-journal write meets the records directory another tile's
// resume removed (a 500 from a pause in TestLiveReloadPauseRace), and the
// last resume still leaves no data/deployments behind.
func TestConcurrentOptInsAndOutsAcrossTiles(t *testing.T) {
	f := newOpsFx(t, true)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, tile := range []string{opSite, opAPI, opNode, opPy} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				res, err := f.do(ownerP, OpPause, &PauseRequest{Tile: tile})
				if err != nil {
					errs <- fmt.Errorf("%s pause %d: %w", tile, i, err)
					return
				}
				if a, ok := res.(Answer); ok && a.Deploy != nil {
					if _, err := f.p.Entry(context.Background(), tile, a.Deploy.ID, 10*time.Second); err != nil {
						errs <- fmt.Errorf("%s pause %d's deploy: %w", tile, i, err)
						return
					}
				}
				if _, err := f.do(ownerP, OpResume, &ResumeRequest{Tile: tile}); err != nil {
					errs <- fmt.Errorf("%s resume %d: %w", tile, i, err)
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
	if _, err := os.Lstat(filepath.Join(f.root, "data", "deployments")); !os.IsNotExist(err) {
		t.Errorf("after every tile resumed the records directory is still there (%v)", err)
	}
}
