//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// InitArg is the hidden subcommand buxond (and the test binary) must dispatch
// to RunInit *before* flag parsing — it is the re-exec target that runs inside
// the new namespaces.
const InitArg = "__sandbox-init"

// Available reports whether unprivileged user namespaces are usable here.
func Available() bool {
	if b, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		return len(b) > 0 && b[0] == '1'
	}
	// The sysctl is absent on kernels that always allow it; assume yes on Linux.
	return true
}

// RecvTUN blocks until the init sends the TUN fd (via SCM_RIGHTS) and returns it.
func (h *Handle) RecvTUN() (int, error) { return recvFD(h.ctrl) }

// Launch builds (but does not start) an *exec.Cmd that, when started, re-execs
// this binary as PID 1 of a fresh user+mount+pid+net+ipc+uts namespace set,
// where the init reads the spec and execs the backend. The caller keeps its own
// Stdout/Stderr/Start/Wait wiring; after Start, call h.RecvTUN if h.NeedsRelay.
func Launch(s *Spec) (*exec.Cmd, *Handle, error) {
	h := &Handle{}

	// Choose the uid model and overlay backend up front — the init reads the
	// resulting fd numbers / fuse path out of the spec, so they must be decided
	// before we serialize it.
	ids := detectIDRanges(s.HostUID, s.HostGID)
	s.FuseOverlay = fuseOverlayfsPath()

	// ExtraFiles land at fd 3, 4, … in the init, in append order.
	var extra []*os.File
	nextFD := 3
	fail := func(err error) (*exec.Cmd, *Handle, error) {
		for _, f := range extra {
			f.Close()
		}
		if h.ctrl != nil {
			h.ctrl.Close()
		}
		return nil, nil, err
	}

	if s.Net == "relay" {
		parent, child, err := socketpair()
		if err != nil {
			return fail(err)
		}
		extra = append(extra, child)
		s.CtrlFD = nextFD
		nextFD++
		h.ctrl = parent
	}

	// Range uid mapping needs a helper (newuidmap) run from the parent against
	// the child pid; the init must block until it's done, so pass it a sync pipe.
	var syncW *os.File
	if ids != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		extra = append(extra, r)
		s.SyncFD = nextFD
		nextFD++
		syncW = w
	}

	f, err := os.CreateTemp("", "bx-spec-*.json")
	if err != nil {
		if syncW != nil {
			syncW.Close()
		}
		return fail(err)
	}
	if err := json.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		os.Remove(f.Name())
		if syncW != nil {
			syncW.Close()
		}
		return fail(err)
	}
	f.Close()

	clone := uintptr(unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID |
		unix.CLONE_NEWIPC | unix.CLONE_NEWUTS)
	if !s.HostNet {
		clone |= unix.CLONE_NEWNET // components get their own (default-deny) netns
	}
	cmd := exec.Command("/proc/self/exe", InitArg, f.Name())
	cmd.ExtraFiles = extra
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: clone}
	if ids == nil {
		// Single-uid: the kernel writes a 1:1 map (container root → this user)
		// and denies setgroups — the rootless default when no sub-ids are
		// delegated. apt & friends that switch users won't work in this mode.
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: s.HostUID, Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: s.HostGID, Size: 1}}
		cmd.SysProcAttr.GidMappingsEnableSetgroups = false
	}
	// Range mode leaves the maps unset here; SetupUserns writes them post-Start.

	h.cleanup = func() {
		os.Remove(f.Name())
		if syncW != nil {
			syncW.Close()
		}
	}
	if ids != nil {
		h.setup = func() error {
			defer func() {
				if syncW != nil {
					syncW.Close()
					syncW = nil
				}
			}()
			if cmd.Process == nil {
				return errors.New("SetupUserns: sandbox not started")
			}
			if err := ids.apply(cmd.Process.Pid); err != nil {
				return err // syncW closed by defer → init reads EOF and aborts
			}
			_, err := syncW.Write([]byte{1}) // release the init
			return err
		}
	}
	return cmd, h, nil
}

// idRanges is a container→host uid/gid mapping using a delegated sub-id range:
// container 0 → the buxond user (root-in-container stays this user, as before),
// container 1..N → the sub-uid/gid range from /etc/subuid,/etc/subgid. This maps
// a full uid space into the sandbox so apt/dpkg's user switches and non-root
// in-container users work.
type idRanges struct {
	hostUID, hostGID   int
	uidStart, uidCount int
	gidStart, gidCount int
}

// detectIDRanges returns a range mapping if sub-ids are delegated for this user
// and newuidmap/newgidmap are available; otherwise nil (→ single-uid mode).
func detectIDRanges(hostUID, hostGID int) *idRanges {
	if _, err := exec.LookPath("newuidmap"); err != nil {
		return nil
	}
	if _, err := exec.LookPath("newgidmap"); err != nil {
		return nil
	}
	uStart, uCount, ok := subIDRange("/etc/subuid", hostUID)
	if !ok {
		return nil
	}
	gStart, gCount, ok := subIDRange("/etc/subgid", hostGID)
	if !ok {
		return nil
	}
	return &idRanges{hostUID, hostGID, uStart, uCount, gStart, gCount}
}

// subIDRange parses an /etc/sub{u,g}id line for the user with the given id
// (matched by name or numeric id): "name:start:count".
func subIDRange(path string, id int) (start, count int, ok bool) {
	name := ""
	if u, err := user.LookupId(strconv.Itoa(id)); err == nil {
		name = u.Username
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		p := strings.Split(strings.TrimSpace(line), ":")
		if len(p) != 3 || (p[0] != name && p[0] != strconv.Itoa(id)) {
			continue
		}
		s, err1 := strconv.Atoi(p[1])
		c, err2 := strconv.Atoi(p[2])
		if err1 == nil && err2 == nil && c > 0 {
			return s, c, true
		}
	}
	return 0, 0, false
}

// apply writes the child's uid/gid maps via the setuid/cap helpers (which vet
// the ranges against /etc/sub{u,g}id). Runs from the parent, against the paused
// init's pid.
func (m *idRanges) apply(pid int) error {
	ps := strconv.Itoa(pid)
	uLen := min(m.uidCount, 65535)
	gLen := min(m.gidCount, 65535)
	if err := runHelper("newuidmap", ps, "0", strconv.Itoa(m.hostUID), "1", "1", strconv.Itoa(m.uidStart), strconv.Itoa(uLen)); err != nil {
		return err
	}
	return runHelper("newgidmap", ps, "0", strconv.Itoa(m.hostGID), "1", "1", strconv.Itoa(m.gidStart), strconv.Itoa(gLen))
}

func runHelper(name string, args ...string) error {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// fuseOverlayfsPath finds a fuse-overlayfs binary to mount the sandbox root
// with: $BUXON_FUSE_OVERLAYFS, a copy bundled next to the buxond executable
// (single-artifact distribution), then $PATH. "" ⇒ fall back to kernel overlay.
func fuseOverlayfsPath() string {
	if p := os.Getenv("BUXON_FUSE_OVERLAYFS"); p != "" {
		if isExecutable(p) {
			return p
		}
		return ""
	}
	if exe, err := os.Executable(); err == nil {
		if cand := filepath.Join(filepath.Dir(exe), "fuse-overlayfs"); isExecutable(cand) {
			return cand
		}
	}
	if p, err := exec.LookPath("fuse-overlayfs"); err == nil {
		return p
	}
	return ""
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

func socketpair() (parent, child *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "ctrl-parent"), os.NewFile(uintptr(fds[1]), "ctrl-child"), nil
}

// recvFD receives a single fd sent with SCM_RIGHTS over a unix socket.
func recvFD(f *os.File) (int, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return -1, err
	}
	var fd int
	var opErr error
	err = rc.Read(func(sfd uintptr) bool {
		buf := make([]byte, 16)
		oob := make([]byte, unix.CmsgSpace(4))
		_, oobn, _, _, e := unix.Recvmsg(int(sfd), buf, oob, 0)
		if e == unix.EAGAIN {
			return false // wait for readability
		}
		if e != nil {
			opErr = e
			return true
		}
		scms, e := unix.ParseSocketControlMessage(oob[:oobn])
		if e != nil || len(scms) == 0 {
			opErr = fmt.Errorf("no control message")
			return true
		}
		fds, e := unix.ParseUnixRights(&scms[0])
		if e != nil || len(fds) == 0 {
			opErr = fmt.Errorf("no fd in control message")
			return true
		}
		fd = fds[0]
		return true
	})
	if err != nil {
		return -1, err
	}
	return fd, opErr
}

func must(err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
