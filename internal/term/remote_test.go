package term

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// remoteRig is a terminal manager shaped like TestSessionEnvZeroState's (a
// shared Env closure like boot's, a fixed terminal token) whose has-record
// hook answers from records and logs every tile it is asked about.
type remoteRig struct {
	m       *Manager
	root    string
	records map[string]bool
	asked   []string
}

func newRemoteRig(t *testing.T, xbinURL string, tiles ...string) *remoteRig {
	t.Helper()
	r := &remoteRig{root: t.TempDir(), records: map[string]bool{}}
	for _, d := range tiles {
		if err := os.MkdirAll(filepath.Join(r.root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shared := func() []string {
		return []string{"XBIN_URL=" + xbinURL, "XBIN_WORKSPACE=/w", "XBIN_DOCS=/w/.xbin/docs", "PATH=/opt/xbin/bin:/usr/bin"}
	}
	r.m = &Manager{Root: r.root, Listen: "127.0.0.1:8642", Env: shared, Tokens: &zsTokens{},
		sessions: map[string]*Session{}, envHeld: map[string]bool{}, BxPath: "/opt/xbin/bin/bx"}
	r.m.HasDeploymentRecord = func(tile string) bool {
		r.asked = append(r.asked, tile)
		return r.records[tile]
	}
	return r
}

// gitConfigEnv is the GIT_CONFIG_* part of an env, in order.
func gitConfigEnv(env []string) []string {
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_CONFIG_") {
			out = append(out, e)
		}
	}
	return out
}

// covers P5 P16 T20 SC-WORKTREE — a session opened while its tile has a
// deployment record gets the fetch-only `xbin-deploy` remote as two more
// GIT_CONFIG_* pairs after today's two (GIT_CONFIG_COUNT=4, the fetch
// refspec +refs/heads/deploy/*:refs/deploy/*, the URL on the gateway host
// naming the session's own tile), and its env is otherwise its zero-state
// twin's. A zero-state tile, a tile that opted out, a tile whose record is
// gone while its store stays on disk, a session on another tile, and a
// session with no API token or no URL keep exactly today's pairs (none
// without a token or URL); host shells, which have no pairs today, get none.
// With real git: the injected remote fetches through today's insteadOf
// rewrite and bearer, `deploy/<name>` then resolves and flow H's checkout
// works, and the tile's .git/config is never written.
func TestFetchRemoteInjection(t *testing.T) {
	const url = "http://127.0.0.1:8642"
	tiles := []string{"apps/crm", "apps/shop/admin", "apps/other", "notes+ideas", "lab/a b#1%2"}
	r := newRemoteRig(t, url, tiles...)
	for _, tile := range []string{"apps/crm", "apps/shop/admin", "notes+ideas", "lab/a b#1%2"} {
		r.records[tile] = true
	}
	twin := &Manager{Root: r.root, Listen: r.m.Listen, Env: r.m.Env} // no hook: every tile is zero-state

	today := func(url string) []string {
		return []string{
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=url." + url + "/.insteadOf", "GIT_CONFIG_VALUE_0=http://xbin/",
			"GIT_CONFIG_KEY_1=http." + url + "/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Bearer TERMTOKEN",
		}
	}
	withRemote := func(url, remoteURL string) []string {
		return []string{
			"GIT_CONFIG_COUNT=4",
			"GIT_CONFIG_KEY_0=url." + url + "/.insteadOf", "GIT_CONFIG_VALUE_0=http://xbin/",
			"GIT_CONFIG_KEY_1=http." + url + "/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Bearer TERMTOKEN",
			"GIT_CONFIG_KEY_2=remote.xbin-deploy.url", "GIT_CONFIG_VALUE_2=" + remoteURL,
			"GIT_CONFIG_KEY_3=remote.xbin-deploy.fetch", "GIT_CONFIG_VALUE_3=+refs/heads/deploy/*:refs/deploy/*",
		}
	}

	// Record-bearing tiles: four pairs; everything else is the twin's env,
	// with the count raised and the two pairs appended last.
	for _, c := range []struct {
		tile      string
		relay     bool
		effURL    string
		remoteURL string
	}{
		{"apps/crm", false, url, "http://xbin/api/xbin/checkpoints/apps/crm.git"},
		{"apps/crm", true, "http://10.0.2.2:8642", "http://xbin/api/xbin/checkpoints/apps/crm.git"},
		{"apps/shop/admin", false, url, "http://xbin/api/xbin/checkpoints/apps/shop/admin.git"},
		{"notes+ideas", false, url, "http://xbin/api/xbin/checkpoints/notes+ideas.git"},
		{"lab/a b#1%2", false, url, "http://xbin/api/xbin/checkpoints/lab/a%20b%231%252.git"},
	} {
		r.asked = nil
		got := r.m.sandboxEnv(c.tile, c.relay, "/w/homes/ana", "TERMTOKEN")
		if g, w := zsEnvLines(gitConfigEnv(got)), zsEnvLines(withRemote(c.effURL, c.remoteURL)); g != w {
			t.Errorf("%s (relay %v): GIT_CONFIG env\n%s\nwant\n%s", c.tile, c.relay, g, w)
		}
		if len(r.asked) != 1 || r.asked[0] != c.tile {
			t.Errorf("%s: the has-record hook was asked about %q, want only the session's own tile", c.tile, r.asked)
		}
		base := twin.sandboxEnv(c.tile, c.relay, "/w/homes/ana", "TERMTOKEN")
		want := append([]string(nil), base...)
		for i, e := range want {
			if e == "GIT_CONFIG_COUNT=2" {
				want[i] = "GIT_CONFIG_COUNT=4"
			}
		}
		want = append(want, withRemote(c.effURL, c.remoteURL)[5:]...)
		if zsEnvLines(got) != zsEnvLines(want) {
			t.Errorf("%s (relay %v): env is not its zero-state twin's plus the remote:\n%s\nwant\n%s",
				c.tile, c.relay, zsEnvLines(got), zsEnvLines(want))
		}
	}

	// Two pairs, exactly today's, byte-equal to the never-opted-in twin.
	two := func(name string, m *Manager, tile string) {
		t.Helper()
		got := m.sandboxEnv(tile, false, "/w/homes/ana", "TERMTOKEN")
		if g, w := zsEnvLines(gitConfigEnv(got)), zsEnvLines(today(url)); g != w {
			t.Errorf("%s: GIT_CONFIG env\n%s\nwant today's\n%s", name, g, w)
		}
		if g, w := zsEnvLines(got), zsEnvLines(twin.sandboxEnv(tile, false, "/w/homes/ana", "TERMTOKEN")); g != w {
			t.Errorf("%s: env differs from a never-opted-in twin's:\n%s\nwant\n%s", name, g, w)
		}
		for _, e := range got {
			if strings.Contains(e, "xbin-deploy") || strings.Contains(e, "/checkpoints/") || strings.HasPrefix(e, "XBIN_DEPLOYMENT=") {
				t.Errorf("%s: env carries %q", name, e)
			}
		}
	}
	two("a zero-state tile (no hook wired)", &Manager{Root: r.root, Listen: r.m.Listen, Env: r.m.Env}, "apps/crm")
	r.asked = nil
	two("a session on another tile while apps/crm has a record", r.m, "apps/other")
	if len(r.asked) != 1 || r.asked[0] != "apps/other" {
		t.Errorf("the hook was asked about %q, want only apps/other", r.asked)
	}
	// Opting out removes the record; the store, the view repository and the
	// materialized trees may stay on disk, inert. Only the record counts.
	r.records["apps/crm"] = false
	tk := util.TileKey("apps/crm")
	for _, d := range []string{"data/checkpoints/" + tk + ".git/refs/heads/deploy", "data/checkpoints/" + tk + ".view.git/refs/heads/deploy",
		"data/deployments/" + tk, ".xbin/deploy/" + tk} {
		if err := os.MkdirAll(filepath.Join(r.root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	two("a tile that opted out, its store kept", r.m, "apps/crm")

	// No token (a code-only terminal) or no URL: no git rewrite, so no remote.
	r.records["apps/crm"] = true
	if got := gitConfigEnv(r.m.sandboxEnv("apps/crm", false, "/w/homes/ana", "")); len(got) != 0 {
		t.Errorf("a code-only session on a record-bearing tile got %q", got)
	}
	bare := &Manager{Root: r.root, Listen: r.m.Listen, HasDeploymentRecord: r.m.HasDeploymentRecord}
	if got := gitConfigEnv(bare.sandboxEnv("apps/crm", false, "/w/homes/ana", "TERMTOKEN")); len(got) != 0 {
		t.Errorf("a session without XBIN_URL on a record-bearing tile got %q", got)
	}
	// A root session names no tile: never the remote, and the hook isn't asked.
	r.asked = nil
	r.records[""] = true
	if got := gitConfigEnv(r.m.sandboxEnv("", false, "/w/homes/ana", "TERMTOKEN")); zsEnvLines(got) != zsEnvLines(today(url)) {
		t.Errorf("a root session: %q, want today's two pairs", got)
	}
	if len(r.asked) != 0 {
		t.Errorf("a root session asked the hook about %q", r.asked)
	}

	// Host shells (isolation off) have no GIT_CONFIG pairs today, and gain none.
	for _, o := range []openOpts{
		{cwd: "apps/crm", homeKey: "ana", userID: "ana", api: true},
		{cwd: "apps/crm", homeKey: "ana", userID: "ana", api: true, kind: KindAgent},
	} {
		dir, rel, homeDir, token, revoke, err := r.m.prepare(o)
		if err != nil {
			t.Fatal(err)
		}
		_, cleanup, _, _, env, err := r.m.shellCmd(dir, rel, homeDir, token, o)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		revoke()
		for _, e := range env {
			if strings.HasPrefix(e, "GIT_CONFIG_") && !inProcessEnv(e) || strings.Contains(e, "xbin-deploy") {
				t.Errorf("a host shell (%s) got %q", o.kind, e)
			}
		}
	}

	t.Run("git", func(t *testing.T) { fetchThroughInjectedRemote(t, "apps/crm") })
	t.Run("git, a tile path that needs escaping", func(t *testing.T) { fetchThroughInjectedRemote(t, "lab/a b#1%2") })
}

// inProcessEnv reports whether e comes from the test process's own env (a
// host shell inherits it), so a developer's GIT_CONFIG_* doesn't fail it.
func inProcessEnv(e string) bool {
	for _, p := range os.Environ() {
		if p == e {
			return true
		}
	}
	return false
}

// fetchThroughInjectedRemote runs real git with a record-bearing session's
// env against a dumb-HTTP server standing in for the checkpoint remote: the
// remote is known to git only through the env, the fetch rides today's
// insteadOf rewrite and bearer, `deploy/main` resolves afterwards and
// `git checkout -b hotfix deploy/main` works verbatim. The fetch writes
// nothing to the tile's .git/config.
func fetchThroughInjectedRemote(t *testing.T, tile string) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	quiet := []string{"HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LANG=C"}
	run := func(env []string, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	setup := append([]string{"PATH=" + os.Getenv("PATH"),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}, quiet...)

	// The view repository: deploy/main points at a parentless git-view commit.
	src, view := filepath.Join(tmp, "src"), filepath.Join(tmp, "view.git")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	run(setup, src, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>pinned</h1>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(setup, src, "add", "-A")
	run(setup, src, "commit", "-q", "-m", "checkpoint c:3f2a1c9 of "+tile+" (git view: ignored files left out)")
	run(setup, tmp, "init", "-q", "--bare", view)
	run(setup, src, "push", "-q", view, "HEAD:refs/heads/deploy/main")
	run(setup, view, "symbolic-ref", "HEAD", "refs/heads/deploy/main")
	run(setup, view, "update-server-info")
	pinned := run(setup, view, "rev-parse", "refs/heads/deploy/main")

	// The checkpoint remote, dumb HTTP from the view repository, bearer only.
	prefix := "/api/xbin/checkpoints/" + tile + ".git/"
	var (
		mu    sync.Mutex
		paths []string
		bad   []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.URL.Path)
		if req.Header.Get("Authorization") != "Bearer TERMTOKEN" {
			bad = append(bad, req.URL.Path)
		}
		mu.Unlock()
		rest, ok := strings.CutPrefix(req.URL.Path, prefix)
		if !ok || req.Header.Get("Authorization") != "Bearer TERMTOKEN" || strings.Contains(rest, "..") {
			http.NotFound(w, req)
			return
		}
		b, err := os.ReadFile(filepath.Join(view, filepath.FromSlash(rest)))
		if err != nil {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	// The tile's own repository, with its own history and config.
	r := newRemoteRig(t, srv.URL, tile)
	r.records[tile] = true
	work := filepath.Join(r.root, filepath.FromSlash(tile))
	run(setup, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "app.txt"), []byte("work tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(setup, work, "add", "-A")
	run(setup, work, "commit", "-q", "-m", "work")
	cfgPath := filepath.Join(work, ".git", "config")
	cfg0, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	session := append(r.m.sandboxEnv(tile, false, home, "TERMTOKEN"), quiet...)
	zero := append((&Manager{Root: r.root, Listen: r.m.Listen, Env: r.m.Env}).sandboxEnv(tile, false, home, "TERMTOKEN"), quiet...)
	if got := run(zero, work, "remote"); got != "" {
		t.Fatalf("a zero-state session sees remotes %q", got)
	}
	if got := run(session, work, "remote"); got != "xbin-deploy" {
		t.Fatalf("git remote = %q, want xbin-deploy", got)
	}
	run(session, work, "fetch", "-q", "xbin-deploy")
	if got := run(session, work, "rev-parse", "deploy/main"); got != pinned {
		t.Fatalf("deploy/main = %s, want the pinned view %s", got, pinned)
	}
	if cfg, err := os.ReadFile(cfgPath); err != nil || !bytes.Equal(cfg, cfg0) {
		t.Fatalf("the fetch wrote the tile's .git/config:\n%s\nwas\n%s", cfg, cfg0)
	}
	if out, err := exec.Command(gitBin, "config", "--file", cfgPath, "--get-regexp", `^remote\.`).CombinedOutput(); err == nil {
		t.Fatalf("the tile's .git/config names a remote: %s", out)
	}
	// Flow H, verbatim. (git itself records the new branch's upstream here,
	// by its branch.autoSetupMerge default; xbind writes nothing.)
	run(session, work, "checkout", "-q", "-b", "hotfix", "deploy/main")
	if b, err := os.ReadFile(filepath.Join(work, "index.html")); err != nil || string(b) != "<h1>pinned</h1>\n" {
		t.Fatalf("hotfix's work tree: %q, %v", b, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bad) > 0 {
		t.Errorf("requests without the session's bearer: %q", bad)
	}
	sort.Strings(paths)
	if i := sort.SearchStrings(paths, prefix+"info/refs"); i == len(paths) || paths[i] != prefix+"info/refs" {
		t.Errorf("the fetch never asked for %sinfo/refs: %q", prefix, paths)
	}
}

// covers P15 T11 — the deployment state (the record and the per-deployment
// registration files under data/deployments, the checkpoint store and the
// view repository under data/checkpoints, the materialized trees under
// .xbin/deploy) is out of every terminal's reach. A component terminal —
// an admin's, a restricted user's on the deny-list plan, an agent's — has
// data/ and .xbin/ covered by sealed masks, and no bind re-exposes anything
// beneath them; a restricted terminal on the allow-list view (D40) sees a
// staged root with no data/ at all and an empty .xbin/. No component can
// live there either (both names are reserved), so no readable or own
// component bind can land on them.
func TestTerminalMasksDeploymentState(t *testing.T) {
	root := t.TempDir()
	tk := util.TileKey("apps/crm")
	tree := strings.Repeat("3f2a1c9e", 5)
	state := []string{
		"data/deployments/" + tk + ".json",
		"data/deployments/" + tk + "/dev/cron.json",
		"data/checkpoints/" + tk + ".git/config",
		"data/checkpoints/" + tk + ".git/refs/xbin/log/main",
		"data/checkpoints/" + tk + ".view.git/info/refs",
		".xbin/deploy/" + tk + "/" + tree + "/index.html",
		".xbin/deploy/" + tk + "/d/dev/backend.log",
	}
	for _, p := range append(state, "apps/crm/index.html", "apps/shop/admin/index.html", "apps/other/x", "apps/secret/x", "homes/ana/.bashrc") {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(root, "homes", "ana")
	sdk := filepath.Join(t.TempDir(), "sdk")
	extra := []sandbox.Bind{{Src: sdk, Dst: sdk, RO: true}}
	under := func(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }
	secret := []string{filepath.Join(root, "data"), filepath.Join(root, ".xbin")}

	// effective is the bind that decides what the sandbox shows at p: binds
	// mount ancestors first (sandbox.sortBinds: stable by depth), so the
	// deepest covering one wins, the later one on a tie.
	effective := func(binds []sandbox.Bind, p string) (sandbox.Bind, bool) {
		best, depth, found := sandbox.Bind{}, -1, false
		for _, b := range binds {
			if d := strings.Count(filepath.Clean(b.Dst), "/"); under(p, filepath.Clean(b.Dst)) && d >= depth {
				best, depth, found = b, d, true
			}
		}
		return best, found
	}

	for _, rel := range []string{"apps/crm", "apps/shop/admin", "apps/other"} {
		for _, hide := range [][]string{nil, {"apps/secret"}} {
			for _, kind := range []string{KindShell, KindAgent} {
				binds := scopedBinds(root, rel, home, extra, hide)
				if kind == KindAgent { // sandboxShell's one addition for an agent session
					binds = append(binds, sandbox.Bind{Src: bxBinOr("/opt/xbin/bin/bx"), Dst: agentHostPath, RO: true})
				}
				name := rel + " " + kind + " hide=" + strings.Join(hide, ",")
				for _, p := range state {
					abs := filepath.Join(root, filepath.FromSlash(p))
					b, ok := effective(binds, abs)
					if !ok || !b.Mask || !b.RO {
						t.Errorf("%s: %s is shown by %+v, want a sealed mask", name, p, b)
					}
				}
				for _, b := range binds {
					for _, s := range secret {
						if b.Src != "" && under(b.Src, s) || !b.Mask && under(filepath.Clean(b.Dst), s) {
							t.Errorf("%s: bind %+v re-exposes %s", name, b, s)
						}
					}
				}
			}
		}
	}

	// The restricted allow-list view: the staged root replaces the workspace
	// root; its data/ doesn't exist and its .xbin/ is an empty mountpoint.
	m := &Manager{Root: root}
	readable := []string{"apps/crm", "apps/shop/admin", "apps/other"}
	rootFiles := map[string][]byte{"xbin.json": []byte("{}\n"), "AGENTS.md": []byte("# a\n")}
	for _, rel := range []string{"apps/crm", "apps/other"} {
		viewDir, err := m.stageView(rel, "ana", readable, rootFiles)
		if err != nil {
			t.Fatal(err)
		}
		binds := scopedBindsView(root, rel, home, viewDir, readable, extra)
		for _, p := range state {
			abs := filepath.Join(root, filepath.FromSlash(p))
			b, ok := effective(binds, abs)
			if !ok || b.Src != viewDir || b.Dst != root || !b.RO {
				t.Errorf("view %s: %s is shown by %+v, want the staged view", rel, p, b)
				continue
			}
			if _, err := os.Lstat(filepath.Join(viewDir, filepath.FromSlash(p))); !os.IsNotExist(err) {
				t.Errorf("view %s: the staged view holds %s (%v)", rel, p, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(viewDir, "data")); !os.IsNotExist(err) {
			t.Errorf("view %s: the staged view has a data/ (%v)", rel, err)
		}
		if ents, err := os.ReadDir(filepath.Join(viewDir, ".xbin")); err != nil || len(ents) != 0 {
			t.Errorf("view %s: the staged .xbin/ is %v (%v), want an empty mountpoint", rel, ents, err)
		}
		for _, b := range binds {
			for _, s := range secret {
				if b.Src != viewDir && (under(b.Src, s) || under(filepath.Clean(b.Dst), s)) {
					t.Errorf("view %s: bind %+v lands in %s", rel, b, s)
				}
			}
		}
		_ = os.RemoveAll(viewDir)
	}

	for _, p := range []string{"data/deployments", "data/checkpoints", ".xbin/deploy", "data", ".xbin"} {
		if util.ComponentPathOK(p) {
			t.Errorf("%s is an acceptable component path; a tile's own bind could land on deployment state", p)
		}
	}
}

// bxBinOr is TestMain's bx build, or def when the build didn't run.
func bxBinOr(def string) string {
	if bxBin != "" {
		return bxBin
	}
	return def
}
