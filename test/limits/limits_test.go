//go:build linux && integration

// Package limits drives tile resource limits end to end (D203,
// plans/tile-limits.md §10, the wire in §W): an `xbind --isolate` built
// from this tree (test/xbindtest), a Go backend that allocates on demand
// and writes a blob, and everything through xbind's HTTP API as the owner.
//
//   - memory: XBIN_LIMIT_MEM=96M. A backend OOM-killed at its cap leaves
//     the backend log's line, and a sticky oom alert with a raise action;
//     POST /limits +100 % with restart brings it back onto 192 MiB, where
//     the allocation that killed it succeeds; a raise on a running backend
//     resizes its leaf live, the same generation serving.
//   - disk: XBIN_LIMIT_DISK=8M. A scope over its quota is write-blocked
//     (507) and alerted with a raise action at the next scan; a raise kicks
//     a rescan, and writes succeed at once.
//
// A delegated suite (hack/integration.jsonc): the test binary runs alone in
// a Delegate=yes scope, moves itself into a leaf of it and starts each
// xbind in a sibling cgroup with swap off — so an allocation past the cap
// is an OOM kill, not a swap-out. Without delegation the memory tests skip
// saying why; the disk test needs none. Skips without user namespaces or
// the rootfs (and the disk test without gocryptfs: blobs are encrypted).
//
//	go test -c -tags=integration -o /tmp/limits.test ./test/limits/
//	systemd-run --user --scope -p Delegate=yes --quiet -- /tmp/limits.test -test.v
//
// On a dev box run it with the Bash sandbox disabled.
package limits

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestMain(m *testing.M) { xbindtest.Main(m) }

// ---- the delegated cgroup ----

var scope struct {
	once sync.Once
	dir  string // the scope's cgroup, the test binary moved out of it
	why  string // why there is none
	mu   sync.Mutex
	n    int
}

// xbindCgroup is a fresh cgroup for one xbind beside the test binary, swap
// off; it skips the test without a delegated scope the test binary is alone
// in (never touching a cgroup it shares).
func xbindCgroup(t *testing.T) string {
	t.Helper()
	scope.once.Do(func() { scope.dir, scope.why = takeScope() })
	if scope.dir == "" {
		t.Skip("no delegated cgroup: " + scope.why + " (run it as a delegated suite: make integration, or under systemd-run --user --scope -p Delegate=yes)")
	}
	scope.mu.Lock()
	scope.n++
	dir := filepath.Join(scope.dir, fmt.Sprintf("xbind-%d", scope.n))
	scope.mu.Unlock()
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("0"), 0o644); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("swap off for xbind's cgroup: %v", err)
	}
	t.Cleanup(func() { pruneCgroup(dir) })
	return dir
}

// takeScope moves the test binary into a leaf of its own scope and enables
// the controllers for the scope's children, when the scope is delegated and
// holds the test binary alone.
func takeScope() (dir, why string) {
	b, err := os.ReadFile("/proc/self/cgroup")
	rel, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "0::")
	if err != nil || !ok {
		return "", "no cgroup v2 path in /proc/self/cgroup"
	}
	dir = filepath.Join("/sys/fs/cgroup", rel)
	if procs := strings.Fields(readStr(filepath.Join(dir, "cgroup.procs"))); !slices.Equal(procs, []string{strconv.Itoa(os.Getpid())}) {
		return "", fmt.Sprintf("the test binary isn't alone in its cgroup %s (%d processes)", rel, len(procs))
	}
	runner := filepath.Join(dir, "runner")
	if err := os.Mkdir(runner, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", "its cgroup isn't writable: " + err.Error()
	}
	if err := os.WriteFile(filepath.Join(runner, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		_ = os.Remove(runner)
		return "", "can't move the test binary into a leaf: " + err.Error()
	}
	sc := filepath.Join(dir, "cgroup.subtree_control")
	if os.WriteFile(sc, []byte("+cpu +memory +pids"), 0o644) != nil {
		for _, c := range []string{"+cpu", "+memory", "+pids"} {
			_ = os.WriteFile(sc, []byte(c), 0o644)
		}
	}
	if !strings.Contains(readStr(sc), "memory") {
		return "", "the memory controller isn't delegated: " + readStr(sc)
	}
	return dir, ""
}

// pruneCgroup removes dir's empty cgroups bottom-up, and dir.
func pruneCgroup(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() {
			pruneCgroup(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir)
}

func readStr(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }

// ---- the fixture ----

// hogSource is the fixture's backend: GET /nonce names its process (a new
// one per start); GET /hog?mb=N maps N MiB populated, held for the request
// (MAP_POPULATE: charged in the one call — memory.high, ⅞ of the cap, still
// throttles it some 30 s before the cap's kill; touching page by page took
// minutes); PUT /fill?mb=N&name= writes N MiB to its blob resource and
// answers the broker's status.
const hogSource = `package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"syscall"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

var nonce = strconv.FormatInt(time.Now().UnixNano(), 36)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nonce", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, nonce) })
	mux.HandleFunc("GET /hog", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("mb"))
		b, err := syscall.Mmap(-1, 0, n<<20, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE|syscall.MAP_POPULATE)
		if err != nil {
			xbin.WriteError(w, 500, err.Error())
			return
		}
		defer syscall.Munmap(b)
		fmt.Fprintf(w, "held %d MiB", n)
	})
	mux.HandleFunc("PUT /fill", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("mb"))
		url := "http://xbin/api/xbin/blob/" + xbin.Resource("files") + "/" + r.URL.Query().Get("name")
		req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(make([]byte, n<<20)))
		resp, err := xbin.Client().Do(req)
		if err != nil {
			xbin.WriteError(w, 502, err.Error())
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		w.WriteHeader(resp.StatusCode)
		w.Write(b)
	})
	xbin.Serve(mux)
}
`

// writeHog writes the fixture at tile; blob, with a blob resource it may
// write.
func writeHog(t *testing.T, d *xbindtest.Daemon, tile string, blob bool) {
	t.Helper()
	files := map[string]string{
		"go.mod":          "module hog\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": hogSource,
		"xbin.json":       `{"runtime":"go"}`,
	}
	if blob {
		files["scope.json"] = `{"resources":{"files":{"type":"blob"}}}`
		files["xbin.json"] = `{"runtime":"go","uses":[{"target":"res:` + tile + `/files","role":"writer"}]}`
	}
	d.WriteTile(t, tile, files)
}

// nonce is tile's backend process's name, starting it when it isn't up.
func nonce(t *testing.T, d *xbindtest.Daemon, tile string) string {
	t.Helper()
	var n string
	xbindtest.Eventually(t, 3*time.Minute, tile+"'s backend up", func() (bool, string) {
		r := d.Call(t, http.MethodGet, "/api/"+tile+"/nonce", nil)
		n = string(r.Body)
		return r.Status == 200 && n != "", r.String()
	})
	return n
}

// leafDir is the cgroup directory of tile's main backend leaf under base,
// from its sandbox row.
func leafDir(t *testing.T, d *xbindtest.Daemon, base, tile string) string {
	t.Helper()
	var leaf string
	xbindtest.Eventually(t, time.Minute, tile+"'s backend row with a leaf", func() (bool, string) {
		var out struct {
			Sandboxes []struct {
				Kind, Deployment, Leaf string
			} `json:"sandboxes"`
		}
		r := d.Call(t, http.MethodGet, "/api/xbin/sandboxes?tile="+tile, nil)
		if r.Status != 200 || json.Unmarshal(r.Body, &out) != nil {
			return false, r.String()
		}
		for _, s := range out.Sandboxes {
			if s.Kind == "backend" && s.Deployment == "" && s.Leaf != "" {
				leaf = s.Leaf
				return true, ""
			}
		}
		return false, r.String()
	})
	if !strings.Contains(leaf, "/") {
		return filepath.Join(base, "comp-"+leaf)
	}
	return filepath.Join(base, leaf)
}

// alert is one of GET /alerts' entries, with the action of §W.
type alert struct {
	Level, Kind, Tile, Message, Dismiss string
	Action                              *struct {
		Action, Tile, Scope, Kind, Unit string
		Current                         int64
		Approvers                       []string
		Choices                         []struct {
			Pct        int
			Value      int64
			Fits       bool
			Approvable bool
			Why        string
		}
	}
}

func alerts(t *testing.T, d *xbindtest.Daemon) []alert {
	t.Helper()
	var out struct{ Alerts []alert }
	d.Must(t, http.MethodGet, "/api/xbin/alerts", nil, 200).Decode(t, &out)
	return out.Alerts
}

func findAlert(as []alert, kind, tile string) *alert {
	for i := range as {
		if as[i].Kind == kind && as[i].Tile == tile {
			return &as[i]
		}
	}
	return nil
}

// row is GET /limits?tile=' row, the kinds this test reads.
type row struct {
	Tile   string
	Limits map[string]struct {
		Effective, Default int64
		Applied            int64
		AppliesAt          string
		Hit                *struct{ Count int }
	}
}

// raise POSTs a raise of tile's kind to value (§W) and returns the row.
func raise(t *testing.T, d *xbindtest.Daemon, tile, kind string, value int64, restart bool) row {
	t.Helper()
	var out struct{ Tile row }
	d.Must(t, http.MethodPost, "/api/xbin/limits", map[string]any{"tile": tile, "kind": kind, "value": value, "source": "raise", "restart": restart}, 200).Decode(t, &out)
	return out.Tile
}

// ---- memory ----

// covers D203 — a backend's memory cap, end to end: the OOM path (an
// allocation past 96 MiB kills the backend; the backend log says so; a
// sticky oom alert offers +25/+50/+100 %; a raise to 192 MiB with restart
// clears it, the backend comes back onto the raised leaf, and the
// allocation succeeds) and the live raise (the leaf grows at once, the
// same process serving).
func TestMemoryLimits(t *testing.T) {
	a := xbindtest.Require(t)
	base := xbindCgroup(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Cgroup: base, Env: []string{"XBIN_LIMIT_MEM=96M"}})

	t.Run("oom, raise, restart", func(t *testing.T) {
		const tile = "apps/hog"
		writeHog(t, d, tile, false)
		first := nonce(t, d, tile)
		leaf := leafDir(t, d, base, tile)
		if got := readStr(filepath.Join(leaf, "memory.max")); got != strconv.Itoa(96<<20) {
			t.Fatalf("the leaf's memory.max: %s, want 96 MiB", got)
		}
		// past the cap: throttled at memory.high, then OOM-killed
		if r := d.Call(t, http.MethodGet, "/api/"+tile+"/hog?mb=150", nil); r.Status == 200 {
			t.Fatalf("150 MiB in a 96 MiB leaf answered %d %s", r.Status, r)
		}
		logFile := filepath.Join(d.WS, ".xbin", "log", util.CompKey(tile)+".log")
		xbindtest.Eventually(t, 30*time.Second, "the backend log's OOM line", func() (bool, string) {
			b := readStr(logFile)
			return strings.Contains(b, ": OOM-killed at memory.max 96 MiB ---"), b
		})
		for _, ln := range strings.Split(readStr(logFile), "\n") {
			if strings.HasPrefix(ln, "--- ") {
				t.Log("backend log: " + ln)
			}
		}

		// the store's half (W1): a sticky alert with the raise action
		var oom *alert
		xbindtest.Eventually(t, 30*time.Second, "the sticky oom alert", func() (bool, string) {
			as := alerts(t, d)
			oom = findAlert(as, "oom", tile)
			return oom != nil, fmt.Sprintf("%+v", as)
		})
		switch act := oom.Action; {
		case act == nil:
			t.Fatalf("the oom alert has no action: %+v", *oom)
		case act.Action != "raise-limit" || act.Kind != "memMiB" || act.Unit != "MiB" || act.Current != 96 || act.Tile != tile:
			t.Errorf("the action: %+v", *act)
		case len(act.Choices) != 3 || act.Choices[2].Pct != 100 || act.Choices[2].Value != 192:
			t.Errorf("the choices: %+v, want +25/+50/+100 %%, +100 %% at 192 MiB", act.Choices)
		case !strings.Contains(oom.Message, "/xbin/limits") || !strings.Contains(oom.Message, "bx limits raise "+tile+" memory"):
			t.Errorf("the message names no way to raise it: %q", oom.Message)
		}
		if again := findAlert(alerts(t, d), "oom", tile); again == nil {
			t.Error("the oom alert isn't sticky")
		}

		r := raise(t, d, tile, "memMiB", 192, true)
		if l := r.Limits["memMiB"]; l.Effective != 192 || l.Default != 96 || l.Hit != nil {
			t.Errorf("the row after the raise: %+v", l)
		}
		if findAlert(alerts(t, d), "oom", tile) != nil {
			t.Error("the oom alert outlived the raise")
		}
		if n := nonce(t, d, tile); n == first {
			t.Error("the backend didn't restart")
		}
		if got := readStr(filepath.Join(leafDir(t, d, base, tile), "memory.max")); got != strconv.Itoa(192<<20) {
			t.Errorf("the restarted leaf's memory.max: %s, want 192 MiB", got)
		}
		if r := d.Call(t, http.MethodGet, "/api/"+tile+"/hog?mb=150", nil); r.Status != 200 {
			t.Errorf("150 MiB under the raised 192: %d %s", r.Status, r)
		}
	})

	t.Run("a live raise keeps the generation", func(t *testing.T) {
		const tile = "apps/live"
		writeHog(t, d, tile, false)
		first := nonce(t, d, tile)
		leaf := leafDir(t, d, base, tile)
		r := raise(t, d, tile, "memMiB", 160, false)
		if l := r.Limits["memMiB"]; l.Effective != 160 || l.Applied != 0 && l.Applied != 160 {
			t.Errorf("the row: %+v", l)
		}
		xbindtest.Eventually(t, 10*time.Second, "the live leaf raised", func() (bool, string) {
			got := readStr(filepath.Join(leaf, "memory.max"))
			return got == strconv.Itoa(160<<20), got
		})
		if got := readStr(filepath.Join(leaf, "memory.high")); got != strconv.Itoa(160<<20-160<<20/8) {
			t.Errorf("memory.high: %s, want ⅞ of the raise", got)
		}
		if n := nonce(t, d, tile); n != first {
			t.Error("a live raise restarted the backend")
		}
	})
}

// ---- disk ----

// covers D203 — a scope's data quota, end to end: a blob write past the
// 8 MiB quota lands (the block is the scan's verdict), the next scan blocks
// the scope's writes (507) and alerts with a raise action naming the scope
// and its root tile; a raise kicks a rescan and writes succeed at once.
func TestDiskQuotaRaise(t *testing.T) {
	a := xbindtest.Require(t)
	if a.Gocryptfs == "" {
		t.Skip("no gocryptfs: blob resources are held without it ($XBIN_GOCRYPTFS, make helpers)")
	}
	d := xbindtest.Start(t, a, xbindtest.Options{Env: []string{"XBIN_LIMIT_DISK=8M", "XBIN_VAULT_PASSPHRASE=xbindtest-vault"}})
	const tile = "apps/disk"
	writeHog(t, d, tile, true)
	nonce(t, d, tile)
	if r := d.Call(t, http.MethodPut, "/api/"+tile+"/fill?mb=10&name=a", nil); r.Status != 200 {
		t.Fatalf("the first 10 MiB: %d %s", r.Status, r)
	}
	// the scan (every 45 s) blocks the scope
	n := 0
	xbindtest.Eventually(t, 2*time.Minute, "the scope write-blocked", func() (bool, string) {
		n++
		r := d.Call(t, http.MethodPut, fmt.Sprintf("/api/%s/fill?mb=1&name=b%d", tile, n), nil)
		return r.Status == http.StatusInsufficientStorage, r.String()
	})
	q := findAlert(alerts(t, d), "quota", util.ScopeKey(tile))
	switch {
	case q == nil:
		t.Fatalf("no quota alert for %s: %+v", tile, alerts(t, d))
	case q.Level != "crit" || q.Action == nil:
		t.Fatalf("the quota alert: %+v", *q)
	case q.Action.Kind != "diskGiB" || q.Action.Unit != "GiB" || q.Action.Scope != tile || q.Action.Tile != tile:
		t.Errorf("the action: %+v", *q.Action)
	case !strings.Contains(q.Message, "bx limits raise "+tile+" disk"):
		t.Errorf("the message names no way to raise it: %q", q.Message)
	}

	// the raise the alert offers (+100 % of 1 GiB: the 8 MiB default reads
	// 1 GiB, rounded up as every disk value is), and the kick that lifts
	// the 507
	if q.Action.Current != 1 {
		t.Errorf("the action's current: %d GiB, want 1 (8 MiB rounded up)", q.Action.Current)
	}
	raise(t, d, tile, "diskGiB", 2, false)
	xbindtest.Eventually(t, 10*time.Second, "writes after the raise", func() (bool, string) {
		n++
		r := d.Call(t, http.MethodPut, fmt.Sprintf("/api/%s/fill?mb=1&name=b%d", tile, n), nil)
		return r.Status == 200, r.String()
	})
	if findAlert(alerts(t, d), "quota", util.ScopeKey(tile)) != nil {
		t.Error("the quota alert outlived the raise's rescan")
	}
}
