package deployments

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// A full tree id, and a second one.
var (
	treeA = "3f2a1c9" + strings.Repeat("0", 31) + "ab"
	treeB = "7b19e02" + strings.Repeat("1", 31) + "cd"
)

// owners is a mutable owner store: tile → owner ref.
type owners struct {
	mu sync.Mutex
	m  map[string]string
}

func newOwners(m map[string]string) *owners { return &owners{m: m} }

func (o *owners) ref(tile string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.m[tile]
}

func (o *owners) set(tile, ref string) {
	o.mu.Lock()
	o.m[tile] = ref
	o.mu.Unlock()
}

// recordDoc is a valid record for tile as a JSON object: main pinned to
// treeA, live reload paused.
func recordDoc(tile, owner string) map[string]any {
	return map[string]any{
		"schema": 1, "tile": tile, "owner": owner, "created": "2026-09-27T10:12:03Z", "seq": 3,
		"liveReload": "", "lastLiveReload": "main", "primary": "main", "protectedPrimary": false,
		"nextDeploy": 4,
		"deployments": map[string]any{
			"main": map[string]any{"checkpoint": treeA, "created": "2026-09-27T10:12:03Z", "by": "user:ana"},
		},
	}
}

// writeRecordFile puts body at tile's record path.
func writeRecordFile(t *testing.T, root, tile string, body []byte) {
	t.Helper()
	p := recordPath(root, tile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRecordDoc(t *testing.T, root, tile string, doc map[string]any) {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeRecordFile(t, root, tile, b)
}

// snapshot maps every path under root to its type, mode and content hash.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			out[filepath.ToSlash(rel)+"/"] = fi.Mode().String()
		case fi.Mode().IsRegular():
			sum := "unreadable"
			if b, err := os.ReadFile(p); err == nil {
				sum = fmt.Sprintf("%x", sha256.Sum256(b))
			}
			out[filepath.ToSlash(rel)] = fmt.Sprintf("%s %s %d %d", fi.Mode(), sum, fi.Size(), fi.ModTime().UnixNano())
		default:
			out[filepath.ToSlash(rel)] = fi.Mode().String()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// diffSnapshots lists what differs: +added, -removed, ~changed.
func diffSnapshots(a, b map[string]string) []string {
	var out []string
	for k, v := range b {
		if w, ok := a[k]; !ok {
			out = append(out, "+"+k)
		} else if w != v {
			out = append(out, "~"+k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, "-"+k)
		}
	}
	return sortedKeys(func() map[string]bool {
		m := map[string]bool{}
		for _, s := range out {
			m[s] = true
		}
		return m
	}())
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bootPlane boots a plane over root with the owner store o.
func bootPlane(t *testing.T, root string, o *owners) *Plane {
	t.Helper()
	p := &Plane{Root: root, OwnerRef: o.ref}
	if err := p.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	return p
}

// pinMain is the change pausing live reload on main commits: main pinned.
func pinMain(tree string) func(*Record) error {
	return func(r *Record) error {
		r.LiveReload = ""
		r.Deployments[util.MainDeployment].Checkpoint = &tree
		return nil
	}
}

// covers D119f D119c D119i PO-7 PO-8 — the record lives only at
// data/deployments/<TileKey>.json (mode 0600), never in the root xbin.json or
// the work tree: a committed opt-in adds that one file and its directory and
// changes nothing else; the zero state is synthesized (schema 0, seq 0, main
// the primary following the work tree) and writes nothing; the first commit
// binds the record (schema 1, the full path, the owner ref, a creation stamp,
// seq 1); boot reads it back and never rewrites it; each tile, a nested one
// included, has its own file; removing it (opting out) leaves the workspace
// as it was.
func TestRecordLocation(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "xbin.json"), `{"name": "ws", "grants": []}`)
	mustWrite(t, filepath.Join(root, "data", "users.json"), `{}`)
	mustWrite(t, filepath.Join(root, "apps", "crm", "xbin.json"), `{"runtime": "go"}`)
	mustWrite(t, filepath.Join(root, "apps", "crm", "index.html"), `<p>crm</p>`)
	mustWrite(t, filepath.Join(root, "apps", "crm", ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWrite(t, filepath.Join(root, "apps", "crm", "admin", "xbin.json"), `{}`)
	before := snapshot(t, root)

	o := newOwners(map[string]string{"apps/crm": "user:ana", "apps/crm/admin": "org:devs"})
	p := bootPlane(t, root, o)
	if d := diffSnapshots(before, snapshot(t, root)); len(d) != 0 {
		t.Fatalf("Boot of a workspace without records changed it: %v", d)
	}

	// The zero state, synthesized.
	f := p.Lookup("apps/crm")
	want := &Record{Tile: "apps/crm", LiveReload: "main", LastLiveReload: "main", Primary: "main",
		Deployments: map[string]*DeploymentRecord{"main": {}}}
	if f.State != RecordNone || f.Err != nil || !reflect.DeepEqual(f.Record, want) {
		t.Errorf("Lookup(zero state) = %+v, record %+v; want RecordNone and %+v", f, f.Record, want)
	}
	if d := diffSnapshots(before, snapshot(t, root)); len(d) != 0 {
		t.Fatalf("a zero-state lookup wrote: %v", d)
	}

	// The first commit: what pausing live reload writes.
	t0 := time.Now().UTC().Add(-time.Second)
	rec, err := p.idx.commit("apps/crm", 0, pinMain(treeA))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	key := util.TileKey("apps/crm")
	if len(key) != 32 {
		t.Fatalf("TileKey = %q", key)
	}
	rel := "data/deployments/" + key + ".json"
	if got, want := diffSnapshots(before, snapshot(t, root)), []string{"+data/deployments/", "+" + rel}; !reflect.DeepEqual(got, want) {
		t.Errorf("the commit changed %v; want exactly %v (nothing in the root xbin.json or the work tree)", got, want)
	}
	fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record file: %v, mode %v; want 0600", err, fi.Mode())
	}
	created, err := time.Parse(time.RFC3339, rec.Created)
	if err != nil || created.Before(t0.Truncate(time.Second)) || created.After(time.Now().Add(time.Second)) {
		t.Errorf("creation stamp %q: %v", rec.Created, err)
	}
	if rec.Schema != 1 || rec.Tile != "apps/crm" || rec.Owner != "user:ana" || rec.Seq != 1 ||
		rec.LiveReload != "" || rec.Primary != "main" || *rec.Deployments["main"].Checkpoint != treeA {
		t.Errorf("committed record = %+v", rec)
	}
	var onDisk map[string]any
	raw, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("record file: %v", err)
	}
	for k, v := range map[string]any{"schema": 1.0, "tile": "apps/crm", "owner": "user:ana", "seq": 1.0, "liveReload": "", "primary": "main"} {
		if onDisk[k] != v {
			t.Errorf("record file %s = %v, want %v", k, onDisk[k], v)
		}
	}

	// A fresh boot reads it back and rewrites nothing.
	withRecord := snapshot(t, root)
	for i := 0; i < 2; i++ {
		p2 := bootPlane(t, root, o)
		if f := p2.Lookup("apps/crm"); f.State != RecordActive || f.Record.Seq != 1 || *f.Record.Deployments["main"].Checkpoint != treeA {
			t.Errorf("boot %d: Lookup = %+v", i, f)
		}
		if d := diffSnapshots(withRecord, snapshot(t, root)); len(d) != 0 {
			t.Errorf("boot %d rewrote: %v", i, d)
		}
	}

	// The nested tile has its own file; the parent's is untouched.
	if _, err := p.idx.commit("apps/crm/admin", 0, pinMain(treeB)); err != nil {
		t.Fatalf("commit nested: %v", err)
	}
	nestedRel := "data/deployments/" + util.TileKey("apps/crm/admin") + ".json"
	if got, want := diffSnapshots(withRecord, snapshot(t, root)), []string{"+" + nestedRel}; !reflect.DeepEqual(got, want) {
		t.Errorf("the nested commit changed %v; want %v", got, want)
	}
	if f := p.Lookup("apps/crm"); *f.Record.Deployments["main"].Checkpoint != treeA {
		t.Errorf("the parent's record moved: %+v", f.Record)
	}

	// Opting out removes the files; the workspace is as it was.
	if err := p.idx.remove("apps/crm/admin", 1); err != nil {
		t.Fatal(err)
	}
	if err := p.idx.remove("apps/crm", 1); err != nil {
		t.Fatal(err)
	}
	if d := diffSnapshots(before, snapshot(t, root)); len(d) != 0 {
		t.Errorf("after removing both records the workspace differs: %v", d)
	}
	if f := p.Lookup("apps/crm"); f.State != RecordNone {
		t.Errorf("after removal Lookup = %+v", f)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
