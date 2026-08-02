//go:build linux

package relay

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func pinRelay(allowHost func(name string, port int) bool) *Relay {
	return &Relay{
		allowHost: allowHost,
		pins:      map[netip.Addr]map[string]int64{},
	}
}

// D35: DNS-pinned hostname egress — resolved public addresses of allowed
// names pass; everything else stays denied.
func TestDNSPinning(t *testing.T) {
	r := pinRelay(func(name string, port int) bool {
		return name == "api.stripe.com" && (port == 0 || port == 443)
	})
	pub := netip.MustParseAddr("203.0.113.7")
	priv := netip.MustParseAddr("192.168.1.10")

	if r.permitted(pub, 443) {
		t.Fatal("unpinned address must be denied")
	}
	r.pin("api.stripe.com", pub, 300)
	if !r.permitted(pub, 443) {
		t.Fatal("pinned address of an allowed name must pass at the allowed port")
	}
	if r.permitted(pub, 80) {
		t.Fatal("port not covered by the host rule must be denied")
	}
	// DNS rebinding: private answers never pin — internet-class stays public.
	r.pin("api.stripe.com", priv, 300)
	if r.permitted(priv, 443) {
		t.Fatal("private address must never pin (DNS rebinding)")
	}
	// A name the policy doesn't allow confers nothing even when pinned.
	other := netip.MustParseAddr("198.51.100.9")
	r.pin("evil.example.com", other, 300)
	if r.permitted(other, 443) {
		t.Fatal("pin for a non-allowed name must not pass")
	}
}

// pinAnswers parses a real response and pins under the QUESTION name (CNAME
// chains still pin what the policy speaks about).
func TestPinAnswersParsesResponses(t *testing.T) {
	r := pinRelay(func(name string, port int) bool { return name == "api.stripe.com" })

	q := dnsmessage.Question{
		Name: dnsmessage.MustNewName("api.stripe.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET,
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, Response: true})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(q); err != nil {
		t.Fatal(err)
	}
	if err := b.StartAnswers(); err != nil {
		t.Fatal(err)
	}
	// CNAME then the A record under the canonical name — the pin must land
	// under the question name regardless.
	if err := b.CNAMEResource(dnsmessage.ResourceHeader{
		Name: dnsmessage.MustNewName("api.stripe.com."), Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 300,
	}, dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("edge.stripe.net.")}); err != nil {
		t.Fatal(err)
	}
	if err := b.AResource(dnsmessage.ResourceHeader{
		Name: dnsmessage.MustNewName("edge.stripe.net."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 300,
	}, dnsmessage.AResource{A: [4]byte{203, 0, 113, 42}}); err != nil {
		t.Fatal(err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}

	r.pinAnswers(msg)
	if !r.permitted(netip.MustParseAddr("203.0.113.42"), 443) {
		t.Fatal("A answer for an allowed question name must pin")
	}
	// Queries (non-responses) are ignored.
	r2 := pinRelay(func(string, int) bool { return true })
	qb := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 8})
	_ = qb.StartQuestions()
	_ = qb.Question(q)
	qmsg, _ := qb.Finish()
	r2.pinAnswers(qmsg)
	if len(r2.pins) != 0 {
		t.Fatal("queries must not pin")
	}
}
