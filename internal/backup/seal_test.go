package backup

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const testSubkeyID = "bk-0123456789abcdef0123456789abcdef"

func testSubkey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func keysOf(id string, key []byte) KeyFunc {
	return func(got string) ([]byte, error) {
		if got != id {
			return nil, errors.New("unknown subkey " + got)
		}
		return append([]byte(nil), key...), nil
	}
}

func seal(t *testing.T, key []byte, plain []byte, kind string) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewSealedWriter(&out, key, testSubkeyID, kind)
	if err != nil {
		t.Fatal(err)
	}
	// write in odd slices, so chunk boundaries fall mid-write
	for p := plain; len(p) > 0; {
		n := min(len(p), 7777)
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func unseal(body []byte, keys KeyFunc) ([]byte, SealHeader, error) {
	r, h, err := OpenSealed(bytes.NewReader(body), keys)
	if err != nil {
		return nil, h, err
	}
	out, err := io.ReadAll(r)
	return out, h, err
}

// split is a sealed archive's parts: everything before the body, and each
// sealed chunk.
func split(t *testing.T, body []byte) (pre []byte, chunks [][]byte) {
	t.Helper()
	n := binary.BigEndian.Uint32(body[len(SealMagic):])
	at := len(SealMagic) + 4 + int(n)
	pre, rest := body[:at], body[at:]
	for len(rest) > 0 {
		c := min(len(rest), SealChunk+sealTagLen)
		chunks = append(chunks, rest[:c])
		rest = rest[c:]
	}
	return pre, chunks
}

func join(pre []byte, chunks ...[]byte) []byte {
	out := append([]byte(nil), pre...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

// A seal/open round trip at 0 bytes, under a chunk, exactly a chunk, many
// chunks, and a multiple of the chunk size: the plaintext comes back, the
// header names the subkey and kind, and the body holds no plaintext.
func TestSealRoundTrip(t *testing.T) {
	key := testSubkey(t)
	marker := []byte("PLAINTEXT-MARKER-")
	for _, size := range []int{0, 1, 1000, SealChunk - 1, SealChunk, SealChunk + 1, 2 * SealChunk, 3*SealChunk + 12345} {
		plain := bytes.Repeat(marker, size/len(marker)+1)[:size]
		body := seal(t, key, plain, KindData)
		if !IsSealed(body) {
			t.Fatalf("%d: no magic", size)
		}
		if size >= len(marker) && bytes.Contains(body, marker) {
			t.Fatalf("%d: the sealed archive holds plaintext", size)
		}
		chunks := size/SealChunk + 1
		if size > 0 && size%SealChunk == 0 {
			chunks = size / SealChunk
		}
		if _, cs := split(t, body); len(cs) != chunks {
			t.Errorf("%d bytes: %d chunks, want %d", size, len(cs), chunks)
		}
		got, h, err := unseal(body, keysOf(testSubkeyID, key))
		if err != nil {
			t.Fatalf("%d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("%d: the round trip changed the bytes (%d back)", size, len(got))
		}
		if h.Subkey != testSubkeyID || h.Kind != KindData || h.Chunk != SealChunk || len(h.Salt) != 32 || h.V != 1 {
			t.Fatalf("%d: header %+v", size, h)
		}
		if hh, sealed, err := ReadSealHeader(bytes.NewReader(body)); err != nil || !sealed || !reflect.DeepEqual(hh, h) {
			t.Fatalf("ReadSealHeader: %+v %v %v", hh, sealed, err)
		}
	}
	// Each archive has its own salt: the same plaintext seals differently.
	if a, b := seal(t, key, marker, KindMain), seal(t, key, marker, KindMain); bytes.Equal(a, b) {
		t.Fatal("two archives of the same bytes are identical")
	}
}

// Tampering with the header, a chunk, or their order is caught, as is a
// stream cut short (its last chunk, flagged last, missing) or extended.
func TestSealTamper(t *testing.T) {
	key := testSubkey(t)
	plain := make([]byte, 3*SealChunk+100)
	_, _ = rand.Read(plain)
	body := seal(t, key, plain, KindMain)
	pre, cs := split(t, body)
	if len(cs) != 4 {
		t.Fatalf("%d chunks", len(cs))
	}
	flip := func(b []byte, i int) []byte {
		b = append([]byte(nil), b...)
		b[i] ^= 1
		return b
	}
	keys := keysOf(testSubkeyID, key)
	headerAt := bytes.Index(pre, []byte(`"kind":"main"`))
	cases := map[string][]byte{
		"a header field changed":   append(append(append([]byte(nil), pre[:headerAt]...), []byte(`"kind":"data"`)...), join(pre[headerAt+len(`"kind":"main"`):], cs...)...),
		"a chunk's byte flipped":   join(pre, cs[0], flip(cs[1], 100), cs[2], cs[3]),
		"a tag flipped":            join(pre, cs[0], cs[1], cs[2], flip(cs[3], len(cs[3])-1)),
		"chunks swapped":           join(pre, cs[0], cs[2], cs[1], cs[3]),
		"a chunk dropped":          join(pre, cs[0], cs[2], cs[3]),
		"a chunk repeated":         join(pre, cs[0], cs[1], cs[1], cs[2], cs[3]),
		"the last chunk dropped":   join(pre, cs[0], cs[1], cs[2]),
		"cut mid-chunk":            join(pre, cs[0], cs[1][:500]),
		"cut after the header":     pre,
		"extended past the end":    join(pre, cs[0], cs[1], cs[2], cs[3], []byte("more")),
		"another archive's chunk":  join(pre, cs[0], cs[1], cs[2], split2(t, seal(t, key, plain, KindMain))[3]),
		"the header's length lies": append(binary.BigEndian.AppendUint32([]byte(SealMagic), 5000), body[len(SealMagic)+4:]...),
	}
	for what, b := range cases {
		if got, _, err := unseal(b, keys); err == nil {
			t.Errorf("%s: opened (%d bytes)", what, len(got))
		}
	}
	// A wrong key: nothing opens.
	if _, _, err := unseal(body, keysOf(testSubkeyID, testSubkey(t))); !errors.Is(err, ErrTampered) {
		t.Errorf("a wrong key: %v", err)
	}
	// The key lookup's error is Open's, unchanged; without keys, ErrNoKeys.
	gone := errors.New("this backup's data was erased")
	if _, err := Open(bytes.NewReader(body), func(string) ([]byte, error) { return nil, gone }); err != gone {
		t.Errorf("a lookup's error: %v", err)
	}
	if _, err := Open(bytes.NewReader(body), nil); !errors.Is(err, ErrNoKeys) {
		t.Errorf("no keys: %v", err)
	}
}

func split2(t *testing.T, body []byte) [][]byte {
	_, cs := split(t, body)
	return cs
}

// Open reads a sealed tar as NewReader reads the plaintext one; the writer
// refuses a key or an id that isn't one.
func TestSealedArchive(t *testing.T) {
	key := testSubkey(t)
	var plain bytes.Buffer
	w := NewWriter(&plain)
	if err := w.Manifest(Manifest{Schema: SchemaSplit, Component: "apps/x", Scope: "apps/x", ScopeRoot: true, Kind: KindData,
		Includes: []string{"data"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.File(KVName, 0o644, []byte(`{"state":{"k":"dg=="}}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := Open(bytes.NewReader(seal(t, key, plain.Bytes(), KindData)), keysOf(testSubkeyID, key))
	if err != nil {
		t.Fatal(err)
	}
	if !r.M.DataArchive() || r.M.DeploymentArchive() || r.M.Schema != SchemaSplit {
		t.Fatalf("manifest %+v", r.M)
	}
	if got := members(t, r); got[KVName] != `{"state":{"k":"dg=="}}` {
		t.Fatalf("members %v", got)
	}
	if _, err := NewSealedWriter(io.Discard, key[:16], testSubkeyID, KindMain); err == nil {
		t.Error("a 16-byte subkey was taken")
	}
	if _, err := NewSealedWriter(io.Discard, key, "bk-nothex", KindMain); err == nil {
		t.Error("a malformed id was taken")
	}
}

func members(t *testing.T, r *Reader) map[string]string {
	t.Helper()
	out := map[string]string{}
	for {
		name, rd, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rd)
		out[name] = string(b)
	}
}

// goldenSHA and goldenMembers pin testdata/plaintext-schema1.tar, an archive the
// previous release's writer made (never regenerated): the file, and each
// member's bytes. internal/broker restores it and compares against them.
const goldenSHA = "7c41661ceb5ed2f63942afb97422696a09a6e52c19417054e719bee677be71f7"

var goldenMembers = map[string]string{
	"source/xbin.json":         "f8284a1ee28e3feb31f7f4d2397238fc346347063e3d1c9148ba689d79ca284b",
	"source/backend/main.go":   "55a60bb97151b2b4b680462447ce60ec34511b14fa10d77440c97b9777101566",
	"data/kv.json":             "981640767775c5ba648d202be0bc93d8fec8ae68ac72284a79e5933ea9d69949",
	"data/sqlite/db/db.sqlite": "5b448daf62c54baf52c8142af22b7e7a8aedd86569fccc608bf9e88479403dd5",
	"term/upper/etc/profile":   "7eca7f7ea45b3cf0d34824b555b7d722ccfcc56343102ba2ef2b809fa68f3487",
}

func sha(b string) string { s := sha256.Sum256([]byte(b)); return hex.EncodeToString(s[:]) }

// Open on a plaintext archive is NewReader: a golden archive the previous
// release's writer made (testdata/plaintext-schema1.tar) reads the same
// member for member, with or without keys — its manifest and every
// member's bytes as pinned.
func TestOpenPlaintextGolden(t *testing.T) {
	golden, err := os.ReadFile("testdata/plaintext-schema1.tar")
	if err != nil {
		t.Fatal(err)
	}
	if got := sha(string(golden)); got != goldenSHA {
		t.Fatalf("testdata/plaintext-schema1.tar changed (%s): it is the previous release's writer's, never regenerated", got)
	}
	want, err := NewReader(bytes.NewReader(golden))
	if err != nil {
		t.Fatal(err)
	}
	wantM := want.M
	wantMembers := members(t, want)
	for _, keys := range []KeyFunc{nil, keysOf(testSubkeyID, testSubkey(t))} {
		got, err := Open(bytes.NewReader(golden), keys)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.M, wantM) {
			t.Fatalf("manifest %+v, want %+v", got.M, wantM)
		}
		gm := members(t, got)
		if !reflect.DeepEqual(gm, wantMembers) {
			t.Fatalf("members differ: %v", gm)
		}
		digests := map[string]string{}
		for name, body := range gm {
			digests[name] = sha(body)
		}
		if !reflect.DeepEqual(digests, goldenMembers) {
			t.Fatalf("member digests %v, want %v", digests, goldenMembers)
		}
	}
	pinned := Manifest{Schema: Schema, Component: "apps/golden", Scope: "apps/golden", ScopeRoot: true,
		Resources: map[string]string{"db": "sqlite", "state": "kv"}, XBinVersion: "v0.3.61", Created: "2026-09-01T00:00:00Z",
		Includes: []string{"source", "data", "term-env"}}
	if !reflect.DeepEqual(wantM, pinned) {
		t.Fatalf("golden manifest %+v, want %+v", wantM, pinned)
	}
	if wantM.Schema != Schema || wantM.Component != "apps/golden" || !strings.HasPrefix(wantMembers["source/xbin.json"], "{") {
		t.Fatalf("golden: %+v", wantM)
	}
	// Anything else is what NewReader says of it.
	if _, err := Open(strings.NewReader("XBIN"), nil); err == nil {
		t.Error("a 4-byte archive opened")
	}
}
