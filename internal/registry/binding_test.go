package registry

import (
	"encoding/json"
	"testing"
)

// Binding must accept both the pre-multi plain-string format and arrays, and
// marshal singles back as strings so existing manifests keep their shape.
func TestBindingJSON(t *testing.T) {
	var b Binding
	if err := json.Unmarshal([]byte(`"internet"`), &b); err != nil || len(b) != 1 || b[0] != "internet" {
		t.Fatalf("string form: %v %v", b, err)
	}
	if err := json.Unmarshal([]byte(`["apps/imap#abc","apps/slack"]`), &b); err != nil || len(b) != 2 {
		t.Fatalf("array form: %v %v", b, err)
	}
	if err := json.Unmarshal([]byte(`""`), &b); err != nil || len(b) != 0 {
		t.Fatalf("empty string must mean unbound: %v %v", b, err)
	}

	one, _ := json.Marshal(Binding{"internet"})
	if string(one) != `"internet"` {
		t.Fatalf("single must marshal as plain string, got %s", one)
	}
	many, _ := json.Marshal(Binding{"a", "b"})
	if string(many) != `["a","b"]` {
		t.Fatalf("multi must marshal as array, got %s", many)
	}
	if (Binding{"a", "b"}).First() != "a" {
		t.Fatal("First on non-empty")
	}
	if (Binding{}).First() != "" {
		t.Fatal("First on empty must be \"\"")
	}
}

// A whole workspace manifest in the old format still parses.
func TestBindingsManifestCompat(t *testing.T) {
	var ws WorkspaceManifest
	old := `{"schema":1,"bindings":{"apps/x":{"net":"internet","llm":"apps/gw"}}}`
	if err := json.Unmarshal([]byte(old), &ws); err != nil {
		t.Fatal(err)
	}
	if ws.Bindings["apps/x"]["net"].First() != "internet" || ws.Bindings["apps/x"]["llm"].First() != "apps/gw" {
		t.Fatalf("old-format bindings mis-parsed: %+v", ws.Bindings)
	}
	// Round-trip keeps the string form for singles.
	out, _ := json.Marshal(ws.Bindings)
	want := `{"apps/x":{"llm":"apps/gw","net":"internet"}}`
	if string(out) != want {
		t.Fatalf("round-trip changed shape:\n got %s\nwant %s", out, want)
	}
}
