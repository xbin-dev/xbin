//go:build linux && xbinmeasure

package measure

// save_test.go — save to live, on an isolated xbind (--isolate: Go builds
// run confined, backends in namespace sandboxes):
//
//   - a static tile: the file write → xbind's `reload` event for the tile
//     (the moment every open window of it is told to reload; the browser
//     half — reload to the new page painted — is browser_test.go's);
//   - a Go tile: the write of backend/main.go → the first 200 from its API
//     carrying the new code's answer (polled every 5 ms through xbind's
//     proxy), split by the event stream into the watcher (reload), the
//     confined build plus the new generation's start and health check
//     (build-start → build-ok) and the swap (build-ok → served).
//
// TestSwap runs a closed request loop against the Go tile's API — 8 workers
// GET, 4 POST with a JSON body — through ten saves (each a
// rebuild and a blue/green swap), and counts every request that failed and
// the slowest ones; every request is in swap-requests.csv.gz.

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

var (
	saveN = quick(30, 3) // samples per series (one more, the first, is discarded as cold)
	swapN = quick(10, 2) // saves under the request loop
)

func TestSaveToLive(t *testing.T) {
	d := daemon(t)
	ev := tapEvents(t, d)
	rec := record(t, "save-to-live")

	t.Run("static", func(t *testing.T) {
		writeHandbook(t, d, "")
		waitOK(t, d.URL+"/c/"+handbookTile+"/", owner(d), 30*time.Second)
		index := filepath.Join(d.WS, handbookTile, "index.html")
		for i := 0; i <= saveN; i++ {
			ev.settle(handbookTile, 700*time.Millisecond)
			page := handbookIndex(fmt.Sprintf("Welcome to Northwind — edit %d", i), "Updated just now", "")
			m := ev.mark()
			t0 := time.Now()
			if err := os.WriteFile(index, []byte(page), 0o644); err != nil {
				t.Fatal(err)
			}
			at, ok := ev.next(m, "reload", handbookTile, 10*time.Second)
			if !ok {
				t.Fatalf("save %d: no reload within 10 s: %s", i, ev.describe(m, handbookTile, t0))
			}
			if i == 0 {
				rec.add(map[string]any{"phase": "static-cold", "i": i}, map[string]float64{"static: first save (discarded)": ms(at.Sub(t0))})
				continue
			}
			rec.add(map[string]any{"phase": "static", "i": i, "events": ev.describe(m, handbookTile, t0)},
				map[string]float64{"static: save → reload event": ms(at.Sub(t0))})
		}
	})

	t.Run("go", func(t *testing.T) {
		t0 := time.Now()
		writeOrders(t, d, "r0")
		waitRelease(t, d, "r0", 5*time.Minute)
		rec.add(map[string]any{"phase": "go-first-build"}, map[string]float64{"go: tile written → first answer (cold build)": ms(time.Since(t0))})
		src := filepath.Join(d.WS, ordersTile, "backend", "main.go")
		for i := 0; i <= saveN; i++ {
			ev.settle(ordersTile, 700*time.Millisecond)
			rel := fmt.Sprintf("r%d-%d", i+1, time.Now().UnixNano()%100000)
			m := ev.mark()
			t0 := time.Now()
			if err := os.WriteFile(src, []byte(ordersSource(rel)), 0o644); err != nil {
				t.Fatal(err)
			}
			served := waitRelease(t, d, rel, 2*time.Minute)
			total := ms(served.Sub(t0))
			reload, ok1 := ev.first(m, "reload", ordersTile)
			start, ok2 := ev.first(m, "build-start", ordersTile)
			done, ok3 := ev.first(m, "build-ok", ordersTile)
			fields := map[string]any{"phase": "go", "i": i, "release": rel, "events": ev.describe(m, ordersTile, t0)}
			if i == 0 {
				rec.add(fields, map[string]float64{"go: first save (discarded)": total})
				continue
			}
			vals := map[string]float64{"go: save → new code serving": total}
			if ok1 && ok2 && ok3 && !start.Before(reload) && !done.Before(start) && !served.Before(done) {
				vals["go: save → reload event (watcher)"] = ms(reload.Sub(t0))
				vals["go: build + start + health"] = ms(done.Sub(start))
				vals["go: build-ok → first answer"] = ms(served.Sub(done))
			}
			rec.add(fields, vals)
		}
	})
	rec.done(map[string]any{"pollMs": 5, "isolated": true})
}

// waitRelease polls the orders API every 5 ms until it answers with
// release, and returns when it first did.
func waitRelease(t testing.TB, d *xbindtest.Daemon, release string, timeout time.Duration) time.Time {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var st int
	var b []byte
	var err error
	for {
		st, b, err = call("GET", d.URL+"/api/"+ordersTile+"/orders", nil, owner(d))
		if err == nil && st == 200 && strings.Contains(string(b), `"release":"`+release+`"`) {
			return time.Now()
		}
		if strings.Contains(string(b), "build failed") {
			t.Fatalf("the orders tile doesn't build: %s", cut(b))
		}
		if time.Now().After(deadline) {
			t.Fatalf("release %s never served: %d %s %v", release, st, cut(b), err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// req is one request of the loop: when it started (µs since the loop
// began), how long it took (µs), its status (0: a transport error), which
// worker and kind (G: GET /orders, P: POST /orders), and the release that
// answered.
type req struct {
	startUs, latUs int64
	status         int
	worker         int
	kind           byte
	release        string
	err            string
}

func TestSwap(t *testing.T) {
	d := daemon(t)
	ev := tapEvents(t, d)
	writeOrders(t, d, "r0")
	waitRelease(t, d, "r0", 5*time.Minute)
	// one save first: the build cache is warm, as in a working session
	src := filepath.Join(d.WS, ordersTile, "backend", "main.go")
	if err := os.WriteFile(src, []byte(ordersSource("r0b")), 0o644); err != nil {
		t.Fatal(err)
	}
	waitRelease(t, d, "r0b", 2*time.Minute)
	ev.settle(ordersTile, 700*time.Millisecond)

	// G: GET /orders, P: POST /orders with a JSON body. A request the old
	// generation never answered (a swap's SIGTERM reset it) is sent again
	// only when that is safe: no body, and an idempotent method or an
	// Idempotency-Key (docs/elements.md §Runtimes & backend lifecycle) — a
	// POST with a body never is, so one caught at the swap answers 502.
	kinds := []byte("GGGGGGGGPPPP")
	tr := &http.Transport{MaxIdleConnsPerHost: 64}
	hc := &http.Client{Transport: tr, Timeout: 60 * time.Second}
	tok := "Bearer " + d.Token()
	url := d.URL + "/api/" + ordersTile + "/orders"
	ctx, cancel := context.WithCancel(context.Background())
	begin := time.Now()
	logs := make([][]req, len(kinds))
	var latest atomic.Value // the newest release a GET saw
	latest.Store("r0b")
	var wg sync.WaitGroup
	for w, kind := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; ctx.Err() == nil; n++ {
				var r *http.Request
				if kind == 'G' {
					r, _ = http.NewRequest("GET", url, nil)
				} else {
					r, _ = http.NewRequest("POST", url, strings.NewReader(fmt.Sprintf(`{"item":"Croissants, box of 12","qty":%d}`, 1+n%5)))
					r.Header.Set("Content-Type", "application/json")
				}
				r.Header.Set("Authorization", tok)
				t0 := time.Now()
				resp, err := hc.Do(r)
				x := req{startUs: t0.Sub(begin).Microseconds(), worker: w, kind: kind}
				if err != nil {
					x.latUs, x.err = time.Since(t0).Microseconds(), err.Error()
				} else {
					b, rerr := io.ReadAll(resp.Body)
					resp.Body.Close()
					x.latUs, x.status = time.Since(t0).Microseconds(), resp.StatusCode
					if rerr != nil {
						x.err = rerr.Error()
					}
					var body struct{ Release string }
					if json.Unmarshal(b, &body) == nil {
						x.release = body.Release
					}
					if x.err == "" && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
						x.err = cut(b)
					}
					if kind == 'G' && x.status == 200 {
						latest.Store(x.release)
					}
				}
				logs[w] = append(logs[w], x)
			}
		}()
	}

	type swap struct {
		Release                                    string
		WriteMs, ReloadMs, BuildStartMs, BuildOKMs float64
		FirstNewMs                                 float64
		Events                                     string
	}
	var swaps []swap
	time.Sleep(2 * time.Second) // a baseline before the first save
	for i := 1; i <= swapN; i++ {
		rel := fmt.Sprintf("r%d", i)
		m := ev.mark()
		t0 := time.Now()
		if err := os.WriteFile(src, []byte(ordersSource(rel)), 0o644); err != nil {
			t.Fatal(err)
		}
		deadline := t0.Add(2 * time.Minute)
		for latest.Load().(string) != rel {
			if time.Now().After(deadline) {
				t.Fatalf("save %d: %s never served", i, rel)
			}
			time.Sleep(time.Millisecond)
		}
		firstNew := time.Now()
		sw := swap{Release: rel, WriteMs: ms(t0.Sub(begin)), FirstNewMs: ms(firstNew.Sub(begin)), Events: ev.describe(m, ordersTile, begin)}
		if at, ok := ev.first(m, "reload", ordersTile); ok {
			sw.ReloadMs = ms(at.Sub(begin))
		}
		if at, ok := ev.first(m, "build-start", ordersTile); ok {
			sw.BuildStartMs = ms(at.Sub(begin))
		}
		if at, ok := ev.first(m, "build-ok", ordersTile); ok {
			sw.BuildOKMs = ms(at.Sub(begin))
		}
		swaps = append(swaps, sw)
		time.Sleep(3 * time.Second) // the old generation drains and goes
	}
	time.Sleep(2 * time.Second)
	cancel()
	wg.Wait()

	// every request, gzipped CSV; the failures and the slowest in the summary
	var all []req
	for _, l := range logs {
		all = append(all, l...)
	}
	slices.SortFunc(all, func(a, b req) int { return int(a.startUs - b.startUs) })
	f, err := os.Create(filepath.Join(outDir(t), "swap-requests.csv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	bw := bufio.NewWriter(zw)
	fmt.Fprintln(bw, "start_us,latency_us,worker,kind,status,release,error")
	for _, x := range all {
		fmt.Fprintf(bw, "%d,%d,%d,%c,%d,%s,%q\n", x.startUs, x.latUs, x.worker, x.kind, x.status, x.release, x.err)
	}
	bw.Flush()
	zw.Close()
	f.Close()

	var failed []req
	count := map[byte]int{}
	lat := map[byte][]float64{}
	// the swap windows: from each build-ok to 1 s after the first answer of
	// the new release (the old generation stops in there)
	inWindow := func(x req) bool {
		s := float64(x.startUs) / 1000
		for _, sw := range swaps {
			if s >= sw.BuildOKMs-1000 && s <= sw.FirstNewMs+1000 {
				return true
			}
		}
		return false
	}
	var winLat, restLat []float64
	// a GET that answered an older release after a newer one was first seen
	stale := 0
	for _, x := range all {
		count[x.kind]++
		l := float64(x.latUs) / 1000
		lat[x.kind] = append(lat[x.kind], l)
		if x.status < 200 || x.status >= 300 || x.err != "" {
			failed = append(failed, x)
		}
		if inWindow(x) {
			winLat = append(winLat, l)
		} else {
			restLat = append(restLat, l)
		}
	}
	for i, sw := range swaps {
		older := "r0b"
		if i > 0 {
			older = swaps[i-1].Release
		}
		for _, x := range all {
			if x.kind == 'G' && x.status == 200 && float64(x.startUs)/1000 > sw.FirstNewMs+50 && x.release == older {
				stale++
			}
		}
	}
	slowest := slices.Clone(all)
	slices.SortFunc(slowest, func(a, b req) int { return int(b.latUs - a.latUs) })
	if len(slowest) > 20 {
		slowest = slowest[:20]
	}
	asRows := func(rs []req) []map[string]any {
		var out []map[string]any
		for _, x := range rs {
			out = append(out, map[string]any{"startMs": float64(x.startUs) / 1000, "latencyMs": float64(x.latUs) / 1000,
				"kind": string(x.kind), "status": x.status, "release": x.release, "err": x.err})
		}
		return out
	}
	p := func(v []float64, q float64) float64 {
		if len(v) == 0 {
			return 0
		}
		s := slices.Clone(v)
		slices.Sort(s)
		return round3(s[min(len(s)-1, int(q*float64(len(s))))])
	}
	names := map[byte]string{'G': "GET /orders", 'P': "POST /orders (JSON body)"}
	perKind := map[string]any{}
	failedBy := map[byte]int{}
	for _, x := range failed {
		failedBy[x.kind]++
	}
	workers := map[byte]int{}
	for _, k := range kinds {
		workers[k]++
	}
	for k, name := range names {
		perKind[name] = map[string]any{"workers": workers[k], "requests": count[k], "failed": failedBy[k],
			"latencyMs": map[string]float64{"p50": p(lat[k], 0.5), "p99": p(lat[k], 0.99), "p999": p(lat[k], 0.999), "max": p(lat[k], 1)}}
	}
	sum := map[string]any{
		"byKind":           perKind,
		"requests":         len(all),
		"failed":           len(failed),
		"failures":         asRows(failed[:min(len(failed), 200)]),
		"staleAfterSwitch": stale,
		"latencyMs": map[string]any{
			"inSwapWindows":      map[string]float64{"n": float64(len(winLat)), "p50": p(winLat, 0.5), "p99": p(winLat, 0.99), "max": p(winLat, 1)},
			"outsideSwapWindows": map[string]float64{"n": float64(len(restLat)), "p50": p(restLat, 0.5), "p99": p(restLat, 0.99), "max": p(restLat, 1)},
		},
		"slowest":    asRows(slowest),
		"swaps":      swaps,
		"durationMs": round3(ms(time.Since(begin))),
	}
	b, _ := json.MarshalIndent(sum, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir(t), "swap.summary.json"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("swap: %d requests over %d saves: %d failed (GET %d of %d, POST %d of %d), %d stale; max latency GET %.2f, POST %.2f ms; in swap windows max %.2f ms, outside max %.2f ms",
		len(all), len(swaps), len(failed), failedBy['G'], count['G'], failedBy['P'], count['P'], stale,
		p(lat['G'], 1), p(lat['P'], 1), p(winLat, 1), p(restLat, 1))
	for _, x := range failed[:min(len(failed), 10)] {
		t.Logf("failed: %+v", x)
	}
}
