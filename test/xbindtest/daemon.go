//go:build linux

package xbindtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	"github.com/xbin-dev/xbin/test/portlease"
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
	// TryAddrs are tried, in order, before a free port is picked: for the
	// harness's own test of a taken port. Nothing else sets it.
	TryAddrs []string
	// Cgroup is a cgroup directory the daemon starts in (clone3's
	// CLONE_INTO_CGROUP), every boot: a delegated cgroup of its own, where
	// xbind makes its tiles' leaves ("" = the test's own; test/limits).
	Cgroup string
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
	lease func() // lets go of the port's reservation among harnesses (portlease), held while the daemon lives
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
		d.pickAddr(t)
	}
	d.URL = "http://" + d.Addr
	t.Cleanup(func() { d.cleanup(t) })
	d.start(t)
	return d
}

// A port for a daemon is picked before xbind binds it, and xbind takes
// seconds to get there (it initializes the workspace first). In that window
// the kernel may hand the same port to anyone — in practice to another
// daemon of this harness, in a parallel test or a parallel `go test`: two
// xbinds then want one port, one fails to bind, and the loser's test used
// to carry on talking to the winner (its /healthz answered). Three things
// close that, none of them a retry of the test:
//
//   - harnesses don't pick the same port: a port is reserved among them by
//     a lock file held while the daemon lives (test/portlease, which every
//     helper that picks a port for a program to bind uses);
//   - a boot is ready only when OUR xbind says it listens (its start-up
//     line, after its bind) and is still running — never because something
//     answered on the port;
//   - a first boot that loses its port to a process outside the harness
//     picks another and boots again; a restart, which keeps its address,
//     waits for it.

// pickAddr gives the daemon an address of the harness's choosing.
func (d *Daemon) pickAddr(t testing.TB) {
	t.Helper()
	if d.lease != nil {
		d.lease()
		d.lease = nil
	}
	if len(d.o.TryAddrs) > 0 {
		d.Addr, d.o.TryAddrs = d.o.TryAddrs[0], d.o.TryAddrs[1:]
	} else {
		addr, lease, err := portlease.Reserve()
		if err != nil {
			t.Fatal(err)
		}
		d.Addr, d.lease = addr, lease
	}
	d.URL = "http://" + d.Addr
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
	first := d.boots == 0 // a first boot may move to another port; a restart keeps its address
	ready := d.o.Ready
	if ready == 0 {
		ready = 60 * time.Second
	}
	for attempt := 1; ; attempt++ {
		mark := d.spawn(t)
		why := d.await(mark, ready)
		if why == "" {
			if d.o.Auth {
				b, err := os.ReadFile(filepath.Join(d.WS, ".xbin", "token"))
				if err != nil {
					t.Fatalf("the owner token: %v", err)
				}
				d.token = strings.TrimSpace(string(b))
			}
			return
		}
		// our xbind didn't come up. Its port taken by someone else is the
		// one failure the harness answers itself (the comment above)
		if !strings.Contains(logSince(d.log, mark), "address already in use") || attempt >= 6 {
			t.Fatalf("%s:\n%s", why, logTail(d.log, 60)) // d.mu is held: never LogTail
		}
		switch {
		case first && d.o.Addr == "":
			was := d.Addr
			d.pickAddr(t)
			t.Logf("xbindtest: %s was taken before xbind bound it: booting on %s", was, d.Addr)
		default:
			t.Logf("xbindtest: %s is taken: waiting for it (attempt %d)", d.Addr, attempt)
			time.Sleep(time.Second)
		}
	}
}

// listenLines are what xbind prints once it has bound its listener (boot's
// serve): the proof that the daemon answering on the port is ours.
var listenLines = []string{"xbin (no auth):", "xbin login URL:"}

// spawn starts xbind and returns where this boot's log begins.
func (d *Daemon) spawn(t testing.TB) (mark int64) {
	t.Helper()
	d.boots++
	fmt.Fprintf(d.log, "\n===== xbindtest: boot %d =====\n", d.boots)
	if fi, err := os.Stat(d.log.Name()); err == nil {
		mark = fi.Size()
	}
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
	if d.o.Cgroup != "" {
		f, err := os.OpenFile(d.o.Cgroup, os.O_RDONLY|syscall.O_DIRECTORY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close() // once started: the child is in it
		cmd.SysProcAttr.UseCgroupFD, cmd.SysProcAttr.CgroupFD = true, int(f.Fd())
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d.cmd, d.done = cmd, make(chan struct{})
	go func(done chan struct{}) { _ = cmd.Wait(); close(done) }(d.done)
	return mark
}

// await waits until the xbind spawn started is up: it is still running, it
// has said it listens, and it answers /healthz. "" when it is; otherwise
// why not (it exited, or didn't come up within ready). The order matters:
// an answer on the port proves nothing while our xbind hasn't bound it.
func (d *Daemon) await(mark int64, ready time.Duration) string {
	deadline := time.Now().Add(ready)
	for {
		select {
		case <-d.done:
			return "xbind exited while booting"
		default:
		}
		if log := logSince(d.log, mark); strings.Contains(log, listenLines[0]) || strings.Contains(log, listenLines[1]) {
			if r, err := http.Get(d.URL + "/healthz"); err == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					select {
					case <-d.done: // it answered and died, or never was ours
						return "xbind exited while booting"
					default:
						return ""
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Sprintf("xbind didn't answer /healthz within %s", ready)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// logSince is the log from offset mark on.
func logSince(f *os.File, mark int64) string {
	if f == nil {
		return ""
	}
	b, _ := os.ReadFile(f.Name())
	if mark > int64(len(b)) {
		return ""
	}
	return string(b[mark:])
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
	if d.lease != nil { // the port is anyone's again
		d.lease()
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
