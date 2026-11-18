//go:build linux && integration

// Run with: go test -tags=integration -run '^TestHold' ./internal/sandbox/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs); skips otherwise. The fuse flavour needs fuse-overlayfs
// (XBIN_FUSE_OVERLAYFS, bin/, PATH).
package sandbox

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func holdRootfs(t *testing.T) string {
	t.Helper()
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(fs, "bin/sh")); err != nil || !Available() {
		t.Skip("no rootfs with sh, or no user namespaces")
	}
	return fs
}

// holdFlavours are the flavours this host can mount: the kernel's always,
// fuse-overlayfs where there is one.
func holdFlavours() map[string]string {
	fl := map[string]string{HoldKernel: ""}
	if p := fuseOverlayfsPath(); p != "" {
		fl[HoldFuse] = p
	}
	return fl
}

// holdDirs makes a lower with two files and a directory, and an empty layer.
func holdDirs(t *testing.T) (lower, upper, work string) {
	t.Helper()
	dir := t.TempDir()
	lower, upper, work = filepath.Join(dir, "lower"), filepath.Join(dir, "layer", "upper"), filepath.Join(dir, "layer", "work")
	for _, d := range []string{filepath.Join(lower, "d"), upper, work} {
		mkdir(t, d)
	}
	for name, body := range map[string]string{"a": "real-a\n", "d/f": "real-f\n"} {
		if err := os.WriteFile(filepath.Join(lower, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(work, "work"), 0o700) }) // the kernel overlay's own, mode 000
	return
}

// holdRun runs script in a sandbox over rootfs with a clone of hd's overlay
// at /home/h, and returns what it printed.
func holdRun(t *testing.T, rootfs string, hd *Hold, lower, script string) string {
	t.Helper()
	f, err := hd.Mount()
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	spec := &Spec{
		Lower: []string{rootfs},
		Binds: []Bind{{Src: lower, Dst: "/home/h", Mount: f}},
		Entry: "/bin/sh", Argv: []string{"sh", "-c", script},
		Env:     []string{"PATH=" + RootfsPATH, "HOME=/home/h"},
		HostUID: os.Getuid(), HostGID: os.Getgid(),
		NoFollow: true, FollowBase: true, MountGuard: true,
	}
	cmd, h, err := Launch(spec)
	if err != nil {
		f.Close()
		t.Fatalf("launch: %v", err)
	}
	defer h.Cleanup()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		f.Close()
		t.Fatalf("start: %v", err)
	}
	f.Close() // the init holds its own
	if err := h.SetupUserns(); err != nil {
		t.Fatalf("userns: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("the sandbox did not end:\n%s", out.String())
	}
	return out.String()
}

// One overlay, two sandboxes: each sees the lower's files and the other's
// writes; the lower is never written; the upper holds the writes; a
// directory of the lower renames (fuse) or moves by copy (kernel).
func TestHoldClones(t *testing.T) {
	rootfs := holdRootfs(t)
	for name, fuse := range holdFlavours() {
		t.Run(name, func(t *testing.T) {
			lower, upper, work := holdDirs(t)
			hd, err := StartHold(HoldSpec{Lower: lower, Upper: upper, Work: work, Fuse: fuse}, os.Getuid(), os.Getgid())
			if err != nil {
				t.Fatalf("start the hold: %v", err)
			}
			defer hd.Close()
			if hd.Flavour() != name {
				t.Fatalf("flavour %q, want %q", hd.Flavour(), name)
			}
			one := holdRun(t, rootfs, hd, lower, `cd ~ && cat a d/f && echo new > b && echo more >> a && mkdir -p .claude && echo hook > .claude/settings.json && mv d d2 && ls -a && stat -c '%u %a' . b`)
			for _, want := range []string{"real-a", "real-f", "d2", ".claude", "0 644"} {
				if !strings.Contains(one, want) {
					t.Fatalf("first sandbox: no %q in\n%s", want, one)
				}
			}
			two := holdRun(t, rootfs, hd, lower, `cd ~ && cat a b .claude/settings.json d2/f; test -e d || echo d-''gone; echo x > /home/h/c`)
			for _, want := range []string{"real-a", "more", "new", "hook", "real-f", "d-gone"} {
				if !strings.Contains(two, want) {
					t.Fatalf("second sandbox: no %q in\n%s", want, two)
				}
			}
			// the lower is as it was
			if b, _ := os.ReadFile(filepath.Join(lower, "a")); string(b) != "real-a\n" {
				t.Fatalf("the lower's file changed: %q", b)
			}
			ents, _ := os.ReadDir(lower)
			if len(ents) != 2 {
				t.Fatalf("the lower's entries changed: %v", ents)
			}
			// the upper holds the writes, owned by this user (root in the sandbox)
			for _, f := range []string{"a", "b", "c", ".claude/settings.json"} {
				fi, err := os.Stat(filepath.Join(upper, f))
				if err != nil {
					t.Fatalf("the upper lacks %s: %v", f, err)
				}
				if uid := fi.Sys().(*syscall.Stat_t).Uid; int(uid) != os.Getuid() {
					t.Errorf("%s in the upper is uid %d, want %d", f, uid, os.Getuid())
				}
			}
			pid := hd.cmd.Process.Pid
			hd.Close()
			select {
			case <-hd.Done():
			default:
				t.Fatal("Close returned before the hold was gone")
			}
			if err := syscall.Kill(pid, 0); err == nil {
				t.Fatalf("the hold's process %d is still there", pid)
			}
			if _, err := hd.Mount(); err == nil {
				t.Fatal("a clone from a closed hold")
			}
			holdNoMounts(t, filepath.Dir(lower))
		})
	}
}

// holdNoMounts fails when this mount namespace holds a mount under dir:
// a hold mounts nothing where the host sees it.
func holdNoMounts(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, dir) {
			t.Fatalf("a mount under %s is visible on the host: %s", dir, line)
		}
	}
}

// A hold that cannot mount says why, and leaves nothing running; a Mount
// bind without its mount, or outside a NoFollow root, never becomes a bind
// of its source.
func TestHoldFailsClosed(t *testing.T) {
	rootfs := holdRootfs(t)
	lower, upper, work := holdDirs(t)
	for _, c := range []struct{ name, fuse, want string }{
		{"fuse-overlayfs missing", "/nonexistent/fuse-overlayfs", "start fuse-overlayfs"},
		{"fuse-overlayfs dies", "/bin/false", "it exited before serving the overlay"},
	} {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			hd, err := StartHold(HoldSpec{Lower: lower, Upper: upper, Work: work, Fuse: c.fuse}, os.Getuid(), os.Getgid())
			if err == nil {
				hd.Close()
				t.Fatal("the hold started")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q, want %q in it", err, c.want)
			}
			if d := time.Since(start); d > 20*time.Second {
				t.Fatalf("the refusal took %v", d)
			}
		})
	}
	t.Run("no lower", func(t *testing.T) {
		if hd, err := StartHold(HoldSpec{Lower: filepath.Join(lower, "nope"), Upper: upper, Work: work}, os.Getuid(), os.Getgid()); err == nil {
			hd.Close()
			t.Fatal("the hold started over a lower that isn't there")
		} else if !strings.Contains(err.Error(), "bind the lower layer") {
			t.Fatalf("error %q", err)
		}
	})
	t.Run("a mount bind needs NoFollow", func(t *testing.T) {
		hd, err := StartHold(HoldSpec{Lower: lower, Upper: upper, Work: work}, os.Getuid(), os.Getgid())
		if err != nil {
			t.Fatal(err)
		}
		defer hd.Close()
		f, err := hd.Mount()
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, _, err = Launch(&Spec{Lower: []string{rootfs}, Binds: []Bind{{Src: lower, Dst: "/home/h", Mount: f}}, Entry: "/bin/sh",
			HostUID: os.Getuid(), HostGID: os.Getgid()})
		if err == nil || !strings.Contains(err.Error(), "NoFollow") {
			t.Fatalf("launch without NoFollow: %v", err)
		}
	})
	t.Run("a dead hold's clone is refused, not replaced by the source", func(t *testing.T) {
		hd, err := StartHold(HoldSpec{Lower: lower, Upper: upper, Work: work}, os.Getuid(), os.Getgid())
		if err != nil {
			t.Fatal(err)
		}
		f, err := hd.Mount()
		if err != nil {
			t.Fatal(err)
		}
		f.Close() // the sandbox gets a closed file: the init must fail, never bind Src
		spec := &Spec{Lower: []string{rootfs}, Binds: []Bind{{Src: lower, Dst: "/home/h", Mount: f}},
			Entry: "/bin/sh", Argv: []string{"sh", "-c", "echo RAN-''IT > /home/h/planted"},
			HostUID: os.Getuid(), HostGID: os.Getgid(), NoFollow: true, FollowBase: true}
		hd.Close()
		cmd, h, err := Launch(spec)
		if err == nil {
			defer h.Cleanup()
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			if err = cmd.Start(); err == nil {
				_ = h.SetupUserns()
				err = cmd.Wait()
			}
			if err == nil {
				t.Fatalf("the sandbox ran without its mount:\n%s", out.String())
			}
		}
		if _, serr := os.Stat(filepath.Join(lower, "planted")); serr == nil {
			t.Fatal("the source was bound in the mount's place: the lower was written")
		}
	})
}

// Fifty holds started and ended leave no process and no mount, and each
// start finds the layer the last one left.
func TestHoldCycles(t *testing.T) {
	rootfs := holdRootfs(t)
	for name, fuse := range holdFlavours() {
		t.Run(name, func(t *testing.T) {
			lower, upper, work := holdDirs(t)
			n := 50
			if testing.Short() {
				n = 5
			}
			var pids []int
			for i := 0; i < n; i++ {
				hd, err := StartHold(HoldSpec{Lower: lower, Upper: upper, Work: work, Fuse: fuse}, os.Getuid(), os.Getgid())
				if err != nil {
					t.Fatalf("cycle %d: %v", i, err)
				}
				pids = append(pids, hd.cmd.Process.Pid)
				if i%10 == 0 { // a sandbox on it now and then: the layer carries over
					out := holdRun(t, rootfs, hd, lower, fmt.Sprintf("cat ~/n 2>/dev/null; echo %d > ~/n", i))
					if want := fmt.Sprint(i - 10); i > 0 && !strings.Contains(out, want) {
						t.Fatalf("cycle %d: the layer lost the last write (want %s):\n%s", i, want, out)
					}
				}
				hd.Close()
			}
			for _, pid := range pids {
				if err := syscall.Kill(pid, 0); err == nil {
					t.Fatalf("hold %d is still running", pid)
				}
			}
			holdNoMounts(t, filepath.Dir(lower))
		})
	}
}
