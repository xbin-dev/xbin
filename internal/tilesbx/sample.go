package tilesbx

// sample.go — a running tile sandbox's CPU and memory (D215). Once a
// minute one pass reads the cgroup leaf of every running sandbox
// (cgroup.Usage: cpu.stat usage_usec, memory.current, memory.max,
// pids.current — four small files each, no walk) and keeps the last
// samplePoints of each run in memory: a stop, or xbind's restart, starts
// its series over. The pass is a time.AfterFunc chain, as usage.go's are —
// no ticker: a sandbox coming up arms it, and it re-arms only while a
// sandbox with a leaf runs. Without cgroup delegation there is no leaf,
// nothing is sampled and SandboxInfo carries no usage. Lock order is
// usage.go's: m.mu before m.usage.mu, and the pass holds neither while it
// reads the leaves.
//
// cpuPct is the percent of one core over the interval since the run's
// previous sample; its first sample's interval begins at its start (its
// leaf is made fresh for each run, cgroup.Prepare). SandboxInfo.usage is
// the latest sample; a GET of one sandbox adds the series, oldest first.

import (
	"math"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
)

const (
	sampleEvery  = time.Minute // one pass over the running sandboxes
	samplePoints = 60          // a run's series: its last hour
)

// LiveUsage is a running sandbox's latest sample (SandboxInfo.usage).
type LiveUsage struct {
	At          int64        `json:"at"`                    // unix ms
	CPUPct      float64      `json:"cpuPct"`                // percent of one core over the last interval, to a tenth
	MemBytes    int64        `json:"memBytes"`              // memory.current: its processes, its agent's and fuse-overlayfs's (a VM: its VMM's)
	MemMaxBytes int64        `json:"memMaxBytes,omitempty"` // memory.max: memMiB plus that overhead (absent: no cap)
	Pids        int64        `json:"pids"`                  // pids.current (a VM: its VMM's, not the guest's)
	Series      []UsagePoint `json:"series,omitempty"`      // GET of one sandbox: the last samplePoints, oldest first
}

// UsagePoint is one sample of LiveUsage.Series.
type UsagePoint struct {
	T   int64   `json:"t"`   // unix ms
	CPU float64 `json:"cpu"` // as cpuPct
	Mem int64   `json:"mem"` // as memBytes
}

// liveStats is one run's samples, under m.mu.
type liveStats struct {
	last     LiveUsage    // the latest (At 0: none yet)
	baseAt   time.Time    // the latest reading's time and usage_usec: the next cpuPct's base (zero: the run's start, from 0)
	baseUsec int64        //
	series   []UsagePoint // at most samplePoints, oldest first
}

// add records a reading of the run's leaf taken at now; started is when the
// run started (the first interval's beginning). A reading within a second
// of the last says nothing a minute's doesn't: it is dropped.
func (s *liveStats) add(now, started time.Time, u cgroup.Usage) {
	base := s.baseAt
	if base.IsZero() {
		base = started
	}
	dt := now.Sub(base)
	if dt < time.Second {
		return
	}
	pct := cpuPct(s.baseUsec, u.CPUUsec, dt)
	s.baseAt, s.baseUsec = now, u.CPUUsec
	s.last = LiveUsage{At: now.UnixMilli(), CPUPct: pct, MemBytes: u.MemCurrent, MemMaxBytes: max(u.MemMax, 0), Pids: u.PidsCurrent}
	p := UsagePoint{T: s.last.At, CPU: pct, Mem: u.MemCurrent}
	if len(s.series) < samplePoints {
		s.series = append(s.series, p)
		return
	}
	copy(s.series, s.series[1:])
	s.series[len(s.series)-1] = p
}

// view is the latest sample, with the series when asked (nil: none yet).
func (s *liveStats) view(series bool) *LiveUsage {
	if s.last.At == 0 {
		return nil
	}
	u := s.last
	if series {
		u.Series = append([]UsagePoint(nil), s.series...)
	}
	return &u
}

// cpuPct is the percent of one core that usage_usec going from prev to cur
// over dt is, to a tenth. A counter that went back (never, within one
// leaf) reads 0.
func cpuPct(prev, cur int64, dt time.Duration) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return math.Round(float64(cur-prev)/float64(dt.Microseconds())*1000) / 10
}

// usageLocked is k's sandbox's latest sample while it runs (nil: it doesn't,
// or nothing was sampled yet). Callers hold m.mu.
func (m *Manager) usageLocked(k Key, name string, series bool) *LiveUsage {
	if b := m.live[k][name]; b != nil && b.run != nil && live(b.state) {
		return b.run.stats.view(series)
	}
	return nil
}

// sampling reports whether leaves can be read: the sandboxes' cgroup
// parent is there (delegation), or a test reads a fake directory.
func (m *Manager) sampling() bool { return m.cg.Enabled() || m.leafStats != nil }

// readLeaf is a leaf's accounting now.
func (m *Manager) readLeaf(leaf string) (cgroup.Usage, bool) {
	if f := m.leafStats; f != nil {
		return f(leaf)
	}
	return m.cg.Usage(leaf)
}

// armSampler arms the next pass unless one is armed: a sandbox came up, or
// the pass found one running.
func (m *Manager) armSampler() {
	if !m.sampling() {
		return
	}
	u := &m.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.sample == nil && !u.closed {
		u.sample = m.afterFunc(sampleEvery, m.samplePass)
	}
}

// samplePass is one pass: every running sandbox with a leaf is read, with
// no lock held, and the reading recorded while the run is still its box's
// (one that ended meanwhile took its samples with it). It re-arms while a
// sandbox runs, and ends with the last; the next to come up arms it again.
func (m *Manager) samplePass() {
	u := &m.usage
	u.mu.Lock()
	u.sample = nil
	u.mu.Unlock()
	m.mu.Lock()
	var runs []*run
	for _, boxes := range m.live {
		for _, b := range boxes {
			if r := b.run; r != nil && b.state == StateRunning && r.leaf != "" {
				runs = append(runs, r)
			}
		}
	}
	m.mu.Unlock()
	if len(runs) == 0 {
		return
	}
	now := m.now()
	reads := make([]cgroup.Usage, len(runs))
	ok := make([]bool, len(runs))
	for i, r := range runs {
		reads[i], ok[i] = m.readLeaf(r.leaf)
	}
	m.mu.Lock()
	for i, r := range runs {
		if ok[i] && r.b.run == r {
			r.stats.add(now, r.started, reads[i])
		}
	}
	m.mu.Unlock()
	m.armSampler()
}
