//go:build linux

package relay

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/sys/unix"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// The relay driven through a real gVisor stack: raw IPv4 packets go in over
// a SEQPACKET socketpair standing in for the TUN, the relay's answers come
// back out, and every host-side dial is vetted (and refused) instead of
// leaving the machine. No privileges needed.

var errNoDial = errors.New("test: dial vetted, not made")

var (
	sbxIP   = netip.MustParseAddr("10.0.2.15")
	denied  = netip.MustParseAddr("198.51.100.7") // what the tests' Deny names
	allowed = netip.MustParseAddr("198.51.100.8")
)

type harness struct {
	t     *testing.T
	r     *Relay
	peer  *net.UnixConn // the sandbox's side of the "TUN"
	dials chan string   // "tcp 1.2.3.4:80" per vetted dial
	sport uint16
}

func newHarness(t *testing.T, cfg Config) *harness { return newHarnessDial(t, cfg, nil) }

// newHarnessDial is newHarness with the relay's host-side dials going to
// dial (nil = vetted and refused, each reported on h.dials).
func newHarnessDial(t *testing.T, cfg Config, dial dialFunc) *harness {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Skip("socketpair:", err)
	}
	h := &harness{t: t, dials: make(chan string, 64), sport: 40000}
	cfg.TunFD = fds[0]
	if dial == nil {
		d := net.Dialer{Timeout: time.Second, Control: func(network, address string, _ syscall.RawConn) error {
			h.dials <- network[:3] + " " + address
			return errNoDial
		}}
		dial = d.DialContext
	}
	h.r, err = start(cfg, dial)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fds[1]), "peer")
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	h.peer = c.(*net.UnixConn)
	t.Cleanup(func() {
		h.r.Close()
		h.peer.Close()
		unix.Close(fds[0])
	})
	return h
}

func (h *harness) send(pkt []byte) {
	h.t.Helper()
	if _, err := h.peer.Write(pkt); err != nil {
		h.t.Fatal(err)
	}
}

// recv returns the first packet the relay sends that match accepts, or nil
// after d.
func (h *harness) recv(d time.Duration, match func(header.IPv4) bool) header.IPv4 {
	h.t.Helper()
	deadline := time.Now().Add(d)
	buf := make([]byte, 65536)
	for {
		_ = h.peer.SetReadDeadline(deadline)
		n, err := h.peer.Read(buf)
		if err != nil {
			return nil
		}
		if n < header.IPv4MinimumSize {
			continue
		}
		ip := header.IPv4(append([]byte(nil), buf[:n]...))
		if ip.IsValid(n) && match(ip) {
			return ip
		}
	}
}

// dialed reports the next vetted dial within d ("" = none).
func (h *harness) dialed(d time.Duration) string {
	select {
	case s := <-h.dials:
		return s
	case <-time.After(d):
		return ""
	}
}

func (h *harness) port() uint16 { h.sport++; return h.sport }

func ip4pkt(src, dst netip.Addr, proto tcpip.TransportProtocolNumber, payload []byte) []byte {
	b := make([]byte, header.IPv4MinimumSize+len(payload))
	ip := header.IPv4(b)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(b)), TTL: 64, Protocol: uint8(proto),
		SrcAddr: tcpip.AddrFrom4(src.As4()), DstAddr: tcpip.AddrFrom4(dst.As4()),
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	copy(b[header.IPv4MinimumSize:], payload)
	return b
}

func tcpSYN(src, dst netip.Addr, sport, dport uint16) []byte {
	seg := make([]byte, header.TCPMinimumSize)
	tcp := header.TCP(seg)
	tcp.Encode(&header.TCPFields{
		SrcPort: sport, DstPort: dport, SeqNum: 1000, DataOffset: header.TCPMinimumSize,
		Flags: header.TCPFlagSyn, WindowSize: 65535,
	})
	xsum := header.PseudoHeaderChecksum(header.TCPProtocolNumber,
		tcpip.AddrFrom4(src.As4()), tcpip.AddrFrom4(dst.As4()), uint16(len(seg)))
	tcp.SetChecksum(^tcp.CalculateChecksum(xsum))
	return ip4pkt(src, dst, header.TCPProtocolNumber, seg)
}

func udpPkt(src, dst netip.Addr, sport, dport uint16, payload []byte) []byte {
	seg := make([]byte, header.UDPMinimumSize+len(payload))
	u := header.UDP(seg)
	u.Encode(&header.UDPFields{SrcPort: sport, DstPort: dport, Length: uint16(len(seg))})
	copy(seg[header.UDPMinimumSize:], payload)
	xsum := header.PseudoHeaderChecksum(header.UDPProtocolNumber,
		tcpip.AddrFrom4(src.As4()), tcpip.AddrFrom4(dst.As4()), uint16(len(seg)))
	xsum = checksum.Checksum(payload, xsum)
	u.SetChecksum(^u.CalculateChecksum(xsum))
	return ip4pkt(src, dst, header.UDPProtocolNumber, seg)
}

func icmpEcho(src, dst netip.Addr, seq uint16) []byte {
	msg := make([]byte, header.ICMPv4MinimumSize+8)
	ic := header.ICMPv4(msg)
	ic.SetType(header.ICMPv4Echo)
	ic.SetIdent(7)
	ic.SetSequence(seq)
	ic.SetChecksum(0)
	ic.SetChecksum(header.ICMPv4Checksum(ic, checksum.Checksum(ic.Payload(), 0)))
	return ip4pkt(src, dst, header.ICMPv4ProtocolNumber, msg)
}

// rstFrom matches the relay's RST for a flow from sport to dst.
func rstFrom(dst netip.Addr, sport uint16) func(header.IPv4) bool {
	return func(ip header.IPv4) bool {
		if ip.TransportProtocol() != header.TCPProtocolNumber ||
			netip.AddrFrom4(ip.SourceAddress().As4()) != dst {
			return false
		}
		tcp := header.TCP(ip.Payload())
		return tcp.DestinationPort() == sport && tcp.Flags().Contains(header.TCPFlagRst)
	}
}

// synTo sends a SYN to dst:dport and reports whether it was reset at once
// and what the relay dialed ("" = nothing).
func (h *harness) synTo(dst netip.Addr, dport uint16) (reset bool, dial string) {
	sp := h.port()
	h.send(tcpSYN(sbxIP, dst, sp, dport))
	dial = h.dialed(300 * time.Millisecond)
	return h.recv(2*time.Second, rstFrom(dst, sp)) != nil, dial
}

func (h *harness) udpTo(dst netip.Addr, dport uint16) (dial string) {
	h.send(udpPkt(sbxIP, dst, h.port(), dport, []byte("hello")))
	return h.dialed(300 * time.Millisecond)
}

// flow waits for a recorded flow to dst and returns it.
func (h *harness) flow(proto string, dst netip.Addr) *Flow {
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		for _, f := range h.r.Stats().Recent {
			if f.Proto == proto && f.Dst == dst.String() {
				return &f
			}
		}
	}
	return nil
}

func allowAll(netip.Addr, int) bool { return true }
func denyOne(ip netip.Addr) bool    { return ip == denied }

// Deny beats an Allow that says yes, for TCP.
func TestDenyBeatsAllowTCP(t *testing.T) {
	h := newHarness(t, Config{Allow: allowAll, Deny: denyOne})
	if reset, dial := h.synTo(denied, 80); !reset || dial != "" {
		t.Fatalf("denied destination: reset=%v dial=%q, want an RST and no dial", reset, dial)
	}
	if f := h.flow("tcp", denied); f == nil || f.Allowed {
		t.Fatalf("the denied flow must be recorded as denied: %+v", f)
	}
	if _, dial := h.synTo(allowed, 80); dial != "tcp "+allowed.String()+":80" {
		t.Fatalf("control: an allowed destination must be dialed, got %q", dial)
	}
}

// Deny is checked before the gateway's host forwards (policy-exempt), so a
// relay with Deny has none.
func TestDenyBeatsGatewayForward(t *testing.T) {
	gw := netip.MustParseAddr(gatewayIP)
	cfg := Config{Gateway: gw, HostFwd: map[int]string{8080: "127.0.0.1:1"}}
	h := newHarness(t, cfg)
	if _, dial := h.synTo(gw, 8080); dial != "tcp 127.0.0.1:1" {
		t.Fatalf("control: without Deny the forward is dialed, got %q", dial)
	}
	cfg.Deny = HostDeny()
	h = newHarness(t, cfg)
	if reset, dial := h.synTo(gw, 8080); !reset || dial != "" {
		t.Fatalf("with HostDeny: reset=%v dial=%q, want an RST and no dial", reset, dial)
	}
}

// Deny beats an Allow that says yes, for UDP.
func TestDenyBeatsAllowUDP(t *testing.T) {
	h := newHarness(t, Config{Allow: allowAll, Deny: denyOne})
	if dial := h.udpTo(denied, 9); dial != "" {
		t.Fatalf("denied destination was dialed: %q", dial)
	}
	if dial := h.udpTo(allowed, 9); dial != "udp "+allowed.String()+":9" {
		t.Fatalf("control: an allowed destination must be dialed, got %q", dial)
	}
}

// Deny beats an Allow that says yes, for ICMP.
func TestDenyBeatsAllowICMP(t *testing.T) {
	h := newHarness(t, Config{Allow: allowAll, Deny: denyOne})
	h.send(icmpEcho(sbxIP, denied, 1))
	if f := h.flow("icmp", denied); f == nil || f.Allowed {
		t.Fatalf("a ping to a denied destination must be refused: %+v", f)
	}
	h.send(icmpEcho(sbxIP, allowed, 2))
	if f := h.flow("icmp", allowed); f == nil || !f.Allowed {
		t.Fatalf("control: a ping to an allowed destination goes out: %+v", f)
	}
}

// Deny beats a DNS pin of a host the policy allows (and Allow is nil).
func TestDenyBeatsDNSPin(t *testing.T) {
	h := newHarness(t, Config{
		AllowHost: func(name string, port int) bool { return name == "api.example.com" },
		Deny:      func(ip netip.Addr) bool { return ip == netip.MustParseAddr("203.0.113.7") },
	})
	pinned := netip.MustParseAddr("203.0.113.7")
	other := netip.MustParseAddr("203.0.113.8")
	h.r.pin("api.example.com", pinned, 300)
	h.r.pin("api.example.com", other, 300)
	if h.r.permitted(pinned, 443) {
		t.Fatal("a pinned address Deny names must not be permitted")
	}
	if reset, dial := h.synTo(pinned, 443); !reset || dial != "" {
		t.Fatalf("pinned but denied: reset=%v dial=%q", reset, dial)
	}
	if dial := h.udpTo(pinned, 443); dial != "" {
		t.Fatalf("pinned but denied (udp) was dialed: %q", dial)
	}
	if _, dial := h.synTo(other, 443); dial != "tcp 203.0.113.8:443" {
		t.Fatalf("control: the other pinned address is dialed, got %q", dial)
	}
}

// A nil Allow means nothing is allowed — and nothing panics, ICMP included
// (it used to call the nil func).
func TestNilAllow(t *testing.T) {
	h := newHarness(t, Config{})
	h.send(icmpEcho(sbxIP, allowed, 1))
	if f := h.flow("icmp", allowed); f == nil || f.Allowed {
		t.Fatalf("a ping with no policy must be refused: %+v", f)
	}
	if reset, dial := h.synTo(allowed, 80); !reset || dial != "" {
		t.Fatalf("tcp with no policy: reset=%v dial=%q", reset, dial)
	}
	if dial := h.udpTo(allowed, 9); dial != "" {
		t.Fatalf("udp with no policy was dialed: %q", dial)
	}
}

// DNSRefuse answers REFUSED at once, locally: no resolver is dialed, whatever
// address the query went to.
func TestDNSRefuse(t *testing.T) {
	h := newHarness(t, Config{DNSRefuse: true, Resolver: "192.0.2.53:53", Allow: allowAll})
	dnsIP := netip.MustParseAddr("10.0.2.3")
	ask := func(dst netip.Addr, sport, id uint16) (dnsmessage.Message, time.Duration) {
		t.Helper()
		q := dnsQuery(t, "example.com", dnsmessage.TypeA)
		q[0], q[1] = byte(id>>8), byte(id)
		t0 := time.Now()
		h.send(udpPkt(sbxIP, dst, sport, 53, q))
		resp := h.recv(time.Second, func(ip header.IPv4) bool {
			if ip.TransportProtocol() != header.UDPProtocolNumber {
				return false
			}
			u := header.UDP(ip.Payload())
			return u.SourcePort() == 53 && u.DestinationPort() == sport
		})
		took := time.Since(t0)
		if resp == nil {
			t.Fatalf("no DNS answer from %s", dst)
		}
		var m dnsmessage.Message
		if err := m.Unpack(header.UDP(resp.Payload()).Payload()); err != nil {
			t.Fatal(err)
		}
		return m, took
	}
	sp := h.port()
	for i, dst := range []netip.Addr{dnsIP, dnsIP, netip.MustParseAddr("8.8.8.8")} {
		if i == 2 {
			sp = h.port()
		}
		m, took := ask(dst, sp, uint16(100+i))
		if !m.Response || m.RCode != dnsmessage.RCodeRefused || m.ID != uint16(100+i) {
			t.Fatalf("query %d: want REFUSED for id %d, got %+v", i, 100+i, m.Header)
		}
		if len(m.Questions) != 1 || m.Questions[0].Name.String() != "example.com." || len(m.Answers) != 0 {
			t.Fatalf("query %d: the question is echoed, no answers: %+v", i, m)
		}
		if took > 50*time.Millisecond {
			t.Fatalf("query %d: REFUSED took %v, want < 50ms", i, took)
		}
	}
	if dial := h.dialed(100 * time.Millisecond); dial != "" {
		t.Fatalf("DNSRefuse must not reach a resolver, dialed %q", dial)
	}
	if f := h.flow("udp", dnsIP); f == nil || f.Allowed {
		t.Fatalf("a refused DNS flow is recorded as denied: %+v", f)
	}
}

func TestRefusal(t *testing.T) {
	if refusal([]byte{1, 2, 3}) != nil {
		t.Fatal("garbage gets no answer")
	}
	q := dnsQuery(t, "example.com", dnsmessage.TypeAAAA)
	resp := refusal(q)
	var m dnsmessage.Message
	if err := m.Unpack(resp); err != nil || m.RCode != dnsmessage.RCodeRefused || !m.RecursionDesired {
		t.Fatalf("refusal = %+v, %v", m.Header, err)
	}
	if refusal(resp) != nil {
		t.Fatal("a response is never answered")
	}
	// A header with no question still gets REFUSED.
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 9})
	hdrOnly, _ := b.Finish()
	if err := m.Unpack(refusal(hdrOnly)); err != nil || m.ID != 9 || m.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("header-only refusal = %+v, %v", m.Header, err)
	}
}

// net:internet never admits the IPv4-mapped unspecified address, which dials
// the host.
func TestPublicAddrUnmaps(t *testing.T) {
	for _, s := range []string{"::ffff:0.0.0.0", "::ffff:127.0.0.1", "::ffff:10.1.2.3", "0.0.0.0"} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("publicAddr(%s) = true", s)
		}
	}
	if !publicAddr(netip.MustParseAddr("::ffff:8.8.8.8")) {
		t.Error("a mapped public address is public")
	}
}

// Processors configures the TUN's packet processors (gVisor's default is
// one per CPU); a relay with one still forwards.
func TestProcessorsOne(t *testing.T) {
	h := newHarness(t, Config{Allow: allowAll, Processors: 1})
	if _, dial := h.synTo(allowed, 80); dial == "" {
		t.Fatal("a one-processor relay must still forward")
	}
}

// Close stops the TUN's readers before it returns: left running, they leak
// with every relay (one per CPU) and go on reading whatever file later takes
// the fd's number — another sandbox's TUN, say.
func TestCloseStopsReaders(t *testing.T) {
	base := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Skip("socketpair:", err)
		}
		r, err := Start(Config{TunFD: fds[0], Processors: i}) // 0 = one per CPU
		if err != nil {
			t.Fatal(err)
		}
		for j := uint16(0); j < 20; j++ { // traffic in flight while it closes (all refused)
			_, _ = unix.Write(fds[1], udpPkt(sbxIP, allowed, 5000+j, 9, []byte("x")))
			_, _ = unix.Write(fds[1], tcpSYN(sbxIP, allowed, 6000+j, 80))
		}
		r.Close()
		r.Close() // idempotent
		unix.Close(fds[0])
		defer unix.Close(fds[1]) // the sandbox's end stays up: no EOF to stop a reader
	}
	var n int
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if n = runtime.NumGoroutine(); n <= base+4 {
			return
		}
	}
	t.Fatalf("goroutines: %d before, %d after three relays closed", base, n)
}

// Close stops the TUN's writers too. gVisor checks that a route is still
// good, then writes to the link holding no lock, and its fdbased link
// writes straight to the fd: a flow goroutine between the two — sending the
// RST of a dial Close cancelled, say — reached the link after Close had
// returned, and wrote into whatever file had the fd's number by then. (So
// TestCloseTUN once read an earlier test's 40-byte RST.) Here the write
// reaches the NIC's link endpoint after Close, as such a goroutine's does.
func TestCloseStopsWriters(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Skip("socketpair:", err)
	}
	defer unix.Close(fds[1])
	r, err := Start(Config{TunFD: fds[0], Allow: allowAll})
	if err != nil {
		t.Fatal(err)
	}
	var link stack.LinkWriter = r.gate // the NIC's endpoint: the ICMP tap over the gate
	if r.icmp != nil {
		link = r.icmp
	}
	write := func() {
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(tcpSYN(allowed, sbxIP, 80, 41000)), // any packet
		})
		defer pkt.DecRef()
		pkt.NetworkProtocolNumber = header.IPv4ProtocolNumber
		var pkts stack.PacketBufferList
		pkts.PushBack(pkt)
		_, _ = link.WritePackets(pkts)
	}
	write() // reaches the TUN
	r.Close()
	write()
	unix.Close(fds[0])
	buf := make([]byte, 2048)
	if n, _, err := unix.Recvfrom(fds[1], buf, unix.MSG_DONTWAIT); n == 0 || err != nil {
		t.Fatalf("the packet written before Close: read %d, %v", n, err)
	}
	if n, _, err := unix.Recvfrom(fds[1], buf, unix.MSG_DONTWAIT); n != 0 || err != nil {
		t.Fatalf("a packet written after Close reached the TUN: read %d, %v — want EOF", n, err)
	}
}
