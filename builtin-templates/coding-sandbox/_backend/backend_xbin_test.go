package main

// backend_xbin_test.go — the `xbin` backend against a double of xbind's
// tile-sandbox runtime (the /api/xbin/sandboxes routes it uses, as the SDK's
// own tests fake them): what each contract request becomes at the runtime
// (D120, D122), the mode the operators chose (auto,
// vm, namespace — never a silent fallback), the egress classes, the
// operators' mounts, the idle stop, what hello leaves out while the runtime
// lacks it, and the runtime's refusals as the consumer sees them (its names
// for our sandboxes never among them). The live end to end is the runtime's
// WP-21 (API.md §Testing on xbind).

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

// The SDK's client is made once per process, so every test here shares one
// gateway socket and swaps the handler behind it (these tests aren't
// parallel).
var gateway struct {
	once sync.Once
	mu   sync.Mutex
	h    http.Handler
	err  error
}

func useRuntime(t *testing.T, h http.Handler) {
	t.Helper()
	gateway.once.Do(func() {
		dir, err := os.MkdirTemp("", "csgw")
		if err != nil {
			gateway.err = err
			return
		}
		sock := filepath.Join(dir, "gw.sock")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			gateway.err = err
			return
		}
		go func() {
			_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gateway.mu.Lock()
				h := gateway.h
				gateway.mu.Unlock()
				h.ServeHTTP(w, r)
			}))
		}()
		os.Setenv("XBIN_GATEWAY", sock)
		os.Setenv("XBIN_TOKEN", "tok")
	})
	if gateway.err != nil {
		t.Fatal(gateway.err)
	}
	gateway.mu.Lock()
	gateway.h = h
	gateway.mu.Unlock()
}

// rtCall is one request the runtime double saw.
type rtCall struct {
	Method, Path string
	Query        url.Values
	Body         string
	Header       http.Header
}

// rtDouble answers the runtime's routes from memory: definitions and
// lifecycle as the runtime would, every command exiting 0 at once, a
// directory of one file, snapshots as a list, clones of a snapshot or of a
// stopped source (a running one's cur/ is 409 state). refuse makes a route
// answer a refusal ("POST start" → 501: a route not built yet). slow > 0
// makes every copy outlast the runtime's wait, as a large one does: a
// snapshot answers 202 pending, a clone `creating`, a restore the sandbox
// "busy: restoring …" — each done after slow more reads of it.
type rtDouble struct {
	mu     sync.Mutex
	rt     xbin.SandboxRuntime
	boxes  map[string]*xbin.SandboxInfo
	specs  map[string]xbin.SandboxSpec
	execs  map[string][]xbin.ExecInfo
	snaps  map[string][]xbin.Snapshot
	seen   []rtCall
	refuse map[string]int
	seq    int
	slow   int
	left   map[string]int // a copy still running: reads before it is done ("box:<name>", "snap:<name>/<sid>", "busy:<name>")
}

// tick counts one read of what key names down: whether its copy is done now.
func (d *rtDouble) tick(key string) bool {
	n, ok := d.left[key]
	if !ok {
		return false
	}
	if n <= 1 {
		delete(d.left, key)
		return true
	}
	d.left[key] = n - 1
	return false
}

func newRuntime(modes ...string) *rtDouble {
	d := &rtDouble{boxes: map[string]*xbin.SandboxInfo{}, specs: map[string]xbin.SandboxSpec{},
		execs: map[string][]xbin.ExecInfo{}, snaps: map[string][]xbin.Snapshot{}, refuse: map[string]int{}, left: map[string]int{}}
	d.rt = xbin.SandboxRuntime{Enabled: true, Isolation: true, Users: "any",
		Caps: []string{"exec", "files", "tar", "tty", "snapshots", "clone"},
		Egress: []xbin.SandboxEgress{{Class: "none", Reach: "none"},
			{Class: "class:internet", Slot: "internet", Ref: "internet", Reach: "internet"},
			{Class: "class:open", Slot: "open"}}, // unbound
		Limits: xbin.SandboxLimits{Sandboxes: 8, Running: 4, WaitMaxSec: 120, FileMax: 64 << 20,
			PerSandbox: xbin.SandboxSizeLimits{MaxMemMiB: 8192, MaxVCPUs: 8, MaxDiskGiB: 200}}}
	for _, m := range []string{"namespace", "vm"} {
		if slices.Contains(modes, m) {
			d.rt.Modes = append(d.rt.Modes, xbin.SandboxMode{Mode: m})
		} else {
			d.rt.Unavailable = append(d.rt.Unavailable, xbin.SandboxMode{Mode: m, Reason: "an admin hasn't enabled " + m + " tile sandboxes"})
		}
	}
	return d
}

// calls are the requests seen whose "METHOD path" (below
// /api/xbin/sandboxes) has the suffix.
func (d *rtDouble) calls(method, suffix string) []rtCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []rtCall
	for _, c := range d.seen {
		if c.Method == method && strings.HasSuffix(c.Path, suffix) && (suffix != "" || c.Path == "") {
			out = append(out, c)
		}
	}
	return out
}

func (d *rtDouble) last(t *testing.T, method, suffix string) rtCall {
	t.Helper()
	cs := d.calls(method, suffix)
	if len(cs) == 0 {
		t.Fatalf("the runtime saw no %s …%s", method, suffix)
	}
	return cs[len(cs)-1]
}

func rtFail(w http.ResponseWriter, status int, refusal, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "refusal": refusal})
}

func rtJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (d *rtDouble) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p := strings.TrimPrefix(r.URL.Path, "/api/xbin/sandboxes")
	d.mu.Lock()
	d.seen = append(d.seen, rtCall{Method: r.Method, Path: p, Query: r.URL.Query(), Body: string(body), Header: r.Header.Clone()})
	d.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		rtFail(w, 403, "not-allowed", "no instance credential")
		return
	}
	seg := strings.Split(strings.Trim(p, "/"), "/")
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, status := range d.refuse {
		if m, suffix, _ := strings.Cut(key, " "); m == r.Method && strings.HasSuffix(p, suffix) {
			name := seg[0]
			rtFail(w, status, statusRefusal[status], "sandbox "+name+": this route isn't built yet")
			return
		}
	}
	box := func() *xbin.SandboxInfo {
		b := d.boxes[seg[0]]
		if b == nil {
			rtFail(w, 404, "not-found", "no sandbox "+seg[0])
		}
		return b
	}
	switch {
	case p == "/runtime":
		rtJSON(w, 200, d.rt)
	case p == "" && r.Method == "GET":
		out := []xbin.SandboxInfo{}
		for _, b := range d.boxes {
			out = append(out, *b)
		}
		rtJSON(w, 200, map[string]any{"sandboxes": out})
	case p == "" && r.Method == "POST":
		var spec xbin.SandboxSpec
		if err := json.Unmarshal(body, &spec); err != nil || spec.Name == "" || (spec.Mode != "vm" && spec.Mode != "namespace") {
			rtFail(w, 400, "invalid", "a name and a mode (vm | namespace)")
			return
		}
		if b := d.boxes[spec.Name]; b != nil {
			rtJSON(w, 200, b)
			return
		}
		state := "stopped"
		if f := spec.From; f != nil { // a clone
			src := d.boxes[f.Sandbox]
			switch {
			case src == nil:
				rtFail(w, 404, "not-found", "no sandbox "+f.Sandbox+" to clone")
				return
			case f.Snapshot != "" && !slices.ContainsFunc(d.snaps[f.Sandbox], func(s xbin.Snapshot) bool { return s.ID == f.Snapshot && !s.Pending }):
				rtFail(w, 404, "not-found", "sandbox "+f.Sandbox+" has no snapshot "+f.Snapshot)
				return
			case f.Snapshot == "" && src.State != "stopped":
				rtFail(w, 409, "state", "sandbox "+f.Sandbox+" is "+src.State+": stop it, or clone a snapshot of it")
				return
			case src.Mode != spec.Mode:
				rtFail(w, 400, "invalid", "a clone runs in its source's mode")
				return
			}
			if d.slow > 0 {
				state, d.left["box:"+spec.Name] = "creating", d.slow
			}
		}
		in := &xbin.SandboxInfo{Name: spec.Name, State: state, Mode: spec.Mode, MemMiB: spec.MemMiB, VCPUs: spec.VCPUs,
			DiskGiB: spec.DiskGiB, Labels: spec.Labels, For: spec.For, ForUser: spec.ForUser, IdleStopMin: spec.IdleStopMin,
			Mounts: spec.Mounts, Users: "any", Version: 1, ClientID: spec.ClientID}
		if spec.Net != nil {
			in.Net = xbin.SandboxNetInfo{Egress: spec.Net.Egress, Reach: map[string]string{"class:internet": "internet"}[spec.Net.Egress]}
		}
		if spec.Defaults != nil {
			in.Defaults = *spec.Defaults
		}
		if in.IdleStopMin == 0 {
			in.IdleStopMin = 30
		}
		d.boxes[spec.Name], d.specs[spec.Name] = in, spec
		rtJSON(w, 201, in)
	case len(seg) == 1:
		b := box()
		if b == nil {
			return
		}
		switch r.Method {
		case "GET":
			if d.tick("box:" + seg[0]) {
				b.State = "stopped"
			}
			if d.tick("busy:" + seg[0]) {
				b.StateDetail = ""
			}
			rtJSON(w, 200, b)
		case "PATCH":
			var q xbin.SandboxPatch
			_ = json.Unmarshal(body, &q)
			if q.Net != nil {
				b.Net.EgressNext = q.Net.Egress
			}
			if q.MemMiB != nil {
				b.MemMiB, b.VCPUs, b.DiskGiB = *q.MemMiB, *q.VCPUs, *q.DiskGiB
			}
			if q.IdleStopMin != nil {
				b.IdleStopMin = *q.IdleStopMin
			}
			if q.Defaults != nil {
				b.Defaults = *q.Defaults
			}
			b.Version++
			rtJSON(w, 200, b)
		case "DELETE":
			delete(d.boxes, seg[0])
			w.WriteHeader(204)
		}
	case len(seg) == 2 && (seg[1] == "start" || seg[1] == "stop"):
		b := box()
		if b == nil {
			return
		}
		if b.State == "creating" || strings.HasPrefix(b.StateDetail, "busy:") {
			rtFail(w, 409, "state", "sandbox "+seg[0]+" is "+b.State+" "+b.StateDetail)
			return
		}
		b.State = map[string]string{"start": "running", "stop": "stopped"}[seg[1]]
		if b.State == "running" && b.Net.EgressNext != "" {
			b.Net.Egress, b.Net.EgressNext = b.Net.EgressNext, ""
		}
		rtJSON(w, 200, b)
	case len(seg) == 2 && seg[1] == "run":
		zero := 0
		rtJSON(w, 200, xbin.RunResult{ExitCode: &zero, Ms: 1, Stdout: &xbin.RunOutput{Head: "ran\n", Bytes: 4},
			Stderr: &xbin.RunOutput{}, Output: &xbin.RunOutput{Head: "ran\n", Bytes: 4}})
	case len(seg) == 2 && seg[1] == "execs" && r.Method == "POST":
		var q xbin.ExecRequest
		_ = json.Unmarshal(body, &q)
		d.seq++
		x := xbin.ExecInfo{ID: fmt.Sprintf("b00001-%d", d.seq), Cmd: q.Cmd, Argv: q.Argv, TTY: q.TTY, State: "running",
			ClientID: q.ClientID, ForUser: q.ForUser, UID: q.UID, Label: q.Label}
		d.execs[seg[0]] = append(d.execs[seg[0]], x)
		rtJSON(w, 201, x)
	case len(seg) == 2 && seg[1] == "execs":
		rtJSON(w, 200, map[string]any{"execs": d.execs[seg[0]]})
	case len(seg) == 3 && seg[1] == "execs":
		for _, x := range d.execs[seg[0]] {
			if x.ID == seg[2] {
				rtJSON(w, 200, x)
				return
			}
		}
		rtFail(w, 404, "not-found", "no exec "+seg[2])
	case len(seg) == 4 && seg[1] == "execs" && seg[3] == "output":
		zero := 0
		rtJSON(w, 200, xbin.OutputChunk{Start: 0, End: 6, Total: 6, Data: "built\n", Encoding: r.URL.Query().Get("encoding"),
			State: "exited", ExitCode: &zero})
	case len(seg) == 4 && seg[1] == "execs" && seg[3] == "tty", len(seg) == 2 && seg[1] == "tty":
		// no upgrade here: a refusal, the way the runtime answers one before
		// it upgrades (naming its own sandbox)
		rtFail(w, 403, "not-allowed", "sandbox "+seg[0]+": "+r.URL.Query().Get("forUser")+" may not open a terminal")
	case len(seg) == 3 && seg[1] == "files" && seg[2] == "list":
		rtJSON(w, 200, xbin.FileList{Path: r.URL.Query().Get("path"), Entries: []xbin.FileEntry{{Name: "main.go", Type: "file", Size: 12}}})
	case len(seg) == 3 && seg[1] == "files" && seg[2] == "content" && r.Method == "GET":
		w.Header().Set("ETag", `"e1"`)
		_, _ = io.WriteString(w, "package main")
	case len(seg) == 3 && seg[1] == "files" && seg[2] == "content":
		rtJSON(w, 200, xbin.FileStat{Path: r.URL.Query().Get("path"), Type: "file", Size: int64(len(body)), ETag: "e2"})
	case len(seg) == 2 && seg[1] == "snapshots" && r.Method == "POST":
		var q struct{ Name, ClientID string }
		_ = json.Unmarshal(body, &q)
		d.seq++
		s := xbin.Snapshot{ID: fmt.Sprintf("s-%d", d.seq), Name: q.Name}
		status := 201
		if d.slow > 0 {
			s.Pending, d.left["snap:"+seg[0]+"/"+s.ID], status = true, d.slow, 202
		}
		d.snaps[seg[0]] = append(d.snaps[seg[0]], s)
		rtJSON(w, status, s)
	case len(seg) == 2 && seg[1] == "snapshots":
		for i, s := range d.snaps[seg[0]] {
			if d.tick("snap:" + seg[0] + "/" + s.ID) {
				d.snaps[seg[0]][i].Pending = false
			}
		}
		rtJSON(w, 200, map[string]any{"snapshots": d.snaps[seg[0]]})
	case len(seg) == 4 && seg[1] == "snapshots" && seg[3] == "restore":
		b := box()
		if b != nil && d.slow > 0 {
			b.StateDetail, d.left["busy:"+seg[0]] = "busy: restoring snapshot "+seg[2], d.slow
		}
		if b != nil {
			rtJSON(w, 200, b)
		}
	default:
		rtFail(w, 404, "not-found", "no route "+r.Method+" "+p)
	}
}

// xbinManager is a manager over the `xbin` backend (as the registry opens
// it: the config's default) and rt, served as main serves it.
func xbinManager(t *testing.T, rt *rtDouble, cfg func(*Config)) (*Manager, *httptest.Server, sandboxcontract.Target) {
	t.Helper()
	useRuntime(t, rt)
	st, err := openStore(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		c, _ := st.config()
		cfg(&c)
		if err := c.validate(); err != nil {
			t.Fatal(err)
		}
		if err := st.putConfig(c); err != nil {
			t.Fatal(err)
		}
	}
	m, err := newManager(st, nil)
	if err != nil || m.beErr != nil {
		t.Fatalf("manager: %v %v", err, m.beErr)
	}
	m.Logf = func(string, ...any) {}
	mux := http.NewServeMux()
	mux.Handle("/sbx/", consumerGuard(m.contractHandler()))
	m.operatorRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { srv.Close(); m.Close(); _ = st.close() })
	tg := sandboxcontract.Target{URL: srv.URL, Consumer: func(r *http.Request, from string) {
		r.Header.Set("X-XBin-From", from)
		r.Header.Set("X-XBin-Role", "consumer")
	}}
	return m, srv, tg
}

func TestXbinBackendMapping(t *testing.T) {
	rt := newRuntime("namespace", "vm")
	m, _, tg := xbinManager(t, rt, func(c *Config) {
		c.AutoStopMin = 45
		c.Mounts = []Mount{{Res: "res:apps/coding-sandbox/cache", Path: "go", At: "/cache", RO: true}}
	})
	a := tg.As(t, "apps/agent").Verified("alice")

	h := hello(t, a)
	if caps := strs(h["caps"]); !slices.Equal(caps, []string{"exec", "files", "tar", "tty", "snapshots", "clone"}) {
		t.Fatalf("hello's caps are the runtime's (never archive): %v", caps)
	}
	if eg := strs(h["egress"]); !slices.Equal(eg, []string{"none", "internet"}) {
		t.Fatalf("hello's egress: none, and internet while its class is bound (open is unbound): %v", eg)
	}

	// create: auto mode takes the VM the runtime offers
	sb := a.Create(map[string]any{"name": "api", "egress": "internet", "size": "medium", "labels": map[string]string{"k": "v"}})
	if sb.Isolation != "vm" || sb.State != "running" || sb.Egress != "internet" {
		t.Fatalf("the sandbox: %+v", sb)
	}
	var spec xbin.SandboxSpec
	_ = json.Unmarshal([]byte(rt.last(t, "POST", "").Body), &spec)
	rec := m.recCopy(sb.ID)
	switch {
	case spec.Name != rec.Runtime || !strings.HasPrefix(spec.Name, "s") || spec.Mode != "vm":
		t.Fatalf("the runtime name and mode: %+v (record %s)", spec, rec.Runtime)
	case spec.MemMiB != 4096 || spec.VCPUs != 4 || spec.DiskGiB != 40:
		t.Fatalf("size medium → %d/%d/%d", spec.MemMiB, spec.VCPUs, spec.DiskGiB)
	case spec.Net == nil || spec.Net.Egress != "class:internet":
		t.Fatalf("egress internet → the class: %+v", spec.Net)
	case spec.IdleStopMin != 45:
		t.Fatalf("autoStopMin → idleStopMin: %d", spec.IdleStopMin)
	case len(spec.Mounts) != 1 || spec.Mounts[0] != (xbin.SandboxMount{Res: "res:apps/coding-sandbox/cache", Path: "go", At: "/cache", RO: true}):
		t.Fatalf("the operators' mounts: %+v", spec.Mounts)
	case spec.For != "apps/agent" || spec.ForUser != "alice" || spec.ClientID != spec.Name:
		t.Fatalf("claims and clientId: %+v", spec)
	case spec.Labels["coding-sandbox/id"] != sb.ID || spec.Labels["k"] != "":
		t.Fatalf("labels: the runtime gets the manager's, never the consumer's: %v", spec.Labels)
	case spec.Defaults == nil || spec.Defaults.Cwd != "/work" || *spec.Defaults.UID != 1000 || spec.Defaults.Shell != "/bin/bash" ||
		spec.Defaults.Env["SANDBOX_ID"] != sb.ID || spec.Defaults.Env["SANDBOX_NAME"] != "api" || spec.Defaults.Env["HOME"] != "/home/dev":
		t.Fatalf("defaults (the layout): %+v", spec.Defaults)
	}
	// started, then prepared: a run as root in /
	if st := rt.last(t, "POST", "/"+rec.Runtime+"/start"); st.Query.Get("wait") != "120" {
		t.Fatalf("start waits limits.waitMaxSec: %v", st.Query)
	}
	var prep xbin.RunRequest
	_ = json.Unmarshal([]byte(rt.last(t, "POST", "/run").Body), &prep)
	if *prep.UID != 0 || prep.Cwd != "/" || prep.Argv[0] != "sh" || !slices.Contains(prep.Argv, "1000:1000") {
		t.Fatalf("prepare: %+v", prep)
	}

	// run: the layout's user and the person
	a.Run(sb.ID, map[string]any{"cmd": "go test ./...", "env": map[string]string{"CI": "1"}})
	var run xbin.RunRequest
	_ = json.Unmarshal([]byte(rt.last(t, "POST", "/run").Body), &run)
	if run.Cmd != "go test ./..." || *run.UID != 1000 || *run.GID != 1000 || run.ForUser != "alice" || run.Env["CI"] != "1" {
		t.Fatalf("run: %+v", run)
	}
	// execs: a clientId prefixed with the consumer; the answer strips it
	x := a.Exec(sb.ID, map[string]any{"cmd": "make serve", "clientId": "c1"})
	var ex xbin.ExecRequest
	_ = json.Unmarshal([]byte(rt.last(t, "POST", "/execs").Body), &ex)
	if ex.ClientID != clientPrefix("apps/agent")+"c1" || ex.ForUser != "alice" || x.ClientID != "c1" {
		t.Fatalf("exec clientId: sent %q, answered %q", ex.ClientID, x.ClientID)
	}
	a.Call("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID+"/output?since=3&max=100&waitMs=50&encoding=base64", nil, 200, nil)
	if q := rt.last(t, "GET", "/output").Query; q.Get("since") != "3" || q.Get("max") != "100" || q.Get("waitMs") != "50" || q.Get("encoding") != "base64" {
		t.Fatalf("output's query: %v", q)
	}

	// PATCH: egress, size, autoStopMin and a rename (SANDBOX_NAME) go down
	var patched sandboxcontract.Sandbox
	a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"egress": "none", "size": "large", "autoStopMin": 10, "name": "api2"}, 200, &patched)
	var pb xbin.SandboxPatch
	_ = json.Unmarshal([]byte(rt.last(t, "PATCH", "/"+rec.Runtime).Body), &pb)
	if pb.Net.Egress != "none" || *pb.MemMiB != 8192 || *pb.IdleStopMin != 10 || pb.Defaults.Env["SANDBOX_NAME"] != "api2" {
		t.Fatalf("the patch: %+v", pb)
	}
	if patched.EgressNext != "none" || patched.Egress != "internet" {
		t.Fatalf("a running sandbox's egress waits for its next start: %+v", patched)
	}

	// files and snapshots pass through
	var fl struct{ Entries []map[string]any }
	a.Call("GET", "/sandboxes/"+sb.ID+"/files/list?path=/work", nil, 200, &fl)
	if len(fl.Entries) != 1 || rt.last(t, "GET", "/files/list").Query.Get("path") != "/work" {
		t.Fatalf("files/list: %+v", fl)
	}
	if got := a.Read(sb.ID, "/work/main.go"); got != "package main" {
		t.Fatalf("files/content: %q", got)
	}
	a.Put(sb.ID, "/work/x.txt", "hello", "&mkdirs=1")
	if c := rt.last(t, "PUT", "/files/content"); c.Body != "hello" || c.Query.Get("path") != "/work/x.txt" {
		t.Fatalf("a write: %+v", c)
	}
	var snap struct{ ID string }
	a.Call("POST", "/sandboxes/"+sb.ID+"/snapshots", map[string]any{"name": "before"}, 201, &snap)
	a.Call("POST", "/sandboxes/"+sb.ID+"/snapshots/"+snap.ID+"/restore", nil, 200, nil)

	// stop and delete
	a.Call("POST", "/sandboxes/"+sb.ID+"/stop?wait=5", nil, 200, nil)
	if q := rt.last(t, "POST", "/stop").Query; q.Get("wait") != "5" {
		t.Fatalf("stop ?wait: %v", q)
	}
	a.Call("DELETE", "/sandboxes/"+sb.ID, nil, 204, nil)
	if len(rt.calls("DELETE", "/"+rec.Runtime)) != 1 {
		t.Fatal("a delete reaches the runtime")
	}
}

func TestXbinBackendModes(t *testing.T) {
	// auto without VMs: a namespace, and isolation says so
	rt := newRuntime("namespace")
	_, _, tg := xbinManager(t, rt, func(c *Config) { c.Mode = "auto" })
	a := tg.As(t, "apps/agent")
	if sb := a.Create(map[string]any{"name": "x"}); sb.Isolation != "namespace" {
		t.Fatalf("auto without VMs: %+v", sb)
	}

	// vm chosen, and the runtime has none: no sandbox, and why — never a namespace
	rt = newRuntime("namespace")
	_, _, tg = xbinManager(t, rt, func(c *Config) { c.Mode = "vm" })
	a = tg.As(t, "apps/agent")
	e := a.Refused("POST", "/sandboxes", map[string]any{"name": "x"}, 503, "unavailable")
	if !strings.Contains(e.Error, "vm") || !strings.Contains(e.Error, "an admin hasn't enabled vm tile sandboxes") {
		t.Fatalf("why: %q", e.Error)
	}
	if len(rt.calls("POST", "")) != 0 {
		t.Fatal("nothing was created at the runtime")
	}
	if notes := fmt.Sprint(hello(t, a)["notes"]); !strings.Contains(notes, "no sandbox can be made now") {
		t.Fatalf("hello says it: %s", notes)
	}

	// namespace chosen, though VMs are there
	rt = newRuntime("namespace", "vm")
	_, _, tg = xbinManager(t, rt, func(c *Config) { c.Mode = "namespace" })
	if sb := tg.As(t, "apps/agent").Create(map[string]any{"name": "x"}); sb.Isolation != "namespace" {
		t.Fatalf("namespace chosen: %+v", sb)
	}
}

// Wave 1 of the runtime: definitions only — every other route 501, and no
// capability claimed. Hello offers nothing that isn't there; a create makes
// the definition and fails at its start, as unsupported, and leaves nothing.
func TestXbinBackendRuntimeUnbuilt(t *testing.T) {
	rt := newRuntime("namespace", "vm")
	rt.rt.Caps = []string{}
	rt.refuse = map[string]int{"POST /start": 501, "POST /run": 501}
	m, _, tg := xbinManager(t, rt, func(c *Config) {
		c.Images = append(c.Images, Image{ID: "node", Setup: "apt-get install -y nodejs"})
	})
	a := tg.As(t, "apps/agent")
	h := hello(t, a)
	notes := fmt.Sprint(h["notes"])
	if len(strs(h["caps"])) != 0 || len(h["images"].([]any)) != 1 ||
		!strings.Contains(notes, "setup script are hidden") || !strings.Contains(notes, "exec or files yet") {
		t.Fatalf("hello while the runtime is unbuilt: %v", h)
	}
	e := a.Refused("POST", "/sandboxes", map[string]any{"name": "x"}, 501, "unsupported")
	if regexp.MustCompile(`\bs[0-9a-f]{12}\b`).MatchString(e.Error) || !strings.Contains(e.Error, "sandbox sb-") {
		t.Fatalf("the runtime's name reached the consumer: %q", e.Error)
	}
	m.mu.Lock()
	n := len(m.recs)
	m.mu.Unlock()
	rt.mu.Lock()
	left := len(rt.boxes)
	rt.mu.Unlock()
	if n != 0 || left != 0 {
		t.Fatalf("a create that failed leaves nothing: %d records, %d at the runtime", n, left)
	}
	a.Refused("POST", "/sandboxes", map[string]any{"name": "y", "image": "node"}, 400, "invalid")
}

// With the runtime's snapshots and clones (WP-20) hello offers images with
// a setup script (D122): the first sandbox of one builds it — a template
// sandbox, its setup run as root, stopped, snapshotted — and clones the
// snapshot. Every copy the runtime answers before it is done (a pending
// snapshot, a creating clone, a busy restore) is waited out, so the
// contract answers each one done; a clone of a running sandbox without a
// snapshot is the runtime's 409, passed through.
func TestXbinBackendCopies(t *testing.T) {
	defer func(p time.Duration) { settlePoll = p }(settlePoll)
	settlePoll = time.Millisecond
	rt := newRuntime("vm")
	rt.slow = 3
	m, _, tg := xbinManager(t, rt, func(c *Config) {
		c.Images = append(c.Images, Image{ID: "node", Setup: "apt-get install -y nodejs"})
	})
	a := tg.As(t, "apps/agent").Verified("alice")
	h := hello(t, a)
	if notes := fmt.Sprint(h["notes"]); strings.Contains(notes, "hidden") || !strings.Contains(fmt.Sprint(h["images"]), "node") {
		t.Fatalf("hello offers the image: %v", h)
	}
	var sb sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "n", "image": "node"}, 201, &sb)
	if sb.State != "running" || sb.Image.ID != "node" {
		t.Fatalf("a sandbox of a built image: %+v", sb)
	}
	m.mu.Lock()
	built := *m.imgs["node"]
	m.mu.Unlock()
	if built.State != "ready" || built.Snapshot == "" {
		t.Fatalf("the image: %+v", built)
	}
	var setup xbin.ExecRequest
	_ = json.Unmarshal([]byte(rt.last(t, "POST", "/"+built.Runtime+"/execs").Body), &setup)
	if setup.Cmd != "apt-get install -y nodejs" || *setup.UID != 0 {
		t.Fatalf("the setup runs as root: %+v", setup)
	}
	if len(rt.calls("POST", "/"+built.Runtime+"/stop")) == 0 || len(rt.calls("POST", "/"+built.Runtime+"/snapshots")) != 1 {
		t.Fatal("the template is stopped, then snapshotted once")
	}
	var spec xbin.SandboxSpec
	_ = json.Unmarshal([]byte(rt.last(t, "POST", "").Body), &spec)
	if rec := m.recCopy(sb.ID); spec.Name != rec.Runtime || spec.From == nil || *spec.From != (xbin.SandboxFrom{Sandbox: built.Runtime, Snapshot: built.Snapshot}) {
		t.Fatalf("the sandbox clones the image's snapshot: %+v", spec)
	}
	// the contract's snapshot answers it taken, its restore the sandbox restored
	var snap struct {
		ID      string
		Pending bool
	}
	a.Call("POST", "/sandboxes/"+sb.ID+"/snapshots", map[string]any{"name": "before"}, 201, &snap)
	if snap.ID == "" || snap.Pending {
		t.Fatalf("the snapshot: %+v", snap)
	}
	var restored sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes/"+sb.ID+"/snapshots/"+snap.ID+"/restore", nil, 200, &restored)
	if strings.HasPrefix(restored.StateDetail, "busy:") {
		t.Fatalf("restored: %+v", restored)
	}
	// a clone of it at the snapshot is made (creating at the runtime, waited out)
	var c sandboxcontract.Sandbox
	a.Call("POST", "/sandboxes?wait=30", map[string]any{"name": "c", "from": map[string]any{"sandbox": sb.ID, "snapshot": snap.ID}}, 201, &c)
	if c.State != "running" {
		t.Fatalf("the clone: %+v", c)
	}
	// not of its running self: the runtime's refusal, naming ours
	e := a.Refused("POST", "/sandboxes", map[string]any{"name": "d", "from": map[string]any{"sandbox": sb.ID}}, 409, "state")
	if strings.Contains(e.Error, m.recCopy(sb.ID).Runtime) || !strings.Contains(e.Error, "clone a snapshot") {
		t.Fatalf("the refusal: %q", e.Error)
	}
}

// A terminal is relayed to the runtime's route with the person and our ids;
// a refusal before the upgrade comes back as the runtime said it, with its
// name for the sandbox replaced by ours.
func TestXbinBackendTerminal(t *testing.T) {
	rt := newRuntime("vm")
	m, srv, tg := xbinManager(t, rt, nil)
	a := tg.As(t, "apps/agent").Verified("alice")
	sb := a.Create(map[string]any{"name": "t"})
	rec := m.recCopy(sb.ID)
	dial := func(path string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+"/sbx/sandboxes/"+sb.ID+path, nil)
		req.Header.Set("X-XBin-From", "apps/agent")
		req.Header.Set("X-XBin-Role", "consumer")
		req.Header.Set("X-XBin-User", "alice")
		req.Header.Set("Sbx-User", "mallory")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, body := dial("/tty?cwd=/work/api&cmd=htop&rows=30&cols=100")
	c := rt.last(t, "GET", "/"+rec.Runtime+"/tty")
	q := c.Query
	if q.Get("forUser") != "alice" || q.Get("sandboxId") != sb.ID || q.Get("cwd") != "/work/api" || q.Get("cmd") != "htop" ||
		q.Get("rows") != "30" || q.Get("uid") != "1000" || c.Header.Get("Upgrade") != "websocket" {
		t.Fatalf("the relayed start: %v %v", q, c.Header)
	}
	if c.Header.Get("X-XBin-User") != "" || c.Header.Get("Sbx-User") != "" || c.Header.Get("X-XBin-From") != "" {
		t.Fatalf("who is asking travels in the query, never the consumer's headers: %v", c.Header)
	}
	if code != 403 || !strings.Contains(body, sb.ID) || strings.Contains(body, rec.Runtime) {
		t.Fatalf("the refusal: %d %s", code, body)
	}
	code, body = dial("/execs/b00001-7/tty")
	q = rt.last(t, "GET", "/execs/b00001-7/tty").Query
	if q.Get("sessionId") != "b00001-7" || q.Get("sandboxId") != sb.ID || q.Get("forUser") != "alice" || code != 403 || strings.Contains(body, rec.Runtime) {
		t.Fatalf("an attach: %v → %d %s", q, code, body)
	}
}

// An exec or snapshot id the runtime's grammar can't hold names nothing:
// the contract's not-found, as the fake answers "nope" — though the SDK
// refuses it as invalid — and nothing reaches the runtime, so a consumer's
// "..%2F" can't reach another sandbox or route (the SDK's typed routes).
func TestXbinBackendUnknownIDs(t *testing.T) {
	rt := newRuntime("vm")
	m, srv, tg := xbinManager(t, rt, nil)
	a := tg.As(t, "apps/agent").Verified("alice")
	sb := a.Create(map[string]any{"name": "ids"})
	rec := m.recCopy(sb.ID)
	rt.mu.Lock()
	before := len(rt.seen)
	rt.mu.Unlock()
	p := "/sandboxes/" + sb.ID
	for _, eid := range []string{"nope", "e1", "..%2F" + rec.Runtime, "b00001-1%2F..%2F..%2Fsb-x", "B00001-1", "b00001-"} {
		a.Refused("GET", p+"/execs/"+eid, nil, 404, "not-found")
		a.Refused("DELETE", p+"/execs/"+eid, nil, 404, "not-found")
		a.Refused("GET", p+"/execs/"+eid+"/output?since=0", nil, 404, "not-found")
		a.Refused("POST", p+"/execs/"+eid+"/stdin", []byte("x"), 404, "not-found")
		a.Refused("POST", p+"/execs/"+eid+"/signal", map[string]any{"signal": "TERM"}, 404, "not-found")
		a.Refused("POST", p+"/execs/"+eid+"/resize", map[string]any{"rows": 5, "cols": 5}, 404, "not-found")
		req, _ := http.NewRequest("GET", srv.URL+"/sbx"+p+"/execs/"+eid+"/tty", nil)
		req.Header.Set("X-XBin-From", "apps/agent")
		req.Header.Set("X-XBin-Role", "consumer")
		req.Header.Set("X-XBin-User", "alice")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 404 || !strings.Contains(string(b), `"refusal":"not-found"`) {
			t.Fatalf("a terminal on exec %s: %d %s", eid, resp.StatusCode, b)
		}
	}
	for _, sid := range []string{"s1", "nope", "s-1%2F..", "..%2F..%2F" + rec.Runtime} {
		a.Refused("POST", p+"/snapshots/"+sid+"/restore", nil, 404, "not-found")
		a.Refused("DELETE", p+"/snapshots/"+sid, nil, 404, "not-found")
	}
	rt.mu.Lock()
	var reached []string
	for _, c := range rt.seen[before:] {
		if strings.Contains(c.Path, "/execs/") || strings.Contains(c.Path, "/snapshots/") || strings.HasSuffix(c.Path, "/tty") {
			reached = append(reached, c.Method+" "+c.Path)
		}
	}
	rt.mu.Unlock()
	if len(reached) != 0 {
		t.Fatalf("ids that name nothing reached the runtime: %v", reached)
	}
	// a real one still goes through
	x := a.Exec(sb.ID, map[string]any{"cmd": "true"})
	a.Call("GET", p+"/execs/"+x.ID, nil, 200, nil)
	if len(rt.calls("GET", "/execs/"+x.ID)) != 1 {
		t.Fatal("a real exec id reaches the runtime")
	}
}
