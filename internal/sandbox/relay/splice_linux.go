//go:build linux

package relay

import "golang.org/x/sys/unix"

// Splicer bidirectionally pumps raw IP packets between two TUN fds held by
// xbind — the L3 backplane that links a spliced client's egress TUN to a
// provider tile's per-client TUN (plans/interfaces.md). Both fds are packet-
// framed (IFF_NO_PI), so each read/write is exactly one IP packet.
type Splicer struct{ a, b int }

// Splice starts pumping between a and b and returns a handle; Close stops it and
// closes both fds.
func Splice(a, b int) *Splicer {
	s := &Splicer{a: a, b: b}
	go pump(a, b)
	go pump(b, a)
	return s
}

func pump(from, to int) {
	buf := make([]byte, 65536)
	for {
		n, err := unix.Read(from, buf)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return // fd closed / error → this direction ends
		}
		if n <= 0 {
			return
		}
		if _, err := unix.Write(to, buf[:n]); err != nil && err != unix.EINTR {
			return
		}
	}
}

// Close stops the splice by closing the client-side fd (a). The provider-side fd
// (b) is left open — xbind's netMux owns it and reuses it if the client
// restarts and re-splices; it's closed when the provider itself tears down.
func (s *Splicer) Close() {
	if s == nil {
		return
	}
	unix.Close(s.a)
}
