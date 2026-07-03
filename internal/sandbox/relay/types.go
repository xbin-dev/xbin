package relay

import "net/netip"

// Allow decides whether a flow to (ip, port) may leave. Called per new flow.
type Allow func(ip netip.Addr, port int) bool

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
