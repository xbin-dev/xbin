package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSealer seals by reversing the bytes under a tag of the label: enough
// to tell a sealed file from one in the clear.
type fakeSealer struct{ init, sealed bool }

func (f *fakeSealer) Initialized() bool { return f.init }
func (f *fakeSealer) Sealed() bool      { return f.sealed }
func (f *fakeSealer) EncryptFor(label string, pt []byte) ([]byte, error) {
	out := []byte(label + ":")
	for i := len(pt) - 1; i >= 0; i-- {
		out = append(out, pt[i]^0x5a)
	}
	return out, nil
}
func (f *fakeSealer) DecryptFor(label string, blob []byte) ([]byte, error) {
	rest, ok := bytes.CutPrefix(blob, []byte(label+":"))
	if !ok {
		return nil, errors.New("another label")
	}
	out := make([]byte, 0, len(rest))
	for i := len(rest) - 1; i >= 0; i-- {
		out = append(out, rest[i]^0x5a)
	}
	return out, nil
}

// covers D240 — xbind's own secrets (the LLM connector's keys, the outside
// MCP servers') are sealed whole under a label of their own, never written
// in the clear where a vault is meant to exist, and nothing is read while
// the vault is sealed
func TestSealedMapNeverInTheClear(t *testing.T) {
	dir := t.TempDir()
	v := &fakeSealer{init: true}
	m := &SealedMap{Path: filepath.Join(dir, "s.json"), Label: "test/label", Vault: v}
	if err := m.Set("github", "tok-SECRET-1"); err != nil || m.State() != MapOpen {
		t.Fatalf("set: %v %s", err, m.State())
	}
	raw, _ := os.ReadFile(m.Path)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "github") {
		t.Fatalf("the file holds the map in the clear: %s", raw)
	}
	if got, err := m.Get("github"); err != nil || got != "tok-SECRET-1" {
		t.Fatalf("get: %q %v", got, err)
	}
	// another label opens nothing
	other := &SealedMap{Path: m.Path, Label: "other", Vault: v}
	if _, err := other.Get("github"); err == nil {
		t.Error("a map opened under another label")
	}
	// sealed: nothing read, nothing written, nothing kept
	v.sealed = true
	if _, err := m.Get("github"); !errors.Is(err, ErrMapSealed) {
		t.Errorf("sealed get: %v", err)
	}
	if err := m.Set("x", "y"); !errors.Is(err, ErrMapSealed) {
		t.Errorf("sealed set: %v", err)
	}
	v.sealed = false
	if got, _ := m.Get("github"); got != "tok-SECRET-1" {
		t.Errorf("after unseal: %q", got)
	}
	// locked (no barrier yet, not the plaintext mode): a write is refused, never kept in the clear
	locked := &SealedMap{Path: filepath.Join(dir, "l.json"), Label: "test/label"}
	if err := locked.Set("a", "b"); !errors.Is(err, ErrMapLocked) {
		t.Errorf("locked set: %v", err)
	}
	if _, err := os.Stat(locked.Path); !os.IsNotExist(err) {
		t.Error("a locked map wrote its file")
	}
	// the plaintext mode keeps it in the clear, 0600; the first read under a barrier seals it
	plain := &SealedMap{Path: filepath.Join(dir, "p.json"), Label: "test/label", AllowPlain: true}
	if err := plain.Set("a", "PLAIN-b"); err != nil || plain.State() != MapPlaintext {
		t.Fatalf("plain: %v", err)
	}
	if fi, _ := os.Stat(plain.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	later := &SealedMap{Path: plain.Path, Label: "test/label", Vault: v}
	if got, err := later.Get("a"); err != nil || got != "PLAIN-b" {
		t.Fatalf("read under the barrier: %q %v", got, err)
	}
	if raw, _ := os.ReadFile(plain.Path); strings.Contains(string(raw), "PLAIN-b") {
		t.Error("a map kept in the clear stayed so under a barrier")
	}
	// Update writes once, and not at all for no change
	if err := m.Update(func(mm map[string]string) { mm["b"], mm["c"] = "1", "2"; delete(mm, "github") }); err != nil {
		t.Fatal(err)
	}
	if names, _ := m.Names(); strings.Join(names, ",") != "b,c" {
		t.Errorf("names %v", names)
	}
	st, _ := os.Stat(m.Path)
	if err := m.Set("zz", ""); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(m.Path); !st2.ModTime().Equal(st.ModTime()) || st2.Size() != st.Size() {
		t.Error("removing what isn't there wrote the file")
	}
}
