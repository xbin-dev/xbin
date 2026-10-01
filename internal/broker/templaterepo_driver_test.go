package broker

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/auth"
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
	if !strings.Contains(want, " --rename 'apps/agent=apps/my-agent' ") {
		t.Fatalf("the driver doesn't rename: %s", want)
	}
	if got := strings.TrimSpace(mustGit(t, old, "config", "merge.xbin-manifest.driver")); got != want {
		t.Errorf("driver = %q, want %q", got, want)
	}
	if got := strings.TrimSpace(mustGit(t, old, "config", "merge.xbin-manifest.name")); got != manifestmerge.DriverTitle {
		t.Errorf("name = %q", got)
	}
	if got := strings.TrimSpace(mustGit(t, old, "config", "xbin.manifestDriver")); got != "true" {
		t.Errorf("xbin.manifestDriver = %q (xbind records that it named the driver)", got)
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
	// an older command xbind wrote is brought up to date
	mustGit(t, old, "config", "merge.xbin-manifest.driver", manifestmerge.DriverPrefix+"--marker-size %L %O %A %B || git merge-file %A %O %B")
	b.EnsureTemplateMergeDrivers()
	if got := strings.TrimSpace(mustGit(t, old, "config", "merge.xbin-manifest.driver")); got != want {
		t.Errorf("an older driver stayed: %q", got)
	}

	// a new instance at the template's own path: named when it is made, the
	// rename naming the template's path (bx checks the manifest names it)
	inst := repo("apps/agent", true)
	b.AddTemplateRemote(inst, "agent")
	if got := strings.TrimSpace(mustGit(t, inst, "config", "merge.xbin-manifest.driver")); got != manifestmerge.Driver("apps/agent", "apps/agent") {
		t.Errorf("a new instance's driver = %q", got)
	}

	// a copy of an instance (POST /clone copies .git): its driver renames to
	// the copy's path, not its source's
	if err := os.WriteFile(filepath.Join(old, "xbin.json"), []byte("{\n  \"runtime\": \"static\",\n  \"title\": \"apps/my-agent\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, old, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qam", "names its path")
	_ = b.Reg.Rescan()
	r := httptest.NewRequest("POST", "/clone", strings.NewReader(`{"from":"apps/my-agent","to":"apps/copy"}`))
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	b.apiClone(w, r)
	if w.Code != 200 {
		t.Fatalf("clone: %d %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(mustGit(t, filepath.Join(root, "apps", "copy"), "config", "merge.xbin-manifest.driver")); got != manifestmerge.Driver("apps/agent", "apps/copy") {
		t.Errorf("the copy's driver = %q", got)
	}
}

// covers T1 (review) — the instance's repository is the builder's: xbind
// leaves the driver alone when they opted out (xbin.manifestDriver false),
// named a driver command of their own under the name, removed the
// attributes line xbind wrote, or give xbin.json a merge attribute of their
// own (a tracked .gitattributes, or .git/info/attributes) — and, the line in
// place, doesn't write it again.
func TestTemplateMergeDriverLeftToTheBuilder(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	set, err := builtins.LoadTemplates(fstest.MapFS{"agent/xbin.json": {Data: []byte(`{"template": {"defaultName": "agent"}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	b.SetBuiltinTemplates(set)
	b.MaterializeTemplateRepos(fstest.MapFS{"agent/xbin.json": {Data: []byte("{\n  \"runtime\": \"static\"\n}\n")}})
	instance := func(tile string, files map[string]string, cfg ...[2]string) string {
		t.Helper()
		dir := filepath.Join(root, filepath.FromSlash(tile))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files["xbin.json"] = "{\n  \"runtime\": \"static\"\n}\n"
		for rel, s := range files {
			if err := os.WriteFile(filepath.Join(dir, rel), []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.SeedInstanceRepo(dir, "agent"); err != nil {
			t.Fatal(err)
		}
		mustGit(t, dir, "remote", "add", "template", "http://xbin/api/xbin/templates/agent.git")
		for _, kv := range cfg {
			mustGit(t, dir, "config", kv[0], kv[1])
		}
		return dir
	}
	optedOut := instance("apps/a1", map[string]string{}, [2]string{"xbin.manifestDriver", "false"})
	own := instance("apps/a2", map[string]string{}, [2]string{"merge.xbin-manifest.driver", "my-merge %O %A %B"})
	removed := instance("apps/a3", map[string]string{}, [2]string{"xbin.manifestDriver", "true"}) // xbind wrote the line; the builder took it out
	tracked := instance("apps/a4", map[string]string{".gitattributes": "*.json merge=union\n"})
	infoRule := instance("apps/a5", map[string]string{})
	if err := os.WriteFile(filepath.Join(infoRule, ".git", "info", "attributes"), []byte("xbin.json -merge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelated := instance("apps/a6", map[string]string{".gitattributes": "*.png binary\n* text=auto\n"})
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	b.EnsureTemplateMergeDrivers()
	for _, dir := range []string{optedOut, own, removed, tracked, infoRule} {
		attrs, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "attributes"))
		if strings.Contains(string(attrs), manifestmerge.Attribute) {
			t.Errorf("%s: the attributes line was written: %q", dir, attrs)
		}
		if got, _ := runGitIn(dir, "config", "merge.xbin-manifest.driver"); strings.Contains(got, manifestmerge.DriverPrefix) {
			t.Errorf("%s: xbind's driver was named: %q", dir, got)
		}
	}
	if got := strings.TrimSpace(mustGit(t, own, "config", "merge.xbin-manifest.driver")); got != "my-merge %O %A %B" {
		t.Errorf("the builder's own driver: %q", got)
	}
	if got := strings.TrimSpace(mustGit(t, unrelated, "check-attr", "merge", "xbin.json")); got != "xbin.json: merge: xbin-manifest" {
		t.Errorf("attributes that give xbin.json no merge rule keep the driver out: %q", got)
	}
	// the builder removes the line xbind wrote: it stays removed
	if err := os.WriteFile(filepath.Join(unrelated, ".git", "info", "attributes"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	b.EnsureTemplateMergeDrivers()
	if attrs, _ := os.ReadFile(filepath.Join(unrelated, ".git", "info", "attributes")); len(attrs) != 0 {
		t.Errorf("a removed line came back: %q", attrs)
	}

	cfg := parseGitConfig([]byte("[merge \"xbin-manifest\"]\n\tdriver = \"bx template merge-manifest --rename 'apps/a#b=x' %O\" ; a comment\n[XBIN]\n\tManifestDriver\n"))
	if got := cfg["merge.xbin-manifest.driver"]; got != "bx template merge-manifest --rename 'apps/a#b=x' %O" {
		t.Errorf("a quoted config value: %q", got)
	}
	if got := cfg["xbin.manifestdriver"]; got != "true" {
		t.Errorf("a bare config key: %q", got)
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
	v2 := agentTemplateFS(t)
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
