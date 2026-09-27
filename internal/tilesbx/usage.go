package tilesbx

// usage.go — the disk tile sandboxes hold (plans/tile-sandbox-runtime.md
// §6.3, §9). A namespace upper has no hard cap, so it is measured: its
// state dir (cur/ and its snapshots) by a confined du (confine.DiskUsage:
// the upper is sandbox-written, and xbind never walks one) at each stop,
// and while it runs by one measuring worker that takes the running sandbox
// measured longest ago — one du at a time, never the same sandbox twice
// within measureGap, a time.AfterFunc chain, no ticker. A VM's disks are
// counted by their allocated blocks (a stat, nothing read). After a
// restart every sandbox is measured once.
//
// What is measured feeds admission's per-tile disk cap (admission.go), the
// stop of a tile's largest running namespace sandbox when its tile passes
// perTile.diskGiB while running, the admin's diskBytes, offload's refusal,
// removed tiles' leftovers (workspace.go) and diskmon's disk pressure —
// never a scope's resource-write quota.
//
// The workspace disk: while a namespace sandbox runs, the runtime statfs's
// the workspace partition every diskEvery against diskmon's reserve (cheap:
// no walk), and diskmon's own low-disk verdict (its 45 s scan) calls
// OnLowDisk too. While the disk is low no tile sandbox starts (503), and
// the running namespace sandboxes of every tile above diskmon's fair share
// are synced and stopped, largest tile first: stopping frees nothing, but
// it stops the writer. VM sandboxes run on — their disks are bounded.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

const (
	measureGap   = 2 * time.Minute  // a running sandbox is measured again at most this often
	measureLimit = 30 * time.Minute // one measurement's bound
	diskEvery    = 5 * time.Second  // the workspace partition's statfs while a namespace sandbox runs
)

// usageState is the measuring worker's and the disk watch's. Lock order:
// m.mu before mu, never the other way round.
type usageState struct {
	du     func(ctx context.Context, dir string) (int64, error) // confine.DiskUsage
	statfs func(dir string) (free, total int64)

	mu      sync.Mutex
	working bool          // the worker runs
	next    stopper       // the worker's next round, while a sandbox runs (nil: none armed)
	soon    []usageRef    // measured ahead of the rotation: stopped just now, reset, never measured
	idle    chan struct{} // closed when the worker ends (tests)
	watch   stopper       // the statfs watch (nil: none armed)
	lowPass bool          // a low-disk stop pass runs
	closed  bool          // xbind is shutting down: nothing more is measured
}

// usageRef names a sandbox to measure; its uid tells a re-created name apart.
type usageRef struct {
	k    Key
	name string
	uid  string
}

// statfs is the partition's free (to an unprivileged writer) and total
// bytes; 0, 0 when it can't be read.
func statfs(dir string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize)
}

// measureAll queues every definition with a state dir for a measurement:
// the boot's, when nothing has been measured since the restart.
func (m *Manager) measureAll() {
	m.mu.Lock()
	var refs []usageRef
	for _, tile := range m.defs.tiles() {
		k := Key{Tile: tile}
		for _, d := range m.defs.list(k) {
			dir, err := m.StateDir(k, d)
			if err != nil {
				continue
			}
			if _, err := os.Lstat(dir); err != nil {
				continue // never started: nothing on disk
			}
			m.boxLocked(k, d.Name)
			refs = append(refs, usageRef{k, d.Name, d.UID})
		}
	}
	m.mu.Unlock()
	if len(refs) == 0 {
		return
	}
	m.usage.mu.Lock()
	m.usage.soon = append(m.usage.soon, refs...)
	m.usage.mu.Unlock()
	m.kickUsage()
}

// measureSoon measures k's sandbox d ahead of the rotation: it just
// stopped (§6.3: at each stop) or was reset.
func (m *Manager) measureSoon(k Key, d *Def) {
	if !m.isolated {
		return
	}
	m.usage.mu.Lock()
	m.usage.soon = append(m.usage.soon, usageRef{k, d.Name, d.UID})
	m.usage.mu.Unlock()
	m.kickUsage()
}

// kickUsage runs the measuring worker unless it runs.
func (m *Manager) kickUsage() {
	u := &m.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.working || u.closed {
		return
	}
	if u.next != nil {
		u.next.Stop()
		u.next = nil
	}
	u.working = true
	u.idle = make(chan struct{})
	go m.usageWork()
}

func (m *Manager) usageWork() {
	for {
		ref, ok := m.nextMeasure()
		if !ok {
			return
		}
		m.measure(ref)
	}
}

// nextMeasure picks what the worker measures next: what is queued ahead,
// else the running sandbox measured longest ago, at least measureGap ago.
// With neither, the worker ends — and while a sandbox runs, arms itself
// for when the next one falls due. It decides under both locks, so a
// measurement queued meanwhile is either picked or starts a worker anew.
func (m *Manager) nextMeasure() (usageRef, bool) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	u := &m.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	for len(u.soon) > 0 && !u.closed {
		ref := u.soon[0]
		u.soon = u.soon[1:]
		if m.live[ref.k][ref.name] != nil {
			return ref, true
		}
	}
	var pick *box
	var ref usageRef
	due := time.Duration(-1)
	for _, boxes := range m.live {
		for _, b := range boxes {
			r := b.run
			if r == nil || b.state != StateRunning {
				continue
			}
			if wait := time.UnixMilli(b.measured).Add(measureGap).Sub(now); wait > 0 {
				if due < 0 || wait < due {
					due = wait
				}
				continue
			}
			if pick == nil || b.measured < pick.measured {
				pick, ref = b, usageRef{r.k, r.def.Name, r.def.UID}
			}
		}
	}
	if pick != nil && !u.closed {
		return ref, true
	}
	u.working = false
	close(u.idle)
	if due > 0 && !u.closed {
		u.next = m.afterFunc(due, m.kickUsage)
	}
	return usageRef{}, false
}

// closeUsage ends the measuring: xbind is shutting down, and a confined
// du started now would only outlive it. What the worker measures now it
// finishes.
func (m *Manager) closeUsage() {
	u := &m.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	if u.next != nil {
		u.next.Stop()
		u.next = nil
	}
}

// waitUsage waits until the measuring worker has nothing left (tests).
func (m *Manager) waitUsage() {
	m.usage.mu.Lock()
	idle, working := m.usage.idle, m.usage.working
	m.usage.mu.Unlock()
	if working {
		<-idle
	}
}

// measure measures one sandbox's state and records it; measured while it
// runs, its tile's sandboxes over perTile.diskGiB stop the largest running.
func (m *Manager) measure(ref usageRef) {
	m.mu.Lock()
	d, ok := m.defs.get(ref.k, ref.name)
	b := m.live[ref.k][ref.name]
	m.mu.Unlock()
	if !ok || b == nil || d.UID != ref.uid {
		return
	}
	n, err := m.measureState(ref.k, d)
	m.mu.Lock()
	if m.live[ref.k][ref.name] != b {
		m.mu.Unlock()
		return // deleted meanwhile: its state dir went to .trash under the du, which then fails — nothing to say
	}
	if err != nil {
		slog.Warn("tile sandbox: measuring its disk", "tile", ref.k.Tile, "sandbox", ref.name, "err", err)
	}
	b.measured = m.now().UnixMilli() // a failure waits measureGap too
	if err == nil {
		b.diskBytes = n
	}
	// measured while running and over: a writer is stopped (a stop's own
	// measurement stops nothing — the next running one's does)
	over := b.run != nil && b.state == StateRunning &&
		m.tileDiskBytesLocked(ref.k.Tile) > int64(m.limitsFor(ref.k.Tile).PerTile.DiskGiB)<<30
	m.mu.Unlock()
	if over {
		m.overDiskStop(ref.k.Tile)
	}
}

// measureState is the disk k's sandbox d takes: a namespace sandbox's
// state dir by a confined du, a VM's disks by their allocated blocks.
func (m *Manager) measureState(k Key, d *Def) (int64, error) {
	dir, err := m.StateDir(k, d)
	if err != nil {
		return 0, err
	}
	if d.Mode == ModeVM {
		return vmDiskBytes(dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), measureLimit)
	defer cancel()
	return m.usage.du(ctx, dir)
}

// vmDiskBytes is what a VM sandbox's disks take on the host: the allocated
// blocks of cur/vm/disk.img and of each snapshot's, lstat'ed — never
// followed, opened or parsed (§8.3). A missing disk is 0.
func vmDiskBytes(dir string) (int64, error) {
	paths := []string{filepath.Join(dir, "cur", "vm", "disk.img")}
	snaps, _ := os.ReadDir(filepath.Join(dir, "snapshots"))
	for _, s := range snaps {
		if s.IsDir() {
			paths = append(paths, filepath.Join(dir, "snapshots", s.Name(), "vm", "disk.img"))
		}
	}
	var n int64
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return n, err
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && fi.Mode().IsRegular() {
			n += int64(st.Blocks) * 512
		}
	}
	return n, nil
}

// overDiskStop stops tile's largest running namespace sandbox, state kept
// (§6.3): the tile's sandboxes passed perTile.diskGiB while running. The
// next measurement looks again.
func (m *Manager) overDiskStop(tile string) {
	m.mu.Lock()
	var big *run
	for k, boxes := range m.live {
		if k.Tile != tile {
			continue
		}
		for _, b := range boxes {
			if r := b.run; r != nil && b.state == StateRunning && r.def.Mode == ModeNamespace && (big == nil || b.diskBytes > big.b.diskBytes) {
				big = r
			}
		}
	}
	used, capGiB := m.tileDiskBytesLocked(tile), m.limitsFor(tile).PerTile.DiskGiB
	m.mu.Unlock()
	if big == nil {
		return
	}
	why := fmt.Sprintf("the tile's sandboxes use %s, over its %d GiB (sandboxes policy: perTile.diskGiB): stopped, state kept — delete a sandbox or a snapshot", gib(used), capGiB)
	go func() { _ = m.Stop(big.k, big.def.Name, why) }()
}

// Usage is each tile's sandbox bytes as last measured, snapshots included:
// diskmon counts them for disk pressure (its fair share) only, never
// against a scope's quota (§9).
func (m *Manager) Usage() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for k, boxes := range m.live {
		for _, b := range boxes {
			if b.diskBytes > 0 {
				out[k.Tile] += b.diskBytes
			}
		}
	}
	return out
}

// DiskLow reports whether the workspace disk is low now — diskmon's last
// verdict, or a statfs of the partition taken now: tile sandboxes don't
// start meanwhile (503).
func (m *Manager) DiskLow() bool {
	return m.deps.Disk != nil && (m.deps.Disk.Low() || m.statfsLow())
}

// statfsLow is the workspace partition below diskmon's reserve now.
func (m *Manager) statfsLow() bool {
	free, total := m.usage.statfs(m.root)
	return m.deps.Disk.LowAt(free, total)
}

// watchDisk arms the statfs watch unless it is armed: a namespace sandbox
// runs.
func (m *Manager) watchDisk() {
	if m.deps.Disk == nil {
		return
	}
	u := &m.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.watch == nil {
		u.watch = m.afterFunc(diskEvery, m.diskTick)
	}
}

// diskTick is the watch: the partition below the reserve stops what runs
// above the fair share. It re-arms while a namespace sandbox runs, and ends
// with the last one.
func (m *Manager) diskTick() {
	u := &m.usage
	u.mu.Lock()
	u.watch = nil
	u.mu.Unlock()
	if !m.runsNamespace() {
		return
	}
	if m.statfsLow() {
		m.OnLowDisk()
	}
	u.mu.Lock()
	if u.watch == nil {
		u.watch = m.afterFunc(diskEvery, m.diskTick)
	}
	u.mu.Unlock()
}

// runsNamespace reports a namespace sandbox running.
func (m *Manager) runsNamespace() bool {
	return len(m.running(func(_ Key, d *Def) bool { return d.Mode == ModeNamespace })) > 0
}

// OnLowDisk is the workspace disk below its reserve — diskmon's verdict or
// the runtime's own statfs: the running namespace sandboxes of every tile
// whose sandboxes hold more than diskmon's fair share are synced and
// stopped, state kept, largest tile first. It returns at once; one pass
// runs at a time.
func (m *Manager) OnLowDisk() {
	u := &m.usage
	u.mu.Lock()
	if u.lowPass {
		u.mu.Unlock()
		return
	}
	u.lowPass = true
	u.mu.Unlock()
	go func() {
		defer func() {
			u.mu.Lock()
			u.lowPass = false
			u.mu.Unlock()
		}()
		m.lowDiskStop()
	}()
}

// lowDiskStop is OnLowDisk's pass.
func (m *Manager) lowDiskStop() {
	var fair int64
	if m.deps.Disk != nil {
		fair = m.deps.Disk.FairShare()
	}
	tiles, held := m.lowDiskTiles(fair)
	for _, t := range tiles {
		why := fmt.Sprintf("the workspace disk is low: stopped, state kept — its tile's sandboxes hold %s, over the fair share (%s); start it again once space is freed", gib(held[t]), gib(fair))
		slog.Warn("tile sandboxes: the workspace disk is low; stopping a tile's sandboxes", "tile", t, "bytes", held[t])
		m.StopWhere(func(k Key, d *Def) bool { return k.Tile == t && d.Mode == ModeNamespace }, why)
	}
}

// lowDiskTiles are the tiles a low disk stops, in order: those running a
// namespace sandbox whose sandboxes hold more than fair, largest first;
// held is what each holds.
func (m *Manager) lowDiskTiles(fair int64) (tiles []string, held map[string]int64) {
	m.mu.Lock()
	held, runs := map[string]int64{}, map[string]bool{}
	for k, boxes := range m.live {
		for _, b := range boxes {
			held[k.Tile] += b.diskBytes
			if b.run != nil && b.run.def.Mode == ModeNamespace {
				runs[k.Tile] = true
			}
		}
	}
	m.mu.Unlock()
	for t := range runs {
		if held[t] > fair {
			tiles = append(tiles, t)
		}
	}
	sort.Slice(tiles, func(i, j int) bool {
		if held[tiles[i]] != held[tiles[j]] {
			return held[tiles[i]] > held[tiles[j]]
		}
		return tiles[i] < tiles[j]
	})
	return tiles, held
}
