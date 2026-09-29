//go:build linux

package agentcore

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// streamConn pairs a stream connection with the reader that consumed its
// Hello (so bytes buffered past the line aren't lost).
type streamConn struct {
	c io.ReadWriteCloser
	r io.Reader
}

func (s streamConn) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s streamConn) Write(p []byte) (int, error) { return s.c.Write(p) }
func (s streamConn) Close() error                { return s.c.Close() }

// CloseWrite half-closes the stream when the transport can (a unix socket).
func (s streamConn) CloseWrite() error {
	if cw, ok := s.c.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// Splice copies both ways until either side ends, then closes both.
func Splice(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	cp := func(dst, src io.ReadWriteCloser) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}

// awaitListen polls until path accepts connections (a backend is up), then
// reports "listening" — the host exposes its side only then, so xbind's
// health check means what it says. Gives up when stop closes.
func (c *Core) awaitListen(session int, path string, stop <-chan struct{}) {
	for {
		if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
			conn.Close()
			c.send(proto.Msg{Op: "listening", Session: session})
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// bridgeListen connects a "listen" connection to its session's socket.
func (c *Core) bridgeListen(h proto.Hello, conn io.ReadWriteCloser) {
	s := c.session(h.Session)
	var path string
	if s != nil {
		s.mu.Lock()
		if s.execReceived {
			path = s.ex.Listen
		}
		s.mu.Unlock()
	}
	if path == "" {
		conn.Close()
		return
	}
	g, err := net.Dial("unix", path)
	if err != nil {
		conn.Close()
		return
	}
	Splice(g, conn)
}

// PortDialTimeout bounds a "port" connection's dial of the sandbox's
// loopback (each address tried).
var PortDialTimeout = 5 * time.Second

// bridgePort connects a "port" connection (D135: xbind's inbound path to a
// server in the sandbox, the ports capability) to TCP port h.Port on the
// sandbox's own loopback — 127.0.0.1, else ::1 (a server that bound
// "localhost" may hold only the one) — and answers one PortReply line
// first. It dials nothing but the loopback: the port is all the peer
// chooses.
func (c *Core) bridgePort(h proto.Hello, conn io.ReadWriteCloser) {
	reply := func(r proto.PortReply) error {
		b, _ := json.Marshal(r)
		_, err := conn.Write(append(b, '\n'))
		return err
	}
	if h.Port < 1 || h.Port > 65535 {
		_ = reply(proto.PortReply{Error: "port " + strconv.Itoa(h.Port) + " is out of range (1-65535)"})
		conn.Close()
		return
	}
	g, err := DialLoopback(h.Port, PortDialTimeout)
	if err != nil {
		_ = reply(proto.PortReply{Refused: errors.Is(err, syscall.ECONNREFUSED), Error: err.Error()})
		conn.Close()
		return
	}
	if err := reply(proto.PortReply{OK: true}); err != nil {
		g.Close()
		conn.Close()
		return
	}
	Splice(g, conn)
}

// DialLoopback dials port on 127.0.0.1, then on ::1 when that fails; the
// error is the first address's unless only the second one listens.
func DialLoopback(port int, timeout time.Duration) (net.Conn, error) {
	p := strconv.Itoa(port)
	g, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", p), timeout)
	if err == nil {
		return g, nil
	}
	if g6, err6 := net.DialTimeout("tcp", net.JoinHostPort("::1", p), timeout); err6 == nil {
		return g6, nil
	}
	return nil, err
}
