// Package ws is a WebSocket (RFC 6455) client and server on the standard
// library alone, so the SDK stays dependency-free.
//
// A backend upgrades a request with [Upgrade] and dials with [Dial]. Dial
// sends its opening handshake through an [net/http.Client], so the SDK's
// xbin.Client() reaches another tile (or xbind) through the gateway socket
// with this instance's credential. Any other client reaches a plain ws:// or
// wss:// server:
//
//	c, _, err := ws.Dial(ctx, "ws://xbin/api/apps/other/stream", nil, &ws.DialOptions{Client: xbin.Client()})
//	if err != nil { … }
//	defer c.Close()
//	typ, msg, err := c.ReadMessage()
//
// A [Conn] carries whole messages, text and binary; a fragmented message
// arrives reassembled. Pings are answered while a goroutine reads, so keep
// one reading for as long as the connection lives. Writes may come from any
// number of goroutines; reads from one at a time.
package ws

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Message types: the RFC's opcodes.
const (
	TextMessage   = 1
	BinaryMessage = 2
	CloseMessage  = 8
	PingMessage   = 9
	PongMessage   = 10
)

const opContinuation = 0

// Close codes (RFC 6455 §7.4.1).
const (
	CloseNormalClosure     = 1000
	CloseGoingAway         = 1001
	CloseProtocolError     = 1002
	CloseUnsupportedData   = 1003
	CloseNoStatusReceived  = 1005 // no code in the peer's close frame (never sent)
	CloseAbnormalClosure   = 1006 // the connection dropped without a close frame (never sent)
	CloseInvalidPayload    = 1007
	ClosePolicyViolation   = 1008
	CloseMessageTooBig     = 1009
	CloseMandatoryExt      = 1010
	CloseInternalServerErr = 1011
)

// DefaultMaxMessageSize bounds a received message when the options set no
// MaxMessageSize.
const DefaultMaxMessageSize = 32 << 20

// closeWait is how long Close waits for the peer's close frame.
const closeWait = 2 * time.Second

var (
	// ErrBadHandshake: the server didn't switch to WebSocket (Dial returns
	// its response, with up to 4 KiB of the body, alongside).
	ErrBadHandshake = errors.New("ws: bad handshake")
	// ErrMessageTooBig: a message over the connection's MaxMessageSize
	// arrived; the connection was closed with 1009.
	ErrMessageTooBig = errors.New("ws: message too big")
	// ErrClosed: a write after this side's close frame went out.
	ErrClosed = errors.New("ws: connection closed")
)

// CloseError is how a connection ended by a close frame reads: the peer's
// code and reason. A close frame without a code reads as
// CloseNoStatusReceived.
type CloseError struct {
	Code int
	Text string
}

func (e *CloseError) Error() string {
	if e.Text == "" {
		return fmt.Sprintf("ws: closed (%d)", e.Code)
	}
	return fmt.Sprintf("ws: closed (%d): %s", e.Code, e.Text)
}

// IsClose reports whether err is a CloseError with one of codes (any code
// when none are given).
func IsClose(err error, codes ...int) bool {
	var ce *CloseError
	if !errors.As(err, &ce) {
		return false
	}
	return len(codes) == 0 || slices.Contains(codes, ce.Code)
}

// protocolError is a peer breaking the RFC: the connection closes with code.
type protocolError struct {
	code int
	msg  string
}

func (e *protocolError) Error() string { return "ws: protocol error: " + e.msg }

// Conn is one WebSocket connection.
type Conn struct {
	rwc    io.ReadWriteCloser // the stream; reads go through br
	nc     net.Conn           // its deadlines; nil when the stream isn't a net.Conn (timers stand in)
	br     *bufio.Reader
	server bool // a server reads masked frames and writes plain ones; a client the reverse
	max    int64
	proto  string

	rmu     sync.Mutex // one reader at a time: ReadMessage, or Close waiting for the peer
	readErr error      // sticky (rmu)

	wmu     sync.Mutex // one frame on the wire at a time
	wclosed bool       // our close frame went out (wmu)
	werr    error      // sticky (wmu): a failed write leaves a frame half sent

	peerClosed chan struct{} // closed when the peer's close frame arrives
	peerOnce   sync.Once
	closeOnce  sync.Once

	hmu    sync.Mutex
	onPing func([]byte)
	onPong func([]byte)

	timers *timerDeadlines // nc == nil only
}

func newConn(rwc io.ReadWriteCloser, nc net.Conn, br *bufio.Reader, server bool, max int64, proto string) *Conn {
	if br == nil {
		br = bufio.NewReaderSize(rwc, 4096)
	}
	switch {
	case max == 0:
		max = DefaultMaxMessageSize
	case max < 0:
		max = 0 // no limit
	}
	c := &Conn{rwc: rwc, nc: nc, br: br, server: server, max: max, proto: proto, peerClosed: make(chan struct{})}
	if nc == nil {
		c.timers = &timerDeadlines{}
	}
	return c
}

// Subprotocol is the subprotocol the handshake agreed on ("" for none).
func (c *Conn) Subprotocol() string { return c.proto }

// SetPingHandler sets a function called with each ping's payload (from the
// reading goroutine). The pong answers it either way.
func (c *Conn) SetPingHandler(h func(appData []byte)) {
	c.hmu.Lock()
	c.onPing = h
	c.hmu.Unlock()
}

// SetPongHandler sets a function called with each pong's payload (from the
// reading goroutine) — a keepalive's proof of life.
func (c *Conn) SetPongHandler(h func(appData []byte)) {
	c.hmu.Lock()
	c.onPong = h
	c.hmu.Unlock()
}

// SetReadDeadline bounds reads: past t, a blocked or later ReadMessage fails
// with an error that is os.ErrDeadlineExceeded, and the connection is
// unusable for reading. The zero time clears it. On a connection whose
// stream isn't a net.Conn (a handshake through a custom RoundTripper) the
// deadline closes the connection.
func (c *Conn) SetReadDeadline(t time.Time) error {
	if c.nc != nil {
		return c.nc.SetReadDeadline(t)
	}
	c.timers.set(&c.timers.r, t, c.expire)
	return nil
}

// SetWriteDeadline bounds writes the way SetReadDeadline bounds reads; a
// write that timed out leaves the connection unusable for writing.
func (c *Conn) SetWriteDeadline(t time.Time) error {
	if c.nc != nil {
		return c.nc.SetWriteDeadline(t)
	}
	c.timers.set(&c.timers.w, t, c.expire)
	return nil
}

// SetDeadline sets both deadlines.
func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *Conn) expire() {
	c.timers.expired.Store(true)
	_ = c.rwc.Close()
}

// ioErr maps an error of a stream a timer closed to the deadline it was.
func (c *Conn) ioErr(err error) error {
	if err != nil && c.timers != nil && c.timers.expired.Load() {
		return fmt.Errorf("ws: %w (%v)", os.ErrDeadlineExceeded, err)
	}
	return err
}

// timerDeadlines stand in for a net.Conn's deadlines: a timer that closes
// the stream.
type timerDeadlines struct {
	mu      sync.Mutex
	r, w    *time.Timer
	expired atomic.Bool
}

func (d *timerDeadlines) set(which **time.Timer, t time.Time, expire func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if *which != nil {
		(*which).Stop()
		*which = nil
	}
	if !t.IsZero() {
		*which = time.AfterFunc(time.Until(t), expire)
	}
}

func (d *timerDeadlines) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range []*time.Timer{d.r, d.w} {
		if t != nil {
			t.Stop()
		}
	}
}

// --- reading ------------------------------------------------------------------

// ReadMessage returns the next message, TextMessage or BinaryMessage, whole.
// It answers pings and hands pongs to the pong handler on the way. A close
// from the peer is answered and returned as a *CloseError; once a read has
// failed, every later one returns the same error.
func (c *Conn) ReadMessage() (messageType int, p []byte, err error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	return c.readMessage()
}

func (c *Conn) readMessage() (int, []byte, error) {
	if c.readErr != nil {
		return 0, nil, c.readErr
	}
	typ, msg, err := c.readFrames()
	if err != nil {
		var pe *protocolError
		if errors.As(err, &pe) {
			c.abort(pe.code, pe.msg)
		} else if errors.Is(err, ErrMessageTooBig) {
			c.abort(CloseMessageTooBig, "message too big")
		}
		c.readErr = c.ioErr(err)
		return 0, nil, c.readErr
	}
	return typ, msg, nil
}

// abort closes after the peer broke the protocol: our close frame (best
// effort), then the connection.
func (c *Conn) abort(code int, reason string) {
	_ = c.writeClose(code, reason)
	c.closeConn()
}

type frameHeader struct {
	fin    bool
	op     byte
	masked bool
	key    [4]byte
	length int64
}

func (c *Conn) readFrames() (int, []byte, error) {
	typ := 0
	var msg []byte
	for {
		h, err := c.readHeader()
		if err != nil {
			return 0, nil, err
		}
		if h.op >= CloseMessage { // a control frame, possibly between a message's fragments
			payload, err := c.readPayload(h, nil)
			if err != nil {
				return 0, nil, err
			}
			switch h.op {
			case PingMessage:
				c.hmu.Lock()
				f := c.onPing
				c.hmu.Unlock()
				if f != nil {
					f(payload)
				}
				if err := c.writeFrame(true, PongMessage, payload); err != nil && !errors.Is(err, ErrClosed) {
					return 0, nil, err
				}
			case PongMessage:
				c.hmu.Lock()
				f := c.onPong
				c.hmu.Unlock()
				if f != nil {
					f(payload)
				}
			case CloseMessage:
				return 0, nil, c.peerClose(payload)
			}
			continue
		}
		switch {
		case h.op == opContinuation && typ == 0:
			return 0, nil, &protocolError{CloseProtocolError, "a continuation frame with no message to continue"}
		case h.op != opContinuation && typ != 0:
			return 0, nil, &protocolError{CloseProtocolError, "a new message inside a fragmented one"}
		case h.op != opContinuation:
			typ = int(h.op)
		}
		if c.max > 0 && int64(len(msg))+h.length > c.max {
			return 0, nil, ErrMessageTooBig
		}
		if msg, err = c.readPayload(h, msg); err != nil {
			return 0, nil, err
		}
		if h.fin {
			if typ == TextMessage && !utf8.Valid(msg) {
				return 0, nil, &protocolError{CloseInvalidPayload, "a text message that isn't UTF-8"}
			}
			if msg == nil {
				msg = []byte{}
			}
			return typ, msg, nil
		}
	}
}

func (c *Conn) readHeader() (frameHeader, error) {
	var h frameHeader
	var b [8]byte
	if _, err := io.ReadFull(c.br, b[:2]); err != nil {
		return h, err
	}
	h.fin = b[0]&0x80 != 0
	h.op = b[0] & 0x0f
	h.masked = b[1]&0x80 != 0
	n := int64(b[1] & 0x7f)
	switch {
	case b[0]&0x70 != 0:
		return h, &protocolError{CloseProtocolError, "reserved bits set (no extension was agreed)"}
	case (h.op > BinaryMessage && h.op < CloseMessage) || h.op > PongMessage:
		return h, &protocolError{CloseProtocolError, fmt.Sprintf("unknown opcode %d", h.op)}
	case h.masked != c.server:
		if c.server {
			return h, &protocolError{CloseProtocolError, "an unmasked frame from a client"}
		}
		return h, &protocolError{CloseProtocolError, "a masked frame from a server"}
	}
	switch n {
	case 126:
		if _, err := io.ReadFull(c.br, b[:2]); err != nil {
			return h, unexpected(err)
		}
		n = int64(binary.BigEndian.Uint16(b[:2]))
	case 127:
		if _, err := io.ReadFull(c.br, b[:8]); err != nil {
			return h, unexpected(err)
		}
		u := binary.BigEndian.Uint64(b[:8])
		if u>>63 != 0 {
			return h, &protocolError{CloseProtocolError, "a frame length with its top bit set"}
		}
		n = int64(u)
	}
	if h.op >= CloseMessage && (n > 125 || !h.fin) {
		return h, &protocolError{CloseProtocolError, "a control frame fragmented or over 125 bytes"}
	}
	if h.masked {
		if _, err := io.ReadFull(c.br, h.key[:]); err != nil {
			return h, unexpected(err)
		}
	}
	h.length = n
	return h, nil
}

func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// readPayload appends h's payload to dst, growing it as the bytes arrive
// (never trusting the length for an allocation up front).
func (c *Conn) readPayload(h frameHeader, dst []byte) ([]byte, error) {
	start := len(dst)
	for left := h.length; left > 0; {
		n := int(min(left, 64<<10))
		dst = slices.Grow(dst, n)
		m, err := io.ReadFull(c.br, dst[len(dst):len(dst)+n])
		dst = dst[:len(dst)+m]
		if err != nil {
			return dst, unexpected(err)
		}
		left -= int64(n)
	}
	if h.masked {
		maskBytes(h.key, dst[start:])
	}
	return dst, nil
}

func maskBytes(key [4]byte, b []byte) {
	for i := range b {
		b[i] ^= key[i&3]
	}
}

// peerClose handles the peer's close frame: answered (unless ours went out
// first), then the connection is dropped.
func (c *Conn) peerClose(payload []byte) error {
	code, text := CloseNoStatusReceived, ""
	switch {
	case len(payload) == 1:
		return &protocolError{CloseProtocolError, "a close frame with a 1-byte payload"}
	case len(payload) >= 2:
		code, text = int(binary.BigEndian.Uint16(payload)), string(payload[2:])
		if !validCloseCode(code) {
			return &protocolError{CloseProtocolError, fmt.Sprintf("close code %d", code)}
		}
		if !utf8.ValidString(text) {
			return &protocolError{CloseInvalidPayload, "a close reason that isn't UTF-8"}
		}
	}
	c.peerOnce.Do(func() { close(c.peerClosed) })
	if code == CloseNoStatusReceived {
		_ = c.writeFrame(true, CloseMessage, nil)
	} else {
		_ = c.writeClose(code, "")
	}
	c.closeConn()
	return &CloseError{Code: code, Text: text}
}

// validCloseCode: a code a close frame may carry (RFC 6455 §7.4).
func validCloseCode(code int) bool {
	switch {
	case code >= 1000 && code <= 1003, code >= 1007 && code <= 1014:
		return true
	case code >= 3000 && code <= 4999:
		return true
	}
	return false
}

// --- writing ------------------------------------------------------------------

// WriteMessage sends one message as a single frame: TextMessage (UTF-8),
// BinaryMessage, or a PingMessage / PongMessage of at most 125 bytes. Safe
// from any number of goroutines. Close with Close or CloseWith.
func (c *Conn) WriteMessage(messageType int, data []byte) error {
	switch messageType {
	case TextMessage, BinaryMessage:
	case PingMessage, PongMessage:
		if len(data) > 125 {
			return errors.New("ws: a control frame's payload is 125 bytes at most")
		}
	case CloseMessage:
		return errors.New("ws: close with Close or CloseWith")
	default:
		return fmt.Errorf("ws: unknown message type %d", messageType)
	}
	return c.writeFrame(true, byte(messageType), data)
}

// Ping sends a ping (payload at most 125 bytes); its pong reaches the pong
// handler while a goroutine reads.
func (c *Conn) Ping(data []byte) error { return c.WriteMessage(PingMessage, data) }

func (c *Conn) writeClose(code int, reason string) error {
	if len(reason) > 123 {
		reason = reason[:123]
		for !utf8.ValidString(reason) { // don't cut a character in half
			reason = reason[:len(reason)-1]
		}
	}
	p := binary.BigEndian.AppendUint16(nil, uint16(code))
	return c.writeFrame(true, CloseMessage, append(p, reason...))
}

// writeFrame puts one frame on the wire (masked by a client).
func (c *Conn) writeFrame(fin bool, op byte, p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.wclosed {
		return ErrClosed
	}
	if c.werr != nil {
		return c.werr
	}
	if op == CloseMessage {
		c.wclosed = true
	}
	b0 := op
	if fin {
		b0 |= 0x80
	}
	hdr := make([]byte, 0, 14)
	hdr = append(hdr, b0)
	var mbit byte
	if !c.server {
		mbit = 0x80
	}
	switch n := len(p); {
	case n <= 125:
		hdr = append(hdr, mbit|byte(n))
	case n <= 0xffff:
		hdr = append(hdr, mbit|126)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr = append(hdr, mbit|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	var err error
	if c.server {
		bufs := net.Buffers{hdr, p}
		_, err = bufs.WriteTo(c.rwc)
	} else {
		var key [4]byte
		_, _ = rand.Read(key[:])
		buf := make([]byte, 0, len(hdr)+4+len(p))
		buf = append(append(append(buf, hdr...), key[:]...), p...)
		maskBytes(key, buf[len(hdr)+4:])
		_, err = c.rwc.Write(buf)
	}
	if err != nil {
		c.werr = c.ioErr(err)
		return c.werr
	}
	return nil
}

// --- closing ------------------------------------------------------------------

// Close closes the connection normally (1000); see CloseWith.
func (c *Conn) Close() error { return c.CloseWith(CloseNormalClosure, "") }

// CloseWith sends a close frame with code and reason (at most 123 bytes),
// waits up to two seconds for the peer's — reading for it when no other
// goroutine is, discarding what arrives meanwhile — and drops the
// connection. Safe to call more than once and from any goroutine.
func (c *Conn) CloseWith(code int, reason string) error {
	if err := c.writeClose(code, reason); err == nil {
		if c.rmu.TryLock() {
			if c.readErr == nil {
				_ = c.SetReadDeadline(time.Now().Add(closeWait))
				for c.readErr == nil {
					_, _, _ = c.readMessage()
				}
			}
			c.rmu.Unlock()
		} else { // a reader is at work: it sees the peer's close
			t := time.NewTimer(closeWait)
			select {
			case <-c.peerClosed:
			case <-t.C:
			}
			t.Stop()
		}
	}
	c.closeConn()
	return nil
}

func (c *Conn) closeConn() {
	c.closeOnce.Do(func() {
		if c.timers != nil {
			c.timers.stop()
		}
		_ = c.rwc.Close()
	})
}
