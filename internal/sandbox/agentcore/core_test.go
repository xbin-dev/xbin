//go:build linux

package agentcore

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// harness is a core under test, reached over socketpairs as xbind reaches
// a namespace sandbox's agent through its factory.
type harness struct {
	t     *testing.T
	core  *Core
	root  string
	ctl   *proto.Conn
	syncs atomic.Int32

	mu      sync.Mutex
	events  []proto.Msg
	more    chan struct{}
	ctlGone chan struct{} // the event reader's ctl ended

	// open makes one connection to the agent: a socketpair handed to
	// core.Handle, or a Dial over a sandbox's factory (ns_linux_test.go).
	open func() *net.UnixConn
	// env is an exec's Env when it names none (nil: sent as nil).
	env []string
}

func newHarness(t *testing.T, mod func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, root: t.TempDir(), more: make(chan struct{}, 1), ctlGone: make(chan struct{})}
	o := Options{Spawn: ProcSpawner(), Root: h.root, Sync: func() { h.syncs.Add(1) }, StreamWait: 3 * time.Second}
	if mod != nil {
		mod(&o)
	}
	h.core = New(o)
	h.env = []string{"PATH=" + os.Getenv("PATH")}
	h.open = func() *net.UnixConn {
		a, b := pair(h.t)
		go h.core.Handle(b)
		return a
	}
	h.start()
	return h
}

// start opens the control connection, reads its events, and waits for
// "ready".
func (h *harness) start() {
	t := h.t
	t.Helper()
	h.ctl = proto.NewConn(h.dial(proto.Hello{Kind: "ctl"}), nil)
	go func() {
		defer close(h.ctlGone)
		for {
			var m proto.Msg
			if err := h.ctl.RecvMax(&m, proto.MaxEvent); err != nil {
				return
			}
			h.mu.Lock()
			h.events = append(h.events, m)
			h.mu.Unlock()
			select {
			case h.more <- struct{}{}:
			default:
			}
		}
	}()
	if m := h.wait(0, "ready"); m.Op != "ready" {
		t.Fatalf("first event %+v, want ready", m)
	}
}

// pair is a connected pair of unix stream sockets.
func pair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "pair")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	a, b := conn(fds[0]), conn(fds[1])
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// dial opens a connection to the core and sends its Hello.
func (h *harness) dial(hello proto.Hello) *net.UnixConn {
	h.t.Helper()
	a := h.open()
	if err := proto.NewConn(a, nil).Send(hello); err != nil {
		h.t.Fatal(err)
	}
	return a
}

// wait returns the next event for session with one of ops (skipping the
// others), or fails the test.
func (h *harness) wait(session int, ops ...string) proto.Msg {
	h.t.Helper()
	deadline := time.After(15 * time.Second)
	seen := 0
	for {
		h.mu.Lock()
		for i := seen; i < len(h.events); i++ {
			m := h.events[i]
			for _, op := range ops {
				if m.Op == op && m.Session == session {
					h.events = append(h.events[:i:i], h.events[i+1:]...)
					h.mu.Unlock()
					return m
				}
			}
		}
		seen = len(h.events)
		h.mu.Unlock()
		select {
		case <-h.more:
		case <-deadline:
			h.mu.Lock()
			defer h.mu.Unlock()
			h.t.Fatalf("no %v for session %d; events: %+v", ops, session, h.events)
		}
	}
}

func (h *harness) send(m proto.Msg) {
	h.t.Helper()
	if err := h.ctl.Send(m); err != nil {
		h.t.Fatal(err)
	}
}

// exec dials the streams the exec expects, then sends it.
func (h *harness) exec(ex proto.Exec) map[string]*net.UnixConn {
	h.t.Helper()
	if ex.Env == nil {
		ex.Env = h.env
	}
	ss := map[string]*net.UnixConn{}
	for _, name := range streamsOf(ex) {
		ss[name] = h.dial(proto.Hello{Kind: "stream", Session: ex.Session, Stream: name})
	}
	h.send(proto.Msg{Op: "exec", Exec: &ex})
	return ss
}

func readAll(t *testing.T, c net.Conn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v (after %q)", err, b)
	}
	return string(b)
}

func sh(script string) []string { return []string{"sh", "-c", script} }

func TestExecStreams(t *testing.T) {
	h := newHarness(t, nil)
	ss := h.exec(proto.Exec{Session: 2, Argv: sh("echo out; echo err >&2; cat; exit 3")})
	if _, err := ss["stdin"].Write([]byte("in\n")); err != nil {
		t.Fatal(err)
	}
	_ = ss["stdin"].CloseWrite()
	if m := h.wait(2, "started"); m.Pid <= 0 {
		t.Fatalf("started without a pid: %+v", m)
	}
	out, errs := readAll(t, ss["stdout"]), readAll(t, ss["stderr"])
	if out != "out\nin\n" || errs != "err\n" {
		t.Fatalf("stdout %q, stderr %q", out, errs)
	}
	if m := h.wait(2, "exited"); m.Code != 3 || m.Signal != 0 {
		t.Fatalf("exited %+v, want code 3", m)
	}
	if n := h.syncs.Load(); n != 1 {
		t.Fatalf("%d syncs after an exit, want 1", n)
	}
}

func TestStrictCwd(t *testing.T) {
	h := newHarness(t, nil)
	dir := filepath.Join(h.root, "work")
	file := filepath.Join(h.root, "file")
	_ = os.Mkdir(dir, 0o755)
	_ = os.WriteFile(file, nil, 0o644)
	pwd := func(session int, cwd string, strict bool) (string, proto.Msg) {
		ss := h.exec(proto.Exec{Session: session, Argv: []string{"pwd"}, Cwd: cwd, CwdStrict: strict, Merge: true, NoStdin: true})
		out := readAll(t, ss["stdout"])
		return strings.TrimSpace(out), h.wait(session, "exited", "error")
	}
	if out, m := pwd(2, dir+"/missing", true); m.Op != "error" || !strings.Contains(m.Error, "no such file") || out != "" {
		t.Fatalf("a strict missing cwd: %+v, output %q", m, out)
	}
	if _, m := pwd(3, file, true); m.Op != "error" || !strings.Contains(m.Error, "not a directory") {
		t.Fatalf("a strict cwd that is a file: %+v", m)
	}
	if _, m := pwd(4, "", true); m.Op != "error" {
		t.Fatalf("a strict empty cwd: %+v", m)
	}
	if out, m := pwd(5, dir+"/missing", false); m.Op != "exited" || out != "/" {
		t.Fatalf("a missing cwd, not strict: %+v, %q (want /)", m, out)
	}
	if out, m := pwd(6, dir, true); m.Op != "exited" || out != dir {
		t.Fatalf("a strict cwd that exists: %+v, %q", m, out)
	}
}

// alive reports whether pid runs (a zombie doesn't).
func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || !strings.HasPrefix(s[i+1:], " Z")
}

func grandchild(t *testing.T, h *harness, session int) (int, map[string]*net.UnixConn) {
	t.Helper()
	ss := h.exec(proto.Exec{Session: session, Argv: sh("sleep 60 >/dev/null 2>&1 & echo $!; wait"), Merge: true, NoStdin: true})
	line, err := bufio.NewReader(ss["stdout"]).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("pid line %q", line)
	}
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
	h.wait(session, "started")
	return pid, ss
}

func TestGroupSignal(t *testing.T) {
	h := newHarness(t, nil)
	pid, _ := grandchild(t, h, 2)
	h.send(proto.Msg{Op: "signal", Session: 2, Signal: int(unix.SIGTERM), Group: true})
	if m := h.wait(2, "exited"); m.Signal != int(unix.SIGTERM) || m.Code != 128+int(unix.SIGTERM) {
		t.Fatalf("exited %+v, want killed by TERM", m)
	}
	for end := time.Now().Add(5 * time.Second); alive(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the grandchild %d outlived a group signal", pid)
		}
	}

	// without Group, only the session's own process
	pid, _ = grandchild(t, h, 3)
	h.send(proto.Msg{Op: "signal", Session: 3, Signal: int(unix.SIGTERM)})
	if m := h.wait(3, "exited"); m.Signal != int(unix.SIGTERM) {
		t.Fatalf("exited %+v", m)
	}
	if !alive(pid) {
		t.Fatal("a signal without group reached the grandchild")
	}
}

// TestGroupSignalAfterExit: a member that outlived its session's process
// (it ignored the TERM that ended the leader) still gets a group signal sent
// to the ended session — a timeout's KILL after its grace — but only within
// groupAfterlife, and only as a group signal.
func TestGroupSignalAfterExit(t *testing.T) {
	h := newHarness(t, nil)
	orphan := func(session int) int {
		t.Helper()
		ss := h.exec(proto.Exec{Session: session, Argv: sh("(trap '' TERM; exec sleep 60) >/dev/null 2>&1 & echo $!"), Merge: true, NoStdin: true})
		line, err := bufio.NewReader(ss["stdout"]).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatalf("pid line %q", line)
		}
		t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
		if m := h.wait(session, "exited"); m.Code != 0 {
			t.Fatalf("exited %+v", m)
		}
		return pid
	}
	gone := func(pid int) bool {
		for end := time.Now().Add(5 * time.Second); alive(pid); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				return false
			}
		}
		return true
	}

	pid := orphan(2)
	h.send(proto.Msg{Op: "signal", Session: 2, Signal: int(unix.SIGTERM), Group: true})
	h.send(proto.Msg{Op: "signal", Session: 2, Signal: int(unix.SIGKILL)}) // not a group signal: the process is gone
	time.Sleep(200 * time.Millisecond)
	if !alive(pid) {
		t.Fatal("a TERM it ignores, or a signal without group, ended the member")
	}
	h.send(proto.Msg{Op: "signal", Session: 2, Signal: int(unix.SIGKILL), Group: true})
	if !gone(pid) {
		t.Fatalf("the member %d outlived a group KILL to its ended session", pid)
	}

	// a number that came round to a live session's group is left alone
	h.exec(proto.Exec{Session: 4, Argv: []string{"sleep", "60"}, Merge: true, NoStdin: true})
	started := h.wait(4, "started")
	h.core.mu.Lock()
	h.core.ended[5] = endedGroup{pgid: started.Pid, until: time.Now().Add(time.Minute)}
	h.core.mu.Unlock()
	h.send(proto.Msg{Op: "signal", Session: 5, Signal: int(unix.SIGKILL), Group: true})
	time.Sleep(200 * time.Millisecond)
	if !alive(started.Pid) {
		t.Fatal("a group signal to an ended session reached a live session's group")
	}
	h.send(proto.Msg{Op: "signal", Session: 4, Signal: int(unix.SIGKILL), Group: true})
	h.wait(4, "exited")

	// past the afterlife the group is forgotten
	was := groupAfterlife
	groupAfterlife = 0
	t.Cleanup(func() { groupAfterlife = was })
	pid = orphan(3)
	h.send(proto.Msg{Op: "signal", Session: 3, Signal: int(unix.SIGKILL), Group: true})
	time.Sleep(200 * time.Millisecond)
	if !alive(pid) {
		t.Fatal("a group signal past the afterlife reached the member")
	}
}

func TestExitedSignal(t *testing.T) {
	h := newHarness(t, nil)
	h.exec(proto.Exec{Session: 2, Argv: []string{"sleep", "60"}, Merge: true, NoStdin: true})
	h.wait(2, "started")
	h.send(proto.Msg{Op: "signal", Session: 2, Signal: int(unix.SIGKILL)})
	if m := h.wait(2, "exited"); m.Code != 137 || m.Signal != 9 {
		t.Fatalf("exited %+v, want code 137 signal 9", m)
	}
}

func TestMergeNoStdinNoSync(t *testing.T) {
	h := newHarness(t, nil)
	// no stdin or stderr stream is dialled: the exec expects none
	ss := h.exec(proto.Exec{Session: 2, Argv: sh("echo a; echo b >&2; cat; echo c"), Merge: true, NoStdin: true, NoSync: true})
	if len(ss) != 1 || ss["stdout"] == nil {
		t.Fatalf("streams %v", ss)
	}
	if out := readAll(t, ss["stdout"]); out != "a\nb\nc\n" {
		t.Fatalf("merged output %q", out)
	}
	h.wait(2, "exited")
	if n := h.syncs.Load(); n != 0 {
		t.Fatalf("%d syncs after a NoSync exit", n)
	}
	h.send(proto.Msg{Op: "sync"})
	h.wait(0, "synced")
	if n := h.syncs.Load(); n != 1 {
		t.Fatalf("%d syncs after a sync", n)
	}
}

func TestTTYResize(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx")
	}
	h := newHarness(t, nil)
	ss := h.exec(proto.Exec{Session: 2, Argv: sh("stty size; read x; stty size"), TTY: true, Rows: 24, Cols: 80})
	c := ss["pty"]
	var mu sync.Mutex
	var out strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := c.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	await := func(s string) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			got := out.String()
			mu.Unlock()
			if strings.Contains(got, s) {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("no %q in %q", s, got)
			}
		}
	}
	await("24 80")
	h.send(proto.Msg{Op: "resize", Session: 2, Rows: 40, Cols: 100})
	time.Sleep(50 * time.Millisecond) // the resize and the newline travel apart
	if _, err := c.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	await("40 100")
	if m := h.wait(2, "exited"); m.Code != 0 {
		t.Fatalf("exited %+v", m)
	}
}

func TestSessionsPruned(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.StreamWait = 300 * time.Millisecond; o.MaxSessions = 2 })
	ids := func() []int {
		var out []int
		for _, s := range h.core.Sessions() {
			out = append(out, s.ID)
		}
		return out
	}
	waitIDs := func(want string) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			got := fmt.Sprint(ids())
			if got == want {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("sessions %s, want %s", got, want)
			}
		}
	}

	// a stream with no exec is a phantom, pruned after StreamWait
	orphan := h.dial(proto.Hello{Kind: "stream", Session: 7, Stream: "stdout"})
	waitIDs("[7]")
	waitIDs("[]")
	if got := readAll(t, orphan); got != "" {
		t.Fatalf("the pruned stream carried %q", got)
	}
	h.send(proto.Msg{Op: "exec", Exec: &proto.Exec{Session: 7, Argv: []string{"true"}}})
	if m := h.wait(7, "error"); !strings.Contains(m.Error, "already run") {
		t.Fatalf("an exec for a pruned id: %+v", m)
	}

	// phantoms are capped
	h.dial(proto.Hello{Kind: "stream", Session: 10, Stream: "stdout"})
	h.dial(proto.Hello{Kind: "stream", Session: 11, Stream: "stdout"})
	waitIDs("[10 11]")
	third := h.dial(proto.Hello{Kind: "stream", Session: 12, Stream: "stdout"})
	if got := readAll(t, third); got != "" {
		t.Fatalf("got %q", got)
	}
	waitIDs("[10 11]")
	waitIDs("[]")

	// live sessions are capped
	for _, id := range []int{20, 21} {
		h.exec(proto.Exec{Session: id, Argv: []string{"sleep", "60"}, Merge: true, NoStdin: true})
		h.wait(id, "started")
	}
	h.send(proto.Msg{Op: "exec", Exec: &proto.Exec{Session: 22, Argv: []string{"true"}}})
	if m := h.wait(22, "error"); !strings.Contains(m.Error, "too many") {
		t.Fatalf("a third live session: %+v", m)
	}
	for _, id := range []int{20, 21} {
		h.send(proto.Msg{Op: "signal", Session: id, Signal: int(unix.SIGKILL)})
		h.wait(id, "exited")
	}
	waitIDs("[]")

	// an id never runs twice
	h.exec(proto.Exec{Session: 20, Argv: []string{"true"}, Merge: true, NoStdin: true})
	if m := h.wait(20, "error"); !strings.Contains(m.Error, "already run") {
		t.Fatalf("a second exec of session 20: %+v", m)
	}

	// an exec whose streams never come is dropped after StreamWait
	h.send(proto.Msg{Op: "exec", Exec: &proto.Exec{Session: 30, Argv: []string{"true"}}})
	if m := h.wait(30, "error"); !strings.Contains(m.Error, "did not attach") {
		t.Fatalf("an exec without streams: %+v", m)
	}
	waitIDs("[]")
}

func TestOversizedLines(t *testing.T) {
	h := newHarness(t, nil)
	// a hello past proto.MaxHello: dropped
	a, b := pair(t)
	go h.core.Handle(b)
	go func() {
		_, _ = a.Write([]byte(`{"kind":"ctl","stream":"` + strings.Repeat("x", proto.MaxHello) + "\"}\n"))
	}()
	if got := readAll(t, a); got != "" {
		t.Fatalf("an oversized hello was answered: %q", got)
	}
	// a control line past proto.MaxCommand: that ctl is dropped (a newer
	// ctl takes over events, and the older one is closed)
	ctl := h.dial(proto.Hello{Kind: "ctl"})
	r := bufio.NewReader(ctl)
	if line, err := r.ReadString('\n'); err != nil || !strings.Contains(line, `"ready"`) {
		t.Fatalf("the new ctl: %q %v", line, err)
	}
	select {
	case <-h.ctlGone:
	case <-time.After(10 * time.Second):
		t.Fatal("the older ctl stayed open")
	}
	line := `{"op":"exec","exec":{"session":2,"argv":["` + strings.Repeat("x", proto.MaxCommand) + "\"]}}\n"
	go func() { _, _ = ctl.Write([]byte(line)) }()
	_ = ctl.SetReadDeadline(time.Now().Add(10 * time.Second))
	if got, err := io.ReadAll(r); err != nil || len(got) != 0 {
		t.Fatalf("got %q %v", got, err)
	}
	if len(h.core.Sessions()) != 0 {
		t.Fatal("an oversized exec started a session")
	}
}

func TestIdentity(t *testing.T) {
	h := newHarness(t, nil)
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	ss := h.exec(proto.Exec{Session: 2, Argv: sh("id -u; id -g"), UID: &uid, GID: &gid, Merge: true, NoStdin: true})
	if out := readAll(t, ss["stdout"]); out != strconv.Itoa(int(uid))+"\n"+strconv.Itoa(int(gid))+"\n" {
		t.Fatalf("id: %q", out)
	}
	h.wait(2, "exited")

	// an id the sandbox doesn't map is refused
	maps := t.TempDir()
	for name, v := range map[string]string{"uid_map": "0 1000 1\n", "gid_map": "0 1000 1\n", "setgroups": "deny\n"} {
		_ = os.WriteFile(filepath.Join(maps, name), []byte(v), 0o644)
	}
	idMaps = maps
	t.Cleanup(func() { idMaps = "/proc/self" })
	five := uint32(5)
	h.exec(proto.Exec{Session: 3, Argv: []string{"true"}, UID: &five, Merge: true, NoStdin: true})
	if m := h.wait(3, "error"); !strings.Contains(m.Error, "uid 5 is not mapped") {
		t.Fatalf("an unmapped uid: %+v", m)
	}
	zero := uint32(0)
	h.exec(proto.Exec{Session: 4, Argv: []string{"true"}, UID: &zero, GID: &five, Merge: true, NoStdin: true})
	if m := h.wait(4, "error"); !strings.Contains(m.Error, "gid 5 is not mapped") {
		t.Fatalf("an unmapped gid: %+v", m)
	}
}

func TestNoConfigureRefusesConfig(t *testing.T) {
	h := newHarness(t, nil)
	h.send(proto.Msg{Op: "config", Config: &proto.Config{}})
	if m := h.wait(0, "error"); !strings.Contains(m.Error, "takes no config") {
		t.Fatalf("%+v", m)
	}
}

func TestConfigureFirst(t *testing.T) {
	var got proto.Config
	h := &harness{t: t}
	h.root = t.TempDir()
	core := New(Options{Spawn: ProcSpawner(), Root: h.root, Configure: func(c proto.Config) error { got = c; return nil }})
	h.core = core
	a, b := pair(t)
	go core.Handle(b)
	ctl := proto.NewConn(a, nil)
	_ = ctl.Send(proto.Hello{Kind: "ctl"})
	_ = ctl.Send(proto.Msg{Op: "exec", Exec: &proto.Exec{Session: 2, Argv: []string{"true"}}})
	var m proto.Msg
	if err := ctl.RecvMax(&m, proto.MaxEvent); err != nil || m.Op != "error" || !strings.Contains(m.Error, "before config") {
		t.Fatalf("an exec before config: %+v %v", m, err)
	}
	_ = ctl.Send(proto.Msg{Op: "config", Config: &proto.Config{Hostname: "h"}})
	if err := ctl.RecvMax(&m, proto.MaxEvent); err != nil || m.Op != "ready" || got.Hostname != "h" {
		t.Fatalf("config: %+v %v (%+v)", m, err, got)
	}
	_ = ctl.Send(proto.Msg{Op: "config", Config: &proto.Config{}})
	if err := ctl.RecvMax(&m, proto.MaxEvent); err != nil || m.Op != "error" || !strings.Contains(m.Error, "already") {
		t.Fatalf("a second config: %+v %v", m, err)
	}
}

// A session's environment is its exec's, plus the default PATH when that
// names none — never the agent's own (os.StartProcess inherits it for a nil
// Env).
func TestSessionEnvClean(t *testing.T) {
	t.Setenv("AGENTCORE_AGENT_ONLY", "leaked")
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env(1)")
	}
	h := newHarness(t, nil)
	h.env = nil
	for i, c := range []struct {
		env  []string
		want string
	}{
		{nil, "PATH=" + defaultPATH + "\n"},
		{[]string{"FOO=1"}, "PATH=" + defaultPATH + "\nFOO=1\n"},
		{[]string{"FOO=1", "PATH=/nowhere"}, "FOO=1\nPATH=/nowhere\n"},
	} {
		session := 2 + i
		ss := h.exec(proto.Exec{Session: session, Path: envBin, Argv: []string{"env"}, Env: c.env, Merge: true, NoStdin: true})
		if out := readAll(t, ss["stdout"]); out != c.want {
			t.Errorf("env %q: the session saw %q, want %q", c.env, out, c.want)
		}
		h.wait(session, "exited")
	}
	// a bare name resolves against the default PATH, not the agent's
	t.Setenv("PATH", "/nowhere")
	ss := h.exec(proto.Exec{Session: 9, Argv: []string{"env"}, Merge: true, NoStdin: true})
	if out := readAll(t, ss["stdout"]); out != "PATH="+defaultPATH+"\n" {
		t.Errorf("a bare env(1): %q", out)
	}
	if m := h.wait(9, "exited", "error"); m.Op != "exited" || m.Code != 0 {
		t.Errorf("a bare env(1): %+v", m)
	}
}
