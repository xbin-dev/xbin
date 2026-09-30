package broker

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/jsonc"
)

// TestAgentTemplateDefaultKeepsExistingMode holds the builtin agent
// template's partition default (its "template": {"partition": [...]}) to new
// instances only, with the template as shipped: a new instance carries
// ["user","global"] (none when opted out), and an instance made from the
// template before it had the default — v1, the same files without the
// block's line — never gains a partition from a `git merge template/main`
// of today's template: the merge either leaves its manifest alone or
// conflicts with the instance's side keeping its (absent) value.
func TestAgentTemplateDefaultKeepsExistingMode(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	v2 := fstest.MapFS{}
	src := os.DirFS(filepath.Join("..", "..", "builtin-templates"))
	err := fs.WalkDir(src, "agent", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(p, "/test/") || strings.Contains(p, "/node_modules/") {
			return err
		}
		data, err := fs.ReadFile(src, p)
		v2[p] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(v2["agent/xbin.json"].Data)
	const line = "\n    \"partition\": [\"user\", \"global\"]\n"
	if !strings.Contains(manifest, line) {
		t.Fatalf("the agent template's block no longer ends with %q:\n%s", line, manifest)
	}
	raw, has, err := jsonc.TopLevel([]byte(manifest), "partition")
	if err != nil || has {
		t.Fatalf("the agent template carries a top-level partition %s (%v): the default lives in its block", raw, err)
	}
	v1 := fstest.MapFS{}
	for p, f := range v2 {
		v1[p] = f
	}
	// the block before the default: its last key was defaultName
	old := strings.Replace(strings.Replace(manifest, line, "\n", 1), `"defaultName": "agent",`, `"defaultName": "agent"`, 1)
	if _, err := builtins.RenderTree(fstest.MapFS{"agent/xbin.json": {Data: []byte(old)}}, "agent", "apps/x", "apps/agent", nil); err != nil ||
		strings.Contains(old, `"partition": [`) {
		t.Fatalf("couldn't take the default out of the block (%v):\n%s", err, old)
	}
	v1["agent/xbin.json"] = &fstest.MapFile{Data: []byte(old)}

	tpl := filepath.Join(templateReposDir(root), "agent")
	instantiate := func(tfs fstest.MapFS, tile string, opts builtins.InstanceOpts) string {
		t.Helper()
		files, err := builtins.RenderTree(tfs, "agent", tile, "apps/agent", &opts)
		if err != nil {
			t.Fatal(err)
		}
		inst := filepath.Join(root, filepath.FromSlash(tile))
		if _, err := builtins.WriteTree(inst, tile, files); err != nil {
			t.Fatal(err)
		}
		if err := b.SeedInstanceRepo(inst, "agent"); err != nil {
			t.Fatal(err)
		}
		return inst
	}

	// an instance from before the default…
	b.MaterializeTemplateRepos(v1)
	inst := instantiate(v1, "apps/old-agent", builtins.InstanceOpts{Partition: true})
	if got := instancePartitionOf(t, root, "apps/old-agent"); got != "" {
		t.Fatalf("an instance of the template without the default: partition %s", got)
	}
	// …merges today's template
	b.MaterializeTemplateRepos(v2)
	mustGit(t, inst, "fetch", "-q", tpl, "main")
	if _, merr := runGitIn(inst, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-edit", "FETCH_HEAD"); merr == nil {
		if got := instancePartitionOf(t, root, "apps/old-agent"); got != "" {
			t.Fatalf("a clean merge of today's template gave an existing instance partition %s", got)
		}
		t.Log("the merge was clean; the instance's manifest has no partition")
	} else {
		t.Logf("the merge conflicts (%v): the instance's side must keep its absent partition", merr)
		ours := mustGit(t, inst, "show", ":2:xbin.json")
		theirs := mustGit(t, inst, "show", ":3:xbin.json")
		for side, doc := range map[string]string{"ours": ours, "theirs": theirs} {
			raw, has, err := jsonc.TopLevel([]byte(doc), "partition")
			if err != nil || has {
				t.Errorf("the merge's %s side carries a top-level partition %s (%v)", side, raw, err)
			}
		}
		mustGit(t, inst, "merge", "--abort")
	}

	// a new instance of today's template starts partitioned, unless opted out
	instantiate(v2, "apps/new-agent", builtins.InstanceOpts{Partition: true})
	if got := instancePartitionOf(t, root, "apps/new-agent"); got != `["user","global"]` {
		t.Fatalf("a new instance: partition %q", got)
	}
	instantiate(v2, "apps/plain-agent", builtins.InstanceOpts{})
	if got := instancePartitionOf(t, root, "apps/plain-agent"); got != "" {
		t.Fatalf("an opted-out instance: partition %q", got)
	}
}
