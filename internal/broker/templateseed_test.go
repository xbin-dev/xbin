package broker

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Instance repos seeded from the template repo share ancestry with it, so
// `git fetch template && git merge` applies upstream template fixes cleanly;
// /templates/updates spots instances behind the current snapshot and flags
// pre-seeding (unrelated-history) instances as legacy.
func TestTemplateSeedAndUpdates(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root

	baseJS := "// agent\nconst a = 1;\nconst b = 2;\nconst c = 3;\nexport {};\n"
	v1 := fstest.MapFS{
		"agent/index.html": {Data: []byte("<html>agent</html>\n")},
		"agent/agent.js":   {Data: []byte(baseJS)},
	}
	b.MaterializeTemplateRepos(v1)
	tpl := filepath.Join(templateReposDir(root), "agent")

	// "Instantiate": rendered files land in the workspace (CopyTree's job),
	// then the repo is seeded and the remote added, like apiTemplatesNew.
	inst := filepath.Join(root, "apps", "myagent")
	for rel, f := range map[string]string{"index.html": "<html>agent</html>\n", "agent.js": baseJS + "// rewritten-for: apps/myagent\n"} {
		if err := os.MkdirAll(inst, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inst, rel), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.SeedInstanceRepo(inst, "agent"); err != nil {
		t.Fatal(err)
	}
	b.AddTemplateRemote(inst, "agent")
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}

	// Shared ancestry: the template snapshot is an ancestor of the instance.
	tplHead := strings.TrimSpace(mustGit(t, tpl, "rev-parse", "main"))
	mustGit(t, inst, "merge-base", "--is-ancestor", tplHead, "HEAD")
	if log := mustGit(t, inst, "log", "--format=%s", "-1"); !strings.Contains(log, "instantiate agent as apps/myagent") {
		t.Fatalf("top commit should be the instantiate rewrites: %q", log)
	}

	updates := func() string {
		r := httptest.NewRequest("GET", "/templates/updates", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
		w := httptest.NewRecorder()
		b.apiTemplateUpdates(w, r)
		return w.Body.String()
	}
	if body := updates(); strings.Contains(body, "myagent") {
		t.Fatalf("fresh instance must not be flagged: %s", body)
	}

	// Template evolves (a newer xbind rematerializes) → instance is behind,
	// NOT legacy — and the documented merge applies the fix cleanly.
	v2 := fstest.MapFS{
		"agent/index.html": {Data: []byte("<html>agent</html>\n")},
		"agent/agent.js":   {Data: []byte("// v2 with markdown\n" + baseJS)},
	}
	b.MaterializeTemplateRepos(v2)
	body := updates()
	if !strings.Contains(body, `"path":"apps/myagent"`) || !strings.Contains(body, `"legacy":false`) {
		t.Fatalf("behind instance not flagged (or flagged legacy): %s", body)
	}
	mustGit(t, inst, "fetch", "-q", tpl, "main")
	mustGit(t, inst, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-edit", "FETCH_HEAD")
	if data, _ := os.ReadFile(filepath.Join(inst, "agent.js")); !strings.Contains(string(data), "// v2 with markdown") {
		t.Fatalf("merge did not bring the template fix: %q", data)
	}
	if body := updates(); strings.Contains(body, "myagent") {
		t.Fatalf("merged instance still flagged: %s", body)
	}

	// A pre-seeding instance (fresh unrelated root) reads as legacy.
	old := filepath.Join(root, "apps", "oldagent")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "index.html"), []byte("<html>old</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, old, "init", "-q", "-b", "main")
	mustGit(t, old, "add", "-A")
	mustGit(t, old, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "initial commit")
	b.AddTemplateRemote(old, "agent")
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if body := updates(); !strings.Contains(body, `"path":"apps/oldagent"`) || !strings.Contains(body, `"legacy":true`) {
		t.Fatalf("legacy instance not flagged legacy: %s", body)
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGitIn(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}
