// backend_xbin.go — the `xbin` backend, the default: xbind's own tile-sandbox
// runtime (docs/protocol.md §Tile sandboxes, D120), through the Go SDK
// (sdk/sandbox*.go). It is the SDK itself — *xbin.Sandboxes is the Fleet and
// *xbin.Sandbox each Box, with one translation (xbinBox: an id the runtime's
// grammar can't hold names nothing) — so every Backend method is one runtime
// route, and the contract layer's requests map onto them as D120 and D122
// lay out:
//
//	hello                       GET  /sandboxes/runtime       Runtime: caps, modes, egress classes, limits
//	list · get                  GET  /sandboxes[/{name}]      List · Get (merged with the manager's record)
//	create (+ clone, image)     POST /sandboxes               Create: mode (vm | namespace, never chosen for us),
//	                                                          sizes, net.egress class:<slot>, defaults (the layout),
//	                                                          mounts (config.mounts), idleStopMin (autoStopMin),
//	                                                          for/forUser (claims), labels, clientId, from
//	PATCH                       PATCH /sandboxes/{name}       Patch: sizes, net, idleStopMin, defaults (a rename)
//	DELETE · start · stop       DELETE · POST …/start|stop    Delete · Start · Stop (?wait)
//	run · execs · output …      POST …/run, …/execs/…         Run · Exec · Execs · GetExec · Output · Stdin · Signal · Resize · Kill
//	                                                          (uid/gid the layout's, forUser the person, exec clientIds
//	                                                          prefixed per consumer)
//	terminals                   GET  …/tty, …/execs/{id}/tty  RelayNewTTY · RelayTTY: a byte relay of the WebSocket, with
//	                                                          forUser = the person and the session frame's ids = ours
//	files · tar                 …/files/*, …/tar              Stat · ReadFile · WriteFile · List · Mkdir · Remove · Move · GetTar · PutTar
//	snapshots                   …/snapshots[/{sid}[/restore]] Snapshots · Snapshot · RestoreSnapshot · DeleteSnapshot
//
// Where the runtime lacks something its Runtime answer leaves it out of
// `caps` (routes still being built answer 501 unsupported), and hello offers
// only what it lists: images with a setup script need `snapshots` and
// `clone` (hello.notes says so while they're missing), and `archive` isn't
// offered at all. The call needs cap:sandboxes, which only a workspace admin
// approves; without it every call is 403 and the manager answers 503
// unavailable, saying so.
package main

import (
	"context"
	"io"
	"net/http"
	"strconv"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	registerBackend("xbin", func(BackendEnv) (Backend, error) { return xbinBackend{xbin.SandboxAPI()}, nil })
}

// xbinBackend is the tile's sandboxes at xbind (it takes no settings: the
// runtime's own policy is the workspace admin's).
type xbinBackend struct{ *xbin.Sandboxes }

// Sandbox is one of them, by runtime name.
func (b xbinBackend) Sandbox(name string) Box { return xbinBox{b.Sandboxes.Sandbox(name)} }

// xbinBox is one runtime sandbox: the SDK's own, but for one translation.
// Our exec and snapshot ids are the runtime's, and one that its grammar
// can't hold (xbin.IsExecID, xbin.IsSnapshotID) names no exec or snapshot:
// the contract's not-found. The SDK refuses such an id as invalid before
// anything is sent (so a consumer's "..%2F…" never reaches another route),
// and the runtime would too — so it's answered here, as the contract asks.
type xbinBox struct{ *xbin.Sandbox }

// unknown is the not-found for an id the grammar can't hold (what is
// "exec" or "snapshot").
func unknown(what, id string) error {
	if len(id) > 64 {
		id = id[:64] + "…"
	}
	return &xbin.SandboxError{Status: http.StatusNotFound, Refusal: "not-found", Message: "no " + what + " " + strconv.Quote(id)}
}

func (b xbinBox) GetExec(ctx context.Context, id string) (*xbin.ExecInfo, error) {
	if !xbin.IsExecID(id) {
		return nil, unknown("exec", id)
	}
	return b.Sandbox.GetExec(ctx, id)
}

func (b xbinBox) Output(ctx context.Context, id string, q xbin.OutputQuery) (*xbin.OutputChunk, error) {
	if !xbin.IsExecID(id) {
		return nil, unknown("exec", id)
	}
	return b.Sandbox.Output(ctx, id, q)
}

func (b xbinBox) Stdin(ctx context.Context, id string, r io.Reader, eof bool) error {
	if !xbin.IsExecID(id) {
		return unknown("exec", id)
	}
	return b.Sandbox.Stdin(ctx, id, r, eof)
}

func (b xbinBox) Signal(ctx context.Context, id, sig string, group bool) error {
	if !xbin.IsExecID(id) {
		return unknown("exec", id)
	}
	return b.Sandbox.Signal(ctx, id, sig, group)
}

func (b xbinBox) Resize(ctx context.Context, id string, rows, cols int) error {
	if !xbin.IsExecID(id) {
		return unknown("exec", id)
	}
	return b.Sandbox.Resize(ctx, id, rows, cols)
}

func (b xbinBox) Kill(ctx context.Context, id string) error {
	if !xbin.IsExecID(id) {
		return unknown("exec", id)
	}
	return b.Sandbox.Kill(ctx, id)
}

func (b xbinBox) RelayTTY(w http.ResponseWriter, r *http.Request, execID string, o xbin.TTYOptions) {
	if !xbin.IsExecID(execID) {
		xbin.WriteSandboxError(w, unknown("exec", execID))
		return
	}
	b.Sandbox.RelayTTY(w, r, execID, o)
}

func (b xbinBox) RestoreSnapshot(ctx context.Context, id string) (*xbin.SandboxInfo, error) {
	if !xbin.IsSnapshotID(id) {
		return nil, unknown("snapshot", id)
	}
	return b.Sandbox.RestoreSnapshot(ctx, id)
}

func (b xbinBox) DeleteSnapshot(ctx context.Context, id string) error {
	if !xbin.IsSnapshotID(id) {
		return unknown("snapshot", id)
	}
	return b.Sandbox.DeleteSnapshot(ctx, id)
}
