package xbin

// sandbox_stdio.go — a non-tty exec's stdio WebSocket (the sandbox-manager
// contract's optional `stdio` capability, docs/sandbox-manager.md §stdio;
// the runtime's GET /sandboxes/<name>/execs/<id>/stdio): stdout as binary
// frames and stdin back, a split exec's stderr as JSON, from byte offsets,
// on one socket — for a program driven over its stdin and stdout (a coding
// agent speaking ACP) without polling. RelayStdio is a manager relaying its
// consumer's socket; DialStdio a manager driving one itself.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// StdioReplaced is the close code of a stdio socket a newer attach to the
// same exec replaced: the socket attached last holds the exec's stdin.
const StdioReplaced = 4001

// StdioFrame is one JSON frame of the stdio wire, for decoding. From the
// server: "hello" first (ID, Total, ErrTotal, State, Stdin, Split); "gap"
// (Stream "stdout" or "stderr": bytes From…To the ring dropped); "stderr"
// (Data, a split exec's stderr from offset Off); "exit" (Code, nil when a
// signal ended it, Signal, Total, ErrTotal) once the exec ended and all its
// output is out, then a normal close; "pong" (T); "error" (Refusal, Error:
// what a client frame couldn't do — stdin to an exec without it, after eof
// or after its end). From the client: "eof" (close stdin) and "ping" (T).
// Binary frames are stdout from the server and stdin from the client.
type StdioFrame struct {
	Op       string          `json:"op"`
	ID       string          `json:"id,omitempty"`
	Total    int64           `json:"total,omitempty"`
	ErrTotal int64           `json:"errTotal,omitempty"`
	State    string          `json:"state,omitempty"`
	Stdin    bool            `json:"stdin,omitempty"`
	Split    bool            `json:"split,omitempty"`
	Stream   string          `json:"stream,omitempty"`
	From     int64           `json:"from,omitempty"`
	To       int64           `json:"to,omitempty"`
	Off      int64           `json:"off,omitempty"`
	Data     []byte          `json:"data,omitempty"` // base64 on the wire
	Code     *int            `json:"code,omitempty"`
	Signal   string          `json:"signal,omitempty"`
	Refusal  string          `json:"refusal,omitempty"`
	Error    string          `json:"error,omitempty"`
	T        json.RawMessage `json:"t,omitempty"`
}

// stdioQuery is ?since=&errSince= (a negative offset is refused).
func stdioQuery(since, errSince int64) (url.Values, error) {
	if since < 0 || errSince < 0 {
		return nil, invalidf("since and errSince are offsets: not negative")
	}
	q := url.Values{}
	if since > 0 {
		q.Set("since", strconv.FormatInt(since, 10))
	}
	if errSince > 0 {
		q.Set("errSince", strconv.FormatInt(errSince, 10))
	}
	return q, nil
}

// RelayStdio relays the consumer's stdio WebSocket (r, a request the
// manager has checked) to exec execID's, from stdout offset since and
// stderr offset errSince: Forward to ExecStdio(execID). Refusals (a tty
// exec, an offset past its stream's end, a lost exec) come before the
// upgrade, as the runtime gave them.
func (b *Sandbox) RelayStdio(w http.ResponseWriter, r *http.Request, execID string, since, errSince int64) {
	q, err := stdioQuery(since, errSince)
	if err != nil {
		WriteSandboxError(w, err)
		return
	}
	b.Forward(w, r, ExecStdio(execID), q)
}

// DialStdio attaches to exec execID's stdio socket itself, through the
// gateway with this tile's credential, from stdout offset since and stderr
// offset errSince. Read StdioFrame's wire from it (keep one goroutine
// reading); binary frames you write are the command's stdin. Attaching
// replaces any socket attached before (StdioReplaced). A refused attach is
// a *SandboxError.
func (b *Sandbox) DialStdio(ctx context.Context, execID string, since, errSince int64) (*ws.Conn, error) {
	path, err := b.execRoute(execID, "stdio")
	if err != nil {
		return nil, err
	}
	q, err := stdioQuery(since, errSince)
	if err != nil {
		return nil, err
	}
	u := "ws" + sandboxesURL[len("http"):] + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	c, resp, err := ws.Dial(ctx, u, nil, &ws.DialOptions{Client: b.s.c})
	if err != nil {
		if resp != nil && resp.StatusCode >= http.StatusBadRequest {
			return nil, sandboxError(resp)
		}
		return nil, err
	}
	return c, nil
}
