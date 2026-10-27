package devclient

import (
	"encoding/base64"
	"encoding/hex"
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

// Tickets as xbin-iroh 0.1.0 (iroh 1.4.1) printed them: relays off (the
// sidecar's loopback socket), and a custom relay (relay-only).
func TestParseTicket(t *testing.T) {
	for _, c := range []struct {
		ticket, endpoint, relay, addr string
	}{
		{"endpointabmrtlxo4i4z2ewtutyz7v3vm2m3gglhyvczeh26bfgmsubttl54qaibab7qaaabr6sae",
			"5919aeeee2399d12d3a4f19fd7756699b31967c545921f5e094cc950339afbc8", "", "127.0.0.1:37391"},
		{"endpointacpqzm2rz2csbnrjb45xk6jjrkwlvcpqfjxhanl3yoas7z4l6pvwiaiac5uhi5dqhixs6mjsg4xdalrqfyytumzuhazdcly",
			"9f0cb351ce8520b6290f3b7579298aacba89f02a6e70357bc3812fe78bf3eb64", "http://127.0.0.1:34821/", ""},
	} {
		tk, err := ParseTicket(c.ticket)
		if err != nil {
			t.Fatalf("%s: %v", c.ticket, err)
		}
		got := hex.EncodeToString(tk.Endpoint[:])
		if got != c.endpoint || strings.Join(tk.Relays, ",") != c.relay || strings.Join(tk.Addrs, ",") != c.addr {
			t.Errorf("%s: %s %+v", c.ticket, got, tk)
		}
	}
	for _, bad := range []string{"", "nodeabc", "endpoint", "endpoint!!", "endpointaa", "endpointacpqzm2rz2csbnrjb45xk6jjrkwlvcpqfjxhanl3yoas7z4l6pvwiaiac5uhi5dq"} {
		if _, err := ParseTicket(bad); err == nil {
			t.Errorf("%q: parsed", bad)
		}
	}
	// a link's token goes with its ticket's relay
	k := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	l, err := ParseLink("xbin://enroll?u=https%3A%2F%2Fx.example&c=CODE&tv=1&t=endpointacpqzm2rz2csbnrjb45xk6jjrkwlvcpqfjxhanl3yoas7z4l6pvwiaiac5uhi5dqhixs6mjsg4xdalrqfyytumzuhazdcly&k=" + k + "&p=" + k + "&r=tok")
	if err != nil {
		t.Fatal(err)
	}
	if r, err := RelayFor(l); err != nil || r == nil || r.Mode != "custom" || r.URL != "http://127.0.0.1:34821/" || r.Token != "tok" {
		t.Fatalf("RelayFor: %+v %v", r, err)
	}
	l.RelayToken = ""
	if r, err := RelayFor(l); r != nil || err != nil {
		t.Fatalf("RelayFor without a token: %+v %v", r, err)
	}
}
