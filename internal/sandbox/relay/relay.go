//go:build linux

// Package relay is the userspace egress path for a sandboxed component
// (plans/isolation.md §3): a gVisor TCP/IP stack bound to a TUN inside the
// component's network namespace. Every outbound flow is checked against an
// allow func; permitted flows are dialed from the host and spliced, denied ones
// are reset. DNS (:53) is answered by forwarding to a host resolver. This is
// what turns net:* grants into actual, filtered egress.
package relay

import (
	"io"
	"net"
	"net/netip"
	"strconv"
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

// Allow decides whether a flow to (ip, port) may leave. Called per new flow.
type Allow func(ip netip.Addr, port int) bool

// Relay owns a gVisor stack forwarding a component's egress.
type Relay struct {
	stack *stack.Stack
	dial  net.Dialer
	allow Allow
}

// Start attaches a stack to tunFD (a TUN opened inside the target netns and
// passed to us) and begins forwarding. resolver is the host DNS server to
// relay UDP :53 to (e.g. "1.1.1.1:53" or the host's resolv.conf server).
func Start(tunFD int, allow Allow, resolver string) (*Relay, error) {
	ep, err := fdbased.New(&fdbased.Options{FDs: []int{tunFD}, MTU: 1500})
	if err != nil {
		return nil, err
	}
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	if e := s.CreateNIC(nicID, ep); e != nil {
		return nil, errf(e)
	}
	// Accept packets addressed to anyone (we're a transparent gateway).
	s.SetPromiscuousMode(nicID, true)
	s.SetSpoofing(nicID, true)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	r := &Relay{stack: s, allow: allow, dial: net.Dialer{Timeout: 15 * time.Second}}

	tcpFwd := tcp.NewForwarder(s, 0, 2048, r.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s, r.udpHandler(resolver))
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	return r, nil
}

func (r *Relay) Close() { r.stack.Close() }

func (r *Relay) handleTCP(req *tcp.ForwarderRequest) {
	id := req.ID()
	ip, ok := addr(id.LocalAddress)
	port := int(id.LocalPort)
	if !ok || !r.allow(ip, port) {
		req.Complete(true) // RST — denied
		return
	}
	out, err := r.dial.Dial("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		req.Complete(true)
		return
	}
	var wq waiter.Queue
	gep, e := req.CreateEndpoint(&wq)
	if e != nil {
		out.Close()
		req.Complete(true)
		return
	}
	req.Complete(false)
	in := gonet.NewTCPConn(&wq, gep)
	go splice(in, out)
}

// udpHandler forwards UDP flows: DNS (:53) is relayed to the configured host
// resolver (so name resolution works for net:internet); other UDP is subject to
// the same allow check as TCP.
func (r *Relay) udpHandler(resolver string) func(*udp.ForwarderRequest) bool {
	return func(req *udp.ForwarderRequest) bool {
		id := req.ID()
		ip, ok := addr(id.LocalAddress)
		port := int(id.LocalPort)
		dst := net.JoinHostPort(ip.String(), strconv.Itoa(port))
		if port == 53 && resolver != "" {
			dst = resolver // pin DNS to the host resolver
		} else if !ok || !r.allow(ip, port) {
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
		go splice(in, out)
		return true
	}
}

func splice(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		} else {
			dst.SetReadDeadline(time.Now().Add(2 * time.Second))
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
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
