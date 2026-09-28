//go:build integration

package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A static tile's deployments on an auth-on daemon (15-test-plan §5.1's
// auth-on flavour: --insecure-vault, the root token, users through the API).
// A static tile needs no isolation (P18), so this runs wherever the suite
// does.

// covers P6 P7 P14 P17 P20 — a static dev deployment, live reload attached
// to it: /c/<tile>+dev/ answers a write user 200 with dev's code (the saves
// since the add) and a read user 403, whether or not the name exists; the
// bare URL serves the primary's pinned code to both; the read user's
// GET /deployments answers 200 in the reader view, with no dev and nothing
// that counts it; kv written through dev's frame token is invisible to
// main's and the reverse; each frame token is bound to its document's
// deployment (dev's document gets a dev claim, main's none), and a user
// who may not open dev gets no dev token.
func TestStaticDeploymentURLAndData(t *testing.T) {
	const tile = "apps/sd"
	a, ws := startAuthPlainDaemon(t)
	dir := filepath.Join(ws, filepath.FromSlash(tile))
	page := func(marker string) string {
		return "<!doctype html><html><head></head><body>" + marker + "</body></html>\n"
	}
	for _, f := range [][2]string{ // the manifest last
		{"scope.json", `{"resources":{"kv":{"type":"kv"}}}` + "\n"},
		{"index.html", page("sd-v1")},
		{probeFile, "sd-v1"},
		{"xbin.json", `{"title":"sd","uses":[{"target":"res:` + tile + `/kv","role":"writer"}]}` + "\n"},
	} {
		must(t, saveIfChanged(filepath.Join(dir, f[0]), f[1]))
	}
	a.waitServed(t, tile, "static", "sd-v1", 30*time.Second)

	c, _ := a.do("POST", "/api/xbin/users", `{"id":"wendy","role":"user","tiles":{"`+tile+`":"write"},"password":"wendy-pw1"}`)
	if c != 200 {
		t.Fatalf("creating the write user: %d", c)
	}
	if c, _ = a.do("POST", "/api/xbin/users", `{"id":"rita","role":"user","tiles":{"`+tile+`":"read"},"password":"rita-pw22"}`); c != 200 {
		t.Fatalf("creating the read user: %d", c)
	}
	writer, reader := loginAs(t, a, "wendy", "wendy-pw1"), loginAs(t, a, "rita", "rita-pw22")

	// A deployment URL before dev exists: the tile has no record, so "+"
	// means nothing (P5): the path answers as any other path no tile holds.
	for _, u := range []userClient{writer, reader} {
		c, _ := u.get("/c/" + tile + "+dev/")
		if other, _ := u.get("/c/" + tile + "-xdev/"); c != other || c == 200 {
			t.Errorf("/c/%s+dev/ before any record: %d, want today's answer for a path no tile holds (%d)", tile, c, other)
		}
	}

	// Add dev with live reload attached: main is pinned to a fresh
	// checkpoint of the work tree, and saves reach dev.
	ans, e := a.op(t, "add", tile, "deployment", "dev", "attach", true)
	if e.Result != "ok" || ans.State.LiveReload != "dev" || ans.State.pinned("main") == "" {
		t.Fatalf("adding dev: %+v, state %+v", e, ans.State)
	}
	must(t, saveIfChanged(filepath.Join(dir, "index.html"), page("sd-v2")))
	must(t, saveIfChanged(filepath.Join(dir, probeFile), "sd-v2"))
	if !waitFor(func() bool { c, b := writer.get("/c/" + tile + "+dev/" + probeFile); return c == 200 && b == "sd-v2" }, 30*time.Second) {
		c, b := writer.get("/c/" + tile + "+dev/" + probeFile)
		t.Fatalf("dev never served the save: %d %q", c, b)
	}

	// Who opens which URL.
	devDoc, mainDoc := "", ""
	for _, u := range []struct {
		who            string
		as             userClient
		devCode, bareC int
	}{{"the write user", writer, 200, 200}, {"the read user", reader, 403, 200}} {
		c, b := u.as.get("/c/" + tile + "+dev/")
		if c != u.devCode {
			t.Errorf("%s: /c/%s+dev/ answered %d, want %d: %.200s", u.who, tile, c, u.devCode, b)
		}
		if u.devCode == 200 {
			devDoc = b
			if !strings.Contains(b, "sd-v2") || !strings.Contains(b, `<meta name="xbin-deployment" content="dev">`) {
				t.Errorf("%s: dev's document isn't dev's code with its meta: %.400s", u.who, b)
			}
		} else if strings.Contains(b, "sd-v2") {
			t.Errorf("%s: the refusal carries dev's code: %.200s", u.who, b)
		}
		c, b = u.as.get("/c/" + tile + "/")
		if c != u.bareC || !strings.Contains(b, "sd-v1") || strings.Contains(b, "sd-v2") || strings.Contains(b, "xbin-deployment") {
			t.Errorf("%s: the bare URL answered %d, not the primary's pinned code: %.300s", u.who, c, b)
		}
		if u.who == "the write user" {
			mainDoc = b
		}
		// an unknown deployment: 404 to those who may know the names, and
		// the same 403 as dev's to the rest
		want := 404
		if u.devCode == 403 {
			want = 403
		}
		if c, _ := u.as.get("/c/" + tile + "+nope/"); c != want {
			t.Errorf("%s: /c/%s+nope/ answered %d, want %d", u.who, tile, c, want)
		}
	}
	if c, b := a.do("GET", "/c/"+tile+"/"+probeFile, ""); c != 200 || b != "sd-v1" {
		t.Errorf("the bare URL's file for the owner: %d %q, want the primary's sd-v1", c, b)
	}

	// The read user's state: the reader view, no dev in any form.
	c, body := reader.get("/api/xbin/deployments?tile=" + tile)
	var view struct {
		View        string            `json:"view"`
		Deployments []json.RawMessage `json:"deployments"`
	}
	if c != 200 || json.Unmarshal([]byte(body), &view) != nil {
		t.Fatalf("the read user's GET /deployments: %d %s", c, body)
	}
	if view.View != "reader" || len(view.Deployments) != 1 || strings.Contains(body, `"dev"`) || strings.Contains(body, "+dev") {
		t.Errorf("the read user's state names or counts dev: %s", body)
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal([]byte(body), &top)
	for _, field := range []string{"caps", "edges", "seq", "lastLiveReload", "workTree", "allowed", "selected"} {
		if _, ok := top[field]; ok {
			t.Errorf("the reader view carries %s: %s", field, body)
		}
	}
	if c, body := writer.get("/api/xbin/deployments?tile=" + tile); c != 200 || !strings.Contains(body, `"name":"dev"`) || !strings.Contains(body, `"view":"full"`) {
		t.Errorf("the write user's GET /deployments: %d, want the full view naming dev: %.300s", c, body)
	}
	for _, name := range []string{"dev", "nope"} { // whether or not it exists
		if c, _ := reader.get("/api/xbin/deployments?tile=" + url.QueryEscape(tile) + "&deployment=" + name); c != 403 {
			t.Errorf("the read user's GET /deployments?tile=%s&deployment=%s: %d, want 403", tile, name, c)
		}
	}
	if c, body := writer.get("/api/xbin/deployments?tile=" + url.QueryEscape(tile) + "&deployment=dev"); c != 200 || !strings.Contains(body, `"selected":"dev"`) {
		t.Errorf("the write user's GET /deployments?tile=%s&deployment=dev: %d %.200s", tile, c, body)
	}
	// P17: a query never carries tile+name, escaped or not (a '+' there reads as a space)
	for _, q := range []string{url.QueryEscape(tile + "+dev"), tile + "+dev"} {
		if c, body := writer.get("/api/xbin/deployments?tile=" + q); c != 400 || !strings.Contains(body, "a deployment is named with deployment=") {
			t.Errorf("the write user's GET /deployments?tile=%s: %d %.200s, want 400", q, c, body)
		}
	}

	// Frame tokens: each document's token is bound to its deployment.
	devTok, mainTok := frameTokenIn(devDoc), frameTokenIn(mainDoc)
	if devTok == "" || mainTok == "" || devTok == mainTok {
		t.Fatalf("frame tokens: dev %q, main %q", devTok, mainTok)
	}
	if n := strings.Count(devTok, "|"); n != 5 {
		t.Errorf("dev's frame token has %d fields, want 6 (the deployment claim): %s", n+1, devTok)
	}
	if n := strings.Count(mainTok, "|"); n != 4 {
		t.Errorf("main's frame token has %d fields, want today's 5: %s", n+1, mainTok)
	}
	if _, b := reader.get("/c/" + tile + "/"); frameTokenIn(b) == "" || strings.Count(frameTokenIn(b), "|") != 4 {
		t.Errorf("the read user's bare document carries no main token: %.300s", b)
	}

	// kv through each token: dev's writes are invisible to main's, and the
	// reverse, under the same canonical id.
	kv := func(tok, method, key, val string) (int, string) {
		t.Helper()
		return frameDo(t, a.url, tok, method, "/api/xbin/kv/res:"+tile+"/kv/"+key, val)
	}
	for _, w := range []struct{ tok, key, val string }{{devTok, "from-dev", "dev-value"}, {mainTok, "from-main", "main-value"}, {devTok, "both", "dev-both"}, {mainTok, "both", "main-both"}} {
		if c, b := kv(w.tok, "PUT", w.key, w.val); c != 200 {
			t.Fatalf("PUT kv %s: %d %s", w.key, c, b)
		}
	}
	for _, r := range []struct {
		who, tok, key string
		code          int
		val           string
	}{
		{"dev", devTok, "from-dev", 200, "dev-value"}, {"main", mainTok, "from-dev", 404, ""},
		{"main", mainTok, "from-main", 200, "main-value"}, {"dev", devTok, "from-main", 404, ""},
		{"dev", devTok, "both", 200, "dev-both"}, {"main", mainTok, "both", 200, "main-both"},
	} {
		c, b := kv(r.tok, "GET", r.key, "")
		if c != r.code || r.code == 200 && b != r.val {
			t.Errorf("%s's frame token reads kv %s: %d %q, want %d %q", r.who, r.key, c, b, r.code, r.val)
		}
	}
	// Each token's listing of the same canonical id holds only its own
	// deployment's keys.
	if c, b := kv(devTok, "GET", "?prefix=", ""); c != 200 || strings.Contains(b, "from-main") || !strings.Contains(b, "from-dev") {
		t.Errorf("dev's kv listing: %d %s", c, b)
	}
	if c, b := kv(mainTok, "GET", "?prefix=", ""); c != 200 || strings.Contains(b, "from-dev") || !strings.Contains(b, "from-main") {
		t.Errorf("main's kv listing: %d %s", c, b)
	}

	// main's kv lives in today's store; dev's in its namespace's own file.
	if _, err := os.Stat(filepath.Join(ws, "data", "resources-enc", ".deployments")); err != nil {
		t.Errorf("dev's writes made no namespace under data/resources-enc/.deployments: %v", err)
	}
}

// startAuthPlainDaemon starts an auth-on xbind (--insecure-vault, not
// isolated: static tiles need no isolation) on a workspace of its own, and
// returns its API with the root token, and the workspace. It stops, and its
// workspace goes, when the test ends.
func startAuthPlainDaemon(t *testing.T) (dlAPI, string) {
	t.Helper()
	aws := filepath.Join(t.TempDir(), "ws")
	addr := isoFreeAddr(t)
	cmd := exec.Command(xbindBin, "--workspace", aws, "--listen", addr, "--insecure-vault")
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	startDaemon(t, cmd, aws)
	a := dlAPI{url: "http://" + addr}
	if !waitFor(func() bool { c, _ := a.do("GET", "/healthz", ""); return c == 200 }, 15*time.Second) {
		t.Fatalf("the auth-on xbind never became healthy:\n%s", out.String())
	}
	tok, err := os.ReadFile(filepath.Join(aws, ".xbin", "token"))
	if err != nil {
		t.Fatalf("the auth-on xbind's root token: %v", err)
	}
	a.token = strings.TrimSpace(string(tok))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("auth-on xbind:\n%s", isoTailString(out.String(), 60))
		}
	})
	return a, aws
}

// userClient is one person's session cookie on a daemon.
type userClient struct {
	base   string
	cookie *http.Cookie
}

// loginAs logs user in through POST /login and returns the session.
func loginAs(t *testing.T, a dlAPI, user, pass string) userClient {
	t.Helper()
	rq, _ := http.NewRequest("POST", a.url+"/login", strings.NewReader("username="+user+"&password="+pass))
	rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cl := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := cl.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	for _, c := range r.Cookies() {
		if c.Name == "xbin_session" && c.Value != "" {
			return userClient{base: a.url, cookie: c}
		}
	}
	t.Fatalf("logging in as %s: %d, no session cookie", user, r.StatusCode)
	return userClient{}
}

// get sends a GET as the user.
func (u userClient) get(path string) (int, string) { return u.do("GET", path, "") }

// do sends a request as the user, a JSON body when body isn't "".
func (u userClient) do(method, path, body string) (int, string) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rq, err := http.NewRequest(method, u.base+path, rd)
	if err != nil {
		return 0, err.Error()
	}
	if body != "" {
		rq.Header.Set("Content-Type", "application/json")
	}
	rq.AddCookie(u.cookie)
	r, err := isoClient.Do(rq)
	if err != nil {
		return 0, err.Error()
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

var frameTokenMeta = regexp.MustCompile(`<meta name="xbin-frame-token" content="([^"]*)">`)

// frameTokenIn is the frame token a document's injection carries ("" none).
func frameTokenIn(doc string) string {
	m := frameTokenMeta.FindStringSubmatch(doc)
	if m == nil {
		return ""
	}
	return m[1]
}

// frameDo sends a request with a frame token, as a tile's frontend does.
func frameDo(t *testing.T, base, tok, method, path, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rq, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	rq.Header.Set("X-XBin-Frame-Token", tok)
	r, err := isoClient.Do(rq)
	if err != nil {
		return 0, err.Error()
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

// isoTailString is the last n lines of s.
func isoTailString(s string, n int) string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "")
}
