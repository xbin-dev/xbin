//go:build integration

// End-to-end test of cross-tile change proposals (plans/code-prs.md, D48):
// the author-side flow a terminal agent runs (clone the target's repo,
// commit, format-patch, file via /code/prs), the target-side flow (list,
// read the series, `git am --3way` into the real component dir, close
// merged), and the store bookkeeping in between (summary counts, comments,
// no double-close).
package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir,
		"-c", "user.email=agent-a@apps-author", "-c", "user.name=Agent A",
		"-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestCodePRFlow(t *testing.T) {
	// A static target component; xbind serves it once scanned.
	write(t, "apps/prtarget/index.html", "<!doctype html>\n<html><body>v1</body></html>\n")
	if !waitFor(func() bool { c, _ := get(t, "/api/xbin/components/apps/prtarget"); return c == 200 }, 10*time.Second) {
		t.Fatal("target component never scanned")
	}
	// Its own git repo (EnsureComponentRepos runs at startup/import; this
	// component appeared later, so init like the broker does).
	target := filepath.Join(ws, "apps", "prtarget")
	gitIn(t, target, "init", "-q", "-b", "main")
	gitIn(t, target, "add", "-A")
	gitIn(t, target, "commit", "-q", "-m", "initial commit")
	baseRev := strings.TrimSpace(gitIn(t, target, "rev-parse", "HEAD"))

	// --- author side: clone out of the (conceptually RO) tree, commit, format-patch.
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, filepath.Dir(clone), "clone", "-q", "--no-hardlinks", target, clone)
	if err := os.WriteFile(filepath.Join(clone, "index.html"),
		[]byte("<!doctype html>\n<html><body>v2 — proposed</body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "commit", "-aqm", "bump body to v2")
	series := gitIn(t, clone, "format-patch", "--stdout", "origin/main..HEAD")
	if !strings.Contains(series, "diff --git") {
		t.Fatalf("format-patch produced no diff:\n%s", series)
	}

	open, _ := json.Marshal(map[string]any{
		"target": "apps/prtarget", "title": "bump body to v2",
		"message": "tested by reading it very hard", "base": baseRev, "series": series,
	})
	c, body := req(t, "POST", "/api/xbin/code/prs", string(open))
	if c != 200 || !strings.Contains(body, `"number": 1`) && !strings.Contains(body, `"number":1`) {
		t.Fatalf("open PR: %d %s", c, body)
	}

	// Listed open; counted in the shell-badge summary.
	if _, body = get(t, "/api/xbin/code/prs?target=apps/prtarget&state=open"); !strings.Contains(body, "bump body to v2") {
		t.Fatalf("list: %s", body)
	}
	if _, body = get(t, "/api/xbin/code/prs/summary"); !strings.Contains(body, `"apps/prtarget":1`) {
		t.Fatalf("summary: %s", body)
	}
	// The series round-trips byte-exact.
	if _, got := get(t, "/api/xbin/code/pr/series?target=apps/prtarget&n=1"); got != series {
		t.Fatalf("series not byte-exact (%d vs %d bytes)", len(got), len(series))
	}

	// A review comment lands in the thread.
	if c, body = req(t, "POST", "/api/xbin/code/pr/comment",
		`{"target":"apps/prtarget","n":1,"body":"looks fine — applying"}`); c != 200 {
		t.Fatalf("comment: %d %s", c, body)
	}

	// --- target side: apply the fetched series in the REAL component dir.
	mbox := filepath.Join(t.TempDir(), "1.mbox")
	_, got := get(t, "/api/xbin/code/pr/series?target=apps/prtarget&n=1")
	if err := os.WriteFile(mbox, []byte(got), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, target, "am", "--3way", mbox)
	if html, _ := os.ReadFile(filepath.Join(target, "index.html")); !strings.Contains(string(html), "v2 — proposed") {
		t.Fatalf("patch not applied: %s", html)
	}
	// git am preserved the proposing agent's authorship — the audit trail.
	if log := gitIn(t, target, "log", "-1", "--pretty=%an <%ae> %s"); !strings.Contains(log, "Agent A") {
		t.Fatalf("authorship lost: %s", log)
	}

	applied := strings.TrimSpace(gitIn(t, target, "rev-parse", "--short", "HEAD"))
	if c, body = req(t, "POST", "/api/xbin/code/pr/state",
		fmt.Sprintf(`{"target":"apps/prtarget","n":1,"state":"merged","comment":"applied as %s"}`, applied)); c != 200 || !strings.Contains(body, `"merged"`) {
		t.Fatalf("close merged: %d %s", c, body)
	}
	// Closed PRs leave the badge summary; a second close 409s.
	if _, body = get(t, "/api/xbin/code/prs/summary"); strings.Contains(body, "prtarget") {
		t.Fatalf("summary still counts closed PR: %s", body)
	}
	if c, _ = req(t, "POST", "/api/xbin/code/pr/state",
		`{"target":"apps/prtarget","n":1,"state":"rejected"}`); c != 409 {
		t.Fatalf("double close: want 409, got %d", c)
	}
	// The full thread survives: comment + state change, with the sha.
	if _, body = get(t, "/api/xbin/code/pr?target=apps/prtarget&n=1"); !strings.Contains(body, "looks fine") || !strings.Contains(body, applied) {
		t.Fatalf("thread: %s", body)
	}
}
