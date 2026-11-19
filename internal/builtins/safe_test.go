package builtins

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// safeFixture installs v1 of a scaffold unit "app" (files a.txt, b.txt,
// gone.txt and a manifest) into a fresh workspace and answers the root and
// an updater over v2.
func safeFixture(t *testing.T, v2 fstest.MapFS) (string, *Updater) {
	t.Helper()
	root := t.TempDir()
	v1 := fstest.MapFS{
		"app/xbin.json": {Data: []byte("{\n  \"title\": \"v1\"\n}\n")},
		"app/a.txt":     {Data: []byte("a1\na2\na3\na4\na5\n")},
		"app/b.txt":     {Data: []byte("b1\nb2\nb3\n")},
		"app/gone.txt":  {Data: []byte("going\n")},
	}
	if _, err := NewUpdater(root, nil, v1).ApplyReplace("scaffold:app"); err != nil {
		t.Fatal(err)
	}
	return root, NewUpdater(root, nil, v2)
}

func v2FS() fstest.MapFS {
	return fstest.MapFS{
		"app/xbin.json": {Data: []byte("{\n  \"title\": \"v2\"\n}\n")},
		"app/a.txt":     {Data: []byte("a1-up\na2\na3\na4\na5\n")}, // upstream changes a's first line
		"app/b.txt":     {Data: []byte("b1\nb2\nb3\n")},
		"app/new.txt":   {Data: []byte("new\n")},
		// gone.txt: removed upstream
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// covers D248 — safe mode never overwrites a local edit: an edit in
// another hunk than upstream's is merged and kept, an edit only here is
// kept, and a conflict — overlapping lines, or upstream removing a file
// edited here — writes nothing at all (ConflictError: the caller files a
// proposal instead).
func TestSafeUpdateNeverOverwritesLocalEdits(t *testing.T) {
	t.Run("clean merge keeps the edits", func(t *testing.T) {
		root, u := safeFixture(t, v2FS())
		app := filepath.Join(root, "app")
		write(t, filepath.Join(app, "a.txt"), "a1\na2\na3\na4\na5-mine\n") // another hunk than upstream's
		write(t, filepath.Join(app, "b.txt"), "b1\nb2-mine\nb3\n")         // only here
		sp, err := u.SafePlan("scaffold:app")
		if err != nil {
			t.Fatal(err)
		}
		if sp.Mode != SafeMerge || len(sp.Conflicts) != 0 || !slices.Equal(sp.Kept, []string{"b.txt"}) {
			t.Fatalf("plan: %+v", sp)
		}
		if !slices.Equal(sp.Changes, []string{"a.txt", "new.txt", "xbin.json"}) && !slices.Equal(sp.Changes, []string{"a.txt", "gone.txt", "new.txt", "xbin.json"}) {
			t.Errorf("changes: %v", sp.Changes)
		}
		if _, how, err := u.ApplySafe("scaffold:app"); err != nil || how != SafeMerge {
			t.Fatalf("apply: %q %v", how, err)
		}
		got := snapshot(t, app)
		if got["a.txt"] != "a1-up\na2\na3\na4\na5-mine\n" || got["b.txt"] != "b1\nb2-mine\nb3\n" || got["new.txt"] != "new\n" {
			t.Fatalf("after a safe update: %q", got)
		}
		if _, ok := got["gone.txt"]; ok {
			t.Error("an unedited file upstream removed is still there")
		}
		if _, err := u.SafePlan("scaffold:app"); !errors.Is(err, ErrUpToDate) {
			t.Errorf("after it: %v", err)
		}
	})
	t.Run("overlapping lines write nothing", func(t *testing.T) {
		root, u := safeFixture(t, v2FS())
		app := filepath.Join(root, "app")
		write(t, filepath.Join(app, "a.txt"), "a1-mine\na2\na3\na4\na5\n") // upstream's very line
		before := snapshot(t, app)
		sp, err := u.SafePlan("scaffold:app")
		if err != nil {
			t.Fatal(err)
		}
		if sp.Mode != SafePropose || !slices.Equal(sp.Conflicts, []string{"a.txt"}) {
			t.Fatalf("plan: %+v", sp)
		}
		_, _, err = u.ApplySafe("scaffold:app")
		var ce *ConflictError
		if !errors.As(err, &ce) || !slices.Equal(ce.Files, []string{"a.txt"}) {
			t.Fatalf("apply: %v", err)
		}
		if after := snapshot(t, app); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("a conflicting safe update wrote:\n%q\n→\n%q", before, after)
		}
	})
	t.Run("an edited file upstream removed writes nothing", func(t *testing.T) {
		root, u := safeFixture(t, v2FS())
		app := filepath.Join(root, "app")
		write(t, filepath.Join(app, "gone.txt"), "going, but I changed it\n")
		before := snapshot(t, app)
		if sp, err := u.SafePlan("scaffold:app"); err != nil || sp.Mode != SafePropose || !slices.Equal(sp.Conflicts, []string{"gone.txt"}) {
			t.Fatalf("plan: %+v %v", sp, err)
		}
		var ce *ConflictError
		if _, _, err := u.ApplySafe("scaffold:app"); !errors.As(err, &ce) {
			t.Fatalf("apply: %v", err)
		}
		if after := snapshot(t, app); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatal("wrote")
		}
	})
	t.Run("adopted", func(t *testing.T) {
		root := t.TempDir()
		app := filepath.Join(root, "app")
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
		v2 := v2FS()
		write(t, filepath.Join(app, "xbin.json"), string(v2["app/xbin.json"].Data)) // identical: it only gains files
		u := NewUpdater(root, nil, v2)
		sp, err := u.SafePlan("scaffold:app")
		if err != nil || sp.Mode != SafeReplace || len(sp.Conflicts) != 0 {
			t.Fatalf("identical adopted: %+v %v", sp, err)
		}
		if _, _, err := u.ApplySafe("scaffold:app"); err != nil {
			t.Fatal(err)
		}
		if got := snapshot(t, app); got["a.txt"] == "" || got["xbin.json"] != string(v2["app/xbin.json"].Data) {
			t.Fatalf("%q", got)
		}
		// an adopted copy that differs: nothing written
		root2 := t.TempDir()
		app2 := filepath.Join(root2, "app")
		_ = os.MkdirAll(app2, 0o755)
		write(t, filepath.Join(app2, "xbin.json"), "{\"title\": \"mine\"}\n")
		u2 := NewUpdater(root2, nil, v2)
		before := snapshot(t, app2)
		if sp, err := u2.SafePlan("scaffold:app"); err != nil || sp.Mode != SafePropose {
			t.Fatalf("a differing adopted copy: %+v %v", sp, err)
		}
		var ce *ConflictError
		if _, _, err := u2.ApplySafe("scaffold:app"); !errors.As(err, &ce) || fmt.Sprint(snapshot(t, app2)) != fmt.Sprint(before) {
			t.Fatalf("apply: %v", err)
		}
	})
}

// covers D248 — what safe refuses: a pinned unit, one with no update, one
// not installed, and a missing essential unit's path that holds something;
// a missing essential unit is installed where nothing is.
func TestSafeUpdateRefusals(t *testing.T) {
	root, u := safeFixture(t, v2FS())
	if err := u.Pin("scaffold:app", true); err != nil {
		t.Fatal(err)
	}
	if _, err := u.SafePlan("scaffold:app"); !errors.Is(err, ErrPinned) {
		t.Fatalf("pinned: %v", err)
	}
	if _, _, err := u.ApplySafe("scaffold:app"); !errors.Is(err, ErrPinned) {
		t.Fatalf("pinned apply: %v", err)
	}
	_ = u.Pin("scaffold:app", false)
	if _, err := u.SafePlan("scaffold:nope"); err == nil {
		t.Fatal("an unknown unit")
	}
	// a missing essential unit
	ess := fstest.MapFS{"tiles/organisations/index.html": {Data: []byte("<p>orgs</p>\n")}}
	u3 := NewUpdater(root, nil, ess)
	sp, err := u3.SafePlan("scaffold:tiles/organisations")
	if err != nil || sp.Mode != SafeInstall || !slices.Equal(sp.Changes, []string{"index.html"}) {
		t.Fatalf("missing: %+v %v", sp, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tiles", "organisations"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "tiles", "organisations", "notes.md"), "mine\n")
	if _, err := u3.SafePlan("scaffold:tiles/organisations"); !errors.Is(err, ErrOccupied) {
		t.Fatalf("occupied: %v", err)
	}
	_ = os.Remove(filepath.Join(root, "tiles", "organisations", "notes.md"))
	if _, how, err := u3.ApplySafe("scaffold:tiles/organisations"); err != nil || how != SafeInstall {
		t.Fatalf("install: %q %v", how, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "tiles", "organisations", "index.html")); string(b) != "<p>orgs</p>\n" {
		t.Fatalf("installed: %q", b)
	}
	// a non-essential unit that isn't installed
	u4 := NewUpdater(root, nil, fstest.MapFS{"apps/other/index.html": {Data: []byte("x\n")}})
	if _, err := u4.SafePlan("scaffold:apps/other"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("not installed: %v", err)
	}
}

// covers D248 — the pin moves with any installed file the update concerns:
// a line described before an edit is stale after it.
func TestSafePinFollowsTheInstalledFiles(t *testing.T) {
	root, u := safeFixture(t, v2FS())
	a, err := u.SafePlan("scaffold:app")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := u.SafePlan("scaffold:app")
	if a.Pin != b.Pin {
		t.Fatal("the pin of the same state differs")
	}
	write(t, filepath.Join(root, "app", "b.txt"), "b1\nb2\nb3\nb4\n")
	c, _ := u.SafePlan("scaffold:app")
	if c.Pin == a.Pin {
		t.Fatal("an edit didn't move the pin")
	}
}

// covers D248 — the property: over random edits on both sides, a safe
// update either writes nothing (a conflict) or leaves every file as a clean
// 3-way merge of the installed copy, its base and upstream — so whatever
// was edited here is still there.
func TestSafeUpdateProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(248))
	lines := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s%d", prefix, i)
		}
		return out
	}
	edit := func(ls []string, tag string) []string {
		out := slices.Clone(ls)
		for k := rng.Intn(3); k > 0; k-- {
			i := rng.Intn(len(out))
			out[i] = out[i] + "-" + tag
		}
		return out
	}
	join := func(ls []string) string { return strings.Join(ls, "\n") + "\n" }
	conflicts, merged := 0, 0
	for iter := 0; iter < 40; iter++ {
		base := lines("l", 8)
		up := edit(base, "up")
		v1 := fstest.MapFS{"app/f.txt": {Data: []byte(join(base))}, "app/index.html": {Data: []byte("<p>1</p>\n")}}
		v2 := fstest.MapFS{"app/f.txt": {Data: []byte(join(up))}, "app/index.html": {Data: []byte("<p>2</p>\n")}}
		root := t.TempDir()
		if _, err := NewUpdater(root, nil, v1).ApplyReplace("scaffold:app"); err != nil {
			t.Fatal(err)
		}
		mine := edit(base, "mine")
		f := filepath.Join(root, "app", "f.txt")
		write(t, f, join(mine))
		before := snapshot(t, filepath.Join(root, "app"))
		u := NewUpdater(root, nil, v2)
		_, _, err := u.ApplySafe("scaffold:app")
		after := snapshot(t, filepath.Join(root, "app"))
		var ce *ConflictError
		switch {
		case errors.As(err, &ce):
			if fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("iteration %d: a conflict wrote", iter)
			}
			conflicts++
		case err != nil:
			t.Fatal(err)
		default:
			want, conflicted, err := mergeFileC([]byte(join(mine)), []byte(join(base)), []byte(join(up)))
			if err != nil || conflicted || !bytes.Equal([]byte(after["f.txt"]), want) {
				t.Fatalf("iteration %d: not a clean merge (%v %v):\n%s", iter, conflicted, err, after["f.txt"])
			}
			for _, l := range mine {
				if strings.HasSuffix(l, "-mine") && !strings.Contains(after["f.txt"], l) {
					t.Fatalf("iteration %d: the local edit %q is gone", iter, l)
				}
			}
			merged++
		}
	}
	if conflicts == 0 || merged == 0 {
		t.Fatalf("the generator reached %d conflicts and %d clean merges: both are wanted", conflicts, merged)
	}
}
