//go:build ignore

// WIRE ON MERGE (WP-14): DialTTY needs the SDK's WebSocket package, sdk/ws,
// which lands with branch p3/prep. Once sdk/ws is on this branch, delete the
// `//go:build ignore` line above (and the one in sandbox_dialtty_test.go),
// add DialTTY to docs/sdk.md §Tile sandboxes, and run `go test ./sdk/...`.
// Everything else about tile sandboxes works without it: a relay needs no
// WebSocket code (Forward, RelayTTY).

package xbin

import (
	"context"
	"net/http"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// DialTTY attaches to tty exec execID itself, through the gateway with this
// tile's credential — for a manager that drives the terminal rather than
// relaying a consumer's socket (an SSH bridge). The connection speaks
// /ws/term's wire (docs/protocol.md): binary frames of terminal bytes both
// ways, the session frame first, {"op":"exit"} at the end; keep one
// goroutine reading. A refused attach is a *SandboxError.
func (b *Sandbox) DialTTY(ctx context.Context, execID string, o TTYOptions) (*ws.Conn, error) {
	path, err := b.execRoute(execID, "tty")
	if err != nil {
		return nil, err
	}
	u := "ws" + sandboxesURL[len("http"):] + path
	if q := o.values(); len(q) > 0 {
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
