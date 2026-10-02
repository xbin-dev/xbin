//go:build linux && xbinmeasure

package measure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestMain(m *testing.M) { xbindtest.Main(m) }

// quick is full, or few under MEASURE_QUICK=1 (a run that checks the setup).
func quick(full, few int) int {
	if os.Getenv("MEASURE_QUICK") == "1" {
		return few
	}
	return full
}

// envInt is $name as a positive number, else def.
func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// outDir is where the raw data goes: $MEASURE_OUT, else a directory under
// $TMPDIR (said so).
func outDir(t testing.TB) string {
	t.Helper()
	dir := os.Getenv("MEASURE_OUT")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "xbin-measure")
		t.Logf("MEASURE_OUT unset: raw data goes to %s", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// addr is the n-th address this run may listen on: 127.0.0.1:$MEASURE_PORT+n
// ($MEASURE_PORT defaults to 9341, the port this work was given).
func addr(n int) string {
	base := 9341
	if p, err := strconv.Atoi(os.Getenv("MEASURE_PORT")); err == nil && p > 0 {
		base = p
	}
	return fmt.Sprintf("127.0.0.1:%d", base+n)
}

// samples is a recorder: one JSON object per line in <out>/<name>.jsonl,
// every sample as it was taken, plus the series it summarizes.
type samples struct {
	t    testing.TB
	name string
	mu   sync.Mutex
	f    *os.File
	ser  map[string][]float64 // series → ms
	keys []string             // series in first-seen order
}

func record(t testing.TB, name string) *samples {
	t.Helper()
	f, err := os.Create(filepath.Join(outDir(t), name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := &samples{t: t, name: name, f: f, ser: map[string][]float64{}}
	t.Cleanup(func() { f.Close() })
	return s
}

// add writes one sample line (with its wall time and the host's load) and,
// for each series named in ms, adds the value.
func (s *samples) add(fields map[string]any, ms map[string]float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	line := map[string]any{"at": time.Now().UnixMilli(), "load1": load1()}
	for k, v := range fields {
		line[k] = v
	}
	keys := make([]string, 0, len(ms))
	for k := range ms {
		keys = append(keys, k)
	}
	slices.Sort(keys) // a stable order for the series one sample adds
	for _, k := range keys {
		v := ms[k]
		line[k+"Ms"] = round3(v)
		if _, ok := s.ser[k]; !ok {
			s.keys = append(s.keys, k)
		}
		s.ser[k] = append(s.ser[k], v)
	}
	b, _ := json.Marshal(line)
	s.f.Write(append(b, '\n'))
}

// summary is one series' statistics, in ms.
type summary struct {
	Series string  `json:"series"`
	N      int     `json:"n"`
	Min    float64 `json:"minMs"`
	Median float64 `json:"medianMs"`
	P90    float64 `json:"p90Ms"`
	Max    float64 `json:"maxMs"`
	Mean   float64 `json:"meanMs"`
}

// stats: the median (the mean of the middle two for an even n), the p90 by
// nearest rank (the ⌈0.9·n⌉-th smallest: never interpolated down), min,
// max and mean.
func stats(name string, v []float64) summary {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	out := summary{Series: name, N: n}
	if n == 0 {
		return out
	}
	out.Min, out.Max = s[0], s[n-1]
	if n%2 == 1 {
		out.Median = s[n/2]
	} else {
		out.Median = (s[n/2-1] + s[n/2]) / 2
	}
	out.P90 = s[int(math.Ceil(0.9*float64(n)))-1]
	var sum float64
	for _, x := range s {
		sum += x
	}
	out.Mean = sum / float64(n)
	out.Min, out.Median, out.P90, out.Max, out.Mean = round3(out.Min), round3(out.Median), round3(out.P90), round3(out.Max), round3(out.Mean)
	return out
}

func (s summary) String() string {
	return fmt.Sprintf("%-34s n=%-3d min %8.1f  median %8.1f  p90 %8.1f  max %8.1f  (ms)", s.Series, s.N, s.Min, s.Median, s.P90, s.Max)
}

// done logs every series' summary and writes them, with extra facts, to
// <out>/<name>.summary.json.
func (s *samples) done(extra map[string]any) []summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []summary
	for _, k := range s.keys {
		st := stats(k, s.ser[k])
		s.t.Logf("%s: %s", s.name, st)
		out = append(out, st)
	}
	doc := map[string]any{"name": s.name, "series": out, "finished": time.Now().Format(time.RFC3339)}
	for k, v := range extra {
		doc[k] = v
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir(s.t), s.name+".summary.json"), append(b, '\n'), 0o644); err != nil {
		s.t.Error(err)
	}
	return out
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// load1 is the host's one-minute load average (other work on the box shows
// in the samples).
func load1() float64 {
	b, _ := os.ReadFile("/proc/loadavg")
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return -1
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// memAvailableMiB is the host's MemAvailable.
func memAvailableMiB() int64 {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "MemAvailable:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				return kb / 1024
			}
		}
	}
	return -1
}

// daemon boots an isolated xbind from this tree on the run's first port,
// owner auth on and the vault unsealed (encrypted resources work).
func daemon(t *testing.T, extraEnv ...string) *xbindtest.Daemon {
	t.Helper()
	a := xbindtest.Require(t)
	env := append([]string{"XBIN_VAULT_PASSPHRASE=measure-vault-4d1c"}, extraEnv...)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Env: env, Addr: addr(0), Ready: 2 * time.Minute})
	run, err := os.Readlink(filepath.Join(d.WS, ".xbin", "run")) // a symlink to the tmpfs run dir xbind picked
	if err != nil {
		run = "the workspace's own .xbin/run (no tmpfs found)"
	}
	t.Logf("xbind %s on %s, workspace %s, run dir %s, log %s", a.Bin, d.URL, d.WS, run, d.LogPath())
	// xbind's log goes with the raw data (xbindtest removes it after a
	// passing test; this cleanup runs before that one)
	t.Cleanup(func() {
		if b, err := os.ReadFile(d.LogPath()); err == nil {
			name := strings.NewReplacer("/", "-", " ", "_").Replace(t.Name())
			_ = os.WriteFile(filepath.Join(outDir(t), name+".xbind.log"), b, 0o644)
		}
	})
	return d
}

// client is the HTTP client every timed request uses: keep-alive, as a
// browser or a tile's backend talks to xbind.
var client = &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: 256, MaxConnsPerHost: 0}}

// call sends one request (a JSON body, or raw bytes) with the headers given,
// and returns its status and body; err is a transport failure.
func call(method, url string, body any, hdr map[string]string) (int, []byte, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// owner is the owner token's header.
func owner(d *xbindtest.Daemon) map[string]string {
	return map[string]string{"Authorization": "Bearer " + d.Token()}
}

// frame is a header carrying a frame token of tile minted by the bearer of
// hdr (the owner's when nil): the tile's page calling.
func frame(t testing.TB, d *xbindtest.Daemon, tile string, hdr map[string]string) map[string]string {
	t.Helper()
	if hdr == nil {
		hdr = owner(d)
	}
	st, b, err := call("GET", d.URL+"/api/xbin/frame-token?component="+tile, nil, hdr)
	var out struct{ Token string }
	if err != nil || st != 200 || json.Unmarshal(b, &out) != nil || out.Token == "" {
		t.Fatalf("a frame token of %s: %d %s %v", tile, st, cut(b), err)
	}
	return map[string]string{"X-XBin-Frame-Token": out.Token}
}

func cut(b []byte) string {
	s := string(b)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return strings.TrimSpace(s)
}

// waitOK polls url (GET, headers hdr) every 100 ms until it answers 200,
// failing after timeout.
func waitOK(t testing.TB, url string, hdr map[string]string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st, b, err := call("GET", url, nil, hdr)
		if err == nil && st == 200 {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: not 200 within %s (last %d %s %v)", url, timeout, st, cut(b), err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// descendants counts the processes under pid whose comm is name (the VMMs
// one xbind runs), walking /proc's ppid links.
func descendants(pid int, name string) int {
	kids := map[int][]int{}
	comm := map[int]string{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if i < 0 || j < i {
			continue
		}
		comm[p] = s[i+1 : j]
		f := strings.Fields(s[j+1:])
		if len(f) >= 2 {
			pp, _ := strconv.Atoi(f[1])
			kids[pp] = append(kids[pp], p)
		}
	}
	n := 0
	var walk func(int)
	walk = func(p int) {
		for _, k := range kids[p] {
			if comm[k] == name {
				n++
			}
			walk(k)
		}
	}
	walk(pid)
	return n
}
