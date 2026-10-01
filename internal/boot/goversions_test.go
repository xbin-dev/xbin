package boot

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D166's upgrade check — wired by the boot: its state file is the
// done-marker (a workspace whose check is done never runs it again), its
// alert reaches the owner with the route that dismisses it — and no one
// else: neither a user of the tile it names nor that tile's own terminal
// sees it or reaches its routes (G1 review finding 8) — GET
// /go-build-versions reports it to admins only, a dismissal hides the
// tile's line and is kept, a dismissal of a tile it doesn't list is 404,
// and POST /go-build-versions/check compares the tile with its baseline
// again: its code no longer links the module, so its line goes.
func TestGoBuildVersionsAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := filepath.Join(t.TempDir(), "ws")
	state := `{"since":"v0.3.65","done":true,"baseline":{"apps/zsgo":{"had":["zsgo","example.com/dep v1.2.0"]}},
		"tiles":{"apps/zsgo":{"require":["example.com/dep v1.2.0"],"minimal":true,
		"changes":[{"module":"example.com/dep","had":"v1.2.0","now":"v1.0.0"}],"checkedAt":"2026-10-01T00:00:00Z"}}}`
	for rel, body := range map[string]string{
		"xbin.json":                   `{"schema":1}`,
		"apps/zs/xbin.json":           `{"title":"Zero"}`,
		"apps/zs/index.html":          "<!doctype html><html><head></head><body>zs</body></html>\n",
		"apps/zsgo/xbin.json":         `{"runtime":"go"}`,
		"apps/zsgo/go.mod":            "module zsgo\n\ngo 1.22\n",
		"apps/zsgo/backend/main.go":   "package main\n\nfunc main() {}\n",
		"data/go-build-versions.json": state,
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBoot(t, ws)
	owner := "Bearer " + d.owner
	post := func(path, body, cred string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("POST", d.url+path, strings.NewReader(body))
		if c, ok := strings.CutPrefix(cred, "Cookie: "); ok {
			req.Header.Set("Cookie", c)
		} else if cred != "" {
			req.Header.Set("Authorization", cred)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	alert := func(cred string) map[string]any {
		t.Helper()
		code, b := d.do(t, "GET", "/api/xbin/alerts", cred)
		var out struct{ Alerts []map[string]any }
		if code != 200 || json.Unmarshal(b, &out) != nil {
			t.Fatalf("alerts: %d %s", code, b)
		}
		for _, a := range out.Alerts {
			if a["kind"] == "go-build-versions" {
				return a
			}
		}
		return nil
	}
	a := alert(owner)
	if a == nil || a["level"] != "warn" || a["dismiss"] != "/go-build-versions/dismiss" ||
		a["message"] != "apps/zsgo builds with older dependency versions since v0.3.65 (each Go tile now builds with its own go.mod's versions): add `require example.com/dep v1.2.0` to apps/zsgo's go.mod to keep what it had" {
		t.Fatalf("the owner's alert: %+v", a)
	}
	if code, _ := d.do(t, "GET", "/api/xbin/go-build-versions", ""); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("an anonymous report: %d", code)
	}
	// a user who writes the tile, and the tile's own terminal: not admins
	if _, err := d.st.Users.Upsert(users.User{ID: "ana", Role: users.RoleUser, Tiles: map[string]string{"apps/zsgo": users.LevelWrite}}, "password1"); err != nil {
		t.Fatal(err)
	}
	for who, cred := range map[string]string{
		"a user of the tile":  "Cookie: xbin_session=" + d.st.Auth.NewSession("ana", "127.0.0.1"),
		"the tile's terminal": "Bearer " + d.st.Auth.MintTerminal("apps/zsgo", ""),
	} {
		if code, b := d.do(t, "GET", "/api/xbin/go-build-versions", cred); code != http.StatusForbidden || strings.Contains(string(b), "example.com/dep") {
			t.Errorf("%s: GET %d %s", who, code, b)
		}
		for _, path := range []string{"/api/xbin/go-build-versions/check", "/api/xbin/go-build-versions/dismiss"} {
			if code, b := post(path, "", cred); code != http.StatusForbidden {
				t.Errorf("%s: POST %s %d %s", who, path, code, b)
			}
		}
		if a := alert(cred); a != nil {
			t.Errorf("%s sees the alert: %+v", who, a)
		}
	}
	code, b := d.do(t, "GET", "/api/xbin/go-build-versions", owner)
	var rep struct {
		Since, Running any
		Done           bool
		Tiles          []struct {
			Tile      string
			Require   []string
			Dismissed bool
		}
	}
	if code != 200 || json.Unmarshal(b, &rep) != nil || !rep.Done || rep.Since != "v0.3.65" || len(rep.Tiles) != 1 || rep.Tiles[0].Tile != "apps/zsgo" {
		t.Fatalf("report: %d %s", code, b)
	}
	if code, b := post("/api/xbin/go-build-versions/dismiss", `{"tile":"apps/nope"}`, owner); code != http.StatusNotFound {
		t.Errorf("dismiss an unlisted tile: %d %s", code, b)
	}
	if code, b := post("/api/xbin/go-build-versions/dismiss", "", owner); code != 200 || !strings.Contains(string(b), `"dismissed":true`) {
		t.Fatalf("dismiss: %d %s", code, b)
	}
	if a := alert(owner); a != nil {
		t.Errorf("a dismissed alert: %+v", a)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "data", "go-build-versions.json")); !strings.Contains(string(b), `"dismissed": true`) {
		t.Errorf("the dismissal isn't kept:\n%s", b)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return // the re-check runs `go list`
	}
	// a re-check against the baseline: the tile links no example.com/dep now
	if code, b := post("/api/xbin/go-build-versions/check", "", owner); code != http.StatusAccepted || !strings.Contains(string(b), `"started":true`) {
		t.Fatalf("check: %d %s", code, b)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, b := d.do(t, "GET", "/api/xbin/go-build-versions", owner)
		if strings.Contains(string(b), `"running":false`) && strings.Contains(string(b), `"tiles":[]`) && strings.Contains(string(b), `"errors":[]`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pass never ended: %s", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// covers G1 review finding 8 — Boot → Start → the alert, end to end: a
// workspace an earlier xbind built a Go tile in (its binary, no state
// file), tile a lifting example.com/top for tile b under the shared
// go.work; the first pass starts on its own after the (shortened) delay,
// lists both tiles with the go command (isolation off: as the build runs
// then, from a module proxy of the test's own) and raises the owner's
// alert naming b's one line. A fresh workspace has nothing to re-check.
func TestGoBuildVersionsBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	ws := filepath.Join(t.TempDir(), "ws")
	proxy := filepath.Join(t.TempDir(), "proxy")
	goProxyModule(t, proxy, "example.com/base", "v1.0.0", "module example.com/base\n\ngo 1.22\n",
		map[string]string{"base.go": "package base\n\nconst V = \"base v1.0.0\"\n"})
	goProxyModule(t, proxy, "example.com/base", "v1.1.0", "module example.com/base\n\ngo 1.22\n",
		map[string]string{"base.go": "package base\n\nconst V = \"base v1.1.0\"\n"})
	goProxyModule(t, proxy, "example.com/top", "v1.0.0", "module example.com/top\n\ngo 1.22\n\nrequire example.com/base v1.0.0\n",
		map[string]string{"top.go": "package top\n\nimport \"example.com/base\"\n\nconst V = base.V\n"})
	goProxyModule(t, proxy, "example.com/top", "v1.1.0", "module example.com/top\n\ngo 1.22\n\nrequire example.com/base v1.1.0\n",
		map[string]string{"top.go": "package top\n\nimport \"example.com/base\"\n\nconst V = base.V\n"})
	main := "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/top\"\n)\n\nfunc main() { fmt.Println(top.V) }\n"
	for rel, body := range map[string]string{
		"xbin.json":              `{"schema":1}`,
		"apps/a/xbin.json":       `{"runtime":"go"}`,
		"apps/a/go.mod":          "module a\n\ngo 1.22\n\nrequire example.com/top v1.1.0\n\nrequire example.com/base v1.1.0 // indirect\n",
		"apps/a/backend/main.go": main,
		"apps/b/xbin.json":       `{"runtime":"go"}`,
		"apps/b/go.mod":          "module b\n\ngo 1.22\n\nrequire example.com/top v1.0.0\n\nrequire example.com/base v1.0.0 // indirect\n",
		"apps/b/backend/main.go": main,
		".xbin/build/" + util.CompKey("apps/b") + "/bin": "an old binary",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOPROXY", "file://"+proxy)
	t.Setenv("GONOSUMDB", "example.com")
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "mod"))
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOTOOLCHAIN", "local")
	d := zsBootWith(t, ws, func(c *Config) { c.GoVersionsDelay = 10 * time.Millisecond })
	owner := "Bearer " + d.owner
	want := "apps/b builds with older dependency versions since test (each Go tile now builds with its own go.mod's versions): add `require example.com/top v1.1.0` to apps/b's go.mod to keep what it had"
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, b := d.do(t, "GET", "/api/xbin/alerts", owner)
		var out struct{ Alerts []map[string]any }
		_ = json.Unmarshal(b, &out)
		var got any
		for _, a := range out.Alerts {
			if a["kind"] == "go-build-versions" {
				got = a["message"]
			}
		}
		if got == want {
			break
		}
		if time.Now().After(deadline) {
			_, rep := d.do(t, "GET", "/api/xbin/go-build-versions", owner)
			t.Fatalf("no alert (%v): %s", got, rep)
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, err := os.ReadFile(filepath.Join(ws, "data", "go-build-versions.json"))
	if err != nil || !strings.Contains(string(b), `"done": true`) || !strings.Contains(string(b), `"since": "test"`) {
		t.Errorf("the state: %s %v", b, err)
	}

	// a fresh workspace: no Go build of an earlier xbind, nothing to compare
	f := zsBoot(t, zsWorkspace(t))
	req, _ := http.NewRequest("POST", f.url+"/api/xbin/go-build-versions/check", nil)
	req.Header.Set("Authorization", "Bearer "+f.owner)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"started":false`) || !strings.Contains(string(body), "nothing to compare") {
		t.Errorf("a re-check of a fresh workspace: %d %s", resp.StatusCode, body)
	}
}

// goProxyModule lays out one module version as a GOPROXY serves it: its
// .info, .mod and .zip beneath dir, and its version in @v/list.
func goProxyModule(t *testing.T, dir, path, version, gomod string, files map[string]string) {
	t.Helper()
	at := filepath.Join(dir, filepath.FromSlash(path), "@v")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, s string) {
		if err := os.WriteFile(filepath.Join(at, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(version+".info", fmt.Sprintf(`{"Version":%q,"Time":"2026-01-02T03:04:05Z"}`, version))
	put(version+".mod", gomod)
	f, err := os.Create(filepath.Join(at, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	files["go.mod"] = gomod
	for name, s := range files {
		w, err := zw.Create(path + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	list, _ := os.ReadFile(filepath.Join(at, "list"))
	put("list", string(list)+version+"\n")
}
