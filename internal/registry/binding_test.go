package registry

import (
	"encoding/json"
	"testing"
)

// Binding must accept both the pre-multi plain-string format and arrays, and
// marshal singles back as strings so existing manifests keep their shape.
func TestBindingJSON(t *testing.T) {
	var b Binding
	if err := json.Unmarshal([]byte(`"internet"`), &b); err != nil || len(b) != 1 || b[0].Ref != "internet" {
		t.Fatalf("string form: %v %v", b, err)
	}
	if err := json.Unmarshal([]byte(`["apps/imap#abc","apps/slack"]`), &b); err != nil || len(b) != 2 {
		t.Fatalf("array form: %v %v", b, err)
	}
	if err := json.Unmarshal([]byte(`""`), &b); err != nil || len(b) != 0 {
		t.Fatalf("empty string must mean unbound: %v %v", b, err)
	}

	one, _ := json.Marshal(BindTo("internet"))
	if string(one) != `"internet"` {
		t.Fatalf("single must marshal as plain string, got %s", one)
	}
	many, _ := json.Marshal(BindTo("a", "b"))
	if string(many) != `["a","b"]` {
		t.Fatalf("multi must marshal as array, got %s", many)
	}
	if BindTo("a", "b").First() != "a" {
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

// Config-carrying refs (ingress route config, plans/ingress.md) marshal as
// objects and round-trip; bare refs stay strings in the same array.
func TestBindRefConfigJSON(t *testing.T) {
	var b Binding
	in := `{"ref":"apps/traefik","host":"blog.example.com"}`
	if err := json.Unmarshal([]byte(in), &b); err != nil || len(b) != 1 {
		t.Fatalf("object form: %v %v", b, err)
	}
	if b[0].Ref != "apps/traefik" || b[0].Host != "blog.example.com" {
		t.Fatalf("config lost: %+v", b[0])
	}
	out, _ := json.Marshal(b)
	if string(out) != in {
		t.Fatalf("round-trip changed shape:\n got %s\nwant %s", out, in)
	}
	// A configured single never collapses to a bare string.
	one, _ := json.Marshal(Binding{{Ref: "runtime", Listen: ":2456"}})
	if string(one) != `{"ref":"runtime","listen":":2456"}` {
		t.Fatalf("configured single: %s", one)
	}
	if b.FirstRef().Host != "blog.example.com" || (Binding{}).FirstRef().Ref != "" {
		t.Fatal("FirstRef accessor")
	}
}

// Exposes manifest validation: the slot-level rules builders trip on.
func TestValidateExposes(t *testing.T) {
	ok := Manifest{Exposes: map[string]ExposeDef{
		"web":  {Kind: "http", Paths: []string{"/", "/api/public/*"}},
		"game": {Kind: "stream", Proto: "udp", Port: 2456},
	}}
	if err := ValidateExposes(ok); err != nil {
		t.Fatalf("valid exposes rejected: %v", err)
	}
	bad := []Manifest{
		{Exposes: map[string]ExposeDef{"web": {Kind: "http"}}},                                 // no paths
		{Exposes: map[string]ExposeDef{"web": {Kind: "http", Paths: []string{"x"}}}},           // path not absolute
		{Exposes: map[string]ExposeDef{"game": {Kind: "stream"}}},                              // no port
		{Exposes: map[string]ExposeDef{"game": {Kind: "stream", Port: 70000}}},                 // bad port
		{Exposes: map[string]ExposeDef{"game": {Kind: "stream", Port: 1, Proto: "icmp"}}},      // bad proto
		{Exposes: map[string]ExposeDef{"x": {Kind: "tcp"}}},                                    // bad kind
		{Exposes: map[string]ExposeDef{"web": {Kind: "http", Paths: []string{"/"}, Port: 80}}}, // http with port
		{
			Interfaces: map[string]Iface{"web": {Kind: "http"}},
			Exposes:    map[string]ExposeDef{"web": {Kind: "http", Paths: []string{"/"}}},
		}, // slot collision
	}
	for i, m := range bad {
		if ValidateExposes(m) == nil {
			t.Errorf("case %d: invalid exposes accepted: %+v", i, m.Exposes)
		}
	}
}
