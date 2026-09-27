package sbx

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAddRemove(t *testing.T) {
	r := New()
	rm := r.Add(Entry{ID: "a", Kind: Terminal, Tile: "apps/x", Mode: Namespace})
	if _, ok := r.Get("a"); !ok {
		t.Fatal("added entry missing")
	}
	rm()
	rm() // idempotent
	if _, ok := r.Get("a"); ok {
		t.Fatal("removed entry still listed")
	}
	// a re-add under the same id survives the old remover
	old := r.Add(Entry{ID: "b"})
	old()
	fresh := r.Add(Entry{ID: "b", Label: "new"})
	old()
	if e, ok := r.Get("b"); !ok || e.Label != "new" {
		t.Fatalf("an old remover removed the re-added entry: %+v %v", e, ok)
	}
	fresh()
}

func TestListFilterSortCopies(t *testing.T) {
	r := New()
	t0 := time.Unix(1000, 0)
	r.Add(Entry{ID: "t2", Kind: Terminal, Tile: "apps/b", User: "alice", Started: t0.Add(2 * time.Second)})
	r.Add(Entry{ID: "t1", Kind: Terminal, Tile: "apps/b", User: "bob", Started: t0.Add(time.Second)})
	r.Add(Entry{ID: "b1", Kind: Backend, Tile: "apps/a", Started: t0.Add(5 * time.Second)})
	var ids []string
	for _, e := range r.List(Filter{}) {
		ids = append(ids, e.ID)
	}
	if got := strings.Join(ids, ","); got != "b1,t1,t2" {
		t.Fatalf("order: %s", got)
	}
	if n := len(r.List(Filter{Kind: Terminal, User: "alice"})); n != 1 {
		t.Fatalf("filter: %d", n)
	}
	l := r.List(Filter{Tile: "apps/a"})
	l[0].Label = "changed"
	if e, _ := r.Get("b1"); e.Label != "" {
		t.Fatal("List returned the registry's own entry")
	}
	r.Set("b1", func(e *Entry) { e.PID = 42 })
	if e, _ := r.Get("b1"); e.PID != 42 {
		t.Fatal("Set")
	}
}

func TestConcurrent(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rm := r.Add(Entry{ID: fmt.Sprint(i)})
			r.List(Filter{})
			r.Fail(Failure{Kind: Terminal, Stage: Start, Error: fmt.Sprint(i % 3)})
			rm()
		}(i)
	}
	wg.Wait()
	if n := len(r.List(Filter{})); n != 0 {
		t.Fatalf("%d left", n)
	}
}

func TestNil(t *testing.T) {
	var r *Registry
	r.Add(Entry{ID: "x"})()
	r.Set("x", func(*Entry) {})
	r.Fail(Failure{})
	if r.List(Filter{}) != nil || r.Failures(Filter{}) != nil || len(r.FailureCounts()) != 0 {
		t.Fatal("nil registry lists something")
	}
	if _, ok := r.Get("x"); ok {
		t.Fatal("nil Get")
	}
}

func TestFailureRing(t *testing.T) {
	r := New()
	now := time.Unix(10000, 0)
	r.now = func() time.Time { return now }
	f := Failure{Kind: Backend, Tile: "apps/x", Mode: VM, Stage: Refused, Error: "budget spent"}
	r.Fail(f)
	now = now.Add(time.Minute)
	r.Fail(f)
	got := r.Failures(Filter{})
	if len(got) != 1 || got[0].Count != 2 || !got[0].Time.Equal(now) {
		t.Fatalf("coalesced: %+v", got)
	}
	now = now.Add(11 * time.Minute) // outside the window: a new row
	r.Fail(f)
	if got := r.Failures(Filter{}); len(got) != 2 || got[0].Count != 1 {
		t.Fatalf("after the window: %+v", got)
	}
	for i := 0; i < 100; i++ {
		r.Fail(Failure{Kind: Terminal, Tile: "apps/y", Stage: Start, Error: fmt.Sprint(i)})
	}
	got = r.Failures(Filter{})
	if len(got) != ringSize || got[0].Error != "99" {
		t.Fatalf("ring: %d newest %q", len(got), got[0].Error)
	}
	if c := r.FailureCounts(); c[Refused] != 3 || c[Start] != 100 {
		t.Fatalf("counts: %v", c)
	}
	if n := len(r.Failures(Filter{Tile: "apps/x"})); n != 0 {
		t.Fatalf("pushed out: %d", n)
	}
	r.Fail(Failure{Error: strings.Repeat("x", 5000)})
	if l := len(r.Failures(Filter{})[0].Error); l > errMaxLen+4 {
		t.Fatalf("error not clipped: %d", l)
	}
}

func TestRefuse(t *testing.T) {
	base := fmt.Errorf("wrap: %w", fs.ErrPermission)
	e := Refuse(base)
	if e.Error() != base.Error() || !errors.Is(e, ErrRefused) || !errors.Is(e, fs.ErrPermission) {
		t.Fatalf("refusal: %v", e)
	}
	if Refuse(nil) != nil || Refuse(e) != e {
		t.Fatal("Refuse(nil) / double wrap")
	}
	if StageOf(e, Start) != Refused || StageOf(base, Start) != Start {
		t.Fatal("StageOf")
	}
}
