//go:build linux

package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// A hold (D245, plans/wsagent/home-on-untrusted-tiles.md §4): ONE overlay
// mount kept in a namespace of its own, handed to sandboxes as detached
// clones.
//
// Several sandboxes sometimes need the same writable overlay — a person's
// sessions on one tile share the copy-on-write layer of their home — and
// two overlay mounts over one upper directory are unsafe: two
// fuse-overlayfs each keep their own picture of it, and the kernel calls
// its own "undefined". Nor may xbind mount anything where the host sees
// it. So the overlay is mounted once, by a process that is started like
// any sandbox (Launch: its own namespaces, the uid mapping every sandbox
// of this xbind gets) and does nothing else: its init mounts the overlay
// on a private tmpfs, pivots into that tmpfs, and answers each request on
// its socket with open_tree(OPEN_TREE_CLONE) of the mount — a detached
// mount of the same superblock, which crosses to xbind over SCM_RIGHTS and
// from there into a sandbox's init (Bind.Mount), which attaches it with
// move_mount. The overlay exists in the hold's mount namespace and in the
// sandboxes' and nowhere else; it dies with them.
//
// Sibling user namespaces: a kernel overlay is used like any filesystem.
// A FUSE mount is served to the mounter's kuid and kgid alone unless it is
// mounted allow_other — and allow_other, which fuse-overlayfs asks for
// when it mounts by itself, is confined to the mounting user namespace and
// its children. So the init mounts the FUSE filesystem itself, without
// allow_other, and gives fuse-overlayfs the open /dev/fuse ("/dev/fd/N");
// root of every sandbox of this xbind is that kuid (0 ↦ xbind's uid).

// holdWait bounds a hold's start and each request to it.
const holdWait = 30 * time.Second

// holdMsgMax bounds one message on a hold's socket.
const holdMsgMax = 4 << 10

// Hold is a running hold: xbind's side.
type Hold struct {
	spec HoldSpec
	cmd  *exec.Cmd
	h    *Handle
	sock *os.File
	out  *holdOutput

	mu   sync.Mutex // one request at a time
	done chan struct{}
	end  sync.Once
}

// holdOutput keeps the tail of what the hold's init wrote to stderr.
type holdOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *holdOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.b.Len() < holdMsgMax {
		o.b.Write(p[:min(len(p), holdMsgMax-o.b.Len())])
	}
	return len(p), nil
}

func (o *holdOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return strings.TrimSpace(o.b.String())
}

// StartHold mounts hs in a hold of its own and returns once the overlay is
// up — or the reason it is not: no user namespaces, the mount refused,
// fuse-overlayfs missing or dead. hostUID and hostGID are what root in the
// hold maps to (os.Getuid, os.Getgid), as for every sandbox.
func StartHold(hs HoldSpec, hostUID, hostGID int) (*Hold, error) {
	if !Available() {
		return nil, errors.New("user namespaces are unavailable")
	}
	spec := &Spec{Hold: &hs, HostUID: hostUID, HostGID: hostGID}
	cmd, h, err := Launch(spec)
	if err != nil {
		return nil, err
	}
	hd := &Hold{spec: hs, cmd: cmd, h: h, sock: h.hold, out: &holdOutput{}, done: make(chan struct{})}
	h.hold = nil // the Hold's from here on
	cmd.Stderr = hd.out
	if err := cmd.Start(); err != nil {
		h.Cleanup()
		hd.sock.Close()
		return nil, err
	}
	// The waiter only waits: the launch's Handle is this goroutine's alone
	// (a hold that dies at once — fuse-overlayfs missing — would otherwise
	// have its Cleanup race Started below).
	go func() {
		_ = cmd.Wait()
		close(hd.done)
	}()
	fail := func(err error) (*Hold, error) {
		hd.Close() // ended and reaped: nothing reads the spec any more
		h.Cleanup()
		if out := hd.out.String(); out != "" {
			err = fmt.Errorf("%w (%s)", err, out)
		}
		return nil, err
	}
	if err := h.SetupUserns(); err != nil {
		return fail(fmt.Errorf("userns: %w", err))
	}
	h.Started() // the socket's other end is the hold's alone: its death is our EOF
	msg, fd, err := hd.recv()
	if fd >= 0 {
		unix.Close(fd)
	}
	if err == nil && msg == "ok" {
		// it answered, so its init is long past reading the spec (in range
		// mode the second stage reads it again after the maps are written)
		h.Cleanup()
	}
	switch {
	case err != nil:
		return fail(fmt.Errorf("the overlay's hold did not start: %w", err))
	case msg != "ok":
		return fail(errors.New(strings.TrimPrefix(msg, "err ")))
	}
	return hd, nil
}

// Flavour is the overlay flavour the hold mounted (HoldFuse | HoldKernel).
func (hd *Hold) Flavour() string { return hd.spec.Flavour() }

// Done is closed once the hold's process is gone — ended by Close, or dead
// on its own (fuse-overlayfs exited): its clones then answer ENOTCONN, or
// are a kernel overlay nobody holds any more.
func (hd *Hold) Done() <-chan struct{} { return hd.done }

// Mount is one detached clone of the overlay, for one sandbox
// (Bind.Mount). The caller closes it once that sandbox has started.
func (hd *Hold) Mount() (*os.File, error) {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	select {
	case <-hd.done:
		return nil, errors.New("the overlay's hold is gone")
	default:
	}
	_ = hd.sock.SetDeadline(time.Now().Add(holdWait))
	defer func() { _ = hd.sock.SetDeadline(time.Time{}) }()
	if _, err := hd.sock.Write([]byte{'m'}); err != nil {
		return nil, fmt.Errorf("the overlay's hold: %w", err)
	}
	msg, fd, err := hd.recv()
	switch {
	case err != nil:
		return nil, fmt.Errorf("the overlay's hold: %w", err)
	case msg != "ok" || fd < 0:
		if fd >= 0 {
			unix.Close(fd)
		}
		return nil, errors.New("the overlay could not be cloned: " + strings.TrimPrefix(msg, "err "))
	}
	return os.NewFile(uintptr(fd), "overlay-clone"), nil
}

// recv reads one message, and the fd it carries (-1: none).
func (hd *Hold) recv() (string, int, error) {
	_ = hd.sock.SetReadDeadline(time.Now().Add(holdWait))
	rc, err := hd.sock.SyscallConn()
	if err != nil {
		return "", -1, err
	}
	var (
		msg   string
		fd    = -1
		opErr error
	)
	err = rc.Read(func(sfd uintptr) bool {
		buf := make([]byte, holdMsgMax)
		oob := make([]byte, unix.CmsgSpace(4))
		n, oobn, _, _, e := unix.Recvmsg(int(sfd), buf, oob, unix.MSG_CMSG_CLOEXEC)
		switch {
		case e == unix.EAGAIN:
			return false
		case e != nil:
			opErr = e
			return true
		case n == 0:
			opErr = errors.New("it ended")
			return true
		}
		msg = string(buf[:n])
		if scms, e := unix.ParseSocketControlMessage(oob[:oobn]); e == nil && len(scms) > 0 {
			if fds, e := unix.ParseUnixRights(&scms[0]); e == nil && len(fds) > 0 {
				fd = fds[0]
				for _, extra := range fds[1:] {
					unix.Close(extra)
				}
			}
		}
		return true
	})
	if err == nil {
		err = opErr
	}
	return msg, fd, err
}

// Close ends the hold — pid 1 of its namespaces, so fuse-overlayfs goes
// with it — and waits until it is reaped. The clones sandboxes still hold
// stay mounted there and stop answering (FUSE), so the caller ends those
// sandboxes first. Safe to call again.
func (hd *Hold) Close() {
	hd.end.Do(func() {
		if hd.cmd.Process != nil {
			_ = hd.cmd.Process.Kill()
		}
		select {
		case <-hd.done:
		case <-time.After(holdWait):
		}
		hd.sock.Close()
	})
}

// holdSocketpair is a hold's socket: SOCK_SEQPACKET, so each message is
// whole and either end's death is the other's EOF.
func holdSocketpair() (parent, child *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	_ = unix.SetNonblock(fds[0], true) // deadlines on xbind's end
	return os.NewFile(uintptr(fds[0]), "hold-parent"), os.NewFile(uintptr(fds[1]), "hold-child"), nil
}

// ---- the init's side ---------------------------------------------------------

// runHold is the init of a hold (Spec.Hold): mount the overlay, leave the
// host's tree, serve clones until xbind's end of the socket closes.
func runHold(s *Spec) error {
	say := func(msg string) { _ = unix.Sendmsg(s.HoldFD, []byte(msg), nil, nil, 0) }
	mfd, fuse, err := holdMount(s)
	if err != nil {
		say("err " + err.Error())
		return err
	}
	_ = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
	if fuse != nil {
		// fuse-overlayfs gone is the overlay gone: the hold ends with it, and
		// xbind ends the sandboxes that hold a clone
		go func() {
			_ = fuse.Wait()
			os.Exit(3)
		}()
	}
	say("ok")
	dbg(s.Debug, "hold: serving clones of the %s overlay", s.Hold.Flavour())
	buf := make([]byte, 16)
	for {
		n, _, _, _, err := unix.Recvmsg(s.HoldFD, buf, nil, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n == 0 {
			os.Exit(0) // xbind closed its end, or is gone: nothing to hold for
		}
		tfd, err := unix.OpenTree(mfd, "", unix.AT_EMPTY_PATH|unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
		if err != nil {
			say("err open_tree: " + err.Error())
			continue
		}
		if err := unix.Sendmsg(s.HoldFD, []byte("ok"), unix.UnixRights(tfd), nil, 0); err != nil {
			unix.Close(tfd)
			os.Exit(0)
		}
		unix.Close(tfd)
	}
}

// holdMount builds the hold's world and returns an O_PATH fd on the
// overlay's mount, and fuse-overlayfs when it serves it:
//
//	<tmp>/bxhold.<pid>/        a private tmpfs
//	  lower/                   the lower layer, bound READ-ONLY
//	  layer/                   the layer's directory (upper/, work/)
//	  root/                    a tmpfs: the hold's root after the pivot
//	    proc/                  its pid namespace's /proc
//	    m/                     the overlay
//	    old/                   where the host's tree is detached from
//
// The overlay's options name lower/ and layer/ — paths of ours — never the
// host's, which may hold a comma or a colon.
func holdMount(s *Spec) (int, *exec.Cmd, error) {
	h := s.Hold
	// Detach mount propagation so nothing we do leaks to the host.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return -1, nil, must(err, "make-rprivate /")
	}
	base := filepath.Join(os.TempDir(), fmt.Sprintf("bxhold.%d", os.Getpid()))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return -1, nil, must(err, "mkdir base")
	}
	if err := unix.Mount("tmpfs", base, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755,size=1m"); err != nil {
		return -1, nil, must(err, "mount base tmpfs")
	}
	lower, layer, root := filepath.Join(base, "lower"), filepath.Join(base, "layer"), filepath.Join(base, "root")
	for _, d := range []string{lower, layer, root} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return -1, nil, must(err, "mkdir "+d)
		}
	}
	// the lower layer read-only here, whatever the overlay does with it
	if err := unix.Mount(h.Lower, lower, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return -1, nil, must(err, "bind the lower layer")
	}
	if err := remountRO(lower); err != nil {
		return -1, nil, must(err, "remount the lower layer read-only")
	}
	if filepath.Dir(h.Upper) != filepath.Dir(h.Work) {
		return -1, nil, errors.New("the overlay's upper and work directories are not in one directory")
	}
	if err := unix.Mount(filepath.Dir(h.Upper), layer, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return -1, nil, must(err, "bind the layer")
	}
	const tflags = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC
	if err := unix.Mount("tmpfs", root, "tmpfs", tflags, "mode=0555,size=64k"); err != nil {
		return -1, nil, must(err, "mount the hold's root")
	}
	mp := filepath.Join(root, "m")
	for _, d := range []string{mp, filepath.Join(root, "proc"), filepath.Join(root, "old")} {
		if err := os.Mkdir(d, 0o555); err != nil {
			return -1, nil, must(err, "mkdir "+d)
		}
	}
	if err := unix.Mount("proc", filepath.Join(root, "proc"), "proc", tflags, ""); err != nil {
		return -1, nil, must(err, "mount the hold's /proc")
	}
	opt := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower,
		filepath.Join(layer, filepath.Base(h.Upper)), filepath.Join(layer, filepath.Base(h.Work)))
	dbg(s.Debug, "hold: overlay %s (fuse=%q)", opt, h.Fuse)
	var fuse *exec.Cmd
	if h.Fuse != "" {
		var err error
		if fuse, err = holdFuse(s, opt, mp); err != nil {
			return -1, nil, err
		}
	} else if err := holdKernel(mp, opt); err != nil {
		return -1, nil, err
	}
	mfd, err := unix.Open(mp, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, nil, must(err, "open the overlay")
	}
	// leave the host's tree: the hold — and fuse-overlayfs, rooted where we
	// are — keep a root that holds /proc and the overlay and nothing else
	if err := unix.Mount("", root, "", unix.MS_REMOUNT|unix.MS_RDONLY|tflags, ""); err != nil {
		return -1, nil, must(err, "remount the hold's root read-only")
	}
	if err := unix.PivotRoot(root, filepath.Join(root, "old")); err != nil {
		return -1, nil, must(err, "pivot_root")
	}
	if err := unix.Chdir("/"); err != nil {
		return -1, nil, must(err, "chdir /")
	}
	if err := unix.Unmount("/old", unix.MNT_DETACH); err != nil {
		return -1, nil, must(err, "detach the host's tree")
	}
	return mfd, fuse, nil
}

// holdKernel mounts the kernel's overlayfs at mp: user xattrs (the only
// ones a user namespace may write: opaque directories need them), waiting
// out a predecessor that still holds the upper (mountOverlay's reason).
func holdKernel(mp, opt string) error {
	const flags = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOATIME
	err := unix.Mount("overlay", mp, "overlay", flags, opt+",userxattr")
	for i := 0; errors.Is(err, unix.EBUSY) && i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		err = unix.Mount("overlay", mp, "overlay", flags, opt+",userxattr")
	}
	return must(err, "mount the overlay (kernel overlayfs with user xattrs needs Linux 5.11)")
}

// holdFuse mounts a FUSE filesystem at mp without allow_other and starts
// fuse-overlayfs on its /dev/fuse, in the foreground: our child, so its
// exit is ours to see. It returns once the mount answers.
func holdFuse(s *Spec, opt, mp string) (*exec.Cmd, error) {
	dev, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if err != nil {
		return nil, must(err, "open /dev/fuse")
	}
	defer dev.Close() // fuse-overlayfs holds its own: its death then aborts the connection
	data := fmt.Sprintf("fd=%d,rootmode=40000,user_id=0,group_id=0,default_permissions", dev.Fd())
	if err := unix.Mount("fuse-overlayfs", mp, "fuse.fuse-overlayfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOATIME, data); err != nil {
		return nil, must(err, "mount fuse")
	}
	mfd, err := unix.MemfdCreate("fuse-overlayfs", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, must(err, "fuse-overlayfs output")
	}
	out := os.NewFile(uintptr(mfd), "fuse-overlayfs-output")
	defer out.Close()
	fo := exec.Command(s.Hold.Fuse, "-f", "-o", opt, "/dev/fd/3")
	fo.ExtraFiles = []*os.File{dev}
	fo.Stdout, fo.Stderr = out, out
	fo.Dir = "/" // the pivot moves it into the hold's root with us
	fo.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := fo.Start(); err != nil {
		return nil, must(err, "start fuse-overlayfs")
	}
	dev.Close()
	pid := fo.Process.Pid
	// statfs is answered by fuse-overlayfs: it waits until it serves, and
	// fails once it is dead (the connection is aborted) or killed below
	late := make(chan struct{})
	watchdog := time.AfterFunc(fuseMountWait, func() { close(late); _ = fo.Process.Kill() })
	defer watchdog.Stop()
	var st unix.Statfs_t
	err = unix.Statfs(mp, &st)
	for errors.Is(err, unix.EINTR) {
		err = unix.Statfs(mp, &st)
	}
	if err == nil && st.Type == fuseMagic && !childExited(pid) {
		dbg(s.Debug, "hold: fuse-overlayfs pid %d serves the overlay (said %q)", pid, fuseOutput(out))
		return fo, nil
	}
	_ = fo.Process.Kill()
	_ = fo.Wait()
	select {
	case <-late:
		return nil, fmt.Errorf("fuse-overlayfs mount: no answer after %s: %s", fuseMountWait, fuseOutput(out))
	default:
	}
	return nil, fmt.Errorf("fuse-overlayfs mount: it exited before serving the overlay: %s", fuseOutput(out))
}
