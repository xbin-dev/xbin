//go:build linux && integration

// Run with: go test -tags=integration ./internal/tilesbx/
// Needs unprivileged user namespaces (skips without them). Live namespace
// tile sandboxes, driven through the /api/xbin routes as a manager tile
// holding cap:sandboxes (apps/mgr, the test's fixture tile): a static `bx`
// built here is the agent, a static probe mounted from the tile's own code
// ({source:true}) runs inside. It runs over a minimal lower with the kernel
// overlay (CI, no .rootfs), again with fuse-overlayfs when one is found
// ($XBIN_FUSE_OVERLAYFS, the repo's bin/, PATH), and over the rootfs
// ($XBIN_TEST_ROOTFS or the repo's .rootfs) when present.
//
// The cgroup checks (the leaf and its limits, the OOM kill of a session)
// need a delegated cgroup holding only the test binary; elsewhere they skip:
//
//	go test -c -tags=integration -o /tmp/tilesbx.test ./internal/tilesbx/
//	systemd-run --user --scope -p Delegate=yes /tmp/tilesbx.test -test.v -test.run TestLive
package tilesbx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// TestMain doubles as the sandbox's re-exec init.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2]) // never returns
	}
	os.Exit(m.Run())
}

var (
	liveBinOnce sync.Once
	liveBin     string // bx and the probe, built once
	liveBinErr  error
	liveCgroup  *cgroup.Manager // xbind's delegated cgroup, when the test runs in one
)

func liveBinaries(t *testing.T) string {
	liveBinOnce.Do(func() {
		liveBin, liveBinErr = os.MkdirTemp("", "tilesbx-live-*")
		if liveBinErr != nil {
			return
		}
		for out, pkg := range map[string]string{"bx": "../../cmd/bx", "probe": "./testdata/sbxprobe"} {
			cmd := exec.Command("go", "build", "-o", filepath.Join(liveBin, out), pkg)
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // static: the sandbox may have no libc
			if b, err := cmd.CombinedOutput(); err != nil {
				liveBinErr = fmt.Errorf("build %s: %v\n%s", pkg, err, b)
				return
			}
		}
		liveCgroup = cgroup.New()
	})
	if liveBinErr != nil {
		t.Fatal(liveBinErr)
	}
	return liveBin
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return p != "" && err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

func findFuseOverlayfs() string {
	for _, p := range []string{os.Getenv("XBIN_FUSE_OVERLAYFS"), "../../bin/fuse-overlayfs"} {
		if abs, _ := filepath.Abs(p); executable(abs) {
			return abs
		}
	}
	if p, err := exec.LookPath("fuse-overlayfs"); err == nil {
		return p
	}
	return ""
}

func TestLive(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	bin := liveBinaries(t)
	rootfs := os.Getenv("XBIN_TEST_ROOTFS")
	if rootfs == "" {
		rootfs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(rootfs, "bin", "sh")); err != nil {
		rootfs = ""
	} else {
		confine.Configure(rootfs) // the state removals run confined, as under --isolate
	}
	fuse := findFuseOverlayfs()
	t.Run("minimal", func(t *testing.T) {
		t.Setenv("XBIN_FUSE_OVERLAYFS", "none") // the kernel overlay
		testLive(t, bin, "")
	})
	t.Run("minimal-fuse", func(t *testing.T) {
		if fuse == "" {
			t.Skip("no fuse-overlayfs")
		}
		t.Setenv("XBIN_FUSE_OVERLAYFS", fuse)
		testLive(t, bin, "")
	})
	t.Run("rootfs", func(t *testing.T) {
		if rootfs == "" {
			t.Skip("no rootfs ($XBIN_TEST_ROOTFS or .rootfs)")
		}
		if fuse == "" {
			fuse = "none"
		}
		t.Setenv("XBIN_FUSE_OVERLAYFS", fuse)
		testLive(t, bin, rootfs)
	})
}

// liveEnv is a runtime running real namespace sandboxes.
type liveEnv struct {
	*testEnv
	sbx       *sbx.Registry
	k         Key
	work, ro  string // the fixture tile's filesystem resources
	books     int
	booksHeld int
	bookMu    sync.Mutex
}

func newLiveEnv(t *testing.T, bin, rootfs string) *liveEnv {
	t.Helper()
	root := t.TempDir()
	tile := filepath.Join(root, "apps", "mgr")
	le := &liveEnv{sbx: sbx.New(), k: Key{Tile: "apps/mgr"},
		work: filepath.Join(root, "res", "work"), ro: filepath.Join(root, "res", "ro")}
	for _, d := range []string{tile, le.work, le.ro} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probe, _ := os.ReadFile(filepath.Join(bin, "probe"))
	if err := os.WriteFile(filepath.Join(tile, "probe"), probe, 0o755); err != nil {
		t.Fatal(err)
	}
	if rootfs == "" { // a minimal lower: a base version, nothing else
		rootfs = filepath.Join(t.TempDir(), "lower")
		if err := os.MkdirAll(filepath.Join(rootfs, "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootfs, layers.VersionFile), []byte("min-1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rangeOK, _ := sandbox.IDMapStatus(os.Getuid(), os.Getgid())
	le.testEnv = newEnv(t, func(o *Options) {
		o.Root, o.UIDRange, o.Rootfs, o.BxPath = root, rangeOK, rootfs, filepath.Join(bin, "bx")
		o.DiskUsage = confine.DiskUsage // the real measurement (newEnv's is a fake)
		o.Deps.Mounts = fakeMounts{
			"apps/mgr res:apps/mgr/work": {Src: le.work, Role: "writer", Kind: "filesystem", Ready: true},
			"apps/mgr res:apps/mgr/ro":   {Src: le.ro, Role: "reader", Kind: "filesystem", Ready: true},
		}
		o.Deps.Sbx, o.Deps.Cgroup = le.sbx, liveCgroup
	})
	admit := le.m.reserve // the real admission, counted
	le.m.reserve = func(k Key, d *Def) (func(), error) {
		release, err := admit(k, d)
		if err != nil {
			return nil, err
		}
		le.bookMu.Lock()
		le.books++
		le.booksHeld++
		le.bookMu.Unlock()
		return func() { release(); le.bookMu.Lock(); le.booksHeld--; le.bookMu.Unlock() }, nil
	}
	if !confine.Isolated() {
		le.m.trash.remove = func(_ context.Context, dir string) error { return os.RemoveAll(dir) }
	}
	t.Cleanup(func() { le.m.StopAll("the test ended"); le.m.trash.wait() })
	return le
}

// start starts a sandbox through the route and wants it running.
func (le *liveEnv) start(name string) Info {
	le.t.Helper()
	w := le.do(mgr, "POST", "/sandboxes/"+name+"/start", nil)
	le.want(w, http.StatusOK, "")
	in := le.info(w)
	if in.State != StateRunning {
		le.t.Fatalf("start %s: %+v\n%s", name, in, le.logOf(name))
	}
	return in
}

// stop stops a sandbox through the route and wants it stopped, with nothing left.
func (le *liveEnv) stop(name string) {
	le.t.Helper()
	r := le.runOf(name)
	w := le.do(mgr, "POST", "/sandboxes/"+name+"/stop", nil)
	le.want(w, http.StatusOK, "")
	if in := le.info(w); in.State != StateStopped || in.StateDetail != "" {
		le.t.Fatalf("stop %s: %+v", name, in)
	}
	le.assertGone(r)
}

func (le *liveEnv) runOf(name string) *run {
	le.m.mu.Lock()
	defer le.m.mu.Unlock()
	if b := le.m.live[le.k][name]; b != nil {
		return b.run
	}
	return nil
}

func (le *liveEnv) logOf(name string) string {
	le.m.mu.Lock()
	defer le.m.mu.Unlock()
	if b := le.m.live[le.k][name]; b != nil {
		return b.log.String()
	}
	return ""
}

func (le *liveEnv) waitState(name, state string, within time.Duration) Info {
	le.t.Helper()
	deadline := time.Now().Add(within)
	for {
		in, _ := le.m.infoOf(le.k, name)
		if in.State == state {
			return in
		}
		if time.Now().After(deadline) {
			le.t.Fatalf("sandbox %s is %s (%s), want %s within %s", name, in.State, in.StateDetail, state, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertGone checks nothing of r is left: its process, its book, its
// registry row, its leaf, its relay's flows, its TUN and its factory.
func (le *liveEnv) assertGone(r *run) {
	le.t.Helper()
	select {
	case <-r.done:
	case <-time.After(15 * time.Second):
		le.t.Fatal("no teardown")
	}
	if err := syscall.Kill(r.proc.Pid(), 0); !errors.Is(err, syscall.ESRCH) {
		le.t.Errorf("its process %d is still there: %v", r.proc.Pid(), err)
	}
	up := le.m.running(nil) // other sandboxes may run on
	le.bookMu.Lock()
	held := le.booksHeld
	le.bookMu.Unlock()
	if held != len(up) {
		le.t.Errorf("books held: %d, with %d running", held, len(up))
	}
	for _, row := range le.sbx.List(sbx.Filter{Kind: sbx.Tile}) {
		if row.ID == RegistryID(r.k, r.def.Name) {
			le.t.Errorf("its registry row: %+v", row)
		}
	}
	if r.leaf != "" && le.m.cg.Populated(r.leaf) {
		le.t.Errorf("its leaf %s is still populated", r.leaf)
	}
	if r.leaf != "" {
		if _, ok := le.m.cg.Usage(r.leaf); ok {
			le.t.Errorf("its leaf %s is still there", r.leaf)
		}
	}
	if used, _ := le.m.FlowBudget(); used != 0 && len(up) == 0 {
		le.t.Errorf("flows held: %d", used)
	}
	if _, err := r.fac.Dial(); err == nil {
		le.t.Error("its factory is still open")
	}
}

// probe runs the probe in a sandbox: its combined output and how it ended.
func (le *liveEnv) probe(name string, args ...string) (string, SessionExit) {
	le.t.Helper()
	r := le.runOf(name)
	if r == nil {
		le.t.Fatalf("%s isn't running", name)
	}
	return execRun(le.t, r, append([]string{"/opt/probe/probe"}, args...))
}

func (le *liveEnv) probeOK(name string, args ...string) string {
	le.t.Helper()
	out, ex := le.probe(name, args...)
	if ex.Code != 0 || ex.Error != "" {
		le.t.Fatalf("probe %q in %s: %q %+v", args, name, out, ex)
	}
	return out
}

var probeMount = map[string]any{"source": true, "at": "/opt/probe"}

func testLive(t *testing.T, bin, rootfs string) {
	le := newLiveEnv(t, bin, rootfs)
	le.create(map[string]any{"name": "sb-1", "mode": "namespace", "mounts": []any{probeMount,
		map[string]any{"res": "res:apps/mgr/work", "at": "/mnt/work"},
		map[string]any{"res": "res:apps/mgr/ro", "at": "/mnt/ro"}}})

	t.Run("start, exec, stop, start: the upper persists", func(t *testing.T) {
		le.t = t
		le.start("sb-1")
		r := le.runOf("sb-1")
		rows := le.sbx.List(sbx.Filter{Kind: sbx.Tile})
		if len(rows) != 1 || rows[0].Name != "sb-1" || rows[0].PID != r.proc.Pid() {
			t.Fatalf("the registry row: %+v", rows)
		}
		le.probeOK("sb-1", "mkdir", "/work")
		if out, ex := execRun(t, r, []string{"/opt/probe/probe", "write", "/work/kept", "in the upper"}); ex.Code != 0 {
			t.Fatalf("write: %q %+v\n%s", out, ex, le.logOf("sb-1"))
		}
		d, _ := le.m.defs.get(le.k, "sb-1")
		cur, _ := le.m.CurDir(le.k, d)
		if b, err := os.ReadFile(filepath.Join(cur, "upper", "work", "kept")); err != nil || string(b) != "in the upper" {
			t.Fatalf("the upper: %q %v", b, err)
		}
		le.probeOK("sb-1", "write", "/mnt/work/x", "shared")
		if b, _ := os.ReadFile(filepath.Join(le.work, "x")); string(b) != "shared" {
			t.Fatalf("the writer mount: %q", b)
		}
		le.stop("sb-1")
		le.start("sb-1")
		if out := le.probeOK("sb-1", "cat", "/work/kept"); out != "in the upper" {
			t.Fatalf("after a restart: %q", out)
		}
		if st, _ := layers.Read(cur); st.Base == "" || (st.Overlay != layers.OverlayFuse && st.Overlay != layers.OverlayKernel) {
			t.Fatalf("the stamps: %+v", st)
		}
	})

	t.Run("egress none: reset and REFUSED at once", func(t *testing.T) {
		le.t = t
		out := le.probeOK("sb-1", "tcp", "1.1.1.1:80")
		if f := strings.Fields(out); len(f) < 2 || f[0] != "reset" || atoi(f[1]) >= 100 {
			t.Fatalf("tcp: %q", out)
		}
		out = le.probeOK("sb-1", "dns", "example.com", "10.0.2.3:53")
		if f := strings.Fields(out); len(f) < 3 || f[0] != "rcode" || f[1] != "5" || atoi(f[2]) >= 100 {
			t.Fatalf("dns: %q", out)
		}
		out = le.probeOK("sb-1", "tcp", "10.0.2.2:8642") // the gateway: a dead end
		if !strings.HasPrefix(out, "reset") {
			t.Fatalf("the gateway: %q", out)
		}
	})

	t.Run("lockdown", func(t *testing.T) {
		le.t = t
		if out := le.probeOK("sb-1", "unshare"); !strings.HasPrefix(out, "refused") {
			t.Fatalf("unshare -U: %q", out)
		}
		if out, ex := le.probe("sb-1", "write", "/mnt/ro/x", "no"); ex.Code == 0 || !strings.Contains(out, "read-only") {
			t.Fatalf("a reader's mount took a write: %q %+v", out, ex)
		}
		if _, err := os.Stat(filepath.Join(le.ro, "x")); err == nil {
			t.Fatal("the write reached the resource")
		}
	})

	t.Run("cgroup leaf", func(t *testing.T) {
		le.t = t
		r := le.runOf("sb-1")
		if le.m.cg == nil {
			t.Skip("no delegated cgroup (see the file's header)")
		}
		if p, ok := r.proc.(*nsProc); !ok || !p.inCg {
			t.Fatal("not started into its leaf (UseCgroupFD)")
		}
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", r.proc.Pid()))
		path := strings.TrimSpace(strings.TrimPrefix(string(b), "0::"))
		if !strings.HasSuffix(path, "/comp-"+parentName(le.m.root)+"/comp-"+Leaf(le.k, "sb-1")) {
			t.Fatalf("its cgroup: %s", path)
		}
		for f, want := range map[string]string{"memory.max": strconv.Itoa((2048 + leafOverheadMiB) << 20),
			"cpu.max": "200000 100000", "memory.swap.max": "0", "pids.max": "4096"} {
			if got, _ := os.ReadFile(filepath.Join("/sys/fs/cgroup", path, f)); strings.TrimSpace(string(got)) != want {
				t.Errorf("its %s: %q, want %q", f, got, want)
			}
		}
	})

	t.Run("a session past memory.max is the one killed", func(t *testing.T) {
		le.t = t
		if le.m.cg == nil {
			t.Skip("no delegated cgroup (see the file's header)")
		}
		// the smallest sandbox: 256 MiB (+128 for the agent), no swap past it
		le.create(map[string]any{"name": "sb-oom", "mode": "namespace", "memMiB": 256, "mounts": []any{probeMount}})
		le.start("sb-oom")
		defer func() {
			if r := le.runOf("sb-oom"); r != nil {
				_ = le.m.Stop(le.k, "sb-oom", "")
			}
		}()
		start := time.Now()
		out, ex := le.probe("sb-oom", "alloc", "1024")
		if ex.Signal != int(syscall.SIGKILL) {
			t.Fatalf("the allocation ended %+v: %q", ex, out)
		}
		t.Logf("the allocation was killed after %s", time.Since(start))
		if in, _ := le.m.infoOf(le.k, "sb-oom"); in.State != StateRunning {
			t.Fatalf("the sandbox: %+v", in)
		}
		// the victim's memory goes back as the OOM reaper gets to it
		for i := 0; ; i++ {
			out, ex := le.probe("sb-oom", "mkdir", "/still/here")
			if ex.Code == 0 {
				break
			}
			if i == 20 {
				t.Fatalf("the sandbox runs nothing after the kill: %q %+v", out, ex)
			}
			time.Sleep(100 * time.Millisecond)
		}
		r := le.runOf("sb-oom")
		if n := le.m.cg.OOMKills(r.leaf); n < 1 {
			t.Fatalf("the leaf counts %d OOM kills", n)
		}
		le.stop("sb-oom")
	})

	t.Run("mount refusals", func(t *testing.T) {
		le.t = t
		// a symlink anywhere in a sub-path
		if err := os.Symlink("/etc", filepath.Join(le.work, "link")); err != nil {
			t.Fatal(err)
		}
		le.create(map[string]any{"name": "sb-sub", "mode": "namespace", "mounts": []any{probeMount,
			map[string]any{"res": "res:apps/mgr/work", "path": "link", "at": "/mnt/l"}}})
		w := le.do(mgr, "POST", "/sandboxes/sb-sub/start", nil)
		le.want(w, http.StatusOK, "")
		if in := le.info(w); in.State != StateStopped || !strings.Contains(in.StateDetail, "didn't start") {
			t.Fatalf("a symlinked sub-path: %+v", in)
		} else {
			t.Logf("a symlinked sub-path: %s", in.StateDetail)
		}
		// a symlink planted in the upper at a mount point
		host := t.TempDir()
		le.create(map[string]any{"name": "sb-plant", "mode": "namespace", "mounts": []any{probeMount,
			map[string]any{"res": "res:apps/mgr/work", "at": "/mnt/planted/x"}}})
		d, _ := le.m.defs.get(le.k, "sb-plant")
		cur, _ := le.m.CurDir(le.k, d)
		if err := os.MkdirAll(filepath.Join(cur, "upper", "mnt"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(host, filepath.Join(cur, "upper", "mnt", "planted")); err != nil {
			t.Fatal(err)
		}
		w = le.do(mgr, "POST", "/sandboxes/sb-plant/start", nil)
		le.want(w, http.StatusOK, "")
		if in := le.info(w); in.State != StateStopped || !strings.Contains(in.StateDetail, "didn't start") {
			t.Fatalf("a planted symlink: %+v", in)
		} else {
			t.Logf("a planted symlink: %s", in.StateDetail)
		}
		if ents, _ := os.ReadDir(host); len(ents) != 0 {
			t.Fatalf("the start made %v on the host", ents)
		}
		le.assertNothingRuns()
	})

	t.Run("it ends on its own", func(t *testing.T) {
		le.t = t
		// the agent killed from outside
		r := le.runOf("sb-1")
		if err := syscall.Kill(r.proc.Pid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		in := le.waitState("sb-1", StateStopped, 10*time.Second)
		if !strings.HasPrefix(in.StateDetail, "the sandbox's agent exited") {
			t.Fatalf("stateDetail %q", in.StateDetail)
		}
		le.assertGone(r)
		// its root filesystem killed from outside
		le.start("sb-1")
		r = le.runOf("sb-1")
		if !usesFuse(r) {
			t.Log("the kernel overlay: no fuse-overlayfs to kill")
			le.stop("sb-1")
			return
		}
		pid := fusePid(r.proc.Pid())
		if pid == 0 {
			t.Fatalf("no fuse-overlayfs under the agent %d", r.proc.Pid())
		}
		start := time.Now()
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		in = le.waitState("sb-1", StateStopped, 2*time.Second)
		if in.StateDetail != "the sandbox's root filesystem (fuse-overlayfs) died" {
			t.Fatalf("stateDetail %q", in.StateDetail)
		}
		t.Logf("stopped %s after fuse-overlayfs was killed", time.Since(start))
		le.assertGone(r)
	})

	t.Run("a file made in the root answers; a stopped root still stops", func(t *testing.T) {
		le.t = t
		le.start("sb-1")
		r := le.runOf("sb-1")
		if !usesFuse(r) {
			le.stop("sb-1")
			t.Skip("the kernel overlay: no FUSE server")
		}
		// A file operation's write, made by a thread of the agent itself.
		write := func(p string) <-chan string {
			answered := make(chan string, 1)
			c, err := r.client().File(proto.FileOp{Op: "write", Path: p})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			_ = proto.WriteFrame(c.Writer(), []byte("x"))
			_ = proto.WriteFrame(c.Writer(), nil)
			go func() {
				var res proto.FileResult
				if err := c.RecvMax(&res, proto.MaxResult); err != nil {
					answered <- err.Error()
				} else if !res.OK {
					answered <- fmt.Sprintf("%+v", res)
				}
				close(answered)
			}()
			return answered
		}
		// A file created directly in / used to wedge fuse-overlayfs for good
		// (internal/sandbox/fuseroot_linux.go).
		select {
		case bad, ok := <-write("/made-in-root"):
			if ok {
				t.Fatalf("a write in /: %s", bad)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("a write in / wedged the root\n%s", le.logOf("sb-1"))
		}
		// A root server a session stopped (SIGSTOP) leaves the agent's own
		// thread waiting on it: the stop must still end it all.
		pid := fusePid(r.proc.Pid())
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		answered := write("/wedge")
		select {
		case bad := <-answered:
			t.Fatalf("a write to a stopped root answered: %q", bad)
		case <-time.After(time.Second):
		}
		start := time.Now()
		le.stop("sb-1")
		t.Logf("stopped %s after the stop was asked", time.Since(start))
	})

	t.Run("20 start/stop cycles leave no descriptor behind", func(t *testing.T) {
		le.t = t
		le.start("sb-1")
		le.stop("sb-1") // the shared host Deny's socket is made by the first relay
		before := openFDs(t)
		for i := 0; i < 20; i++ {
			le.start("sb-1")
			le.stop("sb-1")
		}
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
		if after := openFDs(t); after > before {
			t.Fatalf("open fds %d → %d over 20 cycles", before, after)
		}
	})

	testLivePolicy(t, le) // live_policy_linux_test.go

	t.Run("delete", func(t *testing.T) {
		le.t = t
		le.start("sb-1")
		r := le.runOf("sb-1")
		d, _ := le.m.defs.get(le.k, "sb-1")
		dir, _ := le.m.StateDir(le.k, d)
		le.want(le.do(mgr, "DELETE", "/sandboxes/sb-1", nil), http.StatusNoContent, "")
		le.assertGone(r)
		le.m.trash.wait()
		trash, _ := le.m.TrashDir(le.k)
		for _, p := range []string{dir, filepath.Join(trash, d.UID)} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				t.Fatalf("%s is still there: %v", p, err)
			}
		}
	})
}

func (le *liveEnv) assertNothingRuns() {
	le.t.Helper()
	if rows := le.sbx.List(sbx.Filter{Kind: sbx.Tile}); len(rows) > 1 {
		le.t.Fatalf("registry rows: %+v", rows)
	}
	le.bookMu.Lock()
	defer le.bookMu.Unlock()
	if le.booksHeld > 1 {
		le.t.Fatalf("books held: %d", le.booksHeld)
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// usesFuse reports whether the run's root is fuse-overlayfs's.
func usesFuse(r *run) bool { return fusePid(r.proc.Pid()) != 0 }

// fusePid is the fuse-overlayfs among pid's children (0: none).
func fusePid(pid int) int {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, _ := os.ReadFile("/proc/" + e.Name() + "/stat")
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(stat[i+1:]))
		comm, _ := os.ReadFile("/proc/" + e.Name() + "/comm")
		if len(f) > 1 && f[1] == strconv.Itoa(pid) && strings.TrimSpace(string(comm)) == "fuse-overlayfs" {
			return p
		}
	}
	return 0
}

func openFDs(t *testing.T) int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}
