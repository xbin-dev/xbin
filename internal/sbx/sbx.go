// Package sbx is the registry of the sandboxes xbind runs (D112): every
// live backend generation, terminal and agent session — and, next, the
// sandboxes a tile manages itself (plans/tile-sandboxes.md) — with whose it
// is, how it is isolated (a VM, a namespace sandbox, or none) and what it
// holds. It lives in memory and is rebuilt from lifecycle edges: whoever
// starts a sandbox adds its entry and removes it when the sandbox ends.
// Beside the live list, a bounded ring keeps what the sandbox layer refused
// or failed at, so an admin sees why a VM never came up.
//
// The package imports nothing of xbin's, so the runner, the terminal manager
// and the VM manager can all feed it. A nil *Registry is valid and does
// nothing.
package sbx

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Kind is what a sandbox runs.
type Kind string

const (
	Backend  Kind = "backend"  // a component backend's generation
	Terminal Kind = "terminal" // a shell session
	Agent    Kind = "agent"    // an agent session (D74)
	Tile     Kind = "tile"     // a sandbox a manager tile drives (D120, internal/tilesbx)
)

// Mode is how a sandbox is isolated.
type Mode string

const (
	VM        Mode = "vm"        // its own kernel (D89)
	Namespace Mode = "namespace" // the rootless namespace sandbox
	Host      Mode = "host"      // none: xbind runs without --isolate
)

// Accel is how a VM runs.
type Accel string

const (
	KVM     Accel = "kvm"     // Firecracker on KVM
	Emulate Accel = "emulate" // QEMU's software emulation (D90)
)

// Entry is one live sandbox.
type Entry struct {
	// ID is "backend:<key>:g<gen>" for a backend of main, the tile's only
	// deployment until it has others; "backend+<name>:<key>:g<gen>" for
	// another deployment's (a name holds no ':'); a session's id.
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	Tile string `json:"tile"` // the component it belongs to (workspace-relative)
	// Deployment is the tile deployment it runs for, set only when that isn't
	// main, so main's entries stay as they were before deployments (P5).
	// Tile is the tile's path whatever the deployment.
	Deployment string `json:"deployment,omitempty"`
	// Parent is the entry a sub-sandbox belongs to (reserved for tile-managed
	// sandboxes, which nest under their owner).
	Parent string `json:"parent,omitempty"`
	User   string `json:"user,omitempty"`  // terminals and agents: whose session
	Label  string `json:"label,omitempty"` // agents: the provider
	// Name is a tile sandbox's name (its address under its tile); For and
	// ForUser are its manager's claims — the consumer tile and the person it
	// runs for — shown as claims, widening nothing (D120).
	Name    string `json:"name,omitempty"`
	For     string `json:"for,omitempty"`
	ForUser string `json:"forUser,omitempty"`
	Mode    Mode   `json:"mode"`
	Accel   Accel  `json:"accel,omitempty"`
	MemMiB  int    `json:"memMiB,omitempty"` // guest memory reserved (VMs)
	VCPUs   int    `json:"vcpus,omitempty"`
	// PID is the sandbox's first process on the host: for a VM, the shim
	// whose child VMM holds the guest's memory.
	PID     int       `json:"pid,omitempty"`
	Gen     int       `json:"gen,omitempty"` // backends: the generation
	Started time.Time `json:"started"`
	// Leaf is its cgroup (as the cgroup manager names it; "" = none): where
	// its memory, CPU and pids are counted — for a VM, guest and VMM alike.
	Leaf       string `json:"leaf,omitempty"`
	Disk       string `json:"disk,omitempty"` // a VM's persistent disk image
	Net        string `json:"net,omitempty"`
	Restricted bool   `json:"restricted,omitempty"` // a restricted user's terminal (D17d caps)
}

// Filter narrows a listing; the zero value is everything.
type Filter struct {
	Tile, User string
	Kind       Kind
	// Deployment narrows to one deployment's entries: "main" matches those
	// without a Deployment; "" matches every deployment's.
	Deployment string
}

// mainDeployment is the name an entry without a Deployment runs for.
const mainDeployment = "main"

func (f Filter) match(tile, dep, user string, kind Kind) bool {
	if dep == "" {
		dep = mainDeployment
	}
	return (f.Tile == "" || f.Tile == tile) && (f.Deployment == "" || f.Deployment == dep) &&
		(f.User == "" || f.User == user) && (f.Kind == "" || f.Kind == kind)
}

// Stage is where a sandbox failed.
type Stage string

const (
	Refused Stage = "refused" // a policy, budget or availability refusal
	Start   Stage = "start"   // the sandbox or VM couldn't be set up or spawned
	Health  Stage = "health"  // a sandboxed backend never became healthy
	Exit    Stage = "exit"    // the sandbox layer itself died (the VM shim, the init)
)

// Failure is one thing the sandbox layer refused or failed at.
type Failure struct {
	Time       time.Time `json:"time"`
	Kind       Kind      `json:"kind"`
	Tile       string    `json:"tile"`
	Deployment string    `json:"deployment,omitempty"` // as on the entry: never set for main
	User       string    `json:"user,omitempty"`
	Mode       Mode      `json:"mode"`
	Stage      Stage     `json:"stage"`
	Error      string    `json:"error"`
	Count      int       `json:"count"` // the same failure, coalesced
}

const (
	ringSize  = 64
	coalesce  = 10 * time.Minute
	errMaxLen = 2000
)

// Registry is the live list and the failure ring.
type Registry struct {
	mu      sync.Mutex
	entries map[string]*Entry
	ring    []Failure // newest first
	counts  map[Stage]int
	now     func() time.Time
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{entries: map[string]*Entry{}, counts: map[Stage]int{}, now: time.Now}
}

// Add lists e until the returned remove is called. Remove is idempotent and
// removes only this entry: a later Add under the same id (a restarted
// session reusing it) survives an old remover.
func (r *Registry) Add(e Entry) (remove func()) {
	if r == nil {
		return func() {}
	}
	if e.Started.IsZero() {
		e.Started = r.now()
	}
	p := &e
	r.mu.Lock()
	r.entries[e.ID] = p
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if r.entries[p.ID] == p {
				delete(r.entries, p.ID)
			}
			r.mu.Unlock()
		})
	}
}

// Set updates a live entry in place (a pid learnt after the add).
func (r *Registry) Set(id string, fn func(*Entry)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.entries[id]; e != nil {
		fn(e)
	}
}

// Get returns a copy of one entry.
func (r *Registry) Get(id string) (Entry, bool) {
	if r == nil {
		return Entry{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.entries[id]; e != nil {
		return *e, true
	}
	return Entry{}, false
}

// List returns copies of the entries f matches, by tile, then start, then id.
func (r *Registry) List(f Filter) []Entry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if f.match(e.Tile, e.Deployment, e.User, e.Kind) {
			out = append(out, *e)
		}
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Tile != b.Tile {
			return a.Tile < b.Tile
		}
		if !a.Started.Equal(b.Started) {
			return a.Started.Before(b.Started)
		}
		return a.ID < b.ID
	})
	return out
}

// Fail records a failure. The same failure (kind, tile, deployment, user,
// mode, stage and message) within ten minutes of the last is counted, not
// repeated — a crash loop is one row, and one deployment's never merges into
// another's — and the ring keeps the newest 64.
func (r *Registry) Fail(f Failure) {
	if r == nil {
		return
	}
	if f.Time.IsZero() {
		f.Time = r.now()
	}
	if len(f.Error) > errMaxLen {
		f.Error = f.Error[:errMaxLen] + "…"
	}
	f.Count = 1
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[f.Stage]++
	for i, x := range r.ring {
		if x.Kind == f.Kind && x.Tile == f.Tile && x.Deployment == f.Deployment && x.User == f.User && x.Mode == f.Mode &&
			x.Stage == f.Stage && x.Error == f.Error && f.Time.Sub(x.Time) < coalesce {
			f.Count = x.Count + 1
			r.ring = append(r.ring[:i], r.ring[i+1:]...)
			break
		}
	}
	r.ring = append([]Failure{f}, r.ring...)
	if len(r.ring) > ringSize {
		r.ring = r.ring[:ringSize]
	}
}

// Failures returns the recorded failures f matches, newest first.
func (r *Registry) Failures(f Filter) []Failure {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Failure{}
	for _, x := range r.ring {
		if f.match(x.Tile, x.Deployment, x.User, x.Kind) {
			out = append(out, x)
		}
	}
	return out
}

// FailureCounts is how many failures of each stage were recorded since boot
// (coalesced ones included).
func (r *Registry) FailureCounts() map[Stage]int {
	out := map[Stage]int{}
	if r == nil {
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range r.counts {
		out[k] = v
	}
	return out
}

// ErrRefused marks a sandbox that wasn't started by policy — switched off, a
// budget spent, VMs unavailable here — rather than one that failed.
var ErrRefused = errors.New("sandbox refused")

type refusal struct{ err error }

func (e refusal) Error() string        { return e.err.Error() }
func (e refusal) Unwrap() []error      { return []error{e.err, ErrRefused} }
func (e refusal) Is(target error) bool { return target == ErrRefused }

// Refuse marks err as a refusal, keeping its message and its chain.
func Refuse(err error) error {
	if err == nil || errors.Is(err, ErrRefused) {
		return err
	}
	return refusal{err}
}

// StageOf is Refused for a refusal, else def.
func StageOf(err error, def Stage) Stage {
	if errors.Is(err, ErrRefused) {
		return Refused
	}
	return def
}
