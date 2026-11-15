//go:build linux && integration

// Previews end to end (D234, docs/protocol/preview.md): a real, isolated
// xbind with auth on; a non-admin person asks for a picture of a tile and of
// a screen.
//
//	go test -tags=integration ./test/preview/
//
// Needs user namespaces and the base rootfs with Chromium; skips otherwise.
// The memory cap's test needs a delegated cgroup and lives in test/limits.
// Each daemon listens on a port of its own; the workspace's address is the
// one its --external-url names, which only the render's sandbox ever opens.
package preview

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/portlease"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestMain(m *testing.M) { xbindtest.Main(m) }

// tilePage is a static tile: it says who it is, tries a write, tries to
// leave the sandbox, tries to read another tile, and loads a script of its
// own (app.js: the strict modes' asset credentials, legacy's credential-less
// subresource rule).
const tilePage = `<!doctype html><meta charset="utf-8"><title>%[1]s</title>%[3]s
<h1>Preview probe %[1]s</h1>
<p id="who">who: …</p><p id="write">write: …</p><p id="out">out: …</p><p id="other">other: …</p><p id="script">script: not loaded</p><p id="late">late: …</p><button>Save</button>
<script type="module" src="app.js"></script>
<script type="module">
const set = (id, v) => { document.getElementById(id).textContent = id + ': ' + v; };
try {
  const w = await (await xbin.fetch('/api/xbin/whoami')).json();
  set('who', (w.id ?? '?') + ' readOnly=' + !!w.readOnly + ' preview=' + !!w.preview + ' viewedBy=' + (w.impersonatedBy ?? 'nobody'));
} catch (e) { set('who', 'failed ' + e); }
try {
  const r = await xbin.fetch('/api/xbin/prefs/probe', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ from: 'the page' }) });
  set('write', r.status + ' ' + (await r.text()).slice(0, 80));
} catch (e) { set('write', 'failed ' + e); }
try { await fetch('http://127.0.0.1:%[2]d/', { mode: 'no-cors' }); set('out', 'reached'); } catch { set('out', 'blocked'); }
try { const r = await xbin.fetch('/c/%[4]s/app.js'); await r.text(); set('other', r.status); } catch (e) { set('other', 'failed'); }
%[5]s
</script>`

// lateDraw: a tile that draws late and says when (xbin.ready, declared).
const (
	manualMeta = `<meta name="xbin-ready" content="manual">`
	lateDraw   = `setTimeout(() => { set('late', 'drawn'); xbin.ready(); }, 1500);`
)

// backendMain is a Go backend that counts visits unless the call is a
// preview's (the SDK's Caller.Preview), counts raw visits whatever it is (a
// tile that does not honour the header), and tells what role a socket was
// opened at.
const backendMain = `package main

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	var visits, raw atomic.Int64
	var wsRole, lastPreview atomic.Value
	wsRole.Store("")
	lastPreview.Store("")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		xbin.WriteJSON(w, 200, map[string]any{"visits": visits.Load(), "raw": raw.Load(), "wsRole": wsRole.Load(),
			"lastPreview": lastPreview.Load(), "partition": c.Partition, "user": c.User, "role": c.Role, "viewedBy": c.ViewedBy})
	})
	mux.HandleFunc("GET /visit", func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		lastPreview.Store(r.Header.Get("X-XBin-Preview"))
		if !c.Preview && c.ViewedBy == "" { // "seen" is a person's own act
			visits.Add(1)
		}
		xbin.WriteJSON(w, 200, map[string]any{"preview": c.Preview, "role": c.Role, "level": c.UserLevel, "canWrite": c.UserCanWrite()})
	})
	mux.HandleFunc("GET /visit-raw", func(w http.ResponseWriter, r *http.Request) {
		xbin.WriteJSON(w, 200, map[string]any{"raw": raw.Add(1)})
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		role := xbin.Caller(r).Role
		wsRole.Store(role)
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
		buf.Write([]byte{0x81, byte(len(role))})
		buf.WriteString(role)
		buf.Flush()
		time.Sleep(300 * time.Millisecond)
	})
	xbin.Serve(mux)
}
`

const backendPage = `<!doctype html><meta charset="utf-8"><title>be</title>
<h1>Backend probe</h1><p id="visit">visit: …</p><p id="raw">raw: …</p><p id="ws">ws: …</p><p id="state">state: …</p>
<script type="module">
const set = (id, v) => { document.getElementById(id).textContent = id + ': ' + v; };
const api = (p) => '/api/' + xbin.self + p;
try { const v = await (await xbin.fetch(api('/visit'))).json(); set('visit', 'preview=' + v.preview + ' role=' + v.role + ' level=' + v.level + ' canWrite=' + v.canWrite); } catch (e) { set('visit', 'failed ' + e); }
try { const r = await xbin.fetch(api('/visit-raw')); set('raw', r.status); } catch (e) { set('raw', 'failed ' + e); }
try {
  const role = await new Promise((res, rej) => { const s = xbin.ws(api('/ws')); s.onmessage = (m) => res(m.data); s.onerror = () => rej(new Error('socket refused')); setTimeout(() => rej(new Error('no message')), 4000); });
  set('ws', 'role=' + role);
} catch (e) { set('ws', 'failed ' + e.message); }
try { const s = await (await xbin.fetch(api('/state'))).json(); set('state', 'partition=' + (s.partition || 'none') + ' user=' + s.user); } catch (e) { set('state', 'failed ' + e); }
</script>`

// credential: a frame token, a tile-origin exchange parameter, an asset
// token or a path ticket with a value (the service leaves "…" in its place).
var credential = regexp.MustCompile(`(?:[?&]|\\u0026)(?:frame|xbin_ticket|xbin_begin|xbin_state)=[A-Za-z0-9]|/(?:c|api)/~[A-Za-z0-9]`)

type result struct {
	OK             bool
	Notice         string
	URL            string
	Title          string
	Snapshot       string
	PNG            string `json:"png"`
	Width, Height  int
	Frames         []struct{ URL, Snapshot string }
	Console        []map[string]any
	RequestsFailed []map[string]any
	Ready          string
	LeftOut        []string
	Note           string
	Browser        string
	Sandbox        string
	Timing         map[string]int
	MemoryKB       int
	raw            string
}

func (r result) text() string {
	s := r.Snapshot
	for _, f := range r.Frames {
		s += "\n--- frame " + f.URL + "\n" + f.Snapshot
	}
	return s
}

type env struct {
	t     *testing.T
	name  string
	d     *xbindtest.Daemon
	a     *xbindtest.Assets
	ann   xbindtest.Header
	token string
}

// freePort is a loopback port that is the test's until it ends (a lease
// among the harnesses: test/portlease).
func freePort(t *testing.T) int {
	t.Helper()
	return portlease.Port(t)
}

// start boots a daemon with the two static tiles and ann, a non-admin who
// writes apps/pv and reads apps/pv2, with a two-screen layout. args may
// hold one %d: the daemon's port.
func start(t *testing.T, name string, envv []string, args ...string) *env {
	t.Helper()
	a := xbindtest.Require(t)
	if _, err := os.Stat(filepath.Join(a.Rootfs, "usr/local/ms-playwright")); err != nil {
		t.Skip("the rootfs has no Chromium")
	}
	port := freePort(t)
	for i := range args {
		if strings.Contains(args[i], "%d") {
			args[i] = fmt.Sprintf(args[i], port)
		}
	}
	if !slices.ContainsFunc(envv, func(e string) bool { return strings.HasPrefix(e, "XBIN_PREVIEWS=") }) {
		envv = append(envv, "XBIN_PREVIEWS=uncapped") // no delegated cgroup here (test/limits has the capped run)
	}
	if f := os.Getenv("XBIN_PREVIEW_TRACE"); f != "" {
		envv = append(envv, "XBIN_PREVIEW_TRACE="+f)
	}
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Addr: fmt.Sprintf("127.0.0.1:%d", port), Env: envv, Args: args})
	e := &env{t: t, name: name, d: d, a: a}
	closed := freePort(t) // nothing listens there; inside the sandbox nothing could reach it anyway
	for _, tl := range []struct{ tile, other, head, extra string }{{"apps/pv", "apps/pv2", "", ""}, {"apps/pv2", "apps/pv", manualMeta, lateDraw}} {
		up := strings.ToUpper(filepath.Base(tl.tile))
		d.WriteTile(t, tl.tile, map[string]string{"xbin.json": "{}",
			"index.html": fmt.Sprintf(tilePage, up, closed, tl.head, tl.other, tl.extra),
			"app.js":     "document.getElementById('script').textContent = 'script: loaded';\n"})
		d.WaitComponent(t, tl.tile)
	}
	d.AddUser(t, "ann", "ann-password-1", "user", map[string]string{"apps/pv": "write", "apps/pv2": "read"})
	e.token = d.Login(t, "ann", "ann-password-1")
	e.ann = xbindtest.H("Authorization", "Bearer "+e.token)
	layout := map[string]any{"active": "home", "screens": []map[string]any{
		{"id": "home", "name": "Home", "tiles": []map[string]any{{"path": "apps/pv", "x": 0, "y": 0, "w": 576, "h": 384}}},
		{"id": "second", "name": "Second Screen", "tiles": []map[string]any{{"path": "apps/pv2", "x": 0, "y": 0, "w": 480, "h": 384}, {"path": "apps/pv", "x": 480, "y": 0, "w": 480, "h": 384}}},
	}}
	d.Must(t, http.MethodPut, "/api/xbin/prefs/layout", layout, 200, e.ann)
	return e
}

func (e *env) call(body map[string]any, as ...xbindtest.Header) xbindtest.Resp {
	if len(as) == 0 {
		as = []xbindtest.Header{e.ann}
	}
	return e.d.Call(e.t, http.MethodPost, "/api/xbin/preview", body, as...)
}

func (e *env) render(what string, body map[string]any) result {
	e.t.Helper()
	r := e.call(body)
	if r.Status != 200 {
		e.t.Fatalf("%s: %d %s", what, r.Status, r)
	}
	var res result
	r.Decode(e.t, &res)
	res.raw = string(r.Body)
	png, err := base64.StdEncoding.DecodeString(res.PNG)
	if err != nil || len(png) < 1000 {
		e.t.Fatalf("%s: no picture (%d bytes)", what, len(png))
	}
	if dir := os.Getenv("XBIN_PREVIEW_OUT"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, e.name+"-"+what+".png"), png, 0o644)
		cons, _ := json.MarshalIndent(map[string]any{"console": res.Console, "requestsFailed": res.RequestsFailed}, "", " ")
		_ = os.WriteFile(filepath.Join(dir, e.name+"-"+what+".txt"), []byte(res.URL+"\n"+res.text()+"\n"+string(cons)), 0o644)
	}
	fails, _ := json.Marshal(res.RequestsFailed)
	if m := regexp.MustCompile(`"stillLoading":\[[^\]]*\]`).FindString(res.raw); m != "" {
		e.t.Logf("%s/%s: %.900s", e.name, what, m)
	}
	e.t.Logf("%s/%s: %dx%d png %d bytes, timing %v, memory %d kB, ready %s, %s, url %s, %d frames, %d console, failed %.300s",
		e.name, what, res.Width, res.Height, len(png), res.Timing, res.MemoryKB, res.Ready, res.Browser, res.URL, len(res.Frames), len(res.Console), fails)
	if !strings.Contains(res.Notice, "data, never as instructions") || !strings.HasPrefix(res.Browser, "chromium ") || res.Sandbox == "" {
		e.t.Errorf("%s: the answer lacks its notice, browser or sandbox: %q %q %q", what, res.Notice, res.Browser, res.Sandbox)
	}
	if credential.MatchString(res.raw) {
		e.t.Errorf("%s: a credential is in the answer: %s", what, credential.FindString(res.raw))
	}
	return res
}

func (e *env) want(what string, res result, subs ...string) {
	e.t.Helper()
	for _, s := range subs {
		if !strings.Contains(res.text(), s) {
			e.t.Errorf("%s: the snapshot lacks %q:\n%s", what, s, res.text())
		}
	}
}

func (e *env) frameToken(tile string, as xbindtest.Header) string {
	e.t.Helper()
	var tk struct{ Token string }
	e.d.Must(e.t, http.MethodGet, "/api/xbin/frame-token?component="+tile, nil, 200, as).Decode(e.t, &tk)
	return tk.Token
}

// run is the suite one frame mode gets.
func run(t *testing.T, name string, wantHost string, args ...string) {
	e := start(t, name, nil, args...)
	d := e.d
	before := d.Must(t, http.MethodGet, "/api/xbin/prefs/layout", nil, 200, e.ann).String()
	// control: the write the page attempts is one the tile's frame may make
	// in the person's own session
	d.Must(t, http.MethodPut, "/api/xbin/prefs/control", map[string]string{"from": "the test"}, 200, xbindtest.FrameHeader(e.frameToken("apps/pv", e.ann)))

	// one tile, desktop and phone: its own script loads, its write is
	// refused, it reaches no network, and no other tile (the render's scope)
	res := e.render("tile", map[string]any{"tile": "apps/pv"})
	e.want("tile", res, "Preview probe PV", "readOnly=true preview=false viewedBy=nobody", "write: 403", "read-only", "out: blocked", "script: loaded")
	if !strings.Contains(res.text(), "other: 403") && !strings.Contains(res.text(), "other: failed") { // refused: readably, or at the preflight
		t.Errorf("tile: the page read another tile's file (the render's scope):\n%s", res.text())
	}
	if wantHost != "" && !strings.Contains(res.URL, wantHost) {
		t.Errorf("tile: rendered at %s, want a URL on %s", res.URL, wantHost)
	}
	res = e.render("tile-phone", map[string]any{"tile": "apps/pv", "viewport": map[string]int{"width": 390, "height": 844}})
	e.want("tile-phone", res, "Preview probe PV", "write: 403")
	if res.Width != 390 || res.Height != 844 {
		t.Errorf("phone viewport: %dx%d", res.Width, res.Height)
	}
	// a tile that says when it has drawn is waited for, and not a moment longer
	res = e.render("tile-ready", map[string]any{"tile": "apps/pv2"})
	e.want("tile-ready", res, "Preview probe PV2", "late: drawn")
	if res.Ready != "signal" {
		t.Errorf("tile-ready: ready %q, want signal", res.Ready)
	}
	// a screen that is not the active one, by its name, with both its tiles
	res = e.render("screen", map[string]any{"screen": "second screen"})
	e.want("screen", res, `heading "Preview probe PV2"`, `heading "Preview probe PV"`, "write: 403", "script: loaded", "late: drawn", `[selected]`)
	if !strings.Contains(res.URL, "screen=second") {
		t.Errorf("screen: rendered %s", res.URL)
	}
	// The person's own plane is an allow-list of the shell's reads
	// (internal/server shellReads). A read the shell makes that is neither
	// on it nor one of the admin-only ones it leaves out on purpose shows
	// up here: add it to the list, or to this set, deliberately.
	leftOutOnPurpose := map[string]bool{"/api/xbin/status": true, "/api/xbin/backends": true, "/api/xbin/vault-status": true}
	for _, p := range res.LeftOut {
		if !leftOutOnPurpose[p] {
			t.Errorf("screen: the shell asked for %s, which a preview doesn't serve: list it in shellReads (internal/server/preview.go) if a screen needs it to draw", p)
		}
	}
	if len(res.LeftOut) == 0 || res.Note != "" {
		t.Errorf("screen: leftOut %q, note %q — want the admin-only reads named and no note for a member", res.LeftOut, res.Note)
	}
	if strings.Contains(res.Snapshot, "viewing as") {
		t.Errorf("screen: the shell shows a view-as banner:\n%s", res.Snapshot)
	}
	// the active screen, and a screen that isn't there
	res = e.render("home", map[string]any{})
	e.want("home", res, `heading "Preview probe PV"`)
	if strings.Contains(res.text(), "Preview probe PV2") {
		t.Errorf("home: the other screen's tile is in the picture")
	}
	if r := e.call(map[string]any{"screen": "nope"}); r.Status != 404 || !strings.Contains(r.String(), "Second Screen") {
		t.Errorf("an unknown screen: %d %s", r.Status, r)
	}
	if r := e.call(map[string]any{"tile": "tiles/admin"}); r.Status != 404 {
		t.Errorf("a tile ann can't open: %d %s", r.Status, r)
	}

	// bx preview <tile>, as the person
	shot := filepath.Join(t.TempDir(), "bx.png")
	cmd := exec.Command(filepath.Join(e.a.Bin, "bx"), "preview", "apps/pv", "--viewport", "390x844", "--text", "--out", shot)
	cmd.Env = append(os.Environ(), "XBIN_URL="+d.URL, "XBIN_TOKEN="+e.token)
	out, err := cmd.CombinedOutput()
	if b, _ := os.ReadFile(shot); err != nil || !strings.Contains(string(out), "Preview probe PV") || !strings.HasPrefix(string(b), "\x89PNG") {
		t.Errorf("bx preview: %v\n%s", err, out)
	}
	if fi, err := os.Stat(shot); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("bx preview: the picture's mode is %v, want 0600 (%v)", fi.Mode().Perm(), err)
	}
	// without --out: in the working directory, never a guessable name in /tmp
	cmd = exec.Command(filepath.Join(e.a.Bin, "bx"), "preview", "apps/pv")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "XBIN_URL="+d.URL, "XBIN_TOKEN="+e.token, "TMPDIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("bx preview: %v\n%s", err, out)
	} else if fi, err := os.Stat(filepath.Join(cmd.Dir, "preview-apps-pv.png")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("bx preview without --out: %v %v\n%s", fi, err, out)
	}

	// nothing was written: not the page's pref, not the shell's layout
	// (opening a non-active screen saves in a real session)
	if r := d.Call(t, http.MethodGet, "/api/xbin/prefs/probe", nil, xbindtest.FrameHeader(e.frameToken("apps/pv", e.ann))); r.Status == 200 && strings.Contains(r.String(), "the page") {
		t.Errorf("the page's write landed: %s", r)
	}
	if after := d.Must(t, http.MethodGet, "/api/xbin/prefs/layout", nil, 200, e.ann).String(); after != before {
		t.Errorf("the render changed the person's layout:\nbefore %s\nafter  %s", before, after)
	}
	// no sign-in is left behind, and none was listed
	if s := d.Must(t, http.MethodGet, "/api/xbin/sessions", nil, 200).String(); strings.Contains(s, "~preview") {
		t.Errorf("a preview session is listed: %s", s)
	}
	// who may ask: a person, for themselves — not the root token, not a tile
	if r := e.call(map[string]any{"tile": "apps/pv"}, xbindtest.H("Authorization", "Bearer "+d.Token())); r.Status != 403 {
		t.Errorf("the root token got a preview: %d %s", r.Status, r)
	}
	if r := e.call(map[string]any{"tile": "apps/pv"}, xbindtest.FrameHeader(e.frameToken("apps/pv", e.ann))); r.Status != 403 {
		t.Errorf("a tile's frame got a preview: %d %s", r.Status, r)
	}
	// the status: anyone signed in learns whether; an admin, the build
	var st struct {
		Available bool
		Browser   string
		PerUser   int
		TimeoutMs int
	}
	d.Must(t, http.MethodGet, "/api/xbin/preview", nil, 200, e.ann).Decode(t, &st)
	if !st.Available || st.Browser != "" || st.PerUser != 1 || st.TimeoutMs != 45000 {
		t.Errorf("status as ann: %+v", st)
	}
	d.Must(t, http.MethodGet, "/api/xbin/preview", nil, 200).Decode(t, &st)
	if !strings.HasPrefix(st.Browser, "chromium ") {
		t.Errorf("status as the owner: %+v", st)
	}
}

func TestPreviewLegacyFrames(t *testing.T) { run(t, "legacy", "") }

// An admin's screen render: drawn as a member sees the shell — the
// admin-only reads are left out, and the answer says so.
func TestPreviewAdminScreen(t *testing.T) {
	e := start(t, "admin", nil)
	e.d.AddUser(t, "root2", "root2-password-1", "admin", nil)
	adm := xbindtest.H("Authorization", "Bearer "+e.d.Login(t, "root2", "root2-password-1"))
	r := e.d.Call(t, http.MethodPost, "/api/xbin/preview", map[string]any{}, adm)
	if r.Status != 200 {
		t.Fatalf("an admin's screen: %d %s", r.Status, r)
	}
	var res result
	r.Decode(t, &res)
	if !strings.Contains(res.Note, "workspace admin") {
		t.Errorf("no note about the admin-only parts: %q", res.Note)
	}
	want := map[string]bool{"/api/xbin/status": true, "/api/xbin/backends": true, "/api/xbin/vault-status": true,
		"/api/xbin/grants": true, "/api/xbin/bindings": true, "/api/xbin/access-requests": true, "/ws/events": true}
	for _, p := range res.LeftOut {
		if !want[p] {
			t.Errorf("an admin's screen: the shell asked for %s, which is neither served nor left out on purpose", p)
		}
		delete(want, p)
	}
	if len(want) != 0 {
		t.Errorf("an admin's screen served what it should leave out: %v", want)
	}
	if !strings.Contains(res.text(), `tablist "screens"`) || !strings.Contains(res.text(), "tile menu: apps/pv") { // the shell is drawn, its sidebar too
		fails, _ := json.Marshal(res.RequestsFailed)
		cons, _ := json.Marshal(res.Console)
		t.Errorf("an admin's screen: no shell in the picture:\n%s\n%s\n%s\n%s", res.URL, res.text(), fails, cons)
	}
}

func TestPreviewAssetTokens(t *testing.T) { run(t, "tokens", "", "--tile-assets", "tokens") }

func TestPreviewTileOrigins(t *testing.T) {
	run(t, "origins", ".xbin.localhost", "--tile-assets", "origins", "--tiles-domain", "xbin.localhost", "--external-url", "http://xbin.localhost:%d")
}

// An https workspace on a real name, tile origins on another: the sandbox
// resolves every name to its one socket, where xbind ends TLS under a
// throwaway key. The browser sees the real origins.
func TestPreviewHTTPSOrigins(t *testing.T) {
	run(t, "https", "https://t-", "--tile-assets", "origins", "--tiles-domain", "tiles.xbin-preview.test", "--external-url", "https://ws.xbin-preview.test")
}

// A plain-http workspace on a LAN name (no TLS, no localhost).
func TestPreviewPlainHTTPName(t *testing.T) {
	run(t, "lan", "http://ws.xbin-preview.test:8080/", "--external-url", "http://ws.xbin-preview.test:8080")
}

// A tile with a backend: a GET with an effect that honours X-XBin-Preview
// has none in a preview, one that doesn't still has it (the contract is the
// tile's to keep); a socket is opened at reader, whatever the frame's role.
// The same tile, partitioned: a person's own partition opens read-only, and
// a partition never opened is not made by a preview.
func TestPreviewBackendAndPartition(t *testing.T) {
	e := start(t, "backend", nil)
	d := e.d
	files := map[string]string{
		"go.mod":          "module pvbe\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": backendMain, "index.html": backendPage,
	}
	for tile, manifest := range map[string]string{"apps/be": `{"runtime":"go"}`, "apps/pt": `{"runtime":"go","partition":["user"],"partitionNote":"each person's counters"}`} {
		f := map[string]string{"xbin.json": manifest}
		for k, v := range files {
			f[k] = strings.Replace(v, "module pvbe", "module pv"+filepath.Base(tile), 1)
		}
		d.WriteTile(t, tile, f)
		d.WaitComponent(t, tile)
	}
	d.AddUser(t, "bea", "bea-password-1", "user", map[string]string{"apps/be": "write", "apps/pt": "write"})
	bea := xbindtest.H("Authorization", "Bearer "+d.Login(t, "bea", "bea-password-1"))
	type backendState struct {
		Visits, Raw         int
		WsRole, LastPreview string
		Partition, Role     string
	}
	state := func(tile string) (st backendState) {
		t.Helper()
		var tk struct{ Token string }
		d.Must(t, http.MethodGet, "/api/xbin/frame-token?component="+tile, nil, 200, bea).Decode(t, &tk)
		xbindtest.Eventually(t, 3*time.Minute, tile+"'s backend", func() (bool, string) {
			r := d.Call(t, http.MethodGet, "/api/"+tile+"/state", nil, xbindtest.FrameHeader(tk.Token))
			if r.Status != 200 {
				return false, fmt.Sprintf("%d %s", r.Status, r)
			}
			return json.Unmarshal(r.Body, &st) == nil, r.String()
		})
		return st
	}
	visit := func(tile string) {
		t.Helper()
		var tk struct{ Token string }
		d.Must(t, http.MethodGet, "/api/xbin/frame-token?component="+tile, nil, 200, bea).Decode(t, &tk)
		xbindtest.Eventually(t, 3*time.Minute, tile+"'s backend", func() (bool, string) {
			r := d.Call(t, http.MethodGet, "/api/"+tile+"/visit", nil, xbindtest.FrameHeader(tk.Token))
			return r.Status == 200, fmt.Sprintf("%d %s", r.Status, r)
		})
	}
	body := func(tile string) map[string]any { return map[string]any{"tile": tile} }

	// the unpartitioned tile: bea's own visit counts, the preview's doesn't
	visit("apps/be")
	if st := state("apps/be"); st.Role != "admin" || st.Visits != 1 {
		t.Fatalf("a frame's own role and visit: %+v", st)
	}
	r := d.Call(t, http.MethodPost, "/api/xbin/preview", body("apps/be"), bea)
	if r.Status != 200 {
		t.Fatalf("backend tile: %d %s", r.Status, r)
	}
	var res result
	r.Decode(t, &res)
	// every identity header says read-only: the role, the person's level (bea writes this tile), UserCanWrite
	e.want("backend", res, "visit: preview=true role=reader level=read canWrite=false", "raw: 200", "ws: role=reader", "state: partition=none user=bea")
	st := state("apps/be")
	if st.Visits != 1 || st.Raw != 1 || st.WsRole != "reader" || st.LastPreview != "1" {
		t.Errorf("after the preview: %+v — want 1 visit (bea's own), 1 raw visit (the handler that ignores the header), a reader socket, X-XBin-Preview: 1", st)
	}

	// the partitioned tile: never opened → refused, and nothing was made
	people := func() string { return d.Must(t, http.MethodGet, "/api/xbin/partitions", nil, 200).String() }
	r = d.Call(t, http.MethodPost, "/api/xbin/preview", body("apps/pt"), bea)
	if r.Status != 409 || !strings.Contains(r.String(), "open it once first") {
		t.Fatalf("a partition never opened: %d %s", r.Status, r)
	}
	if p := people(); strings.Contains(p, `"bea"`) {
		t.Errorf("the refused preview made bea a partition: %s", p)
	}
	// bea opens it (her frame's call starts her partition), then previews it
	visit("apps/pt")
	if st := state("apps/pt"); st.Partition != "user:bea" || st.Visits != 1 {
		t.Fatalf("bea's partition: %+v", st)
	}
	r = d.Call(t, http.MethodPost, "/api/xbin/preview", body("apps/pt"), bea)
	if r.Status != 200 {
		t.Fatalf("partitioned tile: %d %s", r.Status, r)
	}
	res = result{}
	r.Decode(t, &res)
	e.want("partitioned", res, "visit: preview=true", "ws: role=reader", "state: partition=user:bea user=bea")
	if st := state("apps/pt"); st.Visits != 1 || st.WsRole != "reader" {
		t.Errorf("bea's partition after the preview: %+v", st)
	}
	// ann has no access to it at all
	if r := e.call(body("apps/pt")); r.Status != 404 {
		t.Errorf("ann previewing a tile she can't open: %d %s", r.Status, r)
	}
}

// Limits: one render at a time per person (429), and a render that never
// settles is cut at the workspace's timeout with what it had.
func TestPreviewLimits(t *testing.T) {
	e := start(t, "limits", []string{"XBIN_PREVIEW_TIMEOUT=12s", "XBIN_PREVIEW_PER_USER=1", "XBIN_PREVIEW_TOTAL=1"})
	e.d.WriteTile(t, "apps/never", map[string]string{"xbin.json": "{}",
		"index.html": `<!doctype html><meta name="xbin-ready" content="manual"><title>never</title><h1>Never ready</h1>`})
	e.d.WaitComponent(t, "apps/never")
	e.d.AddUser(t, "cy", "cy-password-12", "user", map[string]string{"apps/never": "read", "apps/pv": "read"})
	cy := xbindtest.H("Authorization", "Bearer "+e.d.Login(t, "cy", "cy-password-12"))

	var wg sync.WaitGroup
	var slow xbindtest.Resp
	began := time.Now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		slow = e.d.Call(t, http.MethodPost, "/api/xbin/preview", map[string]any{"tile": "apps/never"}, cy)
	}()
	time.Sleep(1500 * time.Millisecond)
	r := e.d.Call(t, http.MethodPost, "/api/xbin/preview", map[string]any{"tile": "apps/pv"}, cy)
	if r.Status != 429 || r.Header.Get("Retry-After") == "" {
		t.Errorf("a second render for the same person: %d %s", r.Status, r)
	}
	// past the workspace's limit: the same answer, word for word — whether
	// someone else is rendering is not ann's to learn
	if r2 := e.call(map[string]any{"tile": "apps/pv"}); r2.Status != 429 || string(r2.Body) != string(r.Body) {
		t.Errorf("a render past the workspace's limit: %d %s (the per-person refusal: %s)", r2.Status, r2, r)
	}
	wg.Wait()
	var res result
	if slow.Status != 200 {
		t.Fatalf("the slow render: %d %s", slow.Status, slow)
	}
	slow.Decode(t, &res)
	if took := time.Since(began); res.Ready != "timeout" || took > 14*time.Second || !strings.Contains(res.Snapshot, "Never ready") {
		t.Errorf("a tile that never says ready: ready %q after %s, snapshot %q", res.Ready, took.Round(time.Millisecond), res.Snapshot)
	}
	// and the limit frees up
	if r := e.call(map[string]any{"tile": "apps/pv"}); r.Status != 200 {
		t.Errorf("a render after the slow one: %d %s", r.Status, r)
	}
}

// Without an enforceable memory cap — xbind's cgroup is not delegated here —
// previews are unavailable unless the admin opted in (XBIN_PREVIEWS=uncapped,
// what every other test here runs with).
func TestPreviewNeedsAMemoryCap(t *testing.T) {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		t.Skip("no cgroup v2 here")
	}
	e := start(t, "needcap", []string{"XBIN_PREVIEWS=on"})
	var st struct {
		Available, MemoryCap bool
		Reason               string
	}
	e.d.Must(t, http.MethodGet, "/api/xbin/preview", nil, 200, e.ann).Decode(t, &st)
	if st.MemoryCap {
		t.Skip("this test runs in a delegated cgroup: the cap is enforceable (test/limits covers it)")
	}
	if st.Available || !strings.Contains(st.Reason, "can't be capped") || !strings.Contains(st.Reason, "XBIN_PREVIEWS=uncapped") {
		t.Errorf("status: %+v", st)
	}
	if r := e.call(map[string]any{"tile": "apps/pv"}); r.Status != 503 || !strings.Contains(r.String(), "can't be capped") {
		t.Errorf("a preview without a memory cap: %d %s", r.Status, r)
	}
}

// XBIN_PREVIEWS=off: the route answers 503 with the reason, and the status
// says so.
func TestPreviewSwitchedOff(t *testing.T) {
	e := start(t, "off", []string{"XBIN_PREVIEWS=off"})
	if r := e.call(map[string]any{"tile": "apps/pv"}); r.Status != 503 || !strings.Contains(r.String(), "switched off") {
		t.Errorf("a preview while off: %d %s", r.Status, r)
	}
	var st struct {
		Available bool
		Reason    string
	}
	e.d.Must(t, http.MethodGet, "/api/xbin/preview", nil, 200, e.ann).Decode(t, &st)
	if st.Available || !strings.Contains(st.Reason, "switched off") {
		t.Errorf("status: %+v", st)
	}
}
