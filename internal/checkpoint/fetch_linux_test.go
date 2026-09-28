//go:build linux && integration

// Run with: go test -tags=integration ./internal/checkpoint/
// Needs user namespaces and an unpacked rootfs with git (XBIN_TEST_ROOTFS, or
// the repo's .rootfs from `make rootfs`); skips otherwise.
package checkpoint

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

// fetchServer is a stand-in for the checkpoint route (its gate is the
// plane's, WP-15's TestFetchRemoteReadGate): GET only, one bearer, the path
// split by SplitFetchPath over the tiles with a record, then ServeFetch; any
// refusal is a 404 or a 403, as the route answers.
func fetchServer(t *testing.T, s *Store, token string, withRecord ...string) *httptest.Server {
	t.Helper()
	has := map[string]bool{}
	for _, tile := range withRecord {
		has[tile] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/xbin/checkpoints/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		tile, rel, ok := SplitFetchPath(r.PathValue("rest"), func(tile string) bool { return has[tile] })
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := s.ServeFetch(w, r, tile, rel); err != nil {
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// sessionGit runs a git client confined, as a terminal session would: the
// tile's directory read-write, the host's network (to reach the test
// server's loopback), and the session's GIT_CONFIG_* pairs.
func sessionGit(t *testing.T, dir string, env []string, script string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := confine.Run(ctx, confine.Cmd{
		Argv: []string{"sh", "-c", "set -e\n" + script},
		Dir:  dir,
		Net:  confine.NetHost,
		Env: append([]string{"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
			"GIT_AUTHOR_NAME=ana", "GIT_AUTHOR_EMAIL=ana@x", "GIT_COMMITTER_NAME=ana", "GIT_COMMITTER_EMAIL=ana@x"}, env...),
	})
	return string(res.Stdout) + string(res.Stderr), err
}

// sessionEnv is the terminal's four GIT_CONFIG_* pairs for tile (the
// terminal manager's sandboxenv: today's two, then the xbin-deploy remote).
func sessionEnv(srv, token, tile string) []string {
	return []string{
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=url." + srv + "/.insteadOf", "GIT_CONFIG_VALUE_0=http://xbin/",
		"GIT_CONFIG_KEY_1=http." + srv + "/.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: Bearer " + token,
		"GIT_CONFIG_KEY_2=remote.xbin-deploy.url", "GIT_CONFIG_VALUE_2=http://xbin/api/xbin/checkpoints/" + tile + ".git",
		"GIT_CONFIG_KEY_3=remote.xbin-deploy.fetch", "GIT_CONFIG_VALUE_3=+refs/heads/deploy/*:refs/deploy/*",
	}
}

// covers P16 T15, flow H — flow H literally, confined: main is pinned (its
// checkpoint taken from a clean work tree with gitignored files), exp is
// pinned to a later checkpoint, dev follows the work tree. A confined git
// client with the terminal's four GIT_CONFIG_* pairs runs `git fetch
// xbin-deploy` and gets only refs/deploy/main and refs/deploy/exp; puts the
// unfinished work on a branch; `git checkout -b hotfix deploy/main` gives
// main's git view — the branch carries no ignored file, which stays in the
// work tree untouched; a fix is committed, and the unfinished branch is
// moved onto it with `git rebase --onto` from the Xbin-Work-Tree-Head the
// view commit names. A push fails (GET-only dumb HTTP, no receive-pack) and
// leaves the view repository as it was. Without the bearer, and for another
// tile or a tile without a record, the fetch fails.
func TestCheckpointFetchRemoteFlowH(t *testing.T) {
	needGit(t) // host git builds the fixture
	s, _ := testStore(t)
	ctx := context.Background()
	src := tile(t, s, "apps/crm", map[string]string{
		".gitignore":          "node_modules/\n.env\n",
		"app.js":              "one\n",
		"lib/util.js":         "util\n",
		".env":                "SECRET=1\n",
		"node_modules/x/i.js": "dep\n",
	})
	head := repo(t, src.WorkTree)
	other := tile(t, s, "apps/other", map[string]string{"o.txt": "o\n"})

	confined(t)
	main := capture(t, s, src, true) // pause live reload: main pinned
	if err := os.WriteFile(filepath.Join(src.WorkTree, "app.js"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exp := capture(t, s, src, false) // exp pinned to the unfinished work
	capture(t, s, other, true)
	for i, e := range []LogEntry{
		{ID: 1, Deployment: "main", How: "pause", Checkpoint: main.Hash, Feed: FeedWorkTree, By: "user:ana", Result: LogOK},
		{ID: 2, Deployment: "exp", How: "add", Checkpoint: exp.Hash, Feed: FeedWorkTree, By: "user:ana", Result: LogOK},
	} {
		e.FinishedAt = logAt.Add(time.Duration(i) * time.Second)
		if err := s.AppendLog(ctx, "apps/crm", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RefreshView(ctx, "apps/crm", Pins{Primary: "main", Trees: map[string]string{"main": main.Hash, "exp": exp.Hash}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshView(ctx, "apps/other", Pins{Primary: "main", Trees: map[string]string{"main": s.mustList(t, "apps/other")}}); err != nil {
		t.Fatal(err)
	}
	viewBefore := snapshot(t, s.ViewDir("apps/crm"))

	const token = "tok-session"
	srv := fetchServer(t, s, token, "apps/crm", "apps/none") // apps/other has a view repository but no record; apps/none the reverse
	env := sessionEnv(srv.URL, token, "apps/crm")

	out, err := sessionGit(t, src.WorkTree, env, `git fetch -q xbin-deploy
git for-each-ref --format='%(refname)' refs/deploy/ refs/xbin/ refs/remotes/
`)
	if err != nil {
		t.Fatalf("git fetch xbin-deploy: %v\n%s", err, out)
	}
	if refs := strings.Fields(out); strings.Join(refs, " ") != "refs/deploy/exp refs/deploy/main" {
		t.Fatalf("the fetch brought %q, want only the pinned deployments' refs/deploy/exp and refs/deploy/main", refs)
	}

	out, err = sessionGit(t, src.WorkTree, env, `git checkout -q -b wip
git commit -q -am 'unfinished'
git checkout -q -b hotfix deploy/main
git ls-tree -r --name-only hotfix
echo ---
cat app.js
echo ---
git log -1 --format=%B deploy/main
`)
	if err != nil {
		t.Fatalf("flow H's branch: %v\n%s", err, out)
	}
	parts := strings.Split(out, "---\n")
	if len(parts) != 3 {
		t.Fatalf("flow H printed %q", out)
	}
	files := strings.Fields(parts[0])
	sort.Strings(files)
	if strings.Join(files, " ") != ".gitignore app.js lib/util.js" {
		t.Errorf("the hotfix branch holds %v: a branch from deploy/main carries no ignored file", files)
	}
	if parts[1] != "one\n" {
		t.Errorf("the hotfix branch's app.js: %q, want main's", parts[1])
	}
	if !strings.Contains(parts[2], "Xbin-Work-Tree-Head: "+head) || !strings.Contains(parts[2], "Xbin-Checkpoint: "+main.Hash) {
		t.Errorf("deploy/main's message doesn't name the work-tree head %s:\n%s", head, parts[2])
	}
	for _, rel := range []string{".env", "node_modules/x/i.js"} {
		if !exists(filepath.Join(src.WorkTree, filepath.FromSlash(rel))) {
			t.Errorf("the checkout removed the ignored %s from the work tree", rel)
		}
	}

	out, err = sessionGit(t, src.WorkTree, env, `echo fix >fix.txt
git add fix.txt
git commit -q -m fix
git rebase -q --onto hotfix `+head+` wip
git ls-tree -r --name-only wip
cat app.js
`)
	if err != nil {
		t.Fatalf("flow H's rebase --onto: %v\n%s", err, out)
	}
	if !strings.Contains(out, "fix.txt") || !strings.HasSuffix(out, "two\n") {
		t.Errorf("after the rebase the unfinished branch holds %q: want the fix and the unfinished app.js", out)
	}

	// nothing is ever pushed
	out, err = sessionGit(t, src.WorkTree, env, `git push xbin-deploy hotfix:refs/heads/deploy/main`)
	if err == nil {
		t.Errorf("a push through the checkpoint remote succeeded:\n%s", out)
	}
	sameSnapshot(t, "the view repository after a push", viewBefore, snapshot(t, s.ViewDir("apps/crm")))

	// refusals, beside a control that the same fetch from a fresh
	// repository works
	fetchInto := func(e []string) (string, error) {
		return sessionGit(t, t.TempDir(), e, "git init -q\ngit fetch -q xbin-deploy\ngit for-each-ref --format='%(refname)'\n")
	}
	if out, err := fetchInto(env); err != nil || strings.Join(strings.Fields(out), " ") != "refs/deploy/exp refs/deploy/main" {
		t.Fatalf("the control fetch: %v\n%s", err, out)
	}
	for what, e := range map[string][]string{
		"no bearer":                        sessionEnv(srv.URL, "wrong", "apps/crm"),
		"a tile without a record":          sessionEnv(srv.URL, token, "apps/other"),
		"a tile without a view repository": sessionEnv(srv.URL, token, "apps/none"),
		"a path climbing out of the tile":  sessionEnv(srv.URL, token, "apps/crm.git/../other"),
	} {
		if out, err := fetchInto(e); err == nil {
			t.Errorf("%s: the fetch succeeded\n%s", what, out)
		}
	}
}

// mustList is the full tree id of tile's only checkpoint.
func (s *Store) mustList(t *testing.T, tile string) string {
	t.Helper()
	list, err := s.List(context.Background(), tile)
	if err != nil || len(list) != 1 {
		t.Fatalf("%s's checkpoints: %v %v", tile, list, err)
	}
	return list[0].Hash
}

// covers T1 T15 P16 — the view repository stays private under confinement,
// built from a hostile tile's checkpoints: git's files only (HEAD, config,
// refs, objects, info/refs, objects/info/packs), the constant config, no
// hooks, description, exclude file or alternates; its objects pass git fsck
// --strict; no command the tile names ran. The deploy log, the diff and the
// drift count run in their sandboxes too — the drift count's scratch
// directory bound read-write inside the read-only store — with the same
// answers as in direct mode.
func TestViewRepoPrivateConfined(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "PWNED")
	src, _ := hostileTile(t, s, "apps/hostile", marker)

	confined(t)
	c1 := capture(t, s, src, true)
	if err := os.WriteFile(filepath.Join(src.WorkTree, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := capture(t, s, src, false)
	if err := s.AppendLog(ctx, "apps/hostile", LogEntry{ID: 1, Deployment: "main", How: "deploy", Checkpoint: c2.Hash, Previous: c1.Hash,
		Feed: FeedWorkTree, By: "user:ana", FinishedAt: logAt, Result: LogOK}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.Log(ctx, "apps/hostile", LogQuery{}); err != nil || len(got) != 1 || got[0].Checkpoint != c2.Hash {
		t.Fatalf("the confined log: %+v (%v)", got, err)
	}
	if err := s.RefreshView(ctx, "apps/hostile", Pins{Primary: "main", Trees: map[string]string{"main": c2.Hash, "dev": c1.Hash}}); err != nil {
		t.Fatal(err)
	}
	view := s.ViewDir("apps/hostile")
	top, err := os.ReadDir(view)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"HEAD": true, "config": true, "info": true, "objects": true, "refs": true, "packed-refs": true}
	for _, e := range top {
		if !allowed[e.Name()] {
			t.Errorf("the view repository holds %s", e.Name())
		}
	}
	if info, _ := os.ReadDir(filepath.Join(view, "info")); len(info) != 1 || info[0].Name() != "refs" {
		t.Errorf("the view repository's info/: %v, want refs alone", info)
	}
	if b, _ := os.ReadFile(filepath.Join(view, "config")); string(b) != viewConfig {
		t.Errorf("the view repository's config: %q", b)
	}
	for _, rel := range []string{"hooks", "description", "info/exclude", "objects/info/alternates", "objects/info/http-alternates"} {
		if exists(filepath.Join(view, rel)) {
			t.Errorf("the view repository has %s", rel)
		}
	}
	for _, line := range strings.Split(viewGit(t, s, "apps/hostile", "fsck", "--strict", "--no-dangling"), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "notice:") {
			t.Errorf("git fsck --strict: %s", line)
		}
	}

	res, err := s.Diff(ctx, DiffRequest{Source: src, From: DiffSide{Tree: c1.Hash}, To: DiffSide{Tree: c2.Hash}, Stat: true})
	if err != nil || len(res.Files) != 1 || res.Files[0].Path != "a.txt" {
		t.Errorf("the confined diff: %+v (%v)", res.Files, err)
	}
	if err := os.WriteFile(filepath.Join(src.WorkTree, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeBefore := snapshot(t, s.Dir("apps/hostile"))
	if n, err := s.Drift(ctx, src, c1.Hash); err != nil || n != 2 {
		t.Errorf("the confined drift count: %d (%v), want 2 (a.txt, new.txt)", n, err)
	}
	sameSnapshot(t, "the store after a confined drift count", storeBefore, snapshot(t, s.Dir("apps/hostile")))
	if _, err := os.Stat(marker); err == nil || exists(filepath.Join(s.Dir("apps/hostile"), "PWNED")) || exists(filepath.Join(view, "PWNED")) {
		t.Fatal("a command ran")
	}
}
