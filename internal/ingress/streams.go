package ingress

import (
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"
)

// The L4 plane (ING-4): each bound stream expose becomes a host listener
// whose connections xbind relays into the tile's netns — the inbound twin of
// the egress relay. Userspace only: no host firewall/routing is touched.

const (
	dialTimeout    = 15 * time.Second
	udpIdleTimeout = 30 * time.Second // mirror the egress relay's conntrack-style expiry
	udpMaxSessions = 4096             // per listener; new peers beyond this are dropped
)

// StreamSpec is one desired host listener, computed by the broker from a
// bound stream expose.
type StreamSpec struct {
	Component string `json:"component"`
	Slot      string `json:"slot"`
	Proto     string `json:"proto"`  // tcp | udp
	Listen    string `json:"listen"` // host listen address (":2456")
	Port      int    `json:"port"`   // in-netns port the backend listens on
}

func (s StreamSpec) key() string {
	return s.Component + "\x00" + s.Slot + "\x00" + s.Proto + "\x00" + s.Listen + "\x00" + fmt.Sprint(s.Port)
}

// StreamStatus is one listener's live state for the admin UI / bx ingress.
type StreamStatus struct {
	StreamSpec
	Error  string `json:"error,omitempty"` // listen failure (port taken, privileged port, …)
	Active int    `json:"active"`          // open TCP conns / live UDP sessions
}

// Streams reconciles the set of host listeners against the bound stream
// exposes. Dial is the netns-reach primitive (runner.DialInto).
type Streams struct {
	Dial func(ctx context.Context, component, proto string, port int) (net.Conn, error)

	mu sync.Mutex
	ls map[string]*streamListener
}

type streamListener struct {
	spec  StreamSpec
	err   string
	close func()

	mu     sync.Mutex
	active int
	conns  map[io.Closer]struct{}
}

func (l *streamListener) track(c io.Closer) func() {
	l.mu.Lock()
	if l.conns == nil {
		l.conns = map[io.Closer]struct{}{}
	}
	l.conns[c] = struct{}{}
	l.active++
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.conns, c)
			l.active--
			l.mu.Unlock()
		})
	}
}

// closeAll shuts the listener and severs every live flow — unbinding an
// expose cuts traffic now, not when clients feel like hanging up.
func (l *streamListener) closeAll() {
	if l.close != nil {
		l.close()
	}
	l.mu.Lock()
	conns := make([]io.Closer, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// Reconcile makes the live listener set match specs: removed ones are closed
// (flows severed), new ones opened, failed ones retried. Idempotent and
// cheap; called on boot, binding changes, and workspace rescans.
func (s *Streams) Reconcile(specs []StreamSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ls == nil {
		s.ls = map[string]*streamListener{}
	}
	want := map[string]StreamSpec{}
	for _, sp := range specs {
		want[sp.key()] = sp
	}
	for k, l := range s.ls {
		if _, ok := want[k]; !ok {
			l.closeAll()
			delete(s.ls, k)
		}
	}
	for k, sp := range want {
		if l, ok := s.ls[k]; ok && l.err == "" {
			continue // already serving
		} else if ok {
			l.closeAll() // failed before — retry below
		}
		s.ls[k] = s.open(sp)
	}
}

// Close tears down every listener (daemon shutdown).
func (s *Streams) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, l := range s.ls {
		l.closeAll()
		delete(s.ls, k)
	}
}

// Status snapshots the listener set, stably ordered.
func (s *Streams) Status() []StreamStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]StreamStatus, 0, len(s.ls))
	for _, l := range s.ls {
		l.mu.Lock()
		out = append(out, StreamStatus{StreamSpec: l.spec, Error: l.err, Active: l.active})
		l.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

func (s *Streams) open(sp StreamSpec) *streamListener {
	l := &streamListener{spec: sp}
	switch sp.Proto {
	case "udp":
		pc, err := net.ListenPacket("udp", sp.Listen)
		if err != nil {
			l.err = err.Error()
			return l
		}
		l.close = func() { _ = pc.Close() }
		go s.serveUDP(pc, sp, l)
	default:
		ln, err := net.Listen("tcp", sp.Listen)
		if err != nil {
			l.err = err.Error()
			return l
		}
		l.close = func() { _ = ln.Close() }
		go s.serveTCP(ln, sp, l)
	}
	return l
}

func (s *Streams) serveTCP(ln net.Listener, sp StreamSpec, l *streamListener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go func(conn net.Conn) {
			release := l.track(conn)
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			in, err := s.Dial(ctx, sp.Component, "tcp", sp.Port)
			cancel()
			if err != nil {
				_ = conn.Close()
				return
			}
			splice(conn, in)
		}(conn)
	}
}

// serveUDP is a sessioned packet relay: each remote peer gets a dialed
// in-netns flow; datagrams are pumped both ways until the session idles out.
func (s *Streams) serveUDP(pc net.PacketConn, sp StreamSpec, l *streamListener) {
	var mu sync.Mutex
	sessions := map[string]*udpSession{}
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			mu.Lock()
			for _, sess := range sessions {
				_ = sess.out.Close()
			}
			mu.Unlock()
			return // listener closed
		}
		key := addr.String()
		mu.Lock()
		sess := sessions[key]
		if sess == nil {
			if len(sessions) >= udpMaxSessions {
				mu.Unlock()
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			out, err := s.Dial(ctx, sp.Component, "udp", sp.Port)
			cancel()
			if err != nil {
				mu.Unlock()
				continue
			}
			sess = &udpSession{out: out}
			sessions[key] = sess
			release := l.track(out)
			// Return path: in-netns datagrams back to this peer, until idle.
			go func() {
				defer release()
				rbuf := make([]byte, 64*1024)
				for {
					_ = out.SetReadDeadline(time.Now().Add(udpIdleTimeout))
					k, err := out.Read(rbuf)
					if k > 0 {
						_, _ = pc.WriteTo(rbuf[:k], addr)
					}
					if err != nil { // idle or closed → end the session
						mu.Lock()
						if sessions[key] == sess {
							delete(sessions, key)
						}
						mu.Unlock()
						_ = out.Close()
						return
					}
				}
			}()
		}
		out := sess.out
		mu.Unlock()
		_, _ = out.Write(buf[:n])
		_ = out.SetReadDeadline(time.Now().Add(udpIdleTimeout)) // inbound traffic keeps it alive
	}
}

type udpSession struct{ out net.Conn }

// splice copies bidirectionally and closes both ends when either direction
// finishes (same shape as the egress relay's).
func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.SetReadDeadline(time.Now().Add(2 * time.Second))
		}
	}
	go cp(b, a)
	go cp(a, b)
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}
