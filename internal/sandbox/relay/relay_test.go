//go:build linux

package relay

import (
	"net"
	"testing"
	"time"
)

// A UDP flow has no close handshake, so spliceUDPIdle must end it once idle —
// otherwise the flow lingers as "open" forever and never stops counting toward
// Active (the bug behind DNS flows showing open indefinitely).
func TestSpliceUDPIdleReapsSilentFlow(t *testing.T) {
	srv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Skip("udp listen:", err)
	}
	defer srv.Close()
	cli, err := net.DialUDP("udp", nil, srv.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Skip("udp dial:", err)
	}
	defer cli.Close()

	done := make(chan struct{})
	go func() { spliceUDPIdle(cli, srv, 40*time.Millisecond); close(done) }()
	select {
	case <-done: // reaped on idle, as it should be
	case <-time.After(3 * time.Second):
		t.Fatal("spliceUDPIdle hung on an idle flow — UDP would show 'open' forever")
	}
}
