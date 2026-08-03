package broker

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
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

// Walk-heavy resource sizes are TTL-cached with ONE background refresher —
// the admin tab polls /runtime every 2s, and without this every poll
// re-walked every filesystem/blob tree and kv bucket.
func TestCachedUsage(t *testing.T) {
	b := testBroker(t)
	var computes atomic.Int64
	compute := func() (int64, string) {
		computes.Add(1)
		return 4096, "1 file"
	}
	// First call kicks the background measure and reports the placeholder.
	if _, detail := b.cachedUsage("res:x/fs", compute); detail != "measuring…" {
		t.Fatalf("first call detail = %q, want measuring…", detail)
	}
	// The refresh lands shortly; then values serve from cache.
	deadline := time.Now().Add(2 * time.Second)
	for {
		size, detail := b.cachedUsage("res:x/fs", compute)
		if detail == "1 file" {
			if size != 4096 {
				t.Fatalf("cached size = %d, want 4096", size)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background measure never landed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Fresh entries must not recompute on every poll.
	for i := 0; i < 20; i++ {
		b.cachedUsage("res:x/fs", compute)
	}
	if n := computes.Load(); n != 1 {
		t.Fatalf("compute ran %d times within the TTL, want 1", n)
	}
}
