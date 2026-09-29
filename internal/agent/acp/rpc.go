// Package acp is the agent.Driver for the Agent Client Protocol (ACP,
// https://agentclientprotocol.com, protocol version 1): sdk/acp's client —
// the codec, the protocol subset, the state machine that turns ACP into
// agent.Events — configured for xbind (driver.go): its prompts' files
// reach the sandbox through the host (_xbin/attach), the host's log lines
// (_xbin/log) land in the session log, and a signed-out agent names the
// terminal login on the tile. types.go keeps xbind's own frames, daemon ⇄
// host (_xbin/*), which never reach an agent; everything else here is the
// SDK's, re-exported so the host (internal/agent/host) and the scripted
// agent (hack/fakeacp) speak the very same codec.
package acp

import (
	"io"

	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// JSON-RPC error codes ACP defines (sdk/acp: Code*).
const (
	ErrParse        = sdkacp.CodeParse
	ErrInvalidReq   = sdkacp.CodeInvalidRequest
	ErrNotFound     = sdkacp.CodeMethodNotFound
	ErrInvalidParam = sdkacp.CodeInvalidParams
	ErrInternal     = sdkacp.CodeInternal
	ErrCancelled    = sdkacp.CodeRequestCancelled // $/cancel_request answered
	ErrAuthRequired = sdkacp.CodeAuthRequired
	ErrNoResource   = sdkacp.CodeResourceNotFound
)

type (
	// Message is one JSON-RPC 2.0 frame: a request (id + method), a
	// notification (method, no id), or a response (id + result | error).
	Message = sdkacp.Message
	// Error is the JSON-RPC error object.
	Error = sdkacp.Error
	// Decoder reads frames line by line; a malformed line is returned as an
	// error for that line only, the reader stays usable.
	Decoder = sdkacp.Decoder
	// Conn is one JSON-RPC peer over a reader/writer pair.
	Conn = sdkacp.Conn
)

// ErrBadLine marks a line that was not a JSON-RPC frame.
var ErrBadLine = sdkacp.ErrBadLine

// Encode writes one frame as a line.
func Encode(w io.Writer, m *Message) error { return sdkacp.Encode(w, m) }

func NewDecoder(r io.Reader) *Decoder { return sdkacp.NewDecoder(r) }

func NewConn(r io.Reader, w io.Writer) *Conn { return sdkacp.NewConn(r, w) }
