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
