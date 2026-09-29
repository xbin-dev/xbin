package jsonc

import (
	"encoding/json"
	"strings"
	"testing"
)

const manifest = `{
  // header comment
  "runtime": "go", // trailing
  /* block */ "partition": ["user", // why
    "global"],
  "uses": [
    { "target": "res:apps/a/db", "role": "writer" }, // a use
  ],
  "template": {"partition": ["user"]},
  "note": "a } in a \"string\" // not a comment",
}
`

func TestTopLevel(t *testing.T) {
	raw, ok, err := TopLevel([]byte(manifest), "partition")
	if err != nil || !ok {
		t.Fatalf("partition: %v %v", ok, err)
	}
	if want := "[\"user\", // why\n    \"global\"]"; string(raw) != want {
		t.Fatalf("raw = %q, want %q", raw, want)
	}
	if _, ok, _ := TopLevel([]byte(manifest), "missing"); ok {
		t.Fatal("a missing key is present")
	}
	raw, ok, _ = TopLevel([]byte(manifest), "note")
	if !ok || string(raw) != `"a } in a \"string\" // not a comment"` {
		t.Fatalf("note = %q", raw)
	}
	// Only the top level: template.partition is not "partition".
	raw, _, _ = TopLevel([]byte(`{"template": {"partition": ["user"]}}`), "partition")
	if raw != nil {
		t.Fatalf("a nested key read as top level: %q", raw)
	}
	// Duplicates: the last, as encoding/json reads it.
	raw, _, _ = TopLevel([]byte(`{"p": 1, "p": 2}`), "p")
	if string(raw) != "2" {
		t.Fatalf("duplicate = %q", raw)
	}
	for _, bad := range []string{`[1]`, `{"a": }`, `"x"`, `{"a": 1`, `{"a": 1} x`} {
		if _, _, err := TopLevel([]byte(bad), "a"); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// parsed is src's top level as xbind reads it.
func parsed(t *testing.T, src []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := Unmarshal(src, &m); err != nil {
		t.Fatalf("result doesn't parse: %v\n%s", err, src)
	}
	return m
}

func TestSetTopLevel(t *testing.T) {
	out, err := SetTopLevel([]byte(manifest), "partition", []byte(`["user"]`))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(manifest, "[\"user\", // why\n    \"global\"]", `["user"]`, 1)
	if string(out) != want {
		t.Fatalf("replace changed more than the value:\n%s", out)
	}
	// Absent: added before the first member, at its indent; the rest intact.
	src := strings.Replace(manifest, "/* block */ \"partition\": [\"user\", // why\n    \"global\"],\n", "", 1)
	out, err = SetTopLevel([]byte(src), "partition", []byte(`["user", "global"]`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "// header comment\n  \"partition\": [\"user\", \"global\"],\n  \"runtime\": \"go\", // trailing\n") {
		t.Fatalf("insert:\n%s", out)
	}
	if got := strings.Replace(string(out), "\"partition\": [\"user\", \"global\"],\n  ", "", 1); got != src {
		t.Fatalf("insert changed more than one member:\n%s", out)
	}
	if got := string(parsed(t, out)["partition"]); got != `["user", "global"]` {
		t.Fatalf("partition = %s", got)
	}
	for src, want := range map[string]string{
		`{}`:                       `{"p": 1}`,
		"{\n}":                     "{\"p\": 1\n}",
		`{"a": 1}`:                 `{"p": 1, "a": 1}`,
		`{"a": 1, "p": 2}`:         `{"a": 1, "p": 1}`,
		`{"p": 2, "a": 1, "p": 3}`: `{"p": 1, "a": 1, "p": 1}`,
	} {
		out, err := SetTopLevel([]byte(src), "p", []byte("1"))
		if err != nil || string(out) != want {
			t.Errorf("%s → %s (%v), want %s", src, out, err, want)
		}
	}
	// A JSONC value (the installed one, comments and all) is written as is.
	out, err = SetTopLevel([]byte(`{"a": 1}`), "p", []byte("[\"user\", // why\n]"))
	if err != nil || !strings.Contains(string(out), "// why") {
		t.Fatalf("JSONC value: %s (%v)", out, err)
	}
	parsed(t, out)
	if _, err := SetTopLevel([]byte(`{}`), "p", []byte("[")); err == nil {
		t.Error("a value that isn't JSON was written")
	}
}

func TestDeleteTopLevel(t *testing.T) {
	out, err := DeleteTopLevel([]byte(manifest), "partition")
	if err != nil {
		t.Fatal(err)
	}
	// The member goes; the block comment before it on its line stays, so
	// the line does too.
	if strings.Contains(string(out), `"global"`) || !strings.Contains(string(out), "/* block */") {
		t.Fatalf("delete:\n%s", out)
	}
	m := parsed(t, out)
	if _, ok := m["partition"]; ok || m["template"] == nil || m["runtime"] == nil {
		t.Fatalf("delete removed the wrong members:\n%s", out)
	}
	for src, want := range map[string]string{
		`{"p": 1}`:         `{}`,
		`{"a": 1, "p": 2}`: `{"a": 1 }`,
		`{"p": 2, "a": 1}`: `{"a": 1}`,
		"{\n  \"a\": 1,\n  \"p\": [\n    1\n  ]\n}\n": "{\n  \"a\": 1\n}\n",
		"{\n  \"p\": 1, // why\n  \"a\": 1,\n}\n":     "{\n  \"a\": 1,\n}\n",
		"{\n  \"a\": 1,\n  \"p\": 1,\n}\n":            "{\n  \"a\": 1,\n}\n",
		`{"a": 1}`:                                    `{"a": 1}`,
		`{"p": 1, "a": 2, "p": 3}`:                    `{"a": 2 }`,
	} {
		out, err := DeleteTopLevel([]byte(src), "p")
		if err != nil || string(out) != want {
			t.Errorf("%q → %q (%v), want %q", src, out, err, want)
			continue
		}
		parsed(t, out)
	}
}
