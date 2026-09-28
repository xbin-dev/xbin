package tilesbx

// policy.go — the sandboxes policy (.xbin/sandboxes/policy.json): the
// workspace admin's kill switch and the quotas every manager tile is held
// to, with per-tile overrides. Zero means "the default", so an editor that
// saves the stored policy back keeps following the defaults.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/fsutil"
)

// PerTile caps what one tile holds across its sandboxes.
type PerTile struct {
	Max     int `json:"max,omitempty"`     // definitions
	Running int `json:"running,omitempty"` // running at once
	MemMiB  int `json:"memMiB,omitempty"`  // memory of the running ones
	VCPUs   int `json:"vcpus,omitempty"`   // vCPUs of the running ones
	DiskGiB int `json:"diskGiB,omitempty"` // disk, snapshots included
}

// PerSandbox is one sandbox's defaults and caps.
type PerSandbox struct {
	MemMiB     int `json:"memMiB,omitempty"`  // default
	VCPUs      int `json:"vcpus,omitempty"`   // default
	DiskGiB    int `json:"diskGiB,omitempty"` // default
	MaxMemMiB  int `json:"maxMemMiB,omitempty"`
	MaxVCPUs   int `json:"maxVCPUs,omitempty"`
	MaxDiskGiB int `json:"maxDiskGiB,omitempty"`
	Pids       int `json:"pids,omitempty"` // a namespace sandbox's pids.max
}

// Limits is the part of the policy an override may change per tile.
type Limits struct {
	PerTile         PerTile    `json:"perTile"`
	PerSandbox      PerSandbox `json:"perSandbox"`
	IdleStopMin     int        `json:"idleStopMin,omitempty"`     // ≤ 1440
	OutputRingMiB   int        `json:"outputRingMiB,omitempty"`   // one exec's output ring, ≤ 8
	OutputBudgetMiB int        `json:"outputBudgetMiB,omitempty"` // a tile's rings together
}

// Total caps every tile sandbox together: the comp-tilesbx cgroup parent
// that holds each sandbox's leaf (§6.2). It is the workspace's alone: no
// override changes it.
type Total struct {
	MemMiB int `json:"memMiB,omitempty"` // 0 = ¾ of the host's RAM
	Pids   int `json:"pids,omitempty"`   // 0 = 32768
}

// Policy is the sandboxes policy. Enabled nil = on (the default).
type Policy struct {
	Enabled *bool `json:"enabled,omitempty"`
	Limits
	Total     Total              `json:"total"`
	Overrides map[string]*Limits `json:"overrides,omitempty"` // by tile path
}

// defaultLimits are D120's (D113 §6).
var defaultLimits = Limits{
	PerTile:         PerTile{Max: 8, Running: 4, MemMiB: 8192, VCPUs: 8, DiskGiB: 100},
	PerSandbox:      PerSandbox{MemMiB: 2048, VCPUs: 2, DiskGiB: 20, MaxMemMiB: 8192, MaxVCPUs: 8, MaxDiskGiB: 200, Pids: 4096},
	IdleStopMin:     30,
	OutputRingMiB:   1,
	OutputBudgetMiB: 64,
}

// The fixed limits every manager sees in GET /sandboxes/runtime.
const (
	runTimeoutMaxMs = 600000
	runOutputMax    = 1 << 20
	execsRunningMax = 16
	stdinMax        = 1 << 20
	fileMax         = 64 << 20
	tarMax          = 1 << 30
	waitMaxSec      = 120
	defBodyMax      = 64 << 10 // a definition, a PATCH, the policy
	minMemMiB       = 256
	flowsTCP        = 1024  // a sandbox's concurrent relay TCP flows (the relay's MaxTCP)
	flowsUDP        = 256   // … and UDP flows (MaxUDP)
	totalPids       = 32768 // policy.total.pids's default
)

// over lays o's set (non-zero) fields over l.
func (l Limits) over(o Limits) Limits {
	pick := func(dst *int, v int) {
		if v != 0 {
			*dst = v
		}
	}
	pick(&l.PerTile.Max, o.PerTile.Max)
	pick(&l.PerTile.Running, o.PerTile.Running)
	pick(&l.PerTile.MemMiB, o.PerTile.MemMiB)
	pick(&l.PerTile.VCPUs, o.PerTile.VCPUs)
	pick(&l.PerTile.DiskGiB, o.PerTile.DiskGiB)
	pick(&l.PerSandbox.MemMiB, o.PerSandbox.MemMiB)
	pick(&l.PerSandbox.VCPUs, o.PerSandbox.VCPUs)
	pick(&l.PerSandbox.DiskGiB, o.PerSandbox.DiskGiB)
	pick(&l.PerSandbox.MaxMemMiB, o.PerSandbox.MaxMemMiB)
	pick(&l.PerSandbox.MaxVCPUs, o.PerSandbox.MaxVCPUs)
	pick(&l.PerSandbox.MaxDiskGiB, o.PerSandbox.MaxDiskGiB)
	pick(&l.PerSandbox.Pids, o.PerSandbox.Pids)
	pick(&l.IdleStopMin, o.IdleStopMin)
	pick(&l.OutputRingMiB, o.OutputRingMiB)
	pick(&l.OutputBudgetMiB, o.OutputBudgetMiB)
	return l
}

// withDefaults fills the zero fields from the defaults.
func (l Limits) withDefaults() Limits { return defaultLimits.over(l) }

// On reports the kill switch.
func (p Policy) On() bool { return p.Enabled == nil || *p.Enabled }

// Effective is the workspace's policy with the defaults filled (the
// overrides as stored).
func (p Policy) Effective() Policy {
	on := p.On()
	out := Policy{Enabled: &on, Limits: p.Limits.withDefaults(), Total: p.Total.withDefaults(), Overrides: p.Overrides}
	return out
}

// withDefaults fills pids; memMiB 0 stays 0 (¾ of the host's RAM, which
// the cgroup parent resolves).
func (t Total) withDefaults() Total {
	if t.Pids == 0 {
		t.Pids = totalPids
	}
	return t
}

func (t Total) validate() error {
	switch {
	case t.MemMiB < 0 || t.MemMiB > 1<<24:
		return fmt.Errorf("total.memMiB must be between 0 and %d (0 = ¾ of the host's RAM)", 1<<24)
	case t.MemMiB != 0 && t.MemMiB < minMemMiB:
		return fmt.Errorf("total.memMiB must be at least %d MiB", minMemMiB)
	case t.Pids < 0 || t.Pids > 1<<22:
		return fmt.Errorf("total.pids must be between 0 and %d (0 = the default)", 1<<22)
	case t.Pids != 0 && t.Pids < 64:
		return fmt.Errorf("total.pids must be at least 64")
	}
	return nil
}

// For is what one tile is held to: the workspace's limits, its override on
// top, the defaults under both.
func (p Policy) For(tile string) Limits {
	l := p.Limits
	if o := p.Overrides[tile]; o != nil {
		l = l.over(*o)
	}
	return l.withDefaults()
}

// Validate refuses out-of-range values and a default above its cap, for
// the workspace and for every override.
func (p Policy) Validate() error {
	if err := p.Limits.validate(); err != nil {
		return err
	}
	if err := p.Total.validate(); err != nil {
		return err
	}
	if err := p.Limits.withDefaults().consistent(); err != nil {
		return err
	}
	for tile, o := range p.Overrides {
		if !validTilePath(tile) {
			return fmt.Errorf("overrides: %q isn't a tile path", tile)
		}
		if o == nil {
			continue
		}
		if err := o.validate(); err != nil {
			return fmt.Errorf("overrides[%s]: %w", tile, err)
		}
		if err := p.For(tile).consistent(); err != nil {
			return fmt.Errorf("overrides[%s]: %w", tile, err)
		}
	}
	return nil
}

func (l Limits) validate() error {
	for _, c := range []struct {
		name     string
		v, lo, h int
	}{
		{"perTile.max", l.PerTile.Max, 0, 1024},
		{"perTile.running", l.PerTile.Running, 0, 256},
		{"perTile.memMiB", l.PerTile.MemMiB, 0, 1 << 22},
		{"perTile.vcpus", l.PerTile.VCPUs, 0, 1024},
		{"perTile.diskGiB", l.PerTile.DiskGiB, 0, 1 << 20},
		{"perSandbox.memMiB", l.PerSandbox.MemMiB, 0, 1 << 20},
		{"perSandbox.vcpus", l.PerSandbox.VCPUs, 0, 256},
		{"perSandbox.diskGiB", l.PerSandbox.DiskGiB, 0, 65536},
		{"perSandbox.maxMemMiB", l.PerSandbox.MaxMemMiB, 0, 1 << 20},
		{"perSandbox.maxVCPUs", l.PerSandbox.MaxVCPUs, 0, 256},
		{"perSandbox.maxDiskGiB", l.PerSandbox.MaxDiskGiB, 0, 65536},
		{"perSandbox.pids", l.PerSandbox.Pids, 0, 1 << 22},
		{"idleStopMin", l.IdleStopMin, 0, 1440},
		{"outputRingMiB", l.OutputRingMiB, 0, 8},
		{"outputBudgetMiB", l.OutputBudgetMiB, 0, 4096},
	} {
		if c.v < c.lo || c.v > c.h {
			return fmt.Errorf("%s must be between %d and %d (0 = the default)", c.name, c.lo, c.h)
		}
	}
	for _, c := range []struct {
		name string
		v    int
	}{{"perSandbox.memMiB", l.PerSandbox.MemMiB}, {"perSandbox.maxMemMiB", l.PerSandbox.MaxMemMiB}} {
		if c.v != 0 && c.v < minMemMiB {
			return fmt.Errorf("%s must be at least %d MiB", c.name, minMemMiB)
		}
	}
	if l.PerSandbox.Pids != 0 && l.PerSandbox.Pids < 64 {
		return fmt.Errorf("perSandbox.pids must be at least 64")
	}
	return nil
}

// consistent checks a filled Limits: a default fits its cap, a ring its budget.
func (l Limits) consistent() error {
	ps := l.PerSandbox
	switch {
	case ps.MemMiB > ps.MaxMemMiB:
		return fmt.Errorf("perSandbox.memMiB (%d) is above perSandbox.maxMemMiB (%d)", ps.MemMiB, ps.MaxMemMiB)
	case ps.VCPUs > ps.MaxVCPUs:
		return fmt.Errorf("perSandbox.vcpus (%d) is above perSandbox.maxVCPUs (%d)", ps.VCPUs, ps.MaxVCPUs)
	case ps.DiskGiB > ps.MaxDiskGiB:
		return fmt.Errorf("perSandbox.diskGiB (%d) is above perSandbox.maxDiskGiB (%d)", ps.DiskGiB, ps.MaxDiskGiB)
	case l.OutputRingMiB > l.OutputBudgetMiB:
		return fmt.Errorf("outputRingMiB (%d) is above outputBudgetMiB (%d)", l.OutputRingMiB, l.OutputBudgetMiB)
	}
	return nil
}

// validTilePath: a clean, relative, workspace path.
func validTilePath(p string) bool {
	return p != "" && p != "." && path.Clean(p) == p && !strings.HasPrefix(p, "/") &&
		p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\x00\\")
}

// mergePolicy lays a partial policy (a PUT body) over the stored one: the
// fields the body names change, the rest stay. An override the body names
// replaces that tile's; null removes it.
func mergePolicy(stored Policy, body []byte) (Policy, error) {
	b, err := json.Marshal(stored)
	if err != nil {
		return Policy{}, err
	}
	var out Policy // a deep copy: nothing aliases stored
	if err := json.Unmarshal(b, &out); err != nil {
		return Policy{}, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Policy{}, err
	}
	for tile, o := range out.Overrides {
		if o == nil {
			delete(out.Overrides, tile)
		}
	}
	if len(out.Overrides) == 0 {
		out.Overrides = nil
	}
	return out, nil
}

func (m *Manager) policyPath() string {
	return filepath.Join(m.root, ".xbin", "sandboxes", "policy.json")
}

// policyStore is the policy file, read once and written through.
//
// An unreadable file fails closed (§3.2): its error is logged once at
// load, shown in GET /sandboxes/policy (error) and the admin's health, and
// while it stands tile sandboxes are off — no start is admitted — whatever
// the file may have said, so a corrupt file never undoes an admin's kill
// switch. The limits meanwhile are the defaults. An admin's PUT writes a
// new file (merged onto the defaults) and clears the error.
type policyStore struct {
	path   string
	loaded bool
	p      Policy
	err    error // unreadable: tile sandboxes are off until a PUT replaces it
}

// get is the stored policy (zero while the file is unreadable). Callers
// hold m.mu.
func (s *policyStore) get() Policy {
	if !s.loaded {
		s.loaded = true
		b, err := os.ReadFile(s.path)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			s.err = err
		default:
			if err := json.Unmarshal(b, &s.p); err != nil {
				s.err, s.p = fmt.Errorf("%s: %w", s.path, err), Policy{}
			}
		}
		if s.err != nil {
			slog.Error("tile sandboxes: the sandboxes policy file is unreadable; tile sandboxes are off until an admin saves the policy again", "err", s.err)
		}
	}
	return s.p
}

// on reports whether tile sandboxes may start now, and why not: the kill
// switch, or a policy file that can't be read. Callers hold m.mu.
func (s *policyStore) on() (bool, string) {
	p := s.get()
	switch {
	case s.err != nil:
		return false, "the sandboxes policy file is unreadable (" + s.err.Error() + "): tile sandboxes are off until an admin saves the policy again"
	case !p.On():
		return false, "tile sandboxes are switched off (sandboxes policy: enabled)"
	}
	return true, ""
}

// effective is the policy in force: the stored one with the defaults
// filled, and enabled false while the file can't be read. Callers hold m.mu.
func (s *policyStore) effective() Policy {
	e := s.get().Effective()
	if s.err != nil {
		off := false
		e.Enabled = &off
	}
	return e
}

// set validates and stores p. Callers hold m.mu.
func (s *policyStore) set(p Policy) error {
	if err := p.Validate(); err != nil {
		return refuse(RefInvalid, "%v", err)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(s.path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	s.p, s.loaded, s.err = p, true, nil
	return nil
}

// Policy is the stored policy (zero = default).
func (m *Manager) Policy() Policy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.policy.get()
}

// PolicyError is why the policy file can't be read ("" = it can): while
// it can't, tile sandboxes are off (the admin's health view).
func (m *Manager) PolicyError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy.get()
	if m.policy.err != nil {
		return m.policy.err.Error()
	}
	return ""
}

// limitsFor is what tile is held to now. Callers hold m.mu.
func (m *Manager) limitsFor(tile string) Limits { return m.policy.get().For(tile) }
