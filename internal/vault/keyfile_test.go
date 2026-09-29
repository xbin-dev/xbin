package vault

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A barrier's descriptor opens, in memory, on another machine with the
// passphrase: what EncryptFor sealed there, DecryptFor opens here; a wrong
// passphrase, a corrupt descriptor or out-of-range parameters are refused,
// and the in-memory barrier can't be persisted.
func TestFromKeyfile(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Keyfile(); err != ErrNotInited {
		t.Fatalf("an uninitialized barrier's keyfile: %v", err)
	}
	if err := b.Init("pass-one"); err != nil {
		t.Fatal(err)
	}
	blob, err := b.EncryptFor("backup-subkey:x", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	kf, err := b.Keyfile()
	if err != nil {
		t.Fatal(err)
	}
	other, err := FromKeyfile(kf)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Unseal("wrong"); err != ErrBadPassphrase {
		t.Fatalf("a wrong passphrase: %v", err)
	}
	if err := other.Unseal("pass-one"); err != nil {
		t.Fatal(err)
	}
	if got, err := other.DecryptFor("backup-subkey:x", blob); err != nil || !bytes.Equal(got, []byte("secret")) {
		t.Fatalf("DecryptFor: %q %v", got, err)
	}
	if err := other.Rekey("pass-two"); err == nil {
		t.Error("an in-memory barrier was persisted")
	}
	other.Seal()
	if _, err := other.DecryptFor("backup-subkey:x", blob); err != ErrSealed {
		t.Errorf("after Seal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(kf, &m)
	m["memory"] = 1 << 30
	huge, _ := json.Marshal(m)
	for what, data := range map[string][]byte{"corrupt": []byte("{"), "empty": []byte("{}"), "huge memory": huge} {
		if _, err := FromKeyfile(data); err == nil {
			t.Errorf("%s: taken", what)
		}
	}
}
