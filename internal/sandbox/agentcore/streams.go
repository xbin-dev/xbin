//go:build linux

package agentcore

import (
	"io"
	"net"
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
