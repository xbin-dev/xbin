package broker

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// The admin overview says which tiles ask for a VM, with their sizes, so an
// idle tile shows how it will run (D112).
func TestAuthOverviewVMIntent(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"xbin.json":            `{"schema":1}`,
		"apps/vm/xbin.json":    `{"runtime":"go","vm":{"memory":"1G","vcpus":2}}`,
		"apps/vmdef/xbin.json": `{"runtime":"go","vm":true}`,
		"apps/ns/xbin.json":    `{"runtime":"go"}`,
	} {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/xbin/auth-overview", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	b.apiAuthOverview(w, r)
	var out struct {
		Components []struct {
			Path string          `json:"path"`
			VM   json.RawMessage `json:"vm"`
		} `json:"components"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err, w.Body.String())
	}
	got := map[string]string{}
	for _, c := range out.Components {
		got[c.Path] = string(c.VM)
	}
	if got["apps/vm"] != `{"memMiB":1024,"vcpus":2}` || got["apps/vmdef"] != `{}` || got["apps/ns"] != "" {
		t.Fatalf("vm intents: %v", got)
	}
}
