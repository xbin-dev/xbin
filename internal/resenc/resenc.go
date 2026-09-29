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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Manager owns the gocryptfs mounts for one workspace. Safe for concurrent use.
//
// Each volume, (scopeKey, name), has a lock of its own, held across its
// gocryptfs -init and mount, so N people's cold starts don't queue behind
// one another (PD-48); mu guards the maps only, never a gocryptfs run. A
// volume's users may hold it (Hold): a user partition's volume nobody held
// for a while is unmounted by UnmountIdle.
type Manager struct {
	root   string                             // workspace root
	bin    string                             // gocryptfs binary ("" = unavailable)
	derive func(label string) ([]byte, error) // vault barrier DeriveKey

	mu     sync.Mutex
	mounts map[string]string // key(scopeKey,name) → mount dir
	modes  map[string]bool   // key → mounted in single-tenant mode
	locks  map[string]*keyLock
	refs   map[string]int       // key → Holds not yet released
	used   map[string]time.Time // key → when it was mounted or last released
	// epoch counts UnmountAll/Close: an Ensure that began before one doesn't
	// record, and takes down, the mount it made meanwhile (a seal).
	epoch uint64
	// stSupport caches whether the binary understands -xbin-single-tenant
	// (our patched build does; a stock/distro gocryptfs does not).
	stOnce    sync.Once
	stSupport bool
}

// keyLock is one volume's lock and how many wait on or hold it.
type keyLock struct {
	mu    sync.Mutex
	users int
}

// New builds a Manager. bin is the gocryptfs path (see Resolve); derive is the
// barrier's DeriveKey (returns ErrSealed when sealed).
func New(root, bin string, derive func(string) ([]byte, error)) *Manager {
	return &Manager{root: root, bin: bin, derive: derive,
		mounts: map[string]string{}, modes: map[string]bool{}, locks: map[string]*keyLock{},
		refs: map[string]int{}, used: map[string]time.Time{}}
}

// lockKey takes volume k's lock; the answer releases it.
func (m *Manager) lockKey(k string) (unlock func()) {
	m.mu.Lock()
	l := m.locks[k]
	if l == nil {
		l = &keyLock{}
		m.locks[k] = l
	}
	l.users++
	m.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		m.mu.Lock()
		if l.users--; l.users == 0 {
			delete(m.locks, k)
		}
		m.mu.Unlock()
	}
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

// plainSegment: s names one directory entry — no separator, not . or ..,
// no NUL.
func plainSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

// deploymentsLevel is the directory level, inside data/resources-enc and
// .xbin/resenc, that holds the volumes of every tile deployment but main;
// partitionsLevel the one that holds user partitions' volumes
// (plans/partitions/03 §B.1).
const (
	deploymentsLevel = ".deployments"
	partitionsLevel  = ".partitions"
)

// dirKeyOK: scopeKey is one of the three shapes a resource's directory key
// takes. main's is one plain segment, a scope key, which never starts with
// "." (no scope directory does), so it can't reach the other levels. Every
// other deployment's is exactly ".deployments/<scope>/<deployment>/fs", and
// a user partition's ".partitions/<scope>/<deployment>/<partition>/fs", each
// middle part a plain segment that doesn't start with "." either: the
// broker's encoded scope, a deployment name, a partition key.
func dirKeyOK(scopeKey string) bool {
	if plainSegment(scopeKey) {
		return scopeKey[0] != '.'
	}
	parts := strings.Split(scopeKey, "/")
	switch {
	case len(parts) == 4 && parts[0] == deploymentsLevel,
		len(parts) == 5 && parts[0] == partitionsLevel:
	default:
		return false
	}
	for _, p := range parts[1 : len(parts)-1] {
		if !plainSegment(p) || p[0] == '.' {
			return false
		}
	}
	return parts[len(parts)-1] == "fs"
}

// PartitionVolume reports whether scopeKey is a user partition's directory
// key: its new volumes take a cheap scrypt cost, and it is unmounted when
// idle (PD-48).
func PartitionVolume(scopeKey string) bool {
	return strings.HasPrefix(scopeKey, partitionsLevel+"/") && dirKeyOK(scopeKey)
}

// partitionScryptN is the scrypt cost (log2 N) of a new user partition's
// volume (PD-48): its password is already a 256-bit HKDF subkey of the
// vault DEK, so stretching it buys nothing, while the default (16) costs
// ~0.3 s and 64 MiB on every init and mount. Existing volumes keep theirs.
const partitionScryptN = "10"

// CipherDir is the on-disk ciphertext directory for a resource.
func (m *Manager) CipherDir(scopeKey, name string) string {
	return filepath.Join(m.root, "data", "resources-enc", scopeKey, name)
}

// MountDir is the (runtime) decrypted mountpoint bound into sandboxes.
func (m *Manager) MountDir(scopeKey, name string) string { return MountPath(m.root, scopeKey, name) }

// MountPath is the decrypted mountpoint of a resource of the workspace at
// root, as a Manager on it mounts it: the canonical path the runner
// re-derives for a shared resource of a partitioned scope.
func MountPath(root, scopeKey, name string) string {
	return filepath.Join(root, ".xbin", "resenc", scopeKey, name)
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
	// The last line behind the registry's resource name rule (D118): the two
	// become directories under data/resources-enc and .xbin/resenc, where
	// this creates, initializes and mounts — never anywhere else. A
	// deployment beyond main keeps its volumes one level down (dirKeyOK).
	if !dirKeyOK(scopeKey) || !plainSegment(name) {
		return "", fmt.Errorf("resource %q/%q: not a plain path segment — refused", scopeKey, name)
	}
	if m.bin == "" {
		return "", fmt.Errorf("gocryptfs not available")
	}
	k := mkey(scopeKey, name)
	defer m.lockKey(k)()
	m.mu.Lock()
	epoch, mode := m.epoch, m.modes[k]
	m.mu.Unlock()

	cipher := m.CipherDir(scopeKey, name)
	mount := m.MountDir(scopeKey, name)
	if isMounted(mount) {
		if mode == singleTenant {
			return mount, m.record(k, mount, singleTenant, epoch)
		}
		// Mounted in the other mode (cap:containers granted/revoked since):
		// remount. The broker stops the scope's backends around cap changes,
		// so the mount should be free; a straggler surfaces as EBUSY here.
		if err := fusermountU(mount, false); err != nil {
			return "", fmt.Errorf("remount %s for mode change: %w", resID, err)
		}
		m.forget(k)
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
		args := []string{"-init", "-q", "-passfile", "/dev/stdin"}
		if PartitionVolume(scopeKey) {
			args = append(args, "-scryptn", partitionScryptN)
		}
		if err := m.run(pw, append(args, cipher)...); err != nil {
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
		if hint := m.mountDeniedHint(err.Error()); hint != "" {
			err = fmt.Errorf("%w — %s", err, hint)
		}
		return "", fmt.Errorf("gocryptfs mount %s: %w", resID, err)
	}
	return mount, m.record(k, mount, singleTenant, epoch)
}

// errUnmountedMeanwhile: every view was unmounted (a seal, a shutdown) while
// an Ensure mounted one; the Ensure takes its mount down again.
var errUnmountedMeanwhile = errors.New("the resource views were unmounted meanwhile (the vault was sealed or xbind is stopping)")

// record notes k's mount, made under its lock, unless an UnmountAll or a
// Close ran since epoch: then it takes the mount down instead.
func (m *Manager) record(k, mount string, singleTenant bool, epoch uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch != epoch {
		if isMounted(mount) {
			_ = fusermountU(mount, false)
		}
		return errUnmountedMeanwhile
	}
	m.used[k] = time.Now() // every Ensure is a use: the idle clock restarts
	m.mounts[k] = mount
	m.modes[k] = singleTenant
	return nil
}

// forget drops k from the maps (its view is unmounted).
func (m *Manager) forget(k string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.mounts, k)
	delete(m.modes, k)
	delete(m.used, k)
}

// apparmorProfile is where Ubuntu keeps AppArmor's profile for fusermount3
// (a var so tests can point it elsewhere).
var apparmorProfile = "/etc/apparmor.d/fusermount3"

// mountDeniedHint explains fusermount3's "mount failed: Permission denied"
// on a host whose AppArmor confines fusermount3 (D110): Ubuntu's profile
// allows FUSE mount points only under home dirs, /mnt, /media, /tmp and
// /run/user, so a workspace elsewhere (the installer's /opt/xbin/workspace)
// can't mount encrypted resources — and the tiles using them stay held —
// until a local rule allows its resenc dir. "" when the output isn't that
// denial or the host has no such profile.
func (m *Manager) mountDeniedHint(out string) string {
	if !strings.Contains(out, "fusermount") || !strings.Contains(out, "mount failed: Permission denied") {
		return ""
	}
	if _, err := os.Stat(apparmorProfile); err != nil {
		return ""
	}
	dir := filepath.Join(m.root, ".xbin", "resenc")
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return fmt.Sprintf("AppArmor's fusermount3 profile (%s) most likely refused the mount point: it allows FUSE mounts only under home dirs, /mnt, /media and /tmp. "+
		"Fix: re-run the installer (deploy/install.sh allows this workspace), or as root add "+
		"`mount fstype=@{fuse_types} options=(nosuid,nodev) options in (ro,rw,noatime,dirsync,nodiratime,noexec,sync) -> \"%s/**/\",` and `umount \"%s/**/\",` "+
		"to /etc/apparmor.d/local/fusermount3, run `apparmor_parser -r %s` and restart xbind (/docs/resources.md, Encryption at rest)",
		apparmorProfile, dir, dir, apparmorProfile)
}

// SupportsSingleTenant reports whether the gocryptfs binary carries the xbin
// single-tenant patch (probed once via its long help text).
func (m *Manager) SupportsSingleTenant() bool {
	if m.bin == "" {
		return false
	}
	m.stOnce.Do(func() {
		out, _ := exec.Command(m.bin, "-hh").CombinedOutput() // exec-ok: gocryptfs's own help, no workspace input
		m.stSupport = strings.Contains(string(out), "xbin-single-tenant")
	})
	return m.stSupport
}

// Unmount unmounts one resource's decrypted view (the ciphertext stays).
func (m *Manager) Unmount(scopeKey, name string) error {
	k := mkey(scopeKey, name)
	defer m.lockKey(k)()
	m.forget(k)
	mount := m.MountDir(scopeKey, name)
	if !isMounted(mount) {
		return nil
	}
	return fusermountU(mount, false)
}

// Hold takes a reference on a resource's view for one of its users (a
// backend instance, a blob request, a backup, a reset) until release: a
// user partition's view isn't unmounted for idleness while held (PD-48).
// It mounts nothing; the holder then Ensures it. release is idempotent.
func (m *Manager) Hold(scopeKey, name string) (release func()) {
	k := mkey(scopeKey, name)
	m.mu.Lock()
	m.refs[k]++
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.refs[k]--; m.refs[k] <= 0 {
				delete(m.refs, k)
				if _, ok := m.mounts[k]; ok {
					m.used[k] = time.Now()
				}
			}
			m.mu.Unlock()
		})
	}
}

// Touch restarts a mounted view's idle clock: a use that found it already
// mounted (the broker's Ensure short-circuit). A no-op for a view this
// Manager doesn't hold.
func (m *Manager) Touch(scopeKey, name string) {
	k := mkey(scopeKey, name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.mounts[k]; ok {
		m.used[k] = time.Now()
	}
}

// UnmountIdle unmounts every user partition's view (PartitionVolume) that
// nobody holds and nobody used since before now-idle, and for which keep
// (nil: none) doesn't answer true — the broker's word that its partition's
// instance runs, which restarts its idle clock at now (so it runs from when
// the instance stopped). A view something still has open stays mounted
// (fusermount refuses it) and is tried again at the next call. Other views
// stay mounted until seal, as always. It answers the views it unmounted.
func (m *Manager) UnmountIdle(now time.Time, idle time.Duration, keep func(scopeKey, name string) bool) []Mount {
	var out []Mount
	for _, mt := range m.Mounts() {
		k := mkey(mt.ScopeKey, mt.Name)
		if !PartitionVolume(mt.ScopeKey) {
			continue
		}
		if keep != nil && keep(mt.ScopeKey, mt.Name) {
			m.mu.Lock()
			if _, ok := m.mounts[k]; ok {
				m.used[k] = now
			}
			m.mu.Unlock()
			continue
		}
		unlock := m.lockKey(k)
		m.mu.Lock()
		_, still := m.mounts[k]
		due := still && m.refs[k] == 0 && now.Sub(m.used[k]) >= idle
		m.mu.Unlock()
		if due {
			if mount := m.MountDir(mt.ScopeKey, mt.Name); !isMounted(mount) || fusermountU(mount, false) == nil {
				m.forget(k)
				out = append(out, mt)
			}
		}
		unlock()
	}
	return out
}

// Mount is one decrypted view a Manager holds: the directory key and name it
// was ensured with, and its mode.
type Mount struct {
	ScopeKey, Name string
	SingleTenant   bool
}

// Mounts lists the views this Manager has ensured and not unmounted since,
// sorted, so a caller can re-Ensure them when their mode may have changed.
func (m *Manager) Mounts() []Mount {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Mount, 0, len(m.mounts))
	for k := range m.mounts {
		sk, name, _ := strings.Cut(k, "\x00")
		out = append(out, Mount{ScopeKey: sk, Name: name, SingleTenant: m.modes[k]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ScopeKey != out[j].ScopeKey {
			return out[i].ScopeKey < out[j].ScopeKey
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// UnmountAll unmounts every mount this Manager holds (seal / shutdown). An
// Ensure under way meanwhile takes its own mount down (record).
func (m *Manager) UnmountAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epoch++
	for k, mount := range m.mounts {
		if isMounted(mount) {
			_ = fusermountU(mount, false)
		}
		delete(m.mounts, k)
		delete(m.modes, k)
		delete(m.used, k)
	}
}

// Close unmounts every decrypted view as xbind shuts down, so nothing of a
// resource stays readable — or a gocryptfs holding its key running — once
// xbind is gone: the ciphertext stays, and the next boot mounts again. A
// view something still holds (a terminal's bind, say) goes lazily:
// detached now, its FUSE server gone with its last user.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epoch++
	for k, mount := range m.mounts {
		if isMounted(mount) && fusermountU(mount, false) != nil {
			_ = fusermountU(mount, true)
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
	cmd := exec.Command(m.bin, args...) // exec-ok: gocryptfs on cipher dirs under data/ — xbind-only, never sandbox-writable
	cmd.Stdin = strings.NewReader(pw)   // -passfile /dev/stdin reads one line
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// isMounted reports whether dir is a mount point (scans mountinfo).
// IsMountPoint reports whether dir is itself a mount point (an encrypted
// view is up there), not the bare directory under it: the runner's check
// before it binds a user partition's volume into a sandbox.
func IsMountPoint(dir string) bool { return isMounted(dir) }

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
	out, err := exec.Command(bin, args...).CombinedOutput() // exec-ok: fusermount -u of xbind's own mountpoint
	if err != nil {
		return fmt.Errorf("%s -u %s: %v: %s", bin, dir, err, strings.TrimSpace(string(out)))
	}
	return nil
}
