//go:build linux && integration

// Run with: go test -tags=integration ./internal/sandbox/agentcore/
// Needs unprivileged user namespaces (skips without them). The sandbox is a
// minimal lower holding a static probe, with a static `bx` built here bound
// in as the entry, so CI (no .rootfs) runs it; with $XBIN_TEST_ROOTFS (or
// the repo's .rootfs) it runs again over the rootfs, with fuse-overlayfs
// when one is found ($XBIN_FUSE_OVERLAYFS, bin/, PATH).
package agentcore

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// TestMain doubles as the sandbox's re-exec init.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2]) // never returns
	}
	os.Exit(m.Run())
}

// covers WP-3 — `bx __sbx-agent` as a tile sandbox's PID 1, reached only
// through the factory, under Restricted + MountGuard + NoFollow (+ FuseWatch,
// WP-3b): execs with a clean environment and no inherited fds, strict cwd,
// oom_score_adj 500 (the agent keeps its own), stdio streams, a group
// signal, a tty with a resize, file operations and tar that land in the
// upper and stay in the sandbox; the agent survives every signal and every
// attempt on it its execs can make, as root or a mapped non-root uid;
// closing the factory empties the pid namespace and frees the lock, even
// with fuse-overlayfs stopped by a session.
func TestNamespaceAgent(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	bin := t.TempDir()
	build(t, filepath.Join(bin, "bx"), "../../../cmd/bx")
	build(t, filepath.Join(bin, "probe"), "./testdata/nsprobe")

	t.Run("minimal", func(t *testing.T) {
		t.Setenv("XBIN_FUSE_OVERLAYFS", "none") // the kernel overlay
		testNamespaceAgent(t, bin, "")
	})
	t.Run("rootfs", func(t *testing.T) {
		rootfs := os.Getenv("XBIN_TEST_ROOTFS")
		if rootfs == "" {
			rootfs, _ = filepath.Abs("../../../.rootfs")
		}
		if _, err := os.Stat(filepath.Join(rootfs, "bin", "sh")); err != nil {
			t.Skip("no rootfs ($XBIN_TEST_ROOTFS or .rootfs)")
		}
		fuse := findFuseOverlayfs()
		if fuse == "" {
			fuse = "none"
		}
		t.Setenv("XBIN_FUSE_OVERLAYFS", fuse)
		testNamespaceAgent(t, bin, rootfs)
	})
}

// findFuseOverlayfs is a fuse-overlayfs binary ($XBIN_FUSE_OVERLAYFS, the
// repo's bin/, PATH), or "".
func findFuseOverlayfs() string {
	fuse := os.Getenv("XBIN_FUSE_OVERLAYFS")
	if !executable(fuse) {
		fuse, _ = filepath.Abs("../../../bin/fuse-overlayfs")
	}
	if !executable(fuse) {
		fuse, _ = exec.LookPath("fuse-overlayfs")
	}
	if !executable(fuse) {
		return ""
	}
	return fuse
}

func build(t *testing.T, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // static: the sandbox has no libc
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, b)
	}
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return p != "" && err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// logBuf is the agent's stdout and stderr (xbind's log pipe).
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// nsSandbox is a running tile sandbox with the agent as its entry.
type nsSandbox struct {
	*harness
	cmd                *exec.Cmd
	log                *logBuf
	fac                *sandbox.Factory
	spec               *sandbox.Spec
	dir, upper, lockAt string
	next               int // the next session number
}

// startNamespaceAgent launches a tile sandbox as the runtime will (mod
// changes its spec first) and connects to its agent.
func startNamespaceAgent(t *testing.T, bin, rootfs string, mod ...func(*sandbox.Spec)) *nsSandbox {
	t.Helper()
	sb := launchNamespaceAgent(t, bin, rootfs, mod...)
	hs := &harness{t: t, more: make(chan struct{}, 1), ctlGone: make(chan struct{})}
	hs.open = func() *net.UnixConn {
		c, err := sb.fac.Dial()
		if err != nil {
			hs.t.Fatalf("dial the agent: %v\n%s", err, sb.log)
		}
		hs.t.Cleanup(func() { c.Close() })
		return c.(*net.UnixConn)
	}
	hs.start()
	sb.harness = hs
	return sb
}

// launchNamespaceAgent starts the sandbox, without a connection to it.
func launchNamespaceAgent(t *testing.T, bin, rootfs string, mod ...func(*sandbox.Spec)) *nsSandbox {
	t.Helper()
	dir := t.TempDir()
	lower, state := filepath.Join(dir, "lower"), filepath.Join(dir, "state")
	sb := &nsSandbox{dir: dir, upper: filepath.Join(state, "upper"), lockAt: filepath.Join(state, "lock"), log: &logBuf{}, next: 2}
	for _, d := range []string{lower, sb.upper, filepath.Join(state, "work"), filepath.Join(dir, "code")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "code", "f"), []byte("code"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(bin, "probe"), filepath.Join(lower, "probe")); err != nil {
		t.Fatal(err)
	}
	lowers := []string{lower}
	if rootfs != "" {
		lowers = append(lowers, rootfs)
	}
	lock, err := os.OpenFile(sb.lockAt, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	fac, child, err := sandbox.NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	sb.fac = fac
	t.Cleanup(func() { fac.Close() })
	spec := &sandbox.Spec{
		Lower: lowers, Upper: sb.upper, Work: filepath.Join(state, "work"),
		Binds: []sandbox.Bind{
			{Src: filepath.Join(bin, "bx"), Dst: "/opt/xbin/bin/bx", RO: true},
			{Src: filepath.Join(dir, "code"), Dst: "/code", RO: true}, // a {source:true} mount
		},
		Entry: "/opt/xbin/bin/bx", Argv: []string{"bx", "__sbx-agent"},
		Env:      []string{"AGENT_ONLY=secret"}, // the agent's; no session may see it
		Hostname: "sbx-agent-test",
		HostUID:  os.Getuid(), HostGID: os.Getgid(),
		Restricted: true, MountGuard: true, NoFollow: true, FuseWatch: true,
		Agent: child, Lock: lock,
	}
	for _, m := range mod {
		m(spec)
	}
	lowerOwnOOMScore() // the agent inherits it: testOOMScores needs it below 500
	cmd, h, err := sandbox.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Cleanup)
	cmd.Stdout, cmd.Stderr = sb.log, sb.log
	if err := cmd.Start(); err != nil {
		child.Close()
		lock.Close()
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("sandbox creation denied by environment: %v", err)
		}
		t.Fatal(err)
	}
	h.Started() // the sandbox alone holds the factory's end and the lock now
	sb.cmd, sb.spec = cmd, spec
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("the agent's log:\n%s", sb.log)
		}
	})
	if err := h.SetupUserns(); err != nil {
		t.Fatalf("userns: %v\n%s", err, sb.log)
	}
	t.Logf("fuse-overlayfs=%q (watched %v) agentFd=%d lockFd=%d", spec.FuseOverlay, spec.FuseWatch, spec.AgentFD, spec.LockFD)
	return sb
}

// run runs one exec to its end: its combined output and its last event.
func (sb *nsSandbox) run(ex proto.Exec) (string, proto.Msg) {
	sb.t.Helper()
	ex.Session, ex.Merge, ex.NoStdin = sb.next, true, true
	sb.next++
	ss := sb.exec(ex)
	out := readAll(sb.t, ss["stdout"])
	return out, sb.wait(ex.Session, "exited", "error")
}

// probe runs /probe op args… (Env nil: the agent's defaults).
func (sb *nsSandbox) probe(op string, args ...string) string {
	sb.t.Helper()
	out, m := sb.run(proto.Exec{Argv: append([]string{"/probe", op}, args...)})
	if m.Op != "exited" || m.Code != 0 {
		sb.t.Fatalf("probe %s %q: %+v\n%s", op, args, m, out)
	}
	return out
}

// descendants are the host pids of pid's descendants.
func descendants(pid int) []int {
	parent := map[int]int{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		i := bytes.LastIndexByte(b, ')')
		if err != nil || i < 0 {
			continue
		}
		if f := strings.Fields(string(b[i+1:])); len(f) > 1 {
			parent[p], _ = strconv.Atoi(f[1])
		}
	}
	var out []int
	for p := range parent {
		for q := parent[p]; q > 1; q = parent[q] {
			if q == pid {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func testNamespaceAgent(t *testing.T, bin, rootfs string) {
	sb := startNamespaceAgent(t, bin, rootfs)

	t.Run("env and fds", func(t *testing.T) {
		sb.t = t
		if out := sb.probe("env"); out != "PATH="+defaultPATH+"\n" {
			t.Errorf("a session's environment, the exec naming none: %q", out)
		}
		out, _ := sb.run(proto.Exec{Argv: []string{"/probe", "env"}, Env: []string{"FOO=bar"}})
		if out != "PATH="+defaultPATH+"\nFOO=bar\n" {
			t.Errorf("a session's environment: %q", out)
		}
		if out := strings.TrimSpace(sb.probe("fds")); out != "" {
			t.Errorf("a session inherited descriptors: %s", out)
		}
		if out, _ := sb.run(proto.Exec{Argv: []string{"probe", "echo", "found"}, Env: []string{"PATH=/"}}); out != "found\n" {
			t.Errorf("argv0 through the exec's PATH: %q", out)
		}
	})

	t.Run("cwd", func(t *testing.T) {
		sb.t = t
		if _, m := sb.run(proto.Exec{Argv: []string{"/probe", "pwd"}, Cwd: "/nope", CwdStrict: true}); m.Op != "error" || !strings.Contains(m.Error, "no such file") {
			t.Errorf("a strict missing cwd: %+v", m)
		}
		if out, _ := sb.run(proto.Exec{Argv: []string{"/probe", "pwd"}, Cwd: "/tmp", CwdStrict: true}); out != "/tmp\n" {
			t.Errorf("a strict cwd: %q", out)
		}
		if out, _ := sb.run(proto.Exec{Argv: []string{"/probe", "pwd"}, Cwd: "/nope"}); out != "/\n" {
			t.Errorf("a missing cwd, not strict: %q", out)
		}
	})

	t.Run("oom_score_adj", func(t *testing.T) {
		sb.t = t
		testOOMScores(t, sb)
	})

	t.Run("streams", func(t *testing.T) {
		sb.t = t
		s := sb.next
		sb.next++
		ss := sb.exec(proto.Exec{Session: s, Argv: []string{"/probe", "cat"}})
		if _, err := ss["stdin"].Write([]byte("in the sandbox\n")); err != nil {
			t.Fatal(err)
		}
		_ = ss["stdin"].CloseWrite()
		if m := sb.wait(s, "started"); m.Pid <= 1 {
			t.Errorf("started: %+v", m)
		}
		if out, errs := readAll(t, ss["stdout"]), readAll(t, ss["stderr"]); out != "in the sandbox\n" || errs != "cat: done\n" {
			t.Errorf("stdout %q, stderr %q", out, errs)
		}
		if m := sb.wait(s, "exited"); m.Code != 0 {
			t.Errorf("exited %+v", m)
		}
		if _, m := sb.run(proto.Exec{Argv: []string{"/probe", "exit", "7"}}); m.Code != 7 {
			t.Errorf("an exit code: %+v", m)
		}
	})

	t.Run("group signal", func(t *testing.T) {
		sb.t = t
		s := sb.next
		sb.next++
		ss := sb.exec(proto.Exec{Session: s, Argv: []string{"/probe", "sleeper"}, Merge: true, NoStdin: true})
		_ = ss["stdout"].SetReadDeadline(time.Now().Add(hangGuard))
		line, err := bufio.NewReader(ss["stdout"]).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		grandchild := strings.TrimSpace(line)
		sb.wait(s, "started")
		sb.send(proto.Msg{Op: "signal", Session: s, Signal: int(unix.SIGTERM), Group: true})
		if m := sb.wait(s, "exited"); m.Signal != int(unix.SIGTERM) {
			t.Errorf("exited %+v, want killed by TERM", m)
		}
		waitUntil(t, "the grandchild "+grandchild+" ended by a group signal", func() bool { return sb.probe("alive", grandchild) == "gone\n" })
	})

	t.Run("tty", func(t *testing.T) {
		sb.t = t
		tty := func(argv []string, input, want1, want2 string) {
			s := sb.next
			sb.next++
			ss := sb.exec(proto.Exec{Session: s, Argv: argv, TTY: true, Rows: 30, Cols: 100})
			c := ss["pty"]
			_ = c.SetReadDeadline(time.Now().Add(hangGuard))
			var seen bytes.Buffer
			until := func(want string) {
				buf := make([]byte, 4096)
				for !strings.Contains(seen.String(), want) {
					n, err := c.Read(buf)
					seen.Write(buf[:n])
					if err != nil {
						t.Fatalf("%q: no %q in %q: %v", argv, want, seen.String(), err)
					}
				}
			}
			until(want1)
			sb.send(proto.Msg{Op: "resize", Session: s, Rows: 40, Cols: 120})
			sb.barrier() // the resize is on ctl, the input on the pty: the resize first
			if _, err := c.Write([]byte(input)); err != nil {
				t.Fatal(err)
			}
			until(want2)
			if m := sb.wait(s, "exited"); m.Code != 0 {
				t.Errorf("%q exited %+v", argv, m)
			}
		}
		tty([]string{"/probe", "tty"}, "hello\n", "tty=true size=30x100", "then size=40x120 line=hello")
		if rootfs != "" {
			tty([]string{"sh"}, "stty size; exit\n", "#", "40 120")
		}
	})

	t.Run("files", func(t *testing.T) {
		sb.t = t
		sb.ok(proto.FileOp{Op: "write", Path: "/work/src/hello.txt", Mkdirs: true}, []byte("hello, sandbox"))
		if b, err := os.ReadFile(filepath.Join(sb.upper, "work", "src", "hello.txt")); err != nil || string(b) != "hello, sandbox" {
			t.Errorf("the write in the upper: %q %v", b, err)
		}
		if got := sb.read(proto.FileOp{Path: "/work/src/hello.txt", Offset: 7, Length: 7}); got != "sandbox" {
			t.Errorf("a ranged read: %q", got)
		}
		if st := sb.ok(proto.FileOp{Op: "stat", Path: "/work/src/hello.txt"}, nil).Stat; st == nil || st.Size != 14 || st.Type != "file" {
			t.Errorf("stat: %+v", st)
		}
		if l := sb.ok(proto.FileOp{Op: "list", Path: "/work/src"}, nil); len(l.Entries) != 1 || l.Entries[0].Name != "hello.txt" {
			t.Errorf("list: %+v", l.Entries)
		}
		// a read-only mount stays read-only; a planted host symlink resolves
		// inside the sandbox
		if got := sb.read(proto.FileOp{Path: "/code/f"}); got != "code" {
			t.Errorf("a read under a read-only bind: %q", got)
		}
		if r, _, _ := sb.call(proto.FileOp{Op: "write", Path: "/code/new"}, []byte("x")); r.OK || !strings.Contains(r.Error, "read-only") {
			t.Errorf("a write under a read-only bind: %+v", r)
		}
		if _, err := os.Stat(filepath.Join(sb.dir, "code", "new")); err == nil {
			t.Error("a write under a read-only bind reached the host")
		}
		outside := filepath.Join(sb.dir, "outside") // a host path
		if err := os.Mkdir(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		sb.probe("ln", outside, "/work/escape") // planted by sandbox code
		if r, _, _ := sb.call(proto.FileOp{Op: "write", Path: "/work/escape/x"}, []byte("x")); r.OK {
			t.Errorf("a write through a planted symlink: %+v", r)
		}
		if ents, _ := os.ReadDir(outside); len(ents) != 0 {
			t.Errorf("a file operation wrote on the host: %v", ents)
		}

		first, data, last := sb.call(proto.FileOp{Op: "tar-get", Path: "/work/src"}, nil)
		if !first.OK || !last.OK {
			t.Fatalf("tar-get: %+v %+v", first, last)
		}
		tr, names := tar.NewReader(bytes.NewReader(data)), []string{}
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			names = append(names, h.Name)
		}
		if strings.Join(names, ",") != "hello.txt" {
			t.Errorf("tar-get entries %q", names)
		}
		sb.ok(proto.FileOp{Op: "mkdir", Path: "/work/copy"}, nil)
		sb.ok(proto.FileOp{Op: "tar-put", Path: "/work/copy"}, data)
		if got := sb.read(proto.FileOp{Path: "/work/copy/hello.txt"}); got != "hello, sandbox" {
			t.Errorf("after tar-put: %q", got)
		}
	})

	t.Run("the agent holds", func(t *testing.T) {
		sb.t = t
		attack := func(ex proto.Exec) {
			t.Helper()
			var r struct {
				Peers        []string          `json:"peers"`
				Readlinked   []string          `json:"readlinked"`
				Opened       []string          `json:"opened"`
				Proc         []string          `json:"proc"`
				PidfdGetfd   []string          `json:"pidfdGetfd"`
				Ptraced      []string          `json:"ptraced"`
				UnshareUser  string            `json:"unshareUser"`
				UnshareMount string            `json:"unshareMount"`
				Signals      map[string]string `json:"signals"`
			}
			who := "root"
			if ex.UID != nil {
				who = "uid " + strconv.Itoa(int(*ex.UID))
			}
			ex.Argv = []string{"/probe", "attack"}
			out, m := sb.run(ex)
			if m.Op != "exited" || m.Code != 0 {
				t.Fatalf("attack as %s: %+v\n%s", who, m, out)
			}
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatalf("attack report: %v\n%s", err, out)
			}
			t.Logf("attack as %s: %s", who, out)
			if got := strings.Join(r.Peers, " "); !strings.HasPrefix(got, "1=bx") || sb.spec.FuseOverlay != "" && !strings.Contains(got, "=fuse-overlayfs") {
				t.Errorf("the other processes an exec sees: %q", got)
			}
			for what, got := range map[string][]string{"readlink a descriptor": r.Readlinked, "open a descriptor": r.Opened,
				"reach a root, cwd, environ or mem": r.Proc, "pidfd_getfd a descriptor": r.PidfdGetfd, "ptrace": r.Ptraced} {
				if len(got) != 0 {
					t.Errorf("an exec as %s could %s of the agent or fuse-overlayfs: %q", who, what, got)
				}
			}
			for what, got := range map[string]string{"a user namespace": r.UnshareUser, "a mount namespace": r.UnshareMount} {
				if got == "ok" {
					t.Errorf("an exec as %s made %s", who, what)
				}
			}
			if len(r.Signals) < 10 {
				t.Errorf("signals sent: %v", r.Signals)
			}
			// still here: a new exec runs
			if out := sb.probe("echo", "still", "here"); out != "still here\n" {
				t.Errorf("after the attack: %q", out)
			}
		}
		attack(proto.Exec{})
		// A session as a mapped non-root user reaches nothing either. Its
		// setuid runs in a vfork child that shares the agent's memory, which
		// resets the agent's dumpable to fs.suid_dumpable: a root session
		// after it must still find the agent closed.
		if uid := uint32(1000); idMapped(filepath.Join("/proc", strconv.Itoa(sb.cmd.Process.Pid), "uid_map"), uid) &&
			idMapped(filepath.Join("/proc", strconv.Itoa(sb.cmd.Process.Pid), "gid_map"), uid) {
			attack(proto.Exec{UID: &uid, GID: &uid})
			attack(proto.Exec{})
		} else {
			t.Log("uid 1000 isn't mapped (a single-id sandbox): no non-root session")
		}
		if rootfs != "" {
			out, _ := sb.run(proto.Exec{Argv: []string{"sh", "-c", "unshare -U true; echo rc=$?"}})
			if strings.Contains(out, "rc=0") || !strings.Contains(out, "rc=") {
				t.Errorf("unshare -U in an exec: %q", out)
			}
		}
	})

	t.Run("factory close", func(t *testing.T) {
		sb.t = t
		s := sb.next
		sb.next++
		sb.exec(proto.Exec{Session: s, Argv: []string{"/probe", "sleep", "600"}, Merge: true, NoStdin: true})
		sb.wait(s, "started")
		pids := descendants(sb.cmd.Process.Pid)
		if len(pids) == 0 {
			t.Fatal("no process under the agent")
		}
		if err := tryLock(sb.lockAt); !errors.Is(err, unix.EWOULDBLOCK) {
			t.Errorf("the lock while the sandbox runs: %v", err)
		}
		if sb.spec.FuseOverlay != "" {
			// a session stops the root's FUSE server: the agent's final
			// sync must not wait on it forever
			out, m := sb.run(proto.Exec{Argv: []string{"/probe", "stop", "fuse-overlayfs"}, NoSync: true})
			if m.Code != 0 || !strings.HasPrefix(out, "stopped ") {
				t.Fatalf("stop fuse-overlayfs: %+v %q", m, out)
			}
		}
		closed := time.Now()
		sb.fac.Close()
		done := make(chan error, 1)
		go func() { done <- sb.cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the agent's exit: %v", err)
			}
		case <-time.After(hangGuard):
			t.Fatal("the agent outlived its factory")
		}
		// The agent was PID 1: its exit kills the rest of the namespace
		// (how soon they are gone is the scheduler's business).
		for _, p := range append(pids, sb.cmd.Process.Pid) {
			waitUntil(t, "pid "+strconv.Itoa(p)+" of the sandbox gone with the factory", func() bool { return unix.Kill(p, 0) != nil })
		}
		t.Logf("the pid namespace emptied %s after the factory closed", time.Since(closed).Round(time.Millisecond))
		waitUntil(t, "the lock released with the sandbox", func() bool { return tryLock(sb.lockAt) == nil })
		if _, err := sb.fac.Dial(); err == nil {
			t.Error("a dial after the close")
		}
	})
}

func tryLock(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
