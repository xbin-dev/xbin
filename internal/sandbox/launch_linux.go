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
	"time"

	"golang.org/x/sys/unix"
)

// InitArg is the hidden subcommand xbind (and the test binary) must dispatch
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

// RecvTUN waits until the init sends the TUN fd (via SCM_RIGHTS) and returns
// it. Bounded: an init that dies before creating the TUN gives no EOF (the
// ctrl socket is SOCK_DGRAM — no teardown signal), so an unbounded read here
// would hang the whole session-open forever, with nothing ever logged. A
// healthy init sends the fd well under a second; the generous bound only
// bites on an already-dead sandbox.
func (h *Handle) RecvTUN() (int, error) {
	_ = h.ctrl.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer func() { _ = h.ctrl.SetReadDeadline(time.Time{}) }()
	return recvFD(h.ctrl)
}

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
	s.Debug = s.Debug || os.Getenv("XBIN_SANDBOX_DEBUG") != ""

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

	if s.Net == "relay" || s.Net == "splice" {
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
// container 0 → the xbind user (root-in-container stays this user, as before),
// container 1..N → the sub-uid/gid range from /etc/subuid,/etc/subgid. This maps
// a full uid space into the sandbox so apt/dpkg's user switches and non-root
// in-container users work.
type idRanges struct {
	hostUID, hostGID   int
	uidStart, uidCount int
	gidStart, gidCount int
}

// IDMapStatus reports whether full sub-uid/gid RANGE mapping is available for
// this host uid/gid — the mode that lets apt/dpkg chown files to the system
// users their post-install scripts create (systemd, dbus, messagebus, …).
// When false the sandbox falls back to SINGLE-UID mode: only container-root is
// mapped, so those chowns fail with EINVAL ("Invalid argument") and such
// package installs break midway, while simple packages still install. reason
// names what's missing (for a startup warning). Mirrors detectIDRanges' checks.
func IDMapStatus(hostUID, hostGID int) (rangeOK bool, reason string) {
	if _, err := exec.LookPath("newuidmap"); err != nil {
		return false, "newuidmap not on PATH (install the 'uidmap' package)"
	}
	if _, err := exec.LookPath("newgidmap"); err != nil {
		return false, "newgidmap not on PATH (install the 'uidmap' package)"
	}
	owner := ownerName(hostUID)
	if _, _, ok := subIDRange("/etc/subuid", owner, hostUID); !ok {
		return false, fmt.Sprintf("no /etc/subuid range delegated for user %q (uid %d)", owner, hostUID)
	}
	// /etc/subgid is keyed by the USER, not the gid — match by owner/uid.
	if _, _, ok := subIDRange("/etc/subgid", owner, hostUID); !ok {
		return false, fmt.Sprintf("no /etc/subgid range delegated for user %q (uid %d)", owner, hostUID)
	}
	return true, ""
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
	owner := ownerName(hostUID)
	uStart, uCount, ok := subIDRange("/etc/subuid", owner, hostUID)
	if !ok {
		return nil
	}
	gStart, gCount, ok := subIDRange("/etc/subgid", owner, hostUID)
	if !ok {
		return nil
	}
	return &idRanges{hostUID, hostGID, uStart, uCount, gStart, gCount}
}

// ownerName resolves a uid to its login name (for matching /etc/sub{u,g}id
// entries, which are keyed by the user). "" if the uid has no passwd entry.
func ownerName(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return ""
}

// subIDRange parses the /etc/sub{u,g}id range delegated to a user. BOTH files
// are keyed by the USER (login name or UID) — /etc/subgid is NOT keyed by gid —
// so the caller passes the user's name + uid for both, even when reading the
// gid range from subgid. (Matching subgid by gid was a bug: it silently
// dropped to single-uid mode whenever a user's uid != gid, e.g. a `useradd
// --system` account — breaking apt/dpkg system-user installs.)
func subIDRange(path, owner string, uid int) (start, count int, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	return parseSubID(data, owner, uid)
}

// parseSubID finds the "owner:start:count" line for owner (login name) or uid.
func parseSubID(data []byte, owner string, uid int) (start, count int, ok bool) {
	uidStr := strconv.Itoa(uid)
	for _, line := range strings.Split(string(data), "\n") {
		p := strings.Split(strings.TrimSpace(line), ":")
		if len(p) != 3 || (p[0] != owner && p[0] != uidStr) {
			continue
		}
		if owner == "" && p[0] != uidStr {
			continue // don't let an empty owner match a named line
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
// with: $XBIN_FUSE_OVERLAYFS, a copy bundled next to the xbind executable
// (single-artifact distribution), then $PATH. "" ⇒ fall back to kernel overlay.
func fuseOverlayfsPath() string {
	if p := os.Getenv("XBIN_FUSE_OVERLAYFS"); p != "" {
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
	// The parent end must be nonblocking so os.File registers it with the
	// runtime poller — that's what makes RecvTUN's read deadline (above)
	// actually work. The child end stays blocking (the init writes once).
	_ = unix.SetNonblock(fds[0], true)
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
