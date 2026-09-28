package deployments

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"regexp"
	"testing"
)

// invNames is the deployment names a random change draws from: valid ones,
// main, and two the grammar refuses.
var invNames = []string{"main", "dev", "exp", "qa", "Dev", "x+y"}

// invTrees is the checkpoints a random change draws from: two full ids, a
// short one, and none.
var invTrees = []*string{&treeA, &treeB, strPtr("c:3f2a1c9"), nil}

func strPtr(s string) *string { return &s }

var invNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// checkInvariants is 05-model §4, written out again without validate: the
// record's binding, seq, main and the primary existing, the grammar, live
// reload driving exactly its one unpinned deployment, full ids, states, D127m.
func checkInvariants(r *Record, tile, owner, created string) error {
	switch {
	case r.Schema != 1 || r.Tile != tile || r.Owner != owner || r.Created != created:
		return fmt.Errorf("binding %d %q %q %q", r.Schema, r.Tile, r.Owner, r.Created)
	case r.Seq < 1:
		return fmt.Errorf("seq %d", r.Seq)
	case r.Deployments["main"] == nil:
		return fmt.Errorf("main is gone")
	case r.Deployments[r.Primary] == nil:
		return fmt.Errorf("primary %q is gone", r.Primary)
	case r.LiveReload != "" && r.Deployments[r.LiveReload] == nil:
		return fmt.Errorf("live reload drives %q, which is gone", r.LiveReload)
	case r.ProtectedPrimary && r.LiveReload == r.Primary:
		return fmt.Errorf("live reload drives the protected primary")
	}
	unpinned := 0
	for name, d := range r.Deployments {
		if !invNameRE.MatchString(name) {
			return fmt.Errorf("name %q", name)
		}
		if d.Checkpoint == nil {
			unpinned++
			if name != r.LiveReload {
				return fmt.Errorf("%s is unpinned and not driven", name)
			}
			continue
		}
		if name == r.LiveReload {
			return fmt.Errorf("live reload drives pinned %s", name)
		}
		if c := *d.Checkpoint; c != treeA && c != treeB {
			return fmt.Errorf("%s's checkpoint %q", name, c)
		}
		if d.State != "" && d.State != "failed" {
			return fmt.Errorf("%s's state %q", name, d.State)
		}
	}
	if (r.LiveReload == "") != (unpinned == 0) || unpinned > 1 {
		return fmt.Errorf("%d unpinned deployments, live reload %q", unpinned, r.LiveReload)
	}
	return nil
}

// invChange is one random change: a raw field edit (often breaking an
// invariant), a well-formed operation's edit (attach, pause, add, remove),
// or an attempt on the binding.
func invChange(rng *rand.Rand) (string, func(*Record) error) {
	name := invNames[rng.Intn(len(invNames))]
	tree := invTrees[rng.Intn(len(invTrees))]
	switch rng.Intn(12) {
	case 0:
		return "add " + name, func(r *Record) error {
			if r.Deployments[name] == nil {
				r.Deployments[name] = &DeploymentRecord{Checkpoint: tree, Created: "2026-09-27T10:12:03Z"}
			}
			return nil
		}
	case 1:
		return "remove " + name, func(r *Record) error { delete(r.Deployments, name); return nil }
	case 2:
		return "live reload → " + name, func(r *Record) error { r.LiveReload = name; return nil }
	case 3:
		return "pause", func(r *Record) error { r.LiveReload = ""; return nil }
	case 4:
		return "primary → " + name, func(r *Record) error { r.Primary = name; return nil }
	case 5:
		return "checkpoint of " + name, func(r *Record) error {
			if d := r.Deployments[name]; d != nil {
				d.Checkpoint = tree
			}
			return nil
		}
	case 6:
		return "state of " + name, func(r *Record) error {
			if d := r.Deployments[name]; d != nil {
				d.State = []string{"", "failed", "broken"}[rng.Intn(3)]
			}
			return nil
		}
	case 7:
		return "protect", func(r *Record) error { r.ProtectedPrimary = !r.ProtectedPrimary; return nil }
	case 8: // attach live reload to name, as the operation does: pin the old target
		return "attach " + name, func(r *Record) error {
			d := r.Deployments[name]
			if d == nil {
				return errors.New("no such deployment")
			}
			if old := r.Deployments[r.LiveReload]; old != nil {
				old.Checkpoint = &treeB
			}
			d.Checkpoint, d.State = nil, ""
			r.LiveReload, r.LastLiveReload = name, name
			return nil
		}
	case 9: // pause, as the operation does: pin the target
		return "pause live reload", func(r *Record) error {
			if d := r.Deployments[r.LiveReload]; d != nil {
				d.Checkpoint = &treeA
			}
			r.LiveReload = ""
			return nil
		}
	case 10:
		return "tamper with the binding", func(r *Record) error {
			switch rng.Intn(5) {
			case 0:
				r.Seq += 5
			case 1:
				r.Tile = "apps/other"
			case 2:
				r.Owner = "user:mallory"
			case 3:
				r.Created = "2020-01-01T00:00:00Z"
			default:
				r.Schema = 2
			}
			return nil
		}
	default:
		return "a change that fails", func(r *Record) error {
			r.Deployments["main"].Checkpoint = nil
			r.LiveReload = "main"
			return errors.New("the capture failed")
		}
	}
}

// covers D119d D119f T11 — the record half of 05-model §4 under random change
// sequences through the index's commit: every commit either leaves the file
// and the index exactly as they were (a change breaking an invariant, a
// change that fails, an edit of the binding or seq, a stale compare-and-set)
// or writes the next seq; the invariants hold after every step, checked
// independently of validate (live reload "" or exactly one deployment with a
// null checkpoint, the primary exists, main exists and can't be removed,
// the grammar, full ids, D127m); a fresh load of the file equals the index's
// record; and fields this xbind doesn't know, at the top level and on main,
// survive every rewrite.
func TestRecordInvariantsRandomized(t *testing.T) {
	steps := 3000
	if testing.Short() {
		steps = 300
	}
	const seed = 20260927
	rng := rand.New(rand.NewSource(seed))
	t.Logf("seed %d, %d steps", seed, steps)

	root := t.TempDir()
	const tile, owner = "apps/crm", "user:ana"
	o := newOwners(map[string]string{tile: owner})
	doc := recordDoc(tile, owner)
	doc["futureKey"] = map[string]any{"kept": true}
	deps(doc)["main"].(map[string]any)["futureDep"] = []any{1, 2}
	writeRecordDoc(t, root, tile, doc)
	p := bootPlane(t, root, o)
	start := p.Lookup(tile)
	if start.State != RecordActive {
		t.Fatalf("start: %+v", start)
	}
	created := start.Record.Created

	var ok, refused, stale int
	path := recordPath(root, tile)
	for i := 0; i < steps; i++ {
		desc, change := invChange(rng)
		cur := p.Lookup(tile).Record
		was, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		expect := int64(-1)
		switch rng.Intn(4) {
		case 0:
			expect = cur.Seq
		case 1:
			expect = cur.Seq - 1 // stale
		}
		rec, err := p.idx.commit(tile, expect, change)
		now, _ := os.ReadFile(path)
		got := p.Lookup(tile)
		if err != nil {
			if errors.Is(err, ErrStaleSeq) {
				stale++
				if expect == cur.Seq {
					t.Fatalf("step %d (%s): a current seq lost the compare-and-set: %v", i, desc, err)
				}
			} else {
				refused++
			}
			if !bytes.Equal(now, was) || got.Record != cur {
				t.Fatalf("step %d (%s): a refused commit (%v) changed the record", i, desc, err)
			}
			continue
		}
		ok++
		if expect >= 0 && expect != cur.Seq {
			t.Fatalf("step %d (%s): a stale seq %d won against %d", i, desc, expect, cur.Seq)
		}
		if rec.Seq != cur.Seq+1 || got.State != RecordActive || got.Record != rec {
			t.Fatalf("step %d (%s): seq %d after %d; lookup %+v", i, desc, rec.Seq, cur.Seq, got)
		}
		if err := checkInvariants(rec, tile, owner, created); err != nil {
			t.Fatalf("step %d (%s): committed a record breaking an invariant: %v", i, desc, err)
		}
		for name, d := range cur.Deployments {
			if nd := rec.Deployments[name]; nd != nil && nd.Created != d.Created {
				t.Fatalf("step %d (%s): %s changed identity", i, desc, name)
			}
		}
		var disk map[string]any
		if err := json.Unmarshal(now, &disk); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if !reflect.DeepEqual(disk["futureKey"], map[string]any{"kept": true}) ||
			!reflect.DeepEqual(deps(disk)["main"].(map[string]any)["futureDep"], []any{1.0, 2.0}) {
			t.Fatalf("step %d (%s): a field this xbind doesn't know was lost:\n%s", i, desc, now)
		}
		if i%50 == 0 {
			fresh := bootPlane(t, root, o).Lookup(tile)
			a, _ := fresh.Record.encode()
			b, _ := rec.encode()
			if fresh.State != RecordActive || !bytes.Equal(a, b) {
				t.Fatalf("step %d (%s): a fresh load differs from the index:\n%s\nvs\n%s", i, desc, a, b)
			}
		}
	}
	t.Logf("%d committed, %d refused, %d stale", ok, refused, stale)
	if ok < steps/20 || refused < steps/20 || stale < steps/20 {
		t.Errorf("the sequence exercised too little: %d committed, %d refused, %d stale", ok, refused, stale)
	}
}
