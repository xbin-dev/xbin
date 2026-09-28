//go:build linux && integration

package vm

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// A resident VM (plans/tile-sandbox-runtime.md §2.5), driven the way the
// tile-sandbox runtime drives one: through the connection factory only —
// one ctl, execs as sessions from 2, a connection per stream and per file
// operation (resident_client_linux_test.go). Under KVM, and again with
// XBIN_VM_ACCEL=emulate (make integration).
func TestResidentVM(t *testing.T) {
	m, ws := harness(t)
	tile := filepath.Join(ws, "tiles", "t1")
	state := filepath.Join(ws, ".xbin", "sbx", "ck", "box")
	for _, d := range []string{tile, state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tile, "from-host.txt"), []byte("host bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	disk, err := EnsureDiskAt(state, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	probe := buildGoProgram(t, `package main

import ("fmt"; "os")

func main() { fmt.Println("probe-ok", len(os.Args)) }
`)
	binds := []sandbox.Bind{
		{Src: tile, Dst: tile},
		{Src: probe, Dst: "/run/xbin-probe", RO: true}, // a single-file export (WP-0's path)
	}
	// 1 vCPU: the single-file exec is the case that hung before WP-0
	vm := startResident(t, m, binds, disk, filepath.Join(state, "lock"), 1)
	c := vm.c

	// the shim holds the factory; the VMM never does
	holders, unreadable := vm.factoryHolders(t)
	t.Logf("factory held by %v (fds unreadable here: %v)", holders, unreadable)
	if len(holders) != 1 || !strings.HasPrefix(holders[0], strconv.Itoa(vm.cmd.Process.Pid)+" ") {
		if len(unreadable) == 0 || len(holders) > 1 {
			t.Errorf("the factory's holders are %v, want the shim (pid %d) alone", holders, vm.cmd.Process.Pid)
		}
	}

	t.Run("exec", func(t *testing.T) {
		r := c.run(t, proto.Exec{Argv: []string{"sh", "-c", "echo out; echo err >&2; hostname; exit 3"}}, "")
		if r.code != 3 || r.stdout != "out\nbox\n" || r.stderr != "err\n" {
			t.Errorf("got %+v", r)
		}
		r = c.run(t, proto.Exec{Argv: []string{"cat"}}, "stdin reaches it\n")
		if r.code != 0 || r.stdout != "stdin reaches it\n" {
			t.Errorf("cat: %+v", r)
		}
		r = c.run(t, proto.Exec{Argv: []string{"sh", "-c", "echo a; echo b >&2"}, Merge: true, NoStdin: true}, "")
		if r.code != 0 || r.stdout != "a\nb\n" {
			t.Errorf("merged: %+v", r)
		}
		r = c.run(t, proto.Exec{Path: "/run/xbin-probe", Argv: []string{"probe", "x"}}, "")
		if r.code != 0 || r.stdout != "probe-ok 2\n" {
			t.Errorf("the single-file export: %+v", r)
		}
		// WP-3b: user work, not the guest's agent, is what an OOM kill takes.
		// The agent sets a session's score just after it started
		// (adjustOOM), so the session waits for its own (at most 2 s) before
		// it reads it and the agent's: a bare cat could read it first.
		r = c.run(t, proto.Exec{Argv: []string{"sh", "-c", `i=0
while [ "$(cat /proc/$$/oom_score_adj)" != 500 ] && [ $i -lt 200 ]; do sleep 0.01; i=$((i+1)); done
cat /proc/$$/oom_score_adj /proc/1/oom_score_adj`}}, "")
		if r.code != 0 || r.stdout != "500\n0\n" {
			t.Errorf("the oom_score_adj of an exec, then of the agent: %+v", r)
		}
	})

	t.Run("strict cwd", func(t *testing.T) {
		r := c.run(t, proto.Exec{Argv: []string{"pwd"}, Cwd: "/nonexistent", CwdStrict: true}, "")
		if r.err == "" {
			t.Errorf("a missing strict cwd ran: %+v", r)
		}
		r = c.run(t, proto.Exec{Argv: []string{"pwd"}, Cwd: tile, CwdStrict: true}, "")
		if r.code != 0 || r.stdout != tile+"\n" {
			t.Errorf("strict cwd %s: %+v", tile, r)
		}
		r = c.run(t, proto.Exec{Argv: []string{"pwd"}, Cwd: "/nonexistent"}, "")
		if r.code != 0 || r.stdout != "/\n" {
			t.Errorf("a missing cwd, not strict: %+v", r)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		const n = 20
		var wg sync.WaitGroup
		res := make([]execResult, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res[i] = c.run(t, proto.Exec{Argv: []string{"sh", "-c", `echo "$0"; sleep 0.2; echo "e$0" >&2; exit $(($0 % 7))`, strconv.Itoa(i)}}, "")
			}()
		}
		wg.Wait()
		for i, r := range res {
			if r.code != i%7 || r.stdout != fmt.Sprintf("%d\n", i) || r.stderr != fmt.Sprintf("e%d\n", i) {
				t.Errorf("exec %d: %+v", i, r)
			}
		}
	})

	t.Run("tty", func(t *testing.T) {
		x := c.start(t, proto.Exec{Argv: []string{"sh", "-c", "stty size; read x; stty size; exit 5"}, TTY: true, Rows: 24, Cols: 80})
		x.expect(t, x.pty, "24 80")
		c.send(t, proto.Msg{Op: "resize", Session: x.id, Rows: 30, Cols: 100})
		time.Sleep(100 * time.Millisecond) // the resize reaches the guest before the line does
		_, _ = x.pty.Write([]byte("\n"))
		x.expect(t, x.pty, "30 100")
		if code, _, err := x.wait(t); code != 5 || err != "" {
			t.Errorf("tty exec ended %d %q", code, err)
		}
	})

	t.Run("group signal", func(t *testing.T) {
		x := c.start(t, proto.Exec{Argv: []string{"sh", "-c", "sleep 300 & echo $!; wait"}, NoStdin: true})
		pid := strings.TrimSpace(x.expect(t, x.stdout, "\n"))
		c.send(t, proto.Msg{Op: "signal", Session: x.id, Signal: int(unix.SIGTERM), Group: true})
		if code, sig, err := x.wait(t); sig != int(unix.SIGTERM) || code != 128+int(unix.SIGTERM) || err != "" {
			t.Errorf("ended %d (signal %d) %q, want SIGTERM", code, sig, err)
		}
		// the grandchild went too (a moment to be reaped by the guest's PID 1)
		check := fmt.Sprintf(`for i in $(seq 50); do kill -0 %s 2>/dev/null || { echo gone; exit 0; }; sleep 0.1; done; echo alive`, pid)
		if r := c.run(t, proto.Exec{Argv: []string{"sh", "-c", check}}, ""); r.stdout != "gone\n" {
			t.Errorf("the grandchild %s outlived a group signal: %+v", pid, r)
		}
	})

	t.Run("files on the disk", func(t *testing.T) {
		res, _ := c.file(t, proto.FileOp{Op: "write", Path: "/root/wp4/f.txt", Mkdirs: true}, []byte("disk bytes"))
		if !res.OK || res.Stat == nil || res.Stat.Size != 10 {
			t.Fatalf("write: %+v", res)
		}
		etag := res.Stat.ETag
		if res, data := c.file(t, proto.FileOp{Op: "read", Path: "/root/wp4/f.txt", Offset: 5}, nil); !res.OK || string(data) != "bytes" {
			t.Errorf("ranged read: %+v %q", res, data)
		}
		if res, _ := c.file(t, proto.FileOp{Op: "write", Path: "/root/wp4/f.txt", IfMatch: "nope"}, []byte("x")); res.Refusal != proto.RefusePrecondition {
			t.Errorf("a stale IfMatch: %+v", res)
		}
		if res, _ := c.file(t, proto.FileOp{Op: "write", Path: "/root/wp4/f.txt", IfMatch: etag}, []byte("v2")); !res.OK {
			t.Errorf("a matching IfMatch: %+v", res)
		}
		if res, _ := c.file(t, proto.FileOp{Op: "mkdir", Path: "/root/wp4/d/e", Parents: true}, nil); !res.OK {
			t.Errorf("mkdir: %+v", res)
		}
		if res, _ := c.file(t, proto.FileOp{Op: "move", Path: "/root/wp4/f.txt", To: "/root/wp4/d/g.txt"}, nil); !res.OK {
			t.Errorf("move: %+v", res)
		}
		res, _ = c.file(t, proto.FileOp{Op: "list", Path: "/root/wp4/d"}, nil)
		var names []string
		for _, e := range res.Entries {
			names = append(names, e.Name+":"+e.Type)
		}
		if got := strings.Join(names, ","); !res.OK || got != "e:dir,g.txt:file" {
			t.Errorf("list: %+v (%s)", res, got)
		}
		if res, _ := c.file(t, proto.FileOp{Op: "remove", Path: "/root/wp4/d", Recursive: true}, nil); !res.OK {
			t.Errorf("remove: %+v", res)
		}
		if res, _ := c.file(t, proto.FileOp{Op: "stat", Path: "/root/wp4/d"}, nil); res.Refusal != proto.RefuseNotFound {
			t.Errorf("stat after remove: %+v", res)
		}
	})

	t.Run("files through a mount", func(t *testing.T) {
		if res, data := c.file(t, proto.FileOp{Op: "read", Path: filepath.Join(tile, "from-host.txt")}, nil); !res.OK || string(data) != "host bytes" {
			t.Errorf("read a host file: %+v %q", res, data)
		}
		if res, _ := c.file(t, proto.FileOp{Op: "write", Path: filepath.Join(tile, "from-guest.txt")}, []byte("guest bytes")); !res.OK {
			t.Errorf("write through the mount: %+v", res)
		}
		if b, err := os.ReadFile(filepath.Join(tile, "from-guest.txt")); string(b) != "guest bytes" {
			t.Errorf("the host sees %q, %v", b, err)
		}
	})

	t.Run("tar", func(t *testing.T) {
		var in bytes.Buffer
		tw := tar.NewWriter(&in)
		for _, h := range []*tar.Header{
			{Name: "a.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
			{Name: "sub/", Mode: 0o755, Typeflag: tar.TypeDir},
			{Name: "sub/b.txt", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg},
			{Name: "link", Linkname: "a.txt", Typeflag: tar.TypeSymlink},
		} {
			_ = tw.WriteHeader(h)
			if h.Size > 0 {
				_, _ = tw.Write([]byte(h.Name[len(h.Name)-5 : len(h.Name)-4]))
			}
		}
		_ = tw.Close()
		if res, _ := c.file(t, proto.FileOp{Op: "tar-put", Path: "/root/tarred", Mkdirs: true}, in.Bytes()); !res.OK {
			t.Fatalf("tar-put: %+v", res)
		}
		res, out := c.file(t, proto.FileOp{Op: "tar-get", Path: "/root/tarred"}, nil)
		if !res.OK {
			t.Fatalf("tar-get: %+v", res)
		}
		got := map[string]string{}
		tr := tar.NewReader(bytes.NewReader(out))
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(tr)
			got[strings.TrimSuffix(h.Name, "/")] = string(b) + h.Linkname
		}
		for name, want := range map[string]string{"a.txt": "a", "sub": "", "sub/b.txt": "b", "link": "a.txt"} {
			if v, ok := got[name]; !ok || v != want {
				t.Errorf("tar-get %s: %q (have %v)", name, v, got)
			}
		}
	})

	// the last exec leaves something only a flush keeps: the factory's close
	// must sync the disk before the VM goes
	if r := c.run(t, proto.Exec{Argv: []string{"sh", "-c", "echo kept > /root/persist"}, NoSync: true}, ""); r.code != 0 {
		t.Fatalf("persist: %+v", r)
	}
	select {
	case <-vm.exited:
		t.Fatalf("the VM ended with its execs:\n%s", vm.out.String())
	default:
	}

	// xbind is gone: the VMM is gone within 3 s (18 s emulated), the lock free
	start := time.Now()
	vm.f.Close()
	limit := 3 * time.Second
	if m.Status().Emulated {
		limit = 18 * time.Second
	}
	select {
	case <-vm.exited:
	case <-time.After(limit):
		t.Fatalf("the VM outlived its factory by %s\n%s", limit, vm.out.String())
	}
	t.Logf("gone %s after the factory closed (exit %d)", time.Since(start).Round(time.Millisecond), vm.cmd.ProcessState.ExitCode())
	if code := vm.cmd.ProcessState.ExitCode(); code != 129 {
		t.Errorf("the shim exited %d, want 129\n%s", code, vm.out.String())
	}
	if !lockFree(t, filepath.Join(state, "lock")) {
		t.Error("the state lock is still held after the VM ended")
	}

	// the next VM on the same disk finds the file; SIGHUP ends it (129)
	vm2 := startResident(t, m, binds, disk, filepath.Join(state, "lock"), 2)
	if r := vm2.c.run(t, proto.Exec{Argv: []string{"cat", "/root/persist"}}, ""); r.stdout != "kept\n" {
		t.Errorf("the flush on the factory's close lost the file: %+v", r)
	}
	start = time.Now()
	_ = vm2.cmd.Process.Signal(syscall.SIGHUP)
	select {
	case <-vm2.exited:
	case <-time.After(limit):
		t.Fatalf("SIGHUP didn't end the VM within %s\n%s", limit, vm2.out.String())
	}
	t.Logf("SIGHUP: gone after %s", time.Since(start).Round(time.Millisecond))
	if code := vm2.cmd.ProcessState.ExitCode(); code != 129 {
		t.Errorf("the shim exited %d on SIGHUP, want 129\n%s", code, vm2.out.String())
	}
}

// residentVM is one resident VM and its client.
type residentVM struct {
	cmd        *exec.Cmd
	out        lockedBuf
	exited     chan struct{}
	f          *sandbox.Factory
	factoryIno uint64 // the factory's child end, as a socket inode
	c          *rclient
}

// factoryHolders lists the VM's processes — the shim (the sandbox's PID 1)
// and its children, the VMM — holding the factory's child end, and those
// whose fds this test can't read.
func (vm *residentVM) factoryHolders(t *testing.T) (holders, unreadable []string) {
	t.Helper()
	shim := vm.cmd.Process.Pid
	pids := []int{shim}
	all, _ := filepath.Glob("/proc/[0-9]*/stat")
	for _, p := range all {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
		if len(f) > 1 && f[1] == strconv.Itoa(shim) {
			pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(p)))
			pids = append(pids, pid)
		}
	}
	want := fmt.Sprintf("socket:[%d]", vm.factoryIno)
	for _, pid := range pids {
		comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		name := fmt.Sprintf("%d (%s)", pid, strings.TrimSpace(string(comm)))
		fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			unreadable = append(unreadable, name)
			continue
		}
		for _, fd := range fds {
			if l, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name())); l == want {
				holders = append(holders, name)
			}
		}
	}
	return holders, unreadable
}

// startResident launches a resident VM as the runtime will — the factory as
// spec.Agent, the state lock as spec.Lock (flocked here, then the
// sandbox's alone) — and waits for its "ready".
func startResident(t *testing.T, m *Manager, binds []sandbox.Bind, disk, lockPath string, vcpus int) *residentVM {
	t.Helper()
	f, child, err := sandbox.NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(lockPath, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("the state lock is held: %v", err)
	}
	spec := &sandbox.Spec{
		Lower:    []string{m.Rootfs},
		Binds:    binds,
		Env:      []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Cwd:      "/",
		HostUID:  os.Getuid(),
		HostGID:  os.Getgid(),
		Agent:    child,
		Lock:     lock,
		Hostname: "box",
		NoFollow: true,
	}
	// what a resident VM can't have is an error
	for _, bad := range []struct {
		spec sandbox.Spec
		o    Options
	}{
		{sandbox.Spec{Lower: spec.Lower}, Options{Resident: true}}, // no factory
		{*spec, Options{Resident: true, Listen: "/run/x.sock"}},
		{*spec, Options{Resident: true, Gateway: "/run/gw.sock"}},
		{*spec, Options{Resident: true, TTY: true}},
		{*spec, Options{}}, // a factory, but no resident shim to take it
	} {
		if err := m.Apply(context.Background(), &bad.spec, bad.o); err == nil {
			t.Errorf("Apply took a resident VM with %+v (agent %v)", bad.o, bad.spec.Agent != nil)
		}
	}
	if err := m.Apply(context.Background(), spec, Options{Resident: true, VCPUs: vcpus, MemMiB: 512, Disk: disk}); err != nil {
		t.Fatal(err)
	}
	if !spec.VM.Resident || spec.VM.Guest.Path != "" || len(spec.VM.Guest.Argv) != 0 || spec.Agent != child || spec.Lock != lock || spec.VM.Hostname != "box" {
		t.Fatalf("Apply's resident spec: %+v (agent kept %v, lock kept %v)", spec.VM, spec.Agent == child, spec.Lock == lock)
	}
	cmd, h, err := sandbox.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	vm := &residentVM{cmd: cmd, exited: make(chan struct{}), f: f}
	var st unix.Stat_t
	if err := unix.Fstat(int(child.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	vm.factoryIno = st.Ino
	cmd.Stdout, cmd.Stderr = &vm.out, &vm.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.Started() // xbind's copies of the factory's child end and the lock go
	go func() { _ = cmd.Wait(); close(vm.exited) }()
	t.Cleanup(func() {
		f.Close()
		_ = cmd.Process.Kill()
		<-vm.exited
		h.Cleanup()
		if t.Failed() {
			t.Logf("shim output:\n%s", vm.out.String())
		}
	})
	if err := h.SetupUserns(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	vm.c = newRClient(t, f, vm.exited)
	t.Logf("resident VM ready after %s", time.Since(start).Round(time.Millisecond))
	return vm
}

// lockFree reports whether nothing holds the flock on path any more.
func lockFree(t *testing.T, path string) bool {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}

// buildGoProgram builds src as a static binary.
func buildGoProgram(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "prog")
	cmd := exec.Command("go", "build", "-o", out, "main.go")
	cmd.Dir = dir
	cmd.Env = append(cleanEnv("GOFLAGS", "GOWORK"), "CGO_ENABLED=0", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	return out
}
