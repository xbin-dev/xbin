//go:build linux

package relay

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func dnsQuery(t *testing.T, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 42, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name: dnsmessage.MustNewName(name + "."), Type: typ, Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Published names answer locally with the VIP; everything else forwards.
func TestAnswerPublished(t *testing.T) {
	vip := netip.MustParseAddr(HairpinIP)
	pub := func(h string) bool { return h == "blog.example.com" }

	resp := answerPublished(dnsQuery(t, "blog.example.com", dnsmessage.TypeA), pub, vip)
	if resp == nil {
		t.Fatal("published A query must be answered locally")
	}
	var p dnsmessage.Parser
	hdr, err := p.Start(resp)
	if err != nil || !hdr.Response || hdr.ID != 42 {
		t.Fatalf("bad response header: %+v %v", hdr, err)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AnswerHeader(); err != nil {
		t.Fatalf("expected an answer: %v", err)
	}
	a, err := p.AResource()
	if err != nil {
		t.Fatalf("expected an A answer: %v", err)
	}
	if netip.AddrFrom4(a.A) != vip {
		t.Fatalf("answered %v, want %v", a.A, vip)
	}

	// AAAA for a published name: empty NOERROR (fall back to A), still local.
	resp = answerPublished(dnsQuery(t, "blog.example.com", dnsmessage.TypeAAAA), pub, vip)
	if resp == nil {
		t.Fatal("published AAAA must be answered (empty) locally")
	}
	// Case-insensitive.
	if answerPublished(dnsQuery(t, "Blog.Example.COM", dnsmessage.TypeA), pub, vip) == nil {
		t.Fatal("query names are case-insensitive")
	}
	// Unpublished / non-address queries forward.
	if answerPublished(dnsQuery(t, "other.example.com", dnsmessage.TypeA), pub, vip) != nil {
		t.Fatal("unpublished names must forward upstream")
	}
	if answerPublished(dnsQuery(t, "blog.example.com", dnsmessage.TypeMX), pub, vip) != nil {
		t.Fatal("non-A/AAAA queries must forward upstream")
	}
}
