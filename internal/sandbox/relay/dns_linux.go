//go:build linux

package relay

import (
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Split-horizon DNS (plans/ingress.md ING-6): the relay already terminates
// the sandbox's :53. When a query names a PUBLISHED hostname, answer it
// locally with the hairpin VIP instead of forwarding — so a tile using its
// own public URL is routed straight back through the ingress path even when
// public DNS doesn't point at this host (NAT, LAN-only deployments).

const splitHorizonTTL = 30 // seconds; routes move when bindings change

// spliceDNS pumps a DNS flow like spliceUDPIdle, but answers published-name
// A/AAAA queries locally (A → vip, AAAA → empty NOERROR so clients fall back
// to v4). Everything else forwards to the resolver untouched.
func spliceDNS(in, out net.Conn, published func(string) bool, vip netip.Addr, idle time.Duration) (aToB, bToA int64) {
	extend := func() {
		d := time.Now().Add(idle)
		_ = in.SetReadDeadline(d)
		_ = out.SetReadDeadline(d)
	}
	extend()
	done := make(chan struct{})
	go func() { // resolver → sandbox (forwarded replies)
		defer close(done)
		buf := make([]byte, 64*1024)
		for {
			k, err := out.Read(buf)
			if k > 0 {
				extend()
				w, _ := in.Write(buf[:k])
				bToA += int64(w)
			}
			if err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 64*1024)
	for {
		k, err := in.Read(buf)
		if k > 0 {
			extend()
			if resp := answerPublished(buf[:k], published, vip); resp != nil {
				_, _ = in.Write(resp)
			} else {
				w, _ := out.Write(buf[:k])
				aToB += int64(w)
			}
		}
		if err != nil {
			break
		}
	}
	_ = in.Close()
	_ = out.Close()
	<-done
	return aToB, bToA
}

// answerPublished returns a local response for an A/AAAA query naming a
// published host, or nil to forward the query upstream.
func answerPublished(query []byte, published func(string) bool, vip netip.Addr) []byte {
	var p dnsmessage.Parser
	hdr, err := p.Start(query)
	if err != nil || hdr.Response {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	if q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA {
		return nil
	}
	if q.Class != dnsmessage.ClassINET {
		return nil
	}
	name := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
	if !published(name) {
		return nil
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: hdr.ID, Response: true, OpCode: hdr.OpCode,
		Authoritative: true, RecursionDesired: hdr.RecursionDesired, RecursionAvailable: true,
	})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil
	}
	if err := b.Question(q); err != nil {
		return nil
	}
	if q.Type == dnsmessage.TypeA {
		if err := b.StartAnswers(); err != nil {
			return nil
		}
		if err := b.AResource(dnsmessage.ResourceHeader{
			Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: splitHorizonTTL,
		}, dnsmessage.AResource{A: vip.As4()}); err != nil {
			return nil
		}
	}
	// AAAA for a published name: empty NOERROR — "no v6 address", try A.
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}
