//go:build linux

package tilesbx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
)

// startAll starts every name at once and reports which ran and which were
// refused (by refusal).
func (fe *fakeEnv) startAll(names []string) (ran []string, refused map[string]int) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	refused = map[string]int{}
	for _, n := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			w := fe.do(mgr, "POST", "/sandboxes/"+n+"/start", nil)
			mu.Lock()
			defer mu.Unlock()
			switch w.Code {
			case http.StatusOK:
				if in := fe.info(w); in.State == StateRunning {
					ran = append(ran, n)
				} else {
					refused["failed: "+in.StateDetail]++
				}
			case http.StatusTooManyRequests:
				refused[RefLimit]++
			default:
				refused[fmt.Sprintf("%d %s", w.Code, w.Body)]++
			}
		}(n)
	}
	wg.Wait()
	return ran, refused
}

// the book is empty: nothing booked for the tile, nothing in the total.
func (fe *fakeEnv) assertBookEmpty() {
	fe.t.Helper()
	if run, mem, vcpus := fe.m.booked(fe.k.Tile); run != 0 || mem != 0 || vcpus != 0 {
		fe.t.Fatalf("the tile's book: running %d, memMiB %d, vcpus %d", run, mem, vcpus)
	}
	if used, _ := fe.m.TotalBook(); used != 0 {
		fe.t.Fatalf("the total book: %d MiB", used)
	}
}

// Ten concurrent starts under each cap admit exactly what it allows — the
// running count, the memory, the vCPUs, the workspace's total — and the
// books come back to zero once they stop.
func TestAdmissionCaps(t *testing.T) {
	for _, c := range []struct {
		name   string
		policy string
		size   map[string]any
		want   int
		cap    string
	}{
		{"running", `{"overrides":{"apps/mgr":{"perTile":{"max":16}}}}`, nil, 4, "perTile.running"},
		{"memMiB", `{"overrides":{"apps/mgr":{"perTile":{"max":16,"running":16,"memMiB":3072}}}}`, map[string]any{"memMiB": 1024}, 3, "perTile.memMiB"},
		{"vcpus", `{"overrides":{"apps/mgr":{"perTile":{"max":16,"running":16,"vcpus":5}}}}`, map[string]any{"vcpus": 1, "memMiB": 256}, 5, "perTile.vcpus"},
		// each leaf may take 256 + 128 MiB: 1200 MiB hold three
		{"total", `{"total":{"memMiB":1200},"overrides":{"apps/mgr":{"perTile":{"max":16,"running":16}}}}`, map[string]any{"memMiB": 256}, 3, "total.memMiB"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fe := newFakeEnv(t)
			fe.putPolicy(c.policy, http.StatusOK)
			var names []string
			for i := 0; i < 10; i++ {
				body := ns(fmt.Sprintf("sb-%d", i))
				for k, v := range c.size {
					body[k] = v
				}
				fe.create(body)
				names = append(names, body["name"].(string))
			}
			ran, refused := fe.startAll(names)
			if len(ran) != c.want || refused[RefLimit] != 10-c.want {
				t.Fatalf("ran %v, refused %v; want %d running", ran, refused, c.want)
			}
			// the refusal names the cap
			for _, n := range names {
				if fe.runOf(n) != nil {
					continue
				}
				w := fe.do(mgr, "POST", "/sandboxes/"+n+"/start", nil)
				fe.want(w, http.StatusTooManyRequests, RefLimit)
				if !strings.Contains(w.Body.String(), c.cap) {
					t.Fatalf("the refusal doesn't name %s: %s", c.cap, w.Body)
				}
				break
			}
			fe.m.StopAll("the test")
			fe.assertBookEmpty()
			// and it admits again
			if ran, _ := fe.startAll(names); len(ran) != c.want {
				t.Fatalf("after the stops: ran %v", ran)
			}
		})
	}
}

// A refusal is made while the book is held: its figures are read under
// the book's mutex, never after a concurrent release changed them (-race).
func TestAdmissionRefusalUnderLock(t *testing.T) {
	fe := newFakeEnv(t)
	fe.putPolicy(`{"overrides":{"apps/mgr":{"perTile":{"running":1}}}}`, http.StatusOK)
	d := &Def{Name: "sb-1", Mode: ModeNamespace, MemMiB: 256, VCPUs: 1}
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if release, err := fe.m.admit(fe.k, d); err == nil {
					release()
				}
			}
		}()
	}
	wg.Wait()
	fe.assertBookEmpty()
}

// The total book counts each sandbox's leaf as its mode makes it: the
// sandbox's memory and that mode's overhead.
func TestAdmissionPerMode(t *testing.T) {
	fe := newFakeEnv(t)
	vm := *nsOps
	vm.leaf = func(d *Def, lim Limits) cgroup.Limits {
		l := leafLimits(d, lim)
		l.MemMax = int64(d.MemMiB+512) << 20
		return l
	}
	fe.m.modes[ModeVM] = &vm
	fe.create(map[string]any{"name": "sb-ns", "mode": "namespace", "memMiB": 256})
	fe.create(map[string]any{"name": "sb-vm", "mode": "vm", "memMiB": 256})
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-ns/start", nil), http.StatusOK, "")
	if used, _ := fe.m.TotalBook(); used != 256+leafOverheadMiB {
		t.Fatalf("a namespace sandbox's leaf: %d MiB", used)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-vm/start", nil), http.StatusOK, "")
	if used, _ := fe.m.TotalBook(); used != 256+leafOverheadMiB+256+512 {
		t.Fatalf("with a VM's leaf: %d MiB", used)
	}
	if run, mem, vcpus := fe.m.booked(fe.k.Tile); run != 2 || mem != 512 || vcpus != 4 {
		t.Fatalf("the tile's book: %d %d %d", run, mem, vcpus)
	}
	fe.m.StopAll("the test")
	fe.assertBookEmpty()
}

// The books come back to zero after every unwind and every teardown: a
// failed launch, an agent that dies starting, a stop, an end of its own.
func TestAdmissionReleased(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.l.fail = fmt.Errorf("no namespaces here")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.assertBookEmpty()
	fe.l.fail = nil
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	if run, _, _ := fe.m.booked(fe.k.Tile); run != 1 {
		t.Fatalf("running, booked %d", run)
	}
	fe.l.last().die(ExitStatus{Code: 1})
	fe.waitState("sb-1", StateStopped)
	fe.assertBookEmpty()
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusOK, "")
	fe.assertBookEmpty()
}

// Twenty concurrent creates under perTile.max 8 store exactly eight.
func TestCreateMaxConcurrent(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := e.do(mgr, "POST", "/sandboxes", ns(fmt.Sprintf("sb-%d", i)))
			mu.Lock()
			codes[w.Code]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	e.m.mu.Lock()
	n := e.m.defs.count(Key{Tile: "apps/mgr"})
	e.m.mu.Unlock()
	if codes[http.StatusCreated] != 8 || codes[http.StatusTooManyRequests] != 12 || n != 8 {
		t.Fatalf("answers %v, %d stored", codes, n)
	}
}

type fakeDisk struct {
	mu  sync.Mutex
	low bool
}

func (f *fakeDisk) Low() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.low }

// A low workspace disk holds every start (503); a tile whose sandboxes'
// bytes pass perTile.diskGiB is refused (429) — sandbox bytes count against
// that cap, never against the scope's resource quota.
func TestAdmissionDisk(t *testing.T) {
	disk := &fakeDisk{low: true}
	fe := newFakeEnv(t, func(o *Options) { o.Deps.Disk = disk })
	fe.create(ns("sb-1"))
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	fe.want(w, http.StatusServiceUnavailable, RefUnavailable)
	if !strings.Contains(w.Body.String(), "disk is low") {
		t.Fatalf("low disk: %s", w.Body)
	}
	disk.mu.Lock()
	disk.low = false
	disk.mu.Unlock()
	fe.putPolicy(`{"perTile":{"diskGiB":1}}`, http.StatusOK)
	fe.m.mu.Lock()
	fe.m.boxLocked(fe.k, "sb-1").diskBytes = 2 << 30 // as the usage worker measured it
	fe.m.mu.Unlock()
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	fe.want(w, http.StatusTooManyRequests, RefLimit)
	if !strings.Contains(w.Body.String(), "perTile.diskGiB") {
		t.Fatalf("over the disk cap: %s", w.Body)
	}
	fe.putPolicy(`{"perTile":{"diskGiB":3}}`, http.StatusOK)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.waitState("sb-1", StateRunning)
}

// Turning the kill switch off stops every running tile sandbox (state
// kept) and refuses starts; turning it on again lets them start.
func TestPolicyOffStops(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.create(ns("sb-2"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-2/start", nil), http.StatusOK, "")
	fe.putPolicy(`{"enabled":false}`, http.StatusOK)
	for _, n := range []string{"sb-1", "sb-2"} {
		if in := fe.waitState(n, StateStopped); !strings.Contains(in.StateDetail, "switched off") {
			t.Fatalf("%s: %+v", n, in)
		}
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusServiceUnavailable, RefUnavailable)
	fe.assertBookEmpty()
	fe.putPolicy(`{"enabled":true}`, http.StatusOK)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.waitState("sb-1", StateRunning)
}

// A start already past the policy's check when an admin turns tile
// sandboxes off (the switch stops only what runs by then) doesn't come up
// running: it answers 503 and ends stopped, the switch in stateDetail.
func TestPolicyOffDuringStart(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	entered, proceed := make(chan struct{}), make(chan struct{})
	admit := fe.m.reserve
	fe.m.reserve = func(k Key, d *Def) (func(), error) {
		close(entered) // past the policy's check, nothing launched yet
		<-proceed
		return admit(k, d)
	}
	answer := make(chan *httptest.ResponseRecorder, 1)
	go func() { answer <- fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil) }()
	<-entered
	fe.putPolicy(`{"enabled":false}`, http.StatusOK)
	close(proceed)
	fe.want(<-answer, http.StatusServiceUnavailable, RefUnavailable)
	if in := fe.get("sb-1"); in.State != StateStopped || !strings.Contains(in.StateDetail, "switched off") {
		t.Fatalf("a start the switch overtook: %+v", in)
	}
	if fe.runOf("sb-1") != nil || fe.l.count() != 1 {
		t.Fatalf("it runs, or never launched (%d)", fe.l.count())
	}
	fe.assertBookEmpty()
}

// A definition is checked again at every start: a mount the tile no
// longer holds fails the start, with the reason.
func TestStartRevalidates(t *testing.T) {
	mounts := fakeMounts{"apps/mgr res:apps/mgr/work": {Src: "/x/work", Role: "writer", Kind: "filesystem", Ready: true}}
	fe := newFakeEnv(t, func(o *Options) { o.Deps.Mounts = mounts })
	fe.create(map[string]any{"name": "sb-1", "mode": "namespace", "mounts": []any{map[string]any{"res": "res:apps/mgr/work", "at": "/mnt/work"}}})
	delete(mounts, "apps/mgr res:apps/mgr/work") // the grant revoked, say
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	fe.want(w, http.StatusBadRequest, RefInvalid)
	if !strings.Contains(w.Body.String(), "/mnt/work") || !strings.Contains(w.Body.String(), "doesn't hold") {
		t.Fatalf("the refusal: %s", w.Body)
	}
	if fe.l.count() != 0 {
		t.Fatal("it launched anyway")
	}
	fe.assertBookEmpty()
}

// A stop whose kill didn't take (a PID 1 stuck in the kernel) answers 503
// and leaves the sandbox stopping; a second stop kills again.
func TestSecondStopKillsAgain(t *testing.T) {
	fe := newFakeEnv(t)
	fe.m.endWait = 300 * time.Millisecond
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	p := fe.l.last()
	p.ignoreKills.Store(1)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusServiceUnavailable, RefUnavailable)
	if in := fe.get("sb-1"); in.State != StateStopping {
		t.Fatalf("after a kill that didn't take: %+v", in)
	}
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped {
		t.Fatalf("after the second stop: %+v", in)
	}
	fe.assertBookEmpty()
}
