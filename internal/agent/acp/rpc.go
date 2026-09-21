// Package acp is the agent.Driver for the Agent Client Protocol (ACP,
// https://agentclientprotocol.com, protocol version 1): JSON-RPC 2.0, one
// message per line, over the agent's stdio. rpc.go is the codec and the
// call/reply plumbing; types.go the v1 subset xbin uses; client.go the
// state machine that turns ACP into agent.Events; driver.go the Driver.
// The same codec runs inside the sandbox in internal/agent/host, which
// proxies these frames and answers the fs/terminal ones itself.
package acp

import (
	"bufio"
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
	ErrParse        = -32700
	ErrInvalidReq   = -32600
	ErrNotFound     = -32601
	ErrInvalidParam = -32602
	ErrInternal     = -32603
	ErrCancelled    = -32800 // $/cancel_request answered
	ErrAuthRequired = -32000
	ErrNoResource   = -32002
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
// error for that line only, the reader stays usable.
type Decoder struct{ r *bufio.Reader }

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: bufio.NewReaderSize(r, 1<<20)}
}

// ErrBadLine marks a line that was not a JSON-RPC frame.
var ErrBadLine = errors.New("acp: not a JSON-RPC frame")

// Next returns the next frame, ErrBadLine (with the offending text in the
// error) for an unparsable line, io.EOF at the end.
func (d *Decoder) Next() (*Message, error) {
	for {
		line, err := d.r.ReadBytes('\n')
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
// numbered ids and wait for their response; incoming requests and
// notifications go to the handlers. Serve runs the read loop.
type Conn struct {
	w      io.Writer
	wmu    sync.Mutex
	dec    *Decoder
	nextID atomic.Int64
	mu     sync.Mutex
	calls  map[string]chan *Message
	// OnRequest answers an incoming request (return result or *Error);
	// OnNotify sees an incoming notification. Both run on the read loop.
	OnRequest func(m *Message) (any, *Error)
	OnNotify  func(m *Message)
	// OnBad sees a line that was not a frame (logging); the loop continues.
	OnBad func(err error)
}

func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{w: w, dec: NewDecoder(r), calls: map[string]chan *Message{}}
}

// Call sends a request and waits for its response (or the read loop's end).
func (c *Conn) Call(method string, params any, result any) error {
	id := strconv.FormatInt(c.nextID.Add(1), 10)
	ch := make(chan *Message, 1)
	c.mu.Lock()
	c.calls[id] = ch
	c.mu.Unlock()
	p, _ := json.Marshal(params)
	if err := c.send(&Message{ID: json.RawMessage(id), Method: method, Params: p}); err != nil {
		c.drop(id)
		return err
	}
	resp, ok := <-ch
	if !ok || resp == nil {
		return io.ErrClosedPipe
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 && string(resp.Result) != "null" {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
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

func (c *Conn) drop(id string) {
	c.mu.Lock()
	delete(c.calls, id)
	c.mu.Unlock()
}

// Serve reads frames until the reader ends; responses are matched to their
// calls, the rest go to the handlers. On return every waiting call fails.
func (c *Conn) Serve() error {
	defer func() {
		c.mu.Lock()
		for id, ch := range c.calls {
			close(ch)
			delete(c.calls, id)
		}
		c.mu.Unlock()
	}()
	for {
		m, err := c.dec.Next()
		if err != nil {
			if errors.Is(err, ErrBadLine) {
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
			c.mu.Lock()
			ch := c.calls[string(m.ID)]
			delete(c.calls, string(m.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.IsRequest():
			if c.OnRequest == nil {
				_ = c.Reply(m.ID, nil, &Error{Code: ErrNotFound, Message: "method not found: " + m.Method})
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
