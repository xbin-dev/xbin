package devclient

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseLink(t *testing.T) {
	k := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	good := "xbin://enroll?u=https%3A%2F%2Fx.example&c=CODE&tv=1&t=endpointabc&k=" + k + "&p=" + k
	l, err := ParseLink(good + "&r=tok%2Fen&rq=1")
	if err != nil || !l.Tunnel() || l.Origin != "https://x.example" || l.Code != "CODE" || l.RelayToken != "tok/en" || !l.Required || len(l.PSK) != 32 {
		t.Fatalf("%+v %v", l, err)
	}
	if l, err := ParseLink("xbin://enroll?u=https%3A%2F%2Fx.example&c=CODE"); err != nil || l.Tunnel() {
		t.Fatalf("classic: %+v %v", l, err)
	}
	for name, bad := range map[string]string{
		"tv 2":      strings.Replace(good, "tv=1", "tv=2", 1),
		"no tv":     strings.Replace(good, "tv=1&", "", 1),
		"no p":      strings.Replace(good, "&p="+k, "", 1),
		"short k":   strings.Replace(good, "k="+k, "k=AAAA", 1),
		"bad p":     strings.Replace(good, "p="+k, "p=!!", 1),
		"bad t":     strings.Replace(good, "t=endpointabc", "t=nodeabc", 1),
		"only t":    "xbin://enroll?u=https%3A%2F%2Fx.example&c=CODE&t=endpointabc",
		"no code":   strings.Replace(good, "c=CODE&", "", 1),
		"not xbin":  strings.Replace(good, "xbin://", "https://", 1),
		"not enrol": strings.Replace(good, "enroll", "sso", 1),
	} {
		if _, err := ParseLink(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDeviceKeys(t *testing.T) {
	d, err := New("phone")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.EndpointHex()) != 64 || len(d.NoiseKeyB64()) != 43 || d.PublicKeyB64() == "" {
		t.Fatal("keys")
	}
	sig, err := d.Sign("https://x.example", "d1", "nonce")
	if err != nil || sig == "" {
		t.Fatal(err)
	}
}
