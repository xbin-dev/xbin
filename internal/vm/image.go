package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// imageKey names a rootfs image: the base version plus a content identity of
// the unpacked tree (a rebuilt base keeps its recipe-hash version) and the
// mkfs build that makes it.
func (m *Manager) imageKey(rootfs string) string {
	ver := "unversioned"
	if b, err := os.ReadFile(filepath.Join(rootfs, "etc", "xbin-base-version")); err == nil {
		ver = strings.TrimSpace(string(b))
	}
	h := sha256.New()
	for _, p := range []string{"etc/xbin-base-version", "etc/os-release", "etc/xbin-rootfs-tools", "usr/bin"} {
		if fi, err := os.Stat(filepath.Join(rootfs, p)); err == nil {
			fmt.Fprintf(h, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
		}
	}
	if fi, err := os.Stat(m.assets.Mkfs); err == nil {
		fmt.Fprintf(h, "mkfs %d %d\n", fi.Size(), fi.ModTime().UnixNano())
	}
	fmt.Fprintln(h, imageOpts)
	return ver + "-" + hex.EncodeToString(h.Sum(nil))[:12]
}

const imageOpts = "--all-root -zlz4"

// image returns the read-only erofs image of rootfs, building it on first
// use (seconds; single-flight per key). mkfs.erofs runs confined (D78) with
// the rootfs bound read-only; xbind only renames its output into place.
// --all-root makes the image's files root's, as the namespace sandbox's uid
// map shows them.
func (m *Manager) image(ctx context.Context, rootfs string) (string, error) {
	key := m.imageKey(rootfs)
	dir := filepath.Join(m.Root, ".xbin", "vm", "images")
	out := filepath.Join(dir, key+".erofs")
	unlock := m.lockKey("image:" + key)
	defer unlock()
	if isFile(out) {
		return out, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, "."+key+".tmp")
	_ = os.Remove(tmp)
	start := time.Now()
	args := append([]string{m.assets.Mkfs}, strings.Fields(imageOpts)...)
	args = append(args, "--quiet", tmp, rootfs)
	if _, err := confine.Run(ctx, confine.Cmd{
		Argv:    args,
		Dir:     dir,
		Binds:   []sandbox.Bind{confine.RO(m.assets.Mkfs), confine.RO(rootfs)},
		Timeout: 15 * time.Minute,
	}); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("build the VM rootfs image: %w", err)
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	m.logf("vm: built rootfs image %s in %s", filepath.Base(out), time.Since(start).Round(time.Millisecond))
	return out, nil
}

// GC removes rootfs images built from anything but the current base, and
// leftovers of interrupted builds. Run at boot, before any VM starts.
func (m *Manager) GC() {
	if m.assets.Mkfs == "" || m.Rootfs == "" {
		return
	}
	current := m.imageKey(m.Rootfs) + ".erofs"
	for _, sub := range []string{"images", "initrd"} {
		dir := filepath.Join(m.Root, ".xbin", "vm", sub)
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			name := e.Name()
			if strings.HasPrefix(name, ".") || (sub == "images" && name != current) {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
	}
}
