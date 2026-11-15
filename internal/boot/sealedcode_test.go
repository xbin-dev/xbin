package boot

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/sealed"
	"github.com/xbin-dev/xbin/internal/server"
)

// A page of a sealed component that was open across an xbind restart onto
// another release is the old release's; its first call to the component's
// API is turned away with the new code's id and a reload event, so the
// frame ends on the new page and no old page talks to the new backend
// (server/sealed.go). Two builds of the fixture, one workspace.
func TestSealedPageFollowsTheRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace twice and builds two backends")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	release := func(code string) []sealed.Spec {
		return []sealed.Spec{{Name: "counter", Source: sealedCounter(code), Authored: "apps/counter"}}
	}
	meta := regexp.MustCompile(`<meta name="xbin-code" content="([0-9a-f]+)">`)
	page := func(d *sealedDaemon) string {
		t.Helper()
		code, body := d.do("GET", "/c/xbin/counter/", nil)
		m := meta.FindStringSubmatch(body)
		if code != 200 || m == nil {
			t.Fatalf("the document carries no code id: %d %s", code, body)
		}
		return m[1]
	}
	// its page's calls carry its frame token (the root token's own page here)
	pageToken := func(d *sealedDaemon) string {
		t.Helper()
		code, body := d.do("GET", "/api/xbin/frame-token?component=xbin/counter", nil)
		var ft struct{ Token string }
		if code != 200 || json.Unmarshal([]byte(body), &ft) != nil || ft.Token == "" {
			t.Fatalf("its frame token: %d %s", code, body)
		}
		return ft.Token
	}
	var frameTok string
	call := func(d *sealedDaemon, path, code string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", d.base+path, nil)
		req.Header.Set("X-XBin-Frame-Token", frameTok)
		if code != "" {
			req.Header.Set(server.SealedCodeHeader, code)
		}
		resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get(server.SealedReloadHeader)
	}

	// release 1: the page names its code, and its calls pass
	d := startSealed(t, ws, release("code-1"))
	frameTok = pageToken(d)
	id1 := page(d)
	if row := d.sealedRow("xbin/counter"); !strings.HasPrefix(row.Digest, id1) {
		t.Errorf("the page's code id %s is not the component's digest %s", id1, row.Digest)
	}
	if code, body, _ := call(d, "/api/xbin/counter/hello", id1); code != 200 || !strings.HasPrefix(body, "code-1 ") {
		t.Fatalf("release 1, its own page: %d %s", code, body)
	}
	// an ordinary tile's document carries no such meta
	if code, body := d.do("GET", "/c/apps/counter/", nil); code == 200 && meta.MatchString(body) {
		t.Errorf("a tile's document names a code id: %s", body)
	}
	d.stop()

	// release 2 on the same workspace; the old page is still open somewhere
	d = startSealed(t, ws, release("code-2"))
	frameTok = pageToken(d)
	id2 := page(d)
	if id2 == id1 {
		t.Fatal("two releases, one code id")
	}
	evs, cancel := d.st.Hub.Subscribe(func(e events.Event) bool { return e.Type == "reload" })
	defer cancel()
	// the event is for the person whose page was stale, nobody else's frames
	reloaded := func() bool {
		select {
		case e := <-evs:
			to, ok := e.Data.(interface{ VisibleTo(auth.Principal) bool })
			return e.Component == "xbin/counter" && ok && to.VisibleTo(auth.Principal{Owner: true, Via: "cookie"}) &&
				!to.VisibleTo(auth.Principal{UserID: "someone", Via: "session"}) && !to.VisibleTo(auth.Principal{UserID: "someone", Component: "xbin/counter", Via: "frame"})
		case <-time.After(2 * time.Second):
			return false
		}
	}
	// a code named by anything that is not this component's page counts for
	// nothing: no 409, no reload for anyone (any credential with a made-up
	// X-XBin-Code once reloaded every open frame, once a second)
	for what, hdr := range map[string]string{"the root token": "Bearer " + d.token, "no page's token": ""} {
		req, _ := http.NewRequest("GET", d.base+"/api/xbin/counter/hello?xbin-code=ffffffffffffffffffffffff", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		req.Header.Set(server.SealedCodeHeader, "000000000000000000000000")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusConflict || strings.Contains(string(b), "xbin-code") {
			t.Errorf("a made-up code from %s: %d %s", what, resp.StatusCode, b)
		}
	}
	select {
	case e := <-evs:
		t.Errorf("a made-up code from no page reloaded frames: %+v", e)
	case <-time.After(150 * time.Millisecond):
	}

	code, body, reload := call(d, "/api/xbin/counter/hello", id1)
	if code != http.StatusConflict || reload != id2 || strings.Contains(body, "code-2 ") || !strings.Contains(body, "updated with xbin") {
		t.Errorf("the old page's call: %d reload=%q %s (want 409 naming %s, nothing of the backend)", code, reload, body, id2)
	}
	if !reloaded() {
		t.Error("no reload event for the component after the old page's call")
	}
	// the same on a URL (a socket, a tag): refused, and the events are spaced
	if code, _, reload := call(d, "/api/xbin/counter/hello?a=1&xbin-code="+id1, ""); code != http.StatusConflict || reload != id2 {
		t.Errorf("the old page's URL: %d reload=%q", code, reload)
	}
	select {
	case e := <-evs:
		t.Errorf("a second reload event within the second: %+v", e)
	case <-time.After(200 * time.Millisecond):
	}

	// the new page's calls pass, and the parameter is xbind's: the backend
	// never sees it
	if code, body, _ := call(d, "/api/xbin/counter/hello", id2); code != 200 || !strings.HasPrefix(body, "code-2 ") {
		t.Errorf("release 2, its own page: %d %s", code, body)
	}
	if code, body, _ := call(d, "/api/xbin/counter/hello?a=1&xbin-code="+id2, ""); code != 200 || !strings.Contains(body, " query=a=1 ") {
		t.Errorf("the new page's URL: %d %s (want query=a=1)", code, body)
	}
	// a caller that is no page names no code and is not asked for one
	if code, body, _ := call(d, "/api/xbin/counter/hello", ""); code != 200 || !strings.HasPrefix(body, "code-2 ") {
		t.Errorf("a call naming no code: %d %s", code, body)
	}
}

// What a browser is sent of a sealed component is the release's bytes from
// memory, never the copy under .xbin/sealed/; and that copy — what its
// backend runs in — is checked against the release before every start and
// written again when it differs.
func TestSealedFilesAreTheReleasesNotTheDisks(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace and builds a backend")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	d := startSealed(t, ws, []sealed.Spec{{Name: "counter", Source: sealedCounter("code-1"), Authored: "apps/counter"}})
	c, ok := d.st.Sealed.Component("xbin/counter")
	if !ok {
		t.Fatal("no sealed component")
	}
	served := func(what string) {
		t.Helper()
		if code, body := d.do("GET", "/c/xbin/counter/app.js", nil); code != 200 || body != "export const version = 'code-1';\n" {
			t.Errorf("%s: app.js is %d %q", what, code, body)
		}
		if code, body := d.do("GET", "/c/xbin/counter/", nil); code != 200 || !strings.Contains(body, "<body>code-1</body>") || strings.Contains(body, "planted") {
			t.Errorf("%s: its document is %d %s", what, code, body)
		}
		if code, _ := d.do("GET", "/c/xbin/counter/planted.js", nil); code != 404 {
			t.Errorf("%s: a file the release doesn't have answers %d", what, code)
		}
	}
	served("as booted")
	if code, body := d.do("GET", "/api/xbin/counter/hello", nil); code != 200 {
		t.Fatalf("its backend: %d %s", code, body)
	}
	// someone writes into xbind's copy
	for name, body := range map[string]string{"app.js": "export const version = 'planted';\n", "index.html": "<html><body>planted</body></html>", "planted.js": "planted"} {
		p := filepath.Join(c.Dir, name)
		_ = os.Chmod(p, 0o644)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	served("with its files on disk changed")
	// the next start of its backend puts the release's files back
	for _, state := range []string{"disabled", "enabled"} {
		if code, body := d.do("POST", "/api/xbin/lifecycle", map[string]any{"component": "xbin/counter", "state": state}); code != 200 {
			t.Fatalf("lifecycle %s: %d %s", state, code, body)
		}
	}
	if code, body := d.do("GET", "/api/xbin/counter/hello", nil); code != 200 || !strings.HasPrefix(body, "code-1 ") {
		t.Fatalf("its backend after a restart: %d %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(c.Dir, "app.js")); string(b) != "export const version = 'code-1';\n" {
		t.Errorf("after a start its tree on disk still holds %q", b)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "planted.js")); err == nil {
		t.Error("a planted file survived a start")
	}
	served("after the restart")
}
