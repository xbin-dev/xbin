package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// zsDaemon is an in-process xbind on a zero-state workspace, auth on, no
// isolation: its State (what the goldens read besides HTTP), its URL and
// the owner token.
type zsDaemon struct {
	st    *State
	url   string
	owner string
	ws    string
}

// zsNodeServer is a node backend that answers every request with what it
// received: the path and the X-XBin-* headers.
const zsNodeServer = `const http = require('http');
http.createServer((req, res) => {
  const h = {};
  for (const [k, v] of Object.entries(req.headers)) if (k.startsWith('x-xbin-')) h[k] = v;
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify({url: req.url, xbin: h}));
}).listen(process.env.XBIN_SOCKET);
`

// zsWorkspace writes the zero-state fixture: a static tile, a node tile, a
// node tile whose backend can't build, a tile whose own path holds +. No
// tile has opted in to anything; the broker's backfills add what every
// workspace has.
func zsWorkspace(t *testing.T) string {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	for rel, body := range map[string]string{
		"xbin.json":                        `{"schema":1}`,
		"apps/zs/xbin.json":                `{"title":"Zero"}`,
		"apps/zs/index.html":               "<!doctype html><html><head></head><body>zs</body></html>\n",
		"apps/zsnode/xbin.json":            `{"runtime":"node"}`,
		"apps/zsnode/backend/server.js":    zsNodeServer,
		"apps/zsnode/index.html":           "<!doctype html><html><head></head><body>node</body></html>\n",
		"apps/zsbroken/xbin.json":          `{"runtime":"node"}`,
		"notes+ideas/xbin.json":            `{}`,
		"notes+ideas/index.html":           "<!doctype html><html><head></head><body>n</body></html>\n",
		"apps/zsbroken/backend/README.txt": "no server.js: the build fails\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

// zsBoot runs the boot steps on ws exactly as Run does — kept here so the
// test holds the State — and serves until the test ends.
func zsBoot(t *testing.T, ws string) *zsDaemon {
	t.Helper()
	quiet(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Workspace: ws, Listener: ln, Listen: ln.Addr().String(), InsecureVault: true,
		Privileges: NoPrivileges{}, Stdout: io.Discard, Version: "test", LimitMem: "2G"}
	if sdk, err := filepath.Abs("../../sdk"); err == nil {
		t.Setenv("XBIN_SDK_PATH", sdk)
	}
	t.Setenv("XBIN_LIMIT_DISK", "50G") // the default quota, whatever the environment says
	d, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Cfg: cfg, WS: d.ws, trusted: d.trusted, externalURL: d.externalURL, overlay: d.overlay,
		Started: time.Now(), priv: cfg.Privileges}
	for _, s := range Steps {
		if err := s.Run(st); err != nil {
			t.Fatalf("boot step %s: %v", s.Name, err)
		}
	}
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- st.serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the daemon did not stop")
		}
	})
	select {
	case addr := <-ready:
		return &zsDaemon{st: st, url: "http://" + addr, owner: st.Auth.OwnerTokenValue(), ws: d.ws}
	case err := <-done:
		t.Fatalf("serve ended: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon never served")
	}
	return nil
}

// do sends a request with the given credential ("Bearer …", or a
// "Cookie: name=value" header) and returns the status and body.
func (d *zsDaemon) do(t *testing.T, method, path, cred string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, d.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := strings.CutPrefix(cred, "Cookie: "); ok {
		req.Header.Set("Cookie", c)
	} else if cred != "" {
		req.Header.Set("Authorization", cred)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// zsMasked are the keys whose values two runs of today's xbind never
// share (process ids, clocks, counters, host facts); their values render
// as "<masked>", their presence stays asserted.
var zsMasked = map[string]bool{
	"pid": true, "uptimeSec": true, "lastReqSec": true, "rssKb": true, "threads": true, "fds": true,
	"cpuSec": true, "started": true, "ts": true, "heapMB": true, "goroutines": true,
	"numCPU": true, "kernel": true, "uid": true, "namespaces": true, "version": true,
	"series": true, "cur": true, "protections": true, "health": true,
}

// zsNorm renders a JSON answer with sorted keys, two-space indented, the
// zsMasked values masked; drop removes, and mask masks, the values at
// dotted paths ("*" = every list item or map value) whose presence or
// value the host decides (a delegated cgroup), not xbind.
func zsNorm(t *testing.T, b []byte, drop, mask []string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "non-JSON: " + strings.TrimSpace(string(b))
	}
	var at func(v any, path []string, leaf func(m map[string]any, k string))
	at = func(v any, path []string, leaf func(m map[string]any, k string)) {
		switch x := v.(type) {
		case map[string]any:
			if len(path) == 1 {
				if _, ok := x[path[0]]; ok {
					leaf(x, path[0])
				}
				return
			}
			if path[0] == "*" {
				for _, e := range x {
					at(e, path[1:], leaf)
				}
			} else if e, ok := x[path[0]]; ok {
				at(e, path[1:], leaf)
			}
		case []any:
			if path[0] == "*" {
				for _, e := range x {
					at(e, path[1:], leaf)
				}
			}
		}
	}
	for _, p := range drop {
		at(v, strings.Split(p, "."), func(m map[string]any, k string) { delete(m, k) })
	}
	for _, p := range mask {
		at(v, strings.Split(p, "."), func(m map[string]any, k string) { m[k] = "<host>" })
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if zsMasked[k] {
					x[k] = "<masked>"
					continue
				}
				x[k] = walk(e)
			}
		case []any:
			for i, e := range x {
				x[i] = walk(e)
			}
		}
		return v
	}
	var out strings.Builder
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(walk(v))
	return strings.TrimSuffix(out.String(), "\n")
}

// zsPick keeps a listing's entries for the fixture's tiles only (other
// rows come from backfills and change with the scaffold, not with this
// feature) — a list of objects by a key, or an object by its keys.
func zsPick(t *testing.T, b []byte, key string) []byte {
	t.Helper()
	fixture := map[string]bool{"apps/zs": true, "apps/zsnode": true, "apps/zsbroken": true, "notes+ideas": true}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	switch x := v.(type) {
	case []any:
		kept := []any{}
		for _, e := range x {
			if m, ok := e.(map[string]any); ok && fixture[fmt.Sprint(m[key])] {
				kept = append(kept, e)
			}
		}
		v = kept
	case map[string]any:
		for k := range x {
			if !fixture[k] {
				delete(x, k)
			}
		}
	}
	out, _ := json.Marshal(v)
	return out
}

// zsKeys lists the keys every row of a JSON list carries, sorted, deduped.
func zsKeys(t *testing.T, b []byte) string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatalf("not a list of objects: %s", b)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// covers PO-11 PO-14 PO-4 Z5 Z7 Z8 SC-ZERO — on a zero-state workspace the
// listings answer as today, keyed by tile path, one row per tile and no
// new key: /components (every row's keys within today's set, the
// fixture's rows exact), /backends, /runtime, /tile-status, /whoami,
// /cron/jobs, /bus/subscriptions and D112's /sandboxes (a backend's row
// id backend:<CompKey>:g<gen>, no deployment key, the flat leaf). A
// proxied call reaches a real backend with today's identity headers only
// (a forged X-XBin-Deployment stripped) and comes back with the backend's
// headers and nothing of xbind's. Values that differ between two runs of
// today's xbind, or between hosts, are masked. With no node on PATH the
// running-backend rows are skipped, said so. Hand-maintained goldens:
// changing one is a compat change (12-compat.md).
func TestZeroStateListings(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	_, err := exec.LookPath("node")
	haveNode := err == nil
	d := zsBoot(t, zsWorkspace(t))
	owner := "Bearer " + d.owner
	if _, err := d.st.Users.Upsert(users.User{ID: "ana", Role: users.RoleUser,
		Tiles: map[string]string{"apps/zs": users.LevelRead, "notes+ideas": users.LevelWrite}}, "password1"); err != nil {
		t.Fatal(err)
	}
	ana := "Cookie: xbin_session=" + d.st.Auth.NewSession("ana", "127.0.0.1")
	term := "Bearer " + d.st.Auth.MintTerminal("apps/zsnode", "")

	// Start what the fixture can start: the node backend (proxied once, so
	// its request shows what a backend receives), the broken one (a failed
	// build is a row too).
	if haveNode {
		code, body := d.do(t, "GET", "/api/apps/zsnode/ping?x=1", owner)
		if code != 200 {
			t.Fatalf("node backend: %d %s", code, body)
		}
		if got, want := zsNorm(t, body, nil, nil), `{
  "url": "/ping?x=1",
  "xbin": {
    "x-xbin-from": "owner",
    "x-xbin-role": "admin"
  }
}`; got != want {
			t.Errorf("what the node backend received:\n%s\nwant\n%s", got, want)
		}
		// A forged identity never reaches it, and xbind adds no header to
		// the backend's answer.
		req, _ := http.NewRequest("GET", d.url+"/api/apps/zsnode/echo", nil)
		req.Header.Set("Authorization", owner)
		req.Header.Set("X-XBin-Deployment", "dev")
		req.Header.Set("X-XBin-User", "mallory")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		var hs []string
		for k, vs := range resp.Header {
			for _, v := range vs {
				if k == "Date" {
					v = "<date>"
				}
				hs = append(hs, k+": "+v)
			}
		}
		sort.Strings(hs)
		got := strings.Join(hs, "\n") + "\n\n" + zsNorm(t, body, nil, nil)
		if want := zsEchoGolden; got != want {
			t.Errorf("a proxied answer:\n%s\nwant\n%s", got, want)
		}
	} else {
		t.Log("SKIP (partial): no node on PATH — the running-backend rows are not checked")
	}
	if code, body := d.do(t, "GET", "/api/apps/zsbroken/ping", owner); code != http.StatusBadGateway {
		t.Fatalf("broken backend: %d %s", code, body)
	}

	check := func(label, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s:\n%s\nwant\n%s", label, got, want)
		}
	}
	get := func(path, cred string) []byte {
		t.Helper()
		code, body := d.do(t, "GET", path, cred)
		if code != 200 {
			t.Fatalf("GET %s: %d %s", path, code, body)
		}
		return body
	}

	// /components: today's key set on every row; the fixture's rows exact.
	comps := get("/api/xbin/components", owner)
	allowed := map[string]bool{}
	for _, k := range strings.Split("path,scope,runtime,hasIndex,template,state,roles,uses,deps,manifestError,owner,chrome,chromeRequested,sandbox,native,origin", ",") {
		allowed[k] = true
	}
	for _, k := range strings.Split(zsKeys(t, comps), ",") {
		if !allowed[k] {
			t.Errorf("/components rows carry %q, a key today's rows never have", k)
		}
	}
	var rows []map[string]any
	_ = json.Unmarshal(comps, &rows)
	if n := len(d.st.Reg.Components()); len(rows) != n {
		t.Errorf("/components has %d rows for %d components", len(rows), n)
	}
	for _, want := range zsListingGoldens {
		var body []byte
		switch {
		case want.backend && !haveNode:
			continue
		case want.pick != "":
			body = zsPick(t, get(want.path, want.cred(owner, ana, term)), want.pick)
		default:
			body = get(want.path, want.cred(owner, ana, term))
		}
		check("GET "+want.path+" as "+want.as, zsNorm(t, body, want.drop, want.mask), want.want)
	}

	// /sandboxes: the backend's row, checked field by field where the host
	// decides (the leaf exists only with a delegated cgroup).
	if haveNode {
		var sb struct {
			Sandboxes []map[string]any `json:"sandboxes"`
		}
		_ = json.Unmarshal(get("/api/xbin/sandboxes?tile=apps/zsnode", owner), &sb)
		if len(sb.Sandboxes) != 1 {
			t.Fatalf("/sandboxes?tile=apps/zsnode: %v", sb.Sandboxes)
		}
		row := sb.Sandboxes[0]
		key := util.CompKey("apps/zsnode")
		if row["id"] != "backend:"+key+":g1" || row["kind"] != "backend" || row["tile"] != "apps/zsnode" {
			t.Errorf("the backend's registry row: %v", row)
		}
		if l, ok := row["leaf"]; ok && l != key {
			t.Errorf("leaf %v, want the flat %s", l, key)
		}
		if s, ok := row["stats"].(map[string]any); ok && s["scope"] != "tile" {
			t.Errorf("stats.scope %v, want tile", s["scope"])
		}
		if _, ok := row["deployment"]; ok {
			t.Error("a zero-state backend row carries a deployment key")
		}
	}
}

// zsListing is one listing golden: the request, who makes it, and today's
// normalized answer (pick: keep the fixture's entries only, by that key).
type zsListing struct {
	path, as, pick string
	backend        bool     // needs the running node backend
	drop, mask     []string // host-decided values (zsNorm)
	want           string
}

func (l zsListing) cred(owner, ana, term string) string {
	switch l.as {
	case "ana":
		return ana
	case "apps/zsnode's terminal":
		return term
	}
	return owner
}

// zsEchoGolden is a proxied answer: the backend's own headers, nothing of
// xbind's; the backend saw the owner, not the forged identity.
const zsEchoGolden = `Content-Length: 68
Content-Type: application/json
Date: <date>

{
  "url": "/echo",
  "xbin": {
    "x-xbin-from": "owner",
    "x-xbin-role": "admin"
  }
}`

var zsListingGoldens = []zsListing{
	{path: "/api/xbin/components", as: "the owner", pick: "path",
		want: `[
  {
    "hasIndex": true,
    "path": "apps/zs"
  },
  {
    "hasIndex": false,
    "path": "apps/zsbroken",
    "runtime": "node"
  },
  {
    "hasIndex": true,
    "path": "apps/zsnode",
    "runtime": "node"
  },
  {
    "hasIndex": true,
    "path": "notes+ideas"
  }
]`},
	{path: "/api/xbin/components", as: "ana", pick: "path",
		want: `[
  {
    "hasIndex": true,
    "path": "apps/zs"
  },
  {
    "hasIndex": true,
    "path": "notes+ideas"
  }
]`},
	{path: "/api/xbin/backends", as: "the owner", pick: "-",
		want: `{
  "apps/zsbroken": {
    "error": "build failed:\nentry backend/server.js not found (set \"entry\" in xbin.json)",
    "gen": 0,
    "state": "failed"
  },
  "apps/zsnode": {
    "gen": 1,
    "state": "healthy"
  }
}`},
	{path: "/api/xbin/runtime", as: "the owner", drop: zsBackendHostKeys("backends.*."),
		mask: []string{"stats.cgroup", "host"}, // host: the process and the machine, no tile's row
		want: `{
  "backends": [
    {
      "activeConns": 0,
      "error": "build failed:\nentry backend/server.js not found (set \"entry\" in xbin.json)",
      "gen": 0,
      "isolated": false,
      "lastReqSec": "<masked>",
      "path": "apps/zsbroken",
      "restarts": 0,
      "runtime": "node",
      "sandbox": "host",
      "state": "failed"
    },
    {
      "activeConns": 0,
      "cpuSec": "<masked>",
      "fds": "<masked>",
      "gen": 1,
      "isolated": false,
      "lastReqSec": "<masked>",
      "namespaces": "<masked>",
      "path": "apps/zsnode",
      "pid": "<masked>",
      "restarts": 0,
      "rssKb": "<masked>",
      "runtime": "node",
      "sandbox": "host",
      "state": "healthy",
      "threads": "<masked>"
    }
  ],
  "host": "<host>",
  "resources": null,
  "stats": {
    "cgroup": "<host>",
    "intervalSec": 2,
    "tiles": [
      {
        "cur": "<masked>",
        "path": "apps/zsnode",
        "series": "<masked>"
      }
    ]
  }
}`},
	{path: "/api/xbin/tile-status?component=apps/zsnode", as: "the owner", backend: true, drop: zsBackendHostKeys("backend."),
		want: `{
  "alerts": null,
  "backend": {
    "activeConns": 0,
    "cpuSec": "<masked>",
    "fds": "<masked>",
    "gen": 1,
    "isolated": false,
    "lastReqSec": "<masked>",
    "namespaces": "<masked>",
    "path": "apps/zsnode",
    "pid": "<masked>",
    "restarts": 0,
    "rssKb": "<masked>",
    "runtime": "node",
    "sandbox": "host",
    "state": "healthy",
    "threads": "<masked>"
  },
  "component": "apps/zsnode",
  "disk": {
    "blocked": false,
    "quotaBytes": 53687091200,
    "usageBytes": 0
  },
  "net": {}
}`},
	{path: "/api/xbin/tile-status?component=apps/zsbroken", as: "the owner",
		want: `{
  "alerts": null,
  "backend": {
    "activeConns": 0,
    "error": "build failed:\nentry backend/server.js not found (set \"entry\" in xbin.json)",
    "gen": 0,
    "isolated": false,
    "lastReqSec": "<masked>",
    "path": "apps/zsbroken",
    "restarts": 0,
    "runtime": "node",
    "sandbox": "host",
    "state": "failed"
  },
  "component": "apps/zsbroken",
  "disk": {
    "blocked": false,
    "quotaBytes": 53687091200,
    "usageBytes": 0
  },
  "net": {}
}`},
	{path: "/api/xbin/tile-status?component=notes+ideas", as: "the owner",
		want: `{
  "alerts": null,
  "backend": null,
  "component": "notes ideas",
  "disk": {
    "blocked": false,
    "quotaBytes": 53687091200,
    "usageBytes": 0
  },
  "net": {}
}`},
	{path: "/api/xbin/tile-status?component=notes%2Bideas", as: "the owner",
		want: `{
  "alerts": null,
  "backend": null,
  "component": "notes+ideas",
  "disk": {
    "blocked": false,
    "quotaBytes": 53687091200,
    "usageBytes": 0
  },
  "net": {}
}`},
	{path: "/api/xbin/tile-status", as: "apps/zsnode's terminal", backend: true, drop: zsBackendHostKeys("backend."),
		want: `{
  "alerts": null,
  "backend": {
    "activeConns": 0,
    "cpuSec": "<masked>",
    "fds": "<masked>",
    "gen": 1,
    "isolated": false,
    "lastReqSec": "<masked>",
    "namespaces": "<masked>",
    "path": "apps/zsnode",
    "pid": "<masked>",
    "restarts": 0,
    "rssKb": "<masked>",
    "runtime": "node",
    "sandbox": "host",
    "state": "healthy",
    "threads": "<masked>"
  },
  "component": "apps/zsnode",
  "disk": {
    "blocked": false,
    "quotaBytes": 53687091200,
    "usageBytes": 0
  },
  "net": {}
}`},
	{path: "/api/xbin/whoami", as: "the owner",
		want: `{
  "admin": true,
  "id": "root",
  "kind": "root",
  "name": "root (token)",
  "native": {
    "runtime": 1
  },
  "personalTiles": true,
  "role": "admin",
  "terminal": true,
  "tileCreation": "any"
}`},
	{path: "/api/xbin/whoami", as: "ana",
		want: `{
  "admin": false,
  "canCreate": null,
  "id": "ana",
  "kind": "user",
  "name": "",
  "native": {
    "runtime": 1
  },
  "personal": {
    "allow": null,
    "netRules": null,
    "netSets": null,
    "sets": null
  },
  "personalTiles": true,
  "role": "user",
  "termApi": false,
  "termNet": false,
  "terminal": false,
  "tileCreation": "any",
  "tiles": {
    "apps/zs": "read",
    "notes+ideas": "write"
  }
}`},
	{path: "/api/xbin/whoami", as: "apps/zsnode's terminal",
		want: `{
  "admin": false,
  "id": "apps/zsnode",
  "kind": "element",
  "native": {
    "runtime": 1
  },
  "personalTiles": true,
  "terminal": false,
  "tileCreation": "any"
}`},
	{path: "/api/xbin/cron/jobs", as: "the owner",
		want: `{
  "jobs": []
}`},
	{path: "/api/xbin/bus/subscriptions", as: "the owner",
		want: `{
  "subscriptions": []
}`},
	{path: "/api/xbin/sandboxes?tile=apps/zsnode", as: "the owner", backend: true,
		drop: []string{"sandboxes.*.leaf", "sandboxes.*.stats"}, mask: []string{"cgroup"},
		want: `{
  "cgroup": "<host>",
  "disks": [],
  "failureCounts": {},
  "failures": [],
  "health": "<masked>",
  "intervalSec": 2,
  "sandboxes": [
    {
      "gen": 1,
      "id": "backend:apps~zsnode-a398c13a:g1",
      "kind": "backend",
      "mode": "host",
      "pid": "<masked>",
      "started": "<masked>",
      "tile": "apps/zsnode",
      "uptimeSec": "<masked>"
    }
  ]
}`},
	{path: "/api/xbin/sandboxes?tile=apps/zsbroken", as: "the owner", mask: []string{"cgroup"},
		want: `{
  "cgroup": "<host>",
  "disks": [],
  "failureCounts": {},
  "failures": [],
  "health": "<masked>",
  "intervalSec": 2,
  "sandboxes": []
}`},
}

// zsBackendHostKeys are a backend row's keys that exist only where the host
// has them — cgroup accounting, the egress relay — at prefix.
func zsBackendHostKeys(prefix string) []string {
	return []string{prefix + "cgroup", prefix + "egress", prefix + "activity"}
}
