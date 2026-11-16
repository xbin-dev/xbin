package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// A slot the manifest marks "required" is listed so; once every required
// slot of a tile is bound, its other unbound slots are optional — the
// shell folds them into one line instead of a popover over a tile that
// already works (the journey's G10). A tile that marks nothing required
// never has optional rows: nothing says what it needs.
func TestPendingRequiredAndOptional(t *testing.T) {
	pending := func(t *testing.T, bindings string) map[string]map[string]any {
		t.Helper()
		root := t.TempDir()
		for rel, content := range map[string]string{
			"xbin.json": `{"schema":1` + bindings + `}`,
			"apps/assistant/xbin.json": `{"runtime":"go","interfaces":{"llm":{"kind":"http","service":"openai","multi":true,"required":true},
				"mcp":{"kind":"http","service":"mcp","multi":true},"scm":{"kind":"http","service":"scm","multi":true}}}`,
			"apps/llm/xbin.json":   `{"runtime":"go","provides":{"openai":{"kind":"http","service":"openai","role":"writer"}}}`,
			"apps/plain/xbin.json": `{"runtime":"go","interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true}}}`,
		} {
			p := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
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
		testUsers(t, b)
		w := call(t, b.apiBindingsList, auth.Principal{Owner: true}, "GET", "/bindings", "", nil)
		var d struct {
			Pending []map[string]any `json:"pending"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, p := range d.Pending {
			out[p["component"].(string)+" "+p["slot"].(string)] = p
		}
		return out
	}

	// nothing bound: llm is required, and nothing is optional yet
	p := pending(t, "")
	if p["apps/assistant llm"]["required"] != true || p["apps/assistant mcp"]["optional"] != nil || p["apps/assistant scm"]["optional"] != nil {
		t.Fatalf("unbound: %v", p)
	}
	// the model bound: llm leaves the list, mcp and scm are optional
	p = pending(t, `,"bindings":{"apps/assistant":{"llm":["apps/llm"]}}`)
	if _, still := p["apps/assistant llm"]; still {
		t.Fatalf("a bound slot is pending: %v", p["apps/assistant llm"])
	}
	if p["apps/assistant mcp"]["optional"] != true || p["apps/assistant scm"]["optional"] != true || p["apps/assistant mcp"]["required"] != nil {
		t.Fatalf("required bound: %v", p)
	}
	// a tile that marks nothing required: never optional
	if p["apps/plain mcp"]["optional"] != nil || p["apps/plain mcp"]["required"] != nil {
		t.Fatalf("apps/plain: %v", p["apps/plain mcp"])
	}
}
