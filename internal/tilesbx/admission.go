package tilesbx

// admission.go — what a start is admitted against (plans/tile-sandbox-runtime.md
// §6.1): the workspace disk, the tile's disk cap, and the books. Each tile
// has one book (summed across its deployments) of what its running
// sandboxes hold — how many, their memory, their vCPUs — and the workspace
// has one more, the total book, of the memory every tile sandbox's cgroup
// leaf may take together (the comp-tilesbx parent's memory.max, §6.2), so a
// start is refused before the kernel would have to OOM. A booking checks
// "with this one included ≤ the cap" and takes it in one critical section,
// as vm.Reserve does: ten concurrent starts under running 4 give exactly
// four sandboxes. Its release is idempotent; the start's unwind or the
// run's teardown calls it.
//
// (The definitions count, perTile.max, is a create's check, made under the
// definitions mutex — api.go.)

import (
	"fmt"
	"sync"
	"time"
)

// tileBook is what one tile's running sandboxes hold. Its own mutex.
type tileBook struct {
	mu      sync.Mutex
	running int
	memMiB  int
	vcpus   int
}

// books are the admission's bookkeeping: a book per tile, and the total.
type books struct {
	mu    sync.Mutex // the tiles map only
	tiles map[string]*tileBook

	totalMu  sync.Mutex
	totalMiB int // what every running tile sandbox's leaf may take together
}

// tile is tile's book, made on first use.
func (bs *books) tile(tile string) *tileBook {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.tiles == nil {
		bs.tiles = map[string]*tileBook{}
	}
	b := bs.tiles[tile]
	if b == nil {
		b = &tileBook{}
		bs.tiles[tile] = b
	}
	return b
}

// admit is the runtime's reserve (§6.1, §7 step 1): the workspace disk
// isn't low (503), the tile's sandbox bytes are within perTile.diskGiB
// (429), and the tile's book and the total book take this sandbox (429,
// naming the cap). d's sizes are as it starts (clamped to the caps now).
func (m *Manager) admit(k Key, d *Def) (func(), error) {
	m.mu.Lock()
	lim := m.limitsFor(k.Tile)
	totalCap := m.totalMemMiBLocked()
	disk := m.tileDiskBytesLocked(k.Tile)
	ops := m.modes[d.Mode]
	m.mu.Unlock()
	if m.deps.Disk != nil && m.deps.Disk.Low() {
		return nil, &Error{Refusal: RefUnavailable, Msg: "the workspace disk is low: tile sandboxes don't start until space is freed", RetryAfter: time.Minute}
	}
	if capB := int64(lim.PerTile.DiskGiB) << 30; disk > capB {
		return nil, refuse(RefLimit, "the tile's sandboxes use %s of disk, over its %d GiB (sandboxes policy: perTile.diskGiB): delete a sandbox or a snapshot", gib(disk), lim.PerTile.DiskGiB)
	}
	leafMiB := d.MemMiB
	if ops != nil && ops.leaf != nil {
		if mx := ops.leaf(d, lim).MemMax; mx > 0 {
			leafMiB = int(mx >> 20) // what its leaf may take: the sandbox and its overhead
		}
	}

	tb := m.books.tile(k.Tile)
	tb.mu.Lock()
	pt := lim.PerTile
	var over error // made under the book's mutex: a release may change it the moment it's let go
	switch {
	case tb.running+1 > pt.Running:
		over = refuse(RefLimit, "the tile runs %d sandboxes, its limit (sandboxes policy: perTile.running): stop one first", tb.running)
	case tb.memMiB+d.MemMiB > pt.MemMiB:
		over = refuse(RefLimit, "the tile's running sandboxes would hold %d MiB of memory, over its %d MiB (sandboxes policy: perTile.memMiB)", tb.memMiB+d.MemMiB, pt.MemMiB)
	case tb.vcpus+d.VCPUs > pt.VCPUs:
		over = refuse(RefLimit, "the tile's running sandboxes would hold %d vCPUs, over its %d (sandboxes policy: perTile.vcpus)", tb.vcpus+d.VCPUs, pt.VCPUs)
	}
	if over != nil {
		tb.mu.Unlock()
		return nil, over
	}
	tb.running++
	tb.memMiB += d.MemMiB
	tb.vcpus += d.VCPUs
	tb.mu.Unlock()
	untile := func() {
		tb.mu.Lock()
		tb.running--
		tb.memMiB -= d.MemMiB
		tb.vcpus -= d.VCPUs
		tb.mu.Unlock()
	}

	bs := &m.books
	bs.totalMu.Lock()
	if totalCap > 0 && bs.totalMiB+leafMiB > totalCap {
		used := bs.totalMiB
		bs.totalMu.Unlock()
		untile()
		return nil, refuse(RefLimit, "every tile sandbox together would hold %d MiB of memory, over the workspace's %d MiB (sandboxes policy: total.memMiB): %d MiB are held now", used+leafMiB, totalCap, used)
	}
	bs.totalMiB += leafMiB
	bs.totalMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			untile()
			bs.totalMu.Lock()
			bs.totalMiB -= leafMiB
			bs.totalMu.Unlock()
		})
	}, nil
}

// totalMemMiBLocked is the total book's cap: policy.total.memMiB, 0 = ¾ of
// the host's RAM (0 when that is unknown: no cap). Callers hold m.mu.
func (m *Manager) totalMemMiBLocked() int {
	if t := m.policy.get().Effective().Total.MemMiB; t > 0 {
		return t
	}
	return int(hostMemBytes() / 4 * 3 >> 20)
}

// tileDiskBytesLocked is what the tile's sandboxes were last measured to
// hold on disk, snapshots included (diskBytes; measured by the usage
// worker). Callers hold m.mu.
func (m *Manager) tileDiskBytesLocked(tile string) int64 {
	var n int64
	for k, boxes := range m.live {
		if k.Tile != tile {
			continue
		}
		for _, b := range boxes {
			n += b.diskBytes
		}
	}
	return n
}

// TotalBook is the total book: the memory every running tile sandbox's
// leaf may take, of the workspace's cap (MiB; cap 0 = none) — the admin's
// health view.
func (m *Manager) TotalBook() (usedMiB, capMiB int) {
	m.mu.Lock()
	capMiB = m.totalMemMiBLocked()
	m.mu.Unlock()
	m.books.totalMu.Lock()
	defer m.books.totalMu.Unlock()
	return m.books.totalMiB, capMiB
}

// booked is tile's book now (tests).
func (m *Manager) booked(tile string) (running, memMiB, vcpus int) {
	tb := m.books.tile(tile)
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.running, tb.memMiB, tb.vcpus
}

// gib prints bytes as GiB.
func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }
