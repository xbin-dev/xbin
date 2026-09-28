package relay

import (
	"net"
	"net/netip"
)

// Allow decides whether a flow to (ip, port) may leave. Called per new flow.
type Allow func(ip netip.Addr, port int) bool

// Deny names destinations a sandbox may never reach, whatever Allow or a DNS
// pin says: the host itself (HostDeny). The relay checks it before any other
// decision — TCP (the gateway forwards and the hairpin included), UDP, ICMP
// and flows to DNS-pinned hosts — so a relay with Deny has no gateway
// services. The relay's own DNS service is not a flow to the address a query
// was sent to (it answers locally or forwards to Config.Resolver), so Deny
// doesn't apply to it.
type Deny func(ip netip.Addr) bool

// HairpinIP is the virtual address split-horizon DNS answers for PUBLISHED
// hostnames (plans/ingress.md ING-6): a tile that hardcodes its public name
// connects here, and the relay short-circuits the flow to the ingress path —
// an internal direct hop, never a real out-and-back (which the public-only
// egress policy would drop).
const HairpinIP = "10.0.2.4"

// Config configures a relay (see Start).
type Config struct {
	// TunFD is the sandbox's egress TUN. The caller keeps owning it — close
	// it after Relay.Close returns (Close stops the TUN's readers first), or
	// after a failed Start — unless CloseTUN hands it to the relay, which
	// then closes it at those two points itself.
	TunFD    int
	CloseTUN bool
	Allow    Allow // egress policy for IP destinations; nil = none
	// Deny is checked before everything else (see Deny); nil = nothing is
	// denied beyond what Allow refuses.
	Deny Deny
	// DNSRefuse answers every DNS query (UDP :53) with REFUSED at once
	// instead of forwarding it to Resolver: for a policy that grants
	// nothing, so a lookup fails fast and DNS is no exfiltration channel.
	DNSRefuse bool
	// Processors is how many goroutines process the TUN's inbound packets;
	// 0 = gVisor's default, one per CPU. Every relay pays for them, so a
	// relay per sandbox passes 1.
	Processors int

	// MaxTCP and MaxUDP cap this relay's concurrent TCP and UDP flows (0 =
	// no cap: backends and terminals). Budget, when set, caps them together
	// with every other relay that shares it (a ping holds a Budget slot
	// too). A flow takes its slots before it dials and gives them back when
	// it closes; past a cap a TCP SYN is answered with a RST and a UDP
	// datagram with an ICMP port-unreachable, at once, and the flow is
	// recorded as denied. The relay runs inside xbind, so these are what
	// keep a sandbox from spending xbind's fds and memory.
	MaxTCP, MaxUDP int
	Budget         *Budget
	// StrictPublic judges the addresses DNS pins may name (AllowHost) with
	// the strict "internet" test of a tile sandbox's policy
	// (sandbox.EgressPolicy.Strict): besides private, loopback, link-local
	// and multicast addresses it refuses CGNAT (100.64.0.0/10), benchmarking
	// (198.18.0.0/15), reserved (240.0.0.0/4) and NAT64 (64:ff9b::/96,
	// 64:ff9b:1::/48) ones. Set it with a strict policy's Allow.
	StrictPublic bool

	// AllowHost enables DNS-pinned HOSTNAME egress (D35): when set, the
	// relay inspects the DNS responses it forwards and records name→address
	// pins; a flow to a pinned PUBLIC address is admitted when AllowHost
	// approves (name, port). Set it only when the policy carries host rules
	// — the pin table is bounded but not free. Private/LAN answers are never
	// pinned (a hostname rule is internet-class, so DNS rebinding can't
	// steer a tile into the LAN).
	AllowHost func(name string, port int) bool
	Resolver  string         // host DNS server for :53 ("" = no DNS forwarding; DNSRefuse wins)
	Gateway   netip.Addr     // virtual gateway IP (10.0.2.2); host-forwards apply here
	HostFwd   map[int]string // gateway port → host dial address, policy-exempt
	// HostDial, when set, dials HostFwd targets (so values may be "unix:<path>"
	// or other schemes the host side understands); nil = net.Dial("tcp", dst).
	HostDial func(dst string) (net.Conn, error)

	// TileIP is the in-netns address of the sandboxed peer — the address
	// DialIn connects to (default 10.0.2.15, the egress TUN's).
	TileIP netip.Addr

	// Split-horizon resolution for published hostnames (plans/ingress.md):
	// DNS queries for names Published() reports true for are answered with
	// HairpinIP, and TCP flows to HairpinIP are handed to HairpinDial instead
	// of the public internet. Both nil = no split horizon.
	Published   func(host string) bool
	HairpinDial func(port int) (net.Conn, error)
}

// Flow is one observed egress connection (for network-activity visibility).
type Flow struct {
	Proto   string `json:"proto"`
	Dst     string `json:"dst"`
	Port    int    `json:"port"`
	Allowed bool   `json:"allowed"`
	TxBytes int64  `json:"txBytes"`
	RxBytes int64  `json:"rxBytes"`
	Start   int64  `json:"start"` // unix ms
	End     int64  `json:"end"`   // unix ms, 0 = still open
}

// Stats is a snapshot of a relay's network activity.
type Stats struct {
	Allowed int64  `json:"allowed"`
	Denied  int64  `json:"denied"`
	Active  int64  `json:"active"`
	TxBytes int64  `json:"txBytes"`
	RxBytes int64  `json:"rxBytes"`
	Recent  []Flow `json:"recent"`
}
