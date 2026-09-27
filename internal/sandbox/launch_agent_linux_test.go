//go:build linux && integration

// Run with: go test -tags=integration ./internal/sandbox/
// Needs unprivileged user namespaces (skips if unavailable). A minimal lower
// with a static probe built here, so CI (no .rootfs) runs it; fuse-overlayfs
// is used when Launch finds one ($XBIN_FUSE_OVERLAYFS, next to the binary,
// $PATH), else the kernel overlay.
package sandbox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// agentReport is what testdata/agentprobe answers.
type agentReport struct {
	Args         []string          `json:"args"`
	Hostname     string            `json:"hostname"`
	AgentFD      int               `json:"agentFd"`
	AgentCloexec bool              `json:"agentCloexec"`
	LockFD       int               `json:"lockFd"`
	LockIno      uint64            `json:"lockIno"`
	Ops          map[string]string `json:"ops"`
}

func buildAgentProbe(t *testing.T, out string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, "./testdata/agentprobe")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0") // static → minimal rootfs
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agentprobe: %v\n%s", err, b)
	}
}

// probeRoot is a minimal lower holding the probe, and an empty persistent
// upper/work pair (what the sandbox "wrote" is planted in upper by the test).
func probeRoot(t *testing.T) (dir, lower, upper, work string) {
	t.Helper()
	dir = t.TempDir()
	lower, upper, work = filepath.Join(dir, "lower"), filepath.Join(dir, "upper"), filepath.Join(dir, "work")
	for _, d := range []string{lower, upper, work} {
		mkdir(t, d)
	}
	buildAgentProbe(t, filepath.Join(lower, "probe"))
	return
}

func probeSpec(lower, upper, work string, ops ...string) *Spec {
	return &Spec{
		Lower: []string{lower}, Upper: upper, Work: work,
		Entry: "/probe", Argv: append([]string{"/probe"}, ops...),
		Env:     []string{"PATH=/"},
		HostUID: os.Getuid(), HostGID: os.Getgid(),
	}
}

// startSpec launches and starts spec, completing the uid mapping.
func startSpec(t *testing.T, spec *Spec) (*exec.Cmd, *Handle, *bytes.Buffer) {
	t.Helper()
	cmd, h, err := Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Cleanup)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("sandbox creation denied by environment: %v", err)
		}
		t.Fatal(err)
	}
	h.Started()
	if err := h.SetupUserns(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("userns: %v\n%s", err, buf.Bytes())
	}
	return cmd, h, &buf
}

// runProbe runs a probe without an agent to its end: its report, or the
// start's failure with everything it printed.
func runProbe(t *testing.T, spec *Spec) (agentReport, string, error) {
	t.Helper()
	cmd, h, buf := startSpec(t, spec)
	if h.NeedsRelay() {
		if fd, err := h.RecvTUN(); err == nil {
			unix.Close(fd) // no egress wanted, only the init's side of it
		}
	}
	err := cmd.Wait()
	var r agentReport
	if err == nil {
		if jerr := json.Unmarshal(lastJSONLine(buf.Bytes()), &r); jerr != nil {
			t.Fatalf("probe output not JSON: %v\n%s", jerr, buf.Bytes())
		}
	}
	return r, buf.String(), err
}

// covers WP-2 — the entry (a stand-in agent) finds the factory and the lock
// by the numbers in its argv, the factory inheritable and nothing else
// telling; xbind reaches it by dialling after Handle.Started closed xbind's
// copies; the sandbox alone then holds the lock, fuse-overlayfs included,
// and fuse-overlayfs never holds the factory; the hostname is set; closing
// the factory ends the sandbox and releases the lock.
func TestAgentFactoryAndLock(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	dir, lower, upper, work := probeRoot(t)
	lockPath := filepath.Join(dir, "lock")
	lock, err := os.OpenFile(lockPath, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	fac, child, err := NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer fac.Close()
	var fst, lst unix.Stat_t
	if err := unix.Fstat(int(child.Fd()), &fst); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fstat(int(lock.Fd()), &lst); err != nil {
		t.Fatal(err)
	}

	spec := probeSpec(lower, upper, work, "agent")
	spec.Agent, spec.Lock, spec.Hostname, spec.NoFollow = child, lock, "sbx-test", true
	cmd, _, buf := startSpec(t, spec)
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	t.Logf("fuse-overlayfs=%q agentFd=%d lockFd=%d", spec.FuseOverlay, spec.AgentFD, spec.LockFD)

	c, err := fac.Dial()
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	var r agentReport
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&r); err != nil {
		t.Fatalf("no answer over the factory: %v\n%s", err, buf.Bytes())
	}
	c.Close()
	wantArgs := []string{"agent", "--fd", strconv.Itoa(spec.AgentFD), "--lock", strconv.Itoa(spec.LockFD)}
	if spec.AgentFD == 0 || spec.LockFD != spec.AgentFD+1 || !slices.Equal(r.Args, wantArgs) {
		t.Errorf("argv %q (agent %d, lock %d), want %q", r.Args, spec.AgentFD, spec.LockFD, wantArgs)
	}
	if r.AgentFD != spec.AgentFD || r.AgentCloexec {
		t.Errorf("the entry's factory: fd %d cloexec=%v, want fd %d inheritable", r.AgentFD, r.AgentCloexec, spec.AgentFD)
	}
	if r.LockFD != spec.LockFD || r.LockIno != lst.Ino {
		t.Errorf("the entry's lock: fd %d ino %d, want fd %d ino %d", r.LockFD, r.LockIno, spec.LockFD, lst.Ino)
	}
	if r.Hostname != "sbx-test" {
		t.Errorf("hostname %q, want sbx-test", r.Hostname)
	}

	// xbind's copy is closed (Started): the sandbox alone holds the lock.
	if err := tryLock(lockPath); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Errorf("the lock while the sandbox runs: %v, want EWOULDBLOCK", err)
	}
	if spec.FuseOverlay != "" {
		fuse := childNamed(t, cmd.Process.Pid, "fuse-overlayfs")
		if fuse == 0 {
			t.Fatalf("no fuse-overlayfs under the init (pid %d)", cmd.Process.Pid)
		}
		links := fdLinks(t, fuse)
		if slices.Contains(links, "socket:["+strconv.FormatUint(fst.Ino, 10)+"]") {
			t.Errorf("fuse-overlayfs holds the factory: %q", links)
		}
		if !slices.Contains(links, lockPath) {
			t.Errorf("fuse-overlayfs does not hold the lock: %q", links)
		}
	}

	// The factory's EOF ends the agent, the pid namespace with it, and the lock.
	fac.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil || !strings.Contains(buf.String(), "accept: EOF") {
			t.Errorf("the agent after the factory closed: %v\n%s", err, buf.Bytes())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the agent outlived the factory\n%s", buf.Bytes())
	}
	deadline := time.Now().Add(5 * time.Second)
	for tryLock(lockPath) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the lock outlived the sandbox")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// covers WP-2 — Bind.Sub shows a path beneath a trusted root (a directory
// read-only, a file) and refuses a symlink anywhere in the sub-path, whether
// the root is NoFollow or not.
func TestBindSub(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	for _, noFollow := range []bool{false, true} {
		t.Run("nofollow="+strconv.FormatBool(noFollow), func(t *testing.T) {
			dir, lower, upper, work := probeRoot(t)
			res, outside := filepath.Join(dir, "res"), filepath.Join(dir, "outside")
			mkdir(t, filepath.Join(res, "data"))
			mkdir(t, outside)
			for f, body := range map[string]string{filepath.Join(res, "data", "x"): "res-data", filepath.Join(outside, "f"): "outside"} {
				if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, filepath.Join(res, "link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../outside", filepath.Join(res, "data", "up")); err != nil {
				t.Fatal(err)
			}
			spec := probeSpec(lower, upper, work, "read:/mnt/data/x", "write:/mnt/data/y", "read:/etc/x")
			spec.NoFollow = noFollow
			spec.Binds = []Bind{
				{Src: res, Sub: "data", Dst: "/mnt/data", RO: true},
				{Src: res, Sub: "data/x", Dst: "/etc/x", RO: true},
			}
			r, out, err := runProbe(t, spec)
			if err != nil {
				t.Fatalf("sandbox run: %v\n%s", err, out)
			}
			for op, want := range map[string]string{"read:/mnt/data/x": "res-data", "write:/mnt/data/y": "ERR", "read:/etc/x": "res-data"} {
				if got := r.Ops[op]; got != want {
					t.Errorf("%s = %q, want %q", op, got, want)
				}
			}
			for _, sub := range []string{"link", "link/f", "data/up/f"} {
				spec := probeSpec(lower, upper, work, "read:/mnt/f")
				spec.NoFollow = noFollow
				spec.Binds = []Bind{{Src: res, Sub: sub, Dst: "/mnt/f", RO: true}}
				if _, out, err := runProbe(t, spec); err == nil || !strings.Contains(out, "a symlink is in the way") {
					t.Errorf("sub %q through a symlink: %v\n%s", sub, err, out)
				}
			}
		})
	}
}

// covers WP-2 — in a NoFollow root, a symlink the sandbox planted in its
// upper where a mount point goes (a bind's, /proc's) fails the start naming
// the path and makes nothing where it points; binds nest (a read-only tree
// sealed only after the bind inside it) and masks hide.
func TestNoFollowMountPoints(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	for _, c := range []struct{ plant, dst, want string }{
		{"opt", "/opt/xbin/bin/bx", "nested mount point /opt: a symlink is in the way"},
		{"work", "/work", "nested mount point /work: a symlink is in the way"},
		{"proc", "", "nested mount point /proc: a symlink is in the way"},
		{"tmp", "", "nested mount point /tmp: a symlink is in the way"},
		{"dev", "", "nested mount point /dev: a symlink is in the way"},
		{".oldroot", "", "nested mount point /.oldroot: a symlink is in the way"},
	} {
		t.Run(c.plant, func(t *testing.T) {
			dir, lower, upper, work := probeRoot(t)
			outside, src := filepath.Join(dir, "outside"), filepath.Join(dir, "src")
			mkdir(t, outside)
			mkdir(t, src)
			if err := os.Symlink(outside, filepath.Join(upper, c.plant)); err != nil {
				t.Fatal(err)
			}
			spec := probeSpec(lower, upper, work)
			spec.NoFollow = true
			if c.dst != "" {
				spec.Binds = []Bind{{Src: src, Dst: c.dst, RO: true}}
				if strings.HasSuffix(c.dst, "/bx") {
					spec.Binds[0].Src = filepath.Join(lower, "probe") // a file bind
				}
			}
			_, out, err := runProbe(t, spec)
			if err == nil || !strings.Contains(out, c.want) {
				t.Errorf("start through a planted symlink: %v, want %q in\n%s", err, c.want, out)
			}
			if ents, _ := os.ReadDir(outside); len(ents) != 0 {
				t.Errorf("the init made %v where the planted symlink points", ents)
			}
		})
	}

	t.Run("nested", func(t *testing.T) {
		dir, lower, upper, work := probeRoot(t)
		code, res := filepath.Join(dir, "code"), filepath.Join(dir, "res")
		for _, d := range []string{filepath.Join(code, "hidden"), res} {
			mkdir(t, d)
		}
		for f, body := range map[string]string{filepath.Join(code, "f"): "code", filepath.Join(code, "hidden", "s"): "secret"} {
			if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		spec := probeSpec(lower, upper, work, "read:/code/f", "write:/code/new", "write:/code/data/x", "read:/code/hidden/s")
		spec.NoFollow = true
		spec.Binds = []Bind{
			{Src: code, Dst: "/code", RO: true},
			{Src: res, Dst: "/code/data"}, // its mount point is missing from code: made, then /code sealed
			{Dst: "/code/hidden", Mask: true, RO: true},
		}
		r, out, err := runProbe(t, spec)
		if err != nil {
			t.Fatalf("sandbox run: %v\n%s", err, out)
		}
		for op, want := range map[string]string{
			"read:/code/f": "code", "write:/code/new": "ERR", "write:/code/data/x": "ok", "read:/code/hidden/s": "ERR",
		} {
			if got := r.Ops[op]; got != want {
				t.Errorf("%s = %q, want %q", op, got, want)
			}
		}
		if b, err := os.ReadFile(filepath.Join(res, "x")); err != nil || string(b) != "w" {
			t.Errorf("the nested read-write bind: %q %v", b, err)
		}
	})
}

// covers WP-2 — the egress setup's /etc/resolv.conf never follows a symlink
// the sandbox planted in its upper, NoFollow or not: before pivot_root an
// absolute one would resolve to a host file. It is replaced by a regular
// file inside the sandbox, and the host file is untouched.
func TestResolvConfNeverFollowed(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no /dev/net/tun")
	}
	dir, lower, upper, work := probeRoot(t)
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(upper, "etc"))
	if err := os.Symlink(victim, filepath.Join(upper, "etc", "resolv.conf")); err != nil {
		t.Fatal(err)
	}
	spec := probeSpec(lower, upper, work, "read:/etc/resolv.conf")
	spec.Net = "relay"
	r, out, err := runProbe(t, spec)
	if err != nil {
		t.Fatalf("sandbox run: %v\n%s", err, out)
	}
	if got := r.Ops["read:/etc/resolv.conf"]; !strings.HasPrefix(got, "nameserver 10.0.2.3\n") {
		t.Errorf("the sandbox's resolv.conf: %q", got)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Errorf("the host file behind the planted symlink was written: %q", b)
	}
}

func tryLock(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// childNamed is the pid of a child of ppid named comm (0 = none).
func childNamed(t *testing.T, ppid int, comm string) int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		i := bytes.LastIndexByte(b, ')')
		if err != nil || i < 0 {
			continue
		}
		name := string(b[bytes.IndexByte(b, '(')+1 : i])
		f := strings.Fields(string(b[i+1:]))
		if len(f) > 1 && name == comm && f[1] == strconv.Itoa(ppid) {
			return pid
		}
	}
	return 0
}

// fdLinks is what each of pid's fds points at.
func fdLinks(t *testing.T, pid int) []string {
	t.Helper()
	dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		if l, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil {
			out = append(out, l)
		}
	}
	return out
}
