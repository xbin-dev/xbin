package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// pinIsolated sets what templatesIsolated answers for one test.
func pinIsolated(t *testing.T, on bool) {
	t.Helper()
	was := templatesIsolated
	templatesIsolated = func() bool { return on }
	t.Cleanup(func() { templatesIsolated = was })
}

// instancePartitionOf is the top-level "partition" of the instance at tile,
// as compact JSON ("" when absent).
func instancePartitionOf(t *testing.T, root, tile string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tile), "xbin.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, has, err := jsonc.TopLevel(b, "partition")
	if err != nil {
		t.Fatalf("%s/xbin.json doesn't parse: %v\n%s", tile, err, b)
	}
	if !has {
		return ""
	}
	var v any
	_ = json.Unmarshal(jsonc.Strip(raw), &v)
	out, _ := json.Marshal(v)
	return string(out)
}

// covers PD-35 PD-52 PD-19 — POST /templates/new writes the template's
// default partition mode into the instance, unless the body says
// "partition": false or xbind runs without --isolate; the answer and the
// catalog say which, and the fresh instance's first rescan records the mode
// ("auto") naming the person who instantiated it.
func TestTemplatesNewPartition(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	set, err := builtins.LoadTemplates(fstest.MapFS{
		"agent/xbin.json":  {Data: []byte(`{"template": {"title": "Agent", "defaultName": "agent", "partition": ["user", "global"]}}`)},
		"agent/index.html": {Data: []byte("<html></html>\n")},
		"plain/xbin.json":  {Data: []byte(`{"template": {"title": "Plain"}}`)},
		"plain/index.html": {Data: []byte("<html></html>\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	b.SetBuiltinTemplates(set)
	if _, err := b.Users.Upsert(users.User{ID: "alice", Role: users.RoleAdmin}, "password"); err != nil {
		t.Fatal(err)
	}
	alice := principalFor(t, b.Users, "alice")

	list := func() map[string]templateItem {
		t.Helper()
		w := call(t, b.apiTemplatesList, alice, "GET", "/templates", "", nil)
		var items []templateItem
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		out := map[string]templateItem{}
		for _, it := range items {
			out[it.ID] = it
		}
		return out
	}
	create := func(body string) map[string]any {
		t.Helper()
		w := call(t, b.apiTemplatesNew, alice, "POST", "/templates/new", body, nil)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	record := func(tile string) *modeRecord {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, "data", "partitions", util.TileKey(tile), "mode.json"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var rec modeRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatal(err)
		}
		return &rec
	}

	t.Run("isolated: the default is written and recorded by who", func(t *testing.T) {
		pinIsolated(t, true)
		if it := list()["agent"]; strings.Join(it.Partition, ",") != "user,global" || it.PartitionSkipped != "" {
			t.Errorf("catalog: %+v", it)
		}
		if it := list()["plain"]; it.Partition != nil || it.PartitionSkipped != "" {
			t.Errorf("a template without a default: %+v", it)
		}
		out := create(`{"source":"agent","path":"apps/a1"}`)
		if got := instancePartitionOf(t, root, "apps/a1"); got != `["user","global"]` {
			t.Fatalf("instance partition = %q", got)
		}
		if p, _ := out["partition"].([]any); len(p) != 2 || out["partitionSkipped"] != nil {
			t.Errorf("answer: %v", out)
		}
		rec := record("apps/a1")
		if rec == nil || len(rec.History) != 1 || rec.History[0].Op != modeOpAuto || rec.History[0].By != "alice" {
			t.Fatalf("mode record: %+v", rec)
		}
		c, _ := b.Reg.Component("apps/a1")
		if spec, ok := c.Partitioned(); !ok || !spec.Global {
			t.Errorf("the instance isn't partitioned: %v %v", spec, ok)
		}
		// Explicit true is the default too.
		create(`{"source":"agent","path":"apps/a3","partition":true}`)
		if got := instancePartitionOf(t, root, "apps/a3"); got != `["user","global"]` {
			t.Errorf("partition:true → %q", got)
		}
		if _, ok := instantiators.Load(root + "\x00apps/a1"); ok {
			t.Error("the instantiator note outlived the request")
		}
	})

	t.Run("opted out", func(t *testing.T) {
		pinIsolated(t, true)
		out := create(`{"source":"agent","path":"apps/a2","partition":false}`)
		if got := instancePartitionOf(t, root, "apps/a2"); got != "" {
			t.Fatalf("instance partition = %q", got)
		}
		if out["partitionSkipped"] != "opted out" || out["partition"] != nil {
			t.Errorf("answer: %v", out)
		}
		if rec := record("apps/a2"); rec != nil {
			t.Errorf("an unpartitioned instance got a mode record: %+v", rec)
		}
	})

	t.Run("without isolation", func(t *testing.T) {
		pinIsolated(t, false)
		if it := list()["agent"]; it.PartitionSkipped != "needs --isolate" {
			t.Errorf("catalog: %+v", it)
		}
		if it := list()["plain"]; it.PartitionSkipped != "" {
			t.Errorf("a template without a default: %+v", it)
		}
		out := create(`{"source":"agent","path":"apps/n1"}`)
		if got := instancePartitionOf(t, root, "apps/n1"); got != "" {
			t.Fatalf("instance partition = %q", got)
		}
		if out["partitionSkipped"] != "needs --isolate" {
			t.Errorf("answer: %v", out)
		}
		if rec := record("apps/n1"); rec != nil {
			t.Errorf("zero state expected: %+v", rec)
		}
	})

	t.Run("a workspace template", func(t *testing.T) {
		pinIsolated(t, true)
		dir := filepath.Join(root, "templates", "wt")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte("{\n  // jsonc\n  \"template\": {\"title\": \"WT\", \"partition\": [\"user\"]},\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := b.Reg.Rescan(); err != nil {
			t.Fatal(err)
		}
		if it := list()["templates/wt"]; strings.Join(it.Partition, ",") != "user" {
			t.Errorf("catalog: %+v", it)
		}
		create(`{"source":"templates/wt","path":"apps/w1"}`)
		if got := instancePartitionOf(t, root, "apps/w1"); got != `["user"]` {
			t.Errorf("instance partition = %q", got)
		}
		create(`{"source":"templates/wt","path":"apps/w2","partition":false}`)
		if got := instancePartitionOf(t, root, "apps/w2"); got != "" {
			t.Errorf("opted-out instance partition = %q", got)
		}
	})
}

// covers PD-52 — the template-merge fixture (01 §Tests): a builder's `git
// merge template/main` never introduces or changes a top-level "partition".
// The default lives in the template block, which instances never carry and
// the served repo never changes (templaterepo_block.go): an upstream change
// to it is no change to merge — the merge is clean, keeps the instance's own
// value and brings no block, and upstream's fix elsewhere (the title) comes
// in. Should a merge still conflict, neither side carries a top-level key.
func TestTemplateMergeNeverAddsPartition(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	manifest := func(tplPartition, title string) []byte {
		p := ""
		if tplPartition != "" {
			p = ",\n    \"partition\": " + tplPartition
		}
		return []byte("{\n  \"runtime\": \"go\",\n  \"template\": {\n    \"defaultName\": \"agent\"" + p +
			",\n    \"title\": \"Agent\"\n  },\n  \"title\": \"" + title + "\"\n}\n")
	}
	tfs := func(tplPartition, title, html string) fstest.MapFS {
		return fstest.MapFS{
			"agent/xbin.json":  {Data: manifest(tplPartition, title)},
			"agent/index.html": {Data: []byte("<html>" + html + "</html>\n")},
		}
	}
	tpl := filepath.Join(templateReposDir(root), "agent")

	for _, c := range []struct {
		name       string
		v1, v2     string // template.partition before and after
		opts       builtins.InstanceOpts
		want       string // the instance's top-level partition, before and after
		changeLine bool   // v2 also changes a top-level line
		clean      bool   // the merge is clean (upstream's fix is elsewhere)
	}{
		{"default changes", `["user", "global"]`, `["user"]`, builtins.InstanceOpts{Partition: true}, `["user","global"]`, true, true},
		{"opted out, default changes", `["user", "global"]`, `["user"]`, builtins.InstanceOpts{}, "", true, true},
		{"a default appears", "", `["user", "global"]`, builtins.InstanceOpts{Partition: true}, "", false, true},
		{"the default goes", `["user"]`, "", builtins.InstanceOpts{Partition: true}, `["user"]`, false, true},
		{"a fix elsewhere", `["user"]`, `["user"]`, builtins.InstanceOpts{Partition: true}, `["user"]`, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := os.RemoveAll(tpl); err != nil { // a fresh template repo per case
				t.Fatal(err)
			}
			b.MaterializeTemplateRepos(tfs(c.v1, "v1", "v1"))
			tile := "apps/" + strings.ReplaceAll(c.name, " ", "-")
			tile = strings.ReplaceAll(tile, ",", "")
			files, err := builtins.RenderTree(tfs(c.v1, "v1", "v1"), "agent", tile, "apps/agent", &c.opts)
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
			if got := instancePartitionOf(t, root, tile); got != c.want {
				t.Fatalf("instantiated partition = %q, want %q", got, c.want)
			}

			v2Title := "v1"
			if c.changeLine {
				v2Title = "v2"
			}
			b.MaterializeTemplateRepos(tfs(c.v2, v2Title, "v2"))
			mustGit(t, inst, "fetch", "-q", tpl, "main")
			_, merr := runGitIn(inst, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-edit", "FETCH_HEAD")
			if c.clean != (merr == nil) {
				t.Fatalf("merge error %v, want clean: %v", merr, c.clean)
			}
			if merr == nil {
				if got := instancePartitionOf(t, root, tile); got != c.want {
					t.Fatalf("a clean merge changed the instance's partition to %q (want %q)", got, c.want)
				}
				doc, _ := os.ReadFile(filepath.Join(inst, "xbin.json"))
				if _, has, err := jsonc.TopLevel(doc, "template"); err != nil || has {
					t.Fatalf("the merge brought the template block in (%v):\n%s", err, doc)
				}
				if title, _, _ := jsonc.TopLevel(doc, "title"); c.changeLine != strings.Contains(string(title), "v2") {
					t.Fatalf("upstream's own change to the title merged: %v (title %s)", !c.changeLine, title)
				}
				return
			}
			// A conflict: the instance's side keeps its value, upstream's side
			// is the template itself (block and all), with no top-level key.
			ours := mustGit(t, inst, "show", ":2:xbin.json")
			theirs := mustGit(t, inst, "show", ":3:xbin.json")
			for side, doc := range map[string]string{"ours": ours, "theirs": theirs} {
				raw, has, err := jsonc.TopLevel([]byte(doc), "partition")
				if err != nil {
					t.Fatalf("%s side doesn't parse: %v\n%s", side, err, doc)
				}
				switch {
				case side == "theirs" && has:
					t.Errorf("upstream's side carries a top-level partition %s", raw)
				case side == "ours" && (c.want != "") != has:
					t.Errorf("the instance's side lost or gained its partition: %s", doc)
				}
			}
			if !strings.Contains(theirs, `"template"`) {
				t.Errorf("upstream's side isn't the template:\n%s", theirs)
			}
			mustGit(t, inst, "merge", "--abort")
		})
	}
}
