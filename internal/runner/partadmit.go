package runner

// partadmit.go — how many people's partitions run, and for how long
// (plans/partitions/03 §A.3, §A.5-§A.6; PD-18, PD-35):
//
//   - Caps. User partitions run at most perTile per tile and workspace
//     across the workspace, derived from host memory — clamp(MemTotal/4 ÷ E,
//     4, 32) and clamp(MemTotal/2 ÷ E, 8, 128), E the per-instance estimate
//     — unless an admin or a tile manager set others (PartitionCapsFor). The
//     global instance is a primary and never counts.
//   - Eviction. Past a cap, an interactive start stops the least recently
//     used partition that isn't in use — no active connection or hold, no
//     interactive request in the last 2 minutes; passive streams don't count
//     — of the same tile, then of the workspace. A background start (cron,
//     bus, mail) evicts only one that also holds no passive stream. With no
//     victim, an interactive start is refused (sbx.Refuse, the proxy's 503)
//     and a background one deferred (ErrPartitionDeferred: cron retries,
//     mail rings again).
//   - Background cold starts: at most 4 at once workspace-wide, and mail
//     doorbell starts at most 6 a minute per tile.
//   - The idle stop: a user partition with no active connection stops 10
//     minutes after its last non-passive use; nothing starts one at boot or
//     keeps one up (alwaysOn is the global instance's).
//   - One build: a partitioned tile's work tree is built once per change
//     (a build sequence Changed advances) and every instance started for
//     that change reuses it, so N people's starts after a save never race
//     `go build` onto one output path. Checkpoint artifacts are already
//     shared per (tile, checkpoint).

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
)

const (
	partitionIdleReap     = 10 * time.Minute // a user partition's idle stop (PD-18)
	partitionInUse        = 2 * time.Minute  // an interactive request keeps a partition in use this long
	maxBackgroundStarts   = 4                // background cold starts at once, workspace-wide
	mailStartsPerMinute   = 6                // mail doorbell cold starts per tile per minute
	partitionSwapsPerTile = 4                // blue/green restarts of one tile's partitions at once
	// PartitionInstanceEstimate is E, the memory one partition instance is
	// taken to hold — a placeholder with two gocryptfs processes, until the
	// QA-box measurement of the default agent replaces it (I2).
	PartitionInstanceEstimate = 160 << 20
)

var (
	// ErrPartitionBusy refuses an interactive start past the caps with no
	// partition to evict; it is an sbx refusal, and the proxy's 503.
	ErrPartitionBusy = errors.New("partition caps reached")
	// ErrPartitionDeferred defers a background start (cron, bus, mail)
	// that admission can't take now: the delivery waits and retries.
	ErrPartitionDeferred = errors.New("partition start deferred")
)

// partAdmission is the admission bookkeeping; the zero value is ready.
type partAdmission struct {
	mu       sync.Mutex             // taken before r.mu and any state's mu
	reserved map[string]string      // partStateKey → its tile: a cold start admitted, not yet live
	bg       int                    // background cold starts in flight
	mail     map[string][]time.Time // tile → its mail-started cold starts in the last minute
	memTotal atomic.Int64           // bytes; 0 = read /proc/meminfo (tests set it)
}

// partitionCapsFrom derives the default caps from host memory (03 §A.5).
func partitionCapsFrom(memTotal int64) (perTile, workspace int) {
	clamp := func(n int64, lo, hi int) int { return max(lo, min(hi, int(n))) }
	return clamp(memTotal/4/PartitionInstanceEstimate, 4, 32), clamp(memTotal/2/PartitionInstanceEstimate, 8, 128)
}

var memTotalOnce = sync.OnceValue(func() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			if kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64); err == nil {
				return kb << 10
			}
		}
	}
	return 0
})

// DefaultPartitionCaps are the running caps of user partitions without an
// override: per tile and workspace-wide, from this host's memory.
func (r *Runner) DefaultPartitionCaps() (perTile, workspace int) {
	m := r.parts.adm.memTotal.Load()
	if m == 0 {
		m = memTotalOnce()
	}
	return partitionCapsFrom(m)
}

// partitionCaps are tile's effective caps: the overrides, else the defaults.
func (r *Runner) partitionCaps(tile string) (perTile, workspace int) {
	perTile, workspace = r.DefaultPartitionCaps()
	if f := r.PartitionCapsFor; f != nil {
		t, w := f(tile)
		if t > 0 {
			perTile = t
		}
		if w > 0 {
			workspace = w
		}
	}
	return perTile, workspace
}

// admitPartition admits a cold start of the user partition at key (03
// §A.5): the background limits, then the caps, evicting what the class
// may. It reserves key until the returned release, so concurrent starts
// count each other.
func (r *Runner) admitPartition(tile, key string, class StartClass) (func(), error) {
	a := &r.parts.adm
	a.mu.Lock()
	defer a.mu.Unlock()
	now := r.now()
	if class != StartInteractive {
		if a.bg >= maxBackgroundStarts {
			return nil, fmt.Errorf("%w: %d background starts of people's partitions already run", ErrPartitionDeferred, a.bg)
		}
		if class == StartMail {
			recent := a.mail[tile][:0]
			for _, t := range a.mail[tile] {
				if now.Sub(t) < time.Minute {
					recent = append(recent, t)
				}
			}
			if a.mail == nil {
				a.mail = map[string][]time.Time{}
			}
			if a.mail[tile] = recent; len(recent) >= mailStartsPerMinute {
				return nil, fmt.Errorf("%w: %s's mail started %d partitions in the last minute", ErrPartitionDeferred, tile, len(recent))
			}
		}
	}
	perTile, workspace := r.partitionCaps(tile)
	for {
		tileN, wsN, victim := r.partitionLoad(tile, key, now, class, perTile, a.reserved)
		if tileN < perTile && wsN < workspace {
			break
		}
		if victim == nil {
			if class != StartInteractive {
				return nil, fmt.Errorf("%w: people's partitions are at their cap (%d of %s, %d in the workspace)", ErrPartitionDeferred, tileN, tile, wsN)
			}
			return nil, sbx.Refuse(fmt.Errorf("too many people's instances of %s are running; try again shortly: %w", tile, ErrPartitionBusy))
		}
		slog.Info("partition evicted", "component", victim.comp, "for", tile, "class", class)
		r.stopPart(victim, false)
	}
	if a.reserved == nil {
		a.reserved = map[string]string{}
	}
	a.reserved[key] = tile
	if class != StartInteractive {
		a.bg++
	}
	if class == StartMail {
		a.mail[tile] = append(a.mail[tile], now)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			delete(a.reserved, key)
			if class != StartInteractive {
				a.bg--
			}
			a.mu.Unlock()
		})
	}, nil
}

// partitionLoad counts the user partitions running, building or reserved,
// of tile and of the workspace, leaving out self, and picks the victim a
// start of class may evict when a cap is reached: the least recently used
// one not in use (nor, for a background start, streaming), of tile if
// there is one — the only kind that frees a place under tile's cap —
// else of the workspace, unless tile's cap is the one reached.
func (r *Runner) partitionLoad(tile, self string, now time.Time, class StartClass, perTile int, reserved map[string]string) (tileN, wsN int, victim *state) {
	type cand struct {
		s    *state
		last time.Time
	}
	var mine, others []cand
	for _, s := range r.partStates("") {
		if partStateKey(s.comp, s.dep, s.pt.pkey) == self {
			continue
		}
		s.mu.Lock()
		live := s.live()
		evictable := s.cur != nil && !s.building && s.active == 0 && now.Sub(s.pt.lastInteractive) >= partitionInUse &&
			(class == StartInteractive || s.pt.passive == 0)
		last := s.lastReq
		s.mu.Unlock()
		if !live {
			continue
		}
		wsN++
		if s.comp == tile {
			tileN++
		}
		switch {
		case !evictable:
		case s.comp == tile:
			mine = append(mine, cand{s, last})
		default:
			others = append(others, cand{s, last})
		}
	}
	for k, t := range reserved {
		if k != self {
			wsN++
			if t == tile {
				tileN++
			}
		}
	}
	lru := func(cs []cand) *state {
		if len(cs) == 0 {
			return nil
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].last.Before(cs[j].last) })
		return cs[0].s
	}
	if victim = lru(mine); victim == nil && tileN < perTile {
		victim = lru(others)
	}
	return tileN, wsN, victim
}

// reapPartitions stops every user partition idle for partitionIdleReap: a
// running generation with no active connection and no non-passive use
// since (03 §A.6). Its state stays, dirty: the next request starts it.
func (r *Runner) reapPartitions() {
	now := r.now()
	for _, s := range r.partStates("") {
		s.mu.Lock()
		inst := s.cur
		if inst == nil || s.building || s.active > 0 || now.Sub(s.lastReq) <= partitionIdleReap {
			s.mu.Unlock()
			continue
		}
		s.cur = nil
		s.dirty = true
		s.mu.Unlock()
		slog.Info("reaping idle partition", "component", s.comp, "deployment", s.dep)
		if r.Auth != nil {
			r.Auth.RevokeInstance(inst.token)
		}
		go r.stopGen(inst, 5*time.Second)
	}
}

// ---- one build per change (03 §A.3) ----

// partBuilds is the shared work-tree build of partitioned tiles.
type partBuilds struct {
	mu      sync.Mutex
	seq     map[string]uint64       // tile → its build sequence, advanced by every change
	flights map[string]*buildFlight // stateKey(tile, dep) → the build of the current sequence
}

// buildFlight is one shared build.
type buildFlight struct {
	seq  uint64
	done chan struct{}
	bin  string
	err  error
}

// nextBuild advances tile's build sequence: the next generation of any of
// its instances builds anew (Changed, a work-tree deploy).
func (r *Runner) nextBuild(tile string) {
	b := &r.parts.builds
	b.mu.Lock()
	if b.seq == nil {
		b.seq = map[string]uint64{}
	}
	b.seq[tile]++
	b.mu.Unlock()
}

// buildWorkTree builds the work tree of deployment dep of c from view v:
// the primary's through the tile's shared build while it runs user
// partitions (the first start after a change builds, the others wait for
// and reuse its bin, its error included), anything else through buildGen,
// today's.
func (r *Runner) buildWorkTree(c *registry.Component, dep string, v *registry.Component) (string, error) {
	if _, ok := r.partitionSpec(c.Path); !ok || dep != r.primary(c.Path) {
		return r.buildGen(v)
	}
	b, k := &r.parts.builds, stateKey(c.Path, dep)
	b.mu.Lock()
	seq := b.seq[c.Path]
	if f := b.flights[k]; f != nil && f.seq == seq {
		b.mu.Unlock()
		<-f.done
		return f.bin, f.err
	}
	f := &buildFlight{seq: seq, done: make(chan struct{})}
	if b.flights == nil {
		b.flights = map[string]*buildFlight{}
	}
	b.flights[k] = f
	b.mu.Unlock()
	f.bin, f.err = r.buildGen(v)
	close(f.done)
	return f.bin, f.err
}

// followPrimary is told a deploy or restart of deployment dep of c ended
// with err: a successful one on a partitioned tile's primary moves every
// person's partition onto what it runs now (changedPartitions).
func (r *Runner) followPrimary(c *registry.Component, dep string, err error) {
	if err != nil || dep != r.primary(c.Path) {
		return
	}
	if _, ok := r.partitionSpec(c.Path); ok {
		r.changedPartitions(c)
	}
}
