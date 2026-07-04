package relay

import "net/netip"

// Allow decides whether a flow to (ip, port) may leave. Called per new flow.
type Allow func(ip netip.Addr, port int) bool

// Config configures a relay (see Start).
type Config struct {
	TunFD    int
	Allow    Allow          // egress policy for IP destinations
	Resolver string         // host DNS server for :53 (e.g. "1.1.1.1:53")
	Gateway  netip.Addr     // virtual gateway IP (10.0.2.2); host-forwards apply here
	HostFwd  map[int]string // gateway port → host address (e.g. buxond), policy-exempt
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
