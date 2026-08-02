//go:build linux

// Package relay is the userspace egress path for a sandboxed component
// (plans/isolation.md §3): a gVisor TCP/IP stack bound to a TUN inside the
// component's network namespace. Every outbound flow is checked against an
// allow func; permitted flows are dialed from the host and spliced, denied ones
// are reset. DNS (:53) is answered by forwarding to a host resolver. This is
// what turns net:* grants into actual, filtered egress.
package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const nicID = 1
const recentFlows = 64

// gatewayIP is the virtual gateway inside a relay netns (sandbox.GatewayIP;
// duplicated here so the relay stays importable standalone). DialIn flows
// source from it — the backend's default gateway.
const gatewayIP = "10.0.2.2"

// udpIdleTimeout ends a UDP flow after this much silence. UDP has no close, so
// without it a flow (e.g. a DNS query/reply) would show as "open" forever and
// keep counting toward Active. Any datagram in either direction refreshes it —
// mirroring conntrack's UDP idle expiry — so a live flow stays up.
const udpIdleTimeout = 30 * time.Second

// Relay owns a gVisor stack forwarding a component's egress. The same stack
// is the component's INBOUND door (plans/ingress.md): DialIn opens a flow
// from the host side to a port the backend listens on inside its netns — the
// TUN carries it like any other packet, no setns and no extra privilege.
type Relay struct {
	stack     *stack.Stack
	dial      net.Dialer
	allow     Allow
	gateway   netip.Addr     // virtual gateway IP that host-forwards apply to
	hostFwd   map[int]string // gateway port → host dial addr (e.g. xbind)
	hostDial  func(dst string) (net.Conn, error)
	tileIP    netip.Addr                       // the in-netns peer address DialIn targets
	hairpin   netip.Addr                       // split-horizon VIP (invalid = off)
	hairDial  func(port int) (net.Conn, error) // ingress-path dial for hairpin flows
	icmp      *icmpTap                         // link-layer ICMP echo forwarder (nil if setup failed)
	allowHost func(name string, port int) bool // host-rule policy (D35; nil = no host rules)

	pinMu sync.Mutex
	pins  map[netip.Addr]map[string]int64 // DNS pins: addr → name → expiry (unix)

	mu      sync.Mutex
	allowed int64
	denied  int64
	active  int64
	txBytes int64
	rxBytes int64
	ring    []*Flow // most-recent-last, capped at recentFlows
}

// Stats returns a snapshot of this relay's activity.
func (r *Relay) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	recent := make([]Flow, 0, len(r.ring))
	for i := len(r.ring) - 1; i >= 0; i-- { // newest first
		recent = append(recent, *r.ring[i])
	}
	return Stats{
		Allowed: r.allowed, Denied: r.denied, Active: r.active,
		TxBytes: r.txBytes, RxBytes: r.rxBytes, Recent: recent,
	}
}

// record starts a flow record, updates counters, and returns it (nil for a
// denied flow beyond the count, which we still tally).
func (r *Relay) record(proto string, ip netip.Addr, port int, allowed bool) *Flow {
	f := &Flow{Proto: proto, Dst: ip.String(), Port: port, Allowed: allowed, Start: nowMS()}
	r.mu.Lock()
	if allowed {
		r.allowed++
		r.active++
	} else {
		r.denied++
		f.End = f.Start
	}
	r.ring = append(r.ring, f)
	if len(r.ring) > recentFlows {
		r.ring = r.ring[len(r.ring)-recentFlows:]
	}
	r.mu.Unlock()
	return f
}

// finish closes out an allowed flow with its byte counts.
func (r *Relay) finish(f *Flow, tx, rx int64) {
	r.mu.Lock()
	f.TxBytes, f.RxBytes, f.End = tx, rx, nowMS()
	r.active--
	r.txBytes += tx
	r.rxBytes += rx
	r.mu.Unlock()
}

func nowMS() int64 { return time.Now().UnixMilli() }

// permitted is the full egress decision: the static IP policy, or — when
// host rules are configured — a DNS pin naming this address whose hostname
// the policy allows at this port (D35).
func (r *Relay) permitted(ip netip.Addr, port int) bool {
	if r.allow != nil && r.allow(ip, port) {
		return true
	}
	if r.allowHost == nil {
		return false
	}
	now := time.Now().Unix()
	r.pinMu.Lock()
	names := r.pins[ip]
	var live []string
	for name, exp := range names {
		if exp >= now {
			live = append(live, name)
		} else {
			delete(names, name)
		}
	}
	r.pinMu.Unlock()
	for _, name := range live {
		if r.allowHost(name, port) {
			return true
		}
	}
	return false
}

// maxPins bounds the pin table (a hostile resolver can't balloon memory);
// far above any real tile's working set of names.
const maxPins = 4096

// pin records a DNS answer (name → addr) for ttl seconds. Only public
// addresses pin — hostname egress is internet-class, so a name resolving
// into RFC1918/loopback (DNS rebinding) confers nothing.
func (r *Relay) pin(name string, addr netip.Addr, ttl uint32) {
	if r.pins == nil || !publicAddr(addr) {
		return
	}
	if ttl < 60 {
		ttl = 60 // don't churn on aggressive TTLs; re-resolution refreshes anyway
	} else if ttl > 3600 {
		ttl = 3600
	}
	exp := time.Now().Unix() + int64(ttl) + 30 // grace: in-flight connects after expiry
	r.pinMu.Lock()
	defer r.pinMu.Unlock()
	if len(r.pins) >= maxPins {
		now := time.Now().Unix()
		for a, names := range r.pins { // drop expired first
			for n, e := range names {
				if e < now {
					delete(names, n)
				}
			}
			if len(names) == 0 {
				delete(r.pins, a)
			}
		}
		if len(r.pins) >= maxPins {
			return // table full of live pins — refuse new ones over losing old
		}
	}
	m := r.pins[addr]
	if m == nil {
		m = map[string]int64{}
		r.pins[addr] = m
	}
	if e, ok := m[name]; !ok || exp > e {
		m[name] = exp
	}
}

// publicAddr mirrors the sandbox policy's "internet" test (kept local — the
// parent package imports us).
func publicAddr(ip netip.Addr) bool {
	return ip.IsValid() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified()
}

// Start attaches a stack to a TUN (opened inside the target netns and passed to
// us) and begins forwarding under cfg.
func Start(cfg Config) (*Relay, error) {
	ep, err := fdbased.New(&fdbased.Options{FDs: []int{cfg.TunFD}, MTU: 1500})
	if err != nil {
		return nil, err
	}
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	r := &Relay{
		stack: s, allow: cfg.Allow, dial: net.Dialer{Timeout: 15 * time.Second},
		gateway: cfg.Gateway, hostFwd: cfg.HostFwd, hostDial: cfg.HostDial,
		tileIP: cfg.TileIP, hairDial: cfg.HairpinDial, allowHost: cfg.AllowHost,
	}
	if r.allowHost != nil {
		r.pins = map[netip.Addr]map[string]int64{}
	}
	if !r.tileIP.IsValid() {
		r.tileIP = netip.MustParseAddr("10.0.2.15") // the egress TUN default
	}
	if cfg.Published != nil && cfg.HairpinDial != nil {
		r.hairpin = netip.MustParseAddr(HairpinIP)
	}

	// Interpose an ICMP-echo forwarder at the link layer so `ping` works
	// (gVisor has no ICMP forwarder). Best-effort — on failure we just lack ping.
	var nic stack.LinkEndpoint = ep
	if tap, e := newICMPTap(ep, cfg.TunFD, r); e == nil {
		r.icmp = tap
		nic = tap
	}
	if e := s.CreateNIC(nicID, nic); e != nil {
		if r.icmp != nil {
			r.icmp.close()
		}
		return nil, errf(e)
	}
	// Accept packets addressed to anyone (we're a transparent gateway).
	s.SetPromiscuousMode(nicID, true)
	s.SetSpoofing(nicID, true)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	tcpFwd := tcp.NewForwarder(s, 0, 2048, r.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s, r.udpHandler(cfg))
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	return r, nil
}

func (r *Relay) Close() {
	r.stack.Close()
	if r.icmp != nil {
		r.icmp.close()
	}
}

func (r *Relay) handleTCP(req *tcp.ForwarderRequest) {
	id := req.ID()
	ip, ok := addr(id.LocalAddress)
	port := int(id.LocalPort)

	// Gateway host-forward: flows to the virtual gateway IP on a mapped port go
	// to a host service (e.g. xbind), policy-exempt — that's how a netns-isolated
	// terminal reaches the workspace controller without seeing host interfaces.
	if r.gateway.IsValid() && ip == r.gateway {
		host, mapped := r.hostFwd[port]
		if !mapped {
			r.record("tcp", ip, port, false)
			req.Complete(true)
			return
		}
		r.proxyTCP(req, ip, port, host)
		return
	}

	// Split-horizon hairpin (plans/ingress.md ING-6): flows to the published-
	// services VIP take the internal ingress path. Policy-exempt like the
	// gateway forwards — the target is a PUBLISHED endpoint, reachable by the
	// whole internet; the tile arrives as the same anonymous ingress caller.
	if r.hairpin.IsValid() && ip == r.hairpin {
		out, err := r.hairDial(port)
		if err != nil {
			f := r.record("tcp", ip, port, true)
			r.finish(f, 0, 0)
			req.Complete(true)
			return
		}
		r.spliceTCP(req, ip, port, out)
		return
	}

	if !ok || !r.permitted(ip, port) {
		r.record("tcp", ip, port, false) // RST — denied
		req.Complete(true)
		return
	}
	r.proxyTCP(req, ip, port, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
}

// proxyTCP dials dst on the host, accepts the netns-side endpoint, and splices
// them, recording the flow under (ip, port). "unix:<path>" targets dial a
// unix socket (the ingress-forward door); cfg.HostDial overrides entirely.
func (r *Relay) proxyTCP(req *tcp.ForwarderRequest, ip netip.Addr, port int, dst string) {
	var out net.Conn
	var err error
	switch {
	case r.hostDial != nil:
		out, err = r.hostDial(dst)
	case strings.HasPrefix(dst, "unix:"):
		out, err = r.dial.Dial("unix", strings.TrimPrefix(dst, "unix:"))
	default:
		out, err = r.dial.Dial("tcp", dst)
	}
	if err != nil {
		f := r.record("tcp", ip, port, true) // allowed, but unreachable
		r.finish(f, 0, 0)
		req.Complete(true)
		return
	}
	r.spliceTCP(req, ip, port, out)
}

// spliceTCP accepts the netns-side endpoint and splices it with an
// already-dialed host-side conn, recording the flow under (ip, port).
func (r *Relay) spliceTCP(req *tcp.ForwarderRequest, ip netip.Addr, port int, out net.Conn) {
	var wq waiter.Queue
	gep, e := req.CreateEndpoint(&wq)
	if e != nil {
		out.Close()
		f := r.record("tcp", ip, port, true)
		r.finish(f, 0, 0)
		req.Complete(true)
		return
	}
	req.Complete(false)
	in := gonet.NewTCPConn(&wq, gep)
	f := r.record("tcp", ip, port, true)
	go func() { tx, rx := splice(in, out); r.finish(f, tx, rx) }()
}

// DialIn opens a flow from the host side INTO the netns — to a port the
// backend listens on (plans/ingress.md: the L4 ingress plane and the last
// hop of every published route). The gVisor stack sources the flow from the
// virtual gateway address, so to the backend it's an ordinary connection
// from its default gateway.
func (r *Relay) DialIn(ctx context.Context, proto string, port int) (net.Conn, error) {
	if r == nil {
		return nil, errors.New("no relay")
	}
	remote := tcpip.FullAddress{NIC: nicID, Addr: tcpip.AddrFrom4(r.tileIP.As4()), Port: uint16(port)}
	local := tcpip.FullAddress{NIC: nicID, Addr: tcpip.AddrFrom4(netip.MustParseAddr(gatewayIP).As4())}
	if proto == "udp" {
		return gonet.DialUDP(r.stack, &local, &remote, ipv4.ProtocolNumber)
	}
	return gonet.DialTCPWithBind(ctx, r.stack, local, remote, ipv4.ProtocolNumber)
}

// udpHandler forwards UDP flows: DNS (:53) is relayed to the configured host
// resolver (so name resolution works for net:internet) — with published
// hostnames answered locally (split horizon) when configured; other UDP is
// subject to the same allow check as TCP.
func (r *Relay) udpHandler(cfg Config) func(*udp.ForwarderRequest) bool {
	resolver := cfg.Resolver
	return func(req *udp.ForwarderRequest) bool {
		id := req.ID()
		ip, ok := addr(id.LocalAddress)
		port := int(id.LocalPort)
		dst := net.JoinHostPort(ip.String(), strconv.Itoa(port))
		dns := port == 53 && resolver != ""
		if dns {
			dst = resolver // pin DNS to the host resolver
		} else if !ok || !r.permitted(ip, port) {
			return true // consumed (dropped)
		}
		var wq waiter.Queue
		gep, e := req.CreateEndpoint(&wq)
		if e != nil {
			return true
		}
		out, err := r.dial.Dial("udp", dst)
		if err != nil {
			gep.Close()
			return true
		}
		in := gonet.NewUDPConn(&wq, gep)
		f := r.record("udp", ip, port, true)
		if dns && ((r.hairpin.IsValid() && cfg.Published != nil) || r.pins != nil) {
			pub := cfg.Published
			if pub == nil {
				pub = func(string) bool { return false }
			}
			go func() {
				tx, rx := spliceDNS(in, out, pub, r.hairpin, udpIdleTimeout, r.pinAnswers)
				r.finish(f, tx, rx)
			}()
			return true
		}
		go func() { tx, rx := spliceUDPIdle(in, out, udpIdleTimeout); r.finish(f, tx, rx) }()
		return true
	}
}

// splice copies bidirectionally and returns (a→b, b→a) byte counts.
func splice(a, b net.Conn) (aToB, bToA int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn, n *int64) {
		defer wg.Done()
		c, _ := io.Copy(dst, src)
		*n = c
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			dst.SetReadDeadline(time.Now().Add(2 * time.Second))
		}
	}
	go cp(b, a, &aToB) // a → b
	go cp(a, b, &bToA) // b → a
	wg.Wait()
	a.Close()
	b.Close()
	return aToB, bToA
}

// spliceUDPIdle copies bidirectionally like splice, but — since UDP has no close
// — treats an idle stretch (no datagram either way for idle) as end-of-flow.
// Every datagram in either direction refreshes the shared read deadline, so a
// live flow keeps going while a quiet one is reaped (conntrack-style). Read
// deadlines are goroutine-safe, so the two directions can extend concurrently.
func spliceUDPIdle(a, b net.Conn, idle time.Duration) (aToB, bToA int64) {
	extend := func() {
		d := time.Now().Add(idle)
		_ = a.SetReadDeadline(d)
		_ = b.SetReadDeadline(d)
	}
	extend()
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn, n *int64) {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			k, err := src.Read(buf)
			if k > 0 {
				extend()
				w, _ := dst.Write(buf[:k])
				*n += int64(w)
			}
			if err != nil { // idle deadline or peer gone
				return
			}
		}
	}
	go cp(b, a, &aToB) // a → b
	go cp(a, b, &bToA) // b → a
	wg.Wait()
	a.Close()
	b.Close()
	return aToB, bToA
}

func addr(a tcpip.Address) (netip.Addr, bool) {
	switch a.Len() {
	case 4:
		return netip.AddrFrom4(a.As4()), true
	case 16:
		return netip.AddrFrom16(a.As16()), true
	}
	return netip.Addr{}, false
}

type tcpipErr struct{ e tcpip.Error }

func (t tcpipErr) Error() string { return t.e.String() }
func errf(e tcpip.Error) error   { return tcpipErr{e} }
