package vm

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsureDisk returns a VM terminal's persistent disk image in layer (the
// tile's terminal layer dir, .xbin/term/<key>): a sparse file the guest
// formats as ext4 on first use and keeps its root filesystem changes on —
// `apt install` survives the session (plans/vm-sandbox.md). It grows to the
// policy's size (never shrinks; the guest grows its filesystem to match) and
// shares the layer's base pin, lock and Reset. xbind only creates and sizes
// it; the host never reads what the guest wrote.
func (m *Manager) EnsureDisk(layer string) (string, error) {
	dir := filepath.Join(layer, "vm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "disk.img")
	want := int64(m.Policy().DiskGiB) << 30
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.Size() < want {
		if err := f.Truncate(want); err != nil {
			return "", fmt.Errorf("size the VM disk: %w", err)
		}
	}
	return p, nil
}

// Disk is one VM terminal disk image on the host.
type Disk struct {
	Key            string `json:"key"` // the tile's layer key (util.CompKey)
	Path           string `json:"path"`
	ApparentBytes  int64  `json:"apparentBytes"`  // its size to the guest
	AllocatedBytes int64  `json:"allocatedBytes"` // what it takes on the host (sparse)
}

// ListDisks finds the VM disk images under root's terminal layers. It only
// stats them (never follows a symlink, never reads what the guest wrote).
func ListDisks(root string) []Disk {
	paths, _ := filepath.Glob(filepath.Join(root, ".xbin", "term", "*", "vm", "disk.img"))
	out := []Disk{}
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		key := filepath.Base(filepath.Dir(filepath.Dir(p)))
		out = append(out, Disk{Key: key, Path: p, ApparentBytes: fi.Size(), AllocatedBytes: allocated(fi)})
	}
	return out
}
