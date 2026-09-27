// backend_xbin.go — the `xbin` backend, the default: xbind's own tile-sandbox
// runtime (docs/protocol.md §Tile sandboxes, D120), through the Go SDK
// (sdk/sandbox*.go). It is the SDK itself — *xbin.Sandboxes is the Fleet and
// *xbin.Sandbox each Box — so every Backend method is one runtime route, and
// the contract layer's requests map onto them as plans/tile-sandbox-runtime.md
// §11 lays out:
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

import xbin "github.com/xbin-dev/xbin/sdk"

func init() {
	registerBackend("xbin", func(BackendEnv) (Backend, error) { return xbinBackend{xbin.SandboxAPI()}, nil })
}

// xbinBackend is the tile's sandboxes at xbind (it takes no settings: the
// runtime's own policy is the workspace admin's).
type xbinBackend struct{ *xbin.Sandboxes }

// Sandbox is one of them, by runtime name.
func (b xbinBackend) Sandbox(name string) Box { return b.Sandboxes.Sandbox(name) }
