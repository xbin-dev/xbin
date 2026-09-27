//go:build linux

package relay

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// hostLocal is HostDeny's locality test on Linux: one route lookup per flow,
// on a netlink socket opened now — so in the caller's network namespace,
// which the socket keeps — and closed when the Deny is dropped.
func hostLocal() func(netip.Addr) bool {
	l := &routeLookup{sock: &nlSock{fd: -1}, buf: make([]byte, 8192)}
	l.mu.Lock()
	_ = l.sock.open() // a failure is retried by the next lookup (and denies until then)
	l.mu.Unlock()
	runtime.AddCleanup(l, func(s *nlSock) { s.close() }, l.sock)
	return l.local
}

// routeLookupTimeout bounds one lookup; one that times out denies the flow.
const routeLookupTimeout = time.Second

// routeLookup asks the kernel, per destination, how it would route a flow
// there (RTM_GETROUTE) — the lookup a dial makes, so there is no window
// between an address appearing on the host and its first flow being denied.
type routeLookup struct {
	mu   sync.Mutex // one request in flight per socket
	sock *nlSock
	seq  uint32
	buf  []byte
}

// nlSock is the lookup's netlink socket (-1 = not open).
type nlSock struct{ fd int }

func (s *nlSock) open() error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	tv := unix.NsecToTimeval(routeLookupTimeout.Nanoseconds())
	if err := errors.Join(
		unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv),
		unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv),
		unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}),
	); err != nil {
		unix.Close(fd)
		return err
	}
	s.fd = fd
	return nil
}

func (s *nlSock) close() {
	if s.fd >= 0 {
		unix.Close(s.fd)
		s.fd = -1
	}
}

// local reports whether the host delivers ip to itself: its route is local,
// broadcast, multicast or anycast. No route (ENETUNREACH, EHOSTUNREACH) is
// not local — the flow goes on to the policy, and its dial would fail
// anyway; any other failure counts as local, so the flow is denied.
func (l *routeLookup) local(ip netip.Addr) bool {
	typ, err := l.routeType(norm(ip))
	if err != nil {
		return !errors.Is(err, unix.ENETUNREACH) && !errors.Is(err, unix.EHOSTUNREACH)
	}
	switch typ {
	case unix.RTN_LOCAL, unix.RTN_BROADCAST, unix.RTN_MULTICAST, unix.RTN_ANYCAST:
		return true
	}
	return false
}

// routeType is the type (unix.RTN_*) of the route the kernel picks for ip,
// or the lookup's error.
func (l *routeLookup) routeType(ip netip.Addr) (uint8, error) {
	if !ip.IsValid() {
		return 0, errors.New("relay: invalid address")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sock.fd < 0 {
		if err := l.sock.open(); err != nil {
			return 0, err
		}
	}
	l.seq++
	seq := l.seq
	if err := unix.Sendto(l.sock.fd, routeRequest(ip, seq), 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		l.sock.close() // start the next lookup on a clean socket
		return 0, err
	}
	for {
		n, _, err := unix.Recvfrom(l.sock.fd, l.buf, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			l.sock.close() // a late answer must not meet the next request
			return 0, err
		}
		msgs, err := syscall.ParseNetlinkMessage(l.buf[:n])
		if err != nil {
			l.sock.close()
			return 0, err
		}
		for _, m := range msgs {
			if m.Header.Seq != seq {
				continue // an earlier request's answer
			}
			switch m.Header.Type {
			case unix.NLMSG_ERROR:
				if len(m.Data) < 4 {
					return 0, errors.New("relay: short netlink error")
				}
				if errno := -int32(binary.NativeEndian.Uint32(m.Data)); errno != 0 {
					return 0, unix.Errno(errno)
				}
				return 0, errors.New("relay: route lookup acknowledged with no route")
			case unix.RTM_NEWROUTE:
				if len(m.Data) < unix.SizeofRtMsg {
					return 0, errors.New("relay: short route answer")
				}
				return m.Data[7], nil // struct rtmsg's rtm_type
			}
		}
	}
}

// routeRequest is an RTM_GETROUTE request for ip: a netlink header, a
// struct rtmsg and one RTA_DST attribute.
func routeRequest(ip netip.Addr, seq uint32) []byte {
	a := ip.AsSlice()
	family := unix.AF_INET
	if ip.Is6() {
		family = unix.AF_INET6
	}
	attrLen := unix.SizeofRtAttr + len(a) // 8 or 20: already aligned
	b := make([]byte, unix.SizeofNlMsghdr+unix.SizeofRtMsg+attrLen)
	ne := binary.NativeEndian
	ne.PutUint32(b[0:], uint32(len(b)))
	ne.PutUint16(b[4:], unix.RTM_GETROUTE)
	ne.PutUint16(b[6:], unix.NLM_F_REQUEST)
	ne.PutUint32(b[8:], seq)
	m := b[unix.SizeofNlMsghdr:]
	m[0] = byte(family)
	m[1] = byte(len(a) * 8) // rtm_dst_len
	at := m[unix.SizeofRtMsg:]
	ne.PutUint16(at[0:], uint16(attrLen))
	ne.PutUint16(at[2:], unix.RTA_DST)
	copy(at[unix.SizeofRtAttr:], a)
	return b
}
