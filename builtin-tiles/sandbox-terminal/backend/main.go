// sandbox-terminal — terminals onto sandboxes for people (D121): a consumer
// of the sandbox-manager contract (docs/sandbox-manager.md) that creates no
// sandboxes. A sandbox reaches it by being shared with it (a share naming
// this tile, users "*" or a list) or by being its own.
//
//   - Browser terminals: the page dials the manager's `tty` route itself
//     with its frame token (<bx-terminal src>), so the manager sees the
//     verified person. This backend isn't in that path.
//   - SSH: `ssh <sandbox>@host -p <port>` on the `ssh` stream expose. A key
//     registered by a person says who is asking; the tile lists the
//     sandboxes AS that person (Sbx-User, asserted — the manager records it
//     and trusts this backend to enforce the person rules, managers.go) and
//     bridges the session to the manager's `tty` route, or to an exec
//     without a terminal (bridge.go).
//
// No xbin identity reaches a sandbox: the bytes of a terminal are all that
// cross.
package main

import (
	"log"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	t := newTile(xbin.Self(), xbin.KV(xbin.Resource("state")), xbin.Secret, xbin.SetSecret, managersFromEnv, xbin.Client())
	go func() {
		t.load()
		t.startSSH(":2222") // the `ssh` expose's port (xbin.json)
	}()
	log.Printf("sandbox-terminal: %d sandbox manager(s) bound", len(t.managers()))
	xbin.Serve(t.routes())
}
