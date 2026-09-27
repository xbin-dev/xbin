// Package tilesbx is xbind's tile-sandbox runtime (D120): the sandboxes a
// manager tile — one holding cap:sandboxes — defines and drives for the
// tiles it serves, through /api/xbin/sandboxes/… (docs/protocol.md §Tile
// sandboxes). Its routes mirror the sandbox-manager contract
// (docs/sandbox-manager.md), so a manager forwards most calls unchanged.
//
// This package holds the definitions store (data/sandboxes.json, xbind-only),
// the sandboxes policy (.xbin/sandboxes/policy.json), the gates and the
// route handlers. What it needs from the rest of xbind comes in through Deps,
// small interfaces a test fakes. The runtime proper — launch, the agent
// client, execs, files, snapshots — fills the handlers that answer
// `unsupported` until it is built (plans/tile-sandbox-runtime.md §12).
package tilesbx

import (
	"os"
	"sync"
	"time"
)

// Caps answers cap:sandboxes: whether a tile may drive tile sandboxes.
type Caps interface {
	SandboxesFor(tile string) bool
}

// Net lists a tile's sandbox-net classes, resolved now: the request-side
// interface slots of kind sandbox-net its manifest declares, with what each
// is bound to and reaches. An unbound class reaches nothing.
type Net interface {
	Classes(tile string) []EgressClass
}

// EgressClass is one egress a sandbox may be given: "none", or a
// sandbox-net slot of its tile ("class:<slot>").
type EgressClass struct {
	Class string   `json:"class"`          // "none" | "class:<slot>"
	Slot  string   `json:"slot,omitempty"` // the interface slot
	Ref   string   `json:"ref,omitempty"`  // what it is bound to ("" = unbound)
	Reach string   `json:"reach"`          // none | internet | open (the contract's words)
	Rules []string `json:"rules,omitempty"`
	Note  string   `json:"note,omitempty"` // why a binding is inert, or unbound
}

// Mounts resolves a resource a tile would mount into its sandboxes: the
// broker's ResourceMount. A refusal (not held, not a filesystem, another
// scope's) is an error whose text says why.
type Mounts interface {
	ResourceMount(tile, res string) (MountSource, error)
}

// MountSource is a resolved resource mount.
type MountSource struct {
	Src       string // the resource's root on the host (xbind-created, trusted)
	Role      string // the tile's role on it: a reader's mount is read-only
	Kind      string // the resource's type
	Encrypted bool   // behind the vault: Src exists only while Ready
	Ready     bool   // mountable now (the vault unsealed, the view up)
}

// Vault says whether the vault is sealed: a start needing an encrypted
// resource answers unavailable meanwhile.
type Vault interface {
	Sealed() bool
}

// Users answers D88's noTerminal for a forUser claim (the TTY routes).
type Users interface {
	NoTerminal(user string) bool
}

// Disk says whether the tile's scope is over its disk quota (diskmon).
type Disk interface {
	Blocked(tile string) bool
}

// Modes says whether VM mode may run tile sandboxes now: its acceleration
// ("kvm" | "emulate"), or why not. Namespace mode needs only isolation.
type Modes interface {
	VM() (accel, reason string)
}

// Tiles says whether a tile exists (an admin's leftovers view).
type Tiles interface {
	Exists(tile string) bool
}

// Deps is what the runtime needs from the rest of xbind. A nil member
// answers conservatively: no cap, no classes, no mounts, VM unavailable.
type Deps struct {
	Caps   Caps
	Admin  AdminFunc // workspace admins (broker.IsAdmin)
	Net    Net
	Mounts Mounts
	Vault  Vault
	Users  Users
	Disk   Disk
	Modes  Modes
	Tiles  Tiles
}

// Options configure a Manager.
type Options struct {
	Root     string // the workspace
	Isolated bool   // xbind --isolate: without it every manager route but runtime answers unsupported
	// UIDRange: namespace sandboxes map a delegated sub-id range, so they
	// run as any user; otherwise only as root ("users": "root").
	UIDRange bool
	Deps     Deps
	Now      func() time.Time // tests
}

// Manager is the runtime: definitions, policy, live state, handlers.
type Manager struct {
	root     string
	isolated bool
	uidRange bool
	deps     Deps
	now      func() time.Time

	mu     sync.Mutex // defs, live and the policy file
	defs   *defStore
	policy *policyStore
	live   map[Key]map[string]*box
}

// New builds the runtime over a workspace and loads its definitions. An
// unreadable definitions file doesn't stop xbind: Health reports it, reads
// see nothing and writes are refused, so the file is never clobbered.
func New(o Options) *Manager {
	m := &Manager{root: o.Root, isolated: o.Isolated, uidRange: o.UIDRange, deps: o.Deps, now: o.Now,
		live: map[Key]map[string]*box{}}
	if m.now == nil {
		m.now = time.Now
	}
	m.defs = loadDefs(m.defsPath())
	m.defs.flush() // uids given to definitions written before uids existed
	m.policy = &policyStore{path: m.policyPath()}
	return m
}

// Health is what went wrong loading the definitions (nil = fine).
func (m *Manager) Health() error { return m.defs.err }

// Isolated reports whether tile sandboxes can run here at all.
func (m *Manager) Isolated() bool { return m.isolated }

// box is one sandbox's live state. The skeleton knows only "stopped"; the
// lifecycle fills the rest.
type box struct {
	state        string // creating | stopped | starting | running | stopping | error
	detail       string // why: a failed start, a stop the runtime made
	accel        string // a running VM's
	started      int64  // unix ms
	lastActive   int64  // unix ms
	launched     *Def   // the definition the running sandbox started with
	reach        string // the running relay's reach
	execsRunning int
	diskBytes    int64 // allocated, measured at each stop
	snapshots    int
}

// States a sandbox is in. StateCreating is a clone whose copy still runs
// (Def.Pending "clone"): every call but GET, list and DELETE answers 409
// state until it ends stopped, running or error (WP-20 sets it).
const (
	StateCreating = "creating"
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
	StateStopping = "stopping"
	StateError    = "error"
)

// boxOf is a copy of the sandbox's live state (stopped when there is none).
// Callers hold m.mu.
func (m *Manager) boxOf(k Key, name string) box {
	if b, ok := m.live[k][name]; ok {
		return *b
	}
	return box{state: StateStopped}
}

// setDetail records why a stopped sandbox is stopped (a failed start).
// Callers hold m.mu.
func (m *Manager) setDetail(k Key, name, detail string) {
	b := m.live[k][name]
	if b == nil {
		b = &box{state: StateStopped}
		if m.live[k] == nil {
			m.live[k] = map[string]*box{}
		}
		m.live[k][name] = b
	}
	b.detail = detail
}

// The lifecycle hooks the runtime core fills: until it is built nothing
// starts, so nothing runs and there is nothing to stop.

// start brings a stopped sandbox up. Callers hold m.mu.
func (m *Manager) start(k Key, name string) error {
	_, _ = k, name
	return notBuilt("starting tile sandboxes")
}

// stop brings a sandbox down, keeping its state (sync, then kill; its
// execs end killed). Callers hold m.mu.
func (m *Manager) stop(k Key, name, why string) error {
	_, _, _ = k, name, why
	return nil
}

// removeState deletes a stopped sandbox's state directory. The state is
// sandbox-written, so only a confined remove may touch it (D78); until the
// runtime brings one, state that exists is refused rather than walked as
// xbind — and the definition is kept, so nothing is orphaned silently.
func (m *Manager) removeState(k Key, d *Def) error {
	dir, err := m.StateDir(k, d)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil
	}
	return refuse(RefUnsupported, "sandbox %q has state on disk, and this xbind can't remove sandbox state yet", d.Name)
}
