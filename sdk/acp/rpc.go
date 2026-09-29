// The JSON-RPC 2.0 codec ACP runs on: one message per line over the
// agent's stdio. Conn is one peer (calls, replies, notifications); the
// Client drives an agent through one, and a proxy between a client and an
// agent (xbind's in-sandbox host) speaks the same frames with a Conn on
// each side.

package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

// JSON-RPC error codes ACP defines (docs: protocol/v1/overview).
const (
	CodeParse            = -32700
	CodeInvalidRequest   = -32600
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodeInternal         = -32603
	CodeRequestCancelled = -32800 // $/cancel_request answered
	CodeAuthRequired     = -32000
	CodeResourceNotFound = -32002
)

// Message is one JSON-RPC 2.0 frame: a request (id + method), a
// notification (method, no id), or a response (id + result | error).
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	// off is where the frame ends in its stream (Decoder.Offset after it);
	// 0 for a frame that was not decoded.
	off int64
}

// Error is the JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// IsRequest / IsNotification / IsResponse classify a frame.
func (m *Message) IsRequest() bool      { return m.Method != "" && len(m.ID) > 0 }
func (m *Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }
func (m *Message) IsResponse() bool     { return m.Method == "" && len(m.ID) > 0 }

// Encode writes one frame as a line. Newlines inside the JSON never occur
// (encoding/json escapes them in strings), so the line framing holds.
func Encode(w io.Writer, m *Message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Decoder reads frames line by line; a malformed line is returned as an
// error for that line only, the reader stays usable. It counts what it
// consumed (Offset), so a reader that resumes a stream where an earlier one
// stopped can say where each frame ends in the whole stream.
type Decoder struct {
	r      *bufio.Reader
	off    int64 // stream position after the last line consumed
	resync bool  // bytes were lost: drop through the next newline
}

func NewDecoder(r io.Reader) *Decoder { return NewDecoderAt(r, 0) }

// NewDecoderAt is NewDecoder for a reader whose first byte is at stream
// position off (a pipe resumed where an earlier process stopped reading).
func NewDecoderAt(r io.Reader, off int64) *Decoder {
	return &Decoder{r: bufio.NewReaderSize(r, 1<<20), off: off}
}

// ErrBadLine marks a line that was not a JSON-RPC frame.
var ErrBadLine = errors.New("acp: not a JSON-RPC frame")

// ErrGap marks output the reader lost (Gap): Next returns an error that
// matches it, once per gap, and reads on.
var ErrGap = errors.New("acp: output lost")

// Gap is what a Decoder's reader returns from Read, as its error, when
// bytes of the stream were lost before the next byte it delivers (a ring
// that dropped them before they were read). Bytes returned with the error
// precede the gap. The decoder counts Lost into its Offset, drops the
// broken line on each side of the gap, and reads on.
type Gap struct{ Lost int64 }

func (g *Gap) Error() string        { return fmt.Sprintf("acp: %d bytes of output lost", g.Lost) }
func (g *Gap) Is(target error) bool { return target == ErrGap }

// Offset is the stream position through the last line Next consumed — the
// newline ending the frame it returned last, a bad or blank line it
// skipped, the bytes a gap lost — never what the buffered reader read
// ahead. A frame's Offset is where a reader resumes to receive what
// follows it.
func (d *Decoder) Offset() int64 { return d.off }

// Next returns the next frame, ErrBadLine (with the offending text in the
// error) for an unparsable line, an ErrGap error where output was lost,
// io.EOF at the end.
func (d *Decoder) Next() (*Message, error) {
	for {
		line, err := d.r.ReadBytes('\n')
		d.off += int64(len(line))
		var gap *Gap
		if errors.As(err, &gap) {
			// the line before the gap lost its tail: dropped, and so is the
			// head of the one after it (resync)
			d.off += gap.Lost
			d.resync = true
			return nil, fmt.Errorf("%w: %d bytes, and a line broken by it", ErrGap, gap.Lost)
		}
		if d.resync { // the rest of the line a gap broke
			d.resync = false
			if err != nil {
				return nil, err
			}
			continue
		}
		if len(line) == 0 && err != nil {
			return nil, err
		}
		trimmed := trimSpace(line)
		if len(trimmed) == 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		var m Message
		if jerr := json.Unmarshal(trimmed, &m); jerr != nil || (m.Method == "" && len(m.ID) == 0) {
			return nil, fmt.Errorf("%w: %.200s", ErrBadLine, trimmed)
		}
		m.off = d.off
		return &m, nil
	}
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r' || b[j-1] == '\n') {
		j--
	}
	return b[i:j]
}

// Conn is one JSON-RPC peer over a reader/writer pair: outgoing calls get
// ids (numbered, or "<prefix>-N" strings: ConnOptions) and wait for their
// response; incoming requests and notifications go to the handlers. Serve
// runs the read loop.
type Conn struct {
	w      io.Writer
	wmu    sync.Mutex
	dec    *Decoder
	prefix string
	nextID atomic.Int64
	mu     sync.Mutex
	calls  map[string]*waiter // idKey → the call waiting for it
	ended  bool               // Serve has returned: a new waiter fails at once
	// OnRequest answers an incoming request (return result or *Error);
	// OnNotify sees an incoming notification. Both run on the read loop.
	OnRequest func(m *Message) (any, *Error)
	OnNotify  func(m *Message)
	// OnBad sees a line that was not a frame, or output the reader lost
	// (ErrGap) — for logging; the loop continues.
	OnBad func(err error)
	// onResponse sees a response on the read loop before its call does
	// (the method it answers; "" for an Expect).
	onResponse func(method string, m *Message)
}

// waiter is a call waiting for its response.
type waiter struct {
	ch     chan *Message
	method string
}

// ConnOptions set up a Conn (NewConnWith); the zero value is NewConn's.
type ConnOptions struct {
	// IDPrefix makes request ids strings, "<prefix>-1", "<prefix>-2", …
	// instead of numbers — ids a later process taking over the same agent
	// (with another prefix) cannot reuse while the agent may still answer an
	// earlier one (Expect).
	IDPrefix string
	// Offset is the stream position of the reader's first byte (a pipe
	// resumed where an earlier process stopped reading): the Decoder's
	// Offset, and so each frame's, counts from it.
	Offset int64
}

func NewConn(r io.Reader, w io.Writer) *Conn { return NewConnWith(r, w, ConnOptions{}) }

// NewConnWith is NewConn with options.
func NewConnWith(r io.Reader, w io.Writer, o ConnOptions) *Conn {
	return &Conn{w: w, dec: NewDecoderAt(r, o.Offset), prefix: o.IDPrefix, calls: map[string]*waiter{}}
}

// Offset is the read loop's Decoder.Offset: call it from a handler, or
// once Serve has returned.
func (c *Conn) Offset() int64 { return c.dec.Offset() }

// Call sends a request and waits for its response (or the read loop's end).
func (c *Conn) Call(method string, params any, result any) error {
	return c.CallCtx(context.Background(), method, params, result)
}

// CallCtx is Call bounded by ctx: a hung peer answers with ctx.Err() and the
// call is dropped (a late response is discarded).
func (c *Conn) CallCtx(ctx context.Context, method string, params any, result any) error {
	resp, err := c.callID(ctx, c.newID(), method, params)
	if err != nil {
		return err
	}
	if result != nil && len(resp.Result) > 0 && string(resp.Result) != "null" {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

// newID is the next request id: a number, or "<prefix>-N"; never one a
// call (or an Expect) is still waiting on.
func (c *Conn) newID() json.RawMessage {
	for {
		n := c.nextID.Add(1)
		var id json.RawMessage
		if c.prefix == "" {
			id = json.RawMessage(strconv.FormatInt(n, 10))
		} else {
			id, _ = json.Marshal(c.prefix + "-" + strconv.FormatInt(n, 10))
		}
		c.mu.Lock()
		_, taken := c.calls[idKey(id)]
		c.mu.Unlock()
		if !taken {
			return id
		}
	}
}

// callID sends a request with the given id and waits for its response;
// an error response is returned as its *Error.
func (c *Conn) callID(ctx context.Context, id json.RawMessage, method string, params any) (*Message, error) {
	key := idKey(id)
	ch := c.wait(key, method)
	p, _ := json.Marshal(params)
	if err := c.send(&Message{ID: id, Method: method, Params: p}); err != nil {
		c.drop(key)
		return nil, err
	}
	return c.await(ctx, key, ch)
}

// wait registers a waiter for key (closed at once when the loop has ended).
func (c *Conn) wait(key, method string) chan *Message {
	ch := make(chan *Message, 1)
	c.mu.Lock()
	if c.ended {
		close(ch)
	} else {
		c.calls[key] = &waiter{ch: ch, method: method}
	}
	c.mu.Unlock()
	return ch
}

// await is a waiter's response: io.ErrClosedPipe when the loop ended
// first, its *Error when the peer answered one.
func (c *Conn) await(ctx context.Context, key string, ch chan *Message) (*Message, error) {
	var resp *Message
	var ok bool
	select {
	case resp, ok = <-ch:
	case <-ctx.Done():
		c.drop(key)
		return nil, ctx.Err()
	}
	if !ok || resp == nil {
		return nil, io.ErrClosedPipe
	}
	if resp.Error != nil {
		return resp, resp.Error
	}
	return resp, nil
}

// Expect adopts a call an earlier process sent on the same agent (its
// request id): the response is delivered on the channel, which is closed
// if the read loop ends first. Register it before Serve, or a response
// that arrives before is discarded like any unknown one.
func (c *Conn) Expect(id json.RawMessage) <-chan *Message { return c.wait(idKey(id), "") }

// idKey is a request id as the calls map knows it: a number as written, a
// string re-encoded (the peer may escape it differently).
func idKey(id json.RawMessage) string {
	t := trimSpace(id)
	if len(t) > 0 && t[0] == '"' {
		var s string
		if json.Unmarshal(t, &s) == nil {
			b, _ := json.Marshal(s)
			return string(b)
		}
	}
	return string(t)
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	p, _ := json.Marshal(params)
	return c.send(&Message{Method: method, Params: p})
}

// Reply answers an incoming request by id.
func (c *Conn) Reply(id json.RawMessage, result any, rerr *Error) error {
	m := &Message{ID: id, Error: rerr}
	if rerr == nil {
		if result == nil {
			result = map[string]any{}
		}
		m.Result, _ = json.Marshal(result)
	}
	return c.send(m)
}

// Send writes a raw frame (the host's proxy path).
func (c *Conn) Send(m *Message) error { return c.send(m) }

func (c *Conn) send(m *Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return Encode(c.w, m)
}

func (c *Conn) drop(key string) {
	c.mu.Lock()
	delete(c.calls, key)
	c.mu.Unlock()
}

// Serve reads frames until the reader ends; responses are matched to their
// calls, the rest go to the handlers. On return every waiting call fails.
func (c *Conn) Serve() error {
	defer func() {
		c.mu.Lock()
		c.ended = true
		for id, w := range c.calls {
			close(w.ch)
			delete(c.calls, id)
		}
		c.mu.Unlock()
	}()
	for {
		m, err := c.dec.Next()
		if err != nil {
			if errors.Is(err, ErrBadLine) || errors.Is(err, ErrGap) {
				if c.OnBad != nil {
					c.OnBad(err)
				}
				continue
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch {
		case m.IsResponse():
			key := idKey(m.ID)
			c.mu.Lock()
			w := c.calls[key]
			delete(c.calls, key)
			c.mu.Unlock()
			if w != nil {
				if c.onResponse != nil {
					c.onResponse(w.method, m)
				}
				w.ch <- m
			}
		case m.IsRequest():
			if c.OnRequest == nil {
				_ = c.Reply(m.ID, nil, &Error{Code: CodeMethodNotFound, Message: "method not found: " + m.Method})
				continue
			}
			res, rerr := c.OnRequest(m)
			if res == nil && rerr == nil {
				continue // the handler replies later (a pending permission)
			}
			_ = c.Reply(m.ID, res, rerr)
		default:
			if c.OnNotify != nil {
				c.OnNotify(m)
			}
		}
	}
}
