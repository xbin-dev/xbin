//go:build linux && integration

// Package isolated drives tile sandboxes end to end (plans/tile-sandbox-
// runtime.md WP-21): a real `xbind --isolate` built from this tree
// (test/xbindtest), examples/sandbox-go imported as apps/sbx, its
// cap:sandboxes approved and its internet class bound, and everything
// through xbind's proxy — as the owner, and as another tile's page (a
// second consumer, by its frame token).
//
//	go test -tags=integration -count=1 -v ./test/isolated/
//	XBIN_VM_ACCEL=emulate go test -tags=integration -count=1 -v -run '^TestVM' ./test/isolated/
//
// codingsandbox_test.go does the same for the builtin manager,
// coding-sandbox, with owner auth on; with XBIN_E2E_URL (test/xbindtest
// remote.go) it drives another xbind — the QA box's test instance —
// instead (plans/tile-sandbox-runtime.md §13 has the commands).
//
// Skips without user namespaces or the rootfs; the VM tests without VM
// assets (make vm-assets). On a dev box run it with the Bash sandbox
// disabled. The base-GC test copies the rootfs: set XBIN_ITEST_DIR to a dir
// on the rootfs's filesystem (reflinks) rather than a tmpfs.
package isolated

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestMain(m *testing.M) { xbindtest.Main(m) }

// The manager's and the consumer's paths in the workspace.
const (
	mgrTile  = "apps/sbx"
	consTile = "apps/cons"
)

// manager is examples/sandbox-go on a daemon, called as one consumer: the
// owner (no header), or a tile's page (its frame token).
type manager struct {
	d   *xbindtest.Daemon
	as  []xbindtest.Header
	who string
}

// setup boots an isolated xbind and imports the manager: examples/sandbox-go
// as apps/sbx with cap:sandboxes approved and its internet class bound, and
// apps/cons, a page granted the manager's writer role. It returns the owner's
// and the consumer's view of the manager.
func setup(t *testing.T, o xbindtest.Options) (owner, cons *manager) {
	t.Helper()
	a := xbindtest.Require(t)
	d := xbindtest.Start(t, a, o)
	importManager(t, d)
	return managers(t, d)
}

// importManager imports the example and the consumer page, grants and binds
// them, and waits for the manager's backend.
func importManager(t *testing.T, d *xbindtest.Daemon) {
	t.Helper()
	d.CopyTile(t, d.A.Example("sandbox-go"), mgrTile)
	d.Grant(t, mgrTile, "cap:sandboxes", "writer")
	d.Bind(t, mgrTile, "internet", "internet")
	d.WriteTile(t, consTile, map[string]string{
		"xbin.json":  `{"uses": [{"target": "` + mgrTile + `", "role": "writer"}]}`,
		"index.html": "<!doctype html><title>a consumer</title>",
	})
	d.Grant(t, consTile, mgrTile, "writer")
	waitManager(t, d)
}

// waitManager waits for the manager's backend to build and answer.
func waitManager(t *testing.T, d *xbindtest.Daemon) {
	t.Helper()
	xbindtest.Eventually(t, 5*time.Minute, "the manager's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+mgrTile+"/runtime", nil)
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
}

func managers(t *testing.T, d *xbindtest.Daemon) (owner, cons *manager) {
	return &manager{d: d, who: "owner"},
		&manager{d: d, who: consTile, as: []xbindtest.Header{xbindtest.FrameHeader(d.FrameToken(t, consTile))}}
}

// call calls the manager's route sub ("/sandboxes/a/run").
func (m *manager) call(t *testing.T, method, sub string, body any) xbindtest.Resp {
	t.Helper()
	return m.d.Call(t, method, "/api/"+mgrTile+sub, body, m.as...)
}

// must is call, failing unless the status is want; out (non-nil) gets the
// JSON answer.
func (m *manager) must(t *testing.T, method, sub string, body any, want int, out any) xbindtest.Resp {
	t.Helper()
	r := m.call(t, method, sub, body)
	if r.Status != want {
		t.Fatalf("%s %s %s: %d %s (want %d)", m.who, method, sub, r.Status, r, want)
	}
	if out != nil {
		r.Decode(t, out)
	}
	return r
}

// The runtime's shapes, as far as the tests read them.
type (
	runtimeInfo struct {
		Enabled     bool
		Isolation   bool
		Modes       []struct{ Mode, Accel string }
		Unavailable []struct{ Mode, Reason string }
		Users       string
		Egress      []struct{ Class, Slot, Reach string }
		Caps        []string
		Limits      struct {
			Flows struct{ TCP, UDP int } `json:"flows"`
		}
	}
	sandboxInfo struct {
		Name, UID, State, StateDetail, Mode, Accel string
		Net                                        struct{ Egress, Reach string }
		Labels                                     map[string]string
		For                                        string
		Base                                       struct {
			Version  string
			Outdated bool
		}
		Users        string
		DiskBytes    int64
		Snapshots    int
		ExecsRunning int
	}
	runOutput struct {
		Head, Tail string
		Bytes      int64
	}
	runResult struct {
		ExitCode *int
		Signal   string
		TimedOut bool
		Stdout   runOutput
		Stderr   runOutput
		Output   *runOutput
	}
	execInfo struct {
		ID, State, Signal string
		ExitCode          *int
		TTY               bool
		Total             int64
	}
	outputChunk struct {
		Start, End, Total int64
		Data, Encoding    string
		State             string
		ExitCode          *int
	}
	refusal struct {
		Error, Refusal, State string
	}
	snapshotInfo struct {
		ID, Name string
		Bytes    int64
		Pending  bool
	}
)

// create defines and starts sandbox name in mode (egress "" = none).
func (m *manager) create(t *testing.T, name, mode, egress string) sandboxInfo {
	t.Helper()
	var in sandboxInfo
	m.must(t, "POST", "/sandboxes", map[string]any{"name": name, "mode": mode, "egress": egress, "memMiB": 1024, "start": true}, 201, &in)
	if in.State != "running" {
		t.Fatalf("%s: created %s isn't running: %+v", m.who, name, in)
	}
	return in
}

func (m *manager) get(t *testing.T, name string) sandboxInfo {
	t.Helper()
	var in sandboxInfo
	m.must(t, "GET", "/sandboxes/"+name, nil, 200, &in)
	return in
}

// run runs cmd in sandbox name and returns its result, failing unless it
// exited 0.
func (m *manager) run(t *testing.T, name, cmd string, extra ...map[string]any) runResult {
	t.Helper()
	body := map[string]any{"cmd": cmd, "timeoutMs": 120000}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	var res runResult
	m.must(t, "POST", "/sandboxes/"+name+"/run", body, 200, &res)
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("%s: run %q in %s: %+v", m.who, cmd, name, res)
	}
	return res
}

// exec starts a background exec.
func (m *manager) exec(t *testing.T, name string, req map[string]any) execInfo {
	t.Helper()
	var ex execInfo
	m.must(t, "POST", "/sandboxes/"+name+"/execs", req, 201, &ex)
	return ex
}

// follow reads exec id's output through the manager's Forward route, from
// offset 0 until it ends (or until the output holds until, when set).
func (m *manager) follow(t *testing.T, name, id, until string, timeout time.Duration) (string, outputChunk) {
	t.Helper()
	var out strings.Builder
	var since int64
	deadline := time.Now().Add(timeout)
	for {
		var c outputChunk
		m.must(t, "GET", fmt.Sprintf("/sandboxes/%s/execs/%s/output?since=%d&waitMs=5000&ignored=1", name, id, since), nil, 200, &c)
		out.WriteString(c.Data)
		since = c.End
		if (until != "" && strings.Contains(out.String(), until)) || (c.State != "running" && c.End >= c.Total) {
			return out.String(), c
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: exec %s's output didn't end within %s: %q (%+v)", m.who, id, timeout, out.String(), c)
		}
	}
}

// refused decodes a refusal answer.
func refusedAs(t *testing.T, r xbindtest.Resp) refusal {
	t.Helper()
	var e refusal
	_ = json.Unmarshal(r.Body, &e)
	return e
}

func hasMode(rt runtimeInfo, mode string) bool {
	for _, m := range rt.Modes {
		if m.Mode == mode {
			return true
		}
	}
	return false
}

// statUID is a file's owner on the host.
func statUID(fi os.FileInfo) int { return int(fi.Sys().(*syscall.Stat_t).Uid) }
