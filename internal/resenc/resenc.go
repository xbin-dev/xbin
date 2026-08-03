// Package resenc provides at-rest encryption for file-backed resources
// (filesystem, sqlite) by mounting a per-resource gocryptfs on top of vault
// keys (plans/vault-data.md):
//
//   - ciphertext lives under  data/resources-enc/<scopeKey>/<name>/  (this is
//     what a stolen disk/backup/snapshot sees — encrypted names + contents);
//   - the decrypted view is mounted at  .xbin/resenc/<scopeKey>/<name>/  and
//     bind-mounted into the component sandbox, so the backend sees a normal rw
//     directory / sqlite file;
//   - the gocryptfs password is a 32-byte subkey the vault barrier derives from
//     the DEK (HKDF, label "fs:<res>"), so it exists only in memory while the
//     vault is unsealed and never lands on disk.
//
// Seal ⇒ the DEK is gone ⇒ no new mounts (the broker also unmounts + holds the
// components that use encrypted resources). kv/blob are encrypted separately by
// the broker (they flow through it); this package is only for the two
// directly-bind-mounted resource types.
package resenc

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Manager owns the gocryptfs mounts for one workspace. Safe for concurrent use.
type Manager struct {
	root   string                             // workspace root
	bin    string                             // gocryptfs binary ("" = unavailable)
	derive func(label string) ([]byte, error) // vault barrier DeriveKey

	mu     sync.Mutex
	mounts map[string]string // key(scopeKey,name) → mount dir
	modes  map[string]bool   // key → mounted in single-tenant mode
	// stSupport caches whether the binary understands -xbin-single-tenant
	// (our patched build does; a stock/distro gocryptfs does not).
	stSupport *bool
}

// New builds a Manager. bin is the gocryptfs path (see Resolve); derive is the
// barrier's DeriveKey (returns ErrSealed when sealed).
func New(root, bin string, derive func(string) ([]byte, error)) *Manager {
	return &Manager{root: root, bin: bin, derive: derive,
		mounts: map[string]string{}, modes: map[string]bool{}}
}

// Resolve finds the gocryptfs binary: $XBIN_GOCRYPTFS, a copy bundled next to
// the xbind executable (single-artifact distribution), then $PATH. "" ⇒
// encryption is unavailable and file-backed resources stay plaintext.
func Resolve() string {
	if p := os.Getenv("XBIN_GOCRYPTFS"); p != "" {
		if isExecutable(p) {
			return p
		}
		return ""
	}
	if exe, err := os.Executable(); err == nil {
		if cand := filepath.Join(filepath.Dir(exe), "gocryptfs"); isExecutable(cand) {
			return cand
		}
	}
	if p, err := exec.LookPath("gocryptfs"); err == nil {
		return p
	}
	return ""
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// Available reports whether encryption can run (a gocryptfs binary was found).
func (m *Manager) Available() bool { return m.bin != "" }

func mkey(scopeKey, name string) string { return scopeKey + "\x00" + name }

// CipherDir is the on-disk ciphertext directory for a resource.
func (m *Manager) CipherDir(scopeKey, name string) string {
	return filepath.Join(m.root, "data", "resources-enc", scopeKey, name)
}

// MountDir is the (runtime) decrypted mountpoint bound into sandboxes.
func (m *Manager) MountDir(scopeKey, name string) string {
	return filepath.Join(m.root, ".xbin", "resenc", scopeKey, name)
}

// Encrypted reports whether a resource is stored encrypted (its cipherdir has
// been initialized). This is independent of seal state: once encrypted, always
// encrypted, so the broker must route through the mount even after a restart.
func (m *Manager) Encrypted(scopeKey, name string) bool {
	_, err := os.Stat(filepath.Join(m.CipherDir(scopeKey, name), "gocryptfs.conf"))
	return err == nil
}

// Mounted reports whether the decrypted view is currently mounted.
func (m *Manager) Mounted(scopeKey, name string) bool {
	return isMounted(m.MountDir(scopeKey, name))
}

// password derives the gocryptfs password for a resource: base64 of a 32-byte
// HKDF subkey. The raw key is zeroed before returning.
func (m *Manager) password(resID string) (string, error) {
	k, err := m.derive("fs:" + resID)
	if err != nil {
		return "", err
	}
	pw := base64.RawStdEncoding.EncodeToString(k)
	for i := range k {
		k[i] = 0
	}
	return pw, nil
}

// Ensure initializes (if needed) and mounts a resource's decrypted view,
// returning the mount dir. resID is the key-derivation label. Requires the vault
// unsealed (derive must succeed).
//
// singleTenant mounts with -xbin-single-tenant (our gocryptfs patch,
// hack/gocryptfs-patches/): ownership/mode/special files are virtualized into
// encrypted xattrs and in-mount permission checks are skipped, which is what
// a container layer store needs (sub-uid chowns, 0555 layer dirs, whiteouts)
// and is safe exactly because a resenc mount serves one scope's sandboxes —
// the broker requests it only for filesystem resources of cap:containers
// scopes (docs/resources.md). The on-disk format is identical either way; a
// mode change (cap granted/revoked) just remounts.
func (m *Manager) Ensure(resID, scopeKey, name string, singleTenant bool) (string, error) {
	if m.bin == "" {
		return "", fmt.Errorf("gocryptfs not available")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	k := mkey(scopeKey, name)
	cipher := m.CipherDir(scopeKey, name)
	mount := m.MountDir(scopeKey, name)
	if isMounted(mount) {
		if m.modes[k] == singleTenant {
			m.mounts[k] = mount
			return mount, nil
		}
		// Mounted in the other mode (cap:containers granted/revoked since):
		// remount. The broker stops the scope's backends around cap changes,
		// so the mount should be free; a straggler surfaces as EBUSY here.
		if err := fusermountU(mount, false); err != nil {
			return "", fmt.Errorf("remount %s for mode change: %w", resID, err)
		}
		delete(m.mounts, k)
		delete(m.modes, k)
	}
	if err := os.MkdirAll(cipher, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(mount, 0o700); err != nil {
		return "", err
	}

	if singleTenant && !m.SupportsSingleTenant() {
		return "", fmt.Errorf("this gocryptfs (%s) lacks -xbin-single-tenant, which container-store resources need — rebuild it (make build / hack/build-gocryptfs.sh), or point XBIN_GOCRYPTFS at the xbin-built binary", m.bin)
	}

	pw, err := m.password(resID)
	if err != nil {
		return "", err // ErrSealed when the vault is sealed
	}

	if _, err := os.Stat(filepath.Join(cipher, "gocryptfs.conf")); err != nil {
		if err := m.run(pw, "-init", "-q", "-passfile", "/dev/stdin", cipher); err != nil {
			return "", fmt.Errorf("gocryptfs init %s: %w", resID, err)
		}
	}
	args := []string{"-q", "-passfile", "/dev/stdin"}
	if singleTenant {
		args = append(args, "-xbin-single-tenant")
	}
	args = append(args, cipher, mount)
	if err := m.run(pw, args...); err != nil {
		if singleTenant && strings.Contains(err.Error(), "user_allow_other") {
			// fusermount3 gates -allow_other (implied by single-tenant mode)
			// behind /etc/fuse.conf for non-root; the system installer
			// enables it, user installs need it enabled once by root.
			err = fmt.Errorf("%w — container-store mounts need the line `user_allow_other` in /etc/fuse.conf (root: `echo user_allow_other >> /etc/fuse.conf`)", err)
		}
		return "", fmt.Errorf("gocryptfs mount %s: %w", resID, err)
	}
	m.mounts[k] = mount
	m.modes[k] = singleTenant
	return mount, nil
}

// SupportsSingleTenant reports whether the gocryptfs binary carries the xbin
// single-tenant patch (probed once via its long help text).
func (m *Manager) SupportsSingleTenant() bool {
	if m.bin == "" {
		return false
	}
	if m.stSupport == nil {
		out, _ := exec.Command(m.bin, "-hh").CombinedOutput()
		ok := strings.Contains(string(out), "xbin-single-tenant")
		m.stSupport = &ok
	}
	return *m.stSupport
}

// Unmount unmounts one resource's decrypted view (the ciphertext stays).
func (m *Manager) Unmount(scopeKey, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.mounts, mkey(scopeKey, name))
	delete(m.modes, mkey(scopeKey, name))
	mount := m.MountDir(scopeKey, name)
	if !isMounted(mount) {
		return nil
	}
	return fusermountU(mount, false)
}

// UnmountAll unmounts every mount this Manager holds (seal / shutdown).
func (m *Manager) UnmountAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, mount := range m.mounts {
		if isMounted(mount) {
			_ = fusermountU(mount, false)
		}
		delete(m.mounts, k)
		delete(m.modes, k)
	}
}

// RecoverStale lazy-unmounts any resenc mounts left over from a previous xbind
// (e.g. after a crash) so Ensure starts from a clean slate. Call once at start.
func (m *Manager) RecoverStale() {
	prefix, err := filepath.Abs(filepath.Join(m.root, ".xbin", "resenc"))
	if err != nil {
		return
	}
	for _, mp := range mountsUnder(prefix + string(os.PathSeparator)) {
		_ = fusermountU(mp, true) // lazy: it may be a dead FUSE endpoint
	}
}

// --- helpers ---------------------------------------------------------------

func (m *Manager) run(pw string, args ...string) error {
	cmd := exec.Command(m.bin, args...)
	cmd.Stdin = strings.NewReader(pw) // -passfile /dev/stdin reads one line
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// isMounted reports whether dir is a mount point (scans mountinfo).
func isMounted(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for _, mp := range mountsUnder("") {
		if mp == abs {
			return true
		}
	}
	return false
}

// mountsUnder returns current mount points; if prefix != "", only those under it.
func mountsUnder(prefix string) []string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		// mountinfo field 5 (index 4) is the mount point, octal-escaped.
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		mp := unescapeMountField(fields[4])
		if prefix == "" || strings.HasPrefix(mp, prefix) {
			out = append(out, mp)
		}
	}
	return out
}

// unescapeMountField decodes the octal escapes (\040 space, \011 tab, …) the
// kernel writes in mountinfo paths.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var v int
			if _, err := fmt.Sscanf(s[i+1:i+4], "%o", &v); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func fusermountU(dir string, lazy bool) error {
	bin := "fusermount3"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "fusermount"
	}
	args := []string{"-u", dir}
	if lazy {
		args = []string{"-uz", dir}
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s -u %s: %v: %s", bin, dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}
