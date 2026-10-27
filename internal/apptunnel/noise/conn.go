package noise

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Transport frame types (spec §4): the plaintext is type(1) ‖ body.
const (
	TypeData  byte = 0x00
	TypeRekey byte = 0x01
	TypeClose byte = 0x02
)

// MaxFrame is the most a frame's ciphertext may be (the tag included).
const MaxFrame = 65535

// tagLen is ChaChaPoly's tag.
const tagLen = 16

// MaxBody is the most data one frame carries.
const MaxBody = MaxFrame - tagLen - 1

// Rekey policy (spec §4): a sender rekeys after this many messages, this
// many plaintext bytes, or this long since its last (re)key.
const (
	RekeyMessages = 1 << 20
	RekeyBytes    = 1 << 30
	RekeyAge      = 10 * time.Minute
)

// closeGrace bounds the close frame Close sends to a peer that isn't reading.
const closeGrace = time.Second

var (
	// ErrFrame is a frame that failed to decrypt or isn't one the spec has.
	ErrFrame = errors.New("noise: bad transport frame")
	// ErrWriteClosed is a write after this side sent its close.
	ErrWriteClosed = errors.New("noise: write after close")
)

// Conn is a net.Conn over a completed handshake: each Write goes out as data
// frames, a sender rekeys in-band, CloseWrite sends the authenticated close
// (a half-close), and Read gives io.EOF only after the peer's close — an
// underlying stream that just ends is truncation, io.ErrUnexpectedEOF.
// Deadlines and addresses are the underlying conn's.
type Conn struct {
	c net.Conn
	s *Session

	rmu   sync.Mutex
	rbuf  []byte // decrypted data not yet read
	rerr  error  // sticky
	frame []byte // len16 ‖ ciphertext of the frame being read
	fill  int    // bytes of it read so far

	wmu      sync.Mutex
	wclosed  bool
	werr     error
	sentMsgs uint64
	sentB    uint64
	keyed    time.Time

	// the rekey policy and clock; tests shrink them
	rekeyMsgs  uint64
	rekeyBytes uint64
	rekeyAge   time.Duration
	now        func() time.Time
}

// NewConn wraps c, over which s was negotiated.
func NewConn(c net.Conn, s *Session) *Conn {
	return &Conn{c: c, s: s, frame: make([]byte, 2+MaxFrame), keyed: time.Now(),
		rekeyMsgs: RekeyMessages, rekeyBytes: RekeyBytes, rekeyAge: RekeyAge, now: time.Now}
}

// Session is the handshake this conn runs on (its hash, SAS, peer key).
func (c *Conn) Session() *Session { return c.s }

// Underlying is the conn the frames travel on.
func (c *Conn) Underlying() net.Conn { return c.c }

// Read returns decrypted data. io.EOF means the peer closed its direction
// with an authenticated close; io.ErrUnexpectedEOF means the stream ended
// without one. A deadline leaves the stream usable: a frame read halfway
// resumes on the next Read (http.Server aborts its background read that way).
func (c *Conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for len(c.rbuf) == 0 {
		if c.rerr != nil {
			return 0, c.rerr
		}
		if err := c.readFrameLocked(); err != nil {
			if isTimeout(err) {
				return 0, err
			}
			c.rerr = err
			if err != io.EOF {
				_ = c.c.Close()
			}
			return 0, err
		}
	}
	n := copy(p, c.rbuf)
	c.rbuf = c.rbuf[n:]
	return n, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// fillLocked reads until c.frame[:c.fill] holds one whole frame.
func (c *Conn) fillLocked() error {
	for {
		want := 2
		if c.fill >= 2 {
			want = 2 + int(binary.BigEndian.Uint16(c.frame[:2]))
			if c.fill == want {
				return nil
			}
		}
		n, err := c.c.Read(c.frame[c.fill:want])
		c.fill += n
		if err != nil {
			if c.fill >= 2 && c.fill == 2+int(binary.BigEndian.Uint16(c.frame[:2])) {
				return nil // whole; the error surfaces on the next read
			}
			return err
		}
	}
}

// readFrameLocked reads and handles one frame.
func (c *Conn) readFrameLocked() error {
	if err := c.fillLocked(); err != nil {
		return truncated(err)
	}
	ct := c.frame[2:c.fill]
	c.fill = 0
	if len(ct) < tagLen+1 {
		return fmt.Errorf("%w: %d bytes", ErrFrame, len(ct))
	}
	pt, err := c.s.recv.Decrypt(ct[:0], nil, ct)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFrame, err)
	}
	switch typ, body := pt[0], pt[1:]; typ {
	case TypeData:
		c.rbuf = body
	case TypeRekey:
		if len(body) != 0 {
			return fmt.Errorf("%w: rekey with a body", ErrFrame)
		}
		c.s.recv.Rekey()
	case TypeClose:
		if len(body) != 0 {
			return fmt.Errorf("%w: close with a body", ErrFrame)
		}
		return io.EOF
	default:
		return fmt.Errorf("%w: type %#x", ErrFrame, typ)
	}
	return nil
}

func truncated(err error) error {
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// Write sends p as data frames.
func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.wclosed {
		return 0, ErrWriteClosed
	}
	if c.werr != nil {
		return 0, c.werr
	}
	n := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > MaxBody {
			chunk = chunk[:MaxBody]
		}
		if err := c.sendLocked(TypeData, chunk); err != nil {
			c.werr = err
			return n, err
		}
		n += len(chunk)
		p = p[len(chunk):]
	}
	return n, nil
}

// sendLocked seals and writes one frame, rekeying first when the policy
// says so.
func (c *Conn) sendLocked(typ byte, body []byte) error {
	if typ == TypeData && (c.sentMsgs >= c.rekeyMsgs || c.sentB >= c.rekeyBytes || c.now().Sub(c.keyed) >= c.rekeyAge) {
		if err := c.rekeyLocked(); err != nil {
			return err
		}
	}
	if err := c.sealLocked(typ, body); err != nil {
		return err
	}
	c.sentMsgs++
	c.sentB += uint64(len(body))
	return nil
}

func (c *Conn) sealLocked(typ byte, body []byte) error {
	pt := make([]byte, 1+len(body))
	pt[0] = typ
	copy(pt[1:], body)
	out := make([]byte, 2, 2+len(pt)+tagLen)
	out, err := c.s.send.Encrypt(out, nil, pt)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint16(out, uint16(len(out)-2))
	_, err = c.c.Write(out)
	return err
}

// rekeyLocked sends a rekey frame, then rekeys the sending CipherState.
func (c *Conn) rekeyLocked() error {
	if err := c.sealLocked(TypeRekey, nil); err != nil {
		return err
	}
	c.s.send.Rekey()
	c.sentMsgs, c.sentB, c.keyed = 0, 0, c.now()
	return nil
}

// Rekey sends a rekey now (the policy does it on its own; tests and vectors
// call it).
func (c *Conn) Rekey() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.wclosed {
		return ErrWriteClosed
	}
	return c.rekeyLocked()
}

// CloseWrite sends the authenticated close — the end of this side's
// direction — and half-closes the underlying stream when it can.
func (c *Conn) CloseWrite() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.closeWriteLocked()
}

func (c *Conn) closeWriteLocked() error {
	if c.wclosed {
		return nil
	}
	c.wclosed = true
	if c.werr != nil {
		return c.werr
	}
	if err := c.sealLocked(TypeClose, nil); err != nil {
		return err
	}
	if cw, ok := c.c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	return nil
}

// Close sends the close frame (if CloseWrite didn't), within a short grace,
// and closes the underlying conn.
func (c *Conn) Close() error {
	_ = c.c.SetWriteDeadline(time.Now().Add(closeGrace))
	c.wmu.Lock()
	_ = c.closeWriteLocked()
	c.wmu.Unlock()
	return c.c.Close()
}

func (c *Conn) LocalAddr() net.Addr                { return c.c.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr               { return c.c.RemoteAddr() }
func (c *Conn) SetDeadline(t time.Time) error      { return c.c.SetDeadline(t) }
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.c.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.c.SetWriteDeadline(t) }
