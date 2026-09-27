package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
)

// covers SC-LIVE-RELOAD-PAUSE T1 — a capture while an editor saves: git
// meets a temporary file that was renamed away between listing its
// directory and reading it, as a fatal "unable to stat" in the add stage or
// as "open(…)"/"unable to index file" lines that --ignore-errors lets pass.
// Either way the capture runs again and succeeds with the settled tree,
// instead of failing a pause with a 500 or refusing the file as unreadable
// (seen in TestLiveReloadPauseRace under XBIN_TEST_FULL=1). A file that
// stays gone through every retry keeps today's answer.
func TestCaptureRetriesFilesVanishedMidRead(t *testing.T) {
	needGit(t)
	const gone = ".probe.txt.tmp-6070676979329666611"
	isCapture := func(c confine.Cmd) bool { return len(c.Argv) > 2 && strings.Contains(c.Argv[2], "g add -A -f") }
	for _, tc := range []struct {
		name   string
		faults int // capture runs that meet the vanished file
		fail   func(c confine.Cmd) (confine.Result, error, bool)
		ok     bool
	}{
		{name: "fatal stat", faults: 2, ok: true, fail: func(confine.Cmd) (confine.Result, error, bool) {
			return confine.Result{}, &confine.ExitError{Code: 92, Stderr: "fatal: unable to stat '" + gone + "': No such file or directory"}, true
		}},
		{name: "unable to index", faults: 1, ok: true},
		{name: "gone through every retry", faults: vanishRetries + 1, ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := testStore(t)
			src := tile(t, s, "apps/a", map[string]string{"a.txt": "hello\n", ".probe.txt": "r1\n"})
			left := tc.faults
			s.run = func(ctx context.Context, c confine.Cmd) (confine.Result, error) {
				if !isCapture(c) || left == 0 {
					return rec.run(ctx, c)
				}
				left--
				if tc.fail != nil {
					res, err, _ := tc.fail(c)
					return res, err
				}
				res, err := rec.run(ctx, c)
				q := c.Argv[len(c.Argv)-1] // captureScript's $Q
				f, ferr := os.OpenFile(filepath.Join(q, "add.err"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
				if ferr != nil {
					t.Fatal(ferr)
				}
				_, _ = f.WriteString("error: open(\"" + gone + "\"): No such file or directory\nerror: unable to index file '" + gone + "'\n")
				f.Close()
				return res, err
			}
			res, err := s.Capture(context.Background(), CaptureRequest{Source: src, By: "user:ana", Create: true})
			if !tc.ok {
				var r *Refusal
				if err == nil || !errors.As(err, &r) || r.Rule != RuleUnreadable {
					t.Fatalf("a file gone through every retry: %v (%+v); want today's unreadable refusal", err, res)
				}
				return
			}
			if err != nil || res.Checkpoint.Hash == "" {
				t.Fatalf("capture under a vanishing file: %v (%+v)", err, res)
			}
			if left != 0 {
				t.Errorf("%d faults left unused", left)
			}
		})
	}
	if vanished(nil, errors.New("checkpoint store: open x: no such file or directory")) {
		t.Error("a Go ENOENT outside git's add stage counted as a vanished work-tree file")
	}
}
