package checkpoint

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

// viewGit runs git on tile's view repository (reading it in a test is no
// D78 matter).
func viewGit(t *testing.T, s *Store, tilePath string, args ...string) string {
	t.Helper()
	return hostGit(t, s.ViewDir(tilePath), append([]string{"--git-dir=" + s.ViewDir(tilePath)}, args...)...)
}

// pinnedTile is a tile with a store holding two checkpoints whose git views
// differ from them (ignored files), and a deploy log.
func pinnedTile(t *testing.T, s *Store, path string) (src Source, c1, c2 Result) {
	t.Helper()
	src = tile(t, s, path, map[string]string{
		".gitignore":          "node_modules/\n.env\n",
		"app.js":              "one\n",
		".env":                "SECRET=1\n",
		"node_modules/x/i.js": "dep\n",
	})
	repo(t, src.WorkTree)
	c1 = capture(t, s, src, true)
	if err := os.WriteFile(filepath.Join(src.WorkTree, "app.js"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.WorkTree, ".env"), []byte("SECRET=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 = capture(t, s, src, false)
	for i, e := range []LogEntry{
		{ID: 1, Deployment: "main", How: "pause", Checkpoint: c1.Hash, Feed: FeedWorkTree, By: "user:ana", Result: LogOK},
		{ID: 2, Deployment: "dev", How: "deploy", Checkpoint: c2.Hash, Feed: FeedWorkTree, By: "user:ana", Result: LogFailed, Error: "boom"},
	} {
		e.FinishedAt = logAt.Add(time.Duration(i) * time.Second)
		if err := s.AppendLog(context.Background(), path, e); err != nil {
			t.Fatal(err)
		}
	}
	return src, c1, c2
}

// objectsIn lists every object a repository holds, loose or packed.
func objectsIn(t *testing.T, gitDir string) []string {
	t.Helper()
	out := hostGit(t, gitDir, "--git-dir="+gitDir, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	objs := strings.Fields(out)
	sort.Strings(objs)
	return objs
}

// covers D119g T15 T20 — the view repository holds exactly
// refs/heads/deploy/<name> for each pinned deployment, pointing at its
// checkpoint's git view, and HEAD naming refs/heads/deploy/<primary>,
// dangling while the primary follows the work tree; no refs/xbin/* ref, and
// every object in it reachable from those refs, so no other checkpoint, no
// full checkpoint tree (no ignored file) and no deploy-log commit is
// fetchable (TestFetchRemoteNeverServesDeployLog is this case). update-server-
// info ran after each change (info/refs lists exactly the refs); an unpinned
// deployment's objects go with its ref; deleting the repository and
// refreshing rebuilds the same refs; it is removed with the record, the
// store staying; a tile without a store never gets one.
func TestViewRepoHoldsOnlyPinnedViews(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	ctx := context.Background()
	_, c1, c2 := pinnedTile(t, s, "apps/crm")
	view := s.ViewDir("apps/crm")
	viewOf := func(tree string) string {
		return strings.TrimSpace(storeGit(t, s, "apps/crm", "rev-parse", "refs/xbin/views/"+tree))
	}

	check := func(p Pins) {
		t.Helper()
		refs := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(viewGit(t, s, "apps/crm", "for-each-ref", "--format=%(refname) %(objectname)")), "\n") {
			if name, id, ok := strings.Cut(line, " "); ok {
				refs[name] = id
			}
		}
		want := map[string]string{}
		for name, tree := range p.Trees {
			want["refs/heads/deploy/"+name] = viewOf(tree)
		}
		if len(refs) != len(want) {
			t.Errorf("the view repository's refs: %v, want %v", refs, want)
		}
		for name, id := range want {
			if refs[name] != id {
				t.Errorf("%s: %q, want the git view %s", name, refs[name], id)
			}
		}
		if head := strings.TrimSpace(viewGit(t, s, "apps/crm", "symbolic-ref", "HEAD")); head != "refs/heads/deploy/"+p.Primary {
			t.Errorf("HEAD: %q, want refs/heads/deploy/%s", head, p.Primary)
		}
		// info/refs is update-server-info's, and matches the refs
		b, err := os.ReadFile(filepath.Join(view, "info", "refs"))
		if err != nil {
			t.Fatalf("no info/refs: update-server-info didn't run: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(want) == 0 && strings.TrimSpace(string(b)) != "" || len(want) > 0 && len(lines) != len(want) {
			t.Errorf("info/refs: %q, want %d refs", b, len(want))
		}
		for _, l := range lines {
			if id, name, ok := strings.Cut(l, "\t"); ok && want[name] != id {
				t.Errorf("info/refs advertises %s %s", name, id)
			}
		}
		if !exists(filepath.Join(view, "objects", "info", "packs")) {
			t.Error("no objects/info/packs")
		}
		// every object is reachable from the refs: nothing else to fetch
		var reach []string
		if len(want) > 0 {
			for _, l := range strings.Split(strings.TrimSpace(viewGit(t, s, "apps/crm", "rev-list", "--objects", "--all")), "\n") {
				reach = append(reach, strings.Fields(l)[0])
			}
		}
		sort.Strings(reach)
		if objs := objectsIn(t, view); strings.Join(objs, " ") != strings.Join(reach, " ") {
			t.Errorf("the view repository holds %d objects, %d reachable from its refs", len(objs), len(reach))
		}
		// no store object that isn't a pinned view's: no checkpoint tree, no log
		for _, tree := range []string{c1.Hash, c2.Hash} {
			if strings.Contains(strings.Join(objectsIn(t, view), " "), tree) {
				t.Errorf("the full checkpoint tree %s is in the view repository", tree)
			}
		}
		for _, logRef := range []string{"refs/xbin/log/main", "refs/xbin/log/dev"} {
			id := strings.TrimSpace(storeGit(t, s, "apps/crm", "rev-parse", logRef))
			if strings.Contains(strings.Join(objectsIn(t, view), " "), id) {
				t.Errorf("the deploy-log commit %s (%s) is in the view repository", id, logRef)
			}
		}
		for rel, body := range map[string]string{"config": viewConfig} {
			if b, err := os.ReadFile(filepath.Join(view, rel)); err != nil || string(b) != body {
				t.Errorf("the view repository's %s: %q (%v)", rel, b, err)
			}
		}
		for _, rel := range []string{"hooks", "description", "info/exclude", "objects/info/alternates", "FETCH_HEAD"} {
			if exists(filepath.Join(view, rel)) {
				t.Errorf("the view repository has %s", rel)
			}
		}
		for _, d := range []string{s.viewTmp("apps/crm"), s.viewOld("apps/crm")} {
			if exists(d) {
				t.Errorf("a refresh left %s behind", d)
			}
		}
	}

	// main and dev pinned
	both := Pins{Primary: "main", Trees: map[string]string{"main": c1.Hash, "dev": c2.Hash}}
	before := rec.count()
	if err := s.RefreshView(ctx, "apps/crm", both); err != nil {
		t.Fatal(err)
	}
	check(both)
	// the git views hold no ignored file
	for _, name := range []string{"main", "dev"} {
		files := strings.Fields(viewGit(t, s, "apps/crm", "ls-tree", "-r", "--name-only", "refs/heads/deploy/"+name))
		if strings.Join(files, " ") != ".gitignore app.js" {
			t.Errorf("deploy/%s holds %q, want the git view: .gitignore app.js", name, files)
		}
	}
	// the refresh is one confined run: the fresh repository read-write, the store read-only
	var runs int
	for _, c := range rec.cmds[before:] {
		if strings.Contains(c.Argv[2], "update-server-info") {
			runs++
			if c.Dir != s.viewTmp("apps/crm") || c.ReadOnlyDir || len(c.Binds) != 1 || c.Binds[0] != confine.RO(s.Dir("apps/crm")) || c.Net != confine.NetNone {
				t.Errorf("the refresh run: Dir %s ro %v binds %+v net %v", c.Dir, c.ReadOnlyDir, c.Binds, c.Net)
			}
			if body := strings.TrimPrefix(c.Argv[2], preamble); bareGit.MatchString(body) {
				t.Errorf("the refresh runs git without the hardened prefix:\n%s", body)
			}
		}
	}
	if runs != 1 {
		t.Errorf("%d refresh runs", runs)
	}

	// dev unpinned (it follows the work tree again): its ref and objects go
	onlyMain := Pins{Primary: "main", Trees: map[string]string{"main": c1.Hash}}
	if err := s.RefreshView(ctx, "apps/crm", onlyMain); err != nil {
		t.Fatal(err)
	}
	check(onlyMain)
	if strings.Contains(strings.Join(objectsIn(t, view), " "), viewOf(c2.Hash)) {
		t.Error("dev's git view stayed fetchable after it was unpinned")
	}

	// the primary follows the work tree: HEAD dangles
	primaryFollows := Pins{Primary: "main", Trees: map[string]string{"dev": c2.Hash}}
	if err := s.RefreshView(ctx, "apps/crm", primaryFollows); err != nil {
		t.Fatal(err)
	}
	check(primaryFollows)
	if _, err := os.Lstat(filepath.Join(view, "refs", "heads", "deploy", "main")); err == nil {
		t.Error("deploy/main exists while main follows the work tree")
	}
	none := Pins{Primary: "main"}
	if err := s.RefreshView(ctx, "apps/crm", none); err != nil {
		t.Fatal(err)
	}
	check(none)

	// derived: deleted and refreshed, the same refs
	if err := s.RefreshView(ctx, "apps/crm", both); err != nil {
		t.Fatal(err)
	}
	first := viewGit(t, s, "apps/crm", "for-each-ref")
	if err := os.RemoveAll(view); err != nil {
		t.Fatal(err)
	}
	// a killed refresh's leftovers don't matter
	for _, d := range []string{s.viewTmp("apps/crm"), s.viewOld("apps/crm")} {
		if err := os.MkdirAll(filepath.Join(d, "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RefreshView(ctx, "apps/crm", both); err != nil {
		t.Fatal(err)
	}
	check(both)
	if again := viewGit(t, s, "apps/crm", "for-each-ref"); again != first {
		t.Errorf("rebuilt refs %q, want %q", again, first)
	}

	// refusals: bad names and ids never reach git; an unknown checkpoint is refused
	for _, p := range []Pins{
		{Primary: "Main"},
		{Primary: "main", Trees: map[string]string{"../x": c1.Hash}},
		{Primary: "main", Trees: map[string]string{"dev": "c:" + c1.Hash[:7]}},
		{Primary: "main", Trees: map[string]string{"dev": "HEAD"}},
	} {
		if err := s.RefreshView(ctx, "apps/crm", p); err == nil {
			t.Errorf("RefreshView(%+v) passed", p)
		}
	}
	if err := s.RefreshView(ctx, "apps/crm", Pins{Primary: "main", Trees: map[string]string{"dev": strings.Repeat("ab", 20)}}); !errors.Is(err, ErrUnknownCheckpoint) {
		t.Errorf("a pin on a tree the store doesn't hold: %v", err)
	}
	check(both) // refused refreshes left it as it was

	// removed with the record; the store and its log stay
	if err := s.RemoveView(ctx, "apps/crm"); err != nil {
		t.Fatal(err)
	}
	if exists(view) || !s.Exists("apps/crm") {
		t.Errorf("after RemoveView: view %v, store %v", exists(view), s.Exists("apps/crm"))
	}
	if got, _, _ := s.Log(ctx, "apps/crm", LogQuery{}); len(got) != 2 {
		t.Errorf("RemoveView touched the deploy log: %d entries", len(got))
	}

	// no store: no view repository
	if err := s.RefreshView(ctx, "apps/none", none); !errors.Is(err, ErrNoStore) {
		t.Errorf("RefreshView without a store: %v", err)
	}
	if exists(s.ViewDir("apps/none")) || s.Exists("apps/none") {
		t.Error("a view refresh created state for a tile without a store")
	}
}

// fetchReq serves one request of tile's remote through ServeFetch.
func fetchReq(s *Store, method, tile, rel string) (*httptest.ResponseRecorder, error) {
	w := httptest.NewRecorder()
	err := s.ServeFetch(w, httptest.NewRequest(method, "/api/xbin/checkpoints/"+tile+".git/"+rel+"?service=git-upload-pack", nil), tile, rel)
	return w, err
}

// covers D119g T15 — the remote serves the view repository, never the store,
// from the allow-list only: HEAD, info/refs, objects/info/packs, packs and
// their indexes, loose objects, byte for byte, with git's content types; GET
// and HEAD only; everything else — config, hooks/, description, packed-refs,
// refs/, objects/info/alternates and http-alternates, a pack's .rev or .keep,
// any path that climbs, and every store file — is ErrNotFetchable with
// nothing written. remote.go never names http.ServeFile, ServeContent or
// FileServer (the template remote's opener).
func TestFetchRemoteServesOnlyGitFiles(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	ctx := context.Background()
	_, c1, c2 := pinnedTile(t, s, "apps/crm")
	if err := s.RefreshView(ctx, "apps/crm", Pins{Primary: "main", Trees: map[string]string{"main": c1.Hash, "dev": c2.Hash}}); err != nil {
		t.Fatal(err)
	}
	view := s.ViewDir("apps/crm")
	// a loose object beside the pack, as a dumb client may ask for either
	loose := strings.TrimSpace(hostGit(t, view, "--git-dir="+view, "hash-object", "-w", "--stdin", "--no-filters"))
	packs, err := filepath.Glob(filepath.Join(view, "objects", "pack", "pack-*.pack"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("the view repository's packs: %v %v", packs, err)
	}
	pack := strings.TrimPrefix(packs[0], view+"/")
	idx := strings.TrimSuffix(pack, ".pack") + ".idx"

	served := map[string]string{
		"HEAD":                                   "text/plain; charset=utf-8",
		"info/refs":                              "text/plain; charset=utf-8",
		"objects/info/packs":                     "text/plain; charset=utf-8",
		pack:                                     "application/x-git-packed-objects",
		idx:                                      "application/x-git-packed-objects-toc",
		"objects/" + loose[:2] + "/" + loose[2:]: "application/x-git-loose-object",
	}
	for rel, typ := range served {
		w, err := fetchReq(s, http.MethodGet, "apps/crm", rel)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		disk, _ := os.ReadFile(filepath.Join(view, rel))
		if w.Code != 200 || w.Body.String() != string(disk) || w.Header().Get("Content-Type") != typ || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %d %q, %d bytes (disk %d)", rel, w.Code, w.Header().Get("Content-Type"), w.Body.Len(), len(disk))
		}
		if w, err := fetchReq(s, http.MethodHead, "apps/crm", rel); err != nil || w.Code != 200 || w.Body.Len() != 0 {
			t.Errorf("HEAD %s: %d, %d bytes (%v)", rel, w.Code, w.Body.Len(), err)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, "PROPFIND", http.MethodDelete} {
		if w, err := fetchReq(s, m, "apps/crm", "info/refs"); !errors.Is(err, ErrNotFetchable) || w.Body.Len() != 0 || len(w.Header()) != 0 {
			t.Errorf("%s info/refs: %v, %d bytes written", m, err, w.Body.Len())
		}
	}

	// files that exist in the view repository or the store, never served
	_ = os.WriteFile(filepath.Join(view, "description"), []byte("secret\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(view, "hooks"), 0o755)
	_ = os.WriteFile(filepath.Join(view, "hooks", "post-update"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(view, "objects", "info", "alternates"), []byte(s.Dir("apps/crm")+"/objects\n"), 0o644)
	_ = os.WriteFile(filepath.Join(view, "objects", "info", "http-alternates"), []byte("../../x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(view, strings.TrimSuffix(pack, ".pack")+".keep"), []byte("k\n"), 0o644)
	logCommit := strings.TrimSpace(storeGit(t, s, "apps/crm", "rev-parse", "refs/xbin/log/main"))
	for _, rel := range []string{
		"config", "description", "hooks/post-update", "packed-refs", "refs/heads/deploy/main", "info/exclude",
		"objects/info/alternates", "objects/info/http-alternates", strings.TrimSuffix(pack, ".pack") + ".rev",
		strings.TrimSuffix(pack, ".pack") + ".keep", "objects/pack", "objects", "", ".", "/HEAD", "../HEAD",
		"objects/../HEAD", "objects/info/../../config", "info//refs", "info/refs/", "./HEAD",
		"objects/" + logCommit[:2] + "/" + logCommit[2:] + "/", "objects/" + strings.ToUpper(loose[:2]) + "/" + loose[2:],
		"../" + filepath.Base(s.Dir("apps/crm")) + "/config", "index", "FETCH_HEAD",
	} {
		w, err := fetchReq(s, http.MethodGet, "apps/crm", rel)
		if !errors.Is(err, ErrNotFetchable) || w.Body.Len() != 0 || w.Code != 200 || len(w.Header()) != 0 {
			t.Errorf("%q: served (%v, %d bytes, headers %v)", rel, err, w.Body.Len(), w.Header())
		}
	}
	// the store is never the source: a deploy-log commit or a full checkpoint
	// tree is in the store, not in the view repository
	if _, err := fetchReq(s, http.MethodGet, "apps/crm", "objects/"+logCommit[:2]+"/"+logCommit[2:]); !errors.Is(err, ErrNotFetchable) {
		t.Errorf("a deploy-log commit was served: %v", err)
	}
	if _, err := fetchReq(s, http.MethodGet, "apps/crm", "objects/"+c1.Hash[:2]+"/"+c1.Hash[2:]); !errors.Is(err, ErrNotFetchable) {
		t.Errorf("a full checkpoint tree was served: %v", err)
	}
	// another tile, a tile without a view repository, a bad tile path
	for _, tile := range []string{"apps/other", "../apps/crm", "/apps/crm", ""} {
		if _, err := fetchReq(s, http.MethodGet, tile, "HEAD"); !errors.Is(err, ErrNotFetchable) {
			t.Errorf("tile %q: %v", tile, err)
		}
	}
	if err := s.RemoveView(ctx, "apps/crm"); err != nil {
		t.Fatal(err)
	}
	if _, err := fetchReq(s, http.MethodGet, "apps/crm", "HEAD"); !errors.Is(err, ErrNotFetchable) {
		t.Errorf("HEAD of a removed view repository: %v", err)
	}

	// the opener: never http.ServeFile and friends
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "remote.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "http" {
				switch sel.Sel.Name {
				case "ServeFile", "ServeContent", "FileServer", "ServeFileFS", "FileServerFS", "Dir", "FS":
					t.Errorf("remote.go:%d uses http.%s", fset.Position(sel.Pos()).Line, sel.Sel.Name)
				}
			}
		}
		return true
	})
}

// covers D119g T15 NP-06-18 — every symlink in the view repository is refused,
// whatever it points at and wherever it sits: info/refs to the config, HEAD
// to a FIFO outside (never opened: the tripwire stays quiet and nothing
// blocks), a loose object to /etc/passwd, objects/pack as a symlinked
// directory, the objects directory itself, and a link that stays inside the
// repository and names an allow-listed file.
func TestFetchRemoteRefusesSymlinks(t *testing.T) {
	needGit(t)
	s, _ := testStore(t)
	ctx := context.Background()
	_, c1, _ := pinnedTile(t, s, "apps/crm")
	if err := s.RefreshView(ctx, "apps/crm", Pins{Primary: "main", Trees: map[string]string{"main": c1.Hash}}); err != nil {
		t.Fatal(err)
	}
	view := s.ViewDir("apps/crm")
	w := newTripwire(t)
	packs, _ := filepath.Glob(filepath.Join(view, "objects", "pack", "pack-*.pack"))
	if len(packs) != 1 {
		t.Fatalf("packs: %v", packs)
	}
	pack := strings.TrimPrefix(packs[0], view+"/")
	fake := strings.Repeat("ab", 20)
	link := func(rel, target string) {
		t.Helper()
		p := filepath.Join(view, filepath.FromSlash(rel))
		_ = os.RemoveAll(p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	refuse := func(rel string) {
		t.Helper()
		done := make(chan struct{})
		var err error
		var rw *httptest.ResponseRecorder
		go func() { rw, err = fetchReq(s, http.MethodGet, "apps/crm", rel); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: serving blocked (a FIFO opened)", rel)
		}
		if !errors.Is(err, ErrNotFetchable) || rw.Body.Len() != 0 {
			t.Errorf("%s: served through a symlink (%v, %d bytes)", rel, err, rw.Body.Len())
		}
	}

	link("info/refs", "../config")
	refuse("info/refs")
	link("HEAD", w.path)
	refuse("HEAD")
	link("objects/"+fake[:2]+"/"+fake[2:], "/etc/passwd")
	refuse("objects/" + fake[:2] + "/" + fake[2:])
	link("objects/info/packs", "../../info/refs") // inside, and names an allow-listed file
	refuse("objects/info/packs")

	// a symlinked directory on the way
	real := filepath.Join(t.TempDir(), "pack")
	if err := os.Rename(filepath.Join(view, "objects", "pack"), real); err != nil {
		t.Fatal(err)
	}
	link("objects/pack", real)
	refuse(pack)
	link("objects/pack", "../objects-elsewhere")
	refuse(pack)

	// a FIFO planted in place of a file is refused too, without blocking
	fifo := filepath.Join(view, "objects", "info", "packs")
	_ = os.Remove(fifo)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	refuse("objects/info/packs")
	if w.tripped.Load() {
		t.Fatal("serving opened the FIFO outside the view repository")
	}
}

// covers T15 — the route's {rest...} splits at the last ".git" segment whose
// prefix is a tile with a record, since a tile path may hold ".git"-suffixed
// segments; the git path must be on the allow-list.
func TestSplitFetchPath(t *testing.T) {
	has := map[string]bool{"apps/crm": true, "a.git/b": true, "a": true}
	hasRecord := func(tile string) bool { return has[tile] }
	for rest, want := range map[string][2]string{
		"apps/crm.git/info/refs":         {"apps/crm", "info/refs"},
		"apps/crm.git/HEAD":              {"apps/crm", "HEAD"},
		"a.git/b.git/objects/info/packs": {"a.git/b", "objects/info/packs"},
		"a.git/HEAD":                     {"a", "HEAD"},
		"apps/crm.git/config":            {},
		"apps/crm.git/":                  {},
		"apps/crm.git":                   {},
		"apps/other.git/HEAD":            {},
		".git/HEAD":                      {},
		"apps/crm.git/../crm.git/HEAD":   {},
		"../apps/crm.git/HEAD":           {},
	} {
		tile, rel, ok := SplitFetchPath(rest, hasRecord)
		if ok != (want[0] != "") || tile != want[0] || rel != want[1] {
			t.Errorf("SplitFetchPath(%q) = %q %q %v, want %q %q", rest, tile, rel, ok, want[0], want[1])
		}
	}
}
