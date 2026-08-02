package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

// D40: the redacted manifest keeps only rows whose every referenced
// component the principal can read — the full file is the workspace's whole
// topology (grant edges, wiring, public hostnames).
func TestRedactedManifestJSON(t *testing.T) {
	ws := WorkspaceManifest{
		Schema:    1,
		ImportMap: map[string]string{"lit": "/vendor/lit-all.min.js"},
		Grants: []Grant{
			{From: "apps/mine", Target: "apps/friend", Role: "reader"},        // both readable → kept
			{From: "apps/mine", Target: "apps/secret", Role: "reader"},        // target hidden → dropped
			{From: "apps/secret", Target: "apps/mine", Role: "reader"},        // from hidden → dropped
			{From: "apps/mine", Target: "res:apps/friend/db", Role: "writer"}, // scope readable → kept
			{From: "apps/mine", Target: "res:apps/secret/db", Role: "writer"}, // scope hidden → dropped
			{From: "apps/mine", Target: "res:workspace/kv", Role: "reader"},   // workspace plane → kept
			{From: "apps/mine", Target: "cap:containers", Role: "writer"},     // capability class → kept
			{From: "apps/mine", Target: "code:apps/secret", Role: "reader"},   // code ref hidden → dropped
			{From: "tiles/admin", Target: "xbin", Role: "admin"},              // from hidden → dropped
		},
		Bindings: map[string]map[string]Binding{
			"apps/mine": {
				"net": BindTo("internet:api.example.com:443"), // builtin → kept
				"ha":  BindTo("apps/friend"),                  // readable provider → kept
				"gw":  BindTo("apps/secret"),                  // hidden provider → slot dropped
			},
			"apps/secret": { // hidden component → whole entry dropped
				"net": BindTo("internet"),
			},
			"apps/friend": {
				"web": {{Ref: "runtime", Host: "pub.example.com"}}, // runtime → kept
			},
		},
	}
	readable := func(p string) bool {
		return p == "apps/mine" || p == "apps/friend"
	}
	out := RedactedManifestJSON(ws, readable)
	if strings.Contains(string(out), "secret") || strings.Contains(string(out), "admin") {
		t.Fatalf("redacted manifest leaks a hidden path:\n%s", out)
	}
	var doc struct {
		Schema    int                           `json:"schema"`
		ImportMap map[string]string             `json:"importMap"`
		Grants    []Grant                       `json:"grants"`
		Bindings  map[string]map[string]Binding `json:"bindings"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != 1 || doc.ImportMap["lit"] == "" {
		t.Error("schema/importMap must survive")
	}
	if len(doc.Grants) != 4 {
		t.Errorf("want 4 surviving grants, got %d: %+v", len(doc.Grants), doc.Grants)
	}
	if len(doc.Bindings["apps/mine"]) != 2 {
		t.Errorf("apps/mine must keep net+ha slots: %+v", doc.Bindings["apps/mine"])
	}
	if _, ok := doc.Bindings["apps/secret"]; ok {
		t.Error("hidden component's bindings must vanish")
	}
	if doc.Bindings["apps/friend"]["web"][0].Host != "pub.example.com" {
		t.Error("readable tile's own binding config survives")
	}
}
