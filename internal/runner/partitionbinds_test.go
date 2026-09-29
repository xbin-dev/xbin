package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// partBindsWS is a workspace whose tile apps/docs asks for user partitions,
// its scope declaring a shared sqlite (team), a "read" filesystem (conf) and
// a partitioned filesystem (files); partitioned says whether the registry's
// mode store answers it partitioned. It answers a runner on it, the tile,
// and the canonical and partition directories of each resource.
func partBindsWS(t *testing.T, partitioned bool) (*Runner, *registry.Component, map[string]string) {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"apps/docs/scope.json": `{"resources":{"team":{"type":"sqlite","shared":true},
			"conf":{"type":"filesystem","shared":"read"},"files":{"type":"filesystem"}}}`,
		"apps/docs/xbin.json": `{"runtime":"go","partition":["user"]}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg := &registry.Registry{Root: root, PartitionModes: func(a registry.PartitionAsk) registry.PartitionMode {
		if partitioned && a.Requested != nil {
			return registry.PartitionMode{State: registry.PartitionPartitioned, Recorded: *a.Requested}
		}
		return registry.PartitionMode{}
	}}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Component("apps/docs")
	if !ok {
		t.Fatal("apps/docs isn't registered")
	}
	pk := ".partitions/apps~docs/main/u-0123456789abcdef0123456789abcdef/fs"
	dirs := map[string]string{
		"team": filepath.Join(root, ".xbin/resenc/apps~docs/team"), "conf": filepath.Join(root, ".xbin/resenc/apps~docs/conf"),
		"files": filepath.Join(root, ".xbin/resenc/apps~docs/files"), "files@part": filepath.Join(root, ".xbin/resenc", pk, "files"),
		"conf@dev": filepath.Join(root, ".xbin/resenc/.deployments/apps~docs/dev/fs/conf"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &Runner{Root: root, Reg: reg, states: map[string]*state{}}, c, dirs
}

// covers PD-45 C7 S8 — TestSharedBindNeedsRegistryShared (plans/partitions/03
// §B.4): a user partition's Shared remap entry binds main's data only at
// its own canonical path and only where the runner's own reading of the
// registry says the resource is shared — never on the remap's word: a
// Shared entry for a partitioned resource, one whose source isn't the
// canonical path, or any on a scope the registry doesn't partition, is
// refused; a "read" resource is bound read-only even when the remap says
// read-write, whatever backs it; a partition's own volume passes the
// main-data guard; a deployment's remap never takes a Shared entry.
func TestSharedBindNeedsRegistryShared(t *testing.T) {
	r, c, d := partBindsWS(t, true)
	shared := r.sharedResources(c)
	env := []string{"XBIN_RES_TEAM=" + filepath.Join(d["team"], "team.sqlite"), "XBIN_RES_CONF=" + d["conf"], "XBIN_RES_FILES=" + d["files"],
		"XBIN_RES_KV=res:apps/docs/kv"}
	good := map[string]ResBind{
		d["team"]:  {Src: d["team"], Shared: true},
		d["conf"]:  {Src: d["conf"], Shared: true}, // RW asked: bound RO all the same
		d["files"]: {Src: d["files@part"]},
	}
	binds, err := partitionBindsFor(env, r.Root, "apps/docs:user:alice", good, shared)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]sandbox.Bind{
		d["team"]:  {Src: d["team"], Dst: d["team"]},
		d["conf"]:  {Src: d["conf"], Dst: d["conf"], RO: true},
		d["files"]: {Src: d["files@part"], Dst: d["files"]},
	}
	if len(binds) != len(want) {
		t.Fatalf("binds %+v", binds)
	}
	for _, b := range binds {
		if w := want[b.Dst]; b != w {
			t.Errorf("bind %+v, want %+v", b, w)
		}
	}

	refused := func(name string, remap map[string]ResBind, shared sharedLookup, why string) {
		t.Helper()
		if _, err := partitionBindsFor(env, r.Root, "apps/docs:user:alice", remap, shared); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v, want a refusal naming %q", name, err, why)
		}
	}
	with := func(k string, rb ResBind) map[string]ResBind {
		m := map[string]ResBind{}
		for kk, v := range good {
			m[kk] = v
		}
		m[k] = rb
		return m
	}
	refused("a partitioned resource marked Shared", with(d["files"], ResBind{Src: d["files"], Shared: true}), shared, "isn't shared in its scope.json")
	refused("a Shared entry off its canonical path", with(d["team"], ResBind{Src: d["conf"], Shared: true}), shared, "binds at its own path")
	refused("a relative Shared source", with(d["team"], ResBind{Src: "team", Shared: true}), shared, "binds at its own path")
	refused("main's data without Shared", with(d["team"], ResBind{Src: d["team"]}), shared, "would bind main's data")
	refused("no remap", nil, shared, "has no data namespace")
	refused("a missing entry", map[string]ResBind{d["team"]: good[d["team"]]}, shared, "no data namespace")
	refused("no registry reading", good, nil, "isn't shared in its scope.json")
	pr, pc, _ := partBindsWS(t, false)
	refused("a scope the registry doesn't partition", good, pr.sharedResources(pc), "isn't shared in its scope.json")

	// A "read" resource backed by a deployment's volume (a primary beyond
	// main) is read-only too.
	binds, err = partitionBindsFor(env, r.Root, "apps/docs:user:alice", with(d["conf"], ResBind{Src: d["conf@dev"]}), shared)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range binds {
		if b.Dst == d["conf"] && (!b.RO || b.Src != d["conf@dev"]) {
			t.Errorf("conf from dev's volume: %+v", b)
		}
	}

	// A deployment's remap never takes a Shared entry.
	if _, err := resourceBindsFor(env, r.Root, "dev", good); err == nil || !strings.Contains(err.Error(), "user partition's only") {
		t.Errorf("a deployment's Shared entry: %v", err)
	}

	// The main-data guard: .partitions is not main's.
	for p, want := range map[string]bool{
		d["team"]: true, d["files@part"]: false, filepath.Join(r.Root, "data/resources-enc/.partitions/apps~docs"): false,
		filepath.Join(r.Root, "data/resources-enc/apps~docs"): true,
	} {
		if got := mainData(r.Root, p); got != want {
			t.Errorf("mainData(%s) = %v", p, got)
		}
	}
}
