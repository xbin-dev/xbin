package boot

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/util"
)

// post sends a JSON body with the given bearer.
func (d *zsDaemon) post(t *testing.T, path, body, bearer string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", d.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// zsFiles lists every path under ws (directories with a trailing slash),
// relative, slash-separated.
func zsFiles(t *testing.T, ws string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(ws, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if e.IsDir() {
			if rel == ".git" || strings.HasSuffix(rel, "/.git") {
				return fs.SkipDir
			}
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// zsDeploymentState reports every path that is deployment state, or could
// only be: the three new stores, a dot-level namespace under data/ or
// .xbin/ (no component or scope segment starts with a dot, so only a
// deployment key can produce one), a log, socket directory or build
// directory keyed by anything but a component, a qualified name anywhere
// in xbind's trees.
func zsDeploymentState(t *testing.T, ws string, compKeys map[string]bool) []string {
	t.Helper()
	var bad []string
	for _, rel := range zsFiles(t, ws) {
		switch {
		case strings.HasPrefix(rel, "data/deployments/"), strings.HasPrefix(rel, "data/checkpoints/"),
			strings.HasPrefix(rel, ".xbin/deploy/"):
			bad = append(bad, rel)
			continue
		case !strings.HasPrefix(rel, "data/") && !strings.HasPrefix(rel, ".xbin/"):
			continue
		}
		segs := strings.Split(strings.TrimSuffix(rel, "/"), "/")
		for _, s := range segs[1:] {
			if strings.HasPrefix(s, ".") || strings.Contains(s, "+") {
				bad = append(bad, rel)
				break
			}
		}
		// Per-component runtime files are keyed by CompKey, and only by it.
		for _, dir := range []string{".xbin/log/", ".xbin/build/", ".xbin/run/"} {
			rest, ok := strings.CutPrefix(rel, dir)
			if !ok || rest == "" {
				continue
			}
			key := strings.TrimSuffix(strings.SplitN(rest, "/", 2)[0], ".log")
			if strings.HasSuffix(key, ".sock") || strings.HasSuffix(key, ".pid") {
				continue // xbind's own sockets (gateway.sock) sit in the run dir
			}
			if !compKeys[key] {
				bad = append(bad, rel)
			}
		}
	}
	return bad
}

// zsRunDirState reports what in the runtime socket directory (outside the
// workspace, .xbin/run links to it) is not today's: the gateway socket and
// one directory per component, named by CompKey, holding g<gen>.sock.
func zsRunDirState(t *testing.T, runDir string, compKeys map[string]bool) []string {
	t.Helper()
	sock := regexp.MustCompile(`^g[0-9]+\.sock$`)
	var bad []string
	for _, rel := range zsFiles(t, runDir) {
		dir, name, nested := strings.Cut(strings.TrimSuffix(rel, "/"), "/")
		switch {
		case rel == "gateway.sock":
		case !compKeys[dir]:
			bad = append(bad, "run/"+rel)
		case nested && (strings.Contains(name, "/") || !sock.MatchString(name)):
			bad = append(bad, "run/"+rel)
		}
	}
	return bad
}

// covers PO-7 Z1 Z10 D119f SC-ZERO — boot, a save-and-swap cycle (a static
// tile's save, a node backend rebuilt and swapped to its next generation,
// a failing build), a disable and enable, and reads of the tiles'
// deployment state leave a zero-state workspace with no deployment state:
// no data/deployments/, data/checkpoints/ or .xbin/deploy/, no dot-level
// namespace, no per-deployment log, socket or build directory; the save
// cycle leaves the root xbin.json and every data/*.json byte-identical.
// (No confined run on a save is row 30's and TestWatchLoopZeroStateNoStoreIO's:
// nothing here can observe a run.)
func TestZeroStateCreatesNoDeploymentFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	_, err := exec.LookPath("node")
	haveNode := err == nil
	ws := zsWorkspace(t)
	d := zsBoot(t, ws)
	owner := "Bearer " + d.owner
	keys := map[string]bool{}
	for _, c := range d.st.Reg.Components() {
		keys[util.CompKey(c.Path)] = true
	}
	if haveNode {
		if code, body := d.do(t, "GET", "/api/apps/zsnode/ping", owner); code != 200 {
			t.Fatalf("node backend: %d %s", code, body)
		}
	} else {
		t.Log("SKIP (partial): no node on PATH — the backend swap is not exercised")
	}
	d.do(t, "GET", "/api/apps/zsbroken/ping", owner)
	if bad := append(zsDeploymentState(t, ws, keys), zsRunDirState(t, d.st.Run.RunDir, keys)...); len(bad) > 0 {
		t.Fatalf("boot created deployment state:\n  %s", strings.Join(bad, "\n  "))
	}

	// The save-and-swap cycle.
	stores := func() map[string]string {
		out := map[string]string{}
		matches, _ := filepath.Glob(filepath.Join(ws, "data", "*.json"))
		for _, p := range append(matches, filepath.Join(ws, "xbin.json")) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(ws, p)
			out[rel] = string(b)
		}
		return out
	}
	before := stores()
	gen := func(tile string) (int, string) {
		_, body := d.do(t, "GET", "/api/xbin/backends", owner)
		var bs map[string]struct {
			State string `json:"state"`
			Gen   int    `json:"gen"`
		}
		_ = json.Unmarshal(body, &bs)
		return bs[tile].Gen, bs[tile].State
	}
	appendTo := func(rel, text string) {
		f, err := os.OpenFile(filepath.Join(ws, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	appendTo("apps/zs/index.html", "<!-- saved -->\n")
	appendTo("notes+ideas/index.html", "<!-- saved -->\n")
	if haveNode {
		appendTo("apps/zsnode/backend/server.js", "// saved\n")
		deadline := time.Now().Add(60 * time.Second)
		for {
			if g, st := gen("apps/zsnode"); g >= 2 && st == "healthy" {
				break
			}
			if time.Now().After(deadline) {
				g, st := gen("apps/zsnode")
				t.Fatalf("the saved backend never swapped: gen %d, %s", g, st)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	appendTo("apps/zsbroken/backend/README.txt", "still no server.js\n")
	// A bounded wait for the watcher to settle the other saves (a negative:
	// nothing may appear, so there is no condition to wait for).
	time.Sleep(time.Second)
	after := stores()
	for rel, b := range before {
		if after[rel] != b {
			t.Errorf("the save cycle rewrote %s", rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("the save cycle created %s", rel)
		}
	}

	// Lifecycle and reads of the deployment state, whatever this build
	// answers for them (no route, a reserved one, the zero state).
	for _, st := range []string{"disabled", "enabled"} {
		req := `{"component":"notes+ideas","state":"` + st + `"}`
		if code, body := d.post(t, "/api/xbin/lifecycle", req, owner); code != 200 {
			t.Fatalf("lifecycle %s: %d %s", st, code, body)
		}
	}
	for _, p := range []string{"/api/xbin/deployments?component=apps/zs", "/api/xbin/deployments?component=apps/zsnode",
		"/api/xbin/deployments?component=notes%2Bideas", "/api/xbin/deployments", "/api/xbin/components"} {
		d.do(t, "GET", p, owner)
	}
	if bad := append(zsDeploymentState(t, ws, keys), zsRunDirState(t, d.st.Run.RunDir, keys)...); len(bad) > 0 {
		t.Errorf("a zero-state workspace gained deployment state:\n  %s", strings.Join(bad, "\n  "))
	}
}

// covers D119f T11 PO-7 — the in-process twin of TestDeploymentStateBootsTwice
// (test/deploystate_boot_test.go; 15-test-plan §6): on a fresh workspace the
// feature itself pauses live reload on a static tile and the work tree moves
// on; two boots then change nothing outside derived trees (.xbin/deploy/):
// the record, the checkpoint store, the view repository and every tile
// work tree and repository stay byte-identical. It reuses this package's
// snapshot, changed and bootOnce, and needs host git and GNU find, as the
// store's tools run directly here.
func TestDeploymentStateBootsTwiceInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host")
	}
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "GNU") {
		t.Skip("no GNU find on this host")
	}
	if confine.Isolated() {
		t.Skip("confinement is on: the store's tools run directly here")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	const tile = "apps/ds-paused"
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(tile+"/xbin.json", `{"title":"paused"}`)
	write(tile+"/index.html", "<!doctype html><html><head></head><body>v1</body></html>\n")
	dsServe(t, ws, func(url string) {
		var code int
		for deadline := time.Now().Add(30 * time.Second); code != http.StatusOK && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if r, err := http.Get(url + "/api/xbin/deployments?tile=" + tile); err == nil {
				code = r.StatusCode
				r.Body.Close()
			}
		}
		r, err := http.Post(url+"/api/xbin/deployments/live-reload/pause", "application/json", strings.NewReader(`{"tile":"`+tile+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		var ans struct {
			State  struct{ Record bool }
			Deploy struct{ Result string }
		}
		if r.StatusCode != http.StatusOK || json.Unmarshal(b, &ans) != nil || !ans.State.Record || ans.Deploy.Result != "ok" {
			t.Fatalf("pausing live reload: %d %s", r.StatusCode, b)
		}
		write(tile+"/index.html", "<!doctype html><html><head></head><body>v2</body></html>\n")
	})
	k := util.TileKey(tile)
	before := snapshot(t, ws)
	for _, rel := range []string{"data/deployments/" + k + ".json", "data/checkpoints/" + k + ".git/HEAD",
		"data/checkpoints/" + k + ".view.git/HEAD", tile + "/.git/HEAD", tile + "/.git/config"} {
		if _, ok := before[rel]; !ok {
			t.Fatalf("the fixture's snapshot has no %s", rel)
		}
	}
	prev := before
	for boot := 1; boot <= 2; boot++ {
		bootOnce(t, ws)
		now := snapshot(t, ws)
		for _, rel := range changed(prev, now) {
			if !strings.HasPrefix(rel, ".xbin/deploy/") {
				t.Errorf("boot %d changed %s, outside derived trees", boot, rel)
			}
		}
		prev = now
	}
}

// dsServe runs the daemon in process on ws, hands fn its URL, then stops it.
func dsServe(t *testing.T, ws string, fn func(url string)) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(ws, ln)
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the daemon did not stop")
		}
	}()
	select {
	case addr := <-ready:
		fn("http://" + addr)
	case err := <-done:
		t.Fatalf("boot ended before serving: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("boot never served")
	}
}
