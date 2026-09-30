package xbin

// manager_stdio.go — a sandbox manager's stdio sockets, for consumer tiles
// (docs/sandbox-manager.md §stdio): a consumer's backend drives a program
// it started as a non-tty exec in a bound manager's sandbox (a coding agent
// speaking ACP on stdio, POST …/execs {stdin: true, split: true}) over one
// WebSocket, dialled through xbind with its instance credential and the
// person it acts for in Sbx-User (asserted, as for a terminal — the
// partition's own person, verified, from a person's partition:
// manager_tty.go). Only where the manager's hello.caps has "stdio";
// without it, the exec's output and stdin routes do the same.

import (
	"context"
	"net/http"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// ManagerStdioOptions choose where a stdio socket starts and whom it is
// for.
type ManagerStdioOptions struct {
	// Since is the stdout offset the socket starts at (0: the stream's
	// beginning; what the ring dropped is a gap frame), ErrSince stderr's
	// (a split exec). Resume from the offsets read so far.
	Since, ErrSince int64
	// User is the person the consumer acts for, sent as Sbx-User: the
	// manager records it and doesn't verify it. "" is the consumer itself.
	// From a person's partition of a partitioned tile, with a manager whose
	// hello.caps carry "partitions" (use no other there), the person is the
	// partition's, verified — the manager applies its person rules to them,
	// and a User naming anyone else is refused (403 not-allowed); "" is
	// that person there. A manager without it takes the partition's call as
	// the tile's, User asserted and unchecked.
	User string
	// Client dials the manager (nil: Client() — through the gateway, with
	// this instance's credential).
	Client *http.Client
}

// ManagerStdioURL is the WebSocket URL of exec execID's stdio socket on
// sandbox sandboxID of the manager at endpoint (its binding's url,
// "http://xbin/api/coding-sandbox"): GET …/sbx/sandboxes/{id}/execs/{eid}/stdio?since=&errSince=.
// A sandbox id outside the contract's grammar, an exec id that isn't one
// path segment or a negative offset is refused (a *SandboxError, invalid).
func ManagerStdioURL(endpoint, sandboxID, execID string, since, errSince int64) (string, error) {
	base, err := managerSandboxURL(endpoint, sandboxID)
	if err != nil {
		return "", err
	}
	seg, err := managerExecSeg(execID)
	if err != nil {
		return "", err
	}
	q, err := stdioQuery(since, errSince)
	if err != nil {
		return "", err
	}
	u := base + "/execs/" + seg + "/stdio"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u, nil
}

// DialManagerStdio attaches to exec execID's stdio socket on sandbox
// sandboxID of the manager at endpoint, as this consumer for o.User, and
// returns the connection: StdioFrame's wire — {"op":"hello"} first, stdout
// as binary frames from o.Since (a "gap" frame where the manager's ring
// dropped bytes), a split exec's stderr as "stderr" frames from o.ErrSince,
// "exit" once the exec ended and all its output is out, then a normal
// close; binary frames you write are its stdin, {"op":"eof"} closes it.
// Keep one goroutine reading. Attaching replaces any socket attached before
// (it is closed with StdioReplaced): the newest holds stdin. A refusal —
// the manager's (not-found, invalid, lost, unsupported: no stdio there) or
// xbind's (no binding: 403) — is a *SandboxError. ctx bounds the handshake
// only.
func DialManagerStdio(ctx context.Context, endpoint, sandboxID, execID string, o ManagerStdioOptions) (*ws.Conn, error) {
	u, err := ManagerStdioURL(endpoint, sandboxID, execID, o.Since, o.ErrSince)
	if err != nil {
		return nil, err
	}
	return managerDial(ctx, u, o.User, o.Client)
}
