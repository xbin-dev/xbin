package boot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestResolveVaultMode(t *testing.T) {
	cases := []struct {
		name        string
		cfg         Config
		initialized bool
		want        VaultMode
	}{
		{"passphrase wins over everything", Config{VaultPassphrase: "x", InsecureVault: true, NoAuth: true, Dev: true}, true, VaultFromEnv},
		{"insecure-vault", Config{InsecureVault: true, Dev: true}, true, VaultPlaintext},
		{"no-auth implies plaintext", Config{NoAuth: true, Dev: true}, false, VaultPlaintext},
		{"bare dev auto-encrypts", Config{Dev: true}, false, VaultDevKey},
		{"production with a barrier starts sealed", Config{}, true, VaultSealed},
		{"production without a barrier starts locked", Config{}, false, VaultLocked},
	}
	for _, c := range cases {
		if got := ResolveVaultMode(&c.cfg, c.initialized); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The boot order carries the migration edges docs/compat.md rule 1 relies
// on; a reordering must be deliberate.
func TestStepsOrder(t *testing.T) {
	idx := func(name string) int {
		i := slices.IndexFunc(Steps, func(s Step) bool { return s.Name == name })
		if i < 0 {
			t.Fatalf("no step %q", name)
		}
		return i
	}
	for _, e := range [][2]string{
		{"workspace", "privileges"}, // root seeds AGENTS.md/CLAUDE.md before becoming the owner
		{"auth+users", "homes"},     // the home migration's target is the users store's one human
		{"registry", "broker"},      // the essential-tile backfill rescans the registry
		{"broker", "vault"},         // the vault is the broker's barrier
		{"broker", "proxy"},
		{"proxy", "ingress"},
		{"ingress", "server"},
		{"server", "watch"},
	} {
		if idx(e[0]) >= idx(e[1]) {
			t.Errorf("step %q must run before %q", e[0], e[1])
		}
	}
}

func TestConfigFlagsAndEnv(t *testing.T) {
	t.Setenv("XBIN_LISTEN", "127.0.0.1:9999")
	t.Setenv("XBIN_LIMIT_MEM", "3G")
	var cfg Config
	fs := newFlagSet()
	cfg.RegisterFlags(fs)
	if err := fs.Parse([]string{"--workspace", "/w", "--dev"}); err != nil {
		t.Fatal(err)
	}
	cfg.FromEnv()
	if cfg.Workspace != "/w" || !cfg.Dev {
		t.Errorf("flags: %+v", cfg)
	}
	if cfg.Listen != "127.0.0.1:9999" {
		t.Errorf("env is the flag default: listen = %q", cfg.Listen)
	}
	if cfg.LimitMem != "3G" || cfg.LimitDisk != "50G" {
		t.Errorf("env-only settings: mem %q disk %q", cfg.LimitMem, cfg.LimitDisk)
	}
	if _, err := (&Config{Workspace: ".", ExternalURL: "https://x.example/path"}).Validate(); err == nil {
		t.Error("an external URL with a path must be refused")
	}
	if _, err := (&Config{Workspace: ".", DevOverlay: "."}).Validate(); err == nil {
		t.Error("--dev-overlay without --dev must be refused")
	}
	if d, err := (&Config{Workspace: ".", TrustedProxy: "10.0.0.1, 10.1.0.0/16"}).Validate(); err != nil || len(d.trusted) != 2 {
		t.Errorf("trusted proxies: %v %v", d.trusted, err)
	}
}

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"": 7, "2G": 2 << 30, "512m": 512 << 20, "1024": 1024, "bad": 7, "-3": 7} {
		if got := parseBytes("X", in, 7); got != want {
			t.Errorf("parseBytes(%q) = %d, want %d", in, got, want)
		}
	}
}

// A workspace created by an older xbind must boot on today's daemon with
// exactly the migrations the contract allows, and a second boot must change
// nothing (docs/compat.md rules 1 and 9) — the in-process twin of the
// integration test in test/legacy_workspace_test.go, which boots the real
// binary. The fixture is the current scaffold, aged: a legacy shared home/
// holding real data, no homes/, no backfill ledger, no builtins provenance,
// no tiles/organisations, a .gitignore without homes/.
func TestLegacyWorkspaceBootsTwiceInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// age it
	_ = os.RemoveAll(filepath.Join(ws, "homes"))
	must(os.MkdirAll(filepath.Join(ws, "home", ".claude"), 0o755))
	must(os.WriteFile(filepath.Join(ws, "home", ".claude", "settings.json"), []byte(`{"kept":true}`), 0o644))
	_ = os.Remove(filepath.Join(ws, "data", "backfills.json"))
	_ = os.Remove(filepath.Join(ws, ".xbin", "builtins.json"))
	_ = os.RemoveAll(filepath.Join(ws, ".xbin", "builtins"))
	_ = os.RemoveAll(filepath.Join(ws, "tiles", "organisations"))
	gi := filepath.Join(ws, ".gitignore")
	if b, err := os.ReadFile(gi); err == nil {
		must(os.WriteFile(gi, []byte(strings.ReplaceAll(string(b), "homes/\n", "")), 0o644))
	}
	before := snapshot(t, ws)
	if _, ok := before["home/.claude/settings.json"]; !ok {
		t.Fatal("fixture: legacy home/ missing")
	}

	bootOnce(t, ws)
	after1 := snapshot(t, ws)
	for _, want := range []string{"homes/owner/.claude/settings.json", "tiles/organisations/xbin.json", "data/backfills.json", "AGENTS.md"} {
		if _, ok := after1[want]; !ok {
			t.Errorf("after the first boot %s is missing", want)
		}
	}
	if _, ok := after1["home/.claude/settings.json"]; ok {
		t.Error("legacy home/ still present after migration")
	}
	if b, _ := os.ReadFile(gi); !strings.Contains(string(b), "homes/") {
		t.Error(".gitignore did not gain homes/")
	}
	if before["xbin.json"] != after1["xbin.json"] {
		t.Error("the root xbin.json (the import map, grants) was rewritten at boot — an upgrade must never touch it")
	}
	allowed := func(rel string) bool {
		for _, p := range []string{".xbin/", "data/", "homes/", "home/", "tiles/organisations/", "go.work", ".gitignore", "AGENTS.md", "CLAUDE.md", ".git/"} {
			if rel == strings.TrimSuffix(p, "/") || strings.HasPrefix(rel, p) {
				return true
			}
		}
		return strings.Contains(rel, "/deps/") || strings.Contains(rel, "/.git/")
	}
	for _, rel := range changed(before, after1) {
		if !allowed(rel) {
			t.Errorf("first boot changed %s, which no migration is allowed to touch (docs/compat.md rule 1)", rel)
		}
	}

	bootOnce(t, ws)
	after2 := snapshot(t, ws)
	if diff := changed(after1, after2); len(diff) > 0 {
		t.Errorf("a second boot changed files — migrations must be idempotent:\n  %s", strings.Join(diff, "\n  "))
	}
}

// Both home/ (with real data) and homes/ present: boot must refuse rather
// than merge by guesswork (docs/compat.md rule 1 keeps this stop).
func TestLegacyHomeConflictStopsInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"home/.claude/settings.json", "homes/alice/.claude/settings.json"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(ws, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, p), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := Run(context.Background(), testConfig(ws, nil))
	if err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("want a refusal naming the home conflict, got %v", err)
	}
}

// ---- helpers ----

func quiet(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

func testConfig(ws string, ln net.Listener) *Config {
	cfg := &Config{Workspace: ws, Listen: "127.0.0.1:0", NoAuth: true, InsecureVault: true,
		Privileges: NoPrivileges{}, Stdout: io.Discard, Version: "test", LimitMem: "2G"}
	if ln != nil {
		cfg.Listener = ln
		cfg.Listen = ln.Addr().String()
	}
	if sdk, err := filepath.Abs("../../sdk"); err == nil {
		os.Setenv("XBIN_SDK_PATH", sdk)
	}
	return cfg
}

// bootOnce runs the daemon on ws until /healthz answers, then stops it.
func bootOnce(t *testing.T, ws string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(ws, ln)
	ready := make(chan string, 1)
	cfg.Ready = func(addr string) { ready <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("boot ended before serving: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("boot never served")
	}
	healthy := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		r, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				healthy = true
				break
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("boot returned %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("boot did not stop after cancel")
	}
	if !healthy {
		t.Fatal("the daemon never became healthy")
	}
}

// snapshot maps every file under ws (rel path → content hash), skipping the
// runtime state a boot legitimately churns: logs, sockets, build and cache
// dirs, terminal layers.
func snapshot(t *testing.T, ws string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(ws, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".xbin/log", ".xbin/run", ".xbin/term", ".xbin/build", ".xbin/cache", ".xbin/env", ".git":
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".log") || strings.HasSuffix(rel, ".sock") || strings.HasSuffix(rel, ".pid") {
			return nil
		}
		if strings.HasSuffix(rel, "/.git/index") || strings.Contains(rel, "/.git/logs/") {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			out[rel] = "-> " + target
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func changed(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, k+" (removed)")
		} else if w != v {
			out = append(out, k+" (rewritten)")
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k+" (added)")
		}
	}
	sort.Strings(out)
	return out
}

var _ = exec.Command // git is optional for the fixture (InitWorkspace tolerates its absence)
