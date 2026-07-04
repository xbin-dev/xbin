//go:build linux

package relay

import (
	"net"
	"net/netip"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// ICMP echo (ping) has no gVisor forwarder like TCP/UDP, so we handle it at the
// link layer: intercept echo requests before the stack, ping the destination
// from the host with an *unprivileged* ICMP datagram socket (SOCK_DGRAM +
// IPPROTO_ICMP, allowed by net.ipv4.ping_group_range), and — on a real reply —
// craft the echo reply back to the guest. Reachability and RTT are genuine, not
// faked. Only ICMPv4 echo; other ICMP (and TTL-exceeded, i.e. traceroute) still
// needs the `host` scope. The guest's TTL is propagated so a low-TTL probe
// simply gets no reply rather than a misleading one.
const (
	icmpTimeout  = 3 * time.Second
	icmpInFlight = 32 // cap concurrent host pings; excess echo requests are dropped
)

// icmpTap wraps the TUN link endpoint, siphoning off ICMPv4 echo requests and
// passing everything else straight through to the stack's dispatcher.
type icmpTap struct {
	stack.LinkEndpoint
	r    *Relay
	disp stack.NetworkDispatcher
	wfd  int // dup of the TUN fd, for writing echo replies toward the guest
	sem  chan struct{}
}

func newICMPTap(inner stack.LinkEndpoint, tunFD int, r *Relay) (*icmpTap, error) {
	wfd, err := unix.Dup(tunFD)
	if err != nil {
		return nil, err
	}
	return &icmpTap{LinkEndpoint: inner, r: r, wfd: wfd, sem: make(chan struct{}, icmpInFlight)}, nil
}

func (t *icmpTap) close() { unix.Close(t.wfd) }

// Attach interposes ourselves as the dispatcher so we see inbound packets.
func (t *icmpTap) Attach(disp stack.NetworkDispatcher) {
	if disp == nil {
		t.LinkEndpoint.Attach(nil)
		return
	}
	t.disp = disp
	t.LinkEndpoint.Attach(t)
}

func (t *icmpTap) IsAttached() bool { return t.disp != nil }

func (t *icmpTap) DeliverLinkPacket(p tcpip.NetworkProtocolNumber, pkt *stack.PacketBuffer) {
	t.disp.DeliverLinkPacket(p, pkt)
}

func (t *icmpTap) DeliverNetworkPacket(p tcpip.NetworkProtocolNumber, pkt *stack.PacketBuffer) {
	if p == header.IPv4ProtocolNumber {
		bb := pkt.ToBuffer()
		b := bb.Flatten()
		if len(b) >= header.IPv4MinimumSize {
			ip := header.IPv4(b)
			hlen := int(ip.HeaderLength())
			if ip.Protocol() == uint8(header.ICMPv4ProtocolNumber) &&
				len(b) >= hlen+header.ICMPv4MinimumSize {
				ic := header.ICMPv4(b[hlen:])
				if ic.Type() == header.ICMPv4Echo {
					t.forwardEcho(ip, ic)
					return // consumed — keep it out of the stack (which would drop it)
				}
			}
		}
	}
	t.disp.DeliverNetworkPacket(p, pkt)
}

// forwardEcho pings the destination from the host and, if reachable, replies to
// the guest. It runs the actual ping off the packet-read goroutine so a slow
// or unreachable target never stalls the rest of the relay.
func (t *icmpTap) forwardEcho(ip header.IPv4, ic header.ICMPv4) {
	dst := netip.AddrFrom4(ip.DestinationAddress().As4())
	src := netip.AddrFrom4(ip.SourceAddress().As4())
	id, seq, ttl := ic.Ident(), ic.Sequence(), ip.TTL()
	payload := append([]byte(nil), ic.Payload()...)

	if !t.r.allow(dst, 0) {
		t.r.record("icmp", dst, 0, false)
		return
	}
	select {
	case t.sem <- struct{}{}:
	default:
		return // saturated — drop; ping will retry the next sequence
	}
	f := t.r.record("icmp", dst, 0, true)
	go func() {
		defer func() { <-t.sem }()
		ok := hostPing(dst, payload, ttl)
		if ok {
			_, _ = unix.Write(t.wfd, buildEchoReply(dst, src, id, seq, payload))
			t.r.finish(f, int64(len(payload)), int64(len(payload)))
		} else {
			t.r.finish(f, int64(len(payload)), 0)
		}
	}()
}

// hostPing sends one echo to dst from an unprivileged ICMP socket and waits for
// a reply within icmpTimeout. ttl (from the guest packet) is propagated.
func hostPing(dst netip.Addr, payload []byte, ttl uint8) bool {
	c, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return false
	}
	defer c.Close()
	if ttl > 0 {
		_ = c.IPv4PacketConn().SetTTL(int(ttl))
	}
	wm := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: 1, Data: payload},
	}
	wb, err := wm.Marshal(nil)
	if err != nil {
		return false
	}
	_ = c.SetDeadline(time.Now().Add(icmpTimeout))
	if _, err := c.WriteTo(wb, &net.UDPAddr{IP: net.IP(dst.AsSlice())}); err != nil {
		return false
	}
	rb := make([]byte, 1500)
	n, _, err := c.ReadFrom(rb)
	if err != nil {
		return false
	}
	rm, err := icmp.ParseMessage(int(ipv4.ICMPTypeEchoReply.Protocol()), rb[:n])
	if err != nil {
		return false
	}
	return rm.Type == ipv4.ICMPTypeEchoReply
}

// buildEchoReply crafts an IPv4 ICMP echo reply from src to dst, echoing the
// guest's ident/sequence/payload so its ping matches the reply.
func buildEchoReply(src, dst netip.Addr, id, seq uint16, payload []byte) []byte {
	total := header.IPv4MinimumSize + header.ICMPv4MinimumSize + len(payload)
	buf := make([]byte, total)

	ip := header.IPv4(buf)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(total),
		TTL:         64,
		Protocol:    uint8(header.ICMPv4ProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4(src.As4()),
		DstAddr:     tcpip.AddrFrom4(dst.As4()),
	})
	ip.SetChecksum(^ip.CalculateChecksum())

	ic := header.ICMPv4(buf[header.IPv4MinimumSize:])
	ic.SetType(header.ICMPv4EchoReply)
	ic.SetCode(0)
	ic.SetIdent(id)
	ic.SetSequence(seq)
	copy(ic.Payload(), payload)
	ic.SetChecksum(0)
	ic.SetChecksum(header.ICMPv4Checksum(ic, 0))

	return buf
}
