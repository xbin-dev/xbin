// Package layers pins persistent sandbox layers to the base rootfs they were
// built on (plans/component-env.md; plans/tile-sandbox-runtime.md §9).
//
// A layer is a dir whose contents only make sense on top of one base: a
// terminal's overlay upper (.xbin/term/<key>/upper) or VM disk, and a tile
// sandbox's upper or disk (.xbin/sbx/<CK>/<name>.<uid>/cur/, and its
// snapshots). An upper records apt installs and the dpkg/apt state copied up
// from the base; stacking it on a DIFFERENT base merges new-base packages
// under an old dpkg status and apt breaks. So each layer dir carries stamps,
// written by xbind only:
//
//	base      the base version it was built on (a rootfs's etc/xbin-base-version)
//	overlay   namespace sandboxes: the overlay flavour that wrote its upper
//	          ("fuse" | "kernel"); absent for terminals and VM disks
//
// "Upgrading" a layer to a newer base means discarding it (reset). The
// install upgrade preserves old bases as `<rootfs>-<version>` siblings so
// pinned layers keep resolving, and GC releases the siblings nothing pins.
//
// Nothing here reads what a sandbox wrote: the stamps and the dirs holding
// them are xbind's, and a stamp is opened without following a symlink.
package layers

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// VersionFile is where a base rootfs records its version.
const VersionFile = "etc/xbin-base-version"

// Legacy is the version of an unstamped (pre-versioning) base, and of a
// terminal layer that predates the stamps.
const Legacy = "v0"

// The stamp files in a layer dir.
const (
	BaseFile    = "base"
	OverlayFile = "overlay"
)

// The overlay flavours a namespace upper can be written by. An upper written
// by one must not be mounted by the other (whiteouts and metacopy differ).
const (
	OverlayFuse   = "fuse"
	OverlayKernel = "kernel"
)

// maxStamp bounds a stamp read: a version is a short hash.
const maxStamp = 256

var (
	// ErrBaseMissing: the layer's base isn't installed (neither the current
	// rootfs nor a preserved `<rootfs>-<version>` sibling).
	ErrBaseMissing = errors.New("base image not installed")
	// ErrOverlay: the layer's upper was written by another overlay flavour.
	ErrOverlay = errors.New("overlay flavour changed")
)

// BaseVersion reads a rootfs's stamped base version, defaulting to Legacy
// for an unstamped (pre-versioning) base or none — and for a version file
// that can't be read, which a caller about to discard anything on the
// answer must tell apart (ReadBaseVersion).
func BaseVersion(rootfs string) string {
	v, _ := ReadBaseVersion(rootfs)
	return v
}

// ReadBaseVersion is BaseVersion with the read's error: Legacy, nil for an
// unstamped base (no version file, or an empty one) or none; Legacy and the
// error when the file is there but can't be read (EIO, EACCES, EMFILE…).
func ReadBaseVersion(rootfs string) (string, error) {
	if rootfs == "" {
		return Legacy, nil
	}
	b, err := os.ReadFile(filepath.Join(rootfs, VersionFile))
	if errors.Is(err, os.ErrNotExist) {
		return Legacy, nil
	}
	if err != nil {
		return Legacy, err
	}
	if v := strings.TrimSpace(string(b)); v != "" {
		return v, nil
	}
	return Legacy, nil
}

// ResolveBase returns the rootfs dir serving base version: the current
// rootfs if it matches, else a preserved sibling `<rootfs>-<version>` (kept
// by the install upgrade). ok=false when that base isn't installed, or when
// version can't name a sibling (empty, or it holds a path separator).
func ResolveBase(rootfs, version string) (string, bool) {
	if version == BaseVersion(rootfs) {
		return rootfs, true
	}
	if !validVersion(version) || rootfs == "" {
		return "", false
	}
	sib := rootfs + "-" + version
	if fi, err := os.Stat(sib); err == nil && fi.IsDir() {
		return sib, true
	}
	return "", false
}

// validVersion: a version is appended to a path, so it must stay one name.
func validVersion(v string) bool {
	return v != "" && len(v) <= maxStamp && !strings.ContainsAny(v, "/\x00")
}

// Stamps is what a layer dir records. "" = not recorded.
type Stamps struct {
	Base    string `json:"base,omitempty"`
	Overlay string `json:"overlay,omitempty"`
}

// Read returns dir's stamps. A missing stamp reads as ""; a stamp that is
// not a regular file (a symlink, a FIFO) or can't be read is an error.
func Read(dir string) (Stamps, error) {
	var s Stamps
	var err error
	if s.Base, err = readStamp(filepath.Join(dir, BaseFile)); err != nil {
		return s, err
	}
	s.Overlay, err = readStamp(filepath.Join(dir, OverlayFile))
	return s, err
}

// ReadBase returns dir's base stamp alone: "" when there is none, an error
// when it is there but can't be read or isn't a regular file — never ""
// for a stamp that exists, whatever the overlay stamp beside it holds.
func ReadBase(dir string) (string, error) { return readStamp(filepath.Join(dir, BaseFile)) }

// readStamp reads one stamp without following a symlink, bounded.
func readStamp(p string) (string, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: not a regular file", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxStamp+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxStamp {
		return "", fmt.Errorf("%s: longer than %d bytes", p, maxStamp)
	}
	return strings.TrimSpace(string(b)), nil
}

// Stamp records s in dir (which must exist): each non-empty field replaces
// its stamp atomically (a temp file renamed over it, so a symlink in its
// place is replaced, never followed); an empty field leaves its stamp alone.
func Stamp(dir string, s Stamps) error {
	for _, st := range []struct{ name, val string }{{BaseFile, s.Base}, {OverlayFile, s.Overlay}} {
		if st.val == "" {
			continue
		}
		if strings.ContainsAny(st.val, "\n\x00") || len(st.val) > maxStamp {
			return fmt.Errorf("stamp %s: bad value %q", st.name, st.val)
		}
		if err := writeStamp(filepath.Join(dir, st.name), st.val); err != nil {
			return err
		}
	}
	return nil
}

func writeStamp(p, val string) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.WriteString(val + "\n")
	if err := f.Chmod(0o644); werr == nil {
		werr = err
	}
	if err := f.Close(); werr == nil {
		werr = err
	}
	if werr == nil {
		werr = os.Rename(tmp, p)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// Outdated reports whether dir's stamped base differs from rootfs's (so the
// owner can be offered a reset to upgrade). An unstamped layer is not.
func Outdated(dir, rootfs string) bool {
	if rootfs == "" {
		return false
	}
	s, err := Read(dir)
	return err == nil && s.Base != "" && s.Base != BaseVersion(rootfs)
}

// Pin is a tile sandbox's start-time pin (plans/tile-sandbox-runtime.md §7
// step 3): it stamps an unpinned layer with the current base (and overlay,
// when given), and returns the rootfs dir serving the layer's base. A base
// that isn't installed is ErrBaseMissing; an upper written by another
// overlay flavour is ErrOverlay. Neither writes anything. dir must exist:
// a tile sandbox's is its cur/ (CurDir), where Stamp writes its stamps too.
// A restart keeps the pin; only a reset or rebase re-stamps (Stamp).
func Pin(dir, rootfs, overlay string) (Stamps, string, error) {
	if rootfs == "" {
		return Stamps{}, "", errors.New("pin a layer: no base rootfs")
	}
	s, err := Read(dir)
	if err != nil {
		return s, "", fmt.Errorf("pin a layer: %w", err)
	}
	want := s
	if want.Base == "" {
		want.Base = BaseVersion(rootfs)
	}
	if overlay != "" {
		switch want.Overlay {
		case "":
			want.Overlay = overlay
		case overlay:
		default:
			return s, "", fmt.Errorf("%w: its upper was written by the %s overlay, this host mounts %s — reset it to rebuild", ErrOverlay, want.Overlay, overlay)
		}
	}
	base, ok := ResolveBase(rootfs, want.Base)
	if !ok {
		return s, "", fmt.Errorf("%w: base %q — reset it to rebuild on the current base", ErrBaseMissing, want.Base)
	}
	if want != s {
		if err := Stamp(dir, want); err != nil {
			return s, "", fmt.Errorf("pin a layer: %w", err)
		}
	}
	return want, base, nil
}
