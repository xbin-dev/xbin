//go:build linux

package xbindtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Options tune a daemon. The zero value is an isolated, --no-auth, --dev
// xbind on a fresh workspace over the assets' rootfs.
type Options struct {
	WS     string   // the workspace ("" = a fresh one, removed when the test ends)
	Rootfs string   // "" = the assets' (a GC test passes a copy: CopyRootfs)
	Env    []string // added to the daemon's environment (after the defaults)
	Args   []string // added to its command line
	// Auth boots it with owner auth on (no --no-auth): people are real
	// accounts (AddUser, Login), and Call and Dial send the owner token
	// unless a request carries a credential of its own.
	Auth bool
	// Ready bounds the boot (healthz answering); 0 = 60 s.
	Ready time.Duration
	// Addr is the address it listens on ("" = a free port on 127.0.0.1): a
	// run that shares the host with others keeps to the ports it was given
	// (hack/demo/measure).
	Addr string
}

// Daemon is one isolated xbind, started and stopped by the test.
type Daemon struct {
	URL  string // http://127.0.0.1:<port>
	Addr string // 127.0.0.1:<port>
	WS   string
	A    *Assets
	o    Options
	own  bool // the workspace is the helper's: removed at the end

	token string  // the owner token (Options.Auth; XBIN_E2E_TOKEN)
	rem   *remote // a daemon the test doesn't run (Connect)

	mu    sync.Mutex
	cmd   *exec.Cmd
	done  chan struct{}
	log   *os.File
	seen  map[int]string // every pid seen in its process tree, with its command line
	boots int
}

// Start boots an isolated xbind (--dev --no-auth --isolate) and waits for it
// to answer. Its log goes to a file of its own, whose tail is printed when
// the test fails. When the test ends its tile sandboxes are deleted (their
// state removed, confined), it is stopped, every process it started is
// checked gone, and a fresh workspace is removed.
func Start(t testing.TB, a *Assets, o Options) *Daemon {
	t.Helper()
	d := &Daemon{A: a, o: o, seen: map[int]string{}}
	if o.WS == "" {
		dir, err := os.MkdirTemp("", "xbindtest-ws-*")
		if err != nil {
			t.Fatal(err)
		}
		d.WS, d.own = filepath.Join(dir, "ws"), true
	} else {
		d.WS = o.WS
	}
	if d.Addr = o.Addr; d.Addr == "" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		d.Addr = ln.Addr().String()
		ln.Close()
	}
	d.URL = "http://" + d.Addr
	t.Cleanup(func() { d.cleanup(t) })
	d.start(t)
	return d
}

// Rootfs is the rootfs the daemon runs over.
func (d *Daemon) Rootfs() string {
	if d.o.Rootfs != "" {
		return d.o.Rootfs
	}
	return d.A.Rootfs
}

func (d *Daemon) start(t testing.TB) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.log == nil {
		f, err := os.CreateTemp("", "xbindtest-log-*.log")
		if err != nil {
			t.Fatal(err)
		}
		d.log = f
	}
	d.boots++
	fmt.Fprintf(d.log, "\n===== xbindtest: boot %d =====\n", d.boots)
	args := []string{"--dev", "--isolate", "--rootfs", d.Rootfs(), "--workspace", d.WS, "--listen", d.Addr}
	if !d.o.Auth {
		args = append(args, "--no-auth")
	}
	cmd := exec.Command(filepath.Join(d.A.Bin, "xbind"), append(args, d.o.Args...)...)
	cmd.Dir = d.A.Repo // --dev serves web/ and docs/ from here
	env := append(os.Environ(),
		"XBIN_SDK_PATH="+filepath.Join(d.A.Repo, "sdk"),
		"XBIN_BIN="+d.A.Bin,
		"XBIN_FUSE_OVERLAYFS="+orNone(d.A.FuseOverlayfs),
	)
	if d.A.Gocryptfs != "" {
		env = append(env, "XBIN_GOCRYPTFS="+d.A.Gocryptfs)
	}
	env = append(env, d.A.vmEnv()...)
	cmd.Env = append(env, d.o.Env...)
	cmd.Stdout, cmd.Stderr = d.log, d.log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d.cmd, d.done = cmd, make(chan struct{})
	go func(done chan struct{}) { _ = cmd.Wait(); close(done) }(d.done)
	ready := d.o.Ready
	if ready == 0 {
		ready = 60 * time.Second
	}
	deadline := time.Now().Add(ready)
	for {
		if r, err := http.Get(d.URL + "/healthz"); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				if d.o.Auth {
					b, err := os.ReadFile(filepath.Join(d.WS, ".xbin", "token"))
					if err != nil {
						t.Fatalf("the owner token: %v", err)
					}
					d.token = strings.TrimSpace(string(b))
				}
				return
			}
		}
		select {
		case <-d.done:
			t.Fatalf("xbind exited while booting:\n%s", logTail(d.log, 60)) // d.mu is held: never LogTail
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("xbind didn't answer /healthz within %s:\n%s", ready, logTail(d.log, 60))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func orNone(p string) string {
	if p == "" {
		return "none"
	}
	return p
}

// PID is the running xbind's pid (0 when stopped).
func (d *Daemon) PID() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd == nil {
		return 0
	}
	return d.cmd.Process.Pid
}

// Stop stops xbind as a service manager would: SIGTERM (it stops its tile
// sandboxes, synced, and its VMs), then SIGKILL to its process group after
// 90 s. Every process it started must be gone after: one still running is
// killed by its pid and fails the test.
func (d *Daemon) Stop(t testing.TB) {
	t.Helper()
	if d.rem != nil {
		t.Fatal("xbindtest: a remote xbind isn't the test's to stop")
	}
	d.mu.Lock()
	cmd, done := d.cmd, d.done
	d.cmd = nil
	d.mu.Unlock()
	if cmd == nil {
		return
	}
	d.track(cmd.Process.Pid)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Errorf("xbind (pid %d) didn't exit within 90 s of SIGTERM: killed", cmd.Process.Pid)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // stragglers in its group (a go build)
	d.reap(t)
}

// Restart stops xbind and boots it again on the same workspace and
// address (a remote one: XBIN_E2E_RESTART).
func (d *Daemon) Restart(t testing.TB) {
	t.Helper()
	if d.rem != nil {
		d.restartRemote(t)
		return
	}
	d.Stop(t)
	d.start(t)
}

// Kill kills xbind's process group at once (a crash): what it started dies
// with it or is killed by the reaper check.
func (d *Daemon) Kill(t testing.TB) {
	t.Helper()
	if d.rem != nil {
		t.Fatal("xbindtest: a remote xbind isn't the test's to kill")
	}
	d.mu.Lock()
	cmd, done := d.cmd, d.done
	d.cmd = nil
	d.mu.Unlock()
	if cmd == nil {
		return
	}
	d.track(cmd.Process.Pid)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
	d.reap(t)
}

// track records every process in pid's tree (children by /proc's ppid
// links), so reap can check them gone.
func (d *Daemon) track(pid int) {
	kids := map[int][]int{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if pp := ppid(p); pp > 0 {
			kids[pp] = append(kids[pp], p)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var walk func(int)
	walk = func(p int) {
		if _, ok := d.seen[p]; ok {
			return
		}
		d.seen[p] = cmdline(p)
		for _, k := range kids[p] {
			walk(k)
		}
	}
	walk(pid)
}

// reap waits (10 s) for every process track saw to be gone — the same pid
// running the same command line — and kills and reports what isn't.
func (d *Daemon) reap(t testing.TB) {
	t.Helper()
	d.mu.Lock()
	seen := d.seen
	d.seen = map[int]string{}
	d.mu.Unlock()
	alive := func() []int {
		var out []int
		for p, cl := range seen {
			if cl != "" && cmdline(p) == cl {
				out = append(out, p)
			}
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(alive()) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	for _, p := range alive() {
		t.Errorf("xbind left pid %d running (%s): killed", p, seen[p])
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

func ppid(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// pid (comm) state ppid …; comm may hold spaces and parens
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

func cmdline(pid int) string {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return string(bytes.ReplaceAll(bytes.TrimRight(b, "\x00"), []byte{0}, []byte{' '}))
}

// LogTail is the last n lines of the daemon's log (all of its boots).
func (d *Daemon) LogTail(n int) string {
	d.mu.Lock()
	f := d.log
	d.mu.Unlock()
	return logTail(f, n)
}

// logTail is the last n lines of a daemon log, for callers that hold d.mu
// (start's boot failures: LogTail there would wait on the lock forever, and
// the test would hang until go test's timeout instead of failing).
func logTail(f *os.File, n int) string {
	if f == nil {
		return ""
	}
	b, _ := os.ReadFile(f.Name())
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// LogPath is the daemon's log file.
func (d *Daemon) LogPath() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.log == nil {
		return ""
	}
	return d.log.Name()
}

// cleanup deletes the tile sandboxes (so their state, sub-uid-owned in
// range mode, is removed the daemon's own confined way), stops the daemon,
// and removes a workspace the helper made, and the log unless the test
// failed.
func (d *Daemon) cleanup(t testing.TB) {
	if d.rem != nil {
		if err := d.deleteTileSandboxes(5 * time.Minute); err != nil {
			t.Logf("xbindtest: deleting the tile sandboxes: %v", err)
		}
		if t.Failed() {
			t.Logf("xbindtest: the remote xbind's log:\n%s", d.remoteLogs())
		}
		return
	}
	if d.PID() != 0 {
		if err := d.deleteTileSandboxes(2 * time.Minute); err != nil {
			t.Logf("xbindtest: deleting the tile sandboxes: %v", err)
		}
		d.Stop(t)
	}
	if t.Failed() {
		t.Logf("xbindtest: xbind's log (%s), its tail:\n%s", d.LogPath(), d.LogTail(80))
	} else if p := d.LogPath(); p != "" {
		_ = os.Remove(p)
	}
	if d.log != nil {
		d.log.Close()
	}
	if d.own {
		RemoveTree(filepath.Dir(d.WS))
	}
}

// deleteTileSandboxes deletes every tile sandbox as an admin and waits for
// the removal backlog (.trash) to drain.
func (d *Daemon) deleteTileSandboxes(timeout time.Duration) error {
	var list struct {
		TileSandboxes []struct{ Tile, Name string } `json:"tileSandboxes"`
	}
	if err := d.get("/api/xbin/sandboxes", &list); err != nil {
		return err
	}
	var errs []error
	for _, s := range list.TileSandboxes {
		st, body, err := d.do(http.MethodDelete, "/api/xbin/sandboxes/"+s.Name+"?tile="+s.Tile, nil, nil)
		if err != nil || (st != 204 && st != 404) {
			errs = append(errs, fmt.Errorf("delete %s %s: %d %s %v", s.Tile, s.Name, st, body, err))
		}
	}
	deadline := time.Now().Add(timeout)
	for {
		var h struct {
			Health struct {
				TileSandboxes struct {
					Trash struct{ Entries int } `json:"trash"`
				} `json:"tileSandboxes"`
			} `json:"health"`
			TileSandboxes []struct{} `json:"tileSandboxes"`
		}
		if err := d.get("/api/xbin/sandboxes", &h); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if h.Health.TileSandboxes.Trash.Entries == 0 && len(h.TileSandboxes) == 0 {
			return errors.Join(errs...)
		}
		if time.Now().After(deadline) {
			return errors.Join(append(errs, fmt.Errorf("the removal backlog didn't drain in %s (%d entries, %d sandboxes)",
				timeout, h.Health.TileSandboxes.Trash.Entries, len(h.TileSandboxes)))...)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func bgCtx() context.Context { return context.Background() }
