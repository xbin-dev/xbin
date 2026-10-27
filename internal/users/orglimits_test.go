package users

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/limits"
)

// covers D203 — an org's per-tile resource ceilings (Org.Limits) round-trip
// through users.json, survive an org update (UpsertOrg), change kind by kind
// (0 removes), refuse unknown kinds and nonsense values, and a users.json
// written before them still loads with none.
func TestOrgLimitsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte(`{"users":[{"id":"carol","role":"user"}],
		"orgs":[{"id":"acme","name":"Acme","members":[{"id":"carol","level":"read","admin":true}],"created":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := s.Org("acme"); !ok || o.Limits != nil || s.OrgLimitsOf("acme") != nil {
		t.Fatalf("an old users.json: %+v", o)
	}
	got, err := s.SetOrgLimits("acme", map[limits.Kind]int64{limits.MemMiB: 4096, limits.DiskGiB: 200})
	if err != nil || !reflect.DeepEqual(got, OrgLimits{limits.MemMiB: 4096, limits.DiskGiB: 200}) {
		t.Fatalf("SetOrgLimits: %v %v", got, err)
	}
	if _, err := s.UpsertOrg(Org{ID: "acme", Name: "Acme Inc", Members: []Member{{ID: "carol", Level: LevelRead, Admin: true}}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SetOrgLimits("acme", map[limits.Kind]int64{limits.DiskGiB: 0, limits.SbxVCPUs: 8}); err != nil ||
		!reflect.DeepEqual(got, OrgLimits{limits.MemMiB: 4096, limits.SbxVCPUs: 8}) {
		t.Fatalf("a change: %v %v", got, err)
	}
	for _, bad := range []map[limits.Kind]int64{{"cpu": 1}, {limits.MemMiB: -1}, {limits.MemMiB: 1 << 41}} {
		if _, err := s.SetOrgLimits("acme", bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
	if _, err := s.SetOrgLimits("nope", map[limits.Kind]int64{limits.MemMiB: 1}); err == nil || !strings.Contains(err.Error(), "no such org") {
		t.Errorf("an unknown org: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := s2.Org("acme"); o.Name != "Acme Inc" || !reflect.DeepEqual(o.Limits, OrgLimits{limits.MemMiB: 4096, limits.SbxVCPUs: 8}) {
		t.Errorf("after a reload: %+v", o)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "users.json"))
	if !strings.Contains(string(b), `"limits": {`) || !strings.Contains(string(b), `"memMiB": 4096`) {
		t.Errorf("users.json: %s", b)
	}
	if _, err := s2.SetOrgLimits("acme", map[limits.Kind]int64{limits.MemMiB: 0, limits.SbxVCPUs: 0}); err != nil {
		t.Fatal(err)
	}
	if o, _ := s2.Org("acme"); o.Limits != nil {
		t.Errorf("every ceiling removed: %+v", o.Limits)
	}
}
