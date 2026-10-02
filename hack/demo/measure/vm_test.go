//go:build linux && xbinmeasure

package measure

// vm_test.go — Firecracker microVM sandboxes, from the request to the
// sandbox answering, through the product's own paths on an isolated xbind:
//
//   - a tile sandbox (what a sandbox manager such as coding-sandbox, and
//     through it an agent, runs): a manager tile apps/build-farm holding
//     cap:sandboxes drives xbind's tile-sandbox runtime with the SDK
//     (xbin.SandboxAPI(), as examples/sandbox-go does); the client calls it
//     through xbind's proxy: POST /sandboxes (the runtime answers once the
//     VM booted and its in-box agent answered: state running), then POST
//     …/run `echo` — the request to the command's output;
//   - a VM terminal (the ⧉ toggle): /ws/term?vm=1 opened → the shell's
//     prompt → a command's answer;
//   - the same two in a namespace sandbox, for scale;
//   - N sandboxes asked for at once (8, 16, 32, 64): each one's time to
//     answer and the wall time until all did.
//
// Every VM is coding-sandbox's default size "small": 2 GiB, 2 vCPUs, a
// 20 GiB disk (sparse; a new sandbox's guest formats it at its first start).
// Everything a phase starts is deleted before the next one, and the daemon's
// cleanup deletes what is left and checks every process gone.
//
// Not the builtin coding-sandbox itself: it keeps its state in an encrypted
// sqlite resource (a gocryptfs volume), which can't mount where these runs
// were taken (a single-uid user namespace: the setuid fusermount3 is
// inert). It forwards to the same runtime; its own bookkeeping is not in
// these numbers.

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	farmTile = "apps/build-farm" // the manager
	ciTile   = "apps/ci"         // a static tile the terminals open on
)

// vmSize is every sandbox's size: coding-sandbox's default, "small".
var vmSize = map[string]any{"memMiB": 2048, "vcpus": 2, "diskGiB": 20}

// The sample counts (MEASURE_QUICK=1: a few of each, to check the setup).
var (
	vmSeqN   = quick(30, 3) // sequential VM sandboxes
	termSeqN = quick(20, 2) // terminals per mode
	nsSeqN   = quick(20, 2) // namespace sandboxes
	vmLevels = func() []int {
		if os.Getenv("MEASURE_QUICK") == "1" {
			return []int{4}
		}
		return []int{8, 16, 32, 64}
	}()
)

// farmSource is the manager's backend: create, run and delete over xbind's
// tile-sandbox runtime, nothing else (examples/sandbox-go is the fuller
// one).
const farmSource = `package main

import (
	"encoding/json"
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

var sbx = xbin.SandboxAPI()

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sandboxes", func(w http.ResponseWriter, r *http.Request) {
		var spec xbin.SandboxSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		in, err := sbx.Create(r.Context(), spec)
		reply(w, http.StatusCreated, in, err)
	})
	mux.HandleFunc("POST /sandboxes/{name}/run", func(w http.ResponseWriter, r *http.Request) {
		var req xbin.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		res, err := sbx.Sandbox(r.PathValue("name")).Run(r.Context(), req)
		reply(w, 200, res, err)
	})
	mux.HandleFunc("DELETE /sandboxes/{name}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusNoContent, nil, sbx.Delete(r.Context(), r.PathValue("name")))
	})
	xbin.Serve(mux)
}

func reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		xbin.WriteSandboxError(w, err)
		return
	}
	if v == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
`

func TestVM(t *testing.T) {
	d := daemon(t)
	accel := d.RequireVM(t)
	if accel != "kvm" {
		t.Fatalf("VMs run %q here: these measurements are Firecracker on KVM", accel)
	}
	// room for 64 "small" VMs at once (and the terminals' VMs)
	d.Must(t, "PUT", "/api/xbin/vm/policy", map[string]any{"terminals": true, "tiles": true,
		"maxVMs": 100, "budgetMiB": 100 * 2048, "tilesBudgetMiB": 90 * 2048}, 200)
	var vm struct {
		Status map[string]any `json:"status"`
		Policy map[string]any `json:"policy"`
	}
	d.Must(t, "GET", "/api/xbin/vm", nil, 200).Decode(t, &vm)
	t.Logf("VM status %v, policy %v", vm.Status, vm.Policy)

	setupFarm(t, d)
	rec := record(t, "vm-sandbox")
	extra := map[string]any{"accel": accel, "vmStatus": vm.Status, "vmPolicy": vm.Policy, "size": vmSize}

	// the first VM on a fresh workspace builds the guest image (the rootfs
	// as erofs, once per base): its own series, never mixed with the rest
	t.Run("first", func(t *testing.T) {
		r := sandboxOnce(d, "vm", "first-boot", true)
		if r.err != "" {
			rec.add(map[string]any{"phase": "first", "err": r.err}, nil)
			t.Errorf("the first VM: %s", r.err)
		} else {
			rec.add(map[string]any{"phase": "first", "name": r.name, "accel": r.accel},
				map[string]float64{"vm: first ever (builds the guest image)": r.total})
		}
		drain(t, d, []string{r.name})
	})

	t.Run("sequential", func(t *testing.T) {
		for i := 1; i <= vmSeqN; i++ {
			r := sandboxOnce(d, "vm", fmt.Sprintf("build-%02d", i), true)
			if r.err != "" {
				rec.add(map[string]any{"phase": "vm", "i": i, "err": r.err}, nil)
				t.Errorf("sample %d: %s", i, r.err)
			} else {
				rec.add(map[string]any{"phase": "vm", "i": i, "name": r.name, "accel": r.accel},
					map[string]float64{"vm: create (boot + agent ready)": r.create, "vm: first command": r.run,
						"vm: request → answer": r.total, "vm: delete": r.del})
			}
			drain(t, d, []string{r.name})
		}
	})

	t.Run("namespace", func(t *testing.T) {
		for i := 1; i <= nsSeqN; i++ {
			r := sandboxOnce(d, "namespace", fmt.Sprintf("ns-%02d", i), true)
			if r.err != "" {
				rec.add(map[string]any{"phase": "namespace", "i": i, "err": r.err}, nil)
				t.Errorf("namespace sample %d: %s", i, r.err)
			} else {
				rec.add(map[string]any{"phase": "namespace", "i": i, "name": r.name},
					map[string]float64{"namespace: create": r.create, "namespace: request → answer": r.total})
			}
			drain(t, d, []string{r.name})
		}
	})

	t.Run("terminal", func(t *testing.T) {
		for _, mode := range []string{"vm", "namespace"} {
			vmOn := mode == "vm"
			first := termOnce(t, d, ciTile, vmOn)
			if first.err != "" {
				rec.add(map[string]any{"phase": "term-" + mode + "-first", "err": first.err}, nil)
				t.Errorf("the first %s terminal: %s", mode, first.err)
			} else {
				rec.add(map[string]any{"phase": "term-" + mode + "-first", "vm": first.vm},
					map[string]float64{"term " + mode + ": first on the tile (makes its layer/disk)": first.answer})
			}
			for i := 1; i <= termSeqN; i++ {
				r := termOnce(t, d, ciTile, vmOn)
				if r.err != "" {
					rec.add(map[string]any{"phase": "term-" + mode, "i": i, "err": r.err}, nil)
					t.Errorf("%s terminal %d: %s", mode, i, r.err)
					continue
				}
				rec.add(map[string]any{"phase": "term-" + mode, "i": i, "vm": r.vm},
					map[string]float64{"term " + mode + ": open → session frame": r.session,
						"term " + mode + ": open → prompt": r.prompt, "term " + mode + ": open → command answered": r.answer})
			}
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		var levels []map[string]any
		for _, n := range vmLevels {
			lv := concurrentOnce(t, d, n, rec)
			levels = append(levels, lv)
			t.Logf("concurrent %d: %v", n, lv)
		}
		extra["concurrent"] = levels
	})
	rec.done(extra)
}

// setupFarm writes the manager tile (cap:sandboxes approved, the runtime's
// per-tile limits raised for 64 at once) and the static tile terminals open
// on, and waits for the manager's first build.
func setupFarm(t *testing.T, d *xbindtest.Daemon) {
	t.Helper()
	files := map[string]string{
		"go.mod":          "module buildfarm\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": farmSource,
		"index.html":      "<!doctype html><title>Build farm</title><h1>Build farm</h1>\n",
	}
	if err := d.WriteFiles(farmTile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, farmTile, map[string]string{"xbin.json": `{"runtime": "go", "uses": [{"target": "cap:sandboxes", "role": "writer"}]}` + "\n"})
	d.Grant(t, farmTile, "cap:sandboxes", "writer")
	d.Must(t, "PUT", "/api/xbin/sandboxes/policy", map[string]any{"overrides": map[string]any{farmTile: map[string]any{
		"perTile": map[string]int{"max": 200, "running": 100, "memMiB": 400000, "vcpus": 400, "diskGiB": 4000}}}}, 200)
	d.WriteTile(t, ciTile, map[string]string{
		"xbin.json":  `{"runtime": "static"}` + "\n",
		"index.html": "<!doctype html><title>CI</title><h1>CI</h1>\n",
	})
	t0 := time.Now()
	deadline := t0.Add(6 * time.Minute)
	for {
		// a run in a sandbox that doesn't exist: the backend answers (404)
		st, b, err := call("POST", d.URL+"/api/"+farmTile+"/sandboxes/none/run", map[string]any{"cmd": "true"}, owner(d))
		if err == nil && st == 404 && strings.Contains(string(b), "not-found") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the manager never answered: %d %s %v\n%s", st, cut(b), err, d.LogTail(40))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("the manager's backend answered after %s (its first build)", time.Since(t0).Round(time.Millisecond))
}

type sbResult struct {
	name, accel, err        string
	create, run, total, del float64 // ms
}

// sandboxOnce creates a sandbox (mode "vm" or "namespace") through the
// manager, runs one command in it and, with del, deletes it: the times of
// the create (its 201: running), the run, both, and the delete.
func sandboxOnce(d *xbindtest.Daemon, mode, name string, del bool) sbResult {
	r := sbResult{name: name}
	base := d.URL + "/api/" + farmTile + "/sandboxes"
	spec := map[string]any{"name": name, "mode": mode, "start": true}
	for k, v := range vmSize {
		spec[k] = v
	}
	t0 := time.Now()
	st, b, err := call("POST", base, spec, owner(d))
	t1 := time.Now()
	var in struct{ Name, State, StateDetail, Mode, Accel string }
	if err != nil || st != 201 || json.Unmarshal(b, &in) != nil || in.State != "running" || in.Mode != mode {
		r.err = fmt.Sprintf("create: %d %s %v", st, cut(b), err)
		return r
	}
	r.accel = in.Accel
	st, b, err = call("POST", base+"/"+name+"/run", map[string]any{"cmd": "echo ready-$((6*7))", "timeoutMs": 60000}, owner(d))
	t2 := time.Now()
	var run struct {
		ExitCode *int
		Stdout   *struct{ Head string }
	}
	if err != nil || st != 200 || json.Unmarshal(b, &run) != nil || run.ExitCode == nil || *run.ExitCode != 0 || run.Stdout == nil || !strings.Contains(run.Stdout.Head, "ready-42") {
		r.err = fmt.Sprintf("run: %d %s %v", st, cut(b), err)
		return r
	}
	r.create, r.run, r.total = ms(t1.Sub(t0)), ms(t2.Sub(t1)), ms(t2.Sub(t0))
	if del {
		t3 := time.Now()
		if st, b, err := call("DELETE", base+"/"+name, nil, owner(d)); err != nil || st != 204 {
			r.err = fmt.Sprintf("delete: %d %s %v", st, cut(b), err)
			return r
		}
		r.del = ms(time.Since(t3))
	}
	return r
}

// drain deletes the named sandboxes (already deleted is fine) and waits
// until xbind lists no tile sandbox and its removal backlog drained, so the
// next sample starts from nothing.
func drain(t *testing.T, d *xbindtest.Daemon, names []string) {
	t.Helper()
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, b, err := call("DELETE", d.URL+"/api/"+farmTile+"/sandboxes/"+name, nil, owner(d))
			if err != nil || (st != 204 && st != 404) {
				t.Errorf("delete %s: %d %s %v", name, st, cut(b), err)
			}
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var h struct {
			Health struct {
				TileSandboxes struct {
					Trash struct{ Entries int } `json:"trash"`
				} `json:"tileSandboxes"`
			} `json:"health"`
			TileSandboxes []struct{ Name, State string } `json:"tileSandboxes"`
		}
		st, b, err := call("GET", d.URL+"/api/xbin/sandboxes", nil, owner(d))
		if err == nil && st == 200 && json.Unmarshal(b, &h) == nil && len(h.TileSandboxes) == 0 && h.Health.TileSandboxes.Trash.Entries == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tile sandboxes never drained: %d %s %v", st, cut(b), err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// concurrentOnce asks for n VM sandboxes at the same instant (n goroutines
// released together), each then running one command; it records every
// sandbox's times, counts the VMMs running once all answered, deletes them
// all and returns the level's facts.
func concurrentOnce(t *testing.T, d *xbindtest.Daemon, n int, rec *samples) map[string]any {
	t.Helper()
	memBefore := memAvailableMiB()
	res := make([]sbResult, n)
	ends := make([]time.Time, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res[i] = sandboxOnce(d, "vm", fmt.Sprintf("par%d-%02d", n, i), false)
			ends[i] = time.Now()
		}()
	}
	t0 := time.Now()
	close(start)
	wg.Wait()
	wall := time.Since(t0)
	vmms := descendants(d.PID(), "firecracker")
	memAll := memAvailableMiB()
	failed := 0
	var names []string
	for i, r := range res {
		names = append(names, r.name)
		phase := fmt.Sprintf("concurrent-%d", n)
		if r.err != "" {
			failed++
			rec.add(map[string]any{"phase": phase, "i": i, "err": r.err}, nil)
			continue
		}
		rec.add(map[string]any{"phase": phase, "i": i, "name": r.name, "accel": r.accel, "endMs": round3(ms(ends[i].Sub(t0)))},
			map[string]float64{fmt.Sprintf("concurrent %d: request → answer", n): r.total, fmt.Sprintf("concurrent %d: create", n): r.create})
	}
	lv := map[string]any{"n": n, "ok": n - failed, "failed": failed, "wallMs": round3(ms(wall)),
		"firecrackerRunningOnceAllAnswered": vmms, "memAvailableBeforeMiB": memBefore, "memAvailableAllRunningMiB": memAll}
	t0 = time.Now()
	drain(t, d, names)
	lv["deleteAllMs"] = round3(ms(time.Since(t0)))
	if failed > 0 {
		t.Errorf("concurrent %d: %d of %d failed", n, failed, n)
	}
	return lv
}

type termResult struct {
	err                     string
	vm                      bool
	session, prompt, answer float64 // ms from the dial
}

var (
	ansiRe   = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][0-9A-Za-z]|\r`)
	promptRe = regexp.MustCompile(`[#$❯] $`) // root, user, or xbin's own prompt (… ❯)
)

// termOnce opens a terminal on tile (a VM one with vm), waits for the
// session frame, the shell's prompt and a command's answer, then ends the
// session and waits for it to go.
func termOnce(t *testing.T, d *xbindtest.Daemon, tile string, vm bool) termResult {
	t.Helper()
	var r termResult
	q := "/ws/term?cwd=" + tile
	if vm {
		q += "&vm=1"
	}
	t0 := time.Now()
	c, resp, err := d.Dial(t, q)
	if err != nil {
		r.err = fmt.Sprintf("dial: %v %d %s", err, resp.Status, resp)
		return r
	}
	defer c.Close()
	_ = c.WriteMessage(websocket.TextMessage, []byte(`{"op":"resize","cols":120,"rows":32}`))
	var out strings.Builder
	var sid string
	sent := false
	_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
	for {
		typ, msg, err := c.ReadMessage()
		if err != nil {
			r.err = fmt.Sprintf("read: %v (output so far %q)", err, tail(out.String()))
			break
		}
		if typ == websocket.TextMessage {
			var f struct {
				Op string
				ID string
				VM bool
			}
			_ = json.Unmarshal(msg, &f)
			if f.Op == "session" && sid == "" {
				sid, r.vm, r.session = f.ID, f.VM, ms(time.Since(t0))
				if r.vm != vm {
					r.err = fmt.Sprintf("asked vm=%v, the session says vm=%v", vm, r.vm)
					break
				}
			}
			if f.Op == "exit" {
				r.err = fmt.Sprintf("the shell exited (output %q)", tail(out.String()))
				break
			}
			continue
		}
		out.Write(msg)
		plain := ansiRe.ReplaceAllString(out.String(), "")
		if !sent && promptRe.MatchString(plain) {
			r.prompt = ms(time.Since(t0))
			sent = true
			_ = c.WriteMessage(websocket.BinaryMessage, []byte("echo ready-$((6*7))\r"))
			continue
		}
		if sent && strings.Contains(plain, "ready-42") {
			r.answer = ms(time.Since(t0))
			break
		}
	}
	if sid != "" {
		st, b, err := call("DELETE", d.URL+"/ws/term?session="+sid, nil, owner(d))
		if err != nil || st != 204 {
			t.Errorf("ending session %s: %d %s %v", sid, st, cut(b), err)
		}
		// the socket closes once the shell (and a VM's guest) is gone
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				break
			}
		}
	}
	if r.err != "" {
		fmt.Fprintf(os.Stderr, "terminal (vm=%v): %s\n", vm, r.err)
	}
	return r
}

func tail(s string) string {
	if len(s) > 300 {
		return "…" + s[len(s)-300:]
	}
	return s
}
