package broker

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	xbin "github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/manifestmerge"
)

// agentTemplateFS is the builtin agent template's files (its tests and
// node_modules aside), to hand MaterializeTemplateRepos and RenderTree.
func agentTemplateFS(t *testing.T) fstest.MapFS {
	t.Helper()
	src := xbin.BuiltinTemplatesFS()
	out := fstest.MapFS{}
	if err := fs.WalkDir(src, "agent", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(p, "/test/") || strings.Contains(p, "/node_modules/") {
			return err
		}
		data, err := fs.ReadFile(src, p)
		out[p] = &fstest.MapFile{Data: data}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// covers T1 (review) — a new instance in a workspace an older xbind made:
// the served repository keeps the template block it had, with the header
// comment above it, so the instance's manifest (the block and that comment
// gone, its partition on top) differs from the served one by more than its
// partition line. A later manifest change away from the block still merges
// by lines; one next to it — the header comment reworded — conflicts by
// lines, and the merge by keys (the driver's) takes it: nothing to take,
// the instance's manifest as it was.
func TestNewInstanceManifestInAnOlderRepo(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	v2 := agentTemplateFS(t)
	manifest := string(v2["agent/xbin.json"].Data)

	// the repository an older xbind wrote: the manifest verbatim, block and all
	tpl := filepath.Join(templateReposDir(root), "agent")
	if err := os.MkdirAll(tpl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tpl, "xbin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, tpl, "init", "-q", "-b", "main")
	mustGit(t, tpl, "add", "-A")
	mustGit(t, tpl, "-c", "user.email=xbin@localhost", "-c", "user.name=xbin", "commit", "-q", "-m", "template snapshot")
	b.MaterializeTemplateRepos(v2)
	served, _ := os.ReadFile(filepath.Join(tpl, "xbin.json"))
	if !strings.Contains(string(served), "A TEMPLATE component") || !strings.Contains(string(served), `"template": {`) {
		t.Fatalf("the older repository's manifest lost its block or header:\n%s", served)
	}

	files, err := builtins.RenderTree(v2, "agent", "apps/agent", "apps/agent", &builtins.InstanceOpts{Partition: true})
	if err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(root, "apps", "agent")
	if _, err := builtins.WriteTree(inst, "apps/agent", files); err != nil {
		t.Fatal(err)
	}
	if err := b.SeedInstanceRepo(inst, "agent"); err != nil {
		t.Fatal(err)
	}
	if d := strings.Fields(mustGit(t, inst, "diff", "--numstat", "HEAD~1", "HEAD", "--", "xbin.json")); len(d) < 2 || d[1] == "0" {
		t.Errorf("in an older repository the instance's manifest drops the served block's lines: %q", d)
	}
	mine := string(files["xbin.json"])

	snapshot := func(edit [2]string) {
		t.Helper()
		if !strings.Contains(manifest, edit[0]) {
			t.Fatalf("the agent template's manifest no longer has %q", edit[0])
		}
		manifest = strings.Replace(manifest, edit[0], edit[1], 1)
		v := fstest.MapFS{}
		for p, f := range v2 {
			v[p] = f
		}
		v["agent/xbin.json"] = &fstest.MapFile{Data: []byte(manifest)}
		b.MaterializeTemplateRepos(v)
		mustGit(t, inst, "fetch", "-q", tpl, "main")
	}
	merge := func() (string, error) {
		return runGitIn(inst, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-edit", "FETCH_HEAD")
	}

	// away from the block: git's line merge takes it
	snapshot([2]string{"    { \"target\": \"res:apps/agent/team\", \"role\": \"writer\" },\n",
		"    { \"target\": \"res:apps/agent/team\", \"role\": \"writer\" },\n    { \"target\": \"res:apps/agent/cache\", \"role\": \"writer\" },\n"})
	if out, err := merge(); err != nil {
		t.Fatalf("a change away from the block conflicts (%v): %s\n%s", err, out, mustGit(t, inst, "diff"))
	}
	merged, _ := os.ReadFile(filepath.Join(inst, "xbin.json"))
	if !strings.Contains(string(merged), "res:apps/agent/cache") || !strings.HasPrefix(string(merged), "{\n  \"partition\"") {
		t.Fatalf("merged:\n%s", merged)
	}

	// next to it: the header comment reworded conflicts by lines…
	base, _ := os.ReadFile(filepath.Join(tpl, "xbin.json"))
	snapshot([2]string{"// A TEMPLATE component", "// A TEMPLATE component, reworded"})
	theirs, _ := os.ReadFile(filepath.Join(tpl, "xbin.json"))
	if _, err := merge(); err == nil {
		t.Fatalf("expected git's line merge to conflict next to the dropped block")
	}
	mustGit(t, inst, "merge", "--abort")
	// …and merges by keys: nothing to take, the instance's manifest as it is
	r, err := manifestmerge.Merge(base, merged, theirs, manifestmerge.Options{From: "apps/agent", To: "apps/agent"})
	if err != nil {
		t.Fatalf("by keys: %v", err)
	}
	if string(r.Out) != string(merged) || len(r.Took) != 0 {
		t.Errorf("by keys:\n%s\ntook %v", r.Out, r.Took)
	}
	if !strings.Contains(mine, "// Where xbind rings") {
		t.Errorf("the instance lost the template's comments:\n%s", mine)
	}
}
