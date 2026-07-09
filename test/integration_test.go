//go:build integration

// End-to-end test of the real xbind binary: static serving + injection,
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
	baseURL  string
	repo     string
	ws       string
	xbindBin string // built daemon, reused by tests that need their own instance
)

func TestMain(m *testing.M) {
	var err error
	repo, err = filepath.Abs("..")
	if err != nil {
		panic(err)
	}
	tmp, err := os.MkdirTemp("", "xbin-itest-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	ws = filepath.Join(tmp, "ws")

	bin := filepath.Join(tmp, "xbind")
	xbindBin = bin
	build := exec.Command("go", "build", "-o", bin, "./cmd/xbind")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		panic("build xbind: " + string(out))
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	baseURL = "http://" + addr

	cmd := exec.Command(bin, "--workspace", ws, "--listen", addr, "--no-auth")
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
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
		panic("xbind did not become healthy")
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
	for _, want := range []string{"importmap", `name="xbin-component" content="apps/hello"`, "xbin-client.js", "xbin-frame-token"} {
		if !strings.Contains(body, want) {
			t.Errorf("injected HTML missing %q", want)
		}
	}
	if c, _ := get(t, "/c/.xbin/token"); c != 400 && c != 404 {
		t.Errorf(".xbin served through /c/ (code %d)", c)
	}
}

func TestGoBackendLifecycle(t *testing.T) {
	// Copy the counter example (its go.mod resolves the sdk via the
	// generated go.work + XBIN_SDK_PATH).
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
		_, body := get(t, "/api/xbin/backends")
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
		_, body := get(t, "/api/xbin/backends")
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
	_, grantsBody := get(t, "/api/xbin/grants")
	if !strings.Contains(grantsBody, `"from":"apps/email"`) {
		t.Fatalf("pending grant missing: %s", grantsBody)
	}

	// Approve and verify.
	if c, b := req(t, "POST", "/api/xbin/grants",
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
	if c, b := req(t, "PUT", "/api/xbin/vault/apps/email/imap-pass", `{"value":"hunter2"}`); c != 200 {
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
	_, body := get(t, "/api/xbin/components")
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
	_, one := get(t, "/api/xbin/components/apps/calendar")
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

// TestVaultBarrier verifies encryption at rest: a secret written before the
// barrier is plaintext on disk, `vault-unseal` initializes the barrier and
// migrates it to ciphertext, sealing blocks management reads (503), and
// unsealing restores them. Also checks the vault lockdown — admin/owner can
// list keys but not read a secret's value. Leaves the shared vault unsealed.
func TestVaultBarrier(t *testing.T) {
	write(t, "apps/vaulttest/index.html", `<!doctype html><html><head></head><body>v</body></html>`)
	if !waitFor(func() bool { c, _ := get(t, "/api/xbin/components/apps/vaulttest"); return c == 200 }, 5*time.Second) {
		t.Fatal("component never registered")
	}

	if c, b := req(t, "PUT", "/api/xbin/vault/apps/vaulttest/k", `{"value":"TOPSECRET_XYZ"}`); c != 200 {
		t.Fatalf("vault put: %d %s", c, b)
	}

	// Initialize the barrier (first unseal) and migrate existing plaintext.
	if c, b := req(t, "POST", "/api/xbin/vault-unseal", `{"passphrase":"pw-integration"}`); c != 200 {
		t.Fatalf("unseal/init: %d %s", c, b)
	}

	// On disk it must now be an encrypted envelope with no plaintext.
	files, _ := filepath.Glob(filepath.Join(ws, "data", "vault", "*.json"))
	leaked := false
	migrated := false
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "TOPSECRET_XYZ") {
			leaked = true
		}
		if strings.Contains(string(b), `"enc"`) {
			migrated = true
		}
	}
	if leaked {
		t.Error("plaintext secret found on disk after encryption")
	}
	if !migrated {
		t.Error("no encrypted envelope on disk")
	}

	// Vault lockdown: admin/owner may LIST keys (management) but NOT read a
	// secret's value — values are readable only by the owning element. The key
	// appearing in the list also proves decryption round-trips while unsealed.
	if c, _ := get(t, "/api/xbin/vault/apps/vaulttest/k"); c != 403 {
		t.Errorf("owner reading a secret value: got %d, want 403 (self-only)", c)
	}
	if c, b := get(t, "/api/xbin/vault/apps/vaulttest/"); c != 200 || !strings.Contains(b, `"k"`) {
		t.Fatalf("owner list while unsealed: %d %s", c, b)
	}
	// Seal → management reads 503.
	if c, _ := req(t, "POST", "/api/xbin/vault-seal", ``); c != 200 {
		t.Fatal("seal failed")
	}
	if c, _ := get(t, "/api/xbin/vault/apps/vaulttest/"); c != 503 {
		t.Errorf("list while sealed: got %d, want 503", c)
	}
	// Wrong passphrase → 403.
	if c, _ := req(t, "POST", "/api/xbin/vault-unseal", `{"passphrase":"wrong"}`); c != 403 {
		t.Errorf("wrong passphrase: got %d, want 403", c)
	}
	// Correct → unsealed, listing restored (decrypt round-trips). Leave unsealed.
	if c, _ := req(t, "POST", "/api/xbin/vault-unseal", `{"passphrase":"pw-integration"}`); c != 200 {
		t.Fatal("re-unseal failed")
	}
	if c, b := get(t, "/api/xbin/vault/apps/vaulttest/"); c != 200 || !strings.Contains(b, `"k"`) {
		t.Fatalf("list after re-unseal: %d %s", c, b)
	}
}

// TestAdminCapability verifies the xbin:admin capability: a granted tile
// reaches admin endpoints, an ungranted one is denied, revoking disarms.
func TestAdminCapability(t *testing.T) {
	write(t, "tiles/admin/xbin.json", `{"uses":[{"target":"xbin","role":"admin"}]}`)
	write(t, "tiles/admin/index.html", `<!doctype html><html><head></head><body>admin</body></html>`)
	write(t, "apps/plain/index.html", `<!doctype html><html><head></head><body>plain</body></html>`)
	if c, b := req(t, "POST", "/api/xbin/grants",
		`{"from":"tiles/admin","target":"xbin","role":"admin"}`); c != 200 {
		t.Fatalf("seed admin grant: %d %s", c, b)
	}
	if !waitFor(func() bool { _, ok := frameToken(t, "tiles/admin"); return ok }, 5*time.Second) {
		t.Fatal("admin component never registered")
	}

	adminTok, _ := frameToken(t, "tiles/admin")
	plainTok, _ := frameToken(t, "apps/plain")

	for _, ep := range []string{"/api/xbin/auth-overview", "/api/xbin/vaults",
		"/api/xbin/resources", "/api/xbin/grants", "/api/xbin/backends"} {
		if code := getFramed(t, ep, adminTok); code != 200 {
			t.Errorf("admin tile %s: got %d, want 200", ep, code)
		}
		if code := getFramed(t, ep, plainTok); code != 403 {
			t.Errorf("ungranted tile %s: got %d, want 403", ep, code)
		}
	}

	// Own vault stays accessible to an unprivileged tile (not 403).
	if code := getFramed(t, "/api/xbin/vault/apps/plain/nope", plainTok); code == 403 {
		t.Error("unprivileged tile denied its OWN vault")
	}
	// ...but not another's.
	if code := getFramed(t, "/api/xbin/vault/tiles/admin/x", plainTok); code != 403 {
		t.Errorf("unprivileged cross-vault: got %d, want 403", code)
	}

	// Revoke disarms the admin tile.
	if c, _ := req(t, "DELETE", "/api/xbin/grants",
		`{"from":"tiles/admin","target":"xbin","role":"admin"}`); c != 200 {
		t.Fatal("revoke failed")
	}
	if code := getFramed(t, "/api/xbin/auth-overview", adminTok); code != 403 {
		t.Errorf("revoked admin tile still has access: %d", code)
	}
}

func frameToken(t *testing.T, comp string) (string, bool) {
	t.Helper()
	c, body := get(t, "/api/xbin/frame-token?component="+comp)
	if c != 200 {
		return "", false
	}
	var d struct{ Token string }
	if json.Unmarshal([]byte(body), &d) != nil || d.Token == "" {
		return "", false
	}
	return d.Token, true
}

// TestMultiUser verifies human users + tile-level RBAC end to end against a
// dedicated auth-ON instance (the shared harness runs --no-auth). The root
// token creates a restricted and an admin user; login works; the restricted
// user is confined (allowed tile 200; others, admin endpoints, terminal 403),
// the admin user is not; deleting a user revokes their session.
func TestMultiUser(t *testing.T) {
	dir := t.TempDir()
	muWS := filepath.Join(dir, "ws")
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	base := "http://" + addr

	// auth ON, real (non-dev) start; --insecure-vault so it boots without a
	// passphrase (this test isn't exercising the vault).
	cmd := exec.Command(xbindBin, "--workspace", muWS, "--listen", addr, "--insecure-vault")
	cmd.Env = append(os.Environ(), "XBIN_SDK_PATH="+filepath.Join(repo, "sdk"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if !waitFor(func() bool {
		r, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	}, 10*time.Second) {
		t.Fatalf("auth instance never healthy: %s", out.String())
	}
	rootTok := strings.TrimSpace(mustReadFile(t, filepath.Join(muWS, ".xbin", "token")))

	root := func(method, path, body string) int {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		rq, _ := http.NewRequest(method, base+path, rd)
		rq.Header.Set("Authorization", "Bearer "+rootTok)
		if body != "" {
			rq.Header.Set("Content-Type", "application/json")
		}
		r, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if c := root("POST", "/api/xbin/users", `{"id":"alice","role":"user","tiles":["apps/welcome"],"password":"alice-pw1"}`); c != 200 {
		t.Fatalf("create alice: %d", c)
	}
	if c := root("POST", "/api/xbin/users", `{"id":"bob","role":"admin","password":"bob-pw22"}`); c != 200 {
		t.Fatalf("create bob: %d", c)
	}

	login := func(user, pass string) (*http.Cookie, bool) {
		rq, _ := http.NewRequest("POST", base+"/login", strings.NewReader("username="+user+"&password="+pass))
		rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		r, err := cl.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		for _, c := range r.Cookies() {
			if c.Name == "xbin_session" && c.Value != "" {
				return c, true
			}
		}
		return nil, false
	}
	as := func(cookie *http.Cookie, path string) int {
		rq, _ := http.NewRequest("GET", base+path, nil)
		rq.AddCookie(cookie)
		r, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}

	if _, ok := login("alice", "nope"); ok {
		t.Fatal("wrong password logged in")
	}
	aliceC, ok := login("alice", "alice-pw1")
	if !ok {
		t.Fatal("alice login failed")
	}
	bobC, ok := login("bob", "bob-pw22")
	if !ok {
		t.Fatal("bob login failed")
	}

	if code := as(aliceC, "/c/apps/welcome/"); code != 200 {
		t.Errorf("alice allowed tile: %d, want 200", code)
	}
	for _, p := range []string{"/c/tiles/admin/", "/api/xbin/backends", "/api/xbin/users",
		"/ws/term?cwd=apps/welcome"} {
		if code := as(aliceC, p); code != 403 {
			t.Errorf("alice %s: %d, want 403", p, code)
		}
	}
	// The root terminal is disabled for everyone — even admins (terminal
	// tokens; docs/changes/2026-07-09-terminal-scoped-tokens.md).
	if code := as(bobC, "/ws/term?cwd="); code != 403 {
		t.Errorf("root terminal for admin bob: %d, want 403", code)
	}
	if code := as(bobC, "/api/xbin/backends"); code != 200 {
		t.Errorf("bob backends: %d, want 200", code)
	}
	if code := as(bobC, "/c/tiles/admin/"); code != 200 {
		t.Errorf("bob admin tile: %d, want 200", code)
	}

	if c := root("DELETE", "/api/xbin/users/alice", ""); c != 200 {
		t.Fatal("delete alice failed")
	}
	if code := as(aliceC, "/api/xbin/whoami"); code != 401 {
		t.Errorf("deleted alice session still valid: %d, want 401", code)
	}

	// Per-user prefs isolation: bob writes a layout pref; a fresh bob cookie
	// reads it back, and the root token (a different principal) does not see it.
	putBob := func(path, body string) int {
		rq, _ := http.NewRequest("PUT", base+path, strings.NewReader(body))
		rq.AddCookie(bobC)
		rq.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if c := putBob("/api/xbin/prefs/layout", `{"screens":[{"id":"x","name":"Home","tiles":[]}]}`); c != 200 {
		t.Fatalf("bob prefs put: %d", c)
	}
	getBody := func(cookie *http.Cookie, tok, path string) (int, string) {
		rq, _ := http.NewRequest("GET", base+path, nil)
		if cookie != nil {
			rq.AddCookie(cookie)
		}
		if tok != "" {
			rq.Header.Set("Authorization", "Bearer "+tok)
		}
		r, _ := http.DefaultClient.Do(rq)
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	if c, body := getBody(bobC, "", "/api/xbin/prefs/layout"); c != 200 || !strings.Contains(body, "Home") {
		t.Fatalf("bob prefs get: %d %s", c, body)
	}
	if c, _ := getBody(nil, rootTok, "/api/xbin/prefs/layout"); c == 200 {
		t.Error("root token saw bob's per-user prefs (isolation broken)")
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func getFramed(t *testing.T, path, tok string) int {
	t.Helper()
	rq, _ := http.NewRequest("GET", baseURL+path, nil)
	rq.Header.Set("X-XBin-Frame-Token", tok)
	r, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer r.Body.Close()
	return r.StatusCode
}

// TestTileImport verifies the builtin tile catalog: llm-gw is listed and
// embedded (Go backend included despite go:embed's nested-module skip),
// imports at its default path, and its backend compiles and runs — proving
// the go.mod.tile rename + immediate go.work regen work end to end.
func TestTileImport(t *testing.T) {
	// Catalog includes the Go-backed llm-gw (would be missing if go:embed
	// had dropped its nested module).
	_, body := get(t, "/api/xbin/builtins")
	if !strings.Contains(body, `"llm-gw"`) || !strings.Contains(body, `"chat"`) {
		t.Fatalf("builtins catalog missing tiles: %s", firstN(body, 200))
	}

	if c, b := req(t, "POST", "/api/xbin/builtins/import", `{"name":"llm-gw"}`); c != 200 {
		t.Fatalf("import llm-gw: %d %s", c, b)
	}
	// go.mod restored from go.mod.tile.
	if _, err := os.Stat(filepath.Join(ws, "apps", "llm-gw", "go.mod")); err != nil {
		t.Fatalf("go.mod not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "apps", "llm-gw", "backend", "main.go")); err != nil {
		t.Fatalf("backend not imported: %v", err)
	}

	// The backend compiles (via regenerated go.work) and answers — no OpenAI
	// token, so it returns the "no token" body, not a build error.
	if !waitFor(func() bool {
		c, b := get(t, "/api/apps/llm-gw/v1/models")
		return c == 200 && strings.Contains(b, "no API token configured")
	}, 120*time.Second) {
		c, b := get(t, "/api/apps/llm-gw/v1/models")
		t.Fatalf("llm-gw backend never built/ran: %d %s", c, b)
	}

	// Re-import at a different path: files copied, self-refs rewritten,
	// unique module path so both coexist.
	if c, b := req(t, "POST", "/api/xbin/builtins/import", `{"name":"llm-gw","path":"apps/gw2"}`); c != 200 {
		t.Fatalf("import as apps/gw2: %d %s", c, b)
	}
	mod, _ := os.ReadFile(filepath.Join(ws, "apps", "gw2", "go.mod"))
	if !strings.Contains(string(mod), "module apps/gw2") {
		t.Errorf("renamed module path missing: %s", mod)
	}
	idx, _ := os.ReadFile(filepath.Join(ws, "apps", "gw2", "index.html"))
	if strings.Contains(string(idx), "apps/llm-gw") {
		t.Error("import-as left a stale self-reference to apps/llm-gw")
	}
}

func TestTemplateInstantiate(t *testing.T) {
	// Builtin template catalog lists the starter blueprint.
	_, body := get(t, "/api/xbin/templates")
	if !strings.Contains(body, `"starter"`) || !strings.Contains(body, `"builtin"`) {
		t.Fatalf("templates catalog missing starter: %s", firstN(body, 200))
	}

	// Instantiate it: files copied, the template marker stripped so it's a
	// normal, plugged-in component.
	if c, b := req(t, "POST", "/api/xbin/templates/new", `{"source":"starter"}`); c != 200 {
		t.Fatalf("instantiate starter: %d %s", c, b)
	}
	man, err := os.ReadFile(filepath.Join(ws, "apps", "starter", "xbin.json"))
	if err != nil {
		t.Fatalf("instance xbin.json missing: %v", err)
	}
	if strings.Contains(string(man), "template") {
		t.Errorf("instance xbin.json still carries the template marker: %s", man)
	}
	if _, err := os.Stat(filepath.Join(ws, "apps", "starter", "index.html")); err != nil {
		t.Fatalf("instance index.html missing: %v", err)
	}

	// It's not a template, so the shell lists it as a normal component.
	if _, cb := get(t, "/api/xbin/components"); !strings.Contains(cb, `"apps/starter"`) {
		t.Errorf("instance not listed as a component: %s", firstN(cb, 200))
	}

	// Instantiate again at a custom path — never overwrites, coexists.
	if c, b := req(t, "POST", "/api/xbin/templates/new", `{"source":"starter","path":"apps/starter2"}`); c != 200 {
		t.Fatalf("instantiate at apps/starter2: %d %s", c, b)
	}
	if _, err := os.Stat(filepath.Join(ws, "apps", "starter2", "index.html")); err != nil {
		t.Fatalf("second instance missing: %v", err)
	}

	// A second attempt at an occupied path fails cleanly, not a silent clobber.
	if c, _ := req(t, "POST", "/api/xbin/templates/new", `{"source":"starter","path":"apps/starter"}`); c == 200 {
		t.Error("re-instantiating over an existing component should fail")
	}
}

func TestBuiltinUpdates(t *testing.T) {
	// The scaffold was recorded at init, so with a matching embed nothing is
	// offered.
	if c, b := get(t, "/api/xbin/builtins/updates"); c != 200 || strings.TrimSpace(b) != "[]" {
		// Not fatal — other tests may have imported tiles; just require a 200 array.
		if c != 200 || !strings.HasPrefix(strings.TrimSpace(b), "[") {
			t.Fatalf("updates list: %d %s", c, firstN(b, 200))
		}
	}

	// Simulate a pre-provenance ("adopted") workspace for one scaffold unit:
	// drop its marker entry and diverge the local copy. It must then surface as
	// an adopted conflict — never a silent fast-forward.
	markerPath := filepath.Join(ws, ".xbin", "builtins.json")
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("no origin marker (init should have written one): %v", err)
	}
	var marker struct {
		Units map[string]json.RawMessage `json:"units"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil {
		t.Fatalf("marker parse: %v", err)
	}
	if _, ok := marker.Units["scaffold:apps/welcome"]; !ok {
		t.Fatalf("scaffold not recorded at init; units: %v", keysOf(marker.Units))
	}
	delete(marker.Units, "scaffold:apps/welcome")
	out, _ := json.Marshal(marker)
	if err := os.WriteFile(markerPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	welcome := filepath.Join(ws, "apps", "welcome", "index.html")
	orig := mustReadFile(t, welcome)
	if err := os.WriteFile(welcome, []byte(orig+"\n<!-- local edit -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, b := get(t, "/api/xbin/builtins/updates")
	if !strings.Contains(b, `"scaffold:apps/welcome"`) || !strings.Contains(b, `"adopted":true`) {
		t.Fatalf("adopted unit not surfaced as an update: %s", firstN(b, 400))
	}
	// Merge is refused on an adopted unit (no trustworthy base).
	if c, _ := req(t, "POST", "/api/xbin/builtins/update", `{"id":"scaffold:apps/welcome","mode":"merge"}`); c == 200 {
		t.Error("merge on an adopted unit should be refused")
	}
	// Replace fast-forwards to the embedded version and re-records provenance.
	if c, mb := req(t, "POST", "/api/xbin/builtins/update", `{"id":"scaffold:apps/welcome","mode":"replace"}`); c != 200 {
		t.Fatalf("replace: %d %s", c, mb)
	}
	if got := mustReadFile(t, welcome); strings.Contains(got, "local edit") {
		t.Error("replace did not discard the local edit")
	}
	if _, b := get(t, "/api/xbin/builtins/updates"); strings.Contains(b, `"scaffold:apps/welcome"`) {
		t.Errorf("unit still offered after replace: %s", firstN(b, 300))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	return k
}

func TestComponentCode(t *testing.T) {
	// Commit inside the component's OWN repo — each component is its own repo
	// (plans/lifecycle.md), so git/log?component=shell reads the shell repo, not
	// the workspace root. Touch a file first so there's a change to commit.
	shellDir := filepath.Join(ws, "shell")
	if err := os.WriteFile(filepath.Join(shellDir, ".probe"), []byte("probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "test snapshot"},
	} {
		cmd := exec.Command("git", append([]string{"-C", shellDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	// Files of a component.
	if _, tb := get(t, "/api/xbin/code/tree?component=shell"); !strings.Contains(tb, "bx-shell.js") {
		t.Fatalf("code tree missing bx-shell.js: %s", firstN(tb, 200))
	}
	// One file's content.
	if _, fb := get(t, "/api/xbin/code/file?component=shell&file=xbin.json"); !strings.Contains(fb, `"content"`) {
		t.Fatalf("code file: %s", firstN(fb, 200))
	}
	// History scoped to the component.
	_, lb := get(t, "/api/xbin/git/log?component=shell")
	if !strings.Contains(lb, `"repo":true`) || !strings.Contains(lb, "test snapshot") {
		t.Fatalf("git log: %s", firstN(lb, 300))
	}
	var log struct {
		Commits []struct {
			Hash string `json:"hash"`
		} `json:"commits"`
	}
	_ = json.Unmarshal([]byte(lb), &log)
	if len(log.Commits) == 0 {
		t.Fatal("git log returned no commits")
	}
	if _, db := get(t, "/api/xbin/git/diff?component=shell&rev="+log.Commits[0].Hash); !strings.Contains(db, `"diff"`) {
		t.Errorf("git diff: %s", firstN(db, 200))
	}
	// Path traversal is rejected.
	if c, _ := get(t, "/api/xbin/code/file?component=shell&file=../../data/vault"); c != 400 {
		t.Errorf("path traversal not rejected: %d", c)
	}
}
