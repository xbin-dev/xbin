package builtins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir,
		"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Propose renders base→theirs as a format-patch series that `git am --3way`
// merges into a customized tile (upstream + user edits in different hunks
// both survive); RecordApplied refreshes provenance only for the exact embed
// the proposal was rendered from.
func TestProposeAppliesAndRecords(t *testing.T) {
	root := t.TempDir()
	appJS := "l1\nl2\nl3\nl4\nl5\n"
	v1 := fstest.MapFS{
		"shell/index.html": {Data: []byte("<html>v1</html>\n")},
		"shell/app.js":     {Data: []byte(appJS)},
	}
	u1 := NewUpdater(root, nil, v1)
	if _, err := u1.ApplyReplace("scaffold:shell"); err != nil {
		t.Fatal(err)
	}
	tile := filepath.Join(root, "shell")
	// The component's own repo with the installed base committed — this is
	// what puts the base blobs in reach of `git am --3way`.
	gitT(t, tile, "init", "-q", "-b", "main")
	gitT(t, tile, "add", "-A")
	gitT(t, tile, "commit", "-qm", "initial commit")
	// User customization: append to app.js (a different hunk than upstream's).
	if err := os.WriteFile(filepath.Join(tile, "app.js"), []byte(appJS+"l6-user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, tile, "commit", "-aqm", "user tweak")

	// New embed: index.html rewritten, app.js first line changed, a new file.
	v2 := fstest.MapFS{
		"shell/index.html": {Data: []byte("<html>v2</html>\n")},
		"shell/app.js":     {Data: []byte("l1-upstream\nl2\nl3\nl4\nl5\n")},
		"shell/new.css":    {Data: []byte("body{}\n")},
	}
	u2 := NewUpdater(root, nil, v2)
	prop, err := u2.Propose("scaffold:shell")
	if err != nil {
		t.Fatal(err)
	}
	if prop.InstallPath != "shell" || !strings.Contains(prop.Series, "diff --git") {
		t.Fatalf("bad proposal: %+v", prop)
	}
	if !strings.Contains(prop.Message, "conflict") && !strings.Contains(prop.Message, "clean") {
		t.Fatalf("message lacks file status table:\n%s", prop.Message)
	}

	mbox := filepath.Join(t.TempDir(), "s.mbox")
	if err := os.WriteFile(mbox, []byte(prop.Series), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, tile, "am", "--3way", mbox)

	if b, _ := os.ReadFile(filepath.Join(tile, "index.html")); string(b) != "<html>v2</html>\n" {
		t.Fatalf("index.html not fast-forwarded: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(tile, "app.js")); string(b) != "l1-upstream\nl2\nl3\nl4\nl5\nl6-user\n" {
		t.Fatalf("3-way merge lost an edit: %q", b)
	}
	if _, err := os.Stat(filepath.Join(tile, "new.css")); err != nil {
		t.Fatal("upstream-added file missing")
	}

	// Wrong hash (embed moved on) refuses; right hash records — and the
	// unit stops being offered (user edits read as user-only now).
	if _, err := u2.RecordApplied("scaffold:shell", "sha256:nope"); err == nil {
		t.Fatal("stale-hash RecordApplied must refuse")
	}
	if pending, err := u2.RecordApplied("scaffold:shell", prop.ToHash); err != nil || len(pending) > 0 {
		t.Fatal(pending, err)
	}
	ups, err := u2.Updates()
	if err != nil {
		t.Fatal(err)
	}
	for _, uu := range ups {
		if uu.ID == "scaffold:shell" {
			t.Fatalf("still offered after RecordApplied: %+v", uu)
		}
	}
	// Nothing pending → nothing to propose.
	if _, err := u2.Propose("scaffold:shell"); err == nil {
		t.Fatal("up-to-date unit must not propose")
	}
}

// Adopted units (no recorded base) propose an ours→theirs diff — the path
// merge-file refuses entirely.
func TestProposeAdopted(t *testing.T) {
	root := t.TempDir()
	tile := filepath.Join(root, "shell")
	if err := os.MkdirAll(tile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tile, "index.html"), []byte("<html>local</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := NewUpdater(root, nil, fstest.MapFS{
		"shell/index.html": {Data: []byte("<html>upstream</html>\n")},
	})
	prop, err := u.Propose("scaffold:shell")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prop.Series, "-<html>local</html>") ||
		!strings.Contains(prop.Series, "+<html>upstream</html>") {
		t.Fatalf("adopted series must diff ours→theirs:\n%s", prop.Series)
	}
	if !strings.Contains(prop.Title, "adopted") {
		t.Fatalf("title should flag the adopted case: %s", prop.Title)
	}
}

// covers the review of 2026-11-20, finding 5 — closing a proposal merged
// records what the tile holds, file by file: a file carrying upstream's
// change (beside edits of its own) is updated; a file still without it
// keeps its recorded base, so the unit stays offered for it until it holds
// the change, and the version doesn't move until then.
func TestRecordAppliedRecordsOnlyWhatTheTileHolds(t *testing.T) {
	root := t.TempDir()
	v1 := fstest.MapFS{
		"shell/index.html": {Data: []byte("<html>v1</html>\n")},
		"shell/app.js":     {Data: []byte("l1\nl2\nl3\nl4\nl5\n")},
	}
	if _, err := NewUpdater(root, nil, v1).ApplyReplace("scaffold:shell"); err != nil {
		t.Fatal(err)
	}
	tile := filepath.Join(root, "shell")
	v2 := fstest.MapFS{
		"shell/index.html": {Data: []byte("<html>v2</html>\n")},
		"shell/app.js":     {Data: []byte("l1-upstream\nl2\nl3\nl4\nl5\n")},
	}
	u := NewUpdater(root, nil, v2)
	prop, err := u.Propose("scaffold:shell")
	if err != nil {
		t.Fatal(err)
	}
	// The tile's agent takes app.js's change beside an edit of its own and
	// keeps its own index.html: closing merged now records app.js only.
	must(t, os.WriteFile(filepath.Join(tile, "app.js"), []byte("l1-upstream\nl2\nl3\nl4\nl5\nl6-mine\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(tile, "index.html"), []byte("<html>mine</html>\n"), 0o644))
	pending, err := u.RecordApplied("scaffold:shell", prop.ToHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0] != "shell/index.html" {
		t.Fatalf("pending: %v", pending)
	}
	uu := unitOf(t, u, "scaffold:shell")
	if uu == nil || uu.FromVersion != 1 || noteOf(uu, "index.html") == "" || noteOf(uu, "app.js") != "user: " {
		t.Fatalf("after a partial merge: %+v", uu)
	}
	// It takes index.html's change too: the update is applied.
	must(t, os.WriteFile(filepath.Join(tile, "index.html"), []byte("<html>v2</html>\n"), 0o644))
	if pending, err := u.RecordApplied("scaffold:shell", prop.ToHash); err != nil || len(pending) > 0 {
		t.Fatal(pending, err)
	}
	if uu := unitOf(t, u, "scaffold:shell"); uu != nil {
		t.Fatalf("still offered once the tile holds the update: %+v", uu)
	}
}

// covers the review of 2026-11-20, finding 5 — a file holds upstream's
// change when it carries it beside an edit of its own, even an edit right
// next to it (where git's merge sees one conflict): upstream's line is
// there, the base's line it replaced is gone. A file that kept the base's
// line, or lacks upstream's, doesn't.
func TestHoldsChangeBesideAnAdjacentEdit(t *testing.T) {
	base := []byte("a\nold\nc\nd\n")
	theirs := []byte("a\nnew\nc\nd\n")
	for name, c := range map[string]struct {
		ours string
		want bool
	}{
		"upstream's file":                {"a\nnew\nc\nd\n", true},
		"the change and a line after":    {"a\nnew\nmine\nc\nd\n", true},
		"the change and an edit far off": {"a\nnew\nc\nd-mine\n", true},
		"the base's line kept":           {"a\nold\nmine\nc\nd\n", false},
		"neither":                        {"a\nmine\nc\nd\n", false},
		"as installed":                   {"a\nold\nc\nd\n", false},
	} {
		if got := holdsChange([]byte(c.ours), base, theirs); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
