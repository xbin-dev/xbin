package broker

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/registry"
)

// agentDefaultLines are the agent template block's last lines: the mode
// new instances start in, and its update asking every instance for it
// (D177).
const agentDefaultLines = "\n    \"partition\": [\"user\", \"global\"],\n    \"partitionOnUpdate\": true\n"

// agentVersions is the builtin agent template as shipped (v2), the same
// files before its block had the default (v1: its last key defaultName),
// and a later one whose default narrows to ["user"] (v3).
func agentVersions(t *testing.T) (v1, v2, v3 fstest.MapFS, old string) {
	t.Helper()
	v2 = fstest.MapFS{}
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
	if !strings.Contains(manifest, agentDefaultLines) {
		t.Fatalf("the agent template's block no longer ends with %q:\n%s", agentDefaultLines, manifest)
	}
	raw, has, err := jsonc.TopLevel([]byte(manifest), "partition")
	if err != nil || has {
		t.Fatalf("the agent template carries a top-level partition %s (%v): the default lives in its block", raw, err)
	}
	with := func(doc string) fstest.MapFS {
		out := fstest.MapFS{}
		for p, f := range v2 {
			out[p] = f
		}
		out["agent/xbin.json"] = &fstest.MapFile{Data: []byte(doc)}
		return out
	}
	old = strings.Replace(strings.Replace(manifest, agentDefaultLines, "\n", 1), `"defaultName": "agent",`, `"defaultName": "agent"`, 1)
	if _, err := builtins.RenderTree(fstest.MapFS{"agent/xbin.json": {Data: []byte(old)}}, "agent", "apps/x", "apps/agent", nil); err != nil ||
		strings.Contains(old, `"partition": [`) {
		t.Fatalf("couldn't take the default out of the block (%v):\n%s", err, old)
	}
	v1 = with(old)
	v3 = with(strings.Replace(manifest, agentDefaultLines, "\n    \"partition\": [\"user\"],\n    \"partitionOnUpdate\": true\n", 1))
	return v1, v2, v3, old
}

// agentRig is a workspace for the agent template's served repo: instances
// made from a template version, and their `git merge template/main`.
type agentRig struct {
	t    *testing.T
	b    *Broker
	root string
	tpl  string
}

func newAgentRig(t *testing.T) *agentRig {
	b := testBroker(t)
	return &agentRig{t: t, b: b, root: b.Reg.Root, tpl: filepath.Join(templateReposDir(b.Reg.Root), "agent")}
}

func (r *agentRig) instantiate(tfs fstest.MapFS, tile string, opts builtins.InstanceOpts) string {
	r.t.Helper()
	files, err := builtins.RenderTree(tfs, "agent", tile, "apps/agent", &opts)
	if err != nil {
		r.t.Fatal(err)
	}
	inst := filepath.Join(r.root, filepath.FromSlash(tile))
	if _, err := builtins.WriteTree(inst, tile, files); err != nil {
		r.t.Fatal(err)
	}
	if err := r.b.SeedInstanceRepo(inst, "agent"); err != nil {
		r.t.Fatal(err)
	}
	return inst
}

// merge is an instance's `git fetch template && git merge template/main`:
// clean, with the mode it then asks for, and no template block.
func (r *agentRig) merge(inst, tile, mode string) {
	t := r.t
	t.Helper()
	mustGit(t, inst, "fetch", "-q", r.tpl, "main")
	if out, err := runGitIn(inst, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-edit", "FETCH_HEAD"); err != nil {
		t.Fatalf("%s merging the template conflicts (%v): %s\n%s", tile, err, out, mustGit(t, inst, "diff"))
	}
	if got := instancePartitionOf(t, r.root, tile); got != mode {
		t.Errorf("%s after the merge: partition %q, want %q", tile, got, mode)
	}
	b, _ := os.ReadFile(filepath.Join(inst, "xbin.json"))
	if _, has, err := jsonc.TopLevel(b, "template"); err != nil || has {
		t.Errorf("%s after the merge carries a template block (%v):\n%s", tile, err, b)
	}
}

// snapshot is the message of the served repo's last snapshot.
func (r *agentRig) snapshot() string { return mustGit(r.t, r.tpl, "log", "-1", "--format=%B", "main") }

// served is the top-level partition the served repo's manifest carries.
func (r *agentRig) served() string {
	b, _ := os.ReadFile(filepath.Join(r.tpl, "xbin.json"))
	return servedPartition(b)
}

// TestAgentTemplateUpdateWithoutIsolation holds the builtin agent
// template's partition default (its "template": {"partition": [...]}) to new
// instances only on an xbind without --isolate (D177: no person's partition
// can run there, so no update asks for one): a new instance carries
// ["user","global"] (none when opted out), and an instance made from the
// template before it had the default — v1, the same files without the
// block's lines — never gains a partition from a `git merge
// template/main` of today's template. The served repo never changes the
// block (an instance never carries it: templaterepo_block.go), so that
// merge is clean — for a repo an older xbind wrote with the block in it,
// and for one this xbind wrote without — keeps the instance's mode, brings
// no "template" key, and the snapshot says, as information, that the block
// changed.
func TestAgentTemplateUpdateWithoutIsolation(t *testing.T) {
	pinIsolated(t, false)
	v1, v2, v3, old := agentVersions(t)
	r := newAgentRig(t)
	blockSaid := func() {
		t.Helper()
		if log := r.snapshot(); !strings.Contains(log, `"template" block changed`) || !strings.Contains(log, "keeps its own mode") || strings.Contains(log, "asks every instance") {
			t.Errorf("the snapshot's message: %q", log)
		}
	}

	// A repo an older xbind wrote carries the block as it was (the files
	// mirrored as they are, one commit a version); an instance from before
	// the default…
	writeOldRepo(t, r.tpl, v1)
	inst := r.instantiate(v1, "apps/old-agent", builtins.InstanceOpts{Partition: true})
	if got := instancePartitionOf(t, r.root, "apps/old-agent"); got != "" {
		t.Fatalf("an instance of the template without the default: partition %s", got)
	}
	// …merges today's template: the repo keeps the block it carries, and
	// asks for no mode
	r.b.MaterializeTemplateRepos(v2)
	if kept, _ := os.ReadFile(filepath.Join(r.tpl, "xbin.json")); string(kept) != old {
		t.Fatalf("the repo an older xbind wrote changed its manifest:\n%s", kept)
	}
	r.merge(inst, "apps/old-agent", "")
	blockSaid()
	// the note is said once: another start commits nothing
	head := mustGit(t, r.tpl, "rev-parse", "main")
	r.b.MaterializeTemplateRepos(v2)
	if again := mustGit(t, r.tpl, "rev-parse", "main"); again != head {
		t.Errorf("a start with the same template made another snapshot")
	}

	// A repo this xbind writes first carries no block at all; a partitioned
	// instance of today's template keeps its mode through the next change
	if err := os.RemoveAll(r.tpl); err != nil {
		t.Fatal(err)
	}
	if err := materializeTemplateRepo(r.root, v2, "agent"); err != nil {
		t.Fatal(err)
	}
	carried, _ := os.ReadFile(filepath.Join(r.tpl, "xbin.json"))
	if _, has, err := jsonc.TopLevel(carried, "template"); err != nil || has || r.served() != "" ||
		!strings.Contains(string(carried), `"partitionMail"`) || !strings.Contains(string(carried), "// Where xbind rings") {
		t.Fatalf("a new repo's manifest (no block, no partition, everything else as written):\n%s", carried)
	}
	part := r.instantiate(v2, "apps/new-agent", builtins.InstanceOpts{Partition: true})
	if got := instancePartitionOf(t, r.root, "apps/new-agent"); got != `["user","global"]` {
		t.Fatalf("a new instance: partition %q", got)
	}
	r.b.MaterializeTemplateRepos(v3)
	r.merge(part, "apps/new-agent", `["user","global"]`)
	blockSaid()

	// a new instance of today's template starts partitioned, unless opted out
	r.instantiate(v2, "apps/plain-agent", builtins.InstanceOpts{})
	if got := instancePartitionOf(t, r.root, "apps/plain-agent"); got != "" {
		t.Fatalf("an opted-out instance: partition %q", got)
	}
}

// covers D177 PD-44 PD-52 (amended) — the owner's ruling: after this update
// every agent-template instance becomes partitioned, with no migration.
// Under --isolate the served repo asks every instance for the block's
// partition (partitionOnUpdate), on the line after the opening brace, and
// says so in the snapshot that first does:
//
//   - an old unpartitioned instance holding data takes it in its merge and
//     sits pending — the tile held, nothing deleted — until a manager
//     decides; "Keep the current mode" declines: it runs unpartitioned
//     again (the legacy path), its data untouched;
//   - an empty old instance takes the mode at once (PD-44);
//   - an opted-out instance asks too; a new (partitioned) instance's merge
//     is clean, its line the same;
//   - once served, the request stays as served: a later template
//     narrowing its default, or an xbind without --isolate, changes or
//     removes nothing, so no partitioned instance is asked to switch again.
func TestAgentTemplateUpdateRequestsPartition(t *testing.T) {
	pinIsolated(t, true)
	v1, v2, v3, _ := agentVersions(t)
	r := newAgentRig(t)
	r.b.AllowInsecureVault = true
	if err := materializeTemplateRepo(r.root, v1, "agent"); err != nil {
		t.Fatal(err)
	}
	if r.served() != "" {
		t.Fatalf("the template before the default asks for %s", r.served())
	}
	full := r.instantiate(v1, "apps/full-agent", builtins.InstanceOpts{Partition: true})
	empty := r.instantiate(v1, "apps/empty-agent", builtins.InstanceOpts{Partition: true})
	optedOut := r.instantiate(v2, "apps/plain-agent", builtins.InstanceOpts{})
	fresh := r.instantiate(v2, "apps/new-agent", builtins.InstanceOpts{Partition: true})
	rescan := func() {
		t.Helper()
		if err := r.b.Reg.Rescan(); err != nil {
			t.Fatal(err)
		}
	}
	state := func(tile string) (registry.PartitionState, registry.PartitionSpec, *registry.PartitionRequest) {
		t.Helper()
		c, ok := r.b.Reg.Component(tile)
		if !ok {
			t.Fatalf("%s isn't registered", tile)
		}
		return c.PartitionState()
	}
	rescan()
	if err := r.b.vaultWrite("apps/full-agent", map[string]string{"conversations": "kept"}); err != nil {
		t.Fatal(err)
	}
	for _, tile := range []string{"apps/full-agent", "apps/empty-agent", "apps/plain-agent"} {
		if st, _, req := state(tile); st != registry.PartitionUnpartitioned || req != nil {
			t.Fatalf("%s before the update: %v %+v", tile, st, req)
		}
	}

	// today's template: the repo asks every instance, and says so once
	r.b.MaterializeTemplateRepos(v2)
	if got := r.served(); got != `["user","global"]` {
		t.Fatalf("the served repo asks for %q", got)
	}
	if log := r.snapshot(); !strings.Contains(log, `asks every instance of the template for partition ["user","global"]`) {
		t.Errorf("the snapshot's message: %q", log)
	}
	r.merge(full, "apps/full-agent", `["user","global"]`)
	r.merge(empty, "apps/empty-agent", `["user","global"]`)
	r.merge(optedOut, "apps/plain-agent", `["user","global"]`)
	r.merge(fresh, "apps/new-agent", `["user","global"]`)
	rescan()

	// with data: a request, held, nothing deleted
	st, rec, req := state("apps/full-agent")
	if st != registry.PartitionPending || rec.User || req == nil || req.Spec == nil || *req.Spec != bothSpec || req.Declined {
		t.Fatalf("apps/full-agent after the update: %v recorded %+v request %+v, want pending for user + global", st, rec, req)
	}
	if v, err := r.b.vaultRead("apps/full-agent"); err != nil || v["conversations"] != "kept" {
		t.Fatalf("the request touched the instance's data: %v %v", v, err)
	}
	// empty: the mode at once
	if st, rec, _ := state("apps/empty-agent"); st != registry.PartitionPartitioned || rec != bothSpec {
		t.Errorf("apps/empty-agent after the update: %v %+v, want partitioned", st, rec)
	}
	// "Keep the current mode": runs unpartitioned again, its data untouched
	w := call(t, r.b.apiPartitionMode, auth.Principal{Owner: true}, "POST", "/partitions/mode",
		`{"tile":"apps/full-agent","act":"keep","from":null,"to":{"user":true,"global":true}}`, nil)
	if w.Code != 200 {
		t.Fatalf("keep: %d %s", w.Code, w.Body)
	}
	rescan()
	if st, rec, req := state("apps/full-agent"); st != registry.PartitionUnpartitioned || rec.User || req == nil || !req.Declined {
		t.Errorf("apps/full-agent after keep: %v %+v %+v, want unpartitioned, the request declined", st, rec, req)
	}
	if v, err := r.b.vaultRead("apps/full-agent"); err != nil || v["conversations"] != "kept" {
		t.Errorf("keep touched the instance's data: %v %v", v, err)
	}

	// the request stays as served: a later template narrowing its default,
	// then an xbind without --isolate, change or remove nothing — the next
	// merge asks the kept instance for nothing new
	r.b.MaterializeTemplateRepos(v3)
	if got := r.served(); got != `["user","global"]` {
		t.Errorf("a later template changed the served request to %q", got)
	}
	pinIsolated(t, false)
	r.b.MaterializeTemplateRepos(v2)
	if got := r.served(); got != `["user","global"]` {
		t.Errorf("an xbind without --isolate removed the served request: %q", got)
	}
	r.merge(full, "apps/full-agent", `["user","global"]`)
	rescan()
	if st, _, req := state("apps/full-agent"); st != registry.PartitionUnpartitioned || req == nil || !req.Declined {
		t.Errorf("apps/full-agent after another update: %v %+v, want still declined", st, req)
	}
}

// writeOldRepo writes tfs's agent template as an older xbind's served repo:
// its files mirrored as they are (the block in its manifest), one commit.
func writeOldRepo(t *testing.T, tpl string, tfs fstest.MapFS) {
	t.Helper()
	if err := fs.WalkDir(tfs, "agent", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out := filepath.Join(tpl, strings.TrimPrefix(p, "agent/"))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, tfs[p].Data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	mustGit(t, tpl, "init", "-q", "-b", "main")
	mustGit(t, tpl, "add", "-A")
	mustGit(t, tpl, "-c", "user.email=xbin@localhost", "-c", "user.name=xbin", "commit", "-q", "-m", "template snapshot")
}
