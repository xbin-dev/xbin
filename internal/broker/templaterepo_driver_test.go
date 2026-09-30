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

// covers T1 — every builtin template instance's repository names the
// manifest's merge driver, in its .git/config and .git/info/attributes
// only: one an older xbind made gets it at start (EnsureTemplateMergeDrivers),
// its command renaming the template's own path to the instance's, the
// builder's attributes kept; a new one when it is made (AddTemplateRemote);
// a driver an older command named is brought up to date; a start with it in
// place changes nothing; a tile without the template remote is left alone.
func TestTemplateMergeDriverConfigured(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	set, err := builtins.LoadTemplates(fstest.MapFS{"agent/xbin.json": {Data: []byte(`{"template": {"defaultName": "agent"}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	b.SetBuiltinTemplates(set)
	b.MaterializeTemplateRepos(fstest.MapFS{"agent/xbin.json": {Data: []byte("{\n  \"runtime\": \"static\"\n}\n")}})
	repo := func(tile string, template bool) string {
		t.Helper()
		dir := filepath.Join(root, filepath.FromSlash(tile))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte("{\n  \"runtime\": \"static\"\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if template {
			if err := b.SeedInstanceRepo(dir, "agent"); err != nil {
				t.Fatal(err)
			}
		} else {
			mustGit(t, dir, "init", "-q", "-b", "main")
		}
		return dir
	}
	// an instance an older xbind made: the remote as it wrote it, no driver,
	// and an attributes file of the builder's without a final newline
	old := repo("apps/my-agent", true)
	mustGit(t, old, "remote", "add", "template", "http://xbin/api/xbin/templates/agent.git")
	if err := os.WriteFile(filepath.Join(old, ".git", "info", "attributes"), []byte("*.png binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := repo("apps/plain", false)
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}

	b.EnsureTemplateMergeDrivers()
	want := manifestmerge.Driver("apps/agent", "apps/my-agent")
	if !strings.Contains(want, " --rename apps/agent=apps/my-agent ") {
		t.Fatalf("the driver doesn't rename: %s", want)
	}
	if got := strings.TrimSpace(mustGit(t, old, "config", "merge.xbin-manifest.driver")); got != want {
		t.Errorf("driver = %q, want %q", got, want)
	}
	if got := strings.TrimSpace(mustGit(t, old, "config", "merge.xbin-manifest.name")); got != manifestmerge.DriverTitle {
		t.Errorf("name = %q", got)
	}
	if got := strings.TrimSpace(mustGit(t, old, "check-attr", "merge", "--", "xbin.json", "_backend/xbin.json")); got !=
		"xbin.json: merge: xbin-manifest\n_backend/xbin.json: merge: unspecified" {
		t.Errorf("check-attr: %q", got)
	}
	attrs, _ := os.ReadFile(filepath.Join(old, ".git", "info", "attributes"))
	if string(attrs) != "*.png binary\n/xbin.json merge=xbin-manifest\n" {
		t.Errorf("attributes: %q", attrs)
	}
	if st := mustGit(t, old, "status", "--porcelain"); st != "" {
		t.Errorf("the driver touched the work tree: %q", st)
	}
	if _, err := runGitIn(plain, "config", "merge.xbin-manifest.driver"); err == nil {
		t.Errorf("a tile without the template remote got the driver")
	}
	if _, err := os.Stat(filepath.Join(plain, ".git", "info", "attributes")); err == nil {
		if a, _ := os.ReadFile(filepath.Join(plain, ".git", "info", "attributes")); strings.Contains(string(a), "xbin-manifest") {
			t.Errorf("a tile without the template remote got the attribute")
		}
	}

	// a start with the driver in place changes nothing
	cfgPath := filepath.Join(old, ".git", "config")
	before, _ := os.Stat(cfgPath)
	b.EnsureTemplateMergeDrivers()
	if after, _ := os.Stat(cfgPath); !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("a second start rewrote the config")
	}
	if again, _ := os.ReadFile(filepath.Join(old, ".git", "info", "attributes")); string(again) != string(attrs) {
		t.Errorf("a second start changed the attributes: %q", again)
	}
	// an older command is brought up to date
	mustGit(t, old, "config", "merge.xbin-manifest.driver", "bx old-driver %O %A %B")
	b.EnsureTemplateMergeDrivers()
	if got := strings.TrimSpace(mustGit(t, old, "config", "merge.xbin-manifest.driver")); got != want {
		t.Errorf("an older driver stayed: %q", got)
	}

	// a new instance at the template's own path: named when it is made, no rename
	inst := repo("apps/agent", true)
	b.AddTemplateRemote(inst, "agent")
	if got := strings.TrimSpace(mustGit(t, inst, "config", "merge.xbin-manifest.driver")); got != manifestmerge.Driver("apps/agent", "apps/agent") || strings.Contains(got, "--rename") {
		t.Errorf("a new instance's driver = %q", got)
	}
}

// covers T1 — a new instance's manifest is the template's own JSONC (its
// comments, the key order) without the block and the comment about it,
// plus its partition on the line after the brace: the served repo's
// manifest and one line. So a later
// change to the template's manifest merges line by line, without the
// driver — the new uses entry and interface come in, the partition and the
// comments stay.
func TestNewInstanceManifestMergesByLines(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	src := xbin.BuiltinTemplatesFS()
	v2 := fstest.MapFS{}
	if err := fs.WalkDir(src, "agent", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(p, "/test/") || strings.Contains(p, "/node_modules/") {
			return err
		}
		data, err := fs.ReadFile(src, p)
		v2[p] = &fstest.MapFile{Data: data}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	b.MaterializeTemplateRepos(v2)
	tpl := filepath.Join(templateReposDir(root), "agent")
	served, _ := os.ReadFile(filepath.Join(tpl, "xbin.json"))

	files, err := builtins.RenderTree(v2, "agent", "apps/agent", "apps/agent", &builtins.InstanceOpts{Partition: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(files["xbin.json"])
	wantLine := "  \"partition\": [\"user\", \"global\"],\n"
	if want := strings.Replace(string(served), "{\n", "{\n"+wantLine, 1); got != want {
		t.Fatalf("the new instance's manifest isn't the served one plus its partition:\n%s\n---- served:\n%s", got, served)
	}
	for _, s := range []string{"// This agent's own resources", "// Where xbind rings"} {
		if !strings.Contains(got, s) {
			t.Errorf("the instance lost the template's comment %q", s)
		}
	}
	if strings.Contains(got, "A TEMPLATE component") || strings.Contains(got, `"template"`) {
		t.Errorf("the instance kept the block or the comment about it:\n%s", got)
	}
	inst := filepath.Join(root, "apps", "agent")
	if _, err := builtins.WriteTree(inst, "apps/agent", files); err != nil {
		t.Fatal(err)
	}
	if err := b.SeedInstanceRepo(inst, "agent"); err != nil {
		t.Fatal(err)
	}
	if d := mustGit(t, inst, "diff", "--numstat", "HEAD~1", "HEAD", "--", "xbin.json"); strings.TrimSpace(d) != "1\t0\txbin.json" {
		t.Errorf("instantiation changed more than one line of the manifest: %q", d)
	}

	// the template's next version: a uses entry and an interface of its own
	v3 := fstest.MapFS{}
	for p, f := range v2 {
		v3[p] = f
	}
	m := string(v2["agent/xbin.json"].Data)
	for _, e := range [][2]string{
		{"    { \"target\": \"res:apps/agent/team\", \"role\": \"writer\" },\n", "    { \"target\": \"res:apps/agent/team\", \"role\": \"writer\" },\n    { \"target\": \"res:apps/agent/cache\", \"role\": \"writer\" },\n"},
		{"    \"net\": { \"kind\": \"net\" }\n", "    \"net\": { \"kind\": \"net\" },\n    // Voice (a later version's).\n    \"tts\": { \"kind\": \"http\", \"service\": \"tts\" }\n"},
	} {
		if !strings.Contains(m, e[0]) {
			t.Fatalf("the agent template's manifest no longer has %q", e[0])
		}
		m = strings.Replace(m, e[0], e[1], 1)
	}
	v3["agent/xbin.json"] = &fstest.MapFile{Data: []byte(m)}
	b.MaterializeTemplateRepos(v3)
	mustGit(t, inst, "fetch", "-q", tpl, "main")
	if out, err := runGitIn(inst, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-edit", "FETCH_HEAD"); err != nil {
		t.Fatalf("the merge conflicts (%v): %s\n%s", err, out, mustGit(t, inst, "diff"))
	}
	merged, _ := os.ReadFile(filepath.Join(inst, "xbin.json"))
	for _, s := range []string{wantLine, "res:apps/agent/cache", "// Voice (a later version's).", "// Where xbind rings"} {
		if !strings.Contains(string(merged), s) {
			t.Errorf("the merged manifest lacks %q:\n%s", s, merged)
		}
	}
	if got := instancePartitionOf(t, root, "apps/agent"); got != `["user","global"]` {
		t.Errorf("partition after the merge: %q", got)
	}
}
