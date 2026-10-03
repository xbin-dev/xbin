//go:build linux

package guest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The rootfs's special modes (D182). The base rootfs reaches every host as an
// unpacked directory, and an unprivileged unpack (hack/build-rootfs.sh,
// deploy/install.sh) drops what the image set beyond plain permissions: the
// setuid, setgid and sticky bits, and the owners (--all-root makes the VM
// image's files root's anyway). The host tree stays that way on purpose: no
// setuid-root program sits in xbind's install directory, and a namespace
// sandbox runs with no new privileges, where the bits mean nothing. A VM
// guest is a machine of its own, root included (D89), so it gets them back.
// The image lists them (docker/rootfs.Dockerfile writes modesManifest, its
// last step) and the guest applies the list at each boot, before anything
// runs:
//
//   - a regular file is copied, with its listed mode and owner, into a small
//     tmpfs layer stacked over the image (lowerdir=/fixup:/lower). The
//     sandbox's upper is never written: a rebase onto a newer base finds the
//     newer program, and one the sandbox replaced itself (apt) stays its own.
//   - a directory is changed in the assembled root (a metadata copy-up),
//     only while it still shows the image's unpacked mode and owner: a mode
//     the sandbox gave it is its own.
//
// The list is read from the image, never the sandbox's upper. A missing one
// changes nothing (an image from before it). It is bounded (modesMaxBytes,
// modesMaxEntries, fixupMaxBytes of file data); an entry must carry a special
// bit, name a clean absolute path, and match the type the image has there;
// a symbolic link anywhere on the way is skipped, never followed.

const (
	modesManifest   = "etc/xbin-rootfs-modes" // in the image, from its root
	modesMaxBytes   = 1 << 20
	modesMaxEntries = 4096
	fixupMaxBytes   = 32 << 20 // file data copied into the fixup layer
	fixupDir        = "/fixup"
)

// modeEntry is one line of the list: "<mode> <uid> <gid> <f|d> <path>", the
// mode octal (find -printf '%m %U %G %y %p').
type modeEntry struct {
	mode     uint32 // permission and special bits
	uid, gid int
	dir      bool
	path     string // clean and absolute
}

// parseModes reads a list. Blank lines and '#' comments are skipped, and so
// is (counted in bad) a line that doesn't parse, carries no special bit, or
// repeats a path. A list over modesMaxBytes or modesMaxEntries is refused
// whole: it isn't the list an image build writes.
func parseModes(r io.Reader) (es []modeEntry, bad int, err error) {
	sc := bufio.NewScanner(io.LimitReader(r, modesMaxBytes+1))
	sc.Buffer(make([]byte, 0, 4096), 16<<10)
	seen := map[string]bool{}
	n := 0
	for sc.Scan() {
		n += len(sc.Bytes()) + 1
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, ok := parseModeLine(line)
		if !ok || seen[e.path] {
			bad++
			continue
		}
		seen[e.path] = true
		if es = append(es, e); len(es) > modesMaxEntries {
			return nil, bad, fmt.Errorf("over %d entries", modesMaxEntries)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, bad, err
	}
	if n > modesMaxBytes {
		return nil, bad, fmt.Errorf("over %d bytes", modesMaxBytes)
	}
	return es, bad, nil
}

func parseModeLine(line string) (modeEntry, bool) {
	f := strings.SplitN(line, " ", 5)
	if len(f) != 5 {
		return modeEntry{}, false
	}
	mode, err1 := strconv.ParseUint(f[0], 8, 32)
	uid, err2 := strconv.ParseUint(f[1], 10, 32)
	gid, err3 := strconv.ParseUint(f[2], 10, 32)
	p := f[4]
	switch {
	case err1 != nil || err2 != nil || err3 != nil, mode > 0o7777, mode&0o7000 == 0,
		f[3] != "f" && f[3] != "d",
		!strings.HasPrefix(p, "/"), p == "/", path.Clean(p) != p, strings.ContainsRune(p, 0):
		return modeEntry{}, false
	}
	return modeEntry{mode: uint32(mode), uid: int(uid), gid: int(gid), dir: f[3] == "d", path: p}, true
}

// readModes reads the image's list under the directory lower (nil: none).
func readModes(lower string) []modeEntry {
	root, err := unix.Open(lower, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		logf("rootfs modes: %v", err)
		return nil
	}
	defer unix.Close(root)
	fd, err := openBeneath(root, modesManifest, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY)
	if errors.Is(err, unix.ENOENT) {
		return nil // an image from before the list
	}
	if err != nil {
		logf("rootfs modes: %s: %v", modesManifest, err)
		return nil
	}
	_ = unix.SetNonblock(fd, false)
	f := os.NewFile(uintptr(fd), modesManifest)
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		logf("rootfs modes: %s is not a regular file", modesManifest)
		return nil
	}
	es, bad, err := parseModes(f)
	if err != nil {
		logf("rootfs modes: %s: %v — none applied", modesManifest, err)
		return nil
	}
	if bad > 0 {
		logf("rootfs modes: %s: %d line(s) skipped", modesManifest, bad)
	}
	return es
}

// openBeneath opens rel (relative, clean) beneath the directory dirfd: no
// symbolic link on the way (a trailing one, with O_PATH, is opened as
// itself), no magic link, no other mount.
func openBeneath(dirfd int, rel string, flags int) (int, error) {
	return unix.Openat2(dirfd, rel, &unix.OpenHow{
		Flags:   uint64(flags) | unix.O_NOFOLLOW | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
}

// attrs are what an entry sets: the mode's permission and special bits and
// the owner.
type attrs struct {
	mode     uint32
	uid, gid int
}

func attrsOf(st *unix.Stat_t) attrs {
	return attrs{mode: st.Mode & 0o7777, uid: int(st.Uid), gid: int(st.Gid)}
}

func (e modeEntry) want() attrs { return attrs{mode: e.mode, uid: e.uid, gid: e.gid} }

// modeFix is an entry the image doesn't carry as listed: was is what the
// image has there.
type modeFix struct {
	modeEntry
	was  attrs
	size int64
}

// planModes checks each entry against the image under lowerfd: what the
// image already carries as listed needs nothing; a path that isn't there, is
// a symbolic link (or behind one), or is of the other type is skipped.
func planModes(lowerfd int, es []modeEntry) (files, dirs []modeFix, skipped int) {
	for _, e := range es {
		fd, err := openBeneath(lowerfd, e.path[1:], unix.O_PATH)
		if err != nil {
			skipped++
			continue
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		unix.Close(fd)
		t := st.Mode & unix.S_IFMT
		switch {
		case err != nil, e.dir && t != unix.S_IFDIR, !e.dir && t != unix.S_IFREG:
			skipped++
		case attrsOf(&st) == e.want():
		case e.dir:
			dirs = append(dirs, modeFix{modeEntry: e, was: attrsOf(&st)})
		default:
			files = append(files, modeFix{modeEntry: e, was: attrsOf(&st), size: st.Size})
		}
	}
	return files, dirs, skipped
}

// stageFiles copies each of files from the image under lower into the empty
// directory fix (a tmpfs), with its listed mode and owner, and its parents
// as the image has them (mode, owner, times — the layer's directories are
// what the merged root shows where the sandbox hasn't changed them). It
// stops taking files past budget bytes of data. A file that fails is
// removed: a partial copy must never cover the image's.
func stageFiles(lower, fix string, files []modeFix, budget int64) (staged int, bytes int64, skipped int) {
	lfd, err := unix.Open(lower, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		logf("rootfs modes: %v", err)
		return 0, 0, len(files)
	}
	defer unix.Close(lfd)
	ffd, err := unix.Open(fix, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		logf("rootfs modes: %v", err)
		return 0, 0, len(files)
	}
	defer unix.Close(ffd)
	made := map[string]unix.Stat_t{} // the layer's directories, and the image's for each
	for _, f := range files {
		if bytes+f.size > budget {
			skipped++
			continue
		}
		if err := stageFile(lfd, ffd, f, made); err != nil {
			logf("rootfs modes: %s: %v", f.path, err)
			skipped++
			continue
		}
		staged++
		bytes += f.size
	}
	for rel, st := range made { // after the files: each one made changed its directory's times
		ts := []unix.Timespec{st.Atim, st.Mtim}
		_ = unix.UtimesNanoAt(ffd, rel, ts, unix.AT_SYMLINK_NOFOLLOW)
	}
	return staged, bytes, skipped
}

func stageFile(lfd, ffd int, f modeFix, made map[string]unix.Stat_t) error {
	rel := f.path[1:]
	src, err := openBeneath(lfd, rel, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY)
	if err != nil {
		return err
	}
	_ = unix.SetNonblock(src, false)
	in := os.NewFile(uintptr(src), f.path)
	defer in.Close()
	var st unix.Stat_t
	if err := unix.Fstat(src, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size != f.size {
		return errors.New("the image's file changed")
	}
	dir, name := path.Split(rel)
	pfd := ffd
	if dir = strings.TrimSuffix(dir, "/"); dir != "" {
		if pfd, err = mirrorDirs(lfd, ffd, dir, made); err != nil {
			return err
		}
		defer unix.Close(pfd)
	}
	dst, err := unix.Openat(pfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	out := os.NewFile(uintptr(dst), f.path)
	err = func() error {
		n, err := io.Copy(out, io.LimitReader(in, f.size+1))
		switch {
		case err != nil:
			return err
		case n != f.size:
			return errors.New("the image's file changed size")
		}
		// the owner first: a chown clears the setuid and setgid bits
		if err := unix.Fchown(dst, f.uid, f.gid); err != nil {
			return err
		}
		if err := unix.Fchmod(dst, f.mode); err != nil {
			return err
		}
		return unix.UtimesNanoAt(pfd, name, []unix.Timespec{st.Atim, st.Mtim}, unix.AT_SYMLINK_NOFOLLOW)
	}()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = unix.Unlinkat(pfd, name, 0)
	}
	return err
}

// mirrorDirs makes dir (relative) in the layer ffd, each step with the
// image's mode and owner (times: stageFiles, at the end), and opens it.
func mirrorDirs(lfd, ffd int, dir string, made map[string]unix.Stat_t) (int, error) {
	cur := ""
	for _, seg := range strings.Split(dir, "/") {
		cur = path.Join(cur, seg)
		if _, ok := made[cur]; ok {
			continue
		}
		fd, err := openBeneath(lfd, cur, unix.O_PATH|unix.O_DIRECTORY)
		if err != nil {
			return -1, err
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		if err := unix.Mkdirat(ffd, cur, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
		if err := unix.Fchownat(ffd, cur, int(st.Uid), int(st.Gid), unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return -1, err
		}
		if err := unix.Fchmodat(ffd, cur, st.Mode&0o7777, 0); err != nil {
			return -1, err
		}
		made[cur] = st
	}
	return openBeneath(ffd, dir, unix.O_PATH|unix.O_DIRECTORY)
}

// fixDirs gives the directories under root (the assembled root) their listed
// mode and owner — each only while it shows what the image had (a copy-up of
// its metadata alone); one the sandbox changed is left as it is, and so is a
// mount point (/tmp's tmpfs, say: another mount).
func fixDirs(root string, dirs []modeFix) (fixed, skipped int) {
	rfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		logf("rootfs modes: %v", err)
		return 0, len(dirs)
	}
	defer unix.Close(rfd)
	for _, d := range dirs {
		fd, err := openBeneath(rfd, d.path[1:], unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			skipped++
			continue
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		switch {
		case err != nil:
			skipped++
		case attrsOf(&st) == d.want():
		case attrsOf(&st) != d.was:
			skipped++ // the sandbox's own
		default:
			// the owner first: a chown may clear the special bits
			if err = unix.Fchown(fd, d.uid, d.gid); err == nil {
				err = unix.Fchmod(fd, d.mode)
			}
			if err != nil {
				logf("rootfs modes: %s: %v", d.path, err)
				skipped++
			} else {
				fixed++
			}
		}
		unix.Close(fd)
	}
	return fixed, skipped
}

// imageModes is assembleRoot's first part: the image's list read and its
// files staged in a tmpfs mounted at fix when any needs it. It answers the
// root overlay's lowerdir and the directories to fix once the root is
// assembled (fixDirs). Every failure is logged and leaves the image as it is.
func imageModes(lower, fix string) (lowerdir string, dirs []modeFix) {
	lowerdir = lower
	es := readModes(lower)
	if len(es) == 0 {
		return lowerdir, nil
	}
	lfd, err := unix.Open(lower, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		logf("rootfs modes: %v", err)
		return lowerdir, nil
	}
	files, dirs, skipped := planModes(lfd, es)
	unix.Close(lfd)
	staged, bytes := 0, int64(0)
	if len(files) > 0 {
		if err := mountAt("tmpfs", fix, "tmpfs", 0, fmt.Sprintf("mode=0755,size=%d", fixupMaxBytes+(8<<20))); err != nil {
			logf("rootfs modes: %v", err)
			return lowerdir, dirs
		}
		var s int
		staged, bytes, s = stageFiles(lower, fix, files, fixupMaxBytes)
		skipped += s
		if staged > 0 {
			lowerdir = fix + ":" + lower
		} else {
			_ = unix.Unmount(fix, unix.MNT_DETACH)
		}
	}
	logf("rootfs modes: %d files staged (%d KiB), %d directories to check, %d entries skipped", staged, bytes>>10, len(dirs), skipped)
	return lowerdir, dirs
}
