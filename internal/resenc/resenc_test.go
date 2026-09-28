package resenc

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// locate the vendored gocryptfs (repo bin/) and require a working FUSE stack;
// otherwise skip — these tests mount a real filesystem.
func testManager(t *testing.T) (*Manager, string) {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("..", "..", "bin", "gocryptfs"))
	if err != nil || !fileExists(bin) {
		t.Skip("bin/gocryptfs not built (make build) — skipping FUSE test")
	}
	if !fileExists("/dev/fuse") {
		t.Skip("/dev/fuse absent — skipping FUSE test")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			t.Skip("fusermount(3) absent — skipping FUSE test")
		}
	}
	root := t.TempDir()
	derive := func(label string) ([]byte, error) {
		h := sha256.Sum256([]byte("testkey/" + label))
		return h[:], nil
	}
	m := New(root, bin, derive)
	return m, root
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestEnsureRoundTripAndLeak(t *testing.T) {
	m, _ := testManager(t)
	t.Cleanup(m.UnmountAll)

	mnt, err := m.Ensure("res:apps/thing/store", "apps_thing", "store", false)
	if err != nil {
		t.Skipf("gocryptfs mount failed (no userns/FUSE perms here?): %v", err)
	}
	if !m.Encrypted("apps_thing", "store") || !m.Mounted("apps_thing", "store") {
		t.Fatal("resource should be encrypted + mounted after Ensure")
	}

	secret := "TOP-SECRET-sqlite-rows-and-notes"
	if err := os.WriteFile(filepath.Join(mnt, "notes.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mnt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "sub", "app.sqlite"), []byte("dbdata"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The plaintext must not appear anywhere in the ciphertext dir.
	cipher := m.CipherDir("apps_thing", "store")
	if grepTree(t, cipher, secret) {
		t.Fatal("plaintext leaked into the ciphertext directory")
	}
	// gocryptfs.conf must exist; filenames must be encrypted (not "notes.txt").
	if !fileExists(filepath.Join(cipher, "gocryptfs.conf")) {
		t.Fatal("cipherdir missing gocryptfs.conf")
	}
	if grepNames(t, cipher, "notes.txt") {
		t.Fatal("filename left in plaintext in the ciphertext directory")
	}

	// Unmount → the mount clears; ciphertext stays. Remount → data round-trips.
	if err := m.Unmount("apps_thing", "store"); err != nil {
		t.Fatal(err)
	}
	if m.Mounted("apps_thing", "store") {
		t.Fatal("still mounted after Unmount")
	}
	mnt2, err := m.Ensure("res:apps/thing/store", "apps_thing", "store", false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(mnt2, "notes.txt"))
	if err != nil || string(got) != secret {
		t.Fatalf("round trip after remount: %v %q", err, got)
	}
}

func grepTree(t *testing.T, dir, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), needle) {
			found = true
		}
		return nil
	})
	return found
}

func grepNames(t *testing.T, dir, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), needle) {
			found = true
		}
		return nil
	})
	return found
}

// The vendored gocryptfs must carry the xbin single-tenant patch — a stock
// binary would leave container-store resources permanently held. Probing is
// pure exec (no FUSE), so this runs everywhere bin/gocryptfs exists.
func TestSingleTenantSupport(t *testing.T) {
	m, _ := testManager(t)
	if !m.SupportsSingleTenant() {
		t.Fatal("bin/gocryptfs lacks -xbin-single-tenant — hack/gocryptfs-patches not applied? (rebuild: make gocryptfs)")
	}
	// And an unsupported binary must refuse a single-tenant Ensure loudly.
	stock := New(t.TempDir(), "/bin/false", func(string) ([]byte, error) { return make([]byte, 32), nil })
	if _, err := stock.Ensure("res:x", "s", "n", true); err == nil {
		t.Fatal("Ensure(singleTenant) with an unsupported binary must error")
	}
}

// fusermount3's refusal under Ubuntu's AppArmor profile (D110) — the error
// gocryptfs relays when the mount point is outside the dirs the profile
// allows — gets an actionable hint, and only then: other failures, and hosts
// without the profile, keep their error as it was.
func TestMountDeniedHint(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(t.TempDir(), "fusermount3")
	if err := os.WriteFile(profile, []byte("profile fusermount3 {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := apparmorProfile
	t.Cleanup(func() { apparmorProfile = old })
	apparmorProfile = profile

	m := New(root, "", nil)
	denied := "exit status 19: /usr/bin/fusermount3: mount failed: Permission denied\nfs.Mount failed: fusermount exited with code 256"
	hint := m.mountDeniedHint(denied)
	for _, want := range []string{
		"AppArmor", "re-run the installer", "/etc/apparmor.d/local/fusermount3",
		`-> "` + filepath.Join(root, ".xbin", "resenc") + `/**/",`,
		"apparmor_parser -r " + profile, "/docs/resources.md",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint lacks %q:\n%s", want, hint)
		}
	}
	for _, other := range []string{
		"exit status 1: fusermount3: failed to access mountpoint: Permission denied",
		"exit status 12: password incorrect",
		"",
	} {
		if h := m.mountDeniedHint(other); h != "" {
			t.Errorf("hint for %q: %s", other, h)
		}
	}
	apparmorProfile = filepath.Join(t.TempDir(), "missing")
	if h := m.mountDeniedHint(denied); h != "" {
		t.Errorf("hint without an AppArmor profile: %s", h)
	}
}

// Ensure carries the hint into the error the broker logs: a stand-in
// gocryptfs that fails the mount the way fusermount3 does under AppArmor.
func TestEnsureMountDeniedHint(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gocryptfs")
	script := "#!/bin/sh\ncase \" $* \" in *\" -init \"*) exit 0 ;; esac\n" +
		"echo '/usr/bin/fusermount3: mount failed: Permission denied' >&2\n" +
		"echo 'fs.Mount failed: fusermount exited with code 256' >&2\nexit 19\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, "fusermount3")
	if err := os.WriteFile(profile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := apparmorProfile
	t.Cleanup(func() { apparmorProfile = old })
	apparmorProfile = profile

	m := New(t.TempDir(), bin, func(string) ([]byte, error) { return make([]byte, 32), nil })
	_, err := m.Ensure("apps~x/files", "apps~x", "files", false)
	if err == nil {
		t.Fatal("Ensure succeeded with a failing gocryptfs")
	}
	if !strings.Contains(err.Error(), "mount failed: Permission denied") || !strings.Contains(err.Error(), "re-run the installer") {
		t.Fatalf("Ensure's error lacks the AppArmor hint: %v", err)
	}
}

// Close — xbind shutting down — unmounts every view, one something still
// holds open too (lazily): no decrypted view, no gocryptfs, outlives it.
func TestCloseUnmountsAll(t *testing.T) {
	m, _ := testManager(t)
	t.Cleanup(m.UnmountAll)
	free, err := m.Ensure("res:apps/thing/a", "apps_thing", "a", false)
	if err != nil {
		t.Skipf("gocryptfs mount failed (no userns/FUSE perms here?): %v", err)
	}
	busy, err := m.Ensure("res:apps/thing/b", "apps_thing", "b", false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(busy, "held"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m.Close()
	for _, mp := range []string{free, busy} {
		if isMounted(mp) {
			t.Errorf("%s is still mounted after Close", mp)
		}
	}
	if m.Mounted("apps_thing", "a") || m.Mounted("apps_thing", "b") {
		t.Error("the manager still counts a view mounted")
	}
}
