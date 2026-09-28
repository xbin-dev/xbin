package deployments

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/runner"
)

// covers D119i T11 NP-06-11 NP-12-9 — a record belongs to the tile it was made
// for. One under a tile's key that names another path is ignored (inert: the
// tile answers the zero state), and the path it names never inherits it. One
// whose owner ref isn't the tile's current one is ignored too, compared on
// every read, so an owner change the record didn't follow makes it inert at
// once, and a change back makes it apply again. An inert record takes no
// commit (an admin clears it). A transfer that rewrites the record before the
// owner store moves keeps it applying throughout and writes the new owner ref
// with the next seq, everything else kept; a rewrite never adopts an inert
// record. The creation stamp is kept, not compared (16-open-questions Q1). A
// tile re-created at the path starts in the zero state: the reset drops the
// record (whatever it holds) and the view repository, keeps the checkpoint
// store as a leftover, and the next opt-in writes a fresh record bound to the
// new tile.
func TestRecordTilePathMismatchRefused(t *testing.T) {
	root := t.TempDir()
	o := newOwners(map[string]string{
		"apps/crm": "user:ana", "apps/other": "user:ana", "apps/shop": "user:ana",
		"apps/notes": "user:ana", "apps/old": "user:ana", "apps/bad": "user:ana",
	})
	// A record under apps/crm's key that names apps/other.
	writeRecordFile(t, root, "apps/crm", mustJSON(t, recordDoc("apps/other", "user:ana")))
	// A record made for apps/shop's previous owner.
	writeRecordDoc(t, root, "apps/shop", recordDoc("apps/shop", "user:bob"))
	// A record bound to apps/notes, with fields this xbind doesn't know.
	notes := recordDoc("apps/notes", "user:ana")
	notes["futureKey"] = []any{"kept"}
	deps(notes)["main"].(map[string]any)["futureDep"] = true
	writeRecordDoc(t, root, "apps/notes", notes)
	// A record with a years-old creation stamp: not compared.
	old := recordDoc("apps/old", "user:ana")
	old["created"] = "2019-01-01T00:00:00Z"
	writeRecordDoc(t, root, "apps/old", old)
	// A held record, for the reset.
	bad := recordDoc("apps/bad", "user:ana")
	bad["seq"] = 0
	writeRecordDoc(t, root, "apps/bad", bad)

	p := bootPlane(t, root, o)

	zeroState := func(t *testing.T, tile string) {
		t.Helper()
		if code, err := p.CodeFor(tile, "main"); err != nil || code != (runner.Code{WorkTree: true}) {
			t.Errorf("%s CodeFor = %+v, %v; want the work tree", tile, code, err)
		}
		if dep, attached := p.LiveReload(tile); dep != "main" || !attached {
			t.Errorf("%s LiveReload = %q, %v", tile, dep, attached)
		}
		if pc, ok := p.PinnedPrimary(tile); pc != nil || ok {
			t.Errorf("%s PinnedPrimary = %+v, %v", tile, pc, ok)
		}
		if p.HasRecord(tile) || p.HasDeployment(tile, "dev") || p.Primary(tile) != "main" {
			t.Errorf("%s: not the zero state", tile)
		}
	}
	inert := func(t *testing.T, tile, why string) {
		t.Helper()
		f := p.Lookup(tile)
		if f.State != RecordInert || !errors.Is(f.Err, ErrRecordInert) || !strings.Contains(f.Err.Error(), why) ||
			!reflect.DeepEqual(f.Record, ZeroRecord(tile)) {
			t.Errorf("%s Lookup = %+v (%v); want RecordInert saying %q, with the zero state", tile, f, f.Err, why)
		}
		zeroState(t, tile)
	}
	active := func(t *testing.T, tile string) *Record {
		t.Helper()
		f := p.Lookup(tile)
		if f.State != RecordActive {
			t.Fatalf("%s Lookup = %+v (%v); want RecordActive", tile, f, f.Err)
		}
		if code, err := p.CodeFor(tile, "main"); err != nil || code != (runner.Code{Tree: treeA}) {
			t.Errorf("%s CodeFor = %+v, %v; want its checkpoint", tile, code, err)
		}
		return f.Record
	}
	fileBytes := func(tile string) string {
		b, _ := os.ReadFile(recordPath(root, tile))
		return string(b)
	}

	t.Run("a record naming another tile", func(t *testing.T) {
		inert(t, "apps/crm", "names apps/other")
		if f := p.Lookup("apps/other"); f.State != RecordNone {
			t.Errorf("apps/other inherited a record under another key: %+v", f)
		}
		zeroState(t, "apps/other")
		was := fileBytes("apps/crm")
		if _, err := p.idx.commit("apps/crm", -1, pinMain(treeB)); !errors.Is(err, ErrRecordInert) {
			t.Errorf("commit over an inert record: %v", err)
		}
		if fileBytes("apps/crm") != was {
			t.Error("a refused commit rewrote the inert record")
		}
	})

	t.Run("a record made for another owner", func(t *testing.T) {
		inert(t, "apps/shop", `owner "user:bob"`)
		was := fileBytes("apps/shop")
		if _, err := p.idx.commit("apps/shop", -1, pinMain(treeB)); !errors.Is(err, ErrRecordInert) {
			t.Errorf("commit over an inert record: %v", err)
		}
		if err := p.RewriteDeploymentOwner("apps/shop", "org:devs"); err != nil {
			t.Fatal(err)
		}
		if fileBytes("apps/shop") != was {
			t.Error("a transfer adopted an inert record")
		}
		inert(t, "apps/shop", `owner "user:bob"`)
	})

	t.Run("the creation stamp isn't compared", func(t *testing.T) {
		if r := active(t, "apps/old"); r.Created != "2019-01-01T00:00:00Z" {
			t.Errorf("created = %q", r.Created)
		}
	})

	t.Run("an owner change the record didn't follow", func(t *testing.T) {
		active(t, "apps/notes")
		o.set("apps/notes", "org:devs")
		inert(t, "apps/notes", `made for owner "user:ana"`)
		o.set("apps/notes", "user:ana")
		active(t, "apps/notes")
	})

	t.Run("a transfer", func(t *testing.T) {
		before := active(t, "apps/notes")
		if err := p.RewriteDeploymentOwner("apps/notes", "org:devs"); err != nil {
			t.Fatalf("RewriteDeploymentOwner: %v", err)
		}
		// The owner store hasn't moved yet: the record still applies.
		r := active(t, "apps/notes")
		if r.Owner != "org:devs" || r.Seq != before.Seq+1 || r.Created != before.Created {
			t.Errorf("rewritten record = %+v", r)
		}
		var disk map[string]any
		if err := json.Unmarshal([]byte(fileBytes("apps/notes")), &disk); err != nil {
			t.Fatal(err)
		}
		if disk["owner"] != "org:devs" || disk["seq"] != float64(before.Seq+1) ||
			!reflect.DeepEqual(disk["futureKey"], []any{"kept"}) ||
			deps(disk)["main"].(map[string]any)["futureDep"] != true {
			t.Errorf("record file after the rewrite = %v", disk)
		}
		o.set("apps/notes", "org:devs")
		active(t, "apps/notes")
		// Settled: the former owner no longer matches.
		o.set("apps/notes", "user:ana")
		inert(t, "apps/notes", `made for owner "org:devs"`)
		o.set("apps/notes", "org:devs")
		active(t, "apps/notes")
		// A rewrite to the owner it has, or of a tile without a record,
		// writes nothing.
		was := fileBytes("apps/notes")
		if err := p.RewriteDeploymentOwner("apps/notes", "org:devs"); err != nil || fileBytes("apps/notes") != was {
			t.Errorf("a no-op rewrite: %v", err)
		}
		if err := p.RewriteDeploymentOwner("apps/none", "org:devs"); err != nil || p.idx.fileExists("apps/none") {
			t.Errorf("a rewrite without a record: %v", err)
		}
	})

	t.Run("a tile re-created at the path", func(t *testing.T) {
		for _, tile := range []string{"apps/notes", "apps/crm", "apps/shop", "apps/bad"} {
			store, view := storeDir(root, tile), viewDir(root, tile)
			mustWrite(t, filepath.Join(store, "HEAD"), "ref: refs/heads/deploy/main\n")
			mustWrite(t, filepath.Join(view, "HEAD"), "ref: refs/heads/deploy/main\n")
			if got, want := p.DeploymentLeftovers(tile), []string{"deployment record", "checkpoint store"}; !reflect.DeepEqual(got, want) {
				t.Errorf("%s leftovers = %v, want %v", tile, got, want)
			}
			if err := p.ResetDeploymentState(tile); err != nil {
				t.Fatalf("%s ResetDeploymentState: %v", tile, err)
			}
			if p.idx.fileExists(tile) {
				t.Errorf("%s: the record survived the reset", tile)
			}
			if _, err := os.Lstat(view); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s: the view repository survived the reset: %v", tile, err)
			}
			if _, err := os.Lstat(filepath.Join(store, "HEAD")); err != nil {
				t.Errorf("%s: the reset took the checkpoint store: %v", tile, err)
			}
			if got, want := p.DeploymentLeftovers(tile), []string{"checkpoint store"}; !reflect.DeepEqual(got, want) {
				t.Errorf("%s leftovers after the reset = %v, want %v", tile, got, want)
			}
			if f := p.Lookup(tile); f.State != RecordNone {
				t.Errorf("%s after the reset: %+v", tile, f)
			}
			zeroState(t, tile)
		}
		// The new tile's first opt-in binds a fresh record to it.
		o.set("apps/shop", "org:new")
		r, err := p.idx.commit("apps/shop", 0, pinMain(treeB))
		if err != nil {
			t.Fatalf("commit after the reset: %v", err)
		}
		if r.Seq != 1 || r.Owner != "org:new" || r.Created == "2026-09-27T10:12:03Z" || len(r.Deployments) != 1 || r.extra != nil {
			t.Errorf("the re-created tile's record = %+v", r)
		}
		if got := p.DeploymentLeftovers("apps/none"); got != nil {
			t.Errorf("leftovers of a path with no state = %v", got)
		}
	})
}
