package relay

import (
	"net"
	"net/netip"
)

// Allow decides whether a flow to (ip, port) may leave. Called per new flow.
type Allow func(ip netip.Addr, port int) bool

// HairpinIP is the virtual address split-horizon DNS answers for PUBLISHED
// hostnames (plans/ingress.md ING-6): a tile that hardcodes its public name
// connects here, and the relay short-circuits the flow to the ingress path —
// an internal direct hop, never a real out-and-back (which the public-only
// egress policy would drop).
const HairpinIP = "10.0.2.4"

// Config configures a relay (see Start).
type Config struct {
	TunFD int
	Allow Allow // egress policy for IP destinations
	// AllowHost enables DNS-pinned HOSTNAME egress (D35): when set, the
	// relay inspects the DNS responses it forwards and records name→address
	// pins; a flow to a pinned PUBLIC address is admitted when AllowHost
	// approves (name, port). Set it only when the policy carries host rules
	// — the pin table is bounded but not free. Private/LAN answers are never
	// pinned (a hostname rule is internet-class, so DNS rebinding can't
	// steer a tile into the LAN).
	AllowHost func(name string, port int) bool
	Resolver  string         // host DNS server for :53 ("" = no DNS forwarding)
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
