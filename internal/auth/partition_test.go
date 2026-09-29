package auth

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-01 S13 — instance tokens of partitions (plans/partitions/02 §2):
// a user partition's token names its partition, is part of the token's
// identity and authenticates only while the coverage hook says so — a 401
// the moment it doesn't (the flag turned off, a recreated person's uid);
// the global instance registers as today's instance; an invalid partition,
// a missing uid or a bad deployment registers nothing (fail closed); the
// eager revocations drop exactly the tile's or the person's tokens.
func TestInstancePartition(t *testing.T) {
	a := zsAuth(t)
	var mu sync.Mutex
	covered := map[string]string{"apps/p\x00user:bob": "uid-b"} // tile\0part → the live uid
	a.SetPartitionCoverage(func(tile string, part util.Partition, uid string) bool {
		mu.Lock()
		defer mu.Unlock()
		live, ok := covered[tile+"\x00"+string(part)]
		return ok && live == uid
	})
	who := func(tok string) (Principal, bool) {
		r := httptest.NewRequest("GET", "/api/xbin/kv/x", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return a.FromRequest(r)
	}

	a.RegisterInstancePartition("t-bob", "apps/p", "", "user:bob", "uid-b")
	a.RegisterInstancePartition("t-global", "apps/p", "", util.PartitionGlobal, "")
	a.RegisterInstancePartition("t-bob-q", "apps/q", "main", "user:bob", "uid-b")
	for _, bad := range []struct{ tok, dep, part, uid string }{
		{"t-org", "", "org:x", "u"}, {"t-nouid", "", "user:bob", ""}, {"t-baddep", "Dev", "user:bob", "u"}, {"t-upper", "", "user:Bob", "u"},
	} {
		a.RegisterInstancePartition(bad.tok, "apps/p", bad.dep, util.Partition(bad.part), bad.uid)
		if p, ok := who(bad.tok); ok {
			t.Errorf("%s registered: %+v", bad.tok, p)
		}
	}

	if p, ok := who("t-bob"); !ok || p.Component != "apps/p" || p.Via != "instance" || p.Partition != "user:bob" || p.Deployment != "" {
		t.Errorf("bob's partition token: %+v %v", p, ok)
	}
	if p, ok := who("t-global"); !ok || p.Partition != "" || p.Component != "apps/p" {
		t.Errorf("the global instance's token reads as today's: %+v %v", p, ok)
	}
	if _, ok := who("t-bob-q"); ok {
		t.Error("a partition the hook doesn't cover authenticated")
	}

	// the partition stops being covered (the flag goes off, bob is
	// recreated with another uid): 401 at once, and back when covered again
	mu.Lock()
	covered["apps/p\x00user:bob"] = "uid-b2"
	mu.Unlock()
	if p, ok := who("t-bob"); ok {
		t.Errorf("a recreated person's token still authenticates: %+v", p)
	}
	mu.Lock()
	covered["apps/p\x00user:bob"] = "uid-b"
	mu.Unlock()
	if _, ok := who("t-bob"); !ok {
		t.Error("covered again, the token should authenticate")
	}

	// no hook: no user partition's token authenticates
	b := zsAuth(t)
	b.RegisterInstancePartition("t", "apps/p", "", "user:bob", "uid-b")
	if _, ok := b.lookupInstance("t"); ok {
		t.Error("a user partition's token authenticated without a coverage hook")
	}

	// eager revocation
	a.RegisterInstancePartition("t-alice", "apps/p", "", "user:alice", "uid-a")
	a.RegisterInstance("t-main", "apps/p")
	if n := a.RevokeUserPartitionInstances("bob"); n != 2 {
		t.Errorf("RevokeUserPartitionInstances(bob) dropped %d, want 2", n)
	}
	if n := a.RevokePartitionInstances("apps/p"); n != 1 {
		t.Errorf("RevokePartitionInstances(apps/p) dropped %d, want alice's 1", n)
	}
	if _, ok := who("t-main"); !ok {
		t.Error("revoking partitions dropped the main instance's token")
	}
	if _, ok := who("t-global"); !ok {
		t.Error("revoking partitions dropped the global instance's token")
	}
}
