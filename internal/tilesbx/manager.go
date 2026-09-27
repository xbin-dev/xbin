// Package tilesbx is xbind's tile-sandbox runtime (D120): the sandboxes a
// manager tile — one holding cap:sandboxes — defines and drives for the
// tiles it serves, through /api/xbin/sandboxes/… (docs/protocol.md §Tile
// sandboxes). Its routes mirror the sandbox-manager contract
// (docs/sandbox-manager.md), so a manager forwards most calls unchanged.
//
// This package holds the definitions store (data/sandboxes.json, xbind-only),
// the sandboxes policy (.xbin/sandboxes/policy.json), the gates, the route
// handlers and the runtime core: the launch (launch.go), the agent client
// (agent.go), each sandbox's runs and their one teardown (lifecycle.go),
// its relay (netcfg.go), its cgroup leaf (cgroup.go), its registry row
// (registry.go) and the confined removal of what is deleted (trash.go) —
// and the lifecycle policy: admission (admission.go), the idle stop
// (idle.go), auto-start (autostart.go), reset and rebase (reset.go), and
// the boot's sweep (sweep.go). What it needs from the rest of xbind comes
// in through Deps, small interfaces a test fakes. Execs, files and
// snapshots answer `unsupported` until they are built
// (plans/tile-sandbox-runtime.md §12).
package tilesbx

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/sbx"
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
	// Egress resolves one of the tile's egress selectors ("none" |
	// "class:<slot>") into its class and the policy a sandbox runs under
	// now — strict (sandbox.EgressPolicy.Strict, §4). An error means it names
	// no class of the tile (any longer). The runtime calls it at every start
	// and when the broker says the tile's classes changed.
	Egress(tile, class string) (EgressClass, sandbox.EgressPolicy, error)
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

// Disk is diskmon, as tile sandboxes see it (§6.3): while the workspace
// disk is low no tile sandbox starts, and the running namespace sandboxes
// of the tiles above the fair share are stopped. Sandbox bytes never count
// against a scope's resource-write quota, so that isn't asked.
type Disk interface {
	// Low is diskmon's last verdict (its 45 s scan).
	Low() bool
	// LowAt is the same rule over a statfs of the workspace partition
	// taken now (free and total bytes): the runtime's own 5 s watch.
	LowAt(free, total int64) bool
	// FairShare is diskmon's fair share (bytes; sandbox bytes included):
	// under low disk, a tile whose sandboxes hold more is stopped.
	FairShare() int64
}

// Modes says whether VM mode may run tile sandboxes now: its acceleration
// ("kvm" | "emulate"), or why not. Namespace mode needs only isolation.
type Modes interface {
	VM() (accel, reason string)
}

// Tiles says whether a tile exists (an admin's leftovers view; a tile that
// vanished has its sandboxes stopped, state kept) and whether it is
// enabled (a disabled, hidden or offloaded tile's sandboxes don't run).
type Tiles interface {
	Exists(tile string) bool
	Enabled(tile string) bool
}

// Deps is what the runtime needs from the rest of xbind. A nil member
// answers conservatively: no cap, no classes, no mounts, VM unavailable, no
// cgroup limits, no registry rows.
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
	// Launcher starts a sandbox's first process (nil: sandbox.Launch).
	Launcher Launcher
	// Listen is every address xbind listens on: a sandbox's relay denies
	// them, beside every address the host delivers locally (§4).
	Listen []netip.AddrPort
	// Cgroup is xbind's delegated cgroup: the tile sandboxes' parent is made
	// in it (§6.2). nil or disabled: they run without limits.
	Cgroup *cgroup.Manager
	// Sbx is the sandbox registry: a row per running sandbox, and what
	// failed (D112).
	Sbx *sbx.Registry
}

// Options configure a Manager.
type Options struct {
	Root     string // the workspace
	Isolated bool   // xbind --isolate: without it every manager route but runtime answers unsupported
	// UIDRange: namespace sandboxes map a delegated sub-id range, so they
	// run as any user; otherwise only as root ("users": "root").
	UIDRange bool
	// Rootfs is --isolate's base rootfs (absolute): the base a sandbox's
	// state pins (internal/layers). BxPath is the static bx bound in as a
	// namespace sandbox's agent.
	Rootfs string
	BxPath string
	Deps   Deps
	Now    func() time.Time // tests
	// DiskUsage measures a sandbox's state dir (nil: confine.DiskUsage, a
	// confined du); Statfs is the workspace partition's free and total
	// bytes (nil: statfs(2)). Tests stand in for both.
	DiskUsage func(ctx context.Context, dir string) (int64, error)
	Statfs    func(dir string) (free, total int64)
}

// Manager is the runtime: definitions, policy, live state, handlers.
type Manager struct {
	root     string
	isolated bool
	uidRange bool
	deps     Deps
	now      func() time.Time

	rootfs      string
	baseVersion string // the rootfs's base version: a sandbox pinned to another is outdated (rebase, reset)
	bxPath      string
	launcher    Launcher
	modes       map[string]*modeOps // how each mode starts and stops (VM: WP-16)
	net         *netState           // the relays' shared flow budget and host Deny
	cg          *cgroup.Manager     // the tile sandboxes' parent (nil: no limits)
	cgNote      string              // why there is no parent though xbind's cgroup is delegated
	// reserve books a start against the tile's quotas (§6.1) and returns
	// its idempotent release: admit (admission.go); a test may wrap it.
	reserve func(k Key, d *Def) (release func(), err error)
	books   books      // what the running sandboxes hold (admission.go)
	trash   trashQueue // what waits for the confined remover (trash.go)
	// afterFunc is time.AfterFunc (a test's clock may stand in): the idle
	// timers (idle.go).
	afterFunc func(time.Duration, func()) stopper
	// bootID is this xbind start's exec-id prefix: <bootID>-<n>. An id
	// with another is an exec of an earlier boot — lost (410).
	bootID string
	recon  reconcileState // the rescan and users-event reconcile (reconcile.go)
	usage  usageState     // the disk measurements and the low-disk watch (usage.go)
	// endWait and lockWait are lifecycle.go's (tests shorten them).
	endWait, lockWait time.Duration

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
		rootfs: o.Rootfs, bxPath: o.BxPath, launcher: o.Deps.Launcher,
		modes: map[string]*modeOps{ModeNamespace: nsOps},
		net:   &netState{budget: relay.NewBudget(flowBudgetSize()), listen: o.Deps.Listen},
		live:  map[Key]map[string]*box{}}
	if m.now == nil {
		m.now = time.Now
	}
	if m.launcher == nil {
		m.launcher = nsLauncher{}
	}
	if m.rootfs != "" {
		m.baseVersion = layers.BaseVersion(m.rootfs)
	}
	m.reserve = m.admit
	m.afterFunc = func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }
	m.bootID = newBootID()
	m.endWait, m.lockWait = endWait, lockWait
	m.trash.remove = confine.RemoveAll
	m.usage.du, m.usage.statfs = o.DiskUsage, o.Statfs
	if m.usage.du == nil {
		m.usage.du = confine.DiskUsage
	}
	if m.usage.statfs == nil {
		m.usage.statfs = statfs
	}
	m.defs = loadDefs(m.defsPath())
	m.defs.flush() // uids given to definitions written before uids existed
	m.policy = &policyStore{path: m.policyPath()}
	m.policy.get() // loaded now: an unreadable file is logged at boot (and fails closed)
	if o.Isolated {
		m.initCgroup(o.Deps.Cgroup)
		m.bootSweep()  // what a previous xbind left: its leaves, its trash, its staging
		m.measureAll() // every sandbox's disk, once: nothing was measured since the restart
	}
	return m
}

// Health is what went wrong loading the definitions (nil = fine).
func (m *Manager) Health() error { return m.defs.err }

// Isolated reports whether tile sandboxes can run here at all.
func (m *Manager) Isolated() bool { return m.isolated }

// box is one sandbox's live state, kept while xbind runs (a sandbox with
// none is stopped). Its fields are read and written under m.mu; flight is
// its single flight: every transition (start, stop, delete) holds it, and
// takes m.mu only for moments inside it.
type box struct {
	flight *sync.Mutex
	log    *logRing // its first process's output, across runs

	state        string // creating | stopped | starting | running | stopping | error
	detail       string // why: a failed start, a stop the runtime made
	accel        string // a running VM's
	started      int64  // unix ms
	lastActive   int64  // unix ms
	launched     *Def   // the definition the running sandbox started with
	reach        string // the running relay's reach
	egressNext   bool   // its class's rules widened since it started: they apply at the next start
	run          *run   // the run up now (nil: none)
	execsRunning int
	diskBytes    int64 // allocated, snapshots included: measured at each stop and while it runs (usage.go)
	measured     int64 // when diskBytes was measured (unix ms; 0 = not since xbind started)
	snapshots    int
}

func newBox() *box { return &box{flight: &sync.Mutex{}, log: &logRing{}, state: StateStopped} }

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
