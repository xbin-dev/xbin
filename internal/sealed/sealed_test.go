package sealed

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// fixture is a sealed component's source: a Go backend, one requested slot
// in the table's list and one outside it, a role and a provided interface
// (a sealed component is never a target: both must go), a scope.
func fixture(body string) fs.FS {
	return fstest.MapFS{
		"xbin.json": {Data: []byte(`{
  // a comment: manifests are JSONC
  "runtime": "go", "title": "Fixture", "chrome": true, "partition": ["user"],
  "uses": [{"target": "res:apps/fix/db", "role": "writer"}],
  "interfaces": {"llm": {"kind": "http", "service": "openai"}, "net": {"kind": "net"}},
  "provides": {"inbox": {"kind": "http", "service": "inbox", "role": "writer"}},
  "expose": {"roles": {"writer": "write"}}
}`)},
		"scope.json":      {Data: []byte(`{"resources":{"db":{"type":"kv"}}}`)},
		"index.html":      {Data: []byte("<html>" + body + "</html>")},
		"go.mod.tile":     {Data: []byte("module fix\n\ngo 1.24\n")},
		"backend/main.go": {Data: []byte("package main\n\nfunc main() {}\n")},
	}
}

func spec(body string) Spec {
	return Spec{Name: "fix", Source: fixture(body), Authored: "apps/fix", Slots: []string{"llm"},
		Providers: map[string][]string{"llm": {"apps/llm", "apps/gw"}}}
}

func open(t *testing.T, ws string, o Options, specs ...Spec) *Set {
	t.Helper()
	o.Workspace = ws
	if o.Release == "" {
		o.Release = "v-test"
	}
	s, err := Open(specs, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTableValidation(t *testing.T) {
	bad := map[string]Spec{
		"a reserved identity":        {Name: "cron", Source: fixture("")},
		"the mail doorbell's":        {Name: "mail", Source: fixture("")},
		"an uppercase name":          {Name: "Fix", Source: fixture("")},
		"a nested name":              {Name: "a/b", Source: fixture("")},
		"no name":                    {Source: fixture("")},
		"a name CompKey would cut":   {Name: "abcdefghijklmnopqrst", Source: fixture("")}, // 20 characters
		"a provider for no slot":     {Name: "fix", Source: fixture(""), Providers: map[string][]string{"llm": {"apps/gw"}}},
		"a sealed provider":          {Name: "fix", Source: fixture(""), Slots: []string{"llm"}, Providers: map[string][]string{"llm": {"xbin/other"}}},
		"a provider's instance":      {Name: "fix", Source: fixture(""), Slots: []string{"llm"}, Providers: map[string][]string{"llm": {"apps/gw#a"}}},
		"a slot it doesn't have":     {Name: "fix", Source: fixture(""), Slots: []string{"mcp"}},
		"global without user":        {Name: "fix", Source: fixture(""), Partition: []string{"global"}},
		"a grant without a role":     {Name: "fix", Source: fixture(""), Grants: []registry.Use{{Target: "cap:open-links"}}},
		"a source without xbin.json": {Name: "fix", Source: fstest.MapFS{"index.html": {Data: []byte("x")}}},
	}
	for what, sp := range bad {
		if _, err := Load([]Spec{sp}); err == nil {
			t.Errorf("a table row with %s loaded", what)
		}
	}
	if _, err := Load([]Spec{spec(""), spec("")}); err == nil {
		t.Error("a repeated name loaded")
	}
	// The longest name still has an uncut key: no tile path but the
	// top-level xbin~<name> shares its readable part, and that one's hash
	// differs (Load checks it) — a tile path can't be ground into a sealed
	// component's key.
	long := "abcdefghijklmnopqrs" // util.SealedNameMax characters
	set, err := Load([]Spec{{Name: long, Source: fixture("")}})
	if err != nil || len(long) != util.SealedNameMax {
		t.Fatalf("a name of the longest length: %v", err)
	}
	key := util.CompKey(set.Components()[0].Path)
	if !strings.HasPrefix(key, "xbin~"+long+"-") || key == util.CompKey("xbin~"+long) || key == util.CompKey("xbin~"+long+"/x") || key == util.CompKey("xbin~"+long+"x") {
		t.Errorf("its key %s is cut, or a tile's", key)
	}
	if util.SealedComponentPath("xbin/"+long+"t") || !util.SealedComponentPath("xbin/"+long) {
		t.Error("util.SealedComponentPath and the table disagree about the longest name")
	}
	if !fixtureTable && len(Table()) != 0 {
		t.Error("the compiled-in table is not empty: a workspace in the wild would see a component appear (a release that ships one edits this test with it)")
	}
}

func TestRenderAndRegistry(t *testing.T) {
	ws := t.TempDir()
	s := open(t, ws, Options{Isolate: true, Dev: true}, func() Spec {
		sp := spec("one")
		sp.Partition = []string{"user", "global"}
		sp.Grants = []registry.Use{{Target: "cap:open-links", Role: "writer"}}
		sp.Capabilities.Delegate = true
		return sp
	}())
	c := s.Components()[0]
	if c.Path != "xbin/fix" || c.Runtime != "go" || !c.Partitioned {
		t.Fatalf("component: %+v", c)
	}
	if _, ok := c.Files["go.mod"]; !ok || !strings.Contains(string(c.Files["go.mod"]), "module xbin/fix") {
		t.Errorf("go.mod.tile wasn't rendered as go.mod with the reserved module path: %q", c.Files["go.mod"])
	}
	if strings.Contains(string(c.Files["xbin.json"]), "apps/fix") {
		t.Error("the authored path survives in the rendered manifest")
	}
	if sp, ok := s.Lookup("xbin/fix"); !ok || !sp.Capabilities.Delegate {
		t.Errorf("Lookup: %+v %v", sp, ok)
	}
	if _, ok := s.Lookup("apps/fix"); ok {
		t.Error("Lookup answers for a workspace path")
	}

	reg, err := registry.OpenSealed(ws, s.Registry)
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := reg.Component("xbin/fix")
	if !ok || !rc.Sealed || rc.Scope != "xbin/fix" || rc.ManifestErr != "" {
		t.Fatalf("registered: %+v", rc)
	}
	m := rc.Manifest
	if m.Chrome || m.Expose != nil || m.Provides != nil || m.Exposes != nil || m.Template != nil {
		t.Errorf("the manifest keeps what the table doesn't give: chrome=%v expose=%v provides=%v", m.Chrome, m.Expose, m.Provides)
	}
	if _, has := m.Interfaces["llm"]; !has || len(m.Interfaces) != 1 {
		t.Errorf("slots: %v, want llm alone", m.Interfaces)
	}
	if got := []string(m.Partition); len(got) != 2 {
		t.Errorf("partition: %v, want the table's", got)
	}
	// the table's grant is a row; nothing the file says about the component is
	var granted bool
	for _, g := range reg.Workspace().Grants {
		granted = granted || g.From == "xbin/fix" && g.Target == "cap:open-links" && g.ApprovedBy == "xbin"
	}
	if !granted {
		t.Error("the table's grant isn't in the grant table")
	}
}

// What a workspace xbin.json says about a sealed component is not read:
// grants from it, grants on it or its resources, bindings of slots it
// doesn't have, bindings that name it as a provider.
func TestWorkspaceFileSpeaksForNoSealedComponent(t *testing.T) {
	ws := t.TempDir()
	file := `{"schema":1,
 "grants":[
  {"from":"xbin/fix","target":"xbin","role":"admin"},
  {"from":"apps/a","target":"xbin/fix","role":"admin"},
  {"from":"apps/a","target":"res:xbin/fix/db","role":"writer"},
  {"from":"apps/a","target":"apps/b","role":"reader"}],
 "bindings":{
  "xbin/fix":{"llm":"apps/llm","net":"internet","@archive":"apps/arch"},
  "apps/a":{"inbox":["xbin/fix","apps/b"],"other":"xbin/fix"}},
 "lifecycle":{"xbin/fix":"offloaded-full","apps/a":"offloaded"}}`
	if err := os.WriteFile(filepath.Join(ws, "xbin.json"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	s := open(t, ws, Options{Dev: true}, spec(""))
	reg, err := registry.OpenSealed(ws, s.Registry)
	if err != nil {
		t.Fatal(err)
	}
	w := reg.Workspace()
	if len(w.Grants) != 1 || w.Grants[0].From != "apps/a" || w.Grants[0].Target != "apps/b" {
		t.Errorf("grants read from the file: %+v", w.Grants)
	}
	if b := w.Bindings["xbin/fix"]; len(b) != 1 || b["llm"].First() != "apps/llm" {
		t.Errorf("the sealed component's bindings: %+v, want its declared slot alone", b)
	}
	if b := w.Bindings["apps/a"]; len(b) != 1 || len(b["inbox"]) != 1 || b["inbox"].First() != "apps/b" {
		t.Errorf("bindings naming the sealed component as provider survive: %+v", b)
	}
	if reg.LifecycleState("xbin/fix") != registry.StateDisabled || w.Lifecycle["xbin/fix"] != registry.StateDisabled {
		t.Errorf("an offloaded state in the file reads %q: a sealed component is off, never offloaded", reg.LifecycleState("xbin/fix"))
	}
	if reg.LifecycleState("apps/a") != registry.StateOffloaded {
		t.Error("a tile's offloaded state changed")
	}
	// and the file is as it was
	if b, _ := os.ReadFile(filepath.Join(ws, "xbin.json")); string(b) != file {
		t.Error("the workspace xbin.json was rewritten")
	}
}

func TestMaterializeVerifiesAndReplaces(t *testing.T) {
	ws := t.TempDir()
	s := open(t, ws, Options{Dev: true}, spec("one"))
	dir := s.Components()[0].Dir
	if filepath.Dir(dir) != Dir(ws, "fix") {
		t.Fatalf("materialized at %s", dir)
	}
	// tampered, extra and missing files are all put right by the next boot
	_ = os.Chmod(filepath.Join(dir, "index.html"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("tampered"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "extra.js"), []byte("x"), 0o644)
	open(t, ws, Options{Dev: true}, spec("one"))
	if b, _ := os.ReadFile(filepath.Join(dir, "index.html")); string(b) != "<html>one</html>" {
		t.Errorf("a tampered file survived a boot: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "extra.js")); err == nil {
		t.Error("a planted file survived a boot")
	}
	// other code: another digest, the old tree gone
	s2 := open(t, ws, Options{Dev: true}, spec("two"))
	if s2.Components()[0].Dir == dir {
		t.Fatal("other code has the same digest")
	}
	ents, _ := os.ReadDir(Dir(ws, "fix"))
	if len(ents) != 1 {
		t.Errorf("%d trees after an upgrade, want the running release's alone", len(ents))
	}
}

func TestHolds(t *testing.T) {
	part := func() Spec { sp := spec(""); sp.Partition = []string{"user"}; return sp }

	t.Run("production without a build", func(t *testing.T) {
		ws := t.TempDir()
		s := open(t, ws, Options{Isolate: true, BinDir: filepath.Join(ws, "nobin")}, spec(""))
		h, held := s.Hold("xbin/fix")
		if !held || h.Code != HoldNoBuild || !strings.Contains(h.Reason, "never compiles") {
			t.Fatalf("hold: %+v %v", h, held)
		}
		if why := s.Admit("xbin/fix"); why != h.Reason {
			t.Errorf("Admit: %q", why)
		}
		if s.Registry()[0].NoData != "" {
			t.Error("a component without a build declares no resources: its data is its own")
		}
	})
	t.Run("the install's build is picked up, by digest", func(t *testing.T) {
		ws, bin := t.TempDir(), t.TempDir()
		probe, _ := Load([]Spec{spec("")})
		id := probe.Components()[0].ID()
		_ = os.WriteFile(filepath.Join(bin, "fix-000000000000000000000000"), []byte("#!/bin/sh\n"), 0o755) // other source's
		s := open(t, ws, Options{Isolate: true, BinDir: bin}, spec(""))
		if _, held := s.Hold("xbin/fix"); !held {
			t.Fatal("a build of other source was accepted")
		}
		_ = os.WriteFile(filepath.Join(bin, "fix-"+id), []byte("#!/bin/sh\n"), 0o644) // not executable
		if _, held := open(t, ws, Options{Isolate: true, BinDir: bin}, spec("")).Hold("xbin/fix"); !held {
			t.Fatal("a non-executable file was accepted as the build")
		}
		_ = os.Chmod(filepath.Join(bin, "fix-"+id), 0o755)
		// executable and named for this source, but nothing lists it: a
		// name alone is not trusted
		if h, held := open(t, ws, Options{Isolate: true, BinDir: bin}, spec("")).Hold("xbin/fix"); !held || !strings.Contains(h.Reason, "isn't listed in sealed.json") {
			t.Fatalf("an unlisted file was accepted as the build: %+v", h)
		}
		list := func(source, sum string) {
			t.Helper()
			m := BuildManifest{Schema: 1, Builds: map[string]BuildEntry{"fix-" + id: {Component: "xbin/fix", Source: source, SHA256: sum, Size: int64(len("#!/bin/sh\n"))}}}
			b, _ := json.Marshal(m)
			if err := os.WriteFile(filepath.Join(bin, ManifestName), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		sum, _, _ := FileSum(filepath.Join(bin, "fix-"+id))
		list("another source's digest", sum)
		if h, held := open(t, ws, Options{Isolate: true, BinDir: bin}, spec("")).Hold("xbin/fix"); !held || !strings.Contains(h.Reason, "a build of other source") {
			t.Fatalf("a file listed for other source was accepted: %+v", h)
		}
		list(probe.Components()[0].Digest, strings.Repeat("0", 64))
		if h, held := open(t, ws, Options{Isolate: true, BinDir: bin}, spec("")).Hold("xbin/fix"); !held || !strings.Contains(h.Reason, "SHA-256 differs") {
			t.Fatalf("a file that isn't the one listed was accepted: %+v", h)
		}
		list(probe.Components()[0].Digest, sum)
		s = open(t, ws, Options{Isolate: true, BinDir: bin}, spec(""))
		if h, held := s.Hold("xbin/fix"); held {
			t.Fatalf("held with its build present: %+v", h)
		}
		if got := s.Components()[0].Prebuilt; got != filepath.Join(bin, "fix-"+id) {
			t.Errorf("Prebuilt = %q", got)
		}
	})
	t.Run("a development xbind compiles", func(t *testing.T) {
		s := open(t, t.TempDir(), Options{Dev: true}, spec(""))
		if h, held := s.Hold("xbin/fix"); held {
			t.Fatalf("held: %+v", h)
		}
	})
	t.Run("partitions without sandboxes", func(t *testing.T) {
		s := open(t, t.TempDir(), Options{BinDir: ""}, part())
		if h, _ := s.Hold("xbin/fix"); h.Code != HoldNeedsSandboxes {
			t.Fatalf("production, no sandboxes: %+v", h)
		}
		if got := s.Registry()[0].Partition; len(got) != 1 {
			t.Errorf("held, it still asks for its partition mode: %v", got)
		}
		// a development xbind runs one global instance instead
		s = open(t, t.TempDir(), Options{Dev: true}, part())
		if h, held := s.Hold("xbin/fix"); held {
			t.Fatalf("development: %+v", h)
		}
		if s.Components()[0].Partitioned || s.Registry()[0].Partition != nil {
			t.Error("development without sandboxes: want one unpartitioned instance")
		}
		// with sandboxes it is partitioned in both
		if s = open(t, t.TempDir(), Options{Dev: true, Isolate: true}, part()); !s.Components()[0].Partitioned {
			t.Error("development with sandboxes: want people's partitions")
		}
	})
}

func readStamp(t *testing.T, ws string) Stamp {
	t.Helper()
	b, err := os.ReadFile(stampPath(ws, "fix"))
	if err != nil {
		t.Fatal(err)
	}
	var st Stamp
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStampClaimRaiseAndDowngrade(t *testing.T) {
	ws := t.TempDir()
	v := func(data int) Spec { sp := spec(""); sp.Data = data; return sp }

	// first boot: claimed at the release's format
	s := open(t, ws, Options{Dev: true, Release: "v1"}, v(0))
	if st := readStamp(t, ws); st.Data != 1 || st.Release != "v1" || st.Digest == "" {
		t.Fatalf("claimed stamp: %+v", st)
	}
	if why := s.Admit("xbin/fix"); why != "" {
		t.Fatal(why)
	}
	// its data now exists at its key: with the stamp, it is its own
	for _, d := range DataDirs(ws, "xbin/fix") {
		_ = os.MkdirAll(filepath.Join(d, "db"), 0o755)
	}

	// an upgrade to format 3: not raised by booting, raised by the first start
	s = open(t, ws, Options{Dev: true, Release: "v3"}, v(3))
	if _, held := s.Hold("xbin/fix"); held {
		t.Fatal("held after an upgrade")
	}
	if st := readStamp(t, ws); st.Data != 1 {
		t.Fatalf("a boot alone raised the stamp: %+v", st)
	}
	if why := s.Admit("xbin/fix"); why != "" {
		t.Fatal(why)
	}
	if st := readStamp(t, ws); st.Data != 3 || st.Release != "v3" {
		t.Fatalf("the first start didn't raise the stamp: %+v", st)
	}

	// a downgrade to format 2: held, the data and the stamp untouched
	s = open(t, ws, Options{Dev: true, Release: "v2"}, v(2))
	h, held := s.Hold("xbin/fix")
	if !held || h.Code != HoldNewerData {
		t.Fatalf("after a downgrade: %+v %v", h, held)
	}
	for _, want := range []string{"xbin v3", "data format 3", "(v2)", "up to format 2", "Nothing was changed"} {
		if !strings.Contains(h.Reason, want) {
			t.Errorf("the hold's reason lacks %q: %s", want, h.Reason)
		}
	}
	if strings.Contains(strings.ToLower(h.Reason), "sealed") {
		t.Errorf("a user-facing reason says sealed: %s", h.Reason)
	}
	if s.Admit("xbin/fix") == "" {
		t.Fatal("admitted over newer data")
	}
	if st := readStamp(t, ws); st.Data != 3 || st.Release != "v3" {
		t.Errorf("the held release rewrote the stamp: %+v", st)
	}
	if s.Registry()[0].NoData == "" {
		t.Error("held over newer data, its scope still declares resources")
	}
	// back on the newer release it runs again
	s = open(t, ws, Options{Dev: true, Release: "v3"}, v(3))
	if h, held := s.Hold("xbin/fix"); held {
		t.Fatalf("held on the release that wrote the data: %+v", h)
	}
}

func TestForeignDataHolds(t *testing.T) {
	ws := t.TempDir()
	// a workspace scope xbin~fix held this key before the upgrade
	theirs := filepath.Join(ws, "data", "resources-enc", "xbin~fix", "db")
	if err := os.MkdirAll(theirs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(theirs, "gocryptfs.conf"), []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, ws, Options{Dev: true}, spec(""))
	h, held := s.Hold("xbin/fix")
	if !held || h.Code != HoldForeignData || !strings.Contains(h.Reason, "data/resources-enc/xbin~fix") || !strings.Contains(h.Reason, "not read, moved or deleted") {
		t.Fatalf("hold: %+v %v", h, held)
	}
	if _, err := os.Stat(stampPath(ws, "fix")); err == nil {
		t.Error("the component claimed a key that holds foreign data")
	}
	if b, _ := os.ReadFile(filepath.Join(theirs, "gocryptfs.conf")); string(b) != "theirs" {
		t.Error("the foreign data was touched")
	}
	sc := s.Registry()[0]
	if sc.NoData == "" {
		t.Fatal("held over foreign data, its scope still declares resources")
	}
	reg, err := registry.OpenSealed(ws, s.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if sm := reg.Scopes()["xbin/fix"]; sm == nil || len(sm.Resources) != 0 {
		t.Errorf("the held scope's resources: %+v", sm)
	}
	if c, _ := reg.Component("xbin/fix"); !strings.Contains(c.ManifestErr, "system component xbin/fix is held") {
		t.Errorf("the hold isn't the row's error: %q", c.ManifestErr)
	}
	// and nothing is restored into it while it is held: the data at its
	// key is not this code's to overwrite
	if err := s.RestoreRefusal("xbin/fix", 1, "v1"); err == nil || !strings.Contains(err.Error(), "nothing is restored while its data is held") {
		t.Errorf("a restore into a component held over foreign data: %v", err)
	}
	// moved away by the admin: the next boot claims the key
	if err := os.Rename(filepath.Join(ws, "data", "resources-enc", "xbin~fix"), filepath.Join(ws, "data", "moved-away")); err != nil {
		t.Fatal(err)
	}
	if h, held := open(t, ws, Options{Dev: true}, spec("")).Hold("xbin/fix"); held {
		t.Fatalf("still held: %+v", h)
	}
}

func TestRestoreGoesThroughTheStamp(t *testing.T) {
	ws := t.TempDir()
	sp := spec("")
	sp.Data = 2
	s := open(t, ws, Options{Dev: true, Release: "v2"}, sp)
	if err := s.RestoreRefusal("xbin/fix", 3, "v3"); err == nil || !strings.Contains(err.Error(), "data format 3") {
		t.Fatalf("a newer backup was accepted: %v", err)
	}
	if err := s.RestoreRefusal("xbin/fix", 1, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreRefusal("apps/tile", 9, "v9"); err != nil {
		t.Fatal("a tile's restore asks the stamp")
	}
	if err := s.RestoreRefusal("xbin/none", 1, "v1"); err == nil {
		t.Error("a restore into a name under xbin/ that is no component of this release")
	}
	_ = s.Admit("xbin/fix")
	if s.DataFormat("xbin/fix") != 2 {
		t.Fatal("not at format 2")
	}
}

// A slot's default binding (Spec.Defaults) applies while the workspace file
// says nothing about the slot and the provider is available; whatever the
// file says — a binding, or the empty entry an unbind leaves — wins.
func TestDefaultBindings(t *testing.T) {
	for what, sp := range map[string]Spec{
		"a slot the table doesn't give it": {Name: "fix", Source: fixture(""), Slots: []string{"llm"}, Defaults: map[string]string{"net": "internet"}},
		"a tile's path":                    {Name: "fix", Source: fixture(""), Slots: []string{"llm"}, Defaults: map[string]string{"llm": "apps/llm-gw"}},
		"nothing":                          {Name: "fix", Source: fixture(""), Slots: []string{"llm"}, Defaults: map[string]string{"llm": ""}},
	} {
		if _, err := Load([]Spec{sp}); err == nil {
			t.Errorf("a default binding to %s loaded", what)
		}
	}
	sp := spec("")
	sp.Defaults = map[string]string{"llm": "xbin:llm"}
	bound := func(file string, available bool) (refs []string, applied bool, after string) {
		t.Helper()
		ws := t.TempDir()
		if err := os.WriteFile(filepath.Join(ws, "xbin.json"), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		s := open(t, ws, Options{Dev: true}, sp)
		reg, err := registry.OpenSealed(ws, s.Registry)
		if err != nil {
			t.Fatal(err)
		}
		reg.SealedProvider = func(ref string) bool { return available && ref == "xbin:llm" }
		ref, applied := reg.SealedDefault("xbin/fix", "llm")
		if ref != "xbin:llm" {
			t.Fatalf("SealedDefault ref = %q", ref)
		}
		b, _ := os.ReadFile(filepath.Join(ws, "xbin.json"))
		return reg.Workspace().Bindings["xbin/fix"]["llm"].Refs(), applied, string(b)
	}
	cases := []struct {
		what, file string
		available  bool
		want       []string
		applied    bool
	}{
		{"nothing in the file, the provider available", `{"schema":1}`, true, []string{"xbin:llm"}, true},
		{"nothing in the file, the provider not available", `{"schema":1}`, false, nil, false},
		{"an admin's binding", `{"schema":1,"bindings":{"xbin/fix":{"llm":"apps/gw"}}}`, true, []string{"apps/gw"}, false},
		{"an admin's unbind", `{"schema":1,"bindings":{"xbin/fix":{"llm":[]}}}`, true, nil, false},
		// a provider the release doesn't name reads as no binding at all:
		// the admin's row stands in the file, so no default takes its place
		{"a hand-edited binding to a provider the table doesn't allow", `{"schema":1,"bindings":{"xbin/fix":{"llm":"apps/evil"}}}`, true, nil, false},
		{"…among allowed ones", `{"schema":1,"bindings":{"xbin/fix":{"llm":["apps/gw","apps/evil"]}}}`, true, nil, false},
		{"another component's binding only", `{"schema":1,"bindings":{"apps/a":{"llm":"apps/gw"}}}`, true, []string{"xbin:llm"}, true},
	}
	for _, c := range cases {
		refs, applied, after := bound(c.file, c.available)
		if strings.Join(refs, ",") != strings.Join(c.want, ",") || applied != c.applied {
			t.Errorf("%s: bound to %v (by default: %v), want %v (%v)", c.what, refs, applied, c.want, c.applied)
		}
		if after != c.file {
			t.Errorf("%s: the workspace xbin.json was rewritten: %s", c.what, after)
		}
	}
}
