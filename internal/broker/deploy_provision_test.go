package broker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D127n T2 — the primary's rows of 15-test-plan's D127n provisioning:
// a pinned primary provisions from its checkpoint's scope.json, read beneath
// the checkpoint and checked, so a resource only the work tree declares
// isn't provisioned or resolvable; a checkpoint scope.json naming a resource
// outside the rule is refused before anything of it is provisioned (the
// tile, failing closed, provisions nothing); a plain-directory scope's
// scope.json serves the tiles in it whatever their primaries run; a resource
// the primary's code no longer declares keeps its data; the primary back on
// the work tree (the live reload target) provisions from the work tree.
// The non-primary rows are M2's.
func TestProvisionFollowsCode(t *testing.T) {
	ws := t.TempDir()
	write := func(root string, files map[string]string) {
		t.Helper()
		for rel, content := range files {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(ws, map[string]string{
		"xbin.json":                `{"schema":1}`,
		"apps/crm/xbin.json":       `{"runtime":"go","uses":[{"target":"res:apps/crm/shared","role":"writer"}]}`,
		"apps/crm/scope.json":      `{"resources":{"shared":{"type":"kv"},"wtkv":{"type":"kv"},"wtfiles":{"type":"filesystem"}}}`,
		"apps/bad/xbin.json":       `{"runtime":"go"}`,
		"apps/bad/scope.json":      `{}`,
		"apps/suite/scope.json":    `{"resources":{"suitejobs":{"type":"cron"}}}`,
		"apps/suite/app/xbin.json": `{"runtime":"go"}`,
	})
	crmCP := t.TempDir()
	write(crmCP, map[string]string{
		"xbin.json":  `{"runtime":"go"}`,
		"scope.json": `{"resources":{"shared":{"type":"kv"},"jobs":{"type":"cron"},"files":{"type":"filesystem"}}}`,
	})
	badCP := t.TempDir()
	write(badCP, map[string]string{
		"xbin.json":  `{"runtime":"go","exposes":{"api":{"kind":"http","paths":["/*"]}}}`,
		"scope.json": `{"resources":{"../../escape":{"type":"cron"},"ok":{"type":"cron"}}}`,
	})
	appCP := t.TempDir()
	write(appCP, map[string]string{
		"xbin.json":  `{"runtime":"go"}`,
		"scope.json": `{"resources":{"stray":{"type":"cron"}}}`, // the work tree doesn't root a scope here
	})

	reg, err := registry.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false) // provisions the zero state
	if err != nil {
		t.Fatal(err)
	}
	fileRes := func() []string {
		var out []string
		b.forEachFileRes(func(scope, name, _ string) { out = append(out, scope+"/"+name) })
		slices.Sort(out)
		return out
	}
	declared := func(target string) bool {
		_, res, ok := b.parseRes(target)
		return ok && res != nil
	}
	cronDir := func(scope string) bool {
		_, err := os.Lstat(filepath.Join(ws, "data", "resources", util.ScopeKey(scope)))
		return err == nil
	}

	// The zero state: the work tree declares.
	if got := fileRes(); !slices.Equal(got, []string{"apps/crm/wtfiles"}) {
		t.Fatalf("zero state file resources %v", got)
	}
	if cronDir("apps/crm") || !cronDir("apps/suite") {
		t.Fatalf("zero state cron dirs: apps/crm %v apps/suite %v", cronDir("apps/crm"), cronDir("apps/suite"))
	}
	// Data of resources the checkpoint won't declare.
	if err := b.kv.db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte("res:apps/crm/wtkv"))
		if err != nil {
			return err
		}
		return bk.Put([]byte("k"), []byte("kept"))
	}); err != nil {
		t.Fatal(err)
	}
	write(ws, map[string]string{"data/resources-enc/" + util.ScopeKey("apps/crm") + "/wtfiles/gocryptfs.conf": "cipher"})
	wtData := filepath.Join(ws, "data", "resources-enc", util.ScopeKey("apps/crm"), "wtfiles", "gocryptfs.conf")

	// The plane prepares each pinned primary: a checkpoint that can't be read
	// or checked isn't provisioned from, and its tile fails closed.
	pinned := map[string]*registry.PinnedCode{}
	for rel, root := range map[string]string{"apps/crm": crmCP, "apps/bad": badCP, "apps/suite/app": appCP} {
		pc, err := registry.ReadCheckpoint(root)
		if rel == "apps/bad" {
			if err == nil || pc != nil {
				t.Fatalf("a checkpoint scope.json naming a resource outside the rule was read: %+v", pc)
			}
			pc = &registry.PinnedCode{} // failing closed: no inbound surface, no backend, nothing declared
		} else if err != nil {
			t.Fatal(err)
		}
		pinned[rel] = pc
	}
	reg.PinnedPrimary = func(rel string) (*registry.PinnedCode, bool) { pc, ok := pinned[rel]; return pc, ok }
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	b.Provision()

	if got := fileRes(); !slices.Equal(got, []string{"apps/crm/files"}) {
		t.Errorf("pinned: file resources %v, want the checkpoint's", got)
	}
	if !cronDir("apps/crm") {
		t.Error("pinned: the checkpoint's cron resource isn't provisioned")
	}
	for target, want := range map[string]bool{
		"res:apps/crm/shared": true, "res:apps/crm/jobs": true, "res:apps/crm/files": true,
		"res:apps/crm/wtkv": false, "res:apps/crm/wtfiles": false,
		"res:apps/bad/ok": false, "res:apps/bad/../../escape": false,
		"res:apps/suite/suitejobs": true, "res:apps/suite/app/stray": false,
	} {
		if got := declared(target); got != want {
			t.Errorf("pinned: %s declared = %v, want %v", target, got, want)
		}
	}
	if cronDir("apps/bad") {
		t.Error("a refused checkpoint scope.json provisioned something")
	}
	if c, _ := reg.Component("apps/bad"); c.HasBackend() || len(c.Manifest.Exposes) > 0 {
		t.Errorf("a tile whose preparation failed: %+v, want no backend and no inbound surface", c.Manifest)
	}
	if !cronDir("apps/suite") {
		t.Error("the plain-directory scope isn't provisioned")
	}

	// No longer declared, still kept.
	var kept []byte
	_ = b.kv.db.View(func(tx *bolt.Tx) error {
		if bk := tx.Bucket([]byte("res:apps/crm/wtkv")); bk != nil {
			kept = append(kept, bk.Get([]byte("k"))...)
		}
		return nil
	})
	if string(kept) != "kept" {
		t.Errorf("the kv data of a resource the primary's code no longer declares: %q, want kept", kept)
	}
	if _, err := os.Lstat(wtData); err != nil {
		t.Errorf("the file data of a resource the primary's code no longer declares: %v", err)
	}

	// The primary back on the work tree provisions from it.
	delete(pinned, "apps/crm")
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	b.Provision()
	if got := fileRes(); !slices.Equal(got, []string{"apps/crm/wtfiles"}) {
		t.Errorf("following the work tree: file resources %v, want the work tree's", got)
	}
	if !declared("res:apps/crm/wtkv") || declared("res:apps/crm/jobs") {
		t.Error("following the work tree: the declarations aren't the work tree's")
	}
}
