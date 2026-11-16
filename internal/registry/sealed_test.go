package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sealedReg is a registry over a workspace whose xbin.json is file, with
// one sealed component xbin/fix: an llm slot it may bind to xbin:llm (its
// default) or apps/gw.
func sealedReg(t *testing.T, file string) (*Registry, string) {
	t.Helper()
	ws := t.TempDir()
	dir := filepath.Join(t.TempDir(), "fix")
	for p, body := range map[string]string{
		filepath.Join(ws, "xbin.json"):               file,
		filepath.Join(ws, "apps/a/index.html"):       "<html>a</html>",
		filepath.Join(ws, "apps/a/xbin.json"):        `{"interfaces":{"llm":{"kind":"http","service":"openai"}}}`,
		filepath.Join(dir, "index.html"):             "<html>sealed</html>",
		filepath.Join(ws, "apps/gw/index.html"):      "<html>gw</html>",
		filepath.Join(ws, "apps/gw/xbin.json"):       `{"provides":{"api":{"kind":"http","service":"openai","role":"reader"}},"expose":{"roles":{"reader":"read"}}}`,
		filepath.Join(ws, "apps/a/scope.json"):       `{"importMap":{"mine":"/c/apps/a/mine.js"}}`,
		filepath.Join(ws, "apps/gw/placeholder.txt"): "",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code := SealedCode{Path: "xbin/fix", ID: "0123456789abcdef01234567", Dir: dir,
		Manifest:  []byte(`{"interfaces":{"llm":{"kind":"http","service":"openai"},"net":{"kind":"net"}}}`),
		Scope:     []byte(`{"resources":{"db":{"type":"kv"}},"importMap":{"own":"/c/xbin/fix/own.js"}}`),
		Slots:     []string{"llm"},
		Defaults:  map[string]string{"llm": "xbin:llm"},
		Providers: map[string][]string{"llm": {"apps/gw"}},
	}
	reg, err := OpenSealed(ws, func() []SealedCode { return []SealedCode{code} })
	if err != nil {
		t.Fatal(err)
	}
	return reg, ws
}

// A sealed component's documents get no entry of the workspace file's
// import map: a key there would point the release's frontend at a
// workspace's code.
func TestSealedDocumentsTakeNoWorkspaceImportMap(t *testing.T) {
	reg, _ := sealedReg(t, `{"schema":1,"importMap":{"lit":"/c/apps/a/evil-lit.js","/c/xbin/fix/":"/c/apps/a/","shared":"/c/apps/a/shared.js"}}`)
	sealed, ok := reg.Component("xbin/fix")
	if !ok || !sealed.Sealed || sealed.SealedID != "0123456789abcdef01234567" {
		t.Fatalf("the sealed component: %+v", sealed)
	}
	m := reg.ImportMapFor(sealed)
	if len(m) != 1 || m["own"] != "/c/xbin/fix/own.js" {
		t.Errorf("a sealed document's import map: %v, want its embedded scope's entry alone", m)
	}
	// a tile's is as before: the workspace's, overlaid with its scope's
	tile, _ := reg.Component("apps/a")
	if m := reg.ImportMapFor(tile); m["lit"] != "/c/apps/a/evil-lit.js" || m["shared"] == "" || m["mine"] != "/c/apps/a/mine.js" {
		t.Errorf("a tile's import map: %v", m)
	}
	if m := reg.ImportMapFor(nil); m["lit"] == "" {
		t.Errorf("the workspace's own import map: %v", m)
	}
}

// The workspace file's writer takes no row about a sealed component that
// the file may not hold, whatever the caller checked — and takes the ones
// an admin may write.
func TestMutateWorkspaceTakesNoSealedRow(t *testing.T) {
	reg, ws := sealedReg(t, `{"schema":1}`)
	refused := map[string]func(*WorkspaceManifest){
		"a grant on it": func(w *WorkspaceManifest) {
			w.Grants = append(w.Grants, Grant{From: "apps/a", Target: "xbin/fix", Role: "reader"})
		},
		"a grant on its resource": func(w *WorkspaceManifest) {
			w.Grants = append(w.Grants, Grant{From: "apps/a", Target: "res:xbin/fix/db", Role: "reader"})
		},
		"a grant on its source": func(w *WorkspaceManifest) {
			w.Grants = append(w.Grants, Grant{From: "apps/a", Target: "code:xbin/fix", Role: "reader"})
		},
		"a grant from it": func(w *WorkspaceManifest) {
			w.Grants = append(w.Grants, Grant{From: "xbin/fix", Target: "apps/gw", Role: "reader"})
		},
		"a grant under the namespace": func(w *WorkspaceManifest) {
			w.Grants = append(w.Grants, Grant{From: "apps/a", Target: "xbin/other", Role: "reader"})
		},
		"it as a provider": func(w *WorkspaceManifest) {
			w.Bindings = map[string]map[string]Binding{"apps/a": {"llm": BindTo("xbin/fix")}}
		},
		"it as a provider's instance": func(w *WorkspaceManifest) {
			w.Bindings = map[string]map[string]Binding{"apps/a": {"llm": BindTo("xbin/fix#x")}}
		},
		"its slot to a provider its release doesn't name": func(w *WorkspaceManifest) {
			w.Bindings = map[string]map[string]Binding{"xbin/fix": {"llm": BindTo("apps/a")}}
		},
		"a slot its release doesn't give it": func(w *WorkspaceManifest) {
			w.Bindings = map[string]map[string]Binding{"xbin/fix": {"net": BindTo("internet")}}
		},
		"a pseudo-slot": func(w *WorkspaceManifest) {
			w.Bindings = map[string]map[string]Binding{"xbin/fix": {"@archive": BindTo("apps/gw")}}
		},
		"a slot of a name that is no component": func(w *WorkspaceManifest) {
			w.Bindings = map[string]map[string]Binding{"xbin/none": {"llm": BindTo("apps/gw")}}
		},
		"interface instances of it": func(w *WorkspaceManifest) { w.IfaceInstances = map[string]map[string]string{"xbin/fix": {"a": "/a"}} },
		"ingress hosts of it":       func(w *WorkspaceManifest) { w.IngressHosts = map[string][]string{"xbin/fix": {"a.example.com"}} },
		"offloading it":             func(w *WorkspaceManifest) { w.Lifecycle = map[string]string{"xbin/fix": StateOffloaded} },
	}
	for what, change := range refused {
		before, _ := os.ReadFile(filepath.Join(ws, "xbin.json"))
		err := reg.MutateWorkspace(change)
		var row *SealedRowError
		if !errors.As(err, &row) {
			t.Errorf("%s: MutateWorkspace = %v, want a *SealedRowError", what, err)
		}
		if after, _ := os.ReadFile(filepath.Join(ws, "xbin.json")); string(after) != string(before) {
			t.Errorf("%s: the file was written: %s", what, after)
		}
		if w := reg.Workspace(); len(w.Grants) != 0 || len(w.IfaceInstances) != 0 || len(w.IngressHosts) != 0 || len(w.Lifecycle) != 0 || len(w.Bindings["apps/a"]) != 0 {
			t.Errorf("%s: the registry's manifest kept the change: %+v", what, w)
		}
	}
	allowed := map[string]func(*WorkspaceManifest){
		"its slot to a provider its release names": func(w *WorkspaceManifest) {
			w.Bindings = map[string]map[string]Binding{"xbin/fix": {"llm": BindTo("apps/gw")}}
		},
		"its slot to its default": func(w *WorkspaceManifest) {
			w.Bindings = map[string]map[string]Binding{"xbin/fix": {"llm": BindTo("xbin:llm")}}
		},
		"its slot unbound":         func(w *WorkspaceManifest) { w.Bindings = map[string]map[string]Binding{"xbin/fix": {"llm": Binding{}}} },
		"switching it off":         func(w *WorkspaceManifest) { w.Lifecycle = map[string]string{"xbin/fix": StateDisabled} },
		"a tile's grant on a tile": func(w *WorkspaceManifest) { w.Grants = []Grant{{From: "apps/a", Target: "apps/gw", Role: "reader"}} },
	}
	for what, change := range allowed {
		if err := reg.MutateWorkspace(change); err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "xbin.json")); !strings.Contains(string(b), `"apps/gw"`) {
		t.Errorf("the allowed changes were not written: %s", b)
	}
}

// A row already in a hand-edited file is ignored on every read, never
// honoured, and does not stop other changes from being written.
func TestSealedRowsOfAHandEditedFile(t *testing.T) {
	reg, _ := sealedReg(t, `{"schema":1,
 "grants":[{"from":"apps/a","target":"code:xbin/fix","role":"reader"},{"from":"apps/a","target":"xbin/fix","role":"reader"}],
 "bindings":{"xbin/fix":{"llm":"apps/a"},"apps/a":{"llm":"xbin/fix"}},
 "ifaceInstances":{"xbin/fix":{"a":"/a"},"apps/gw":{"b":"/b"}},
 "ingressHosts":{"xbin/fix":["a.example.com"]}}`)
	reg.SealedProvider = func(string) bool { return true }
	w := reg.Workspace()
	if len(w.Grants) != 0 || len(w.IngressHosts) != 0 || len(w.IfaceInstances) != 1 || w.IfaceInstances["apps/gw"] == nil {
		t.Errorf("rows about the sealed component are read: %+v", w)
	}
	if b := w.Bindings["xbin/fix"]["llm"]; len(b) != 0 {
		t.Errorf("a provider its release doesn't name binds its slot: %v", b)
	}
	if len(w.Bindings["apps/a"]) != 0 {
		t.Errorf("it is read as a provider: %v", w.Bindings["apps/a"])
	}
	if reg.SealedBindingAllowed("xbin/fix", "llm", "apps/a") || !reg.SealedBindingAllowed("xbin/fix", "llm", "apps/gw#inst") ||
		!reg.SealedBindingAllowed("xbin/fix", "llm", "xbin:llm") || reg.SealedBindingAllowed("xbin/fix", "net", "internet") || reg.SealedBindingAllowed("xbin/none", "llm", "apps/gw") {
		t.Error("SealedBindingAllowed: a declared slot, to its default or a provider the table names, and nothing else")
	}
	if err := reg.MutateWorkspace(func(w *WorkspaceManifest) { w.Lifecycle = map[string]string{"apps/a": StateDisabled} }); err != nil {
		t.Errorf("an unrelated change beside the old rows: %v", err)
	}
}
