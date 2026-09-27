package boot

// tilesandboxes.go — the tile-sandbox runtime (D120; internal/tilesbx):
// the boot step that builds it, what it needs from the broker and the
// rest of xbind, and every /api/xbin/sandboxes/… route it serves. A
// manager's GET /sandboxes is served by the registry view's route
// (sandboxes.go), which hands a manager call over.

import (
	"log/slog"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/tilesbx"
	"github.com/xbin-dev/xbin/internal/vm"
)

// stepTileSandboxes builds the runtime: its definitions and policy load
// with or without isolation (so an admin can see and clean them up), and
// its manager routes answer unsupported without it.
func (st *State) stepTileSandboxes() error {
	st.TileSbx = tilesbx.New(tilesbx.Options{
		Root:     st.WS,
		Isolated: st.Cfg.Isolate,
		UIDRange: st.uidRange,
		Deps:     st.tileSandboxDeps(),
	})
	if err := st.TileSbx.Health(); err != nil {
		slog.Error("tile sandboxes: definitions unreadable", "err", err)
	}
	return nil
}

// tileSandboxDeps wires the runtime to the broker, the users store and the
// VM manager.
func (st *State) tileSandboxDeps() tilesbx.Deps {
	brk := st.Broker
	d := tilesbx.Deps{
		Caps:   sandboxesCap(brk),
		Admin:  brk.IsAdmin,
		Mounts: sandboxMounts{brk},
		Vault:  sandboxVault{brk},
		Users:  sandboxUsers{st},
		Disk:   tileDiskQuota{brk},
		Modes:  sandboxModes{st.VM},
		Tiles:  sandboxTiles{st},
	}
	return d
}

// sandboxesCap is the broker's cap:sandboxes check. Until the broker has
// one, no tile holds the cap and every manager call is refused.
func sandboxesCap(brk *broker.Broker) tilesbx.Caps {
	if c, ok := any(brk).(tilesbx.Caps); ok {
		return c
	}
	slog.Warn("tile sandboxes: this xbind has no cap:sandboxes check; manager calls are refused")
	return nil
}

type sandboxMounts struct{ b *broker.Broker }

func (s sandboxMounts) ResourceMount(tile, res string) (tilesbx.MountSource, error) {
	m, err := s.b.ResourceMount(tile, res)
	return tilesbx.MountSource{Src: m.Src, Role: m.Role, Kind: m.Kind, Encrypted: m.Encrypted, Ready: m.Ready}, err
}

type sandboxVault struct{ b *broker.Broker }

func (s sandboxVault) Sealed() bool {
	bar := s.b.Barrier()
	return bar != nil && bar.Initialized() && bar.Sealed()
}

type sandboxUsers struct{ st *State }

func (s sandboxUsers) NoTerminal(user string) bool {
	if s.st.Users == nil {
		return false
	}
	u, ok := s.st.Users.Get(user)
	return ok && u.NoTerminal
}

type tileDiskQuota struct{ b *broker.Broker }

func (s tileDiskQuota) Blocked(tile string) bool {
	_, _, blocked := s.b.TileDiskStatus(tile)
	return blocked
}

// sandboxModes: VM mode for tile sandboxes needs the VM policy's tiles
// switch, which this xbind doesn't have yet — so it is unavailable, with
// the host's own reason when the host can't run VMs at all.
type sandboxModes struct{ vm *vm.Manager }

func (s sandboxModes) VM() (accel, reason string) {
	if st := s.vm.Status(); !st.Available {
		return "", st.Reason
	}
	return "", "this xbind can't run VM tile sandboxes yet"
}

type sandboxTiles struct{ st *State }

func (s sandboxTiles) Exists(tile string) bool {
	_, ok := s.st.Reg.Component(tile)
	return ok
}

// registerTileSandboxAPI mounts the tile-sandbox routes (docs/protocol.md
// §Tile sandboxes). Manager routes answer only a manager tile's backend
// (cap:sandboxes); admins may read and set the policy, and stop and delete
// with ?tile=.
func (st *State) registerTileSandboxAPI(srv *server.Server) {
	m := st.TileSbx
	// runtime and policy
	srv.RegisterAPI("GET /sandboxes/runtime", m.ServeRuntime)
	srv.RegisterAPI("GET /sandboxes/policy", m.ServePolicy)
	srv.RegisterAPI("PUT /sandboxes/policy", m.ServeSetPolicy)
	// definitions (GET /sandboxes: sandboxes.go)
	srv.RegisterAPI("POST /sandboxes", m.ServeCreate)
	srv.RegisterAPI("GET /sandboxes/{name}", m.ServeGet)
	srv.RegisterAPI("PATCH /sandboxes/{name}", m.ServePatch)
	srv.RegisterAPI("DELETE /sandboxes/{name}", m.ServeDelete)
	// lifecycle
	srv.RegisterAPI("POST /sandboxes/{name}/start", m.ServeStart)
	srv.RegisterAPI("POST /sandboxes/{name}/stop", m.ServeStop)
	srv.RegisterAPI("POST /sandboxes/{name}/reset", m.ServeReset)
	srv.RegisterAPI("POST /sandboxes/{name}/rebase", m.ServeRebase)
	// commands
	srv.RegisterAPI("POST /sandboxes/{name}/run", m.ServeRun)
	srv.RegisterAPI("GET /sandboxes/{name}/execs", m.ServeExecs)
	srv.RegisterAPI("POST /sandboxes/{name}/execs", m.ServeExecStart)
	srv.RegisterAPI("GET /sandboxes/{name}/execs/{id}", m.ServeExec)
	srv.RegisterAPI("DELETE /sandboxes/{name}/execs/{id}", m.ServeExecKill)
	srv.RegisterAPI("GET /sandboxes/{name}/execs/{id}/output", m.ServeOutput)
	srv.RegisterAPI("POST /sandboxes/{name}/execs/{id}/stdin", m.ServeStdin)
	srv.RegisterAPI("POST /sandboxes/{name}/execs/{id}/signal", m.ServeSignal)
	srv.RegisterAPI("POST /sandboxes/{name}/execs/{id}/resize", m.ServeResize)
	srv.RegisterAPI("GET /sandboxes/{name}/execs/{id}/tty", m.ServeExecTTY)
	srv.RegisterAPI("GET /sandboxes/{name}/tty", m.ServeTTY)
	// files and trees
	srv.RegisterAPI("GET /sandboxes/{name}/files/stat", m.ServeStat)
	srv.RegisterAPI("GET /sandboxes/{name}/files/content", m.ServeReadFile)
	srv.RegisterAPI("PUT /sandboxes/{name}/files/content", m.ServeWriteFile)
	srv.RegisterAPI("GET /sandboxes/{name}/files/list", m.ServeListDir)
	srv.RegisterAPI("POST /sandboxes/{name}/files/mkdir", m.ServeMkdir)
	srv.RegisterAPI("POST /sandboxes/{name}/files/remove", m.ServeRemove)
	srv.RegisterAPI("POST /sandboxes/{name}/files/move", m.ServeMove)
	srv.RegisterAPI("GET /sandboxes/{name}/tar", m.ServeGetTar)
	srv.RegisterAPI("PUT /sandboxes/{name}/tar", m.ServePutTar)
	srv.RegisterAPI("POST /sandboxes/copy", m.ServeCopy)
	// snapshots
	srv.RegisterAPI("GET /sandboxes/{name}/snapshots", m.ServeSnapshots)
	srv.RegisterAPI("POST /sandboxes/{name}/snapshots", m.ServeSnapshot)
	srv.RegisterAPI("POST /sandboxes/{name}/snapshots/{sid}/restore", m.ServeRestore)
	srv.RegisterAPI("DELETE /sandboxes/{name}/snapshots/{sid}", m.ServeDeleteSnapshot)
}
