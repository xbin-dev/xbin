package wire

import (
	"bytes"
	"errors"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{ALPN: ALPNPair, Conn: 0x0102030405060708}
	for i := range h.Endpoint {
		h.Endpoint[i] = byte(i)
	}
	b := h.Encode()
	if len(b) != HeaderLen || !bytes.Equal(b[0:4], []byte("XBI1")) || b[4] != 1 || b[5] != 2 || b[40] != 1 || b[47] != 8 {
		t.Fatalf("encoded % x", b)
	}
	got, err := ReadHeader(bytes.NewReader(b))
	if err != nil || got != h {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if got.EndpointHex()[:4] != "0001" || len(got.EndpointHex()) != 64 {
		t.Errorf("hex %s", got.EndpointHex())
	}
}

func TestHeaderStrict(t *testing.T) {
	good := Header{ALPN: ALPNTunnel}.Encode()
	for name, mut := range map[string]func([]byte){
		"magic":    func(b []byte) { b[0] = 'Y' },
		"version":  func(b []byte) { b[4] = 2 },
		"alpn":     func(b []byte) { b[5] = 3 },
		"alpn 0":   func(b []byte) { b[5] = 0 },
		"reserved": func(b []byte) { b[7] = 1 },
	} {
		b := bytes.Clone(good)
		mut(b)
		if _, err := ParseHeader(b); !errors.Is(err, ErrHeader) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	if _, err := ParseHeader(good[:47]); !errors.Is(err, ErrHeader) {
		t.Error("short header accepted")
	}
	if _, err := ReadHeader(bytes.NewReader(good[:10])); err == nil {
		t.Error("truncated read accepted")
	}
}

func FuzzParseHeader(f *testing.F) {
	f.Add(Header{ALPN: ALPNTunnel}.Encode())
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := ParseHeader(b)
		if err == nil && !bytes.Equal(h.Encode(), b) {
			t.Fatalf("accepted header doesn't re-encode identically")
		}
	})
}
