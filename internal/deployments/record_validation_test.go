package deployments

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
)

// heldCase is one record that must hold its tile.
type heldCase struct {
	name  string
	setup func(t *testing.T, root, tile string)
	why   string // a substring of the reason
	newer int
}

func docCase(name, why string, edit func(d map[string]any)) heldCase {
	return heldCase{name: name, why: why, setup: func(t *testing.T, root, tile string) {
		d := recordDoc(tile, "user:ana")
		edit(d)
		writeRecordDoc(t, root, tile, d)
	}}
}

func rawCase(name, why, body string) heldCase {
	return heldCase{name: name, why: why, setup: func(t *testing.T, root, tile string) {
		writeRecordFile(t, root, tile, []byte(body))
	}}
}

func deps(d map[string]any) map[string]any { return d["deployments"].(map[string]any) }

// covers T11 P9 P29 C7 NP-06-12 — a record that can't be used holds its
// tile and nothing else: unreadable (a FIFO, an unreadable file, a symlink
// out of the directory, oversize), not a record, a schema not 1 (a newer one
// with 11-contract §1.14's text), a field of the wrong type, and every
// invariant of 05-model §4 (seq, the creation stamp, main and the primary
// existing, the name grammar, live reload driving exactly its unpinned
// deployment, full tree ids, the failed state, P21, limits) and a checkpoint
// the store doesn't hold. A held tile fails closed: CodeFor refuses every
// deployment (nothing starts, never the work tree), a save drives nothing,
// the registry composes the zero manifest with the reason (no backend, no
// inbound surface), CodeRoot refuses, it has no record for the fetch remote,
// and it takes no commit. Boot still succeeds and every other tile answers
// from its own record or the zero state; a pinned primary is never served
// from its work tree. Boot fails only when data/deployments can't be read at
// all, and rewrites nothing.
func TestRecordValidationFailsClosed(t *testing.T) {
	cases := []heldCase{
		rawCase("not JSON", "not a deployment record", `{"schema": 1,`),
		rawCase("not an object", "not a deployment record", `[1, 2]`),
		rawCase("null", "no tile path", `null`),
		rawCase("no tile path", "no tile path", `{"schema": 1, "seq": 3}`),
		rawCase("tile not a string", "no tile path", `{"schema": 1, "tile": 7}`),
		docCase("a field of the wrong type", "doesn't parse", func(d map[string]any) { d["seq"] = "3" }),
		docCase("a deployment of the wrong type", "doesn't parse", func(d map[string]any) { deps(d)["main"] = "pinned" }),
		docCase("schema 0", "schema 0", func(d map[string]any) { d["schema"] = 0 }),
		docCase("schema missing", "schema 0", func(d map[string]any) { delete(d, "schema") }),
		{name: "a newer schema", newer: 2, setup: func(t *testing.T, root, tile string) {
			d := recordDoc(tile, "user:bob") // a newer record isn't judged by its owner either
			d["schema"] = 2
			d["futureRouting"] = map[string]any{"x": 1}
			writeRecordDoc(t, root, tile, d)
		}},
		docCase("seq 0", "seq 0", func(d map[string]any) { d["seq"] = 0 }),
		docCase("no creation stamp", "creation stamp", func(d map[string]any) { delete(d, "created") }),
		docCase("a creation stamp that isn't a time", "creation stamp", func(d map[string]any) { d["created"] = "yesterday" }),
		docCase("negative nextDeploy", "nextDeploy", func(d map[string]any) { d["nextDeploy"] = -1 }),
		docCase("no deployments", "no deployments", func(d map[string]any) { d["deployments"] = map[string]any{} }),
		docCase("no main", "no deployment main", func(d map[string]any) {
			d["deployments"] = map[string]any{"dev": map[string]any{"checkpoint": treeA}}
			d["primary"] = "dev"
		}),
		docCase("a bad name", "deployment name", func(d map[string]any) { deps(d)["Dev"] = map[string]any{"checkpoint": treeA} }),
		docCase("a name with +", "deployment name", func(d map[string]any) { deps(d)["a+b"] = map[string]any{"checkpoint": treeA} }),
		docCase("a null deployment", "is null", func(d map[string]any) { deps(d)["dev"] = nil }),
		docCase("live reload drives a missing deployment", "doesn't exist", func(d map[string]any) { d["liveReload"] = "dev" }),
		docCase("live reload drives a pinned deployment", "which is pinned", func(d map[string]any) { d["liveReload"] = "main" }),
		docCase("a deployment neither pinned nor driven", "no checkpoint", func(d map[string]any) {
			deps(d)["main"].(map[string]any)["checkpoint"] = nil
		}),
		docCase("a short checkpoint id", "full tree id", func(d map[string]any) {
			deps(d)["main"].(map[string]any)["checkpoint"] = "c:3f2a1c9"
		}),
		docCase("an uppercase checkpoint id", "full tree id", func(d map[string]any) {
			deps(d)["main"].(map[string]any)["checkpoint"] = strings.ToUpper(treeA)
		}),
		docCase("an unknown state", "state", func(d map[string]any) { deps(d)["main"].(map[string]any)["state"] = "broken" }),
		docCase("a missing primary", "the primary", func(d map[string]any) { d["primary"] = "dev" }),
		docCase("live reload drives the protected primary", "protected primary", func(d map[string]any) {
			d["protectedPrimary"] = true
			d["liveReload"] = "main"
			deps(d)["main"].(map[string]any)["checkpoint"] = nil
		}),
		docCase("a negative limit", "limit", func(d map[string]any) {
			deps(d)["main"].(map[string]any)["limits"] = map[string]any{"memMiB": -1}
		}),
		docCase("a bad lastLiveReload", "lastLiveReload", func(d map[string]any) { d["lastLiveReload"] = "Main" }),
		{name: "a checkpoint the store doesn't hold", why: "isn't in the store", setup: func(t *testing.T, root, tile string) {
			d := recordDoc(tile, "user:ana")
			deps(d)["main"].(map[string]any)["checkpoint"] = treeB
			writeRecordDoc(t, root, tile, d)
		}},
		{name: "a FIFO", why: "can't read it", setup: func(t *testing.T, root, tile string) {
			p := recordPath(root, tile)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		}},
		{name: "a symlink out of the directory", why: "can't read it", setup: func(t *testing.T, root, tile string) {
			outside := filepath.Join(root, "elsewhere.json")
			b, _ := json.Marshal(recordDoc(tile, "user:ana"))
			mustWrite(t, outside, string(b))
			p := recordPath(root, tile)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../elsewhere.json", p); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversize", why: "larger than", setup: func(t *testing.T, root, tile string) {
			d := recordDoc(tile, "user:ana")
			d["pad"] = strings.Repeat("x", maxRecordBytes)
			writeRecordDoc(t, root, tile, d)
		}},
		{name: "unreadable", why: "can't read it", setup: func(t *testing.T, root, tile string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads a mode-000 file")
			}
			writeRecordDoc(t, root, tile, recordDoc(tile, "user:ana"))
			if err := os.Chmod(recordPath(root, tile), 0); err != nil {
				t.Fatal(err)
			}
		}},
	}

	root := t.TempDir()
	o := newOwners(map[string]string{"apps/good": "user:ana", "apps/live": "user:ana"})
	writeRecordDoc(t, root, "apps/good", recordDoc("apps/good", "user:ana"))
	live := recordDoc("apps/live", "user:ana")
	live["liveReload"] = "main"
	deps(live)["main"].(map[string]any)["checkpoint"] = nil
	writeRecordDoc(t, root, "apps/live", live)

	tiles := map[string]heldCase{}
	for i, c := range cases {
		tile := fmt.Sprintf("apps/t%02d", i)
		o.set(tile, "user:ana")
		skipped := false
		t.Run("setup/"+c.name, func(t *testing.T) {
			defer func() { skipped = t.Skipped() }()
			c.setup(t, root, tile)
		})
		if !skipped {
			tiles[tile] = c
		}
	}
	before := snapshot(t, root)

	idx := newIndex(root, o.ref)
	idx.hasCheckpoint = func(tile, tree string) bool { return tree == treeA }
	done := make(chan error, 1)
	go func() { done <- idx.load() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("load: %v (one bad record must never keep xbind down)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("load blocked (a FIFO?)")
	}
	p := &Plane{Root: root, OwnerRef: o.ref, idx: idx}
	if d := diffSnapshots(before, snapshot(t, root)); len(d) != 0 {
		t.Errorf("load changed the workspace: %v", d)
	}

	for tile, c := range tiles {
		t.Run(c.name, func(t *testing.T) {
			comp := &registry.Component{Path: tile, Dir: filepath.Join(root, tile)}
			f := p.Lookup(tile)
			if f.State != RecordHeld || f.Record != nil || !errors.Is(f.Err, ErrRecordHeld) {
				t.Fatalf("Lookup = %+v; want RecordHeld", f)
			}
			if c.newer > 0 {
				want := fmt.Sprintf("%s's deployment record was written by a newer xbind (schema %d)", tile, c.newer)
				if f.Err.Error() != want {
					t.Errorf("reason %q, want %q", f.Err, want)
				}
			} else if !strings.Contains(f.Err.Error(), c.why) {
				t.Errorf("reason %q doesn't say %q", f.Err, c.why)
			}
			for _, dep := range []string{"main", "dev"} {
				if code, err := p.CodeFor(tile, dep); !errors.Is(err, ErrRecordHeld) || code != (runner.Code{}) {
					t.Errorf("CodeFor(%s) = %+v, %v; want it refused (never the work tree)", dep, code, err)
				}
			}
			if dep, attached := p.LiveReload(tile); dep != "" || attached {
				t.Errorf("LiveReload = %q, %v; want nothing driven", dep, attached)
			}
			pc, ok := p.PinnedPrimary(tile)
			if !ok || pc == nil || !reflect.DeepEqual(pc.Manifest, registry.Manifest{}) || pc.ManifestErr == "" || pc.HasIndex || pc.Native != "" {
				t.Errorf("PinnedPrimary = %+v, %v; want the zero manifest with the reason", pc, ok)
			}
			if root, pinned, err := p.CodeRoot(comp, ""); !errors.Is(err, ErrRecordHeld) || root != "" || pinned {
				t.Errorf("CodeRoot = %q, %v, %v; want it refused", root, pinned, err)
			}
			if p.HasRecord(tile) {
				t.Error("HasRecord: a held record gives the fetch remote")
			}
			if !p.HasDeployment(tile, "main") || p.HasDeployment(tile, "dev") {
				t.Error("HasDeployment: a held tile has main only")
			}
			if got := p.Primary(tile); got != "main" {
				t.Errorf("Primary = %q", got)
			}
			if _, err := idx.commit(tile, -1, pinMain(treeA)); !errors.Is(err, ErrRecordHeld) {
				t.Errorf("commit over a held record: %v", err)
			}
			if err := idx.remove(tile, 3); !errors.Is(err, ErrRecordHeld) {
				t.Errorf("an opt-out over a held record: %v", err)
			}
		})
	}
	if d := diffSnapshots(before, snapshot(t, root)); len(d) != 0 {
		t.Errorf("refused changes wrote: %v", d)
	}

	// A transfer keeps a held tile held: a record that decodes is re-owned
	// with its fault kept (else the owner check would make it inert, and the
	// tile would run its work tree); one without a trustworthy owner (it
	// doesn't decode, a newer schema, not readable) isn't touched.
	for tile, c := range tiles {
		s := idx.tiles[tile]
		reowned := s != nil && s.rec != nil && s.schema == 0
		path := recordPath(root, tile)
		var was string
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm() != 0 {
			b, _ := os.ReadFile(path)
			was = string(b)
		}
		if err := p.RewriteDeploymentOwner(tile, "org:devs"); err != nil {
			t.Errorf("%s RewriteDeploymentOwner: %v", c.name, err)
		}
		o.set(tile, "org:devs")
		if f := p.Lookup(tile); f.State != RecordHeld {
			t.Errorf("%s after a transfer: %+v", c.name, f)
		}
		if was == "" {
			continue
		}
		b, _ := os.ReadFile(path)
		var doc map[string]any
		switch {
		case reowned:
			if err := json.Unmarshal(b, &doc); err != nil || doc["owner"] != "org:devs" {
				t.Errorf("%s re-owned as %v (%v)", c.name, doc["owner"], err)
			}
		case string(b) != was:
			t.Errorf("%s: a record without a trustworthy owner was rewritten", c.name)
		}
	}

	// The tiles beside them: a pinned primary runs its checkpoint and is
	// never served from the work tree; one following it answers today's; a
	// tile without a record the zero state.
	good := &registry.Component{Path: "apps/good", Dir: filepath.Join(root, "apps/good")}
	if f := p.Lookup("apps/good"); f.State != RecordActive || f.Record.Seq != 3 {
		t.Errorf("apps/good: %+v", f)
	}
	if code, err := p.CodeFor("apps/good", "main"); err != nil || code != (runner.Code{Tree: treeA}) {
		t.Errorf("apps/good CodeFor = %+v, %v; want its checkpoint", code, err)
	}
	if dep, attached := p.LiveReload("apps/good"); dep != "" || attached {
		t.Errorf("apps/good LiveReload = %q, %v; want paused", dep, attached)
	}
	if r, pinned, err := p.CodeRoot(good, ""); err == nil || r == good.Dir || pinned {
		t.Errorf("apps/good CodeRoot = %q, %v, %v; want an error while its checkpoint can't be materialized", r, pinned, err)
	}
	if pc, ok := p.PinnedPrimary("apps/good"); !ok || pc.ManifestErr == "" || pc.Manifest.Runtime != "" {
		t.Errorf("apps/good PinnedPrimary = %+v, %v; want the zero manifest until its code is prepared", pc, ok)
	}
	if !p.HasRecord("apps/good") {
		t.Error("apps/good HasRecord = false")
	}
	liveC := &registry.Component{Path: "apps/live", Dir: filepath.Join(root, "apps/live")}
	if code, err := p.CodeFor("apps/live", "main"); err != nil || code != (runner.Code{WorkTree: true}) {
		t.Errorf("apps/live CodeFor = %+v, %v", code, err)
	}
	if dep, attached := p.LiveReload("apps/live"); dep != "main" || !attached {
		t.Errorf("apps/live LiveReload = %q, %v", dep, attached)
	}
	if r, pinned, err := p.CodeRoot(liveC, ""); err != nil || r != liveC.Dir || pinned {
		t.Errorf("apps/live CodeRoot = %q, %v, %v", r, pinned, err)
	}
	if pc, ok := p.PinnedPrimary("apps/live"); pc != nil || ok {
		t.Errorf("apps/live PinnedPrimary = %+v, %v", pc, ok)
	}
	if code, err := p.CodeFor("apps/zero", "main"); err != nil || code != (runner.Code{WorkTree: true}) {
		t.Errorf("apps/zero CodeFor = %+v, %v", code, err)
	}
	if f := p.Lookup("apps/zero"); f.State != RecordNone {
		t.Errorf("apps/zero Lookup = %+v", f)
	}

	// A restore validates an archived record the same way.
	if r, err := ParseRecord(mustJSON(t, recordDoc("apps/good", "user:bob")), "apps/good"); err != nil || r.Seq != 3 {
		t.Errorf("ParseRecord(a valid record) = %+v, %v", r, err)
	}
	for name, doc := range map[string]map[string]any{
		"another tile": recordDoc("apps/other", "user:ana"),
		"seq 0":        func() map[string]any { d := recordDoc("apps/good", ""); d["seq"] = 0; return d }(),
		"newer schema": func() map[string]any { d := recordDoc("apps/good", ""); d["schema"] = 2; return d }(),
	} {
		if _, err := ParseRecord(mustJSON(t, doc), "apps/good"); err == nil {
			t.Errorf("ParseRecord(%s) accepted it", name)
		}
	}

	t.Run("records directory unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode-000 directory")
		}
		root := t.TempDir()
		writeRecordDoc(t, root, "apps/x", recordDoc("apps/x", ""))
		dir := recordDir(root)
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o755)
		p := &Plane{Root: root}
		if err := p.Boot(); err == nil {
			t.Error("Boot succeeded though no record can be read: every pinned primary would run its work tree")
		}
	})
	t.Run("records path not a directory", func(t *testing.T) {
		root := t.TempDir()
		mustWrite(t, recordDir(root), "not a directory")
		p := &Plane{Root: root}
		if err := p.Boot(); err != nil {
			t.Errorf("Boot: %v; want the zero state (no record can exist)", err)
		}
		if code, err := p.CodeFor("apps/x", "main"); err != nil || !code.WorkTree {
			t.Errorf("CodeFor = %+v, %v", code, err)
		}
	})
}
