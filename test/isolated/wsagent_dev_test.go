//go:build linux && integration

package isolated

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// A development xbind (--dev) has no prebuilt backend beside it and
// compiles the workspace agent from the checkout — the embedded tree,
// through the tile build, in the build sandbox — the first time someone
// opens it, never before. It then runs as an install's does: a person's
// partition, in workspace mode. (test/isolated/wsagent_test.go runs the
// release's prebuilt backend, which an install never compiles.)
func TestWorkspaceAgentDevelopmentBuild(t *testing.T) {
	a := xbindtest.Require(t)
	if a.Gocryptfs == "" {
		xbindtest.Skip(t, "no gocryptfs: people's partitions keep their data in encrypted volumes")
	}
	if _, err := os.Stat(filepath.Join(a.Bin, "sealed")); err == nil {
		t.Fatal("the plain assets have a bin/sealed: this test wants an xbind without prebuilt backends")
	}
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Env: []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault"}})
	e := &wsEnv{sealedEnv: &sealedEnv{d: d, sess: map[string]string{}, frames: map[string]xbindtest.Header{}}, t: t}
	row := d.Must(t, "GET", "/api/xbin/sealed", nil, 200).String()
	if !strings.Contains(row, `"path":"`+wsAgent+`"`) || !strings.Contains(row, `"build":"development"`) || strings.Contains(row, `"hold"`) {
		t.Fatalf("GET /sealed on a development xbind: %s", row)
	}
	built := func() []string {
		var out []string
		_ = filepath.WalkDir(filepath.Join(d.WS, ".xbin", "build"), func(p string, _ os.DirEntry, err error) error {
			if err == nil && strings.Contains(filepath.Base(p), "wsagent") {
				out = append(out, p)
			}
			return nil
		})
		return out
	}
	e.person("alice", "user", nil)
	d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true, "who": "everyone"}, 200)
	if b := built(); len(b) != 0 {
		t.Errorf("something of the agent was built before anyone opened it: %v", b)
	}
	fr := e.frame(t, wsAgent, "alice")
	var body string
	xbindtest.Eventually(t, 12*time.Minute, "alice's agent, compiled from the checkout, answers /ws/state", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+wsAgent+"/ws/state", nil, fr)
		body = r.String()
		if r.Status == 403 || r.Status == 404 {
			t.Fatalf("GET /ws/state: %d %s", r.Status, r)
		}
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
	if !strings.Contains(body, `"mode":"workspace"`) || !strings.Contains(body, `"user":"alice"`) {
		t.Fatalf("GET /ws/state: %s", body)
	}
	if r := d.Call(t, "GET", "/c/"+wsAgent+"/boot.js", nil, fr); r.Status != 200 || !strings.Contains(r.String(), "ws/page.js") {
		t.Errorf("its page's boot script: %d %.200s", r.Status, r)
	}
	// a normal instance's routes are not there
	for _, c := range [][2]string{{"GET", "/runs"}, {"GET", "/config"}, {"GET", "/classes"}, {"PUT", "/halt"}} {
		if r := d.Call(t, c[0], "/api/"+wsAgent+c[1], nil, fr); r.Status != 404 && r.Status != 405 {
			t.Errorf("%s %s on a workspace agent: %d %.200s", c[0], c[1], r.Status, r)
		}
	}
}
