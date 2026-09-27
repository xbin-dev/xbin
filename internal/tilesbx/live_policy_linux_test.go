//go:build linux && integration

package tilesbx

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sbx"
)

// testLivePolicy is WP-15b's part of TestLive, on live namespace sandboxes:
// reset, auto-start, an orphan's lock, and xbind's death.
func testLivePolicy(t *testing.T, le *liveEnv) {
	le.create(map[string]any{"name": "sb-r", "mode": "namespace", "mounts": []any{probeMount}})

	t.Run("reset wipes the upper, confined", func(t *testing.T) {
		le.t = t
		le.start("sb-r")
		le.probeOK("sb-r", "mkdir", "/work")
		le.probeOK("sb-r", "write", "/work/kept", "in the upper")
		d, _ := le.m.defs.get(le.k, "sb-r")
		old := le.runOf("sb-r")
		w := le.do(mgr, "POST", "/sandboxes/sb-r/reset", nil)
		le.want(w, http.StatusOK, "")
		if in := le.info(w); in.State != StateRunning || le.runOf("sb-r") == old {
			t.Fatalf("after a reset: %+v\n%s", in, le.logOf("sb-r"))
		}
		<-old.done
		if err := syscall.Kill(old.proc.Pid(), 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("the old run's process %d is still there: %v", old.proc.Pid(), err)
		}
		if rows := le.sbx.List(sbx.Filter{Kind: sbx.Tile}); len(rows) != 1 || rows[0].PID != le.runOf("sb-r").proc.Pid() {
			t.Fatalf("registry rows after a reset: %+v", rows)
		}
		if out, ex := le.probe("sb-r", "cat", "/work/kept"); ex.Code == 0 {
			t.Fatalf("the upper survived a reset: %q", out)
		}
		le.m.trash.wait()
		trash, _ := le.m.TrashDir(le.k)
		ents, _ := os.ReadDir(trash)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), d.UID) {
				t.Fatalf("the old state is still in .trash: %s", e.Name())
			}
		}
	})

	t.Run("an exec on a stopped sandbox starts it", func(t *testing.T) {
		le.t = t
		le.stop("sb-r")
		r, release, err := le.m.acquire(le.k, "sb-r", waitMaxSec*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if out, ex := execRun(t, r, []string{"/opt/probe/probe", "mkdir", "/work"}); ex.Code != 0 {
			t.Fatalf("an exec after the auto-start: %q %+v", out, ex)
		}
	})

	t.Run("an orphan's lock: the start waits, then fails", func(t *testing.T) {
		le.t = t
		le.stop("sb-r")
		le.m.lockWait = time.Second
		defer func() { le.m.lockWait = lockWait }()
		d, _ := le.m.defs.get(le.k, "sb-r")
		dir, _ := le.m.StateDir(le.k, d)
		orphan, err := lockState(dir)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		w := le.do(mgr, "POST", "/sandboxes/sb-r/start", nil)
		le.want(w, http.StatusConflict, RefState)
		if waited := time.Since(start); waited < time.Second {
			t.Fatalf("refused after %s: %s", waited, w.Body)
		}
		le.assertNothingRuns()
		orphan.Close()
		le.start("sb-r")
	})

	t.Run("xbind's death: the factory closed ends it; a new runtime finds it stopped", func(t *testing.T) {
		le.t = t
		r := le.runOf("sb-r")
		r.fac.Close() // what xbind's exit does: the agent reads EOF, syncs and exits
		in := le.waitState("sb-r", StateStopped, 10*time.Second)
		if !strings.HasPrefix(in.StateDetail, "the sandbox's agent exited") {
			t.Fatalf("stateDetail %q", in.StateDetail)
		}
		le.assertGone(r)
		again := New(Options{Root: le.m.root, Isolated: true, UIDRange: le.m.uidRange, Rootfs: le.m.rootfs,
			BxPath: le.m.bxPath, Deps: testDeps()})
		if in, ok := again.infoOf(le.k, "sb-r"); !ok || in.State != StateStopped || in.StateDetail != "" {
			t.Fatalf("after a restart: %+v", in)
		}
		if _, err := os.Stat(filepath.Join(le.m.root, "data", "sandboxes.json")); err != nil {
			t.Fatal(err)
		}
		again.trash.wait()
	})

	le.t = t
	le.want(le.do(mgr, "DELETE", "/sandboxes/sb-r", nil), http.StatusNoContent, "")
}
