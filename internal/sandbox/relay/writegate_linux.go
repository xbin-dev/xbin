//go:build linux

package relay

import (
	"sync"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// writeGate is the TUN's endpoint as the stack writes to it, with a gate
// that Close shuts before the fd is closed. Stopping the stack does not stop
// its writers: a route outlives RemoveNIC, and gVisor's fdbased link writes
// straight to the fd. A flow goroutine that sends late (for example the RST
// of a dial that Close cancelled) would otherwise write after Close
// returned, into whichever file has taken the fd's number by then.
type writeGate struct {
	stack.LinkEndpoint
	mu   sync.RWMutex // held for reading across each write, so shut waits them out
	shut bool
}

// WritePackets writes through to the TUN until the gate is shut, then drops
// the packets as a closed link does.
func (g *writeGate) WritePackets(pkts stack.PacketBufferList) (int, tcpip.Error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.shut {
		return 0, &tcpip.ErrClosedForSend{}
	}
	return g.LinkEndpoint.WritePackets(pkts) // non-blocking (fdbased)
}

// close shuts the gate. It returns once no write is in flight, and no later
// write reaches the fd.
func (g *writeGate) close() {
	g.mu.Lock()
	g.shut = true
	g.mu.Unlock()
}
