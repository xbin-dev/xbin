//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers D119c D119f SC-ZERO Z1 PO-7 — a zero-state Go tile's whole life on the
// shared daemon (created by copying the counter example, saved, hot-swapped,
// crash-restarted) leaves no tile-deployment state behind: no
// data/deployments, data/checkpoints or .xbin/deploy, no dot-level namespace
// root, nothing per deployment. The root xbin.json and every data/*.json
// store stay byte-identical throughout.
func TestZeroStateLifecycleFiles(t *testing.T) {
	const tile = "apps/zs-counter"
	stores := func() map[string]string {
		out := map[string]string{}
		files, _ := filepath.Glob(filepath.Join(ws, "data", "*.json"))
		for _, p := range append(files, filepath.Join(ws, "xbin.json")) {
			if b, err := os.ReadFile(p); err == nil {
				rel, _ := filepath.Rel(ws, p)
				out[filepath.ToSlash(rel)] = string(b)
			}
		}
		return out
	}
	before := stores()
	names := make([]string, 0, len(before))
	for k := range before {
		names = append(names, k)
	}
	sort.Strings(names)
	t.Logf("stores held byte-identical: %s", strings.Join(names, " "))

	// create: a copy of the counter example (never edited in place), with a
	// module path of its own — the shared workspace's go.work already uses
	// one "module counter" (TestGoBackendLifecycle's copy), and a second
	// would fail every Go build there — moved into place whole
	stage := filepath.Join(t.TempDir(), "zs-counter")
	if out, err := exec.Command("cp", "-r", filepath.Join(repo, "examples", "counter-go"), stage).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	mod, err := os.ReadFile(filepath.Join(stage, "go.mod"))
	if err != nil || !bytes.Contains(mod, []byte("module counter\n")) {
		t.Fatalf("fixture: the counter example's go.mod changed: %v\n%s", err, mod)
	}
	if err := os.WriteFile(filepath.Join(stage, "go.mod"), bytes.Replace(mod, []byte("module counter\n"), []byte("module zscounter\n"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, filepath.Join(ws, filepath.FromSlash(tile))); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		c, b := get(t, "/api/"+tile+"/count")
		return c == 200 && strings.Contains(b, `"count":`)
	}, 120*time.Second) {
		t.Fatal("the copied counter never came up")
	}

	// save: the swap serves the new code
	src := filepath.Join(ws, filepath.FromSlash(tile), "backend", "main.go")
	code, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, bytes.Replace(code, []byte(`"count":%d`), []byte(`"count":%d,"zs":2`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		_, b := get(t, "/api/"+tile+"/count")
		return strings.Contains(b, `"zs":2`)
	}, 60*time.Second) {
		t.Fatal("the save was never swapped in")
	}

	// crash: kill the running generation; the next call restarts it
	pid := zeroStatePID(t, tile)
	if pid <= 0 {
		t.Fatal("the sandbox registry lists no pid for the tile")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		c, b := get(t, "/api/"+tile+"/count")
		return c == 200 && strings.Contains(b, `"zs":2`) && zeroStatePID(t, tile) != pid
	}, 60*time.Second) {
		t.Fatal("the crashed backend never came back")
	}

	// nothing of tile deployments exists
	for _, rel := range []string{"data/deployments", "data/checkpoints", ".xbin/deploy"} {
		if _, err := os.Lstat(filepath.Join(ws, rel)); err == nil {
			t.Errorf("%s exists after a zero-state tile's life", rel)
		}
	}
	_ = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == ".deployments" {
			rel, _ := filepath.Rel(ws, p)
			t.Errorf("a dot-level namespace root exists: %s", filepath.ToSlash(rel))
			return fs.SkipDir
		}
		return nil
	})
	after := stores()
	var diff []string
	for k, v := range before {
		if after[k] != v {
			diff = append(diff, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diff = append(diff, k+" (added)")
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Errorf("a zero-state tile's save, swap and restart changed stores:\n  %s", strings.Join(diff, "\n  "))
	}
}

// zeroStatePID is the pid of tile's live backend generation, from the
// sandbox registry (0 when none is listed).
func zeroStatePID(t *testing.T, tile string) int {
	t.Helper()
	_, body := get(t, "/api/xbin/sandboxes?tile="+tile)
	var out struct {
		Sandboxes []struct {
			Kind string
			PID  int
			Gen  int
		}
	}
	if json.Unmarshal([]byte(body), &out) != nil {
		return 0
	}
	// the newest generation: right after a swap the registry still lists
	// the one it replaced, draining or already gone (killing that one was
	// "no such process" on a loaded host, or no crash of the serving one)
	pid, gen := 0, -1
	for _, s := range out.Sandboxes {
		if s.Kind == "backend" && s.Gen > gen {
			pid, gen = s.PID, s.Gen
		}
	}
	return pid
}

// ---- the dev lifecycle's M1 tests (WP-28): shared helpers ----
//
// The tests of pausing live reload, pinning and rolling back talk to an xbind
// through dlAPI (the /deployments family, 11-contract §1), watch its event
// stream through a dlTape, and run the smallest backend of each runtime
// (writeRT) or the probe (probe_test.go). pinned_test.go, deployments_test.go,
// flowc_test.go, deploystate_boot_test.go and latency_test.go use them too.

// dlAPI is one xbind's API: the suite's shared daemon, a plain one, or an
// isolated one (with its root token when auth is on).
type dlAPI struct{ url, token string }

// sharedDL is the suite's shared daemon (--no-auth, not isolated). Only
// tests that create no deployment state use it: the zero-state tests
// (TestZeroStateLifecycleFiles) assert that its workspace never holds any.
func sharedDL() dlAPI { return dlAPI{url: baseURL} }

// startPlainDaemon starts an xbind of the shared daemon's flavour (--no-auth,
// not isolated) on a workspace of its own, for the tests that opt tiles in
// without isolation, and returns its API and workspace. It stops, and its
// workspace goes, when the test ends.
func startPlainDaemon(t *testing.T) (dlAPI, string) {
	t.Helper()
	pws := filepath.Join(t.TempDir(), "ws")
	addr := isoFreeAddr(t)
	cmd := exec.Command(xbindBin, "--workspace", pws, "--listen", addr, "--no-auth")
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	startDaemon(t, cmd, pws)
	a := dlAPI{url: "http://" + addr}
	if !waitFor(func() bool { c, _ := a.do("GET", "/healthz", ""); return c == 200 }, daemonGuard) {
		t.Fatalf("the plain xbind never became healthy:\n%s", out.String())
	}
	return a, pws
}

// dl is an isolated daemon's API.
func (d *isoDaemon) dl() dlAPI { return dlAPI{url: d.URL, token: d.Token} }

// do sends one request; a transport error is code 0 with the error as the
// body, so polls go on across a restart.
func (a dlAPI) do(method, path, body string) (int, string) {
	return isoHTTP(a.url, a.token, method, path, body)
}

// dlCan is one permission (11-contract §1.1 Can).
type dlCan struct {
	OK   bool   `json:"ok"`
	Why  string `json:"why"`
	Kind string `json:"kind"`
}

// dlCheckpoint is a Checkpoint: its short id and its full tree id.
type dlCheckpoint struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

// dlEntry is a DeployEntry: one deploy attempt.
type dlEntry struct {
	ID              int64  `json:"id"`
	Deployment      string `json:"deployment"`
	How             string `json:"how"`
	Checkpoint      string `json:"checkpoint"`
	Previous        string `json:"previous"`
	FollowsWorkTree bool   `json:"followsWorkTree"`
	By              string `json:"by"`
	Result          string `json:"result"`
	Phase           string `json:"phase"`
	Error           string `json:"error"`
}

// final: the attempt has its result (neither queued nor running).
func (e dlEntry) final() bool { return e.Result != "queued" && e.Result != "running" }

// dlDeployment is a Deployment of the state.
type dlDeployment struct {
	Name       string        `json:"name"`
	Primary    bool          `json:"primary"`
	LiveReload bool          `json:"liveReload"`
	Checkpoint *dlCheckpoint `json:"checkpoint"`
	Status     struct {
		State   string `json:"state"`
		Gen     int    `json:"gen"`
		Serving string `json:"serving"`
		Error   string `json:"error"`
	} `json:"status"`
	LastDeploy *dlEntry `json:"lastDeploy"`
}

// dlState is the answer of GET /deployments (the full view).
type dlState struct {
	Tile           string `json:"tile"`
	Record         bool   `json:"record"`
	Seq            int64  `json:"seq"`
	Primary        string `json:"primary"`
	LiveReload     string `json:"liveReload"`
	LastLiveReload string `json:"lastLiveReload"`
	WorkTree       *struct {
		Changed int    `json:"changed"`
		Since   string `json:"since"`
	} `json:"workTree"`
	Allowed     map[string]dlCan `json:"allowed"`
	Deployments []dlDeployment   `json:"deployments"`
	Caller      struct {
		Can map[string]dlCan `json:"can"`
	} `json:"caller"`
}

// dep is the named deployment, nil when the state lists none.
func (s dlState) dep(name string) *dlDeployment {
	for i := range s.Deployments {
		if s.Deployments[i].Name == name {
			return &s.Deployments[i]
		}
	}
	return nil
}

// pinned is the checkpoint id the named deployment runs, "" while it
// follows the work tree (or isn't listed).
func (s dlState) pinned(name string) string {
	if d := s.dep(name); d != nil && d.Checkpoint != nil {
		return d.Checkpoint.ID
	}
	return ""
}

// dlAnswer is what a POST of the family answers.
type dlAnswer struct {
	State     dlState  `json:"state"`
	Deploy    *dlEntry `json:"deploy"`
	Unchanged bool     `json:"unchanged"`
	Error     string   `json:"error"`
}

// dlBody is a request body: the tile ref, then field/value pairs.
func dlBody(tile string, kv ...any) string {
	m := map[string]any{"tile": tile}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// state reads GET /deployments?tile=, which must answer 200.
func (a dlAPI) state(t *testing.T, tile string) dlState {
	t.Helper()
	code, body := a.do("GET", "/api/xbin/deployments?tile="+tile, "")
	var st dlState
	if code != 200 || json.Unmarshal([]byte(body), &st) != nil {
		t.Fatalf("GET /deployments?tile=%s: %d %s", tile, code, body)
	}
	return st
}

// post sends POST /deployments/<route>; the answer is decoded as far as it
// goes (an error answer fills only Error).
func (a dlAPI) post(t *testing.T, route, body string) (int, dlAnswer, string) {
	t.Helper()
	code, raw := a.do("POST", "/api/xbin/deployments/"+route, body)
	var ans dlAnswer
	_ = json.Unmarshal([]byte(raw), &ans)
	return code, ans, raw
}

// mustPost is post for a request that must be accepted, waiting out the
// capture rate (postPaced).
func (a dlAPI) mustPost(t *testing.T, route, body string) dlAnswer {
	t.Helper()
	code, ans, raw, _ := a.postPaced(t, route, body)
	if code != 200 {
		t.Fatalf("POST /deployments/%s %s: %d %s", route, body, code, raw)
	}
	return ans
}

// postPaced is post, waiting out the capture rate: a tile checkpoints a
// burst of 10, then one per 3 s (T10), and a request over it answers 429
// with how long to wait, having changed nothing. It returns when the last
// attempt was sent.
func (a dlAPI) postPaced(t *testing.T, route, body string) (int, dlAnswer, string, time.Time) {
	t.Helper()
	for i := 0; ; i++ {
		sent := time.Now()
		code, ans, raw := a.post(t, route, body)
		wait, limited := rateLimited(code, raw)
		if !limited || i == 20 {
			return code, ans, raw, sent
		}
		time.Sleep(wait)
	}
}

// rateLimited reads a 429 of the capture rate: how long to wait.
func rateLimited(code int, raw string) (time.Duration, bool) {
	m := retryIn.FindStringSubmatch(raw)
	if code != 429 || m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return time.Duration(n)*time.Second + 100*time.Millisecond, true
}

var retryIn = regexp.MustCompile(`checkpointed too often; retry in (\d+)s`)

// op runs an accepted operation on tile and waits for the deploy it
// started, returning the answer and the finished attempt (the zero entry
// when the answer carries no deploy).
func (a dlAPI) op(t *testing.T, route, tile string, kv ...any) (dlAnswer, dlEntry) {
	t.Helper()
	ans := a.mustPost(t, route, dlBody(tile, kv...))
	return ans, a.settle(t, tile, ans)
}

// settle waits for the deploy an answer carries to finish, and returns it.
func (a dlAPI) settle(t *testing.T, tile string, ans dlAnswer) dlEntry {
	t.Helper()
	if ans.Deploy == nil {
		return dlEntry{}
	}
	if ans.Deploy.final() {
		return *ans.Deploy
	}
	return a.entry(t, tile, ans.Deploy.ID, 5*time.Minute)
}

// entry holds GET /deployments/log?id=&wait= until attempt id of tile has
// its result (bounded by max), and returns it.
func (a dlAPI) entry(t *testing.T, tile string, id int64, max time.Duration) dlEntry {
	t.Helper()
	var e dlEntry
	for deadline := time.Now().Add(max); ; {
		code, body := a.do("GET", fmt.Sprintf("/api/xbin/deployments/log?tile=%s&id=%d&wait=25", tile, id), "")
		switch {
		case code == 200:
			var ans struct {
				Entry dlEntry `json:"entry"`
			}
			if json.Unmarshal([]byte(body), &ans) == nil {
				if e = ans.Entry; e.final() {
					return e
				}
			}
		case code != 0:
			t.Fatalf("deploy %d of %s: %d %s", id, tile, code, body)
		default:
			time.Sleep(200 * time.Millisecond) // xbind is restarting
		}
		if time.Now().After(deadline) {
			t.Fatalf("deploy %d of %s is still %q after %v", id, tile, e.Result, max)
		}
	}
}

// log is tile's deploy log, newest first (every deployment).
func (a dlAPI) log(t *testing.T, tile string) []dlEntry {
	t.Helper()
	code, body := a.do("GET", "/api/xbin/deployments/log?tile="+tile+"&limit=200", "")
	var ans struct {
		Entries []dlEntry `json:"entries"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &ans) != nil {
		t.Fatalf("the deploy log of %s: %d %s", tile, code, body)
	}
	return ans.Entries
}

// dlEvent is one /ws/events frame, kept verbatim, and when it arrived.
type dlEvent struct {
	Type      string          `json:"type"`
	Component string          `json:"component"`
	Text      string          `json:"text"`
	Data      json.RawMessage `json:"data"`
	raw       string
	at        time.Time
}

// op is a deployments event's data.op.
func (e dlEvent) op() string {
	var d struct {
		Op string `json:"op"`
	}
	_ = json.Unmarshal(e.Data, &d)
	return d.Op
}

// dlOldTypes are today's event types a frame, the old shell and the shipped
// app act on: none may speak of a deploy (11-contract §3.1, SC-SAFE-DEPLOY).
var dlOldTypes = map[string]bool{"reload": true, "build-start": true, "build-ok": true, "build-error": true, "status": true}

// dlTape records an xbind's whole event stream from the moment it is made.
type dlTape struct {
	mu     sync.Mutex
	evs    []dlEvent
	notify chan struct{}
}

// tape subscribes to a's /ws/events for the rest of the test.
func (a dlAPI) tape(t *testing.T) *dlTape {
	t.Helper()
	hdr := http.Header{}
	if a.token != "" {
		hdr.Set("Authorization", "Bearer "+a.token)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(a.url, "http")+"/ws/events", hdr)
	if err != nil {
		t.Fatalf("/ws/events: %v", err)
	}
	e := &dlTape{notify: make(chan struct{}, 1)}
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			at := time.Now()
			if err != nil {
				return
			}
			var ev dlEvent
			if json.Unmarshal(msg, &ev) != nil {
				continue
			}
			ev.raw, ev.at = string(msg), at
			e.mu.Lock()
			e.evs = append(e.evs, ev)
			e.mu.Unlock()
			select {
			case e.notify <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return e
}

// mark is a cursor: the events after it arrive from now on.
func (e *dlTape) mark() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.evs)
}

// since is every event after the cursor.
func (e *dlTape) since(m int) []dlEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]dlEvent(nil), e.evs[m:]...)
}

// count is how many events after the cursor match.
func (e *dlTape) count(m int, match func(dlEvent) bool) int {
	n := 0
	for _, ev := range e.since(m) {
		if match(ev) {
			n++
		}
	}
	return n
}

// wait waits (bounded) for an event after the cursor that match accepts.
func (e *dlTape) wait(m int, match func(dlEvent) bool, timeout time.Duration) (dlEvent, bool) {
	deadline := time.After(timeout)
	for {
		for _, ev := range e.since(m) {
			if match(ev) {
				return ev, true
			}
		}
		select {
		case <-e.notify:
		case <-deadline:
			return dlEvent{}, false
		}
	}
}

// describe lists the events after the cursor that name tile, for a failure.
func (e *dlTape) describe(m int, tile string) string {
	var parts []string
	for _, ev := range e.since(m) {
		if ev.Component == tile {
			parts = append(parts, ev.raw)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "\n  ")
}

// isEvent matches an event of type typ naming tile.
func isEvent(typ, tile string) func(dlEvent) bool {
	return func(ev dlEvent) bool { return ev.Type == typ && ev.Component == tile }
}

// saveIfChanged writes content to p the way an editor saves (a temporary
// file beside it, renamed over it), unless p holds exactly that already. A
// capture sees the old file or the new one, never half of it.
func saveIfChanged(p, content string) error {
	if b, err := os.ReadFile(p); err == nil && string(b) == content {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".tmp-"+strconv.Itoa(rand.Int()))
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// writeRT writes (or turns into marker) the smallest tile of runtime rt at
// ws/tile that says which code runs and which tree is bound:
//   - "static": index.html and probeFile, both holding marker; served
//     by the static plane;
//   - "go", "node", "python": a backend answering GET /v with the marker
//     compiled into its source, and GET /file with probeFile read at the
//     canonical path (its working directory).
//
// fault makes the backend fail: "build" a source that doesn't compile (for
// node and python, a syntax error: they fail at start), "crash" an exit at
// start, "hang" a process that never listens (a health timeout). Only
// changed files are written, each saved as an editor saves, the manifest
// last. It returns an error rather than failing the test, so a goroutine
// can save.
func writeRT(ws, tile, rt, marker, fault string, alwaysOn bool) error {
	dir := filepath.Join(ws, filepath.FromSlash(tile))
	manifest := `{"runtime":"` + rt + `"`
	if alwaysOn {
		manifest += `,"alwaysOn":true`
	}
	manifest += "}\n"
	src := strings.NewReplacer("__MARKER__", strconv.Quote(marker), "__FAULT__", strconv.Quote(fault),
		"__FILE__", strconv.Quote(probeFile))
	var files [][2]string
	switch rt {
	case "static":
		files = [][2]string{
			{"index.html", "<!doctype html><html><head></head><body>" + marker + "</body></html>\n"},
			{probeFile, marker},
			{"xbin.json", `{"title":"` + tile + `"}` + "\n"},
		}
	case "go":
		stmt := map[string]string{"": "", "build": "this is not Go", "crash": "os.Exit(3)", "hang": "time.Sleep(1 << 62)"}[fault]
		files = [][2]string{
			{"go.mod", "module rt/" + strings.ReplaceAll(tile, "+", "_") + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n"},
			{"backend/main.go", strings.Replace(src.Replace(rtGoSource), "\t__STMT__\n", "\t"+stmt+"\n", 1)},
			{probeFile, marker},
			{"xbin.json", manifest},
		}
	case "node":
		code := src.Replace(rtNodeSource)
		if fault == "build" {
			code = "this is not JavaScript\n" + code
		}
		files = [][2]string{{"backend/server.js", code}, {probeFile, marker}, {"xbin.json", manifest}}
	case "python":
		code := src.Replace(rtPythonSource)
		if fault == "build" {
			code = "this is not Python\n" + code
		}
		files = [][2]string{{"backend/server.py", code}, {probeFile, marker}, {"xbin.json", manifest}}
	default:
		return fmt.Errorf("writeRT: no runtime %q", rt)
	}
	for _, f := range files {
		if err := saveIfChanged(filepath.Join(dir, filepath.FromSlash(f[0])), f[1]); err != nil {
			return err
		}
	}
	return nil
}

// mustRT is writeRT for the test's own goroutine.
func mustRT(t *testing.T, ws, tile, rt, marker, fault string) {
	t.Helper()
	if err := writeRT(ws, tile, rt, marker, fault, false); err != nil {
		t.Fatal(err)
	}
}

const rtGoSource = `package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const marker, fault = __MARKER__, __FAULT__

var _ = time.Second // the hang fault sleeps

func main() {
	__STMT__
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, marker) })
	mux.HandleFunc("GET /file", func(w http.ResponseWriter, r *http.Request) {
		wd, _ := os.Getwd()
		b, err := os.ReadFile(filepath.Join(wd, __FILE__))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Write(b)
	})
	xbin.Serve(mux)
}
`

const rtNodeSource = `const http = require('http'), fs = require('fs'), path = require('path');
const marker = __MARKER__, fault = __FAULT__;
if (fault === 'crash') process.exit(3);
let srv = null;
if (fault === 'hang') setInterval(() => {}, 1 << 30);
else srv = http.createServer((req, res) => {
  if (req.url === '/v') return res.end(marker);
  if (req.url === '/file') {
    try { return res.end(fs.readFileSync(path.join(process.cwd(), __FILE__))); }
    catch (e) { res.statusCode = 404; return res.end(String(e)); }
  }
  res.statusCode = 404;
  res.end('no such path');
}).listen(process.env.XBIN_SOCKET);
// SIGTERM drains, as docs/sdk.md asks: no new connections, and the exit once
// the requests it holds are answered.
process.on('SIGTERM', () => srv ? srv.close(() => process.exit(0)) : process.exit(0));
`

const rtPythonSource = `import os, signal, sys, threading, time
from http.server import BaseHTTPRequestHandler
from socketserver import UnixStreamServer

MARKER, FAULT = __MARKER__, __FAULT__
if FAULT == "crash":
    sys.exit(3)
while FAULT == "hang":
    time.sleep(3600)

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        code, body = 200, MARKER.encode()
        if self.path == "/file":
            try:
                with open(os.path.join(os.getcwd(), __FILE__), "rb") as f:
                    body = f.read()
            except OSError as e:
                code, body = 404, str(e).encode()
        elif self.path != "/v":
            code, body = 404, b"no such path"
        self.send_response(code)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass

class Server(UnixStreamServer):
    def get_request(self):
        req, _ = self.socket.accept()
        return req, ("xbin", 0)

srv = Server(os.environ["XBIN_SOCKET"], Handler)
# SIGTERM drains, as docs/elements.md asks: the request in hand is answered,
# then serve_forever returns and the process ends. shutdown waits for
# serve_forever, which runs here, so it is asked from a thread of its own.
signal.signal(signal.SIGTERM, lambda *_: threading.Thread(target=srv.shutdown).start())
srv.serve_forever()
`

// served is what tile serves now: its backend's GET /v and GET /file, or a
// static tile's probeFile from the static plane (as both). ok: both 200.
func (a dlAPI) served(tile, rt string) (v, file string, ok bool) {
	if rt == "static" {
		code, body := a.do("GET", "/c/"+tile+"/"+probeFile, "")
		return body, body, code == 200
	}
	c1, v := a.do("GET", "/api/"+tile+"/v", "")
	c2, f := a.do("GET", "/api/"+tile+"/file", "")
	return v, f, c1 == 200 && c2 == 200
}

// waitServed waits (bounded by max) until tile serves marker from both its
// code and its tree, and fails with the last answers otherwise.
func (a dlAPI) waitServed(t *testing.T, tile, rt, marker string, max time.Duration) {
	t.Helper()
	var v, f string
	var ok bool
	if !waitFor(func() bool { v, f, ok = a.served(tile, rt); return ok && v == marker && f == marker }, max) {
		t.Fatalf("%s never served %q: /v %q, /file %q", tile, marker, v, f)
	}
	a.waitTile(t, tile)
}

// waitTile waits (bounded) until the registry lists tile: the static plane
// serves a new directory's files before a rescan makes it a tile.
func (a dlAPI) waitTile(t *testing.T, tile string) {
	t.Helper()
	if !waitFor(func() bool { c, _ := a.do("GET", "/api/xbin/deployments?tile="+tile, ""); return c == 200 }, 30*time.Second) {
		t.Fatalf("%s never became a tile", tile)
	}
}

// waitCode waits (bounded by max) until tile's code answers marker on
// GET /v, whatever tree it reads.
func (a dlAPI) waitCode(t *testing.T, tile, rt, marker string, max time.Duration) {
	t.Helper()
	var v string
	var ok bool
	if !waitFor(func() bool { v, _, ok = a.served(tile, rt); return v == marker }, max) {
		t.Fatalf("%s's code never answered %q: %q (ok %v)", tile, marker, v, ok)
	}
}

// hasRecord says whether tile has a deployment record; false while the
// tile isn't registered yet.
func (a dlAPI) hasRecord(tile string) bool {
	code, body := a.do("GET", "/api/xbin/deployments?tile="+tile, "")
	var st dlState
	return code == 200 && json.Unmarshal([]byte(body), &st) == nil && st.Record
}

// dlPaths are the stores a tile's deployment state lives in (11-contract
// §10): the record, its per-tile directory (the deploy journal), the
// checkpoint store, the view repository and the materialized checkpoints.
func dlPaths(ws, tile string) (record, dir, store, view, deploy string) {
	k := util.TileKey(tile)
	return filepath.Join(ws, "data", "deployments", k+".json"), filepath.Join(ws, "data", "deployments", k),
		filepath.Join(ws, "data", "checkpoints", k+".git"), filepath.Join(ws, "data", "checkpoints", k+".view.git"),
		filepath.Join(ws, ".xbin", "deploy", k)
}

// ---- the M1 tests without isolation ----

// covers D119c D119d D119e D119h SC-FAIL-CLOSED SC-LIVE-RELOAD-PAUSE (flow A) — the
// static half without isolation (a daemon of the shared daemon's flavour,
// on a workspace of its own): pausing live reload on a static tile works
// (TestStaticTilePauseLiveReloadWithoutIsolation).
// Paused, an edit changes neither what /c/<tile>/ serves nor the event
// stream (only the work-tree count moves); reload now ships it with exactly
// one reload and live reload stays paused; resume removes the record, and
// saves reload again.
func TestLiveReloadPauseStatic(t *testing.T) {
	const tile = "apps/lr-static"
	a, ws := startPlainDaemon(t)
	mustRT(t, ws, tile, "static", "v1", "")
	must(t, saveIfChanged(filepath.Join(ws, tile, "style.css"), "/* v1 */\n"))
	a.waitServed(t, tile, "static", "v1", 30*time.Second)
	tape := a.tape(t)
	record, _, store, view, _ := dlPaths(ws, tile)

	m := tape.mark()
	ans, e := a.op(t, "live-reload/pause", tile)
	if e.How != "pause" || e.Result != "ok" || !strings.HasPrefix(e.Checkpoint, "c:") {
		t.Fatalf("the pause's deploy: %+v", e)
	}
	st := a.state(t, tile)
	if !st.Record || st.LiveReload != "" || st.LastLiveReload != "main" || st.pinned("main") != e.Checkpoint {
		t.Fatalf("the paused state: %+v", ans.State)
	}
	for _, p := range []string{record, store, view} {
		if !isoExists(p) {
			t.Errorf("pausing live reload made no %s", p)
		}
	}
	paused := e.Checkpoint

	// Paused: a save reaches nothing. The work-tree count is the positive
	// condition to wait for; the rest is a bounded negative wait for the
	// watcher's batch.
	mustRT(t, ws, tile, "static", "v2", "")
	must(t, saveIfChanged(filepath.Join(ws, tile, "style.css"), "/* v2 */\n"))
	if !waitFor(func() bool { s := a.state(t, tile); return s.WorkTree != nil && s.WorkTree.Changed >= 2 }, 15*time.Second) {
		t.Errorf("the work-tree count never saw the two saved files: %+v", a.state(t, tile).WorkTree)
	}
	time.Sleep(3 * latencyDebounce)
	if v, _, _ := a.served(tile, "static"); v != "v1" {
		t.Errorf("paused, a save reached the served tree: %q", v)
	}
	if _, doc := a.do("GET", "/c/"+tile+"/", ""); !strings.Contains(doc, ">v1<") {
		t.Errorf("paused, a save reached the document: %s", doc)
	}
	if _, css := a.do("GET", "/c/"+tile+"/style.css", ""); css != "/* v1 */\n" {
		t.Errorf("paused, a save reached style.css: %q", css)
	}
	for _, ev := range tape.since(m) {
		if ev.Component == tile && (dlOldTypes[ev.Type] || ev.Type == "deployments" && ev.op() != "record" && ev.op() != "deploy" && ev.op() != "work-tree") {
			t.Errorf("pausing and saving published %s", ev.raw)
		}
	}

	// Reload now: exactly one reload, the new files, still paused.
	m = tape.mark()
	_, e = a.op(t, "live-reload/now", tile)
	if e.How != "reload-now" || e.Result != "ok" || e.Checkpoint == paused || e.Previous != paused {
		t.Fatalf("reload now's deploy: %+v (paused at %s)", e, paused)
	}
	if _, ok := tape.wait(m, isEvent("reload", tile), 10*time.Second); !ok {
		t.Errorf("reload now published no reload: %s", tape.describe(m, tile))
	}
	time.Sleep(3 * latencyDebounce) // negative: a second reload may not follow
	if n := tape.count(m, isEvent("reload", tile)); n != 1 {
		t.Errorf("reload now published %d reloads, want 1: %s", n, tape.describe(m, tile))
	}
	if v, _, _ := a.served(tile, "static"); v != "v2" {
		t.Errorf("after reload now: %q, want v2", v)
	}
	if _, css := a.do("GET", "/c/"+tile+"/style.css", ""); css != "/* v2 */\n" {
		t.Errorf("after reload now style.css: %q", css)
	}
	if st := a.state(t, tile); st.LiveReload != "" || st.pinned("main") != e.Checkpoint {
		t.Errorf("reload now resumed live reload or pinned elsewhere: liveReload %q, main %q", st.LiveReload, st.pinned("main"))
	}

	// Resume: the record goes (the zero state), and saves reload again.
	ans = a.mustPost(t, "live-reload/resume", dlBody(tile))
	if ans.State.Record || isoExists(record) || isoExists(view) {
		t.Errorf("resuming onto main left a record: %+v, record file %v, view %v", ans.State, isoExists(record), isoExists(view))
	}
	m = tape.mark()
	mustRT(t, ws, tile, "static", "v3", "")
	if _, ok := tape.wait(m, isEvent("reload", tile), 10*time.Second); !ok {
		t.Errorf("after resuming, a save published no reload: %s", tape.describe(m, tile))
	}
	a.waitServed(t, tile, "static", "v3", 10*time.Second)
}

// covers D119h T12 SC-FAIL-CLOSED — on the shared daemon (no isolation) a Go
// backend can't be pinned: pausing live reload on a copy of the counter
// example is refused with the reason naming --isolate, as the state's Can
// says beforehand, dry run or not, before any state exists (no record, no
// store). Nothing else opts it in either, and a later save still swaps.
func TestLiveReloadPauseRefusedWithoutIsolation(t *testing.T) {
	const tile = "apps/lr-counter"
	a := sharedDL()
	stage := filepath.Join(t.TempDir(), "lr-counter")
	if out, err := exec.Command("cp", "-r", filepath.Join(repo, "examples", "counter-go"), stage).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	gomod := filepath.Join(stage, "go.mod")
	mod, err := os.ReadFile(gomod)
	if err != nil || !bytes.Contains(mod, []byte("module counter\n")) {
		t.Fatalf("fixture: the counter example's go.mod changed: %v\n%s", err, mod)
	}
	must(t, os.WriteFile(gomod, bytes.Replace(mod, []byte("module counter\n"), []byte("module lrcounter\n"), 1), 0o644))
	must(t, os.Rename(stage, filepath.Join(ws, tile)))
	if !waitFor(func() bool {
		c, b := a.do("GET", "/api/"+tile+"/count", "")
		return c == 200 && strings.Contains(b, `"count":`)
	}, 3*time.Minute) {
		t.Fatal("the copied counter never came up")
	}
	record, dir, store, view, deploy := dlPaths(ws, tile)
	nothing := func(when string) {
		t.Helper()
		for _, p := range []string{record, dir, store, view, deploy} {
			if isoExists(p) {
				t.Errorf("%s: %s exists", when, p)
			}
		}
		if st := a.state(t, tile); st.Record {
			t.Errorf("%s: the tile has a record: %+v", when, st)
		}
	}

	const why = "pinning a backend to a checkpoint needs isolation (--isolate)"
	st := a.state(t, tile)
	for what, c := range map[string]dlCan{"caller.can.pause": st.Caller.Can["pause"], "allowed.pause": st.Allowed["pause"]} {
		if c.OK || c.Kind != "policy" || !strings.Contains(c.Why, "--isolate") {
			t.Errorf("%s = %+v, want the isolation refusal", what, c)
		}
	}
	for _, dry := range []bool{true, false} {
		code, ans, body := a.post(t, "live-reload/pause", dlBody(tile, "dryRun", dry))
		if code != 409 || !strings.Contains(ans.Error, why) {
			t.Errorf("pause (dryRun %v): %d %s, want 409 %q", dry, code, body, why)
		}
	}
	nothing("after the refused pause")
	// Without a record nothing else moves code either.
	for _, route := range []string{"live-reload/now", "live-reload/resume", "deploy", "rollback"} {
		if code, _, body := a.post(t, route, dlBody(tile)); code != 409 {
			t.Errorf("%s on a tile without a record: %d %s, want 409", route, code, body)
		}
	}
	nothing("after the other refusals")

	src := filepath.Join(ws, tile, "backend", "main.go")
	code, err := os.ReadFile(src)
	must(t, err)
	must(t, os.WriteFile(src, bytes.Replace(code, []byte(`"count":%d`), []byte(`"count":%d,"lr":1`), 1), 0o644))
	if !waitFor(func() bool { _, b := a.do("GET", "/api/"+tile+"/count", ""); return strings.Contains(b, `"lr":1`) }, time.Minute) {
		t.Fatal("after the refusals a save was never swapped in")
	}
	nothing("after the save")
}

// raceGuard bounds every wait of TestLiveReloadPauseRace that a condition
// ends: a hang guard, far past any answer on a loaded machine, and never
// what a check depends on.
const raceGuard = 3 * time.Minute

// raceRuns is how many runs TestLiveReloadPauseRace makes per runtime: 10,
// or 100 at a milestone exit (XBIN_TEST_FULL=1, SC-LIVE-RELOAD-PAUSE).
func raceRuns() int {
	if os.Getenv("XBIN_TEST_FULL") == "1" {
		return 100
	}
	return 10
}

// covers SC-LIVE-RELOAD-PAUSE D119e — saves run continuously across the
// request that pauses live reload. The checkpoint holds every save that
// completed before the request; from the response on, no save changes what
// is served: a static tile's bytes (without isolation), and a go, node and
// python backend's answers (an isolated daemon). Afterwards the pinned code
// holds through a crash, a grant change and an xbind restart (an idle reap
// is a seam row: its 30-minute constant has no test knob). When the
// pause's own deploy fails, the deployment stays pinned to the attempted
// checkpoint: the old generation serves, and a restart runs the attempted
// checkpoint, never the fixed work tree (05-model §5).
func TestLiveReloadPauseRace(t *testing.T) {
	t.Parallel()
	runs := raceRuns()
	t.Run("static", func(t *testing.T) {
		a, ws := startPlainDaemon(t)
		for run := 1; run <= runs; run++ {
			raceRun(t, a, ws, "apps/lr-race-static", "static", run)
		}
	})
	t.Run("backends", func(t *testing.T) {
		d := startIsolatedDaemon(t, isoOpts{})
		a := d.dl()
		rts := []string{"go", "node", "python"}
		pinned := map[string]string{} // tile → the marker its pinned code answers
		var mu sync.Mutex
		t.Run("runs", func(t *testing.T) {
			for _, rt := range rts {
				t.Run(rt, func(t *testing.T) {
					t.Parallel()
					tile := "apps/lr-race-" + rt
					var cpV, cpF string
					for run := 1; run <= runs; run++ {
						cpV, cpF = raceRun(t, a, d.WS, tile, rt, run)
					}
					racePinnedHolds(t, a, d.WS, tile, rt, cpV, cpF, runs+1)
					fixed := raceFailedPause(t, a, d.WS, tile, rt)
					mu.Lock()
					pinned[tile] = fixed
					mu.Unlock()
				})
			}
		})
		if t.Failed() {
			return
		}
		// An xbind restart: a static tile pinned beside them, and every work
		// tree moved on past its pinned code.
		const static = "apps/lr-race-static-iso"
		mustRT(t, d.WS, static, "static", "st-pinned", "")
		a.waitServed(t, static, "static", "st-pinned", raceGuard)
		if _, e := a.op(t, "live-reload/pause", static); e.Result != "ok" {
			t.Fatalf("pausing %s: %+v", static, e)
		}
		pinned[static] = "st-pinned"
		for tile, marker := range pinned {
			rt := strings.TrimPrefix(tile, "apps/lr-race-")
			if tile == static {
				rt = "static"
			}
			mustRT(t, d.WS, tile, rt, marker+"-then-saved", "")
		}
		d.restart(t)
		for tile, marker := range pinned {
			rt := strings.TrimPrefix(tile, "apps/lr-race-")
			if tile == static {
				rt = "static"
			}
			a.waitServed(t, tile, rt, marker, raceGuard)
		}
	})
}

// raceMarker is save n of a run: markers of one run sort in save order.
func raceMarker(run, n int) string { return fmt.Sprintf("r%03d-%06d", run, n) }

// raceSeq is the save number of marker in run: -1 for an earlier run's
// (older code), and a number past every save for anything that isn't a
// marker at all, so an unexpected answer counts as a leak.
func raceSeq(marker string, run int) int {
	var r, n int
	if _, err := fmt.Sscanf(marker, "r%03d-%06d", &r, &n); err != nil || len(marker) != len("r000-000000") {
		return 1 << 30
	}
	if r < run {
		return -1
	}
	if r > run {
		return 1 << 30
	}
	return n
}

// raceObs is one answer seen while a run's checks go on, and when it was
// asked for.
type raceObs struct {
	at      time.Time
	v, file string
	ok      bool
}

// observe polls what tile serves every 20 ms until the returned stop is
// called, which returns every answer seen.
func observe(a dlAPI, tile, rt string) (stop func() []raceObs) {
	var mu sync.Mutex
	var seen []raceObs
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			asked := time.Now() // an answer counts from when it was asked for
			v, f, ok := a.served(tile, rt)
			mu.Lock()
			seen = append(seen, raceObs{asked, v, f, ok})
			mu.Unlock()
			select {
			case <-quit:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	return func() []raceObs {
		close(quit)
		<-done
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// raceRun is one run of TestLiveReloadPauseRace on tile, which follows its
// work tree when the run starts (a resume opts it out first). It returns the
// markers the checkpoint's code and tree answer: a backend save writes its
// code and its tree file one after the other, so a capture between the two
// holds a save's code beside the next one's file (both after the request's
// cutoff), and the two differ.
func raceRun(t *testing.T, a dlAPI, ws, tile, rt string, run int) (cpV, cpF string) {
	t.Helper()
	first := raceMarker(run, 0)
	mustRT(t, ws, tile, rt, first, "")
	if a.hasRecord(tile) {
		a.op(t, "live-reload/resume", tile)
	}
	a.waitServed(t, tile, rt, first, raceGuard)

	var done atomic.Int64 // the last save that completed
	stop, werr := make(chan struct{}), make(chan error, 1)
	go func() {
		for n := 1; ; n++ {
			select {
			case <-stop:
				werr <- nil
				return
			default:
			}
			if err := writeRT(ws, tile, rt, raceMarker(run, n), "", false); err != nil {
				werr <- err
				return
			}
			done.Store(int64(n))
			time.Sleep(time.Duration(5+rand.Intn(20)) * time.Millisecond)
		}
	}()
	// Where the request lands in the save stream is what varies from run to
	// run: a jittered wait lets saves, and a backend's work-tree builds, pile up.
	time.Sleep(time.Duration(100+rand.Intn(400)) * time.Millisecond)
	before := int(done.Load())
	code, ans, body := a.post(t, "live-reload/pause", dlBody(tile))
	answered := time.Now()
	if wait, limited := rateLimited(code, body); limited {
		// Over the capture rate: refused before anything changed. The run
		// starts over once the rate allows a capture.
		close(stop)
		<-werr
		if st := a.state(t, tile); st.LiveReload != "main" {
			t.Fatalf("run %d: a refused pause moved live reload to %q", run, st.LiveReload)
		}
		time.Sleep(wait)
		return raceRun(t, a, ws, tile, rt, run)
	}
	stopObs := observe(a, tile, rt)
	time.Sleep(3 * latencyDebounce) // negative: saves go on past the watcher's debounce, and none may land
	close(stop)
	if err := <-werr; err != nil || code != 200 {
		stopObs()
		t.Fatalf("run %d: pause %d %s (a save: %v)", run, code, body, err)
	}
	e := a.settle(t, tile, ans)
	swapped := time.Now()
	if e.How != "pause" || e.Result != "ok" {
		stopObs()
		t.Fatalf("run %d: the pause's deploy: %+v", run, e)
	}
	cpV, cpF, ok := a.served(tile, rt)
	nV, nF := raceSeq(cpV, run), raceSeq(cpF, run)
	if !ok || nV < before || nF < before || nV > 1<<29 || nF > 1<<29 {
		t.Errorf("run %d: the checkpoint answers %q (code) and %q (tree), but save %d completed before the request (ok %v)",
			run, cpV, cpF, before, ok)
	}
	time.Sleep(3 * latencyDebounce) // negative: nothing moves any more
	seen := stopObs()
	if v, f, _ := a.served(tile, rt); v != cpV || f != cpF {
		t.Errorf("run %d: the served code moved after the pause: %q/%q, pinned %q/%q", run, v, f, cpV, cpF)
	}
	window := 0
	for _, o := range seen {
		switch {
		case !o.ok:
			t.Errorf("run %d: %v after the answer, a request failed: %q %q", run, o.at.Sub(answered), o.v, o.file)
		case raceSeq(o.v, run) > nV:
			t.Errorf("run %d: %v after the answer the code answered %q, newer than the checkpoint's %q", run, o.at.Sub(answered), o.v, cpV)
		case rt == "static" && o.file != cpF, o.at.After(swapped) && (o.v != cpV || o.file != cpF):
			t.Errorf("run %d: %v after the answer %s served %q/%q, not the checkpoint's %q/%q", run, o.at.Sub(answered), tile, o.v, o.file, cpV, cpF)
		case raceSeq(o.file, run) > nF:
			window++ // the old generation reads the live work tree until the pinned one swaps in
		}
	}
	if st := a.state(t, tile); st.LiveReload != "" || st.pinned("main") != e.Checkpoint {
		t.Errorf("run %d: after the pause live reload %q, main pinned to %q (the pause shipped %s)", run, st.LiveReload, st.pinned("main"), e.Checkpoint)
	}
	t.Logf("run %d: save %d completed before the request; the checkpoint holds %s/%s; %d answers checked, %d reads of the work tree by the old generation before the swap",
		run, before, cpV, cpF, len(seen), window)
	return cpV, cpF
}

// racePinnedHolds checks that tile, pinned to a checkpoint whose code
// answers cpV and whose tree file holds cpF, keeps both through a crash
// restart and a grant change while its work tree moves on (a save of run
// next).
func racePinnedHolds(t *testing.T, a dlAPI, ws, tile, rt, cpV, cpF string, next int) {
	t.Helper()
	cp := cpV
	servesCheckpoint := func() {
		t.Helper()
		var v, f string
		var ok bool
		if !waitFor(func() bool { v, f, ok = a.served(tile, rt); return ok && v == cpV && f == cpF }, raceGuard) {
			t.Fatalf("%s never served its checkpoint %q/%q: /v %q, /file %q", tile, cpV, cpF, v, f)
		}
		a.waitTile(t, tile)
	}
	mustRT(t, ws, tile, rt, raceMarker(next, 1), "")
	pid := backendPID(t, a, tile)
	if pid <= 0 {
		t.Fatalf("%s: no backend pid listed", tile)
	}
	must(t, syscall.Kill(pid, syscall.SIGKILL))
	if !waitFor(func() bool { v, _, ok := a.served(tile, rt); return ok && v == cp && backendPID(t, a, tile) != pid }, raceGuard) {
		v, f, _ := a.served(tile, rt)
		t.Fatalf("%s: after a crash it serves %q/%q, pinned %q", tile, v, f, cp)
	}
	servesCheckpoint()
	gen := backendGen(t, a, tile)
	grantChange(t, a, tile)
	if !waitFor(func() bool { v, _, ok := a.served(tile, rt); return ok && v == cp && backendGen(t, a, tile) > gen }, raceGuard) {
		v, f, _ := a.served(tile, rt)
		t.Fatalf("%s: after a grant change (gen %d → %d) it serves %q/%q, pinned %q", tile, gen, backendGen(t, a, tile), v, f, cp)
	}
	servesCheckpoint()
	t.Logf("%s: idle reap not run end to end: a 30-minute constant with no test knob (seam row 18)", tile)
}

// raceFailedPause pauses live reload while the work tree is broken, so the
// pause's own deploy fails (05-model §5): live reload stays detached, main
// is pinned to the attempted checkpoint, the old generation keeps serving,
// a fixed work tree reaches nothing, and a crash restart runs the attempted
// checkpoint. Reload now then ships the fix. It returns the fix's marker,
// what main is pinned to at the end.
func raceFailedPause(t *testing.T, a dlAPI, ws, tile, rt string) string {
	t.Helper()
	mustRT(t, ws, tile, rt, "fp-good", "")
	if a.hasRecord(tile) {
		a.op(t, "live-reload/resume", tile)
	}
	a.waitServed(t, tile, rt, "fp-good", raceGuard)
	fault := "crash"
	if rt == "go" {
		fault = "build"
	}
	tp := a.tape(t)
	m := tp.mark()
	mustRT(t, ws, tile, rt, "fp-broken", fault)
	// The save's build or start fails, and the old generation keeps serving
	// (its /file reads the live work tree).
	if _, ok := tp.wait(m, isEvent("build-error", tile), raceGuard); !ok {
		t.Fatalf("%s: the broken save's build never failed: %s", tile, tp.describe(m, tile))
	}
	a.waitCode(t, tile, rt, "fp-good", raceGuard)

	_, e := a.op(t, "live-reload/pause", tile)
	if e.How != "pause" || e.Result != "failed" || e.Error == "" {
		t.Fatalf("%s: the pause of a broken work tree: %+v, want a failed deploy with its error", tile, e)
	}
	st := a.state(t, tile)
	if st.LiveReload != "" || st.pinned("main") != e.Checkpoint {
		t.Errorf("%s: after the failed pause live reload %q, main pinned to %q (attempted %s)", tile, st.LiveReload, st.pinned("main"), e.Checkpoint)
	}
	a.waitCode(t, tile, rt, "fp-good", raceGuard)

	mustRT(t, ws, tile, rt, "fp-fixed", "")
	time.Sleep(3 * latencyDebounce) // negative: the fixed work tree reaches nothing
	if v, _, _ := a.served(tile, rt); v != "fp-good" {
		t.Errorf("%s: after the failed pause a save reached the code: %q", tile, v)
	}
	if pid := backendPID(t, a, tile); pid > 0 {
		must(t, syscall.Kill(pid, syscall.SIGKILL))
	}
	var v string
	var ok bool
	// A restart runs the attempted checkpoint, which can't start: the tile
	// answers errors until it is deployed again, and never the work tree.
	if !waitFor(func() bool { v, _, ok = a.served(tile, rt); return !ok || v == "fp-fixed" }, raceGuard) || v == "fp-fixed" {
		t.Errorf("%s: after a crash, pinned to the failed checkpoint, it answers %q (ok %v)", tile, v, ok)
	}
	for i := 0; i < 5; i++ {
		if v, _, _ = a.served(tile, rt); v == "fp-fixed" {
			t.Errorf("%s: pinned to the failed checkpoint, it served the work tree", tile)
			break
		}
	}
	if _, e = a.op(t, "live-reload/now", tile); e.Result != "ok" {
		t.Fatalf("%s: reload now of the fixed work tree: %+v", tile, e)
	}
	a.waitServed(t, tile, rt, "fp-fixed", raceGuard)
	return "fp-fixed"
}

// covers D119c SC-OPT-OUT SC-ZERO Z12 PO-15 — pause live reload → reload now →
// resume on a main-only static tile (without isolation) removes the record,
// its journal and the view repository. The only leftover is the checkpoint
// store with its deploy log, inert: the remote answers 404, the log and the
// diff 409, and neither a dry run nor a diff captures into it. The tile then
// equals a twin that never opted in (the same files at another path): its
// state, its /components row and its document, path aside; a save publishes
// today's reload and no deployments event. On an isolated daemon, a terminal
// session opened while the tile has a record carries the xbin-deploy remote,
// and one opened after the opt-out carries exactly the twin's two
// GIT_CONFIG_* pairs.
func TestOptOutReturnsToZeroState(t *testing.T) {
	const tile, twin = "apps/oo-tile", "apps/oo-twin"
	a, ws := startPlainDaemon(t)
	for _, p := range []string{tile, twin} {
		mustRT(t, ws, p, "static", "v1", "")
		a.waitServed(t, p, "static", "v1", 30*time.Second)
	}
	tape := a.tape(t)
	record, dir, store, view, _ := dlPaths(ws, tile)

	a.op(t, "live-reload/pause", tile)
	if c, b := a.do("GET", "/api/xbin/checkpoints/"+tile+".git/HEAD", ""); c != 200 {
		t.Errorf("opted in, the checkpoint remote's HEAD: %d %s", c, b)
	}
	for _, p := range []string{tile, twin} {
		mustRT(t, ws, p, "static", "v2", "")
	}
	a.op(t, "live-reload/now", tile)
	a.waitServed(t, tile, "static", "v2", 10*time.Second)
	a.waitServed(t, twin, "static", "v2", 10*time.Second)
	if ans := a.mustPost(t, "live-reload/resume", dlBody(tile)); ans.State.Record {
		t.Fatalf("resuming onto main, the only deployment, kept the record: %+v", ans.State)
	}

	for what, p := range map[string]string{"the record": record, "the deploy journal's directory": dir, "the view repository": view} {
		if isoExists(p) {
			t.Errorf("after the opt-out %s is left: %s", what, p)
		}
	}
	if !isoExists(store) {
		t.Errorf("the checkpoint store and its deploy log went with the opt-out (%s); they stay, inert", store)
	}
	kept := dirHash(t, store)
	if c, b := a.do("GET", "/api/xbin/checkpoints/"+tile+".git/HEAD", ""); c != 404 {
		t.Errorf("after the opt-out the checkpoint remote answers %d %s, want 404", c, b)
	}
	for _, p := range []string{"/api/xbin/deployments/log?tile=" + tile, "/api/xbin/deployments/diff?tile=" + tile} {
		if c, b := a.do("GET", p, ""); c != 409 {
			t.Errorf("after the opt-out %s: %d %s, want 409 (no record)", p, c, b)
		}
	}
	if c, _, b := a.post(t, "live-reload/pause", dlBody(tile, "dryRun", true)); c != 200 {
		t.Errorf("a dry run of pausing after the opt-out: %d %s", c, b)
	}
	if got := dirHash(t, store); got != kept {
		t.Errorf("a dry run or a diff after the opt-out wrote into the inert store")
	}

	// The twin, path aside.
	swap := func(s string) string { return strings.ReplaceAll(s, twin, tile) }
	_, s1 := a.do("GET", "/api/xbin/deployments?tile="+tile, "")
	_, s2 := a.do("GET", "/api/xbin/deployments?tile="+twin, "")
	if s1 != swap(s2) {
		t.Errorf("the state after the opt-out:\n  %s\nthe twin's:\n  %s", s1, s2)
	}
	row := func(p string) string {
		_, body := a.do("GET", "/api/xbin/components", "")
		var rows []json.RawMessage
		_ = json.Unmarshal([]byte(body), &rows)
		for _, r := range rows {
			var c struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(r, &c) == nil && c.Path == p {
				return string(r)
			}
		}
		return ""
	}
	if r1, r2 := row(tile), row(twin); r1 == "" || r1 != swap(r2) {
		t.Errorf("the /components row after the opt-out:\n  %s\nthe twin's:\n  %s", r1, r2)
	}
	token := regexp.MustCompile(`(name="xbin-frame-token" content=")[^"]*"`)
	doc := func(p string) string {
		_, b := a.do("GET", "/c/"+p+"/", "")
		return token.ReplaceAllString(b, `$1"`)
	}
	if d1, d2 := doc(tile), doc(twin); d1 != swap(d2) {
		t.Errorf("the document after the opt-out:\n%s\nthe twin's:\n%s", d1, d2)
	}
	m := tape.mark()
	for _, p := range []string{tile, twin} {
		mustRT(t, ws, p, "static", "v3", "")
	}
	for _, p := range []string{tile, twin} {
		if _, ok := tape.wait(m, isEvent("reload", p), 10*time.Second); !ok {
			t.Errorf("after the opt-out a save to %s published no reload: %s", p, tape.describe(m, p))
		}
	}
	time.Sleep(3 * latencyDebounce) // negative: nothing else follows the saves
	kinds := func(p string) string {
		var out []string
		for _, ev := range tape.since(m) {
			if ev.Component == p {
				out = append(out, ev.Type)
			}
		}
		return strings.Join(out, " ")
	}
	if k1, k2 := kinds(tile), kinds(twin); k1 != k2 || strings.Contains(k1, "deployments") {
		t.Errorf("after the opt-out a save published %q, the twin's %q", k1, k2)
	}

	t.Run("terminal env", func(t *testing.T) {
		d := startIsolatedDaemon(t, isoOpts{})
		ia := d.dl()
		for _, p := range []string{tile, twin} {
			mustRT(t, d.WS, p, "static", "v1", "")
			ia.waitServed(t, p, "static", "v1", 30*time.Second)
		}
		gitEnv := func(p string) string {
			t.Helper()
			out, rc := openTerm(t, ia, p).run(t, `env | grep '^GIT_CONFIG' | sort`, 30*time.Second)
			if rc != 0 {
				t.Fatalf("the session's env on %s: exit %d", p, rc)
			}
			return regexp.MustCompile(`Bearer \S+`).ReplaceAllString(out, "Bearer <token>")
		}
		ia.op(t, "live-reload/pause", tile)
		if env := gitEnv(tile); !strings.Contains(env, "GIT_CONFIG_COUNT=4") ||
			!strings.Contains(env, "remote.xbin-deploy.url") || !strings.Contains(env, "/api/xbin/checkpoints/"+tile+".git") {
			t.Errorf("a session opened while the tile has a record lacks the xbin-deploy remote:\n%s", env)
		}
		mustRT(t, d.WS, tile, "static", "v2", "")
		ia.op(t, "live-reload/now", tile)
		if ans := ia.mustPost(t, "live-reload/resume", dlBody(tile)); ans.State.Record {
			t.Fatalf("resuming kept the record: %+v", ans.State)
		}
		e1, e2 := gitEnv(tile), gitEnv(twin)
		if e1 != e2 || !strings.Contains(e1, "GIT_CONFIG_COUNT=2") || strings.Contains(e1, "xbin-deploy") {
			t.Errorf("a session after the opt-out:\n%s\nthe twin's:\n%s", e1, e2)
		}
	})
}
