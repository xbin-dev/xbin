//go:build linux && integration

// Run with: go test -tags=integration ./internal/broker/
// Needs user namespaces and an unpacked rootfs with git (XBIN_TEST_ROOTFS, or
// the repo's .rootfs from `make rootfs`); skips otherwise.
package broker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// TestMain doubles as the sandbox's re-exec init.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
	os.Exit(m.Run())
}

// covers T11 D119g (ledger L13) — the broker half of a restore's store
// rebuild, confined: the archived store's objects are staged in xbind's own
// directory and its refs checked (cat-file) and its objects fscked by git
// inside a sandbox over the stage alone, which never sees the archive's
// config, hooks, info or alternates, so none of their commands runs; only
// refs that name commits among the archived objects reach the plane, under
// refs/xbin/restored/; a forged object (a file's content swapped under its
// id) refuses the restore before anything is written. The store half — the
// plane rebuilding the store from the stage with xbind's own config — lands
// with the deployments plane's restore hook.
func TestRestoreRebuildsStoreConfig(t *testing.T) {
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(fs, "usr", "bin", "git")); err != nil || !sandbox.Available() {
		t.Skip("no rootfs with git, or no user namespaces")
	}
	needStoreGit(t) // the fixtures' checkpoint store is built in direct mode
	checkHostileRestore(t, zeroDataBroker(t), func() {
		confine.Configure(fs)
		t.Cleanup(func() { confine.Configure("") })
		if !confine.Isolated() {
			t.Fatal("not isolated")
		}
	})
}
