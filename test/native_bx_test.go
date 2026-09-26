//go:build integration

package test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// counterNative is plans/native.md §18.1 — used when examples/counter-go
// ships no native.js of its own.
const counterNative = `import { html, render } from '/vendor/xb-native.js';
const api = (p, o) => xbin.fetch(` + "`/api/${xbin.self}${p}`" + `, o);
let count = null, busy = false;
async function load() { count = (await (await api('/count')).json()).count; paint(); }
async function inc() {
  busy = true; paint();
  try { await api('/count', { method: 'POST' }); await load(); }
  finally { busy = false; paint(); }
}
const paint = () => render(html` + "`" + `
  <screen title="Counter" style="form">
    <section>
      <row title="Count" detail=${count ?? '…'} mono="detail"/>
      <button role="primary" icon="plus" ?busy=${busy} @tap=${inc}>+1</button>
    </section>
  </screen>` + "`" + `);
load();
`

// snoopNative tries to use the credential bx lends the browser for things
// it must not reach: every raw fetch must come back 401, while xbin.fetch
// (the tile's frame token) is the tile itself.
const snoopNative = `import { html, render } from '/vendor/xb-native.js';
const st = async (p, f = fetch) => { try { return String((await f(p)).status); } catch (e) { return 'ERR'; } };
const rows = [];
const paint = () => render(html` + "`" + `<screen title="Snoop"><section>${rows.map(([k, v]) => html` + "`" + `<row title=${k} detail=${v}/>` + "`" + `)}</section></screen>` + "`" + `);
paint();
rows.push(['raw-whoami', await st('/api/xbin/whoami')]);
rows.push(['raw-counter-api', await st('/api/apps/counter/count')]);
rows.push(['raw-counter-doc', await st('/c/apps/counter/?native=1')]);
rows.push(['framed-whoami', await (await xbin.fetch('/api/xbin/whoami')).json().then((w) => w.id)]);
paint();
`

// TestBxNative runs bx native tree / preview --native / lint --native
// against a real auth-ON xbind with the owner token, the way bx runs on the
// host: headless Chromium through bx's proxy, the live Go backend, and a
// replayed fixture. Skips without node + Playwright's Chromium
// (PLAYWRIGHT_DIR, or a global install).
func TestBxNative(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	bx := filepath.Join(dir, "bx")
	if out, err := exec.Command("go", "build", "-o", bx, filepath.Join(repo, "cmd", "bx")).CombinedOutput(); err != nil {
		t.Fatalf("build bx: %s", out)
	}
	nws := filepath.Join(dir, "ws")
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	base := "http://" + addr
	cmd := exec.Command(xbindBin, "--workspace", nws, "--listen", addr, "--insecure-vault")
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	startDaemon(t, cmd, nws)
	if !waitFor(func() bool {
		r, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	}, 10*time.Second) {
		t.Fatalf("xbind never healthy: %s", logs.String())
	}

	counter := filepath.Join(nws, "apps", "counter")
	if out, err := exec.Command("cp", "-r", filepath.Join(repo, "examples", "counter-go"), counter).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	if _, err := os.Stat(filepath.Join(counter, "native.js")); err != nil {
		if err := os.WriteFile(filepath.Join(counter, "native.js"), []byte(counterNative), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snoop := filepath.Join(nws, "apps", "snoop")
	must(t, os.MkdirAll(snoop, 0o755))
	must(t, os.WriteFile(filepath.Join(snoop, "xbin.json"), []byte("{}"), 0o644))
	must(t, os.WriteFile(filepath.Join(snoop, "native.js"), []byte(snoopNative), 0o644))
	tok := strings.TrimSpace(mustReadFile(t, filepath.Join(nws, ".xbin", "token")))

	// the cold Go build: wait until the backend answers before timing bx
	if !waitFor(func() bool {
		rq, _ := http.NewRequest("GET", base+"/api/apps/counter/count", nil)
		rq.Header.Set("Authorization", "Bearer "+tok)
		r, err := http.DefaultClient.Do(rq)
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	}, 120*time.Second) {
		t.Fatalf("counter backend never came up: %s", logs.String())
	}

	run := func(args ...string) (string, string, error) {
		c := exec.Command(bx, args...)
		c.Dir = dir
		env := []string{"XBIN_URL=" + base, "XBIN_WORKSPACE=" + nws}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "XBIN_") {
				env = append(env, kv)
			}
		}
		c.Env = env
		var so, se bytes.Buffer
		c.Stdout, c.Stderr = &so, &se
		err := c.Run()
		return so.String(), se.String(), err
	}

	out, errOut, err := run("native", "tree", "apps/counter")
	if strings.Contains(errOut, "headless run unavailable") {
		t.Skipf("no headless browser: %s", errOut)
	}
	if err != nil {
		t.Fatalf("bx native tree: %v\n%s%s", err, out, errOut)
	}
	rows := treeRows(t, out)
	if rows["Count"] != "0" || !strings.Contains(out, `"label": "+1"`) || !strings.Contains(out, `"k": "r.0.1"`) {
		t.Fatalf("counter tree from the live backend:\n%s", out)
	}

	// a replayed fixture instead of the backend, and a step
	fx := filepath.Join(dir, "fx.json")
	must(t, os.WriteFile(fx, []byte(`{"routes": {"GET /api/apps/tile/count": [{"json": {"count": 41}}, {"json": {"count": 42}}], "POST /api/apps/tile/count": {"status": 204}},
		"steps": [{"tap": "r.0.1"}]}`), 0o644))
	out, errOut, err = run("native", "tree", "apps/counter", "--data", fx)
	if err != nil || treeRows(t, out)["Count"] != "42" {
		t.Fatalf("fixture tree: %v\n%s%s", err, out, errOut)
	}

	shot := filepath.Join(dir, "shot.png")
	out, errOut, err = run("preview", "--native", "apps/counter", "--dark", "--out", shot)
	if err != nil || !strings.Contains(out, "wrote "+shot) {
		t.Fatalf("bx preview: %v\n%s%s", err, out, errOut)
	}
	png, err := os.ReadFile(shot)
	if err != nil || len(png) < 24 || string(png[1:4]) != "PNG" ||
		binary.BigEndian.Uint32(png[16:20]) != 780 || binary.BigEndian.Uint32(png[20:24]) != 1688 {
		t.Fatalf("preview is not a 780x1688 PNG (%d bytes, %v)", len(png), err)
	}

	// the tile's code never borrows bx's credential
	out, errOut, err = run("native", "tree", "apps/snoop")
	if err != nil {
		t.Fatalf("snoop tree: %v\n%s%s", err, out, errOut)
	}
	got := treeRows(t, out)
	for k, want := range map[string]string{"raw-whoami": "401", "raw-counter-api": "401", "raw-counter-doc": "401", "framed-whoami": "apps/snoop"} {
		if got[k] != want {
			t.Errorf("snoop %s = %q, want %q (tree %s)", k, got[k], want, out)
		}
	}

	// lint the workspace: both native tiles render; coverage names the rest
	out, errOut, err = run("lint", "--native")
	if err != nil {
		t.Fatalf("bx lint --native: %v\n%s%s", err, out, errOut)
	}
	for _, want := range []string{"apps/counter  (native.js)", "rendered: first tree in", "needs app primitives rev 1: button row screen section", "native coverage: 2 of ", "web only: "} {
		if !strings.Contains(out, want) {
			t.Errorf("lint: no %q in\n%s", want, out)
		}
	}

	// a broken native UI fails the lint with the runtime's own words
	must(t, os.WriteFile(filepath.Join(snoop, "native.js"), []byte("import { html, render } from '/vendor/xb-native.js';\nrender(html`<screen><blink/></screen>`);\n"), 0o644))
	out, _, err = run("lint", "--native", "apps/snoop")
	if err == nil || !strings.Contains(out, "unknown-tag: <blink>") {
		t.Fatalf("broken lint: %v\n%s", err, out)
	}

	// an entry that throws while loading: the runtime document boots it, so
	// the runtime reports kind "module" (the app's fast web fallback), not
	// just an uncaught page error
	must(t, os.WriteFile(filepath.Join(snoop, "native.js"), []byte("import '/vendor/xb-native.js';\nthrow new Error('no config yet');\n"), 0o644))
	out, _, err = run("lint", "--native", "apps/snoop")
	if err == nil || !strings.Contains(out, "module: ") || !strings.Contains(out, "no config yet") {
		t.Fatalf("throwing entry: %v\n%s", err, out)
	}
}

// treeRows maps each row's title to its detail in a printed tree.
func treeRows(t *testing.T, out string) map[string]string {
	t.Helper()
	type node struct {
		T string         `json:"t"`
		P map[string]any `json:"p"`
		C []node         `json:"c"`
	}
	var tree struct {
		V    int  `json:"v"`
		Root node `json:"root"`
	}
	if err := json.Unmarshal([]byte(out), &tree); err != nil || tree.V != 1 {
		t.Fatalf("tree JSON (%v):\n%s", err, out)
	}
	rows := map[string]string{}
	var walk func(n node)
	walk = func(n node) {
		if n.T == "row" {
			title, _ := n.P["title"].(string)
			detail, _ := n.P["detail"].(string)
			rows[title] = detail
		}
		for _, c := range n.C {
			walk(c)
		}
	}
	walk(tree.Root)
	return rows
}
