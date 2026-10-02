//go:build linux && xbinmeasure

package measure

// browser_test.go — the measurements a person sees in the browser (real
// Chromium via Playwright, browser.js):
//
//   - a static tile's save on screen: save → the new page painted in the
//     tile's window in the shell (direct to xbind);
//   - predictive echo: with hack/demo/latency in front of xbind (+150 ms
//     each way, a 300 ms round trip), keydown → the typed glyph in the
//     terminal, prediction auto / on / off.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	browserStaticN = quick(30, 3)
	browserKeysN   = quick(40, 4)
)

// paintProbe goes in the handbook's <head>: the page writes the wall-clock
// time of its first contentful paint on <html data-fcp>.
const paintProbe = `<script>
new PerformanceObserver((l) => { for (const e of l.getEntries()) if (e.name === 'first-contentful-paint') document.documentElement.dataset.fcp = String(performance.timeOrigin + e.startTime); })
  .observe({ type: 'paint', buffered: true });
</script>
`

func TestBrowser(t *testing.T) {
	pwDir := os.Getenv("PLAYWRIGHT_DIR")
	if pwDir == "" {
		t.Skip("PLAYWRIGHT_DIR unset (a checkout with playwright in node_modules)")
	}
	d := daemon(t)
	const user, pass = "mara", "pw-mara-6b1f04"
	d.AddUser(t, user, pass, "admin", nil)
	writeHandbook(t, d, paintProbe)
	waitOK(t, d.URL+"/c/"+handbookTile+"/", owner(d), 30*time.Second)
	out := outDir(t)
	repo := d.A.Repo

	// the saves: index.html versions, rendered here (the headline changes)
	dir := t.TempDir()
	var versions []map[string]string
	for i := 0; i <= browserStaticN; i++ {
		h := fmt.Sprintf("Welcome to Northwind — revision %d", i+2)
		f := filepath.Join(dir, fmt.Sprintf("index-%d.html", i))
		if err := os.WriteFile(f, []byte(handbookIndex(h, "Updated just now", paintProbe)), 0o644); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, map[string]string{"file": f, "headline": h})
	}
	vfile := filepath.Join(dir, "versions.json")
	b, _ := json.Marshal(versions)
	if err := os.WriteFile(vfile, b, 0o644); err != nil {
		t.Fatal(err)
	}

	// the latency proxy, built from this tree
	proxyBin := filepath.Join(t.TempDir(), "latency")
	build := exec.Command("go", "build", "-o", proxyBin, "./hack/demo/latency")
	build.Dir = repo
	if o, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the latency proxy: %v\n%s", err, o)
	}
	proxyAddr := addr(2)
	proxyLog, err := os.Create(filepath.Join(out, "latency-proxy.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLog.Close()
	proxy := exec.Command(proxyBin, "-listen", proxyAddr, "-to", d.Addr, "-delay", "150ms")
	proxy.Stdout, proxy.Stderr = proxyLog, proxyLog
	if err := proxy.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Process.Kill(); _ = proxy.Wait() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if c, err := net.Dial("tcp", proxyAddr); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the latency proxy never listened on %s", proxyAddr)
		}
	}
	rtt := proxyRTT(t, "http://"+proxyAddr)

	rec := record(t, "browser")
	extra := map[string]any{"proxy": map[string]any{"delayEachWay": "150ms", "httpRoundTripsMs": rtt}}
	node := func(which, url string) {
		cmd := exec.Command("node", filepath.Join(repo, "hack", "demo", "measure", "browser.js"), which)
		cmd.Env = append(os.Environ(), "URL="+url, "MEASURE_OUT="+out, "MEASURE_WS="+d.WS, "OUT="+filepath.Join(out, "shots"),
			"MEASURE_USER="+user, "MEASURE_PASSWORD="+pass, "MEASURE_VERSIONS="+vfile, fmt.Sprintf("MEASURE_KEYS=%d", browserKeysN))
		o, err := cmd.CombinedOutput()
		if werr := os.WriteFile(filepath.Join(out, "browser-"+which+".log"), o, 0o644); werr != nil {
			t.Error(werr)
		}
		if err != nil {
			t.Fatalf("browser.js %s: %v\n%s", which, err, tail(string(o)))
		}
	}

	t.Run("static", func(t *testing.T) {
		node("static", d.URL)
		for i, row := range readJSONL(t, filepath.Join(out, "browser-static.jsonl")) {
			series := "static: save → new page painted in the shell"
			if i == 0 {
				series = "static: first save (discarded)"
			}
			rec.add(map[string]any{"phase": "static", "i": row["i"], "navStartMs": row["navStartMs"], "responseEndMs": row["responseEndMs"], "dclMs": row["dclMs"]},
				map[string]float64{series: num(row["visibleMs"])})
			if i > 0 {
				rec.add(nil, map[string]float64{"static: save → window starts loading": num(row["navStartMs"])})
			}
		}
	})

	t.Run("predict", func(t *testing.T) {
		node("predict", "http://"+proxyAddr)
		for _, row := range readJSONL(t, filepath.Join(out, "browser-predict.jsonl")) {
			if row["summary"] == true {
				extra["terminalRTT"] = row
				continue
			}
			if row["err"] != nil {
				rec.add(map[string]any{"phase": "predict", "mode": row["mode"], "ch": row["ch"], "err": row["err"]}, nil)
				t.Errorf("predict %v %v: %v", row["mode"], row["ch"], row["err"])
				continue
			}
			rec.add(map[string]any{"phase": "predict", "mode": row["mode"], "ch": row["ch"], "via": row["via"], "rtt": row["rtt"]},
				map[string]float64{fmt.Sprintf("predict %v: keydown → glyph in the DOM", row["mode"]): num(row["glyphDomMs"]),
					fmt.Sprintf("predict %v: keydown → next frame after it", row["mode"]): num(row["glyphFrameMs"])})
		}
	})
	rec.done(extra)
}

// proxyRTT times ten small requests through the proxy (a new connection
// each: connect plus one exchange), so the added latency is on record.
func proxyRTT(t *testing.T, url string) []float64 {
	t.Helper()
	var out []float64
	for range 10 {
		tr := &http.Transport{DisableKeepAlives: true}
		c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
		t0 := time.Now()
		resp, err := c.Get(url + "/healthz")
		if err != nil {
			t.Fatalf("through the proxy: %v", err)
		}
		resp.Body.Close()
		out = append(out, round3(ms(time.Since(t0))))
		tr.CloseIdleConnections()
	}
	t.Logf("GET /healthz through the proxy, a new connection each (the request +150 ms, the answer +150 ms): %v ms", out)
	return out
}

func readJSONL(t *testing.T, file string) []map[string]any {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		rows = append(rows, m)
	}
	return rows
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
