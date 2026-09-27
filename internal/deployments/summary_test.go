package deployments

import "testing"

// covers P7 P20 — the primary summary of /components (11-contract §8): a
// paused tile says its primary is pinned, a protected one says so too; a tile
// in the zero state, one whose record holds it, and one whose record isn't
// its (another owner's) gain nothing.
func TestPrimarySummary(t *testing.T) {
	root := t.TempDir()
	o := newOwners(map[string]string{"apps/crm": "user:ana", "apps/shop": "user:ana", "apps/bad": "user:ana", "apps/old": "user:ana"})
	writeRecordDoc(t, root, "apps/crm", recordDoc("apps/crm", "user:ana"))
	shop := recordDoc("apps/shop", "user:ana")
	shop["protectedPrimary"] = true
	writeRecordDoc(t, root, "apps/shop", shop)
	bad := recordDoc("apps/bad", "user:ana")
	bad["seq"] = 0
	writeRecordDoc(t, root, "apps/bad", bad)
	writeRecordDoc(t, root, "apps/old", recordDoc("apps/old", "user:bob"))
	p := bootPlane(t, root, o)

	type sum struct {
		primary           string
		pinned, protected bool
		ok                bool
	}
	for tile, want := range map[string]sum{
		"apps/crm":   {"main", true, false, true},
		"apps/shop":  {"main", true, true, true},
		"apps/bad":   {},
		"apps/old":   {},
		"apps/plain": {},
	} {
		var got sum
		got.primary, got.pinned, got.protected, got.ok = p.PrimarySummary(tile)
		if got != want {
			t.Errorf("%s: PrimarySummary = %+v, want %+v", tile, got, want)
		}
	}
	if _, _, _, ok := (&Plane{}).PrimarySummary("apps/crm"); ok {
		t.Error("an unbooted plane has a summary")
	}
}
