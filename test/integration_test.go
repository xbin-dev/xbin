//go:build integration

// End-to-end test of the real buxond binary: static serving + injection,
// a real `go build` backend with hot swap, the grant flow, vault, kv, and
// build-error surfacing. Slower than unit tests (compiles Go twice);
// run with: make integration
package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	baseURL string
	repo    string
	ws      string
)

func TestMain(m *testing.M) {
	var err error
	repo, err = filepath.Abs("..")
	if err != nil {
		panic(err)
	}
	tmp, err := os.MkdirTemp("", "buxon-itest-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	ws = filepath.Join(tmp, "ws")

	bin := filepath.Join(tmp, "buxond")
	build := exec.Command("go", "build", "-o", bin, "./cmd/buxond")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		panic("build buxond: " + string(out))
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	baseURL = "http://" + addr

	cmd := exec.Command(bin, "--workspace", ws, "--listen", addr, "--no-auth")
	cmd.Env = append(os.Environ(), "BUXON_SDK_PATH="+filepath.Join(repo, "sdk"))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	defer cmd.Process.Kill()

	if !waitFor(func() bool {
		r, err := http.Get(baseURL + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	}, 10*time.Second) {
		panic("buxond did not become healthy")
	}

	code := m.Run()
	_ = cmd.Process.Kill()
	os.Exit(code)
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func get(t *testing.T, path string) (int, string) {
	t.Helper()
	r, err := http.Get(baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

func req(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rq, _ := http.NewRequest(method, baseURL+path, rd)
	if body != "" {
		rq.Header.Set("Content-Type", "application/json")
	}
	r, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

func write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(ws, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStaticServingAndInjection(t *testing.T) {
	write(t, "apps/hello/index.html", "<!doctype html>\n<html><head></head><body>hi</body></html>")
	if !waitFor(func() bool { c, _ := get(t, "/c/apps/hello/"); return c == 200 }, 5*time.Second) {
		t.Fatal("component never served")
	}
	_, body := get(t, "/c/apps/hello/")
	for _, want := range []string{"importmap", `name="buxon-component" content="apps/hello"`, "buxon-client.js", "buxon-frame-token"} {
		if !strings.Contains(body, want) {
			t.Errorf("injected HTML missing %q", want)
		}
	}
	if c, _ := get(t, "/c/.buxon/token"); c != 400 && c != 404 {
		t.Errorf(".buxon served through /c/ (code %d)", c)
	}
}

func TestGoBackendLifecycle(t *testing.T) {
	// Copy the counter example (its go.mod resolves the sdk via the
	// generated go.work + BUXON_SDK_PATH).
	cp := exec.Command("cp", "-r", filepath.Join(repo, "examples", "counter-go"), filepath.Join(ws, "apps", "counter"))
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	// Wait for scan, then first request pays the cold build.
	if !waitFor(func() bool {
		c, b := get(t, "/api/apps/counter/count")
		return c == 200 && strings.Contains(b, `"count":0`)
	}, 120*time.Second) {
		t.Fatal("counter backend never came up")
	}

	// Hot swap: change output shape, expect it live within seconds.
	src := filepath.Join(ws, "apps", "counter", "backend", "main.go")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, bytes.Replace(b, []byte(`"count":%d`), []byte(`"count":%d,"v":2`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		_, body := get(t, "/api/apps/counter/count")
		return strings.Contains(body, `"v":2`)
	}, 60*time.Second) {
		t.Fatal("hot swap never became visible")
	}

	// Broken build: old generation keeps serving, error lands in status.
	if err := os.WriteFile(src, append(b, []byte("\nBROKEN!")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		_, body := get(t, "/api/buxon/backends")
		return strings.Contains(body, "build failed")
	}, 30*time.Second) {
		t.Fatal("build error never surfaced in status")
	}
	if c, body := get(t, "/api/apps/counter/count"); c != 200 || !strings.Contains(body, `"v":2`) {
		t.Fatalf("old generation stopped serving during broken build: %d %s", c, body)
	}
	// Fix and confirm recovery.
	if err := os.WriteFile(src, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(func() bool {
		_, body := get(t, "/api/buxon/backends")
		return !strings.Contains(body, "build failed")
	}, 60*time.Second) {
		t.Fatal("never recovered from broken build")
	}
}

func TestGrantFlowAndResources(t *testing.T) {
	for _, ex := range []string{"calendar", "email"} {
		cp := exec.Command("cp", "-r", filepath.Join(repo, "examples", ex), filepath.Join(ws, "apps", ex))
		if out, err := cp.CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	// Same-scope auto-grant: calendar can write its own kv + bus. Use
	// today's date: email's /today reads the current day.
	today := time.Now().Format("2006-01-02")
	if !waitFor(func() bool {
		c, _ := req(t, "POST", "/api/apps/calendar/events",
			fmt.Sprintf(`{"day":%q,"time":"09:00","title":"itest"}`, today))
		return c == 200
	}, 120*time.Second) {
		t.Fatal("calendar backend never came up / could not write kv")
	}

	// Cross-scope call before grant → 403 through email's backend.
	if !waitFor(func() bool {
		c, _ := get(t, "/api/apps/email/today")
		return c == 403
	}, 120*time.Second) {
		c, body := get(t, "/api/apps/email/today")
		t.Fatalf("expected 403 pre-grant, got %d %s", c, body)
	}

	// Pending grant computed from uses.
	_, grantsBody := get(t, "/api/buxon/grants")
	if !strings.Contains(grantsBody, `"from":"apps/email"`) {
		t.Fatalf("pending grant missing: %s", grantsBody)
	}

	// Approve and verify.
	if c, b := req(t, "POST", "/api/buxon/grants",
		`{"from":"apps/email","target":"apps/calendar","role":"reader"}`); c != 200 {
		t.Fatalf("grant approve: %d %s", c, b)
	}
	if !waitFor(func() bool {
		c, body := get(t, "/api/apps/email/today")
		return c == 200 && strings.Contains(body, "itest")
	}, 30*time.Second) {
		c, body := get(t, "/api/apps/email/today")
		t.Fatalf("post-grant call failed: %d %s", c, body)
	}

	// Reader grant must not satisfy writer routes on the callee...
	// (owner curl is admin, so exercise via role check on POST from email:
	// covered at unit level; here just confirm owner POST works.)
	if c, _ := req(t, "POST", "/api/apps/calendar/events",
		`{"day":"2026-01-02","title":"x"}`); c != 200 {
		t.Fatal("owner POST failed")
	}

	// Vault: owner sets, element reads its own.
	if c, b := req(t, "PUT", "/api/buxon/vault/apps/email/imap-pass", `{"value":"hunter2"}`); c != 200 {
		t.Fatalf("vault put: %d %s", c, b)
	}
	if !waitFor(func() bool {
		_, body := get(t, "/api/apps/email/imap-status")
		return strings.Contains(body, `"configured":true`)
	}, 30*time.Second) {
		t.Fatal("element could not read its own vault")
	}
}

func TestComponentsAPI(t *testing.T) {
	_, body := get(t, "/api/buxon/components")
	var comps []map[string]any
	if err := json.Unmarshal([]byte(body), &comps); err != nil {
		t.Fatalf("components not JSON: %v", err)
	}
	found := false
	for _, c := range comps {
		if c["path"] == "apps/calendar" {
			found = true
			if c["runtime"] != "go" {
				t.Errorf("calendar runtime: %v", c["runtime"])
			}
		}
	}
	if !found {
		t.Fatalf("apps/calendar missing from %s", body)
	}
	_, one := get(t, "/api/buxon/components/apps/calendar")
	if !strings.Contains(one, "apiDoc") || !strings.Contains(one, "Roles") {
		t.Errorf("component detail missing API.md: %s", firstN(one, 200))
	}
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// TestAdminCapability verifies the buxon:admin capability: a granted tile
// reaches admin endpoints, an ungranted one is denied, revoking disarms.
func TestAdminCapability(t *testing.T) {
	write(t, "tiles/admin/buxon.json", `{"uses":[{"target":"buxon","role":"admin"}]}`)
	write(t, "tiles/admin/index.html", `<!doctype html><html><head></head><body>admin</body></html>`)
	write(t, "apps/plain/index.html", `<!doctype html><html><head></head><body>plain</body></html>`)
	if c, b := req(t, "POST", "/api/buxon/grants",
		`{"from":"tiles/admin","target":"buxon","role":"admin"}`); c != 200 {
		t.Fatalf("seed admin grant: %d %s", c, b)
	}
	if !waitFor(func() bool { _, ok := frameToken(t, "tiles/admin"); return ok }, 5*time.Second) {
		t.Fatal("admin component never registered")
	}

	adminTok, _ := frameToken(t, "tiles/admin")
	plainTok, _ := frameToken(t, "apps/plain")

	for _, ep := range []string{"/api/buxon/auth-overview", "/api/buxon/vaults",
		"/api/buxon/resources", "/api/buxon/grants", "/api/buxon/backends"} {
		if code := getFramed(t, ep, adminTok); code != 200 {
			t.Errorf("admin tile %s: got %d, want 200", ep, code)
		}
		if code := getFramed(t, ep, plainTok); code != 403 {
			t.Errorf("ungranted tile %s: got %d, want 403", ep, code)
		}
	}

	// Own vault stays accessible to an unprivileged tile (not 403).
	if code := getFramed(t, "/api/buxon/vault/apps/plain/nope", plainTok); code == 403 {
		t.Error("unprivileged tile denied its OWN vault")
	}
	// ...but not another's.
	if code := getFramed(t, "/api/buxon/vault/tiles/admin/x", plainTok); code != 403 {
		t.Errorf("unprivileged cross-vault: got %d, want 403", code)
	}

	// Revoke disarms the admin tile.
	if c, _ := req(t, "DELETE", "/api/buxon/grants",
		`{"from":"tiles/admin","target":"buxon","role":"admin"}`); c != 200 {
		t.Fatal("revoke failed")
	}
	if code := getFramed(t, "/api/buxon/auth-overview", adminTok); code != 403 {
		t.Errorf("revoked admin tile still has access: %d", code)
	}
}

func frameToken(t *testing.T, comp string) (string, bool) {
	t.Helper()
	c, body := get(t, "/api/buxon/frame-token?component="+comp)
	if c != 200 {
		return "", false
	}
	var d struct{ Token string }
	if json.Unmarshal([]byte(body), &d) != nil || d.Token == "" {
		return "", false
	}
	return d.Token, true
}

func getFramed(t *testing.T, path, tok string) int {
	t.Helper()
	rq, _ := http.NewRequest("GET", baseURL+path, nil)
	rq.Header.Set("X-Buxon-Frame-Token", tok)
	r, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer r.Body.Close()
	return r.StatusCode
}
