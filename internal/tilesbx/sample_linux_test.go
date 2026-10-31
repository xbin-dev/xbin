//go:build linux

package tilesbx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
)

// fakeLeaves is a fake cgroup parent: a directory per leaf holding the
// files cgroup.Usage reads, which the runtime's sampler reads as its
// sandboxes' leaves (leafStats). The tool sandbox these tests run in has
// no cgroup delegation, so nothing here depends on a real one.
type fakeLeaves struct {
	t   *testing.T
	dir string
}

func newFakeLeaves(t *testing.T, m *Manager) *fakeLeaves {
	f := &fakeLeaves{t: t, dir: t.TempDir()}
	m.leafStats = func(leaf string) (cgroup.Usage, bool) { return cgroup.ReadUsage(filepath.Join(f.dir, leaf)) }
	return f
}

// set writes a leaf's accounting: usage_usec, memory.current, memory.max
// (0: "max") and pids.current.
func (f *fakeLeaves) set(leaf string, usec, mem, memMax, pids int64) {
	f.t.Helper()
	d := filepath.Join(f.dir, leaf)
	if err := os.MkdirAll(d, 0o755); err != nil {
		f.t.Fatal(err)
	}
	mx := "max"
	if memMax > 0 {
		mx = fmt.Sprint(memMax)
	}
	for name, v := range map[string]string{
		"cpu.stat":       fmt.Sprintf("usage_usec %d\nuser_usec %d\nsystem_usec 0\nnr_periods 0\n", usec, usec),
		"memory.current": fmt.Sprintf("%d\n", mem),
		"memory.max":     mx + "\n",
		"pids.current":   fmt.Sprintf("%d\n", pids),
	} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(v), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

// newSampleEnv is a fake-launcher runtime on a fake clock whose sandboxes'
// leaves are fake directories.
func newSampleEnv(t *testing.T) (*fakeEnv, *fakeClock, *fakeLeaves) {
	clk := newFakeClock()
	fe := newFakeEnv(t, withClock(clk))
	fe.m.afterFunc = clk.AfterFunc
	return fe, clk, newFakeLeaves(t, fe.m)
}

// startOn creates and starts a namespace sandbox that won't idle within
// the test, and gives its run leaf ("" none: no delegation), as a cgroup
// parent places it.
func (fe *fakeEnv) startOn(name, leaf string) {
	fe.t.Helper()
	fe.create(map[string]any{"name": name, "mode": "namespace", "idleStopMin": 1440})
	fe.want(fe.do(mgr, "POST", "/sandboxes/"+name+"/start", nil), http.StatusOK, "")
	fe.runOf(name).leaf = leaf
}

// getOne is the sandbox as a manager's GET of it answers.
func (fe *fakeEnv) getOne(name string) Info {
	fe.t.Helper()
	w := fe.do(mgr, "GET", "/sandboxes/"+name, nil)
	fe.want(w, http.StatusOK, "")
	return fe.info(w)
}

// samplerArmed reports whether the sampler's next pass is armed.
func (fe *fakeEnv) samplerArmed() bool {
	fe.m.usage.mu.Lock()
	defer fe.m.usage.mu.Unlock()
	return fe.m.usage.sample != nil
}

// Once a minute every running sandbox's leaf is read: cpuPct is the percent
// of one core over the minute (the first from its start), memory and pids
// as the leaf has them. A GET of one sandbox carries the series, oldest
// first; the list carries usage without it.
func TestSampleUsage(t *testing.T) {
	fe, clk, leaves := newSampleEnv(t)
	leaves.set("sbx-a", 0, 64<<20, 2176<<20, 2)
	fe.startOn("sb-1", "sbx-a")
	start := clk.Now()
	if in := fe.getOne("sb-1"); in.Usage != nil {
		t.Fatalf("usage before the first pass: %+v", in.Usage)
	}
	if !fe.samplerArmed() {
		t.Fatal("a sandbox came up and the sampler isn't armed")
	}

	leaves.set("sbx-a", 30_000_000, 512<<20, 2176<<20, 7) // half a core over its first minute
	clk.advance(time.Minute)
	t1 := start.Add(time.Minute).UnixMilli()
	want := LiveUsage{At: t1, CPUPct: 50, MemBytes: 512 << 20, MemMaxBytes: 2176 << 20, Pids: 7,
		Series: []UsagePoint{{T: t1, CPU: 50, Mem: 512 << 20}}}
	if got := fe.getOne("sb-1").Usage; got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("after the first pass: %+v, want %+v", got, want)
	}

	leaves.set("sbx-a", 150_000_000, 600<<20, 2176<<20, 9) // two cores over the next
	clk.advance(time.Minute)
	t2 := start.Add(2 * time.Minute).UnixMilli()
	want = LiveUsage{At: t2, CPUPct: 200, MemBytes: 600 << 20, MemMaxBytes: 2176 << 20, Pids: 9,
		Series: []UsagePoint{{T: t1, CPU: 50, Mem: 512 << 20}, {T: t2, CPU: 200, Mem: 600 << 20}}}
	if got := fe.getOne("sb-1").Usage; got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("after the second pass: %+v, want %+v", got, want)
	}

	// the list: usage, never its series
	w := fe.do(mgr, "GET", "/sandboxes", nil)
	fe.want(w, http.StatusOK, "")
	var list struct {
		Sandboxes []map[string]json.RawMessage `json:"sandboxes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Sandboxes) != 1 {
		t.Fatalf("the list: %v %s", err, w.Body.String())
	}
	var lu map[string]any
	if err := json.Unmarshal(list.Sandboxes[0]["usage"], &lu); err != nil {
		t.Fatalf("the list's usage: %v (%s)", err, w.Body.String())
	}
	if _, ok := lu["series"]; ok || lu["cpuPct"] != 200.0 || lu["at"] != float64(t2) || lu["pids"] != 9.0 {
		t.Fatalf("the list's usage: %v", lu)
	}

	// a cap of "max" is no memMaxBytes
	leaves.set("sbx-a", 150_000_000, 600<<20, 0, 9)
	clk.advance(time.Minute)
	if got := fe.getOne("sb-1").Usage; got == nil || got.MemMaxBytes != 0 || got.CPUPct != 0 || len(got.Series) != 3 {
		t.Fatalf("an idle minute without a cap: %+v", got)
	}
	raw := fe.do(mgr, "GET", "/sandboxes/sb-1", nil).Body.String()
	if contains(raw, "memMaxBytes") {
		t.Fatalf("memMaxBytes without a cap: %s", raw)
	}
}

// A run keeps its last 60 samples, oldest first.
func TestSampleRing(t *testing.T) {
	fe, clk, leaves := newSampleEnv(t)
	leaves.set("sbx-a", 0, 1<<20, 0, 1)
	fe.startOn("sb-1", "sbx-a")
	var usec int64
	for i := 1; i <= 70; i++ {
		usec += int64(i) * 600_000 // minute i: i % of one core
		leaves.set("sbx-a", usec, int64(i)<<20, 0, 1)
		clk.advance(time.Minute)
	}
	u := fe.getOne("sb-1").Usage
	if u == nil || len(u.Series) != samplePoints {
		t.Fatalf("after 70 passes: %+v", u)
	}
	for j, p := range u.Series {
		i := 11 + j // the oldest kept is minute 11
		if p.CPU != float64(i) || p.Mem != int64(i)<<20 || p.T != u.At-int64(59-j)*60_000 {
			t.Fatalf("series[%d] = %+v, want minute %d's (at %d)", j, p, i, u.At)
		}
	}
	if u.CPUPct != 70 || u.Series[59].T != u.At {
		t.Fatalf("the latest: %+v", u)
	}
}

// A stop takes the samples with the run; the sampler ends when nothing
// runs and comes back with the next start, whose series starts over.
func TestSampleStopClears(t *testing.T) {
	fe, clk, leaves := newSampleEnv(t)
	leaves.set("sbx-a", 0, 1<<20, 0, 1)
	fe.startOn("sb-1", "sbx-a")
	leaves.set("sbx-a", 6_000_000, 8<<20, 0, 3)
	clk.advance(2 * time.Minute)
	if u := fe.getOne("sb-1").Usage; u == nil || len(u.Series) != 2 {
		t.Fatalf("running: %+v", u)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusOK, "")
	if in := fe.getOne("sb-1"); in.State != StateStopped || in.Usage != nil {
		t.Fatalf("stopped: %s %+v", in.State, in.Usage)
	}
	clk.advance(time.Minute) // the armed pass finds nothing running …
	if fe.samplerArmed() {
		t.Fatal("the sampler re-armed with nothing running")
	}

	// … and the next start arms it again, from a fresh leaf
	leaves.set("sbx-a", 0, 1<<20, 0, 1)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.runOf("sb-1").leaf = "sbx-a"
	if !fe.samplerArmed() {
		t.Fatal("a start didn't arm the sampler")
	}
	if u := fe.getOne("sb-1").Usage; u != nil {
		t.Fatalf("a new run carries the last one's samples: %+v", u)
	}
	leaves.set("sbx-a", 15_000_000, 4<<20, 0, 2)
	clk.advance(time.Minute)
	if u := fe.getOne("sb-1").Usage; u == nil || len(u.Series) != 1 || u.CPUPct != 25 || u.MemBytes != 4<<20 {
		t.Fatalf("the new run's first sample: %+v", u)
	}
}

// Without a leaf (no cgroup delegation) there is nothing to sample: no
// usage — and without a way to read leaves the sampler is never armed.
func TestSampleNoLeaf(t *testing.T) {
	fe, clk, leaves := newSampleEnv(t)
	leaves.set("sbx-a", 0, 1<<20, 0, 1)
	fe.startOn("sb-1", "") // started without a leaf
	fe.startOn("sb-2", "sbx-a")
	clk.advance(3 * time.Minute)
	if u := fe.getOne("sb-1").Usage; u != nil {
		t.Fatalf("a sandbox without a leaf has usage: %+v", u)
	}
	if u := fe.getOne("sb-2").Usage; u == nil || len(u.Series) != 3 {
		t.Fatalf("its neighbour with a leaf: %+v", u)
	}
	// a leaf that went away (read fails) records nothing, keeps what it had
	if err := os.RemoveAll(filepath.Join(leaves.dir, "sbx-a")); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute)
	if u := fe.getOne("sb-2").Usage; u == nil || len(u.Series) != 3 {
		t.Fatalf("an unreadable leaf: %+v", u)
	}

	clk2 := newFakeClock()
	plain := newFakeEnv(t, withClock(clk2)) // no cgroup parent, no reader
	plain.m.afterFunc = clk2.AfterFunc
	plain.startOn("sb-1", "")
	if plain.samplerArmed() {
		t.Fatal("the sampler armed without cgroup delegation")
	}
	clk2.advance(2 * time.Minute)
	if u := plain.getOne("sb-1").Usage; u != nil {
		t.Fatalf("usage without delegation: %+v", u)
	}
}

// cpuPct is percent of one core, to a tenth; a counter going back reads 0;
// a reading within a second of the last is dropped.
func TestCPUPct(t *testing.T) {
	for _, c := range []struct {
		prev, cur int64
		dt        time.Duration
		want      float64
	}{
		{0, 30_000_000, time.Minute, 50},
		{10_000_000, 250_000_000, time.Minute, 400},
		{0, 123_456, time.Second, 12.3},
		{0, 0, time.Minute, 0},
		{5, 4, time.Minute, 0},
		{0, 1, 0, 0},
	} {
		if got := cpuPct(c.prev, c.cur, c.dt); got != c.want {
			t.Errorf("cpuPct(%d, %d, %s) = %v, want %v", c.prev, c.cur, c.dt, got, c.want)
		}
	}
	var s liveStats
	t0 := time.UnixMilli(1790000000000)
	s.add(t0.Add(500*time.Millisecond), t0, cgroup.Usage{CPUUsec: 400_000, MemCurrent: 1})
	if s.view(true) != nil {
		t.Fatalf("a reading half a second after the start: %+v", s.view(true))
	}
	s.add(t0.Add(2*time.Second), t0, cgroup.Usage{CPUUsec: 1_000_000, MemCurrent: 2, MemMax: -1})
	if u := s.view(false); u == nil || u.CPUPct != 50 || u.MemMaxBytes != 0 || u.Series != nil {
		t.Fatalf("two seconds in: %+v", u)
	}
}
