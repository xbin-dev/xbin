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
	if err := u2.RecordApplied("scaffold:shell", "sha256:nope"); err == nil {
		t.Fatal("stale-hash RecordApplied must refuse")
	}
	if err := u2.RecordApplied("scaffold:shell", prop.ToHash); err != nil {
		t.Fatal(err)
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
