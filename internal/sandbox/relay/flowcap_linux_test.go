//go:build linux

package relay

import (
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// Flow caps (plans/tile-sandbox-runtime.md §4, WP-10b): a relay runs inside
// xbind, so its flows are xbind's fds. These drive the caps through a real
// gVisor stack, as hardening_linux_test.go does.

// counts is the relay's admitted TCP and UDP flows now.
func (r *Relay) counts() (tcp, udp int) {
	r.flowMu.Lock()
	defer r.flowMu.Unlock()
	return r.nTCP, r.nUDP
}

// openFDs is how many fds this process holds.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd:", err)
	}
	return len(ents)
}

// drain reads everything the relay sends to the sandbox until the peer
// closes, handing each packet to fn.
func (h *harness) drain(fn func(header.IPv4)) {
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := h.peer.Read(buf)
			if err != nil {
				return
			}
			if n >= header.IPv4MinimumSize {
				if ip := header.IPv4(append([]byte(nil), buf[:n]...)); ip.IsValid(n) {
					fn(ip)
				}
			}
		}
	}()
}

// rstPort reports the sandbox port a RST is addressed to (0 = not a RST).
func rstPort(ip header.IPv4) uint16 {
	if ip.TransportProtocol() != header.TCPProtocolNumber {
		return 0
	}
	tcp := header.TCP(ip.Payload())
	if !tcp.Flags().Contains(header.TCPFlagRst) {
		return 0
	}
	return tcp.DestinationPort()
}

// synUntil sends a SYN from each port to dst:80 — again, round by round,
// for the ports that have no RST yet (the TUN drops what the sandbox doesn't
// read in time; a SYN resent for a flow still dialing is the same flow) —
// until want ports are reset. It returns the reset ports.
func (h *harness) synUntil(ports []uint16, dst netip.Addr, rst <-chan uint16, want int, sample func()) map[uint16]bool {
	h.t.Helper()
	reset := map[uint16]bool{}
	deadline := time.Now().Add(30 * time.Second)
	for len(reset) < want {
		if time.Now().After(deadline) {
			h.t.Fatalf("%d of %d ports reset after 30s", len(reset), want)
		}
		for _, p := range ports {
			if !reset[p] {
				h.send(tcpSYN(sbxIP, dst, p, 80))
			}
		}
		round := time.After(500 * time.Millisecond)
	collect:
		for len(reset) < want {
			select {
			case p := <-rst:
				reset[p] = true
			case <-round:
				break collect
			}
		}
		if sample != nil {
			sample()
		}
	}
	return reset
}

// 5000 concurrent connects through one relay with MaxTCP 1024: at most 1024
// dials reach the dialer, the rest are reset at once, and the process's fds
// stay bounded by the cap rather than by the flood.
func TestFlowCapTCP(t *testing.T) {
	const flood, limit = 5000, 1024
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	t.Cleanup(stop)
	var dials atomic.Int64
	d := net.Dialer{Control: func(string, string, syscall.RawConn) error {
		dials.Add(1)
		<-release // the dial holds its socket until the test lets go
		return errNoDial
	}}
	h := newHarnessDial(t, Config{Allow: allowAll, MaxTCP: limit}, d.DialContext)
	t.Cleanup(stop) // before the harness's cleanup closes the relay
	base := openFDs(t)
	rst := make(chan uint16, 2*flood)
	h.drain(func(ip header.IPv4) {
		if p := rstPort(ip); p != 0 {
			rst <- p
		}
	})

	ports := make([]uint16, flood)
	for i := range ports {
		ports[i] = uint16(20000 + i)
	}
	peak := 0
	t0 := time.Now()
	reset := h.synUntil(ports, allowed, rst, flood-limit, func() { peak = max(peak, openFDs(t)) })
	took := time.Since(t0)
	for end := time.Now().Add(2 * time.Second); dials.Load() < limit && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if n := dials.Load(); n != limit {
		t.Fatalf("%d dials reached the dialer, want exactly %d (the cap)", n, limit)
	}
	if n, _ := h.r.counts(); n != limit {
		t.Fatalf("the relay holds %d TCP flows, want %d", n, limit)
	}
	if len(reset) != flood-limit {
		t.Fatalf("%d ports reset, want %d", len(reset), flood-limit)
	}
	t.Logf("%d SYNs: %d dialed, %d reset in %v; fds %d → peak %d", flood, dials.Load(), len(reset), took, base, peak)
	if peak-base > limit+128 {
		t.Fatalf("open fds grew by %d under a cap of %d", peak-base, limit)
	}
	if took > 20*time.Second {
		t.Fatalf("the refusals took %v: a flow past the cap must be reset at once", took)
	}
	if st := h.r.Stats(); st.Denied < int64(flood-limit) {
		t.Fatalf("refused flows must be recorded as denied: %+v", st.Denied)
	}

	// The held dials fail: their flows end and give their slots back.
	stop()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if n, _ := h.r.counts(); n == 0 {
			break
		}
		if time.Now().After(end) {
			n, _ := h.r.counts()
			t.Fatalf("%d TCP flows still held after their dials failed", n)
		}
	}
}

// One Budget shared by two relays admits 100 flows in all, and a relay's
// Close gives its share back before it returns.
func TestFlowBudgetShared(t *testing.T) {
	b := NewBudget(100)
	var dials atomic.Int64
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		dials.Add(1)
		<-ctx.Done() // held until the relay closes
		return nil, ctx.Err()
	}
	var hs [2]*harness
	var rsts [2]chan uint16
	for i := range hs {
		hs[i] = newHarnessDial(t, Config{Allow: allowAll, Budget: b}, dial)
		rsts[i] = make(chan uint16, 1024)
		ch := rsts[i]
		hs[i].drain(func(ip header.IPv4) {
			if p := rstPort(ip); p != 0 {
				ch <- p
			}
		})
	}
	ports := make([]uint16, 80)
	for i := range ports {
		ports[i] = uint16(30000 + i)
	}
	// Both relays fill the budget together: send on both, then take the
	// RSTs of whichever lost the race.
	for _, h := range hs {
		for _, p := range ports {
			h.send(tcpSYN(sbxIP, allowed, p, 80))
		}
	}
	for end := time.Now().Add(5 * time.Second); dials.Load() < 100 && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	var reset [2]map[uint16]bool
	for i, h := range hs {
		held, _ := h.r.counts()
		reset[i] = h.synUntil(ports, allowed, rsts[i], len(ports)-held, nil)
	}
	// Let the resent SYNs still queued be refused before a slot frees up.
	for last := int64(-1); ; time.Sleep(200 * time.Millisecond) {
		n := hs[0].r.Stats().Denied + hs[1].r.Stats().Denied
		if n == last {
			break
		}
		last = n
	}
	h0, _ := hs[0].r.counts()
	h1, _ := hs[1].r.counts()
	if n := dials.Load(); n != 100 || h0+h1 != 100 || b.Used() != 100 || b.Cap() != 100 {
		t.Fatalf("dials %d, held %d+%d, budget %d/%d: want 100 in all", n, h0, h1, b.Used(), b.Cap())
	}
	if len(reset[0])+len(reset[1]) != 60 {
		t.Fatalf("%d+%d reset, want 60", len(reset[0]), len(reset[1]))
	}
	hs[0].r.Close()
	if got := b.Used(); got != h1 {
		t.Fatalf("after one relay closed the budget holds %d, want the other's %d", got, h1)
	}
	hs[1].r.Close()
	if got := b.Used(); got != 0 {
		t.Fatalf("after both closed the budget holds %d", got)
	}
}

// unreachTo matches the ICMP port-unreachable a UDP datagram from sport to
// dst gets.
func unreachTo(dst netip.Addr, sport uint16) func(header.IPv4) bool {
	return func(ip header.IPv4) bool {
		if ip.TransportProtocol() != header.ICMPv4ProtocolNumber {
			return false
		}
		ic := header.ICMPv4(ip.Payload())
		if ic.Type() != header.ICMPv4DstUnreachable || ic.Code() != header.ICMPv4PortUnreachable {
			return false
		}
		orig := header.IPv4(ic.Payload())
		if len(orig) < header.IPv4MinimumSize || len(orig) < int(orig.HeaderLength())+header.UDPMinimumSize {
			return false
		}
		u := header.UDP(orig[orig.HeaderLength():])
		return netip.AddrFrom4(orig.DestinationAddress().As4()) == dst && u.SourcePort() == sport
	}
}

// pipeDial answers UDP dials with one end of a pipe (a flow that stays
// open), handing the other end to the test; TCP dials hold until the relay
// closes.
func pipeDial(ends chan<- net.Conn) dialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		if network == "udp" {
			a, b := net.Pipe()
			ends <- b
			return a, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

// MaxUDP: a datagram of a flow past the cap gets an ICMP port-unreachable at
// once and is recorded as denied.
func TestFlowCapUDP(t *testing.T) {
	ends := make(chan net.Conn, 8)
	h := newHarnessDial(t, Config{Allow: allowAll, MaxUDP: 2}, pipeDial(ends))
	for _, sp := range []uint16{50001, 50002} {
		h.send(udpPkt(sbxIP, allowed, sp, 9, []byte("hi")))
		select {
		case c := <-ends:
			t.Cleanup(func() { c.Close() })
		case <-time.After(2 * time.Second):
			t.Fatalf("flow from %d was not dialed", sp)
		}
	}
	t0 := time.Now()
	h.send(udpPkt(sbxIP, allowed, 50003, 9, []byte("hi")))
	if h.recv(time.Second, unreachTo(allowed, 50003)) == nil {
		t.Fatal("a datagram past MaxUDP must get a port-unreachable")
	}
	if took := time.Since(t0); took > 200*time.Millisecond {
		t.Fatalf("the port-unreachable took %v", took)
	}
	select {
	case <-ends:
		t.Fatal("a flow past MaxUDP was dialed")
	default:
	}
	if _, n := h.r.counts(); n != 2 {
		t.Fatalf("the relay holds %d UDP flows, want 2", n)
	}
	if st := h.r.Stats(); st.Denied != 1 {
		t.Fatalf("the refused flow is recorded as denied: %d denied", st.Denied)
	}
}

// Closing a relay returns its budget at once, UDP included (a UDP flow used
// to linger until its 30 s idle timeout), and closes every flow's host side.
func TestFlowCloseReturnsBudget(t *testing.T) {
	b := NewBudget(10)
	ends := make(chan net.Conn, 16)
	h := newHarnessDial(t, Config{Allow: allowAll, Budget: b}, pipeDial(ends))
	var pipes []net.Conn
	for sp := uint16(50001); sp <= 50004; sp++ {
		h.send(udpPkt(sbxIP, allowed, sp, 9, []byte("hi")))
		select {
		case c := <-ends:
			pipes = append(pipes, c)
			t.Cleanup(func() { c.Close() })
		case <-time.After(2 * time.Second):
			t.Fatalf("flow from %d was not dialed", sp)
		}
	}
	for sp := uint16(41001); sp <= 41006; sp++ { // clear of h.port()'s 40001…
		h.send(tcpSYN(sbxIP, allowed, sp, 80))
	}
	for end := time.Now().Add(3 * time.Second); b.Used() < 10 && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	if tcp, udp := h.r.counts(); b.Used() != 10 || tcp != 6 || udp != 4 {
		t.Fatalf("budget %d, flows tcp %d udp %d: want 10 = 6 + 4", b.Used(), tcp, udp)
	}
	// The budget is spent: another flow of either kind is refused at once.
	if reset, _ := h.synTo(allowed, 80); !reset {
		t.Fatal("a SYN past the budget must be reset")
	}
	h.send(udpPkt(sbxIP, allowed, 50009, 9, []byte("hi")))
	if h.recv(time.Second, unreachTo(allowed, 50009)) == nil {
		t.Fatal("a datagram past the budget must get a port-unreachable")
	}

	h.r.Close()
	if got := b.Used(); got != 0 {
		t.Fatalf("Close returned with %d budget slots still held", got)
	}
	for i, c := range pipes {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, err := io.Copy(io.Discard, c) // the datagram, then the close
		if err != nil {
			t.Fatalf("UDP flow %d: its host side must be closed by Close, got %v", i, err)
		}
	}
}

// StrictPublic: DNS pins judge addresses by the strict "internet" test, so
// a hostname rule never reaches CGNAT, benchmarking, reserved or NAT64 space
// through an answer; without it, pins are as before.
func TestDNSPinningStrict(t *testing.T) {
	for _, strict := range []bool{false, true} {
		r := pinRelay(func(name string, port int) bool { return name == "api.example.com" })
		r.strict = strict
		pub := netip.MustParseAddr("203.0.113.7")
		r.pin("api.example.com", pub, 300)
		if !r.permitted(pub, 443) {
			t.Fatalf("strict=%v: a public pin passes", strict)
		}
		for _, s := range []string{"100.100.1.2", "198.18.0.9", "240.0.0.1", "64:ff9b::808:808", "64:ff9b:1::5", "::ffff:100.64.0.1"} {
			ip := netip.MustParseAddr(s)
			r.pin("api.example.com", ip, 300)
			if got := r.permitted(ip, 443); got == strict {
				t.Errorf("strict=%v: a pin of %s permitted=%v", strict, s, got)
			}
		}
	}
}

// CloseTUN hands the TUN fd to the relay: Close shuts it after the readers
// stop, so the sandbox's end reads EOF. Close closes the fd before it
// returns, so the EOF is there at once: the reads don't wait. (A packet
// ahead of the EOF would be the relay's, sent before Close; this relay has
// no flows and sends none. It once read another relay's late RST, written
// into this socket because it took that relay's closed TUN fd number:
// TestCloseStopsWriters.)
func TestCloseTUN(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Skip("socketpair:", err)
	}
	defer unix.Close(fds[1])
	r, err := Start(Config{TunFD: fds[0], CloseTUN: true, Processors: 1})
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	r.Close() // idempotent: the fd is closed once
	n, _, err := unix.Recvfrom(fds[1], make([]byte, 2048), unix.MSG_DONTWAIT)
	if n != 0 || err != nil {
		t.Fatalf("the sandbox's end: read %d, %v — want EOF once the relay closed the TUN", n, err)
	}
}

// HostDeny on Linux asks the routing table per flow: every address of this
// host is local, a documentation address isn't.
func TestRouteLookupThisHost(t *testing.T) {
	local := hostLocal()
	if !local(netip.MustParseAddr("127.0.0.1")) {
		t.Error("127.0.0.1 has a local route")
	}
	as, _ := net.InterfaceAddrs()
	for _, a := range as {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && !ip.Unmap().IsLinkLocalUnicast() {
				if !local(ip) {
					t.Errorf("host address %s: its route is not local", ip)
				}
			}
		}
	}
	for _, s := range []string{"192.0.2.1", "2001:db8::77"} {
		if local(netip.MustParseAddr(s)) {
			t.Errorf("%s is not this host's", s)
		}
	}
}

// A dropped HostDeny closes its netlink socket: one per relay would
// otherwise leak with every sandbox start.
func TestHostDenyReleasesSocket(t *testing.T) {
	runtime.GC()
	base := openFDs(t)
	for i := 0; i < 64; i++ {
		d := HostDeny()
		_ = d(netip.MustParseAddr("192.0.2.1"))
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		runtime.GC()
		n := openFDs(t)
		if n <= base+4 {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("fds %d → %d after 64 dropped HostDenys", base, n)
		}
	}
}

// A failed lookup denies; no route passes the flow on.
func TestRouteLookupErrors(t *testing.T) {
	l := &routeLookup{sock: &nlSock{fd: -1}, buf: make([]byte, 8192)}
	defer l.sock.close()
	if _, err := l.routeType(netip.Addr{}); err == nil || !l.local(netip.Addr{}) {
		t.Fatal("an invalid address must fail the lookup and deny")
	}
	if typ, err := l.routeType(netip.MustParseAddr("127.0.0.1")); err != nil || typ != unix.RTN_LOCAL {
		t.Fatalf("127.0.0.1: %d, %v", typ, err)
	}
}

// A closed relay holds no fd: not the TUN (CloseTUN), not gVisor's stop
// eventfd (stopFDs), not the ICMP tap's dup.
func TestCloseReleasesFDs(t *testing.T) {
	runtime.GC()
	base := openFDs(t)
	for i := 0; i < 20; i++ {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Skip("socketpair:", err)
		}
		r, err := Start(Config{TunFD: fds[0], CloseTUN: true, Processors: i % 2, Deny: HostDeny()})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.stopFDs) != 1 {
			t.Fatalf("found %d stop eventfds in gVisor's endpoint, want 1 — did a gVisor bump move them (stopfd_linux.go)?", len(r.stopFDs))
		}
		r.Close()
		unix.Close(fds[1])
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		runtime.GC() // the HostDenys' netlink sockets go with their relays
		if n := openFDs(t); n <= base+2 {
			return
		} else if time.Now().After(end) {
			t.Fatalf("fds %d → %d after 20 relays started and closed", base, n)
		}
	}
}
