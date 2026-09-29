package builtins

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	xbin "github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/jsonc"
)

// topPartition is the top-level "partition" of the manifest at p, as
// compact JSON ("" when absent).
func topPartition(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := jsonc.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s doesn't parse: %v\n%s", p, err, b)
	}
	raw, ok := m["partition"]
	if !ok {
		return ""
	}
	return canonical(raw)
}

// covers PD-35 PD-52 PD-19 — instantiation writes the template block's
// "partition" as the instance's top-level key; opted out (and, at the
// broker, without isolation) the instance asks for none, a top-level key
// the template itself carries included. The template block never survives.
func TestInstantiatePartitionDefault(t *testing.T) {
	tfs := fstest.MapFS{
		"agent/xbin.json": {Data: []byte(`{
  // a template (JSONC)
  "template": {"title": "Agent", "defaultName": "agent", "partition": ["user", "global"]},
  "runtime": "go",
  "entry": "./_backend",
}
`)},
		"agent/index.html": {Data: []byte("<html>apps/agent</html>\n")},
		"plain/xbin.json":  {Data: []byte(`{"template": {"title": "Plain"}, "runtime": "go"}`)},
		"plain/index.html": {Data: []byte("<html></html>\n")},
		"odd/xbin.json":    {Data: []byte(`{"template": {"title": "Odd"}, "partition": ["user"]}`)},
		"odd/index.html":   {Data: []byte("<html></html>\n")},
	}
	set, err := LoadTemplates(tfs)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := set.Get("agent"); strings.Join(e.Partition, ",") != "user,global" {
		t.Fatalf("catalog partition = %v", e.Partition)
	}
	if e, _ := set.Get("plain"); e.Partition != nil {
		t.Fatalf("a template without a default lists %v", e.Partition)
	}

	root := t.TempDir()
	inst := func(name, target string, opts InstanceOpts) string {
		t.Helper()
		if _, _, err := set.Instantiate(root, name, target, opts); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, filepath.FromSlash(target), "xbin.json")
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), `"template"`) {
			t.Fatalf("%s kept its template block:\n%s", target, b)
		}
		return topPartition(t, p)
	}
	if got := inst("agent", "apps/a1", InstanceOpts{Partition: true}); got != `["user","global"]` {
		t.Errorf("default: partition = %q", got)
	}
	if got := inst("agent", "apps/a2", InstanceOpts{}); got != "" {
		t.Errorf("opted out: partition = %q", got)
	}
	if got := inst("plain", "apps/p1", InstanceOpts{Partition: true}); got != "" {
		t.Errorf("a template without a default: partition = %q", got)
	}
	if got := inst("odd", "apps/o1", InstanceOpts{}); got != "" {
		t.Errorf("opted out of a template carrying a top-level key: partition = %q", got)
	}
	// The own-path rewrite still applies beside the partition.
	if b, _ := os.ReadFile(filepath.Join(root, "apps", "a1", "index.html")); !strings.Contains(string(b), "apps/a1") {
		t.Errorf("own path not rewritten: %s", b)
	}
}

// Every builtin template names its instances' mode in its template block
// only: a top-level "partition" in a template would ride into instances by
// `git merge template/main` (D50), and a default must be a valid mode.
func TestTemplatesNoTopLevelPartition(t *testing.T) {
	fsys := xbin.BuiltinTemplatesFS()
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := fs.ReadFile(fsys, path.Join(e.Name(), "xbin.json"))
		if err != nil {
			continue
		}
		if _, has, err := jsonc.TopLevel(raw, "partition"); err != nil || has {
			t.Errorf("builtin-templates/%s/xbin.json: a top-level \"partition\" (%v) — put the instances' mode in the template block", e.Name(), err)
		}
	}
	set, err := LoadTemplates(fsys)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range set.List() {
		switch strings.Join(e.Partition, ",") {
		case "", "user", "user,global", "global,user":
		default:
			t.Errorf("builtin template %s: template.partition %v isn't a mode", e.Name, e.Partition)
		}
	}
}

// covers PD-52 — replace and merge write each manifest with the installed
// "partition" (absent → absent, ["user"] → ["user"]) whatever upstream does
// to it (adds, removes, changes), take every other upstream change, keep
// the rest of the file as upstream wrote it (comments included), and note
// the difference. A proposal's series never touches the key.
func TestUpdateKeepsPartition(t *testing.T) {
	manifest := func(partition, title string) []byte {
		p := ""
		if partition != "" {
			p = "  \"partition\": " + partition + ",\n"
		}
		return []byte("{\n  // upstream's comment\n  \"runtime\": \"go\",\n" + p + "  \"title\": \"" + title + "\"\n}\n")
	}
	unit := func(partition, title string) fstest.MapFS {
		return fstest.MapFS{
			"tiles/x/xbin.json":  {Data: manifest(partition, title)},
			"tiles/x/index.html": {Data: []byte("<html>" + title + "</html>\n")},
		}
	}
	cases := []struct {
		name            string
		v1, installed   string // v1: upstream at install; installed: the workspace's value after local edits ("-" = as installed)
		v2              string // upstream's new value
		wantPartition   string
		wantNote, local bool
	}{
		{name: "upstream adds", v1: "", installed: "-", v2: `["user"]`, wantPartition: "", wantNote: true},
		{name: "upstream removes", v1: `["user"]`, installed: "-", v2: "", wantPartition: `["user"]`, wantNote: true},
		{name: "upstream changes", v1: `["user"]`, installed: "-", v2: `["user","global"]`, wantPartition: `["user"]`, wantNote: true},
		{name: "local add, upstream unchanged", v1: "", installed: `["user", "global"]`, v2: "", wantPartition: `["user","global"]`, wantNote: true, local: true},
		{name: "local remove, upstream changes", v1: `["user"]`, installed: "", v2: `["user","global"]`, wantPartition: "", wantNote: true, local: true},
		{name: "same", v1: `["user"]`, installed: "-", v2: `["user"]`, wantPartition: `["user"]`},
		{name: "absent", v1: "", installed: "-", v2: "", wantPartition: ""},
	}
	for _, mode := range []string{"replace", "merge"} {
		for _, c := range cases {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				root := t.TempDir()
				if _, err := NewUpdater(root, nil, unit(c.v1, "v1")).ApplyReplace("scaffold:tiles/x"); err != nil {
					t.Fatal(err)
				}
				mp := filepath.Join(root, "tiles", "x", "xbin.json")
				if c.installed != "-" {
					if err := os.WriteFile(mp, manifest(c.installed, "v1"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				u2 := NewUpdater(root, nil, unit(c.v2, "v2"))
				var a Applied
				var err error
				if mode == "replace" {
					a, err = u2.ApplyReplace("scaffold:tiles/x")
				} else {
					a, err = u2.ApplyMerge("scaffold:tiles/x")
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := topPartition(t, mp); got != c.wantPartition {
					t.Errorf("partition = %q, want %q", got, c.wantPartition)
				}
				b, _ := os.ReadFile(mp)
				if !strings.Contains(string(b), `"title": "v2"`) || !strings.Contains(string(b), "// upstream's comment") {
					t.Errorf("upstream's other changes (or its comments) didn't land:\n%s", b)
				}
				if strings.Count(string(b), `"partition"`) > 1 {
					t.Errorf("two partition keys:\n%s", b)
				}
				if got, _ := os.ReadFile(filepath.Join(root, "tiles", "x", "index.html")); string(got) != "<html>v2</html>\n" {
					t.Errorf("index.html = %q", got)
				}
				joined := strings.Join(a.Notes, "\n")
				if c.wantNote != (joined != "") || c.wantNote && !strings.Contains(joined, "tiles/x/xbin.json: partition kept as installed") {
					t.Errorf("notes = %q (want one: %v)", a.Notes, c.wantNote)
				}
				// The recorded base is upstream's own form: an unchanged
				// upstream offers nothing, whatever partition is kept.
				ups, err := u2.Updates()
				if err != nil {
					t.Fatal(err)
				}
				for _, uu := range ups {
					if uu.ID == "scaffold:tiles/x" && uu.HasUpdate {
						t.Errorf("an applied update is still offered: %+v", uu)
					}
				}
			})
		}
	}
}

// A merge with local edits on both sides keeps the installed partition
// beside upstream's other changes; a proposal's series leaves it alone.
func TestMergeAndProposeKeepPartition(t *testing.T) {
	v1 := fstest.MapFS{
		"tiles/x/xbin.json":  {Data: []byte("{\n  \"runtime\": \"go\",\n  \"title\": \"v1\",\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}\n")},
		"tiles/x/index.html": {Data: []byte("<html></html>\n")},
	}
	root := t.TempDir()
	if _, err := NewUpdater(root, nil, v1).ApplyReplace("scaffold:tiles/x"); err != nil {
		t.Fatal(err)
	}
	tile := filepath.Join(root, "tiles", "x")
	mp := filepath.Join(tile, "xbin.json")
	// Local: the builder asks for partitions and edits "c".
	local := "{\n  \"runtime\": \"go\",\n  \"title\": \"v1\",\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 30,\n  \"partition\": [\"user\"]\n}\n"
	if err := os.WriteFile(mp, []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, tile, "init", "-q", "-b", "main")
	gitT(t, tile, "add", "-A")
	gitT(t, tile, "commit", "-qm", "local")
	// Upstream: a new title and its own (different) partition at the top.
	v2 := fstest.MapFS{
		"tiles/x/xbin.json":  {Data: []byte("{\n  \"partition\": [\"user\", \"global\"],\n  \"runtime\": \"go\",\n  \"title\": \"v2\",\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}\n")},
		"tiles/x/index.html": {Data: []byte("<html></html>\n")},
	}
	u2 := NewUpdater(root, nil, v2)
	prop, err := u2.Propose("scaffold:tiles/x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prop.Series, "partition") && !strings.Contains(prop.Message, "partition kept as installed") {
		t.Fatalf("the series touches partition without a note:\n%s", prop.Series)
	}
	for _, ln := range strings.Split(prop.Series, "\n") {
		if (strings.HasPrefix(ln, "+") || strings.HasPrefix(ln, "-")) && strings.Contains(ln, `"partition"`) {
			t.Errorf("the series changes the partition line: %q", ln)
		}
	}
	if !strings.Contains(prop.Message, `partition kept as installed (["user"]; upstream asks ["user","global"])`) {
		t.Errorf("proposal message lacks the note:\n%s", prop.Message)
	}

	a, err := u2.ApplyMerge("scaffold:tiles/x")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(mp)
	if strings.Contains(string(b), "<<<<<<<") {
		t.Fatalf("conflict:\n%s", b)
	}
	if got := topPartition(t, mp); got != `["user"]` {
		t.Errorf("partition = %s\n%s", got, b)
	}
	if !strings.Contains(string(b), `"title": "v2"`) || !strings.Contains(string(b), `"c": 30`) {
		t.Errorf("a side's change was lost:\n%s", b)
	}
	if len(a.Notes) != 1 {
		t.Errorf("notes = %q", a.Notes)
	}
}

// A merge that conflicts elsewhere still carries ours' partition — once, and
// never upstream's — beside the markers the builder resolves.
func TestMergeConflictKeepsPartition(t *testing.T) {
	base := "{\n  \"runtime\": \"go\",\n  \"title\": \"v1\"\n}\n"
	ours := "{\n  \"runtime\": \"go\",\n  \"partition\": [\"user\"],\n  \"title\": \"mine\"\n}\n"
	theirs := "{\n  \"runtime\": \"go\",\n  \"partition\": [\"user\", \"global\"],\n  \"title\": \"v2\"\n}\n"
	merged, err := mergeKeeping("xbin.json", []byte(ours), []byte(base), []byte(theirs))
	if err != nil {
		t.Fatal(err)
	}
	m := string(merged)
	if !strings.Contains(m, "<<<<<<<") {
		t.Fatalf("expected a conflict on title:\n%s", m)
	}
	if strings.Count(m, `"partition"`) != 1 || !strings.Contains(m, "{\n  \"partition\": [\"user\"],\n") || strings.Contains(m, "global") {
		t.Errorf("partition not carried once as ours':\n%s", m)
	}
	// Resolving the conflict either way leaves ours' partition.
	for _, side := range []string{`"title": "mine"`, `"title": "v2"`} {
		var out []string
		skip := false
		for _, ln := range strings.Split(m, "\n") {
			switch {
			case strings.HasPrefix(ln, "<<<<<<<"), strings.HasPrefix(ln, "|||||||"), strings.HasPrefix(ln, "======="), strings.HasPrefix(ln, ">>>>>>>"):
				skip = false
				continue
			case strings.Contains(ln, `"title"`) && !strings.Contains(ln, side):
				skip = true
			}
			if !skip {
				out = append(out, ln)
			}
		}
		var v struct{ Partition []string }
		if err := jsonc.Unmarshal([]byte(strings.Join(out, "\n")), &v); err != nil || strings.Join(v.Partition, ",") != "user" {
			t.Errorf("resolved to %s: partition %v (%v)\n%s", side, v.Partition, err, strings.Join(out, "\n"))
		}
	}
	// Sides that agree merge as they are: the line doesn't move.
	same := "{\n  \"runtime\": \"go\",\n  \"partition\": [\"user\"],\n  \"title\": \"v1\"\n}\n"
	merged, err = mergeKeeping("xbin.json", []byte(same), []byte(same), []byte(strings.Replace(same, "v1", "v2", 1)))
	if err != nil || string(merged) != strings.Replace(same, "v1", "v2", 1) {
		t.Errorf("agreeing sides: %s (%v)", merged, err)
	}
}

// withPartitionOf reports a manifest it can't read instead of guessing, and
// leaves a merge result with conflict markers to the builder.
func TestWithPartitionOfUnreadable(t *testing.T) {
	up := []byte(`{"partition": ["user"], "a": 1}`)
	got, note := withPartitionOf([]byte("{ not json"), up)
	if string(got) != string(up) || !strings.Contains(note, "doesn't parse") || !strings.Contains(note, `["user"]`) {
		t.Errorf("unreadable installed: %s / %q", got, note)
	}
	markers := []byte("{\n<<<<<<< ours\n  \"a\": 1\n=======\n  \"a\": 2\n>>>>>>> theirs\n}\n")
	if got, note := withPartitionOf([]byte(`{"a": 1}`), markers); string(got) != string(markers) || note != "" {
		t.Errorf("markers: %s / %q", got, note)
	}
}
