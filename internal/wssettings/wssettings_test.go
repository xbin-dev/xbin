package wssettings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(b bool) *bool { return &b }

// put writes a file the way an editor's save or an older xbind does —
// atomically, so its stamp changes whatever its size and mtime.
func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func keys(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(read(t, path)), &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func newStore(t *testing.T) (*Store, string, string) {
	dir := filepath.Join(t.TempDir(), "data")
	s := New(filepath.Join(dir, "workspace-settings.json"))
	return s, s.Path(), filepath.Join(dir, PoliciesFile)
}

// A missing or empty file is every default: base auto-update on (D175), the
// partitioned tiles' switches off (PD-55).
func TestDefaults(t *testing.T) {
	s, path, _ := newStore(t)
	st, probs := s.Load()
	if st != Defaults() || len(probs) != 0 || !st.BaseAutoUpdate || st.PartitionConsent || st.CredentialResetConfirm {
		t.Fatalf("a missing file: %+v %v", st, probs)
	}
	if !s.BaseAutoUpdate() {
		t.Fatal("the hook says off for a missing file")
	}
	for _, doc := range []string{"", "  \n", "{}", `{"other": 1}`, `{"baseAutoUpdate": null}`} {
		put(t, path, doc)
		if st, probs := s.Load(); st != Defaults() || len(probs) != 0 {
			t.Fatalf("%q: %+v %v (want the defaults)", doc, st, probs)
		}
	}
}

// Each setting persists, comes back, and an absent key leaves it alone.
func TestApplyPersists(t *testing.T) {
	s, p, _ := newStore(t)
	old, st, err := s.Apply(Patch{BaseAutoUpdate: ptr(false)})
	if err != nil || st.BaseAutoUpdate || !old.BaseAutoUpdate {
		t.Fatalf("set off: %+v → %+v %v", old, st, err)
	}
	if st, _ = New(p).Load(); st.BaseAutoUpdate {
		t.Fatalf("off didn't persist: %+v", st)
	}
	if New(p).BaseAutoUpdate() {
		t.Fatal("the hook reads on after off was stored")
	}
	if _, st, err = s.Apply(Patch{}); err != nil || st.BaseAutoUpdate {
		t.Fatalf("an empty patch changed it: %+v %v", st, err)
	}
	if _, st, err = s.Apply(Patch{BaseAutoUpdate: ptr(true), PartitionConsent: ptr(true)}); err != nil || !st.BaseAutoUpdate || !st.PartitionConsent || st.CredentialResetConfirm {
		t.Fatalf("set on: %+v %v", st, err)
	}
	if _, st, err = s.Apply(Patch{CredentialResetConfirm: ptr(true)}); err != nil || !st.PartitionConsent || !st.CredentialResetConfirm {
		t.Fatalf("a partial patch: %+v %v", st, err)
	}
	if st, _ = New(p).Load(); st != (Settings{true, true, true}) {
		t.Fatalf("didn't persist: %+v", st)
	}
	if got := (Patch{BaseAutoUpdate: ptr(false), CredentialResetConfirm: ptr(true)}).Keys(); strings.Join(got, ",") != "baseAutoUpdate,credentialResetConfirm" {
		t.Fatalf("Patch.Keys = %v", got)
	}
}

// A rewrite keeps the keys this xbind doesn't know — a newer xbind's — as
// they were.
func TestApplyKeepsUnknownKeys(t *testing.T) {
	s, p, _ := newStore(t)
	orig := `{"futureSwitch": true, "futureTable": {"a": [1, 2, {"b": null}]}, "baseAutoUpdate": true}`
	put(t, p, orig)
	if _, _, err := s.Apply(Patch{BaseAutoUpdate: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(read(t, p)), &got); err != nil {
		t.Fatalf("the rewrite isn't JSON: %v", err)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(orig), &want)
	want["baseAutoUpdate"] = false
	if g, w := mustJSON(got), mustJSON(want); g != w {
		t.Fatalf("the rewrite lost or changed a key:\n got %s\nwant %s", g, w)
	}
}

// A file that isn't an object is every setting unreadable: base
// auto-update reads off (nothing discarded on a guess), each partitioned
// tiles' switch on (no value read yet: fail closed); Apply leaves the file
// alone. A bad value is that setting's problem alone.
func TestUnreadableFile(t *testing.T) {
	for _, doc := range []string{`not json`, `null`, `[1]`} {
		s, p, _ := newStore(t)
		put(t, p, doc)
		st, probs := s.Load()
		if len(probs) != 3 || st != (Settings{false, true, true}) || !strings.Contains(probs.Of().Error(), "data/workspace-settings.json isn't a JSON object") {
			t.Fatalf("%q: %+v %v", doc, st, probs)
		}
		if s.BaseAutoUpdate() {
			t.Fatalf("%q: the hook says on for an unreadable file", doc)
		}
		if _, _, err := s.Apply(Patch{BaseAutoUpdate: ptr(false)}); err == nil || !strings.Contains(err.Error(), "not overwritten") {
			t.Fatalf("%q: Apply rewrote an unreadable file (%v)", doc, err)
		}
		if b := read(t, p); b != doc {
			t.Fatalf("%q: the file changed to %q", doc, b)
		}
	}
	// base auto-update's bad value: it alone is off; the switches read
	s, p, _ := newStore(t)
	put(t, p, `{"baseAutoUpdate": "yes", "partitionConsent": true}`)
	st, probs := s.Load()
	if st != (Settings{false, true, false}) || len(probs) != 1 || probs.Of(KeyBaseAutoUpdate) == nil {
		t.Fatalf("a bad baseAutoUpdate: %+v %v", st, probs)
	}
	if _, _, err := s.Apply(Patch{PartitionConsent: ptr(false)}); err == nil {
		t.Fatal("Apply rewrote a file with a bad value")
	}
}

// covers PD-55 — both partitioned tiles' switches are protections, so a
// file xbind can't read never turns one off: a switch it can't read keeps
// its last value, or is on when none was read (a cold start); the other
// switch reads as the file says. Each is read by its exact key.
func TestProtectionsFailClosed(t *testing.T) {
	s, p, _ := newStore(t)
	put(t, p, `{"partitionConsent":true,"credentialResetConfirm":false}`)
	if st, probs := s.Load(); !st.PartitionConsent || st.CredentialResetConfirm || len(probs) != 0 {
		t.Fatalf("the good file reads %+v %v", st, probs)
	}
	for _, tc := range []struct {
		name, file string
		pc, crc    bool
	}{
		// the admin's typo while turning the other switch on: the one on stays on
		{"a broken file", `{"partitionConsent": true, "credentialResetConfirm": tru`, true, false},
		{"not an object", `[]`, true, false},
		{"a value of the wrong type", `{"partitionConsent":"no","credentialResetConfirm":true}`, true, true},
		{"a newer schema's changed type", `{"schema":3,"partitionConsent":{"mode":"off"},"credentialResetConfirm":false}`, true, false},
		// encoding/json would read it as the switch; a write would then put
		// the right key beside it and answer false while the file said true
		{"a mis-cased key", `{"partitionconsent":false,"credentialResetConfirm":false}`, true, false},
		{"null", `{"partitionConsent":null,"credentialResetConfirm":false}`, true, false},
	} {
		put(t, p, tc.file)
		st, probs := s.Load()
		if st.PartitionConsent != tc.pc || st.CredentialResetConfirm != tc.crc || probs.Of(KeyPartitionConsent, KeyCredentialResetConfirm) == nil {
			t.Errorf("%s: %+v %v, want partitionConsent %v credentialResetConfirm %v (fail closed)", tc.name, st, probs, tc.pc, tc.crc)
		}
		if strings.Contains(probs.Of().Error(), filepath.Dir(p)) {
			t.Errorf("%s: the problem names the host path: %v", tc.name, probs)
		}
		if _, _, err := s.Apply(Patch{CredentialResetConfirm: ptr(false)}); err == nil {
			t.Errorf("%s: Apply overwrote it", tc.name)
		}
		if got := read(t, p); got != tc.file {
			t.Errorf("%s: the file changed: %s", tc.name, got)
		}
	}
	// fixed by hand: read as it says
	put(t, p, `{"partitionConsent":false,"credentialResetConfirm":false}`)
	if st, probs := s.Load(); st.PartitionConsent || st.CredentialResetConfirm || len(probs) != 0 {
		t.Fatalf("after the fix: %+v %v", st, probs)
	}
	// the last good values are now off: a broken file keeps them off
	put(t, p, `{"partitionConsent": tru`)
	if st, _ := s.Load(); st.PartitionConsent || st.CredentialResetConfirm {
		t.Fatalf("a broken file after both were read off: %+v", st)
	}
	// a cold start (a fresh xbind) on a broken file: every switch on
	if st, _ := New(p).Load(); !st.PartitionConsent || !st.CredentialResetConfirm || st.BaseAutoUpdate {
		t.Fatalf("a cold start on a broken file: %+v, want both switches on, base auto-update off", st)
	}
	// one bad value on a cold start: that switch on, the other as the file says
	put(t, p, `{"partitionConsent":1,"credentialResetConfirm":false}`)
	if st, _ := New(p).Load(); !st.PartitionConsent || st.CredentialResetConfirm || !st.BaseAutoUpdate {
		t.Fatalf("a cold start with one bad value: %+v", st)
	}
}

// covers D180 — the first read after the upgrade imports v0.3.66's
// data/workspace-policies.json into the settings file, keeping the file's
// other keys; reading again (this store, a restarted one) changes nothing;
// the policies file stays as it was.
func TestImportPolicies(t *testing.T) {
	s, p, legacy := newStore(t)
	put(t, p, `{"baseAutoUpdate": false, "newerKey": {"x": 1}}`)
	old := `{"schema":1,"partitionConsent":true,"credentialResetConfirm":false}`
	put(t, legacy, old)
	st, probs := s.Load()
	if st != (Settings{false, true, false}) || len(probs) != 0 {
		t.Fatalf("after the import: %+v %v", st, probs)
	}
	k := keys(t, p)
	if string(k["partitionConsent"]) != "true" || string(k["credentialResetConfirm"]) != "false" ||
		string(k["baseAutoUpdate"]) != "false" || k["newerKey"] == nil || k[keyPoliciesSum] == nil {
		t.Fatalf("the settings file after the import: %s", read(t, p))
	}
	if read(t, legacy) != old {
		t.Fatalf("the import changed the policies file: %s", read(t, legacy))
	}
	once := read(t, p)
	for i, st2 := range []*Store{s, New(p), New(p)} {
		if got, probs := st2.Load(); got != st || len(probs) != 0 {
			t.Fatalf("read %d: %+v %v", i, got, probs)
		}
		if read(t, p) != once {
			t.Fatalf("read %d rewrote the settings file: %s", i, read(t, p))
		}
	}
	// a hand edit of the settings file is what applies: the policies file,
	// unchanged, isn't imported again
	put(t, p, strings.Replace(once, `"partitionConsent": true`, `"partitionConsent": false`, 1))
	if got, _ := s.Load(); got.PartitionConsent {
		t.Fatalf("a hand edit of the settings file: %+v", got)
	}
	// removing the policies file changes nothing
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(); got != (Settings{false, false, false}) {
		t.Fatalf("after the policies file went: %+v", got)
	}
}

// covers D180 — a fresh workspace has no policies file; base auto-update's
// writes never make one; the first write of a partitioned tiles' switch
// writes both into it (for a downgrade to v0.3.66), keeping its other keys
// and a newer schema number.
func TestPoliciesFileForDowngrade(t *testing.T) {
	s, p, legacy := newStore(t)
	if _, _, err := s.Apply(Patch{BaseAutoUpdate: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("a base auto-update write made the policies file: %v", err)
	}
	if _, _, err := s.Apply(Patch{PartitionConsent: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	// what v0.3.66 reads
	var v0366 struct {
		Schema                 int  `json:"schema"`
		PartitionConsent       bool `json:"partitionConsent"`
		CredentialResetConfirm bool `json:"credentialResetConfirm"`
	}
	if err := json.Unmarshal([]byte(read(t, legacy)), &v0366); err != nil || v0366.Schema != 1 || !v0366.PartitionConsent || v0366.CredentialResetConfirm {
		t.Fatalf("the policies file: %s (%v)", read(t, legacy), err)
	}
	if string(keys(t, p)[keyPoliciesSum]) != `"`+sha256Hex([]byte(read(t, legacy)))+`"` {
		t.Fatalf("the settings file doesn't record the policies file it wrote: %s", read(t, p))
	}
	// a newer xbind's policies file keeps its keys and schema number
	put(t, legacy, `{"schema":2,"partitionConsent":true,"credentialResetConfirm":false,"futureSwitch":{"x":1}}`)
	if _, _, err := s.Apply(Patch{CredentialResetConfirm: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	k := keys(t, legacy)
	if string(k["schema"]) != "2" || strings.Join(strings.Fields(string(k["futureSwitch"])), "") != `{"x":1}` || string(k["partitionConsent"]) != "true" || string(k["credentialResetConfirm"]) != "true" {
		t.Fatalf("the policies file after a write: %s", read(t, legacy))
	}
	if st, probs := New(p).Load(); st != (Settings{false, true, true}) || len(probs) != 0 {
		t.Fatalf("a restarted store: %+v %v", st, probs)
	}
}

// covers D180 — a downgrade to v0.3.66 reads and writes the policies file;
// what it set there is what the upgrade back applies (imported once).
func TestDowngradeAndBack(t *testing.T) {
	s, p, legacy := newStore(t)
	if _, _, err := s.Apply(Patch{PartitionConsent: ptr(false), CredentialResetConfirm: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	// v0.3.66 turns credential resets on: its own rewrite, keeping keys
	put(t, legacy, `{
  "credentialResetConfirm": true,
  "partitionConsent": false,
  "schema": 1
}
`)
	up := New(p) // the upgrade back
	if st, probs := up.Load(); st != (Settings{true, false, true}) || len(probs) != 0 {
		t.Fatalf("after the upgrade back: %+v %v", st, probs)
	}
	if string(keys(t, p)["credentialResetConfirm"]) != "true" {
		t.Fatalf("the import wasn't saved: %s", read(t, p))
	}
	// and a write after it: both files agree, and nothing is imported again
	if _, _, err := up.Apply(Patch{PartitionConsent: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	after := read(t, p)
	if st, _ := New(p).Load(); st != (Settings{true, true, true}) || read(t, p) != after {
		t.Fatalf("after a write: %+v; the settings file %s", st, read(t, p))
	}
}

// covers D180, PD-55 — a policies file to import that can't be read makes
// both switches unreadable (each keeps its last value, or is on) until it is
// fixed or removed; base auto-update reads as the settings file says;
// Apply refuses. Fixed, it is imported.
func TestBrokenPoliciesFile(t *testing.T) {
	for _, doc := range []string{``, `{"partitionConsent": tru`, `[]`, `{"partitionConsent":"yes"}`, `{"PartitionConsent":false}`} {
		s, p, legacy := newStore(t)
		put(t, p, `{"baseAutoUpdate": false}`)
		put(t, legacy, doc)
		st, probs := s.Load()
		if st != (Settings{false, true, true}) || probs.Of(KeyBaseAutoUpdate) != nil ||
			!strings.Contains(probs.Of(KeyPartitionConsent, KeyCredentialResetConfirm).Error(), "data/"+PoliciesFile) {
			t.Fatalf("%q: %+v %v", doc, st, probs)
		}
		if _, _, err := s.Apply(Patch{BaseAutoUpdate: ptr(true)}); err == nil {
			t.Fatalf("%q: Apply wrote over a policies file it can't import", doc)
		}
		if read(t, legacy) != doc || read(t, p) != `{"baseAutoUpdate": false}` {
			t.Fatalf("%q: a file changed", doc)
		}
		put(t, legacy, `{"partitionConsent":true}`)
		if st, probs := s.Load(); st != (Settings{false, true, false}) || len(probs) != 0 {
			t.Fatalf("%q fixed: %+v %v", doc, st, probs)
		}
	}
	// a settings file that can't be read imports nothing; the policies file
	// stays to be imported once it is fixed
	s, p, legacy := newStore(t)
	put(t, p, `{broken`)
	put(t, legacy, `{"partitionConsent":false,"credentialResetConfirm":false}`)
	if st, _ := s.Load(); st != (Settings{false, true, true}) || read(t, p) != `{broken` {
		t.Fatalf("a broken settings file: %+v %s", st, read(t, p))
	}
	put(t, p, `{}`)
	if st, probs := s.Load(); st != (Settings{true, false, false}) || len(probs) != 0 || keys(t, p)[keyPoliciesSum] == nil {
		t.Fatalf("fixed: %+v %v %s", st, probs, read(t, p))
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
