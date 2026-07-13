package broker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/magik6k/xbin/internal/util"
)

// A file resource's bytes are CIPHERTEXT under data/resources-enc/<key> once
// encrypted (the plaintext dir is an empty mountpoint), so the old
// plaintext-only scan reported ~0 and quotas went unenforced for the file
// plane on encrypted (production) workspaces. scopeDiskUsage must sum both.
func TestScopeDiskUsageCountsCiphertext(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	writeN := func(p string, n int) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	key := util.ScopeKey("apps/calendar") // a scope in testWorkspace
	writeN(filepath.Join(root, "data", "resources-enc", key, "blobx", "cipher.0"), 4096)
	writeN(filepath.Join(root, "data", "resources", key, "plainx", "f"), 1024)

	got := b.scopeDiskUsage()[key]
	if got != 4096+1024 {
		t.Fatalf("scopeDiskUsage[%s] = %d, want %d (plaintext + ciphertext)", key, got, 5120)
	}
}
