//go:build linux

package host

import (
	"context"
	"io"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// ptyInputWait bounds how long a read of the terminal's input waits for the
// guest to answer a resize before it goes on anyway (logged).
const ptyInputWait = 10 * time.Second

// ptyInput carries a terminal session's input (the host PTY, the shim's
// stdin) to the guest's PTY stream, each read after the guest has the
// terminal's size as it was when the read returned. xbind resizes the host
// PTY (TIOCSWINSZ) before it writes the keys that follow, but the shim
// hears of the resize by SIGWINCH, delivered beside the read: forwarded as
// it came, the keys could reach the guest's PTY before the resize did (the
// resize rides the control connection, the keys their stream, and the agent
// reads each on its own goroutine). So every read first compares the host
// PTY's size with the one last sent and, when it changed, sends it — the
// SIGWINCH handler sends it too, for a resize no key follows — and waits
// for the guest's answer (proto.Resizes) before passing the bytes on. A
// guest from before ResizeAcks is sent its resizes as before, unnumbered,
// and nothing waits.
type ptyInput struct {
	size    func() (unix.Winsize, bool) // the host PTY's size now
	send    func(proto.Msg) error       // one control line to the guest
	resizes *proto.Resizes
	end     <-chan struct{} // closed when the session is over: nothing waits past it
	logf    func(string, ...any)

	mu   sync.Mutex // one resize decision and send at a time, in size order
	last unix.Winsize
}

// sync sends the host PTY's size when it differs from the one last sent.
func (p *ptyInput) sync() {
	ws, ok := p.size()
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ws.Row == p.last.Row && ws.Col == p.last.Col {
		return
	}
	p.last = ws
	seq := p.resizes.Begin()
	if err := p.send(proto.Msg{Op: "resize", Session: 1, Rows: ws.Row, Cols: ws.Col, Seq: seq}); err != nil && seq > 0 {
		p.resizes.End(seq) // it may never have gone: nothing waits for its answer
	}
}

// copy forwards src to dst until either ends, each read after the guest has
// the size the terminal had when it returned.
func (p *ptyInput) copy(dst io.Writer, src io.Reader) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			p.sync()
			if expired, _ := p.resizes.Await(context.Background(), p.end, ptyInputWait); expired && p.logf != nil {
				p.logf("the guest didn't answer a resize within %s; the input goes on", ptyInputWait)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
