package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// runDirFor picks the socket directory. Preferred: <ws>/.xbin/run. Unix
// socket paths are limited to ~108 bytes, so for deeply nested workspaces we
// fall back to a short tmp dir and leave a symlink at .xbin/run so shells
// still find the sockets where the docs say they are.
// runDirFor picks the directory holding xbind's IPC sockets (the gateway socket
// + each backend's per-generation listen socket). It is bind-mounted **RW** into
// every component sandbox, so it must live on a **tmpfs**, never the workspace
// disk: a component sandbox must not get a RW mount backed by host disk
// (plans/isolation.md — only tmpfs / gocryptfs / ro). We prefer systemd's
// RuntimeDirectory (/run/xbin) or $XDG_RUNTIME_DIR (both tmpfs), then $TMPDIR;
// a symlink under `.xbin/run` points at it for discoverability.
func runDirFor(root string) string {
	link := filepath.Join(root, ".xbin", "run")
	_ = os.RemoveAll(link) // stale sockets/symlink from a previous xbind
	h := sha256.Sum256([]byte(root))
	name := "xbin-" + hex.EncodeToString(h[:4])
	for _, base := range runtimeBases() {
		dir := filepath.Join(base, name, "run")
		_ = os.RemoveAll(dir)
		if os.MkdirAll(dir, 0o700) != nil {
			continue
		}
		if !isTmpfs(dir) {
			_ = os.RemoveAll(dir)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(link), 0o755)
		_ = os.Symlink(dir, link)
		return dir
	}
	// No tmpfs available: fall back to the workspace and warn — a component
	// sandbox will then bind a RW host-disk run dir (set RuntimeDirectory=xbin).
	slog.Warn("run dir: no tmpfs runtime dir found (RuntimeDirectory/XDG_RUNTIME_DIR/TMPDIR); component sandboxes will get a RW host-disk run dir — set RuntimeDirectory=xbin in the unit")
	_ = os.MkdirAll(link, 0o755)
	return link
}

// runtimeBases lists tmpfs candidates for the run dir, most-preferred first.
func runtimeBases() []string {
	var out []string
	if d := os.Getenv("RUNTIME_DIRECTORY"); d != "" { // systemd RuntimeDirectory=
		out = append(out, strings.SplitN(d, ":", 2)[0])
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		out = append(out, d)
	}
	return append(out, os.TempDir())
}

// isTmpfs reports whether dir is on a tmpfs/ramfs, so a RW bind of it into a
// sandbox exposes no host disk.
func isTmpfs(dir string) bool {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return false
	}
	switch uint32(st.Type) {
	case 0x01021994, 0x858458f6: // TMPFS_MAGIC, RAMFS_MAGIC
		return true
	}
	return false
}
