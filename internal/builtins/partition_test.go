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
	"github.com/xbin-dev/xbin/internal/registry"
)

// topPartition is the top-level "partition" of the manifest at p as xbind
// reads it (any case), as compact JSON ("" when absent).
func topPartition(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Partition json.RawMessage `json:"partition"`
	}
	if err := jsonc.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s doesn't parse: %v\n%s", p, err, b)
	}
	if m.Partition == nil {
		return ""
	}
	return canonical(m.Partition)
}

// covers PD-35 PD-52 PD-19 — instantiation writes the template block's
// "partition" as the instance's top-level key; opted out (and, at the
// broker, without isolation) the instance asks for none, a top-level key
// the template itself carries included — in any case, as xbind reads keys.
// The template block never survives.
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
		"odd/xbin.json":    {Data: []byte(`{"template": {"title": "Odd"}, "PARTITION": ["user"], "Partition": ["user"]}`)},
		"odd/index.html":   {Data: []byte("<html></html>\n")},
		"caps/xbin.json":   {Data: []byte(`{"template": {"title": "Caps", "Partition": ["user"]}, "Partition": ["user", "global"]}`)},
		"caps/index.html":  {Data: []byte("<html></html>\n")},
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
		if n := strings.Count(strings.ToLower(string(b)), `"partition"`); n > 1 {
			t.Fatalf("%s has %d partition keys:\n%s", target, n, b)
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
		t.Errorf("opted out of a template carrying a top-level key (any case): partition = %q", got)
	}
	// A block default in another case is the default, and replaces a
	// top-level key in any case.
	if got := inst("caps", "apps/c1", InstanceOpts{Partition: true}); got != `["user"]` {
		t.Errorf("a \"Partition\" default: partition = %q", got)
	}
	if got := inst("caps", "apps/c2", InstanceOpts{}); got != "" {
		t.Errorf("opted out of a \"Partition\" default: partition = %q", got)
	}
	// The own-path rewrite still applies beside the partition.
	if b, _ := os.ReadFile(filepath.Join(root, "apps", "a1", "index.html")); !strings.Contains(string(b), "apps/a1") {
		t.Errorf("own path not rewritten: %s", b)
	}
}

// Every builtin template names its instances' mode in its template block
// only: a top-level "partition" in a template would ride into instances by
// `git merge template/main` (D50). A default must be a mode the registry
// accepts, judged on the block's raw value as an instance carries it.
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
		block, _, _ := jsonc.TopLevel(raw, "template")
		if def := templatePartition(block); def != nil {
			var pl registry.PartitionList
			err := json.Unmarshal(jsonc.Strip(def), &pl)
			if _, perr := registry.ParsePartition(pl); err != nil || perr != nil {
				t.Errorf("builtin-templates/%s: template.partition %s isn't a mode: %v %v", e.Name(), def, err, perr)
			}
		}
	}
}

// manifest is a unit manifest with partition (a raw value, "" for none).
func manifest(partition, title string) []byte {
	p := ""
	if partition != "" {
		p = "  \"partition\": " + partition + ",\n"
	}
	return []byte("{\n  // upstream's comment\n  \"runtime\": \"go\",\n" + p + "  \"title\": \"" + title + "\"\n}\n")
}

func unit(partition, title string) fstest.MapFS {
	return fstest.MapFS{
		"tiles/x/xbin.json":  {Data: manifest(partition, title)},
		"tiles/x/index.html": {Data: []byte("<html>" + title + "</html>\n")},
	}
}

// installed puts v1 of the unit in a fresh workspace and returns it with
// the manifest's path.
func installed(t *testing.T, v1 fstest.MapFS) (root, mp string) {
	t.Helper()
	root = t.TempDir()
	if _, err := NewUpdater(root, nil, v1).ApplyReplace("scaffold:tiles/x"); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "tiles", "x", "xbin.json")
}

func offered(t *testing.T, u *Updater) bool {
	t.Helper()
	ups, err := u.Updates()
	if err != nil {
		t.Fatal(err)
	}
	for _, uu := range ups {
		if uu.ID == "scaffold:tiles/x" && uu.HasUpdate {
			return true
		}
	}
	return false
}

// covers PD-52 — replace and merge write each manifest with the installed
// "partition" (absent → absent, ["user"] → ["user"]) whatever upstream does
// to it (adds, removes, changes), take every other upstream change, keep
// the rest of the file as upstream wrote it (comments included), and note
// where upstream asks otherwise: a replace whenever it does, a merge (which
// keeps local edits anyway) when upstream changed the key.
func TestUpdateKeepsPartition(t *testing.T) {
	cases := []struct {
		name          string
		v1, installed string // v1: upstream at install; installed: the workspace's value after local edits ("-" = as installed)
		v2            string // upstream's new value
		wantPartition string
		noteReplace   bool
		noteMerge     bool
	}{
		{name: "upstream adds", v1: "", installed: "-", v2: `["user"]`, wantPartition: "", noteReplace: true, noteMerge: true},
		{name: "upstream removes", v1: `["user"]`, installed: "-", v2: "", wantPartition: `["user"]`, noteReplace: true, noteMerge: true},
		{name: "upstream changes", v1: `["user"]`, installed: "-", v2: `["user","global"]`, wantPartition: `["user"]`, noteReplace: true, noteMerge: true},
		{name: "local add, upstream unchanged", v1: "", installed: `["user", "global"]`, v2: "", wantPartition: `["user","global"]`, noteReplace: true},
		{name: "local remove, upstream changes", v1: `["user"]`, installed: "", v2: `["user","global"]`, wantPartition: "", noteReplace: true, noteMerge: true},
		{name: "same", v1: `["user"]`, installed: "-", v2: `["user"]`, wantPartition: `["user"]`},
		{name: "absent", v1: "", installed: "-", v2: "", wantPartition: ""},
	}
	for _, mode := range []string{"replace", "merge"} {
		for _, c := range cases {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				root, mp := installed(t, unit(c.v1, "v1"))
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
				// Where the key is kept, it sits where the installed copy has it.
				if c.wantPartition != "" && !strings.Contains(string(b), "\"runtime\": \"go\",\n  \"partition\": ") {
					t.Errorf("the kept partition moved:\n%s", b)
				}
				if got, _ := os.ReadFile(filepath.Join(root, "tiles", "x", "index.html")); string(got) != "<html>v2</html>\n" {
					t.Errorf("index.html = %q", got)
				}
				want := c.noteReplace
				if mode == "merge" {
					want = c.noteMerge
				}
				joined := strings.Join(a.Notes, "\n")
				if want != (joined != "") || want && !strings.Contains(joined, "tiles/x/xbin.json: partition kept as installed") {
					t.Errorf("notes = %q (want one: %v)", a.Notes, want)
				}
				// The recorded base is upstream's own form: an unchanged
				// upstream offers nothing, whatever partition is kept.
				if offered(t, u2) {
					t.Error("an applied update is still offered")
				}
			})
		}
	}
}

// A merge whose upstream leaves the manifest alone leaves the builder's
// manifest byte for byte — a local "partition" line and its comment
// included — and says nothing about it.
func TestMergeUpstreamUnchangedManifest(t *testing.T) {
	v1 := fstest.MapFS{
		"tiles/x/xbin.json":  {Data: []byte("{\n  \"runtime\": \"go\",\n  \"title\": \"v1\"\n}\n")},
		"tiles/x/index.html": {Data: []byte("<html>v1</html>\n")},
	}
	root, mp := installed(t, v1)
	local := "{\n  \"runtime\": \"go\",\n  \"title\": \"v1\",\n  \"partition\": [\"user\"] // mine\n}\n"
	if err := os.WriteFile(mp, []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	v2 := fstest.MapFS{
		"tiles/x/xbin.json":  v1["tiles/x/xbin.json"],
		"tiles/x/index.html": {Data: []byte("<html>v2</html>\n")},
	}
	a, err := NewUpdater(root, nil, v2).ApplyMerge("scaffold:tiles/x")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(mp); string(b) != local {
		t.Errorf("the manifest changed:\n%s", b)
	}
	if len(a.Notes) != 0 {
		t.Errorf("notes = %q", a.Notes)
	}
}

// A merge with local edits on both sides keeps the installed partition
// beside upstream's other changes; a proposal's series leaves it alone.
func TestMergeAndProposeKeepPartition(t *testing.T) {
	v1 := fstest.MapFS{
		"tiles/x/xbin.json":  {Data: []byte("{\n  \"runtime\": \"go\",\n  \"title\": \"v1\",\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}\n")},
		"tiles/x/index.html": {Data: []byte("<html></html>\n")},
	}
	root, mp := installed(t, v1)
	tile := filepath.Dir(mp)
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
	if !strings.Contains(string(b), "\"c\": 30,\n  \"partition\": [\"user\"]\n}") {
		t.Errorf("the builder's partition line moved:\n%s", b)
	}
	if len(a.Notes) != 1 {
		t.Errorf("notes = %q", a.Notes)
	}
}

// A merge whose conflict takes in ours' partition line merges again without
// the key and carries ours' value once — never upstream's — so resolving
// the markers either way keeps it.
func TestMergeConflictKeepsPartition(t *testing.T) {
	base := "{\n  \"runtime\": \"go\",\n  \"title\": \"v1\"\n}\n"
	ours := "{\n  \"runtime\": \"go\",\n  \"partition\": [\"user\"],\n  \"title\": \"mine\"\n}\n"
	up := "{\n  \"runtime\": \"go\",\n  \"partition\": [\"user\", \"global\"],\n  \"title\": \"v2\"\n}\n"
	theirs, changed, ok := undoPartition([]byte(base), []byte(up), nil)
	if !ok || !changed || strings.Contains(string(theirs), "partition") {
		t.Fatalf("undo: %s %v %v", theirs, changed, ok)
	}
	merged, err := mergeKeeping("xbin.json", []byte(ours), []byte(base), theirs)
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
	for _, side := range []int{sideOurs, sideTheirs} {
		if !keeps(merged, side, pval{[]byte(`["user"]`), true}) {
			doc, _ := conflictSide(merged, side)
			t.Errorf("resolved to side %d: partition lost\n%s", side, doc)
		}
	}
	// Sides that agree merge as they are: the line doesn't move.
	same := "{\n  \"runtime\": \"go\",\n  \"partition\": [\"user\"],\n  \"title\": \"v1\"\n}\n"
	merged, err = mergeKeeping("xbin.json", []byte(same), []byte(same), []byte(strings.Replace(same, "v1", "v2", 1)))
	if err != nil || string(merged) != strings.Replace(same, "v1", "v2", 1) {
		t.Errorf("agreeing sides: %s (%v)", merged, err)
	}
	// Ours' line is in the conflict only because of it: without the key the
	// merge is clean, and ours' value goes back where ours has it.
	ours2 := "{\n  \"runtime\": \"go\",\n  \"partition\": [\"user\"],\n  \"title\": \"v1\"\n}\n"
	theirs2 := "{\n  \"runtime\": \"node\",\n  \"title\": \"v1\"\n}\n"
	merged, err = mergeKeeping("xbin.json", []byte(ours2), []byte(base), []byte(theirs2))
	if want := "{\n  \"runtime\": \"node\",\n  \"partition\": [\"user\"],\n  \"title\": \"v1\"\n}\n"; err != nil || string(merged) != want {
		t.Errorf("clean without the key: %q (%v), want %q", merged, err, want)
	}
}

// The merge-then-replace flow: a merge that conflicts elsewhere leaves
// markers in the live manifest; abandoning it with a replace reads the
// builder's own side of the markers, so upstream's new "partition" never
// lands.
func TestMergeThenReplaceKeepsPartition(t *testing.T) {
	root, mp := installed(t, unit("", "v1"))
	if err := os.WriteFile(mp, manifest("", "mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	u2 := NewUpdater(root, nil, unit(`["user"]`, "v2"))
	if _, err := u2.ApplyMerge("scaffold:tiles/x"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(mp)
	if !strings.Contains(string(b), "<<<<<<<") || strings.Contains(string(b), "partition") {
		t.Fatalf("want markers and no partition after the merge:\n%s", b)
	}
	a, err := u2.ApplyReplace("scaffold:tiles/x")
	if err != nil {
		t.Fatal(err)
	}
	if got := topPartition(t, mp); got != "" {
		b, _ := os.ReadFile(mp)
		t.Errorf("replace after a conflicted merge: partition = %s\n%s", got, b)
	}
	if n := strings.Join(a.Notes, "\n"); !strings.Contains(n, "none, read from its own side of the conflict markers; upstream asks [\"user\"]") {
		t.Errorf("notes = %q", a.Notes)
	}
}

// An installed manifest that doesn't parse (and has no markers) takes the
// partition xbind last read from the tile's code; without one the manifest
// is left as it is and the update stays offered. A workspace that never
// used partitions gets no note.
func TestReplaceUnreadableManifest(t *testing.T) {
	broken := []byte("{\n  \"runtime\": \"go\",,\n")
	for _, c := range []struct {
		name     string
		v2       string
		recorded func(string) ([]byte, bool, bool)
		want     string // the partition written; "-" = left as it is
		note     string
	}{
		{"recorded user", `["user","global"]`, func(tile string) ([]byte, bool, bool) {
			if tile != "tiles/x" {
				t.Errorf("asked about %q", tile)
			}
			return []byte(`["user"]`), true, true
		}, `["user"]`, `(["user"], as xbind last read it: the file doesn't parse; upstream asks ["user","global"])`},
		{"recorded none", `["user"]`, func(string) ([]byte, bool, bool) { return nil, false, true }, "",
			`(none, as xbind last read it: the file doesn't parse; upstream asks ["user"])`},
		{"no partitions anywhere", "", func(string) ([]byte, bool, bool) { return nil, false, true }, "", ""},
		{"can't tell", "", func(string) ([]byte, bool, bool) { return nil, false, false }, "-", "left as it is and the update stays offered"},
		{"no hook", `["user"]`, nil, "-", "left as it is and the update stays offered"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, mp := installed(t, unit("", "v1"))
			if err := os.WriteFile(mp, broken, 0o644); err != nil {
				t.Fatal(err)
			}
			u2 := NewUpdater(root, nil, unit(c.v2, "v2"))
			if c.recorded != nil {
				u2.SetRecordedPartition(c.recorded)
			}
			a, err := u2.ApplyReplace("scaffold:tiles/x")
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "-" {
				if b, _ := os.ReadFile(mp); string(b) != string(broken) {
					t.Errorf("the manifest was written:\n%s", b)
				}
				if !offered(t, u2) {
					t.Error("the update isn't offered any more")
				}
			} else {
				if got := topPartition(t, mp); got != c.want {
					t.Errorf("partition = %q, want %q", got, c.want)
				}
				if offered(t, u2) {
					t.Error("an applied update is still offered")
				}
			}
			if got := strings.Join(a.Notes, "\n"); c.note == "" && got != "" || !strings.Contains(got, c.note) {
				t.Errorf("notes = %q, want %q", a.Notes, c.note)
			}
		})
	}
}

// "Partition" is the manifest's key too (xbind reads keys in any case): a
// replace keeps it, and never adds a second one.
func TestReplaceKeepsPartitionAnyCase(t *testing.T) {
	root, mp := installed(t, unit("", "v1"))
	local := "{\n  // upstream's comment\n  \"runtime\": \"go\",\n  \"Partition\": [\"user\"],\n  \"title\": \"v1\"\n}\n"
	if err := os.WriteFile(mp, []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v2 := range []string{"", `["user","global"]`} {
		a, err := NewUpdater(root, nil, unit(v2, "v2")).ApplyReplace("scaffold:tiles/x")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(mp)
		if got := topPartition(t, mp); got != `["user"]` || strings.Count(strings.ToLower(string(b)), `"partition"`) != 1 {
			t.Errorf("upstream %q: partition = %q\n%s", v2, got, b)
		}
		if len(a.Notes) != 1 {
			t.Errorf("upstream %q: notes = %q", v2, a.Notes)
		}
	}
}

// A proposal undoes upstream's removal of the key where the base has it, so
// the series changes only what else upstream changed and `git am --3way`
// applies it to a tile whose partition differs from the base; a proposal
// whose only change is the partition has nothing to propose.
func TestProposePartitionInPlace(t *testing.T) {
	body := func(partition, title string) []byte {
		p := ""
		if partition != "" {
			p = "  \"partition\": " + partition + ",\n"
		}
		return []byte("{\n  \"runtime\": \"go\",\n" + p + "  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3,\n  \"d\": 4,\n  \"title\": \"" + title + "\"\n}\n")
	}
	u := func(partition, title string) fstest.MapFS {
		return fstest.MapFS{
			"tiles/x/xbin.json":  {Data: body(partition, title)},
			"tiles/x/index.html": {Data: []byte("<html></html>\n")},
		}
	}
	root, mp := installed(t, u(`["user"]`, "v1"))
	tile := filepath.Dir(mp)
	if err := os.WriteFile(mp, body(`["user", "global"]`, "v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, tile, "init", "-q", "-b", "main")
	gitT(t, tile, "add", "-A")
	gitT(t, tile, "commit", "-qm", "local")

	prop, err := NewUpdater(root, nil, u("", "v2")).Propose("scaffold:tiles/x")
	if err != nil {
		t.Fatal(err)
	}
	var changed []string
	for _, ln := range strings.Split(prop.Series, "\n") {
		if (strings.HasPrefix(ln, "+ ") || strings.HasPrefix(ln, "- ")) && !strings.HasPrefix(ln, "---") {
			changed = append(changed, ln)
		}
	}
	if strings.Join(changed, "\n") != "-  \"title\": \"v1\"\n+  \"title\": \"v2\"" {
		t.Errorf("the series changes more than the title: %q\n%s", changed, prop.Series)
	}
	mbox := filepath.Join(t.TempDir(), "s.mbox")
	if err := os.WriteFile(mbox, []byte(prop.Series), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, tile, "am", "--3way", mbox)
	if got := topPartition(t, mp); got != `["user","global"]` {
		t.Errorf("after git am: partition = %s", got)
	}

	// Upstream changes nothing but the partition: nothing to propose.
	root, _ = installed(t, u(`["user"]`, "v1"))
	prop, err = NewUpdater(root, nil, u("", "v1")).Propose("scaffold:tiles/x")
	if err != nil {
		t.Fatal(err)
	}
	if prop.Series != "" || len(prop.Notes) != 1 || !strings.Contains(prop.Notes[0], `partition kept as installed (["user"]; upstream asks none)`) {
		t.Errorf("only a partition change: series %q, notes %q", prop.Series, prop.Notes)
	}
}

func TestConflictSide(t *testing.T) {
	doc := "{\n<<<<<<< ours\n  \"a\": 1\n||||||| base\n  \"a\": 0\n=======\n  \"a\": 2\n>>>>>>> theirs\n}\n"
	for side, want := range map[int]string{sideOurs: "{\n  \"a\": 1\n}\n", sideBase: "{\n  \"a\": 0\n}\n", sideTheirs: "{\n  \"a\": 2\n}\n"} {
		if got, ok := conflictSide([]byte(doc), side); !ok || string(got) != want {
			t.Errorf("side %d: %q %v", side, got, ok)
		}
	}
	for _, bad := range []string{"{\"a\": 1}", "{\n<<<<<<< ours\n\"a\": 1\n}\n", "{\n>>>>>>> x\n}\n"} {
		if _, ok := conflictSide([]byte(bad), sideOurs); ok {
			t.Errorf("%q: resolved", bad)
		}
	}
}
