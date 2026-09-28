//go:build linux && integration

package sandbox

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// covers the fuse-overlayfs root wedge (fuseroot_linux.go) — a regular file
// created directly in `/` of a fuse-overlayfs root used to wedge the server
// for good: rooted in its own mount, it walked that mount for
// /proc/self/fd/… while serving the write, and waited on itself. Under the
// daemonizing mount (terminals, backends) and the watched one with NoFollow
// (tile sandboxes): the entry writes /x and reads it back, the write lands in
// the upper, a child still gets a user namespace of its own (the sandbox's
// root is the top of its mount namespace, not a chroot, which the kernel
// would refuse it), and fuse-overlayfs's root is a tmpfs holding only /proc:
// neither the FUSE mount it serves nor the host's tree.
func TestFuseRootCreateInRoot(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	fuse := os.Getenv("XBIN_FUSE_OVERLAYFS")
	if !isExecutable(fuse) {
		fuse, _ = filepath.Abs("../../bin/fuse-overlayfs")
	}
	if !isExecutable(fuse) {
		fuse, _ = exec.LookPath("fuse-overlayfs")
	}
	if !isExecutable(fuse) {
		t.Skip("no fuse-overlayfs (make fuse-overlayfs, XBIN_FUSE_OVERLAYFS or PATH)")
	}
	t.Setenv("XBIN_FUSE_OVERLAYFS", fuse)
	for _, v := range []struct {
		name            string
		watch, noFollow bool
	}{{"daemonized", false, false}, {"watched", true, true}} {
		t.Run(v.name, func(t *testing.T) {
			_, lower, upper, work := probeRoot(t)
			fac, child, err := NewFactory()
			if err != nil {
				t.Fatal(err)
			}
			defer fac.Close()
			spec := probeSpec(lower, upper, work, "write:/x", "read:/x", "userns")
			spec.Agent, spec.FuseWatch, spec.NoFollow = child, v.watch, v.noFollow
			cmd, _, buf := startSpec(t, spec)
			// A wedged root holds a thread of PID 1 in a request fuse-overlayfs
			// took: only killing the server frees it.
			fuse := 0
			defer func() {
				if fuse != 0 {
					_ = unix.Kill(fuse, unix.SIGKILL)
				}
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}()
			for deadline := time.Now().Add(10 * time.Second); fuse == 0 && time.Now().Before(deadline); {
				if fuse = childNamed(t, cmd.Process.Pid, "fuse-overlayfs"); fuse == 0 {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if fuse == 0 {
				t.Fatalf("no fuse-overlayfs under the init (pid %d)\n%s", cmd.Process.Pid, buf.Bytes())
			}

			c, err := fac.Dial()
			if err != nil {
				t.Fatalf("dial: %v\n%s", err, buf.Bytes())
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			var r agentReport
			if err := json.NewDecoder(bufio.NewReader(c)).Decode(&r); err != nil {
				t.Fatalf("no report (a file created in / wedged fuse-overlayfs?): %v\n%s", err, buf.Bytes())
			}
			for op, want := range map[string]string{"write:/x": "ok", "read:/x": "w", "userns": "ok"} {
				if got := r.Ops[op]; got != want {
					t.Errorf("%s: %q, want %q", op, got, want)
				}
			}
			if b, err := os.ReadFile(filepath.Join(upper, "x")); err != nil || string(b) != "w" {
				t.Errorf("the upper's x: %q %v", b, err)
			}

			var st unix.Statfs_t
			proc := func(pid int, p string) string { return filepath.Join("/proc", strconv.Itoa(pid), p) }
			if err := unix.Statfs(proc(cmd.Process.Pid, "root")+"/", &st); err != nil || st.Type != fuseMagic {
				t.Errorf("the sandbox's root: statfs type %#x %v, want FUSE", st.Type, err)
			}
			if err := unix.Statfs(proc(fuse, "root")+"/", &st); err != nil || st.Type != unix.TMPFS_MAGIC {
				t.Errorf("fuse-overlayfs's root: statfs type %#x %v, want a tmpfs", st.Type, err)
			}
			ents, err := os.ReadDir(proc(fuse, "root"))
			if err != nil || len(ents) != 1 || ents[0].Name() != "proc" {
				t.Errorf("fuse-overlayfs's root holds %v (%v), want only proc", ents, err)
			}
			if err := unix.Statfs(proc(fuse, "root/proc")+"/", &st); err != nil || st.Type != unix.PROC_SUPER_MAGIC {
				t.Errorf("fuse-overlayfs's /proc: statfs type %#x %v, want proc", st.Type, err)
			}
			var root, cwd unix.Stat_t
			if unix.Stat(proc(fuse, "root")+"/", &root) != nil || unix.Stat(proc(fuse, "cwd")+"/", &cwd) != nil ||
				root.Dev != cwd.Dev || root.Ino != cwd.Ino {
				t.Errorf("fuse-overlayfs's cwd (%d:%d) isn't its root (%d:%d)", cwd.Dev, cwd.Ino, root.Dev, root.Ino)
			}
		})
	}
}
