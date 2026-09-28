package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/xbin-dev/xbin/internal/layers"
)

// EnsureDisk returns a VM terminal's persistent disk image in layer (the
// tile's terminal layer dir, .xbin/term/<key>): a sparse file the guest
// formats as ext4 on first use and keeps its root filesystem changes on —
// `apt install` survives the session (plans/vm-sandbox.md). It grows to the
// policy's size (never shrinks; the guest grows its filesystem to match) and
// shares the layer's base pin, lock and Reset.
func (m *Manager) EnsureDisk(layer string) (string, error) {
	return EnsureDiskAt(layer, int64(m.Policy().DiskGiB)<<30)
}

// EnsureDiskAt makes the sparse disk image dir/vm/disk.img of the layer dir
// dir (a terminal's layer, or a tile sandbox's cur/) and grows it to size
// bytes — grow only: a smaller size leaves it as it is, since the guest's
// filesystem fills it. Tile sandboxes size theirs by their own diskGiB
// (plans/tile-sandbox-runtime.md §6.3); terminals by the VM policy
// (EnsureDisk). xbind only creates, sizes and stats it; the host never reads
// what the guest wrote, and a symlink or anything but a regular file in its
// place is refused, never followed.
func EnsureDiskAt(dir string, size int64) (string, error) {
	vdir := filepath.Join(dir, "vm")
	if err := os.MkdirAll(vdir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(vdir, "disk.img")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if errors.Is(err, syscall.ELOOP) {
		return "", fmt.Errorf("the VM disk %s is a symlink", p)
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
		return "", fmt.Errorf("the VM disk %s is not a regular file", p)
	}
	if fi.Size() < size {
		if err := f.Truncate(size); err != nil {
			return "", fmt.Errorf("size the VM disk: %w", err)
		}
	}
	return p, nil
}

// The kinds of VM disk on the host (Disk.Kind), named as the sandbox
// registry names what uses them.
const (
	DiskTerminal = "terminal" // a tile's terminal layer (VM terminals and agent sessions)
	DiskTile     = "tile"     // a tile sandbox's (plans/tile-sandbox-runtime.md)
)

// Disk is one VM disk image on the host.
type Disk struct {
	Kind           string `json:"kind"`                 // DiskTerminal | DiskTile
	Key            string `json:"key"`                  // the tile's key (util.CompKey)
	Sandbox        string `json:"sandbox,omitempty"`    // DiskTile: the sandbox's name
	SandboxUID     string `json:"sandboxUid,omitempty"` // DiskTile: its uid (a re-created name gets a new one)
	Path           string `json:"path"`
	ApparentBytes  int64  `json:"apparentBytes"`  // its size to the guest
	AllocatedBytes int64  `json:"allocatedBytes"` // what it takes on the host (sparse)
}

// ListDisks finds the VM disk images under root: the terminal layers'
// (.xbin/term/<key>/vm/disk.img) and the tile sandboxes'
// (.xbin/sbx/<CK>/<name>.<uid>/cur/vm/disk.img; a dir not named so, like
// .trash, holds none). It only stats them (never follows a symlink in a
// disk's or a cur/'s place, never reads what the guest wrote).
func ListDisks(root string) []Disk {
	out := []Disk{}
	term, _ := filepath.Glob(filepath.Join(root, ".xbin", "term", "*", "vm", "disk.img"))
	for _, p := range term {
		layer := filepath.Dir(filepath.Dir(p))
		if d, ok := statDisk(p); ok {
			d.Kind, d.Key = DiskTerminal, filepath.Base(layer)
			out = append(out, d)
		}
	}
	tile, _ := filepath.Glob(filepath.Join(root, ".xbin", "sbx", "*", "*", layers.CurDir, "vm", "disk.img"))
	for _, p := range tile {
		cur := filepath.Dir(filepath.Dir(p))
		state := filepath.Dir(cur)
		name, uid, ok := layers.SplitStateDir(filepath.Base(state))
		if !ok {
			continue
		}
		if fi, err := os.Lstat(cur); err != nil || !fi.IsDir() {
			continue
		}
		if d, ok := statDisk(p); ok {
			d.Kind, d.Key, d.Sandbox, d.SandboxUID = DiskTile, filepath.Base(filepath.Dir(state)), name, uid
			out = append(out, d)
		}
	}
	return out
}

func statDisk(p string) (Disk, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return Disk{}, false
	}
	return Disk{Path: p, ApparentBytes: fi.Size(), AllocatedBytes: allocated(fi)}, true
}
