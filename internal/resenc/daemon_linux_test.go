//go:build linux

package resenc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The I2 tests: a sandbox's mount namespace starts as a copy of xbind's,
// every encrypted view mounted at that moment included, and keeps the copy
// for the sandbox's life — so an unmount in xbind's namespace alone left
// the view's gocryptfs serving it, and the view's next mount ran a second
// gocryptfs on the same ciphertext.

const pinnedKey = ".partitions/apps~x/main/u-alice/fs" // a user partition's volume: the idle unmount's

// sandboxNS starts a process in a user and mount namespace of its own, as a
// rootless sandbox is, which keeps its copy of the test's mounts — the
// encrypted view's among them — until the test ends. script runs in it
// with the arguments args; it says "ready" once set up, then keeps the
// namespace (exec cat: until its stdin closes). The answer writes a line to
// its stdin and reads one back. It skips where there are no unprivileged
// user namespaces.
func sandboxNS(t *testing.T, script string, args ...string) (ask func(string) string) {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare absent — skipping the namespace test")
	}
	if out, err := exec.Command(unshare, "--user", "--map-root-user", "--mount", "true").CombinedOutput(); err != nil {
		t.Skipf("no unprivileged user+mount namespaces here — skipping: %v: %s", err, bytes.TrimSpace(out))
	}
	cmd := exec.Command(unshare, append([]string{"--user", "--map-root-user", "--mount", "sh", "-c", script, "sh"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _, _ = io.Copy(io.Discard, stdout); _ = cmd.Wait() })
	r := bufio.NewReader(stdout)
	read := func() string {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("the namespace's process: %v (stderr: %s)", err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSuffix(line, "\n")
	}
	if got := read(); got != "ready" {
		t.Fatalf("the namespace's process said %q (stderr: %s)", got, strings.TrimSpace(stderr.String()))
	}
	return func(line string) string {
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatal(err)
		}
		return read()
	}
}

// gocryptfsOn lists the gocryptfs processes serving cipher as the QA box's
// count did: by command line — gocryptfs, -fg (the daemonized child every
// mount leaves running), and the cipher directory.
func gocryptfsOn(t *testing.T, cipher string) []int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
		hasFg, hasCipher := false, false
		for _, a := range args[1:] {
			hasFg = hasFg || a == "-fg"
			hasCipher = hasCipher || a == cipher
		}
		if filepath.Base(args[0]) == "gocryptfs" && hasFg && hasCipher {
			out = append(out, pid)
		}
	}
	return out
}

// pidfd opens process pid's pidfd: its exit can be waited on, and its pid
// names no other process meanwhile.
func pidfd(t *testing.T, pid int) int {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Skipf("no pidfd here (Linux 5.3+): %v", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

// exited reports whether the process behind pidfd fd has exited, waiting at
// most within for it: poll wakes at the exit, so within only bounds a
// failure. 0 asks whether it has already.
func exited(fd int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		ms := max(int(time.Until(deadline)/time.Millisecond), 0)
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, ms)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return n > 0
	}
}

// pinnedView mounts alice's partition volume with a file in it and answers
// its mountpoint, its cipher directory and its one gocryptfs's pid.
func pinnedView(t *testing.T, m *Manager) (mnt, cipher string, pid int) {
	t.Helper()
	mnt, err := m.Ensure("res:apps/x/db", pinnedKey, "db", false)
	if err != nil {
		t.Skipf("gocryptfs mount failed (no userns/FUSE perms here?): %v", err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "hello"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	cipher = m.CipherDir(pinnedKey, "db")
	pids := gocryptfsOn(t, cipher)
	if len(pids) != 1 {
		t.Fatalf("one mount runs %d gocryptfs: %v", len(pids), pids)
	}
	return mnt, cipher, pids[0]
}

// remountOnce mounts the view again and checks that exactly one gocryptfs
// serves its ciphertext, not old's, and the file is there.
func remountOnce(t *testing.T, m *Manager, cipher string, old int) {
	t.Helper()
	mnt, err := m.Ensure("res:apps/x/db", pinnedKey, "db", false)
	if err != nil {
		t.Fatalf("remount: %v", err)
	}
	if pids := gocryptfsOn(t, cipher); len(pids) != 1 || pids[0] == old {
		t.Errorf("after the remount %v serve the ciphertext, want one new gocryptfs (the earlier was %d)", pids, old)
	}
	if b, err := os.ReadFile(filepath.Join(mnt, "hello")); err != nil || string(b) != "kept" {
		t.Errorf("the file through the remounted view: %q, %v", b, err)
	}
}

// covers PD-48 I2 — the idle unmount ends the view's gocryptfs although a
// sandbox started while it was mounted holds a copy of the mount: it
// removes the mountpoint, which detaches every namespace's copy. Then the
// next start runs exactly one gocryptfs on the ciphertext.
func TestIdleUnmountEndsPinnedDaemon(t *testing.T) {
	m, _ := testManager(t)
	t.Cleanup(m.Close)
	mnt, cipher, pid := pinnedView(t, m)
	sandboxNS(t, `echo ready; exec cat`) // a sandbox started now: a copy of the view's mount in its namespace
	fd := pidfd(t, pid)

	m.Expire(pinnedKey, "db")
	if got := m.UnmountIdle(time.Now(), time.Hour, nil); len(got) != 1 {
		t.Fatalf("the idle unmount took down %v, want alice's view", got)
	}
	if !exited(fd, 30*time.Second) {
		t.Fatalf("the idle-unmounted view's gocryptfs (pid %d) still runs: the sandbox's copy of its mount keeps it", pid)
	}
	if m.Mounted(pinnedKey, "db") {
		t.Error("still mounted")
	}
	if _, err := os.Stat(mnt); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the mountpoint stays: %v", err)
	}
	remountOnce(t, m, cipher, pid)
}

// covers PD-48 I2 — the second-gocryptfs guard: a gocryptfs still serving a
// view's ciphertext when the view mounts again (a copy of its mount with a
// file held open in it, which no removal frees — or an unmount from before
// the fix) is ended before the mount, never run beside.
func TestRemountEndsStaleDaemon(t *testing.T) {
	m, _ := testManager(t)
	t.Cleanup(m.Close)
	mnt, cipher, pid := pinnedView(t, m)
	sandboxNS(t, `cd "$1" && echo ready && exec cat`, mnt) // its working directory inside its copy of the view
	fd := pidfd(t, pid)

	// an unmount as xbind made them before: its own mount only
	if err := fusermountU(mnt, false); err != nil {
		t.Fatal(err)
	}
	m.forget(mkey(pinnedKey, "db"))
	if exited(fd, 0) {
		t.Fatal("the gocryptfs exited: nothing pins it, so this tests nothing")
	}
	prev := daemonExitGrace
	daemonExitGrace = 0 // the held file pins it past any wait: straight to SIGTERM (the outcome is the same)
	t.Cleanup(func() { daemonExitGrace = prev })

	remountOnce(t, m, cipher, pid)
	if !exited(fd, 0) {
		t.Errorf("the earlier gocryptfs (pid %d) still runs beside the new one", pid)
	}
}

// covers PD-48 I2 — what removing a mountpoint leaves alone: a sandbox's
// own bind of the view (an instance's /data) is on a directory of its own,
// so it keeps working, and the unmount neither waits on its gocryptfs for
// ever nor ends it.
func TestUnmountKeepsSandboxBind(t *testing.T) {
	m, root := testManager(t)
	t.Cleanup(m.Close)
	mnt, _, pid := pinnedView(t, m)
	bind := filepath.Join(root, "sandbox-data")
	if err := os.Mkdir(bind, 0o700); err != nil {
		t.Fatal(err)
	}
	ask := sandboxNS(t, `mount --bind "$1" "$2" && echo ready && while read -r f; do cat "$2/$f" && echo; done`, mnt, bind)
	fd := pidfd(t, pid)
	prev := daemonExitGrace
	daemonExitGrace = 0 // the bind keeps it: don't wait for an exit that won't come
	t.Cleanup(func() { daemonExitGrace = prev })

	if err := m.Unmount(pinnedKey, "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mnt); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the mountpoint stays: %v", err)
	}
	if got := ask("hello"); got != "kept" {
		t.Errorf("the sandbox's bind of the view reads %q after the unmount", got)
	}
	if exited(fd, 0) {
		t.Error("the gocryptfs serving the sandbox's bind exited")
	}
}
