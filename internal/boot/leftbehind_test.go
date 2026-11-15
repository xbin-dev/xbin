package boot

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// leftBehind lists the processes this test process started that still run
// out of dir (their command line or working directory is under it): what a
// daemon that has stopped must not leave. Read from /proc; nothing where
// there is none.
func leftBehind(dir string) []string {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	me := strconv.Itoa(os.Getpid())
	var out []string
	for _, e := range ents {
		pid := e.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
		if err != nil {
			continue
		}
		// "<pid> (<comm>) <state> <ppid> …": comm may hold spaces and parens
		rest := string(stat)
		if i := strings.LastIndexByte(rest, ')'); i >= 0 {
			rest = rest[i+1:]
		}
		f := strings.Fields(rest)
		if len(f) < 2 || f[1] != me || f[0] == "Z" { // not ours, or a zombie already gone
			continue
		}
		cmd, _ := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		cwd, _ := os.Readlink(filepath.Join("/proc", pid, "cwd"))
		line := strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " "))
		if strings.Contains(line, dir) || strings.HasPrefix(cwd, dir) {
			out = append(out, pid+" "+line)
		}
	}
	return out
}

// noneLeftBehind fails t when the daemon that served ws has stopped and a
// backend it started still runs. Every in-process boot harness calls it
// after its daemon returned (zsBootWith, startSealed), so every test built
// on them holds xbind to it: a stopped xbind leaves no backend running.
func noneLeftBehind(t *testing.T, ws string) {
	t.Helper()
	var left []string
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if left = leftBehind(ws); len(left) == 0 || time.Now().After(deadline) {
			break // (a process that was signalled is reaped a moment later)
		}
	}
	for _, p := range left {
		t.Errorf("the daemon stopped and left a backend running: %s", p)
		if pid, err := strconv.Atoi(strings.Fields(p)[0]); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill() // not for the next run to find
			}
		}
	}
}

// A backend that is still starting when xbind shuts down is stopped too.
// The tile's backend here takes two seconds to listen; a request spawns it
// and the daemon stops while it is waited for. runner.StopAll once stopped
// only the generations that had become current — after their health check
// — so this one was left running: the orphaned backends short boot tests
// (and a stopped xbind) left behind (runner/live.go; the runner's
// TestStopAllLeavesNothingStarting pins the build side of it).
func TestStopLeavesNoBackendBehind(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node: the fixture's backend is a node one")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"apps/slow/xbin.json":         `{"runtime":"node"}`,
		"apps/slow/index.html":        "<!doctype html><html><head></head><body>slow</body></html>\n",
		"apps/slow/backend/server.js": "setTimeout(() => require('http').createServer((q, s) => s.end('ok')).listen(process.env.XBIN_SOCKET), 2000);\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBootWith(t, ws, nil)
	go func() { // the request that spawns it (cut off by the stop)
		rq, _ := http.NewRequest("GET", d.url+"/api/apps/slow/x", nil)
		rq.Header.Set("Authorization", "Bearer "+d.owner)
		if resp, err := http.DefaultClient.Do(rq); err == nil {
			resp.Body.Close()
		}
	}()
	spawned := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !spawned; time.Sleep(10 * time.Millisecond) {
		for _, p := range leftBehind(ws) {
			spawned = spawned || strings.Contains(p, "apps/slow")
		}
	}
	if !spawned {
		t.Fatal("its backend was never spawned (or leftBehind is blind)")
	}
	d.stop() // checks that nothing is left (noneLeftBehind), as every test's cleanup does
	time.Sleep(2500 * time.Millisecond)
	noneLeftBehind(t, ws) // nor once it would have listened
}
