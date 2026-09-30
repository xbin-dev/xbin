package boot

// tilesandboxes.go — the tile-sandbox runtime (D120; internal/tilesbx):
// the boot step that builds it, what it needs from the broker and the
// rest of xbind, and every /api/xbin/sandboxes/… route it serves. A
// manager's GET /sandboxes is served by the registry view's route
// (sandboxes.go), which hands a manager call over.

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/tilesbx"
	"github.com/xbin-dev/xbin/internal/vm"
)

// stepTileSandboxes builds the runtime: its definitions and policy load
// with or without isolation (so an admin can see and clean them up), and
// its manager routes answer unsupported without it.
func (st *State) stepTileSandboxes() error {
	bx := ""
	if st.bxDir != "" {
		bx = filepath.Join(st.bxDir, "bx") // the static bx: a namespace sandbox's agent
	}
	st.TileSbx = tilesbx.New(tilesbx.Options{
		Root:     st.WS,
		Isolated: st.Cfg.Isolate,
		UIDRange: st.uidRange,
		Rootfs:   st.rootfs,
		BxPath:   bx,
		Deps:     st.tileSandboxDeps(),
	})
	if err := st.TileSbx.Health(); err != nil {
		slog.Error("tile sandboxes: definitions unreadable", "err", err)
	}
	// A bind, an unbind or a network-set change re-resolves the running
	// sandboxes' classes: a narrowed one stops them (§4). A users event and
	// a registry rescan reconcile every running sandbox with its tile: gone,
	// disabled, the cap lost (a hand edit fires no hook), its mounts, its
	// egress (a D20 policy-row edit fires no OnSandboxNetChange). Rescans
	// by the watcher reconcile too (stepWatch).
	brk := st.Broker
	brk.OnSandboxNetChange = st.TileSbx.OnSandboxNetChange
	brk.OnCapChange = tileSandboxCapHook(st.TileSbx.StopTile)
	// Switching a user's noTerminal on kills the tty execs claimed for them (D88).
	brk.OnNoTerminal = st.TileSbx.OnNoTerminal
	brk.SetTileSandboxes(tileSbxHooks{st.TileSbx})
	if prev := brk.OnStructureChange; prev != nil {
		brk.OnStructureChange = func() { prev(); st.TileSbx.Reconcile() }
	}
	if st.Cfg.Isolate {
		go st.onUsersEvents(st.TileSbx.OnUsersChange)
	}
	// The runner's sampler reads a tile sandbox's leaf inside the parent.
	st.Run.TileCgroup = st.TileSbx.Cgroup()
	// Turning the VM policy's tiles or tilesEmulated off stops the VM
	// sandboxes it no longer allows (putVMPolicy).
	st.onVMPolicy = st.TileSbx.OnVMPolicy
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
		Disk:   tileDiskLow{brk},
		Modes:  sandboxModes{st.VM},
		Tiles:  sandboxTiles{st},
		Net:    sandboxNet{brk},
		Listen: st.listenAddrs(),
		Cgroup: st.Run.Cgroup,
		Sbx:    st.Sbx,
	}
	if st.VM != nil { // never a nil *vm.Manager in the interface
		d.VM = st.VM
	}
	return d
}

// sandboxNet is the broker's sandbox-net classes (§4): strict policies,
// resolved at every call.
type sandboxNet struct{ b *broker.Broker }

func egressClass(c broker.SandboxNet) tilesbx.EgressClass {
	return tilesbx.EgressClass{Class: c.Class, Slot: c.Slot, Ref: c.Ref, Reach: c.Reach, Rules: c.Rules, Note: c.Note}
}

func (s sandboxNet) Classes(tile string) []tilesbx.EgressClass {
	var out []tilesbx.EgressClass
	for _, c := range s.b.SandboxNetClasses(tile) {
		if c.Class != broker.SandboxClassNone { // the runtime lists none itself
			out = append(out, egressClass(c))
		}
	}
	return out
}

func (s sandboxNet) Egress(tile, class string) (tilesbx.EgressClass, sandbox.EgressPolicy, error) {
	c, err := s.b.SandboxEgress(tile, class)
	if err != nil {
		return tilesbx.EgressClass{}, sandbox.EgressPolicy{}, err
	}
	return egressClass(c), c.Policy, nil
}

// listenAddrs is every address xbind listens on — the console, the ingress
// listener, an injected listener's — which a tile sandbox's relay denies
// beside every local address (§4). A name is resolved now; a wildcard is
// covered by the relay's locality check anyway.
func (st *State) listenAddrs() []netip.AddrPort {
	var out []netip.AddrPort
	add := func(hostport string) {
		host, port, err := net.SplitHostPort(hostport)
		if err != nil {
			return
		}
		p, _ := strconv.Atoi(port)
		if host == "" {
			host = "0.0.0.0"
		}
		if a, err := netip.ParseAddr(host); err == nil {
			out = append(out, netip.AddrPortFrom(a.Unmap(), uint16(p)))
			return
		}
		ips, _ := net.LookupIP(host)
		for _, ip := range ips {
			if a, ok := netip.AddrFromSlice(ip); ok {
				out = append(out, netip.AddrPortFrom(a.Unmap(), uint16(p)))
			}
		}
	}
	add(st.Cfg.Listen)
	if st.Cfg.IngressListen != "" {
		add(st.Cfg.IngressListen)
	}
	if st.Cfg.Listener != nil {
		add(st.Cfg.Listener.Addr().String())
	}
	return out
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
	return ok && u.NoTerminal && !u.IsAdmin() // admins never have it (users.Access.NoTerminal)
}

// tileDiskLow is diskmon, for tile sandboxes (§6.3): its low-disk verdict
// and rule (no tile sandbox starts meanwhile) and its fair share (the
// running namespace sandboxes of a tile above it stop). Their bytes never
// count against a scope's write quota.
type tileDiskLow struct{ b *broker.Broker }

func (s tileDiskLow) Low() bool                    { return s.b.DiskLow() }
func (s tileDiskLow) LowAt(free, total int64) bool { return s.b.DiskLowAt(free, total) }
func (s tileDiskLow) FairShare() int64             { return s.b.DiskFairShare() }

// tileSandboxCapHook is the broker's OnCapChange for tile sandboxes (§7):
// a tile that lost cap:sandboxes has its sandboxes stopped, state kept. The
// hook fires on the request goroutine, on approves too (held: the state
// after the change), and again from capSweep on every users event — so it
// is filtered, never blocks (the stop runs on its own goroutine), and is
// idempotent (a tile with nothing running stops nothing).
func tileSandboxCapHook(stopTile func(tile, why string)) func(tile, capTarget string, held bool) {
	return func(tile, capTarget string, held bool) {
		if capTarget == broker.SandboxesCap && !held {
			go stopTile(tile, "cap:sandboxes was revoked: stopped, state kept")
		}
	}
}

// tileSbxHooks is the runtime as the broker drives it (backups, offload,
// leftovers, the seal, grant changes, disk pressure).
type tileSbxHooks struct{ m *tilesbx.Manager }

func (h tileSbxHooks) Defs(tile string) []json.RawMessage { return h.m.Defs(tile) }
func (h tileSbxHooks) RestoreDefs(tile string, defs []json.RawMessage) []string {
	return h.m.RestoreDefs(tile, defs)
}
func (h tileSbxHooks) StopTile(tile, why string)          { h.m.StopTile(tile, why) }
func (h tileSbxHooks) HasState(tile string) (int, int64)  { return h.m.HasState(tile) }
func (h tileSbxHooks) Leftovers(path string) (int, int64) { return h.m.Leftovers(path) }
func (h tileSbxHooks) Usage() map[string]int64            { return h.m.Usage() }
func (h tileSbxHooks) OnResourceChange(tile string)       { h.m.OnResourceChange(tile) }
func (h tileSbxHooks) OnLowDisk()                         { h.m.OnLowDisk() }
func (h tileSbxHooks) StopWhere(pred func(broker.TileSandbox) bool, why string) {
	h.m.StopWhere(func(k tilesbx.Key, d *tilesbx.Def) bool { return pred(tileSandboxOf(k, d)) }, why)
}

// tileSandboxOf is a running sandbox as the broker's predicates see it:
// its tile, its name, its resource mounts.
func tileSandboxOf(k tilesbx.Key, d *tilesbx.Def) broker.TileSandbox {
	s := broker.TileSandbox{Tile: k.Tile, Name: d.Name}
	for _, mt := range d.Mounts {
		if mt.Res != "" {
			s.Res = append(s.Res, mt.Res)
		}
	}
	return s
}

// onUsersEvents calls f on every users event on the hub (a users-plane
// change that can move a policy ceiling), for as long as xbind runs; a
// subscription the hub drops for falling behind is made again.
func (st *State) onUsersEvents(f func()) {
	for {
		ch, cancel := st.Hub.Subscribe(func(e events.Event) bool { return e.Type == "users" })
		for range ch {
			f()
		}
		cancel()
		f() // events may have been missed while it lagged
	}
}

// sandboxModes: VM mode for tile sandboxes is the VM manager's TileVMs —
// the host can run VMs, the VM policy's tiles switch is on and, where VMs
// run emulated, so is tilesEmulated — and its acceleration the probe's.
type sandboxModes struct{ vm *vm.Manager }

func (s sandboxModes) VM() (accel, reason string) {
	st, reason := s.vm.TileVMs()
	switch {
	case reason != "":
		return "", reason
	case st.Emulated:
		return "emulate", ""
	}
	return "kvm", ""
}

type sandboxTiles struct{ st *State }

func (s sandboxTiles) Exists(tile string) bool {
	_, ok := s.st.Reg.Component(tile)
	return ok
}

// Enabled: neither disabled, hidden nor offloaded (plans/lifecycle.md).
func (s sandboxTiles) Enabled(tile string) bool {
	return s.st.Reg.LifecycleState(tile) == registry.StateEnabled
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
	srv.RegisterAPI("GET /sandboxes/{name}/execs/{id}/stdio", m.ServeExecStdio)
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
	// ports (D135): any method, WebSocket upgrades included
	srv.RegisterAPI("/sandboxes/{name}/ports/{port}/{path...}", m.ServePort)
	// snapshots
	srv.RegisterAPI("GET /sandboxes/{name}/snapshots", m.ServeSnapshots)
	srv.RegisterAPI("POST /sandboxes/{name}/snapshots", m.ServeSnapshot)
	srv.RegisterAPI("POST /sandboxes/{name}/snapshots/{sid}/restore", m.ServeRestore)
	srv.RegisterAPI("DELETE /sandboxes/{name}/snapshots/{sid}", m.ServeDeleteSnapshot)
}
