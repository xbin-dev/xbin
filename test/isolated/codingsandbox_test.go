//go:build linux && integration

package isolated

// codingsandbox_test.go — the builtin sandbox manager, coding-sandbox
// (builtin-templates/coding-sandbox, D122), live on xbind's own runtime:
// an isolated xbind with owner auth on (people are real accounts), the
// template instantiated as apps/cs with cap:sandboxes approved and its
// internet class bound, consumers bound to it through their `sandboxes`
// slot, and everything through xbind's proxy — a consumer's calls are its
// page's (its frame token: X-XBin-From is the consumer; a token a person
// minted makes them the verified person). Its API.md "Testing on xbind" is
// the plan.
//
// With XBIN_E2E_URL they drive an xbind elsewhere instead (test/xbindtest
// remote.go: the QA box's test instance through an ssh tunnel, VM mode on
// real KVM): the tiles are named per run (apps/cs-<run>), consumers are
// written through XBIN_E2E_SH, and on a --no-auth xbind the checks that
// need verified people are skipped, said so.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

// The manager and a consumer bound to it (per run on a remote xbind,
// whose workspace outlives the test: setupCS).
var (
	csTile = "apps/cs"
	csCons = "apps/csc"
)

// csPeople are the accounts the tests sign in: the contract suite's people
// (read access to every apps/ tile, so each may mint a consumer page's
// frame token), an operator (write access to the manager) and a person
// who may not use terminals (D88).
var csPeople = map[string]map[string]any{
	"alice": nil, "bob": nil, "carol": nil, "zoe": nil, "mallory": nil,
	"wanda": {"operator": true},
	"nora":  {"noTerminal": true},
}

// csEnv is a daemon with the manager and its consumer.
type csEnv struct {
	d      *xbindtest.Daemon
	sess   map[string]string // person → their session (a bearer)
	people bool              // people are accounts (owner auth on): sess holds csPeople

	mu    sync.Mutex
	pages map[string]*csPage // tile + "\x00" + person → its frame token (once)
}

type csPage struct {
	once  sync.Once
	token string
	err   error
}

// setupCS boots an isolated xbind with auth on, instantiates the template
// as apps/cs (cap:sandboxes approved, its internet class bound, the
// runtime's per-tile limits raised for the contract's parallel checks, a
// small default size), binds apps/csc to it and signs the people in.
func setupCS(t *testing.T, vm bool) (*csEnv, string) {
	t.Helper()
	d := xbindtest.StartOrConnect(t, xbindtest.Options{Auth: true, Env: []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault"}})
	csTile, csCons = "apps/cs", "apps/csc"
	if d.IsRemote() {
		run := time.Now().Format("0102-150405")
		csTile, csCons = "apps/cs-"+run, "apps/csc-"+run
	}
	accel := ""
	if vm {
		accel = d.RequireVM(t)
	}
	e := &csEnv{d: d, sess: map[string]string{}, pages: map[string]*csPage{}, people: d.HasPeople()}
	var made struct {
		Path          string
		PendingGrants []struct{ From, Target, Role string }
	}
	d.Must(t, "POST", "/api/xbin/templates/new", map[string]string{"source": "coding-sandbox", "path": csTile}, 200).Decode(t, &made)
	if made.Path != csTile || len(made.PendingGrants) != 1 || made.PendingGrants[0].Target != "cap:sandboxes" {
		t.Fatalf("the instance: %+v", made)
	}
	d.WaitComponent(t, csTile)
	d.Grant(t, csTile, "cap:sandboxes", "writer")
	d.Bind(t, csTile, "internet", "internet")
	d.Must(t, "PUT", "/api/xbin/sandboxes/policy", map[string]any{"overrides": map[string]any{csTile: map[string]any{
		"perTile": map[string]int{"max": 200, "running": 100, "memMiB": 400000, "vcpus": 400, "diskGiB": 4000}}}}, 200)
	e.consumer(t, csCons)
	for id, extra := range csPeople {
		if !e.people {
			break
		}
		tiles := map[string]string{"apps/*": "read"}
		x := map[string]any{}
		for k, v := range extra {
			if k == "operator" { // write access to the manager (named per run remotely)
				tiles[csTile] = "write"
				continue
			}
			x[k] = v
		}
		d.AddUser(t, id, "pw-"+id+"-7f3a9c", "user", tiles, x)
		e.sess[id] = d.Login(t, id, "pw-"+id+"-7f3a9c")
	}
	xbindtest.Eventually(t, 5*time.Minute, "the manager's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+csTile+"/ops/state", nil)
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
	sizes := []map[string]any{{"id": "tiny", "title": "Tiny", "memMiB": 512, "vcpus": 1, "diskGiB": 2, "default": true},
		{"id": "small", "title": "Small", "memMiB": 1024, "vcpus": 1, "diskGiB": 4}}
	d.Must(t, "PUT", "/api/"+csTile+"/ops/config", map[string]any{"sizes": sizes}, 200)
	return e, accel
}

// consumer makes tile a consumer page bound to the manager (its sandboxes
// slot), once; a non-nil error says why it couldn't.
func (e *csEnv) consumer(t *testing.T, tile string) {
	t.Helper()
	if err := e.makeConsumer(tile); err != nil {
		t.Fatal(err)
	}
}

func (e *csEnv) makeConsumer(tile string) error {
	if err := e.d.WriteFiles(tile, map[string]string{
		"xbin.json":  `{"interfaces": {"sandboxes": {"kind": "http", "service": "sandbox-manager", "multi": true}}}`,
		"index.html": "<!doctype html><title>a consumer</title>",
	}); err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		st, err := e.status("GET", "/api/xbin/components/"+tile, nil)
		if err == nil && st == 200 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never registered: %d %v", tile, st, err)
		}
	}
	if st, err := e.status("POST", "/api/xbin/bindings", map[string]string{"component": tile, "slot": "sandboxes", "provider": csTile}); err != nil || st != 200 {
		return fmt.Errorf("binding %s: %d %v", tile, st, err)
	}
	return nil
}

// status sends one request as the owner, without failing a test (the
// contract's hooks run on its checks' goroutines).
func (e *csEnv) status(method, path string, body any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.d.URL+path, rd)
	if err != nil {
		return 0, err
	}
	if tok := e.d.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// page is a frame token of tile's page, minted by person ("": the owner —
// a call with it is the consumer's, with no person). A consumer the suite
// names (apps/ct-…) is made and bound first.
func (e *csEnv) page(tile, person string) (string, error) {
	if strings.HasPrefix(tile, "apps/ct-") {
		if _, err := e.once("made\x00"+tile, func() (string, error) { return "", e.makeConsumer(tile) }); err != nil {
			return "", err
		}
	}
	return e.once(tile+"\x00"+person, func() (string, error) {
		cred := e.d.Token()
		if person != "" {
			cred = e.sess[person]
		}
		req, _ := http.NewRequest("GET", e.d.URL+"/api/xbin/frame-token?component="+url.QueryEscape(tile), nil)
		if cred != "" {
			req.Header.Set("Authorization", "Bearer "+cred)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var out struct{ Token string }
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || json.Unmarshal(b, &out) != nil || out.Token == "" {
			return "", fmt.Errorf("a frame token of %s for %q: %d %s", tile, person, resp.StatusCode, b)
		}
		return out.Token, nil
	})
}

// once is f's answer for key, computed once (concurrent callers wait).
func (e *csEnv) once(key string, f func() (string, error)) (string, error) {
	e.mu.Lock()
	p := e.pages[key]
	if p == nil {
		p = &csPage{}
		e.pages[key] = p
	}
	e.mu.Unlock()
	p.once.Do(func() { p.token, p.err = f() })
	return p.token, p.err
}

// pageTok is page, failing the test.
func (e *csEnv) pageTok(t *testing.T, tile, person string) string {
	t.Helper()
	tok, err := e.page(tile, person)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// target is the contract suite's view of the manager through xbind: each
// consumer the suite names is a tile of that path bound to the manager,
// calling from its page; a verified person is that page's token minted by
// their session; an asserted one is Sbx-User, as the suite sets it.
func (e *csEnv) target() sandboxcontract.Target {
	var skip map[string]string
	if !e.people { // the checks that act as verified people
		why := "no verified people: this xbind runs --no-auth"
		skip = map[string]string{"people/visibility": why, "people/owners": why, "partitions/shares": why, "tty/refusals": why}
	}
	return sandboxcontract.Target{
		Skip:     skip,
		URL:      e.d.URL + "/api/" + csTile,
		Client:   &http.Client{Transport: csTransport{e}},
		Consumer: func(r *http.Request, consumer string) { r.Header.Set("X-Csenv-From", consumer) },
		Verified: func(r *http.Request, user string) { r.Header.Set("X-Csenv-User", user) },
		Caps:     []string{"exec", "files", "tar", "tty", "snapshots", "clone"},
		Grace:    5 * time.Second,
	}
}

// csTransport turns the suite's caller (its consumer and verified person,
// in headers of the target's own) into the page token that is them.
type csTransport struct{ e *csEnv }

func (tr csTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tile, person := r.Header.Get("X-Csenv-From"), r.Header.Get("X-Csenv-User")
	r = r.Clone(r.Context())
	r.Header.Del("X-Csenv-From")
	r.Header.Del("X-Csenv-User")
	if tile != "" {
		if _, ok := tr.e.sess[person]; person != "" && !ok {
			return nil, fmt.Errorf("the contract names a person with no account here: %s", person)
		}
		tok, err := tr.e.page(tile, person)
		if err != nil {
			return nil, err
		}
		r.Header.Set("X-XBin-Frame-Token", tok)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// TestCodingSandboxContract runs the sandbox-manager contract's conformance
// suite (sdk/sandboxcontract) against the builtin manager live, namespace
// sandboxes on xbind's runtime: every section, each consumer and person
// real (the in-process run over the fake backend is the template's own
// contract_test.go).
func TestCodingSandboxContract(t *testing.T) {
	e, _ := setupCS(t, false)
	sandboxcontract.Run(t, e.target())
}

// TestCodingSandboxContractVM is the same with VM sandboxes (the manager's
// `auto` mode where the runtime offers VMs; KVM, or QEMU's emulation with
// XBIN_VM_ACCEL=emulate), under a VM budget that holds the suite's
// parallel checks.
func TestCodingSandboxContractVM(t *testing.T) {
	e, accel := setupCS(t, true)
	if !e.d.IsRemote() { // a shared host keeps RequireVM's budget (run it with -parallel 1)
		e.d.Must(t, "PUT", "/api/xbin/vm/policy", map[string]any{"tiles": true, "tilesEmulated": accel == "emulate",
			"maxVMs": 96, "budgetMiB": 98304, "tilesBudgetMiB": 65536}, 200)
	}
	sandboxcontract.Run(t, e.target())
}

// TestCodingSandbox walks coding-sandbox's API.md "Testing on xbind" live
// with namespace sandboxes (range mode where the host delegates a sub-uid
// range): hello, the mode, the layout, commands, files, tar, terminals
// (a gorilla client through the proxy, as a page's xbin.ws; noTerminal
// refused), snapshots, restores and clones, images built once and cloned,
// quotas, the operators' views, egress, an xbind restart and the idle stop.
func TestCodingSandbox(t *testing.T) {
	e, _ := setupCS(t, false)
	runCS(t, e, "namespace", 1)
}

// TestCodingSandboxVM is the same where the runtime offers VMs: the
// manager's `auto` mode makes VM sandboxes (KVM, or QEMU's emulation with
// XBIN_VM_ACCEL=emulate).
func TestCodingSandboxVM(t *testing.T) {
	e, accel := setupCS(t, true)
	slow := time.Duration(1)
	if accel == "emulate" {
		slow = 6
	}
	runCS(t, e, "vm", slow)
}

// ops calls an operators' route (/api/apps/cs/ops/…) as the owner.
func (e *csEnv) ops(t *testing.T, method, sub string, body any, want int, out any) {
	t.Helper()
	r := e.d.Must(t, method, "/api/"+csTile+"/ops"+sub, body, want)
	if out != nil {
		r.Decode(t, out)
	}
}

// csState is GET /ops/state, as far as the tests read it.
type csState struct {
	Offer struct {
		Caps, Egress, Images, Sizes, Notes []string
	}
	Config struct {
		Mode   string
		Images []map[string]any
	}
	Images []struct {
		ID, Runtime, Snapshot, SetupHash, Mode, State, Detail string
		Built                                                 int64
		Previous                                              *struct{ ID, Snapshot string }
	}
	Sandboxes []struct {
		ID, Name, State, Consumer, Runtime, Mode, Isolation string
		Owner                                               struct{ User, Via string }
	}
	Usage struct {
		Consumers map[string]map[string]int64
		People    map[string]map[string]int64
	}
}

func (e *csEnv) state(t *testing.T) csState {
	t.Helper()
	var st csState
	e.ops(t, "GET", "/state", nil, 200, &st)
	return st
}

// images sets the config's images: the base (default) and extra.
func (e *csEnv) images(t *testing.T, extra ...map[string]any) {
	t.Helper()
	ims := []map[string]any{{"id": "base", "title": "Base", "default": true}}
	e.ops(t, "PUT", "/config", map[string]any{"images": append(ims, extra...)}, 200, nil)
}

func runCS(t *testing.T, e *csEnv, mode string, slow time.Duration) {
	d, tg := e.d, e.target()
	cons := func(t *testing.T) sandboxcontract.Caller { return tg.As(t, csCons) }
	c := cons(t)

	t.Run("hello", func(t *testing.T) {
		var h struct {
			Caps, Egress, Notes []string
			Images, Sizes       []struct{ ID string }
		}
		cons(t).Call("GET", "/hello?protocol=1", nil, 200, &h)
		if fmt.Sprint(h.Caps) != "[exec files tar tty snapshots clone]" || fmt.Sprint(h.Egress) != "[none internet]" || len(h.Notes) != 0 {
			t.Errorf("hello: %+v", h)
		}
		if len(h.Images) != 1 || h.Images[0].ID != "base" || len(h.Sizes) != 2 || h.Sizes[0].ID != "tiny" {
			t.Errorf("hello's images and sizes: %+v", h)
		}
		if mode == "namespace" { // `vm` where the runtime has no VMs: no sandbox, the runtime's reason
			e.ops(t, "PUT", "/config", map[string]any{"mode": "vm"}, 200, nil)
			var h2 struct{ Notes []string }
			cons(t).Call("GET", "/hello", nil, 200, &h2)
			r := cons(t).Refused("POST", "/sandboxes", map[string]any{"name": "novm"}, 503, "unavailable")
			if len(h2.Notes) == 0 || !strings.Contains(r.Error, "VM") {
				t.Errorf("mode vm without VMs: notes %v, refusal %+v", h2.Notes, r)
			}
			e.ops(t, "PUT", "/config", map[string]any{"mode": "auto"}, 200, nil)
		}
	})

	// Sandboxes are stopped or deleted once used, so no more than three run
	// at once (a shared host's VM budget: XBIN_E2E_VM_MIB).
	main := c.Create(map[string]any{"name": "main", "egress": "internet", "visibility": "team", "size": "small"})
	t.Run("mode and layout", func(t *testing.T) {
		if main.Isolation != mode || main.Egress != "internet" || main.State != "running" || main.Workdir != "/work" || main.Home != "/home/dev" {
			t.Errorf("main: %+v", main)
		}
		// the first start made the workdir and home (as root) and the
		// layout's user the image's account of its uid; commands run as it
		out := c.Sh(main.ID, `id -u; id -g; echo "$HOME $USER $IN_SANDBOX $SANDBOX_ID $SANDBOX_NAME"; pwd; stat -c %u:%g /work /home/dev; id -un; id -gn; getent passwd 1000 | cut -d: -f1,6,7`)
		if want := "1000\n1000\n/home/dev dev 1 " + main.ID + " main\n/work\n1000:1000\n1000:1000\ndev\ndev\ndev:/home/dev:/bin/bash\n"; out != want {
			t.Errorf("inside: %q, want %q", out, want)
		}
		// no xbin identity inside (plan §8): no XBIN_ variable, so none of
		// the manager's token, and no `xbin` host to call
		if env := c.Sh(main.ID, `env; getent hosts xbin || echo no-xbin-host`); strings.Contains(env, "XBIN_") || !strings.Contains(env, "no-xbin-host") {
			t.Errorf("xbin's identity inside a sandbox:\n%s", env)
		}
		st := e.state(t)
		for _, s := range st.Sandboxes {
			if s.ID == main.ID && (s.Consumer != csCons || s.Mode != mode || s.Runtime == "" || s.Runtime == main.ID) {
				t.Errorf("the operators' view of main: %+v", s)
			}
		}
	})

	t.Run("commands", func(t *testing.T) {
		c := cons(t)
		r := c.Run(main.ID, map[string]any{"cmd": "echo out; echo err >&2; exit 3"})
		if r.ExitCode == nil || *r.ExitCode != 3 || r.Stdout.Head != "out\n" || r.Stderr.Head != "err\n" {
			t.Errorf("run: %+v %+v %+v", r, r.Stdout, r.Stderr)
		}
		// an output long poll answers when output comes, not at its waitMs
		x := c.Exec(main.ID, map[string]any{"cmd": "sleep 1; echo late; sleep 1; echo done", "label": "poll"})
		t0 := time.Now()
		ch := c.Chunk(main.ID, x.ID, "since=0&waitMs=20000")
		if took := time.Since(t0); ch.Data != "late\n" || took < 500*time.Millisecond || took > 10*time.Second*slow {
			t.Errorf("the long poll: %+v after %s", ch, took)
		}
		if out, end := c.Drain(main.ID, x.ID); out != "late\ndone\n" || end.State != "exited" || *end.ExitCode != 0 {
			t.Errorf("drained: %q %+v", out, end)
		}
		// stdin
		cat := c.Exec(main.ID, map[string]any{"cmd": "cat; echo eof", "stdin": true})
		c.Call("POST", "/sandboxes/"+main.ID+"/execs/"+cat.ID+"/stdin", []byte("piped\n"), 204, nil)
		c.Call("POST", "/sandboxes/"+main.ID+"/execs/"+cat.ID+"/stdin?eof=1", []byte("in\n"), 204, nil)
		if out, _ := c.Drain(main.ID, cat.ID); out != "piped\nin\neof\n" {
			t.Errorf("stdin: %q", out)
		}
		// a signal to the group
		sl := c.Exec(main.ID, map[string]any{"cmd": "sleep 600 & wait"})
		c.Call("POST", "/sandboxes/"+main.ID+"/execs/"+sl.ID+"/signal", map[string]string{"signal": "TERM"}, 204, nil)
		if _, end := c.Drain(main.ID, sl.ID); end.State != "killed" || end.Signal != "TERM" {
			t.Errorf("after TERM: %+v", end)
		}
		var list struct{ Execs []sandboxcontract.Exec }
		c.Call("GET", "/sandboxes/"+main.ID+"/execs", nil, 200, &list)
		if len(list.Execs) < 3 {
			t.Errorf("execs: %+v", list.Execs)
		}
	})

	t.Run("files and tar", func(t *testing.T) {
		c := cons(t)
		st := c.Put(main.ID, "/work/dir/a.txt", "one\n", "&mkdirs=1")
		if st.Type != "file" || st.ETag == "" || st.Size != 4 {
			t.Fatalf("put: %+v", st)
		}
		c.Refused("PUT", "/sandboxes/"+main.ID+"/files/content?path=%2Fwork%2Fdir%2Fa.txt&ifMatch=nope", []byte("x"), 412, "precondition")
		st2 := c.Put(main.ID, "/work/dir/a.txt", "two\n", "&ifMatch="+url.QueryEscape(st.ETag))
		if st2.ETag == st.ETag || c.Read(main.ID, "/work/dir/a.txt") != "two\n" || c.Stat(main.ID, "/work/dir/a.txt").ETag != st2.ETag {
			t.Errorf("a conditional write: %+v", st2)
		}
		if owner := c.Sh(main.ID, "stat -c %u:%g /work/dir/a.txt"); owner != "1000:1000\n" {
			t.Errorf("a file written through the contract is owned by %q", owner)
		}
		c.Call("POST", "/sandboxes/"+main.ID+"/files/move", map[string]any{"from": "/work/dir/a.txt", "to": "/work/dir/b.txt"}, 204, nil)
		var ls struct{ Entries []struct{ Name, Type string } }
		c.Call("GET", "/sandboxes/"+main.ID+"/files/list?path=%2Fwork%2Fdir", nil, 200, &ls)
		if len(ls.Entries) != 1 || ls.Entries[0].Name != "b.txt" {
			t.Errorf("list: %+v", ls.Entries)
		}
		var tb []byte
		c.Call("GET", "/sandboxes/"+main.ID+"/tar?path=%2Fwork%2Fdir", nil, 200, &tb)
		if names := tarNames(t, tb); !slices.Contains(names, "b.txt") {
			t.Errorf("tar: %v", names)
		}
		c.Call("PUT", "/sandboxes/"+main.ID+"/tar?path=%2Fwork%2Fin&mkdirs=1", tb, 204, nil)
		if got := c.Read(main.ID, "/work/in/b.txt"); got != "two\n" {
			t.Errorf("tar put: %q", got)
		}
	})

	t.Run("terminals", func(t *testing.T) { testCSTerminals(t, e, main, slow) })

	t.Run("snapshots, restores and clones", func(t *testing.T) {
		c := cons(t)
		c.Put(main.ID, "/work/snap.txt", "v1", "")
		var s1 sandboxcontract.Snapshot
		c.Call("POST", "/sandboxes/"+main.ID+"/snapshots", map[string]any{"name": "one"}, 201, &s1)
		c.Put(main.ID, "/work/snap.txt", "v2", "")
		// a clone of it now (running: the manager snapshots it for the clone)
		now := c.Create(map[string]any{"name": "now", "from": map[string]any{"sandbox": main.ID}})
		if got := c.Read(now.ID, "/work/snap.txt"); got != "v2" || now.Isolation != mode {
			t.Errorf("a clone of the running sandbox: %q %+v", got, now)
		}
		var snaps struct{ Snapshots []sandboxcontract.Snapshot }
		c.Call("GET", "/sandboxes/"+main.ID+"/snapshots", nil, 200, &snaps)
		if len(snaps.Snapshots) != 1 || snaps.Snapshots[0].ID != s1.ID {
			t.Errorf("the source's snapshots after a clone of it: %+v", snaps.Snapshots)
		}
		c.Call("DELETE", "/sandboxes/"+now.ID, nil, 204, nil)
		at := c.Create(map[string]any{"name": "at-one", "from": map[string]any{"sandbox": main.ID, "snapshot": s1.ID}})
		if got := c.Read(at.ID, "/work/snap.txt"); got != "v1" {
			t.Errorf("a clone of the snapshot: %q", got)
		}
		var back sandboxcontract.Sandbox
		c.Call("POST", "/sandboxes/"+main.ID+"/snapshots/"+s1.ID+"/restore", nil, 200, &back)
		if got := c.Read(main.ID, "/work/snap.txt"); got != "v1" || back.State != "running" {
			t.Errorf("restored: %q %+v", got, back)
		}
		c.Call("DELETE", "/sandboxes/"+at.ID, nil, 204, nil)
		c.Call("DELETE", "/sandboxes/"+main.ID+"/snapshots/"+s1.ID, nil, 204, nil)
	})

	t.Run("images", func(t *testing.T) { testCSImages(t, e, mode, slow) })

	// the idle stop: a sandbox that sits idle for a minute stops (checked
	// once the rest has had its minute)
	idle := c.Create(map[string]any{"name": "idle"})
	c.Call("PATCH", "/sandboxes/"+idle.ID, map[string]any{"autoStopMin": 1}, 200, &idle)
	if idle.AutoStopMin != 1 {
		t.Fatalf("autoStopMin: %+v", idle)
	}
	c.Put(idle.ID, idle.Workdir+"/kept", "idle state\n", "")

	t.Run("quotas", func(t *testing.T) {
		alice := cons(t).Verified("alice")
		if !e.people { // no accounts: a person the consumer asserts, whom quotas count alike
			alice = cons(t).Asserting("alice")
		}
		e.ops(t, "PUT", "/config", map[string]any{"quotas": map[string]any{"person": map[string]int{"sandboxes": 1}}}, 200, nil)
		one := alice.Create(map[string]any{"name": "alice's"})
		if one.Owner.User != "alice" || one.Owner.Asserted == e.people {
			t.Errorf("alice's sandbox: %+v", one.Owner)
		}
		alice.Refused("POST", "/sandboxes", map[string]any{"name": "alice's second"}, 429, "limit")
		// a quota on running sandboxes: a start over it is refused
		e.ops(t, "PUT", "/config", map[string]any{"quotas": map[string]any{"people": map[string]any{"alice": map[string]int{"sandboxes": 2, "running": 1}}}}, 200, nil)
		two := alice.Create(map[string]any{"name": "alice's cold", "start": false})
		alice.Refused("POST", "/sandboxes/"+two.ID+"/start", nil, 429, "limit")
		e.ops(t, "PUT", "/config", map[string]any{"quotas": map[string]any{}}, 200, nil)
		alice.Call("POST", "/sandboxes/"+one.ID+"/stop?wait=60", nil, 200, nil)
		alice.Call("POST", "/sandboxes/"+two.ID+"/start?wait=60", nil, 200, nil)
		var usage csState
		e.ops(t, "GET", "/state", nil, 200, &usage)
		if usage.Usage.People["alice"]["sandboxes"] != 2 || usage.Usage.Consumers[csCons]["sandboxes"] < 4 {
			t.Errorf("usage: %+v", usage.Usage)
		}
		alice.Call("DELETE", "/sandboxes/"+one.ID, nil, 204, nil)
		alice.Call("DELETE", "/sandboxes/"+two.ID, nil, 204, nil)
	})

	t.Run("operators", func(t *testing.T) { testCSOperators(t, e, main) })

	t.Run("egress", func(t *testing.T) { testCSEgress(t, e, main, slow) })

	t.Run("idle stop and auto-start", func(t *testing.T) {
		c := cons(t)
		xbindtest.Eventually(t, 4*time.Minute, "the idle sandbox stops", func() (bool, string) {
			got := c.Get(idle.ID)
			return got.State == "stopped", got.State + " " + got.StateDetail
		})
		if got := c.Get(idle.ID); !strings.HasPrefix(got.StateDetail, "idle for 1 minutes") {
			t.Errorf("stopped for another reason: %+v", got)
		}
		// the next command starts it — within the quotas: over them it's refused
		running := int64(0)
		for _, s := range c.List() {
			if s.State == "running" {
				running++
			}
		}
		e.ops(t, "PUT", "/config", map[string]any{"quotas": map[string]any{"consumers": map[string]any{csCons: map[string]int64{"running": running}}}}, 200, nil)
		c.Refused("POST", "/sandboxes/"+idle.ID+"/run", map[string]any{"cmd": "true"}, 429, "limit")
		e.ops(t, "PUT", "/config", map[string]any{"quotas": map[string]any{}}, 200, nil)
		if got := c.Sh(idle.ID, "cat "+idle.Workdir+"/kept"); got != "idle state\n" {
			t.Errorf("after the idle stop: %q", got)
		}
		if got := c.Get(idle.ID); got.State != "running" {
			t.Errorf("auto-started: %+v", got)
		}
		c.Call("POST", "/sandboxes/"+idle.ID+"/stop?wait=60", nil, 200, nil)
	})

	t.Run("an xbind restart", func(t *testing.T) {
		c := cons(t)
		c.Put(main.ID, "/work/keep.txt", "kept\n", "")
		x := c.Exec(main.ID, map[string]any{"cmd": "sleep 600"})
		// an image build under way: the restarted manager finishes the creation
		e.images(t, map[string]any{"id": "slow", "title": "Slow", "setup": fmt.Sprintf("sleep %d; echo slow > /opt/slow", 10*int(slow)), "buildEgress": "none"})
		var sl sandboxcontract.Sandbox
		c.Call("POST", "/sandboxes?wait=1", map[string]any{"name": "slow", "image": "slow"}, 201, &sl)
		if sl.State != "creating" {
			t.Fatalf("a sandbox of an image that builds: %+v", sl)
		}
		d.Restart(t)
		xbindtest.Eventually(t, 5*time.Minute, "the manager is back", func() (bool, string) {
			r := d.Call(t, "GET", "/api/"+csTile+"/ops/state", nil)
			return r.Status == 200, fmt.Sprint(r.Status)
		})
		if got := c.Get(main.ID); got.State != "stopped" {
			t.Errorf("after the restart: %+v", got)
		}
		c.Refused("GET", "/sandboxes/"+main.ID+"/execs/"+x.ID, nil, 410, "lost")
		c.Refused("GET", "/sandboxes/"+main.ID+"/execs/"+x.ID+"/output", nil, 410, "lost")
		if got := c.Read(main.ID, "/work/keep.txt"); got != "kept\n" { // a read starts it again
			t.Errorf("after the restart: %q", got)
		}
		if got := c.Get(main.ID); got.State != "running" {
			t.Errorf("auto-started: %+v", got)
		}
		xbindtest.Eventually(t, 5*time.Minute*slow, "the creation the restart cut is finished", func() (bool, string) {
			got := c.Get(sl.ID)
			return got.State == "running", fmt.Sprintf("%s %s", got.State, got.StateDetail)
		})
		if got := c.Read(sl.ID, "/opt/slow"); got != "slow\n" {
			t.Errorf("the finished sandbox's image: %q", got)
		}
		c.Call("DELETE", "/sandboxes/"+sl.ID, nil, 204, nil)
	})

}

// ttyPath is a terminal route under the manager, dialled as a page does
// (xbin.ws: its frame token in ?frame=).
func (e *csEnv) ttyPath(t *testing.T, sub, tile, person string) string {
	sep := "?"
	if strings.Contains(sub, "?") {
		sep = "&"
	}
	return "/api/" + csTile + "/sbx" + sub + sep + "frame=" + url.QueryEscape(e.pageTok(t, tile, person))
}

// testCSTerminals: terminals relayed byte for byte through the manager — a
// new shell and an attach to a tty exec, with a gorilla client through
// xbind's proxy as a consumer's page dials them; the person reaches the
// runtime (a noTerminal person is refused there, D88); the tile's own
// page's terminal needs write access to the tile.
func testCSTerminals(t *testing.T, e *csEnv, main sandboxcontract.Sandbox, slow time.Duration) {
	d, tg := e.d, e.target()
	c := tg.As(t, csCons)

	conn, r, err := d.Dial(t, e.ttyPath(t, "/sandboxes/"+main.ID+"/tty?cwd=%2Fwork&rows=24&cols=80", csCons, ""))
	if err != nil {
		t.Fatalf("a new terminal: %v (%d %s)", err, r.Status, r)
	}
	if sess := readSession(t, conn); sess.Sandbox != main.ID || sess.ID == "" {
		t.Errorf("the session frame: %+v (want the contract's ids)", sess)
	}
	send(t, conn, `echo "tty-$((6*7)):$PWD:$(id -u)"`+"\r")
	readUntil(t, conn, "tty-42:/work:1000", 30*time.Second*slow)
	send(t, conn, "exit\r")
	if code := readExit(t, conn, 30*time.Second*slow); code == nil || *code != 0 {
		t.Errorf("the exit frame: %v", code)
	}
	conn.Close()

	// an attach to a tty exec, which outlives its client (by a person, where
	// people are accounts)
	person := ""
	if e.people {
		person = "alice"
	}
	x := c.Exec(main.ID, map[string]any{"cmd": "cat", "tty": true, "rows": 24, "cols": 80})
	conn, r, err = d.Dial(t, e.ttyPath(t, "/sandboxes/"+main.ID+"/execs/"+x.ID+"/tty", csCons, person))
	if err != nil {
		t.Fatalf("an attach: %v (%d %s)", err, r.Status, r)
	}
	if sess := readSession(t, conn); sess.ID != x.ID || sess.Sandbox != main.ID {
		t.Errorf("the attach's session frame: %+v", sess)
	}
	c.Call("POST", "/sandboxes/"+main.ID+"/execs/"+x.ID+"/resize", map[string]int{"rows": 30, "cols": 100}, 204, nil)
	send(t, conn, "typed-through\r")
	readUntil(t, conn, "typed-through", 30*time.Second*slow)
	conn.Close()
	time.Sleep(200 * time.Millisecond)
	var got sandboxcontract.Exec
	c.Call("GET", "/sandboxes/"+main.ID+"/execs/"+x.ID, nil, 200, &got)
	if got.State != "running" || !got.TTY {
		t.Errorf("a detached tty exec: %+v", got)
	}
	c.Call("DELETE", "/sandboxes/"+main.ID+"/execs/"+x.ID, nil, 204, nil)

	if !e.people { // the tile's own page as its owner
		own := tg.As(t, csTile).Create(map[string]any{"name": "the page's"})
		conn, r, err = d.Dial(t, e.ttyPath(t, "/sandboxes/"+own.ID+"/tty?cwd=%2Fwork", csTile, ""))
		if err != nil {
			t.Fatalf("the page's terminal: %v (%d %s)", err, r.Status, r)
		}
		readSession(t, conn)
		send(t, conn, "echo page-$((2+3))\r")
		readUntil(t, conn, "page-5", 30*time.Second*slow)
		conn.Close()
		tg.As(t, csTile).Call("DELETE", "/sandboxes/"+own.ID, nil, 204, nil)
		t.Log("no people here (--no-auth): noTerminal and the page's read-only people aren't checked")
		return
	}

	// nora may not use terminals: the runtime refuses the person the manager
	// passes on (forUser) — a new terminal, an attach, a tty exec
	nora := c.Verified("nora")
	nora.Get(main.ID) // a team sandbox: she may use it otherwise
	nora.Sh(main.ID, "true")
	x = c.Exec(main.ID, map[string]any{"cmd": "cat", "tty": true})
	for _, sub := range []string{"/sandboxes/" + main.ID + "/tty", "/sandboxes/" + main.ID + "/execs/" + x.ID + "/tty"} {
		conn, r, err := d.Dial(t, e.ttyPath(t, sub, csCons, "nora"))
		if err == nil {
			conn.Close()
			t.Errorf("nora (noTerminal) opened %s", sub)
		} else if r.Status != 403 || refusedAs(t, xbindtest.Resp{Body: r.Body}).Refusal != "not-allowed" {
			t.Errorf("nora's terminal %s: %d %s", sub, r.Status, r)
		}
	}
	nora.Refused("POST", "/sandboxes/"+main.ID+"/execs", map[string]any{"cmd": "sh", "tty": true}, 403, "not-allowed")
	c.Call("DELETE", "/sandboxes/"+main.ID+"/execs/"+x.ID, nil, 204, nil)

	// the tile's own page (<bx-terminal src="/api/apps/cs/sbx/…/tty">): a
	// partition of its own; wanda (write access) opens a terminal there,
	// alice (read access) may look and never change
	wanda, alice := tg.As(t, csTile).Verified("wanda"), tg.As(t, csTile).Verified("alice")
	own := wanda.Create(map[string]any{"name": "the page's", "visibility": "team"})
	conn, r, err = d.Dial(t, e.ttyPath(t, "/sandboxes/"+own.ID+"/tty?cwd=%2Fwork", csTile, "wanda"))
	if err != nil {
		t.Fatalf("the page's terminal: %v (%d %s)", err, r.Status, r)
	}
	readSession(t, conn)
	send(t, conn, "echo page-$((2+3))\r")
	readUntil(t, conn, "page-5", 30*time.Second*slow)
	conn.Close()
	alice.Get(own.ID)
	alice.Refused("POST", "/sandboxes/"+own.ID+"/run", map[string]any{"cmd": "true"}, 403, "not-allowed")
	alice.Refused("POST", "/sandboxes", map[string]any{"name": "alice's"}, 403, "not-allowed")
	if _, r, err := d.Dial(t, e.ttyPath(t, "/sandboxes/"+own.ID+"/tty", csTile, "alice")); err == nil || r.Status != 403 {
		t.Errorf("alice (read access) opened the page's terminal: %v %d", err, r.Status)
	}
	wanda.Call("DELETE", "/sandboxes/"+own.ID, nil, 204, nil)
}

// testCSImages: an image's setup script runs once, as root, in a template
// sandbox the manager snapshots; the image's sandboxes are clones of it; a
// changed script builds it again.
func testCSImages(t *testing.T, e *csEnv, mode string, slow time.Duration) {
	c := e.target().As(t, csCons)
	setup := `date +%s%N > /opt/built; id -u > /opt/built-uid; echo "$IMAGE_ID $SANDBOX_USER $SANDBOX_HOME $SANDBOX_WORKDIR" > /opt/built-env`
	e.images(t, map[string]any{"id": "tools", "title": "Tools", "setup": setup, "buildEgress": "none"})
	make := func(name string) sandboxcontract.Sandbox {
		t.Helper()
		var sb sandboxcontract.Sandbox
		c.Call("POST", "/sandboxes?wait=120", map[string]any{"name": name, "image": "tools"}, 201, &sb)
		xbindtest.Eventually(t, 5*time.Minute*slow, name+" made", func() (bool, string) {
			sb = c.Get(sb.ID)
			return sb.State == "running", sb.State + " " + sb.StateDetail
		})
		if sb.Image.ID != "tools" || sb.Isolation != mode {
			t.Errorf("%s: %+v", name, sb)
		}
		return sb
	}
	image := func() (out struct {
		ID, Runtime, Snapshot, SetupHash, Mode, State, Detail string
		Built                                                 int64
		Previous                                              *struct{ ID, Snapshot string }
	}) {
		for _, im := range e.state(t).Images {
			if im.ID == "tools" {
				return im
			}
		}
		t.Fatal("the image isn't listed")
		return
	}
	i1 := make("i1")
	b1 := c.Read(i1.ID, "/opt/built")
	if uid, env := c.Read(i1.ID, "/opt/built-uid"), c.Read(i1.ID, "/opt/built-env"); uid != "0\n" || env != "tools dev /home/dev /work\n" {
		t.Errorf("the setup ran as %q with %q", uid, env)
	}
	im1 := image()
	if im1.State != "ready" || im1.Snapshot == "" || im1.Runtime == "" || im1.Mode != mode {
		t.Fatalf("the built image: %+v", im1)
	}
	c.Call("POST", "/sandboxes/"+i1.ID+"/stop?wait=60", nil, 200, nil)
	for _, s := range c.List() {
		if s.ID == im1.Runtime || strings.HasPrefix(s.Name, "img") {
			t.Errorf("a consumer lists the image's template sandbox: %+v", s)
		}
	}
	// the next sandbox of it is a clone: no second build
	i2 := make("i2")
	if b2 := c.Read(i2.ID, "/opt/built"); b2 != b1 {
		t.Errorf("the image built again: %q, then %q", b1, b2)
	}
	if im2 := image(); im2.Built != im1.Built || im2.Snapshot != im1.Snapshot {
		t.Errorf("the image after a second sandbox: %+v (was %+v)", im2, im1)
	}
	c.Call("POST", "/sandboxes/"+i2.ID+"/stop?wait=60", nil, 200, nil)
	// a changed script builds it again
	e.images(t, map[string]any{"id": "tools", "title": "Tools", "setup": setup + "; echo v2 > /opt/v2", "buildEgress": "none"})
	i3 := make("i3")
	if got := c.Read(i3.ID, "/opt/v2"); got != "v2\n" || c.Read(i3.ID, "/opt/built") == b1 {
		t.Errorf("a changed script: /opt/v2 %q", got)
	}
	if im3 := image(); im3.State != "ready" || im3.Snapshot == "" || im3.SetupHash == im1.SetupHash || im3.Runtime == im1.Runtime {
		t.Errorf("the rebuilt image: %+v (was %+v)", im3, im1)
	}
	for _, sb := range []sandboxcontract.Sandbox{i1, i2, i3} {
		c.Call("DELETE", "/sandboxes/"+sb.ID, nil, 204, nil)
	}
	e.images(t)
}

// testCSOperators: the tile's owner and the people with write access to it
// see every consumer's sandboxes and run their lifecycle; nobody else does;
// who may use a sandbox changes only through its home consumer.
func testCSOperators(t *testing.T, e *csEnv, main sandboxcontract.Sandbox) {
	d := e.d
	var me struct {
		User, Level     string
		Write, Operator bool
	}
	d.Must(t, "GET", "/api/"+csTile+"/me", nil, 200).Decode(t, &me)
	if !me.Operator {
		t.Errorf("the owner: %+v", me)
	}
	if !e.people {
		t.Log("no people here (--no-auth): the operators with write access and the readers aren't checked")
	} else {
		testCSOperatorPeople(t, e)
	}
	// every consumer's, with its consumer and owner
	st := e.state(t)
	found := false
	for _, s := range st.Sandboxes {
		if s.ID == main.ID {
			found = s.Consumer == csCons && s.Owner.Via == csCons
		}
	}
	if !found || st.Usage.Consumers[csCons]["sandboxes"] == 0 {
		t.Errorf("the operators' view: %+v", st)
	}
	// lifecycle, within the partition's rules
	c := e.target().As(t, csCons)
	var v sandboxcontract.Sandbox
	e.ops(t, "POST", "/sandboxes/"+main.ID+"/stop?wait=60", nil, 200, &v)
	if v.State != "stopped" || c.Get(main.ID).State != "stopped" {
		t.Errorf("an operator's stop: %+v", v)
	}
	e.ops(t, "POST", "/sandboxes/"+main.ID+"/start?wait=60", nil, 200, &v)
	if v.State != "running" {
		t.Errorf("an operator's start: %+v", v)
	}
	e.ops(t, "PATCH", "/sandboxes/"+main.ID, map[string]any{"labels": map[string]string{"ops": "1"}}, 200, &v)
	if v.Labels["ops"] != "1" {
		t.Errorf("an operator's PATCH: %+v", v)
	}
	if r := d.Call(t, "PATCH", "/api/"+csTile+"/ops/sandboxes/"+main.ID, map[string]any{"visibility": "private"}); r.Status != 403 {
		t.Errorf("an operator changed who may use it: %d %s", r.Status, r)
	}
	e.ops(t, "GET", "/sandboxes/"+main.ID+"/snapshots", nil, 200, nil)
	c.Sh(main.ID, "true") // prepared, running, as before
}

// testCSOperatorPeople: wanda (write access to the tile) is an operator;
// alice (read access) is not.
func testCSOperatorPeople(t *testing.T, e *csEnv) {
	d := e.d
	var me struct {
		User, Level     string
		Write, Operator bool
	}
	d.Must(t, "GET", "/api/"+csTile+"/me", nil, 200, xbindtest.FrameHeader(e.pageTok(t, csTile, "wanda"))).Decode(t, &me)
	if me.User != "wanda" || me.Level != "write" || !me.Write || !me.Operator {
		t.Errorf("wanda (write access): %+v", me)
	}
	d.Must(t, "GET", "/api/"+csTile+"/ops/state", nil, 200, xbindtest.FrameHeader(e.pageTok(t, csTile, "wanda")))
	aliceHdr := xbindtest.FrameHeader(e.pageTok(t, csTile, "alice"))
	d.Must(t, "GET", "/api/"+csTile+"/me", nil, 200, aliceHdr).Decode(t, &me)
	if me.User != "alice" || me.Level != "read" || me.Write || me.Operator {
		t.Errorf("alice (read access): %+v", me)
	}
	if r := d.Call(t, "GET", "/api/"+csTile+"/ops/state", nil, aliceHdr); r.Status != 403 || refusedAs(t, r).Refusal != "not-allowed" {
		t.Errorf("alice's /ops/state: %d %s", r.Status, r)
	}
}

// testCSEgress: `internet` reaches the internet (where the host does) and
// neither the host nor its LAN; `none` reaches nothing; the unbound `open`
// class isn't offered. On a remote xbind, XBIN_E2E_HOST_TCP names a port
// the host listens on at every address (its sshd, host:port): `internet`
// reaches it at none of the host's own addresses (`ip addr`, public ones
// included). A public address a NAT outside the host maps to it (a cloud
// VM's) isn't one: the flow leaves the host and comes back as any internet
// client's would (docs/isolation.md), so it is only logged.
func testCSEgress(t *testing.T, e *csEnv, main sandboxcontract.Sandbox, slow time.Duration) {
	c := e.target().As(t, csCons)
	c.Refused("POST", "/sandboxes", map[string]any{"name": "open", "egress": "open"}, 400, "invalid")
	connect := func(id, addr string) sandboxcontract.RunResult {
		host, port, _ := net.SplitHostPort(addr)
		return c.Run(id, map[string]any{"argv": []string{"bash", "-c", "exec 3<>/dev/tcp/" + host + "/" + port}, "timeoutMs": 30000})
	}
	conn, err := net.DialTimeout("tcp", "1.1.1.1:443", 3*time.Second)
	if err == nil {
		conn.Close()
	}
	if err == nil || e.d.IsRemote() { // a remote xbind's host (a server) reaches it
		if r := connect(main.ID, "1.1.1.1:443"); r.ExitCode == nil || *r.ExitCode != 0 {
			t.Errorf("internet: 1.1.1.1:443 unreachable: %+v %+v", r, r.Stderr)
		}
	} else {
		t.Logf("the host doesn't reach the internet (%v): only the refusals are checked", err)
	}
	if hp := os.Getenv("XBIN_E2E_HOST_TCP"); e.d.IsRemote() && hp != "" {
		given, port, err := net.SplitHostPort(hp)
		if err != nil {
			t.Fatalf("XBIN_E2E_HOST_TCP %q: %v", hp, err)
		}
		out, err := e.d.HostSh(`ip -o addr show scope global | awk '{ sub("/.*", "", $4); print $4 }'`)
		own := strings.Fields(out)
		if err != nil || len(own) == 0 {
			t.Fatalf("the host's own addresses: %q %v", out, err)
		}
		for _, ip := range own {
			t0 := time.Now()
			if r := connect(main.ID, net.JoinHostPort(ip, port)); r.ExitCode == nil || *r.ExitCode == 0 || r.TimedOut {
				t.Errorf("internet reached the host's own address %s port %s: %+v", ip, port, r)
			}
			if took := time.Since(t0); took > 15*time.Second*slow {
				t.Errorf("the refusal took %s", took)
			}
		}
		if !slices.Contains(own, given) {
			r := connect(main.ID, hp)
			t.Logf("%s isn't one of the host's addresses %v (a NAT outside it): a sandbox reaches it as any internet client does (connected: %v)", given, own, r.ExitCode != nil && *r.ExitCode == 0)
		}
	} else if e.d.IsRemote() {
		t.Log("no XBIN_E2E_HOST_TCP: the remote host's own address isn't checked")
	}
	if ip := privateHostIP(); ip != "" && !e.d.IsRemote() {
		ln, err := net.Listen("tcp", ip+":0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		accepted := make(chan struct{}, 1)
		go func() {
			if conn, err := ln.Accept(); err == nil {
				conn.Close()
				accepted <- struct{}{}
			}
		}()
		t0 := time.Now()
		if r := connect(main.ID, ln.Addr().String()); r.ExitCode == nil || *r.ExitCode == 0 || r.TimedOut {
			t.Errorf("internet reached the host's LAN address %s: %+v", ln.Addr(), r)
		}
		select {
		case <-accepted:
			t.Errorf("a connection from the sandbox reached %s", ln.Addr())
		default:
		}
		if took := time.Since(t0); took > 15*time.Second*slow {
			t.Errorf("the refusal took %s", took)
		}
	}
	quiet := c.Create(map[string]any{"name": "quiet"})
	if quiet.Egress != "none" {
		t.Errorf("the default egress: %+v", quiet)
	}
	if r := connect(quiet.ID, "1.1.1.1:443"); r.ExitCode == nil || *r.ExitCode == 0 || r.TimedOut {
		t.Errorf("egress none connected: %+v", r)
	}
	c.Call("DELETE", "/sandboxes/"+quiet.ID, nil, 204, nil)
}

// privateHostIP is one of the host's private (RFC 1918) IPv4 addresses, or
// "" (a host on a public address has no LAN to keep a sandbox from).
func privateHostIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
			return n.IP.String()
		}
	}
	return ""
}
