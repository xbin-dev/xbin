package push

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The vectors in native/spec/push-vectors.json are generated here from
// fixed keys and nonces; the app's extension tests against the same file.
// UPDATE_VECTORS=1 rewrites it (review the diff: a changed ciphertext means
// the format changed, which old apps cannot read).

type vector struct {
	Name             string   `json:"name"`
	RecipientPrivate string   `json:"recipientPrivate"`
	RecipientPublic  string   `json:"recipientPublic"`
	EphemeralPrivate string   `json:"ephemeralPrivate"`
	EphemeralPublic  string   `json:"ephemeralPublic"`
	Nonce            string   `json:"nonce"`
	SharedSecret     string   `json:"sharedSecret"`
	Key              string   `json:"key"`
	Plaintext        string   `json:"plaintext"`
	Envelope         Envelope `json:"envelope"`
}

type invalidVector struct {
	Name             string   `json:"name"`
	RecipientPrivate string   `json:"recipientPrivate"`
	Envelope         Envelope `json:"envelope"`
}

type vectorFile struct {
	Format      string          `json:"format"`
	Description string          `json:"description"`
	Vectors     []vector        `json:"vectors"`
	Invalid     []invalidVector `json:"invalid"`
}

func fixed(label string, n int) []byte {
	h := sha256.Sum256([]byte("xbin-push-vector/" + label))
	return h[:n]
}

func buildVectors(t *testing.T) vectorFile {
	t.Helper()
	cases := []struct {
		name string
		p    Payload
	}{
		{"agent permission", Payload{V: 1, WS: "Zm9vYmFyYmF6cXV4", Kind: KindAgentPermission, Title: "Permission needed — calendar",
			Body: "Bash: go test ./...", Link: "agent/s-1a2b", CollapseID: "agent:s-1a2b:perm:p3"}},
		{"tile notification, unicode", Payload{V: 1, WS: "Zm9vYmFyYmF6cXV4", Kind: "tile.alert", Title: "Zürich 🚆 delay",
			Body: "Your train is 5 min late — 次の電車 <b>&</b>\nplatform 7", Link: "c/apps/trains/#t=42"}},
		{"test, empty link", Payload{V: 1, WS: "d3M", Kind: KindTest, Title: "xbin", Body: "Test notification"}},
	}
	out := vectorFile{Format: "xbin-push-v1",
		Description: "X25519 + HKDF-SHA256(salt = epk || recipientPublic, info = \"xbin-push-v1\") + AES-256-GCM, no AAD. All binary values base64url without padding; plaintext is the exact UTF-8 JSON that was sealed. See native/spec/push.md."}
	for i, c := range cases {
		rpriv, err := ecdh.X25519().NewPrivateKey(fixed("recipient/"+c.name, 32))
		if err != nil {
			t.Fatal(err)
		}
		eph, err := ecdh.X25519().NewPrivateKey(fixed("ephemeral/"+c.name, 32))
		if err != nil {
			t.Fatal(err)
		}
		nonce := fixed("nonce/"+c.name, 12)
		pt, err := marshalPayload(c.p)
		if err != nil {
			t.Fatal(err)
		}
		env, err := seal(eph, nonce, rpriv.PublicKey().Bytes(), pt)
		if err != nil {
			t.Fatal(err)
		}
		shared, _ := eph.ECDH(rpriv.PublicKey())
		key, _ := deriveKey(eph, rpriv.PublicKey().Bytes())
		out.Vectors = append(out.Vectors, vector{Name: c.name,
			RecipientPrivate: b64.EncodeToString(rpriv.Bytes()), RecipientPublic: b64.EncodeToString(rpriv.PublicKey().Bytes()),
			EphemeralPrivate: b64.EncodeToString(eph.Bytes()), EphemeralPublic: b64.EncodeToString(eph.PublicKey().Bytes()),
			Nonce: b64.EncodeToString(nonce), SharedSecret: b64.EncodeToString(shared), Key: b64.EncodeToString(key),
			Plaintext: string(pt), Envelope: env})
		if i == 0 {
			ct, _ := b64.DecodeString(env.CT)
			ct[0] ^= 1
			out.Invalid = append(out.Invalid, invalidVector{Name: "tampered ciphertext",
				RecipientPrivate: b64.EncodeToString(rpriv.Bytes()), Envelope: Envelope{V: 1, EPK: env.EPK, N: env.N, CT: b64.EncodeToString(ct)}})
			other, _ := ecdh.X25519().NewPrivateKey(fixed("someone else", 32))
			out.Invalid = append(out.Invalid, invalidVector{Name: "sealed to another key",
				RecipientPrivate: b64.EncodeToString(other.Bytes()), Envelope: env})
		}
	}
	return out
}

func TestVectorsFile(t *testing.T) {
	want := buildVectors(t)
	b, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	path := filepath.Join("..", "..", "native", "spec", "push-vectors.json")
	got, err := os.ReadFile(path)
	if os.Getenv("UPDATE_VECTORS") != "" {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("native/spec/push-vectors.json is stale or missing (err %v) — UPDATE_VECTORS=1 go test ./internal/push -run TestVectorsFile, and review the diff", err)
	}
}

// TestVectorsOpen checks the file as a device would: every vector opens to
// its plaintext with only the recipient's private key, every invalid one
// fails.
func TestVectorsOpen(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "native", "spec", "push-vectors.json"))
	if err != nil {
		t.Skip("no vectors file yet")
	}
	var vf vectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatal(err)
	}
	if len(vf.Vectors) < 3 || len(vf.Invalid) < 2 {
		t.Fatalf("vectors file too small: %d / %d", len(vf.Vectors), len(vf.Invalid))
	}
	priv := func(s string) *ecdh.PrivateKey {
		b, err := b64.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		k, err := ecdh.X25519().NewPrivateKey(b)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	for _, v := range vf.Vectors {
		k := priv(v.RecipientPrivate)
		if b64.EncodeToString(k.PublicKey().Bytes()) != v.RecipientPublic {
			t.Fatalf("%s: recipientPublic does not match its private key", v.Name)
		}
		pt, err := Open(k, v.Envelope)
		if err != nil || string(pt) != v.Plaintext {
			t.Fatalf("%s: open = %q, %v", v.Name, pt, err)
		}
		var p Payload
		if err := json.Unmarshal(pt, &p); err != nil || p.V != 1 {
			t.Fatalf("%s: plaintext is not a v1 payload: %v", v.Name, err)
		}
	}
	for _, v := range vf.Invalid {
		if _, err := Open(priv(v.RecipientPrivate), v.Envelope); err == nil {
			t.Fatalf("invalid vector %q opened", v.Name)
		}
	}
}

func TestSealRoundTripRandom(t *testing.T) {
	dev, _ := ecdh.X25519().GenerateKey(nil)
	msg := []byte(`{"v":1,"title":"hi"}`)
	a, err := Seal(dev.PublicKey().Bytes(), msg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Seal(dev.PublicKey().Bytes(), msg)
	if a.EPK == b.EPK || a.N == b.N || a.CT == b.CT {
		t.Fatal("two seals share randomness")
	}
	for _, e := range []Envelope{a, b} {
		pt, err := Open(dev, e)
		if err != nil || !bytes.Equal(pt, msg) {
			t.Fatalf("round trip: %q %v", pt, err)
		}
	}
	a.V = 2
	if _, err := Open(dev, a); err == nil {
		t.Fatal("opened an unknown version")
	}
}

func TestParsePublicKey(t *testing.T) {
	dev, _ := ecdh.X25519().GenerateKey(nil)
	pub := dev.PublicKey().Bytes()
	for _, s := range []string{b64.EncodeToString(pub), strings.TrimRight(b64.EncodeToString(pub), "=") + "", mustStd(pub)} {
		got, err := ParsePublicKey(s)
		if err != nil || !bytes.Equal(got, pub) {
			t.Fatalf("parse %q: %v", s, err)
		}
	}
	for _, s := range []string{"", "abc", b64.EncodeToString(make([]byte, 32)), b64.EncodeToString(make([]byte, 31))} {
		if _, err := ParsePublicKey(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

func mustStd(b []byte) string {
	s := b64.EncodeToString(b)
	s = strings.NewReplacer("-", "+", "_", "/").Replace(s)
	for len(s)%4 != 0 {
		s += "="
	}
	return s
}
