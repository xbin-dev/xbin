//go:build linux && integration

// The preview service end to end (plans/wsagent/s3-preview.md): a real,
// isolated xbind with auth on; a non-admin person asks for a picture of a
// tile and of a screen; the page's own write is refused. Once per frame
// mode: legacy (credentialless frames on the workspace origin) and origins
// (a tile origin per tile under xbin.localhost).
//
//	go test -tags=integration -run TestPreview ./test/preview/
//
// Needs user namespaces and the base rootfs with Chromium; skips otherwise.
// SPIKE: fixed ports from the range this work was given.
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
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestMain(m *testing.M) { xbindtest.Main(m) }

const tilePage = `<!doctype html><meta charset="utf-8"><title>%[1]s</title>
<h1>Preview probe %[1]s</h1>
<p id="who">who: …</p><p id="write">write: …</p><p id="out">outside: …</p><button>Save</button>
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
</script>`

// credential: a frame token, a tile-origin exchange parameter, an asset
// token or a path ticket with a value (the service leaves "…" in its place).
var credential = regexp.MustCompile(`(?:[?&]|\\u0026)(?:frame|xbin_ticket|xbin_begin|xbin_state)=[A-Za-z0-9]|/(?:c|api)/~[A-Za-z0-9]`)

type result struct {
	OK             bool
	URL            string
	Title          string
	Snapshot       string
	PNG            string `json:"png"`
	Width, Height  int
	Frames         []struct{ URL, Snapshot string }
	Console        []map[string]any
	RequestsFailed []map[string]any `json:"requests_failed"`
	Timing         map[string]int
	RSSKB          int `json:"rss_kb"`
	PSSKB          int `json:"pss_kb"`
	Procs          int
	WallMs         int `json:"wall_ms"`
}

func (r result) text() string {
	s := r.Snapshot
	for _, f := range r.Frames {
		s += "\n--- frame " + f.URL + "\n" + f.Snapshot
	}
	return s
}

func run(t *testing.T, name string, port int, args ...string) {
	a := xbindtest.Require(t)
	if _, err := os.Stat(filepath.Join(a.Rootfs, "usr/local/ms-playwright")); err != nil {
		t.Skip("the rootfs has no Chromium")
	}
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Addr: fmt.Sprintf("127.0.0.1:%d", port),
		Env: []string{"XBIN_PREVIEW_SPIKE=1"}, Args: args})
	host := 19050 // a port nothing in the sandbox may reach: nothing listens there either way
	for _, tile := range []string{"apps/pv", "apps/pv2"} {
		d.WriteTile(t, tile, map[string]string{"xbin.json": "{}", "index.html": fmt.Sprintf(tilePage, strings.ToUpper(filepath.Base(tile)), host)})
		d.WaitComponent(t, tile)
	}
	d.AddUser(t, "ann", "ann-password-1", "user", map[string]string{"apps/pv": "write", "apps/pv2": "read"})
	annToken := d.Login(t, "ann", "ann-password-1")
	ann := xbindtest.H("Authorization", "Bearer "+annToken)
	layout := map[string]any{"active": "home", "screens": []map[string]any{
		{"id": "home", "name": "Home", "tiles": []map[string]any{{"path": "apps/pv", "x": 0, "y": 0, "w": 576, "h": 384}}},
		{"id": "second", "name": "Second", "tiles": []map[string]any{{"path": "apps/pv2", "x": 0, "y": 0, "w": 480, "h": 384}, {"path": "apps/pv", "x": 480, "y": 0, "w": 480, "h": 384}}},
	}}
	d.Must(t, http.MethodPut, "/api/xbin/prefs/layout", layout, 200, ann)
	before := d.Must(t, http.MethodGet, "/api/xbin/prefs/layout", nil, 200, ann).String()

	// control: the write the page attempts is one the tile's frame may make
	// in the person's own session
	if r := d.Call(t, http.MethodGet, "/api/xbin/frame-token?component=apps/pv", nil, ann); r.Status == 200 {
		var tk struct{ Token string }
		r.Decode(t, &tk)
		d.Must(t, http.MethodPut, "/api/xbin/prefs/control", map[string]string{"from": "the test"}, 200, xbindtest.FrameHeader(tk.Token))
	} else {
		t.Fatalf("frame token as ann: %d %s", r.Status, r)
	}

	render := func(what string, body map[string]any) result {
		t.Helper()
		r := d.Call(t, http.MethodPost, "/api/xbin/preview", body, ann)
		if r.Status != 200 {
			t.Fatalf("%s: %d %s", what, r.Status, r)
		}
		var res result
		r.Decode(t, &res)
		png, err := base64.StdEncoding.DecodeString(res.PNG)
		if err != nil || len(png) < 1000 {
			t.Fatalf("%s: no picture (%d bytes)", what, len(png))
		}
		if dir := os.Getenv("XBIN_PREVIEW_OUT"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, name+"-"+what+".png"), png, 0o644)
			_ = os.WriteFile(filepath.Join(dir, name+"-"+what+".txt"), []byte(res.URL+"\n"+res.text()), 0o644)
		}
		fails, _ := json.Marshal(res.RequestsFailed)
		t.Logf("%s/%s: %dx%d png %d bytes, wall %d ms, timing %v, rss %d kB pss %d kB in %d processes, url %s, %d frames, %d console, failed %s",
			name, what, res.Width, res.Height, len(png), res.WallMs, res.Timing, res.RSSKB, res.PSSKB, res.Procs, res.URL, len(res.Frames), len(res.Console), fails)
		return res
	}
	want := func(what string, res result, subs ...string) {
		t.Helper()
		for _, s := range subs {
			if !strings.Contains(res.text(), s) {
				t.Errorf("%s: the snapshot lacks %q:\n%s", what, s, res.text())
			}
		}
	}

	// one tile, desktop and phone
	res := render("tile", map[string]any{"tile": "apps/pv"})
	want("tile", res, "Preview probe PV", "readOnly=true preview=true viewedBy=nobody", "write: 403", "read-only", "out: blocked")
	res = render("tile-phone", map[string]any{"tile": "apps/pv", "viewport": map[string]int{"width": 390, "height": 844}})
	want("tile-phone", res, "Preview probe PV", "write: 403")
	if res.Width != 390 {
		t.Errorf("phone viewport: %d wide", res.Width)
	}
	// a screen that is not the active one, with both its tiles
	res = render("screen", map[string]any{"screen": "second"})
	want("screen", res, `heading "Preview probe PV2"`, `heading "Preview probe PV"`, "write: 403", "tab \"Second close Second\" [selected]")
	if raw, _ := json.Marshal(res); credential.Match(raw) {
		t.Errorf("screen: a credential is in the answer: %s", credential.Find(raw))
	}
	if strings.Contains(res.text(), "viewing") && strings.Contains(res.text(), "exit") {
		t.Errorf("screen: the shell shows a view-as banner:\n%s", res.Snapshot)
	}

	// bx preview <tile>, as the person
	shot := filepath.Join(t.TempDir(), "bx.png")
	cmd := exec.Command(filepath.Join(a.Bin, "bx"), "preview", "apps/pv", "--size", "390x844", "--text", "--out", shot)
	cmd.Env = append(os.Environ(), "XBIN_URL="+d.URL, "XBIN_TOKEN="+annToken)
	out, err := cmd.CombinedOutput()
	if b, _ := os.ReadFile(shot); err != nil || !strings.Contains(string(out), "Preview probe PV") || !strings.HasPrefix(string(b), "\x89PNG") {
		t.Errorf("bx preview: %v\n%s", err, out)
	}

	// nothing was written: not the page's pref, not the shell's layout
	// (opening a non-active screen saves in a real session)
	if r := d.Call(t, http.MethodGet, "/api/xbin/prefs/probe", nil, xbindtest.FrameHeader(frameToken(t, d, ann))); r.Status == 200 && strings.Contains(r.String(), "the page") {
		t.Errorf("the page's write landed: %s", r)
	}
	if after := d.Must(t, http.MethodGet, "/api/xbin/prefs/layout", nil, 200, ann).String(); after != before {
		t.Errorf("the render changed the person's layout:\nbefore %s\nafter  %s", before, after)
	}
	// no sign-in is left behind, and none was listed
	if s := d.Must(t, http.MethodGet, "/api/xbin/sessions", nil, 200).String(); strings.Contains(s, "~preview") {
		t.Errorf("a preview session is listed: %s", s)
	}
	// who may ask: a person, for themselves
	if r := d.Call(t, http.MethodPost, "/api/xbin/preview", map[string]any{"tile": "apps/pv"}); r.Status != 403 {
		t.Errorf("the root token got a preview: %d %s", r.Status, r)
	}
}

func frameToken(t *testing.T, d *xbindtest.Daemon, as xbindtest.Header) string {
	t.Helper()
	var tk struct{ Token string }
	d.Must(t, http.MethodGet, "/api/xbin/frame-token?component=apps/pv", nil, 200, as).Decode(t, &tk)
	return tk.Token
}

func TestPreviewLegacyFrames(t *testing.T) { run(t, "legacy", 19045) }

func TestPreviewAssetTokens(t *testing.T) { run(t, "tokens", 19047, "--tile-assets", "tokens") }

func TestPreviewTileOrigins(t *testing.T) {
	run(t, "origins", 19046, "--tile-assets", "origins", "--tiles-domain", "xbin.localhost", "--external-url", "http://xbin.localhost:19046")
}
