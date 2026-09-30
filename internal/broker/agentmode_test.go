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
// of today's template. The served repo never changes the block (an instance
// never carries it: templaterepo_block.go), so that merge is clean — for a
// repo an older xbind wrote with the block in it, and for one this xbind
// wrote without — keeps the instance's mode, brings no "template" key, and
// the snapshot says, as information, that the block changed.
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
	with := func(tfs fstest.MapFS, doc string) fstest.MapFS {
		out := fstest.MapFS{}
		for p, f := range tfs {
			out[p] = f
		}
		out["agent/xbin.json"] = &fstest.MapFile{Data: []byte(doc)}
		return out
	}
	// the block before the default: its last key was defaultName
	old := strings.Replace(strings.Replace(manifest, line, "\n", 1), `"defaultName": "agent",`, `"defaultName": "agent"`, 1)
	if _, err := builtins.RenderTree(fstest.MapFS{"agent/xbin.json": {Data: []byte(old)}}, "agent", "apps/x", "apps/agent", nil); err != nil ||
		strings.Contains(old, `"partition": [`) {
		t.Fatalf("couldn't take the default out of the block (%v):\n%s", err, old)
	}
	v1 := with(v2, old)
	// …and a later one whose default narrows: only the block changes again
	v3 := with(v2, strings.Replace(manifest, line, "\n    \"partition\": [\"user\"]\n", 1))

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
	// merge is an instance's `git fetch template && git merge template/main`:
	// clean, its mode as it was, no template block, and the snapshot's
	// message saying the block changed.
	merge := func(inst, tile, mode string) {
		t.Helper()
		mustGit(t, inst, "fetch", "-q", tpl, "main")
		if out, err := runGitIn(inst, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-edit", "FETCH_HEAD"); err != nil {
			t.Fatalf("%s merging the template conflicts (%v): %s\n%s", tile, err, out, mustGit(t, inst, "diff"))
		}
		if got := instancePartitionOf(t, root, tile); got != mode {
			t.Errorf("%s after the merge: partition %q, want %q", tile, got, mode)
		}
		b, _ := os.ReadFile(filepath.Join(inst, "xbin.json"))
		if _, has, err := jsonc.TopLevel(b, "template"); err != nil || has {
			t.Errorf("%s after the merge carries a template block (%v):\n%s", tile, err, b)
		}
		if log := mustGit(t, inst, "log", "-1", "--format=%B", "FETCH_HEAD"); !strings.Contains(log, `"template" block changed`) {
			t.Errorf("the snapshot doesn't say the block changed: %q", log)
		}
	}

	// A repo an older xbind wrote carries the block as it was (the files
	// mirrored as they are, one commit a version); an instance from before
	// the default…
	if err := fs.WalkDir(v1, "agent", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out := filepath.Join(tpl, strings.TrimPrefix(p, "agent/"))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, v1[p].Data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	mustGit(t, tpl, "init", "-q", "-b", "main")
	mustGit(t, tpl, "add", "-A")
	mustGit(t, tpl, "-c", "user.email=xbin@localhost", "-c", "user.name=xbin", "commit", "-q", "-m", "template snapshot")
	inst := instantiate(v1, "apps/old-agent", builtins.InstanceOpts{Partition: true})
	if got := instancePartitionOf(t, root, "apps/old-agent"); got != "" {
		t.Fatalf("an instance of the template without the default: partition %s", got)
	}
	// …merges today's template: the repo keeps the block it carries
	b.MaterializeTemplateRepos(v2)
	if kept, _ := os.ReadFile(filepath.Join(tpl, "xbin.json")); string(kept) != old {
		t.Fatalf("the repo an older xbind wrote changed its block:\n%s", kept)
	}
	merge(inst, "apps/old-agent", "")
	// the note is said once: another start commits nothing
	head := mustGit(t, tpl, "rev-parse", "main")
	b.MaterializeTemplateRepos(v2)
	if again := mustGit(t, tpl, "rev-parse", "main"); again != head {
		t.Errorf("a start with the same template made another snapshot")
	}

	// A repo this xbind writes first carries no block at all; a partitioned
	// instance of today's template keeps its mode through the next change
	if err := os.RemoveAll(tpl); err != nil {
		t.Fatal(err)
	}
	if err := materializeTemplateRepo(root, v2, "agent"); err != nil {
		t.Fatal(err)
	}
	carried, _ := os.ReadFile(filepath.Join(tpl, "xbin.json"))
	if _, has, err := jsonc.TopLevel(carried, "template"); err != nil || has ||
		!strings.Contains(string(carried), `"partitionMail"`) || !strings.Contains(string(carried), "// Where xbind rings") {
		t.Fatalf("a new repo's manifest (no block, everything else as written):\n%s", carried)
	}
	part := instantiate(v2, "apps/new-agent", builtins.InstanceOpts{Partition: true})
	if got := instancePartitionOf(t, root, "apps/new-agent"); got != `["user","global"]` {
		t.Fatalf("a new instance: partition %q", got)
	}
	b.MaterializeTemplateRepos(v3)
	merge(part, "apps/new-agent", `["user","global"]`)

	// a new instance of today's template starts partitioned, unless opted out
	instantiate(v2, "apps/plain-agent", builtins.InstanceOpts{})
	if got := instancePartitionOf(t, root, "apps/plain-agent"); got != "" {
		t.Fatalf("an opted-out instance: partition %q", got)
	}
}
