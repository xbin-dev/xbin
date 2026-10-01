package wssettings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func ptr(b bool) *bool { return &b }

// A missing file is every default: base auto-update is on (D173).
func TestDefaultIsOn(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "data", "workspace-settings.json"))
	st, err := s.Load()
	if err != nil || !st.BaseAutoUpdate {
		t.Fatalf("a missing file: %+v %v (want baseAutoUpdate on)", st, err)
	}
	if !s.BaseAutoUpdate() {
		t.Fatal("the hook says off for a missing file")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{"", "  \n", "{}", `{"other": 1}`, `{"baseAutoUpdate": null}`} {
		if err := os.WriteFile(s.Path(), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		if st, err := s.Load(); err != nil || !st.BaseAutoUpdate {
			t.Fatalf("%q: %+v %v (want the default)", doc, st, err)
		}
	}
}

// Off persists, on comes back, and an absent key leaves it alone.
func TestApplyPersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data", "workspace-settings.json")
	s := New(p)
	st, err := s.Apply(Patch{BaseAutoUpdate: ptr(false)})
	if err != nil || st.BaseAutoUpdate {
		t.Fatalf("set off: %+v %v", st, err)
	}
	if st, err = New(p).Load(); err != nil || st.BaseAutoUpdate {
		t.Fatalf("off didn't persist: %+v %v", st, err)
	}
	if New(p).BaseAutoUpdate() {
		t.Fatal("the hook reads on after off was stored")
	}
	if st, err = s.Apply(Patch{}); err != nil || st.BaseAutoUpdate {
		t.Fatalf("an empty patch changed it: %+v %v", st, err)
	}
	if st, err = s.Apply(Patch{BaseAutoUpdate: ptr(true)}); err != nil || !st.BaseAutoUpdate {
		t.Fatalf("set on: %+v %v", st, err)
	}
	if st, _ = New(p).Load(); !st.BaseAutoUpdate {
		t.Fatal("on didn't persist")
	}
}

// A rewrite keeps the keys this xbind doesn't know — a newer xbind's — as
// they were.
func TestApplyKeepsUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "workspace-settings.json")
	orig := `{"futureSwitch": true, "futureTable": {"a": [1, 2, {"b": null}]}, "baseAutoUpdate": true}`
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(p).Apply(Patch{BaseAutoUpdate: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("the rewrite isn't JSON: %v\n%s", err, b)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(orig), &want)
	want["baseAutoUpdate"] = false
	if g, w := mustJSON(got), mustJSON(want); g != w {
		t.Fatalf("the rewrite lost or changed a key:\n got %s\nwant %s", g, w)
	}
}

// A file that isn't an object, or a known key of the wrong type, is an
// error: Load answers the defaults with it, the hook turns auto-update off
// (nothing is discarded on a guess), and Apply leaves the file alone.
func TestUnreadableFile(t *testing.T) {
	for _, doc := range []string{`not json`, `null`, `[1]`, `{"baseAutoUpdate": "yes"}`} {
		p := filepath.Join(t.TempDir(), "workspace-settings.json")
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		s := New(p)
		if _, err := s.Load(); err == nil {
			t.Fatalf("%q: no error", doc)
		}
		if s.BaseAutoUpdate() {
			t.Fatalf("%q: the hook says on for an unreadable file", doc)
		}
		if _, err := s.Apply(Patch{BaseAutoUpdate: ptr(false)}); err == nil {
			t.Fatalf("%q: Apply rewrote an unreadable file", doc)
		}
		if b, _ := os.ReadFile(p); string(b) != doc {
			t.Fatalf("%q: the file changed to %q", doc, b)
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
