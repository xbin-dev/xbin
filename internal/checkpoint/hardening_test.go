package checkpoint

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
)

// bareGit finds a git invocation in a script body that doesn't go through
// hg (the hardened prefix): "git" as a command word.
var bareGit = regexp.MustCompile(`(^|[\s;|&(!])git(\s|$)`)

// covers P16 T1 — every tool run of the store goes through confine (the
// Store's run hook, a recorder here, in direct mode): git only through the
// hardened prefix, with the store as its repository, never -C and never the
// tile's .git; the store bound read-write and the work tree read-only, no
// network; and the tile's own git config never runs.
func TestStoreUsesConfineOnly(t *testing.T) {
	needGit(t)
	s, rec := testStore(t)
	src := tile(t, s, "apps/cfg", map[string]string{"a.txt": "a\n", "sub/comp/x": "nested\n"})
	src.Nested = []string{"sub/comp"}
	repo(t, src.WorkTree)
	marker := filepath.Join(t.TempDir(), "PWNED")
	evil := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"core.fsmonitor", evil}, {"core.hooksPath", filepath.Dir(evil)}, {"filter.x.clean", evil}, {"diff.external", evil}} {
		hostGit(t, src.WorkTree, "config", kv[0], kv[1])
	}
	if err := os.WriteFile(filepath.Join(src.WorkTree, ".gitattributes"), []byte("* filter=x diff=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := gitWrapper(t) // from here on, every git is the store's
	res := capture(t, s, src, true)
	if err := os.WriteFile(filepath.Join(src.WorkTree, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	capture(t, s, src, false)
	if _, err := s.Estimate(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	s.forget("apps/cfg")
	if _, err := s.Resolve(context.Background(), "apps/cfg", res.ID); err != nil {
		t.Fatal(err)
	}

	store := s.Dir("apps/cfg")
	if rec.count() < 5 {
		t.Fatalf("only %d confined runs recorded", rec.count())
	}
	for i, c := range rec.cmds {
		if len(c.Argv) < 4 || c.Argv[0] != "sh" || c.Argv[1] != "-c" || !strings.HasPrefix(c.Argv[2], preamble) {
			t.Errorf("run %d isn't a store script: %q", i, c.Argv)
			continue
		}
		if body := strings.TrimPrefix(c.Argv[2], preamble); bareGit.MatchString(body) {
			t.Errorf("run %d runs git without the hardened prefix:\n%s", i, body)
		}
		if c.Net != confine.NetNone {
			t.Errorf("run %d has network %v", i, c.Net)
		}
		switch {
		case c.Dir == store && !c.ReadOnlyDir: // init, capture, record
		case c.Dir == store && c.ReadOnlyDir: // listing checkpoints
		case c.Dir == src.WorkTree && c.ReadOnlyDir: // the estimate: nothing writable
			if len(c.Binds) != 0 {
				t.Errorf("the estimate binds more than the work tree: %+v", c.Binds)
			}
			continue
		default:
			t.Errorf("run %d: Dir %s (read-only %v), neither the store nor the read-only work tree", i, c.Dir, c.ReadOnlyDir)
		}
		for _, b := range c.Binds {
			if !(b.Src == src.WorkTree && b.Dst == src.WorkTree && b.RO) {
				t.Errorf("run %d binds %+v: only the work tree, read-only, may join the store", i, b)
			}
		}
	}

	calls := gitCalls(t, log)
	if len(calls) < 10 {
		t.Fatalf("only %d git invocations logged: is the wrapper on PATH?", len(calls))
	}
	for _, c := range calls {
		all := strings.Join(c.args, "\x00")
		if strings.Contains(all, filepath.Join(src.WorkTree, ".git")) {
			t.Errorf("git touched the tile's .git: %q", c.args)
		}
		for _, a := range c.args {
			if a == "-C" || strings.HasPrefix(a, "--git-dir=") && a != "--git-dir="+store {
				t.Errorf("git ran on another repository than the store: %q", c.args)
			}
		}
		if !strings.Contains(all, "--git-dir="+store) && !(strings.Contains(all, "\x00init\x00") && strings.HasSuffix(all, "\x00"+store)) {
			t.Errorf("git ran without the store as its repository: %q", c.args)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the tile's git config ran a command")
	}
	for rel, want := range map[string]string{"config": storeConfig, "info/attributes": storeAttributes} {
		if b, err := os.ReadFile(filepath.Join(store, rel)); err != nil || string(b) != want {
			t.Errorf("the store's %s: %q (%v), want the constant", rel, b, err)
		}
	}
}

// covers T1 — direct mode (isolation off) keeps the hardening: every git
// run on the store carries 06-security T1's -c set and confine's, first on
// its argv, and the pinned environment; the store's config and attributes
// are the constants, and it has no hooks, description or exclude file.
func TestCaptureDirectModeHardening(t *testing.T) {
	needGit(t)
	log := gitWrapper(t)
	s, _ := testStore(t)
	t.Setenv("GIT_DIR", "/nonexistent") // the daemon's own GIT_* never steer a store run
	t.Setenv("HOME", t.TempDir())       // nor its global config
	_ = os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte("[core]\n\tfsmonitor = /bin/false\n[alias]\n\tadd = nope\n"), 0o644)
	src := tile(t, s, "apps/hard", map[string]string{"a.txt": "a\n", ".gitignore": "x\n"})
	capture(t, s, src, true)

	want := make([]string, 0, 2*len(gitConfig))
	for _, kv := range gitConfig {
		want = append(want, "-c", kv)
	}
	calls := gitCalls(t, log)
	if len(calls) < 8 {
		t.Fatalf("only %d git invocations logged", len(calls))
	}
	for _, c := range calls {
		if len(c.args) < len(want) || strings.Join(c.args[:len(want)], "\x00") != strings.Join(want, "\x00") {
			t.Errorf("git ran without the pinned -c set first: %q", c.args)
		}
		for _, kv := range gitEnv {
			k, v, _ := strings.Cut(kv, "=")
			if c.env[k] != v {
				t.Errorf("git ran with %s=%q, want %q (%q)", k, c.env[k], v, c.args)
			}
		}
		if _, ok := c.env["GIT_DIR"]; ok {
			t.Errorf("the daemon's GIT_DIR reached a store run: %q", c.args)
		}
	}
	for _, want := range []string{
		"core.attributesFile=/dev/null", "core.excludesFile=/dev/null", "core.autocrlf=false", "core.symlinks=true",
		"core.fileMode=true", "core.ignoreCase=false", "core.precomposeUnicode=false", "core.protectNTFS=true",
		"core.protectHFS=true", "core.quotePath=false", "gc.auto=0", "submodule.recurse=false",
		"transfer.fsckObjects=true", "protocol.allow=never", // 06-security T1.3
	} {
		if !contains(gitConfig, want) {
			t.Errorf("the pinned set lacks %s", want)
		}
	}
	// confine's own flags and environment (internal/confine/git.go), read
	// from its source, so a flag added there can't be missed here
	flags, env := confineGitLists(t)
	for i := 0; i+1 < len(flags); i += 2 {
		if flags[i] == "-c" && !contains(gitConfig, flags[i+1]) {
			t.Errorf("confine pins %s; a store run doesn't", flags[i+1])
		}
	}
	for _, kv := range env {
		if !contains(gitEnv, kv) {
			t.Errorf("confine sets %s; a store run doesn't", kv)
		}
	}

	store := s.Dir("apps/hard")
	for rel, want := range map[string]string{"config": storeConfig, "info/attributes": storeAttributes, "HEAD": "ref: refs/heads/deploy/main\n"} {
		if b, err := os.ReadFile(filepath.Join(store, rel)); err != nil || string(b) != want {
			t.Errorf("the store's %s: %q (%v), want %q", rel, b, err, want)
		}
	}
	for _, rel := range []string{"hooks", "description", "info/exclude", "objects/info/alternates"} {
		if exists(filepath.Join(store, rel)) {
			t.Errorf("the store has %s", rel)
		}
	}
	// a store whose files something changed is pinned back on the next capture
	_ = os.WriteFile(filepath.Join(store, "config"), []byte("[core]\n\tbare = true\n\tfsmonitor = /bin/false\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(store, "hooks"), 0o755)
	_ = os.WriteFile(filepath.Join(store, "info", "exclude"), []byte("*\n"), 0o644)
	capture(t, s, src, false)
	if b, _ := os.ReadFile(filepath.Join(store, "config")); string(b) != storeConfig || exists(filepath.Join(store, "hooks")) || exists(filepath.Join(store, "info", "exclude")) {
		t.Errorf("the store's pinned files weren't restored: config %q", b)
	}
}

// covers T1 — tile-controlled names never reach argv: files named like
// options and pathspec magic are captured exactly, and no git or sh argv of
// any run carries them (pathspecs travel in files).
func TestCapturePathNamesNeverArgv(t *testing.T) {
	needGit(t)
	log := gitWrapper(t)
	s, rec := testStore(t)
	names := map[string]string{
		"--output=pwned":     "1\n",
		"-cpwn.x=y":          "2\n",
		":(glob)*pwn":        "3\n",
		"--exec=pwn":         "4\n",
		"dir/--git-dir=pwn":  "5\n",
		"pwn dir/-n":         "6\n",
		":(exclude)pwn/keep": "7\n",
		"pwncomp/x":          "nested\n",
	}
	src := tile(t, s, "apps/argv", names)
	src.Nested = []string{"pwncomp"}
	res := capture(t, s, src, true)
	calls := gitCalls(t, log)
	got := treeOf(t, s, "apps/argv", res.Hash)
	for name, body := range names {
		if name == "pwncomp/x" {
			if _, ok := got[name]; ok {
				t.Errorf("the nested component was captured")
			}
			continue
		}
		if got[name] != (entry{"100644", body}) {
			t.Errorf("%q: %+v, want %q", name, got[name], body)
		}
	}
	for _, c := range calls {
		for _, a := range c.args {
			if strings.Contains(a, "pwn") {
				t.Errorf("a tile string reached git's argv: %q in %q", a, c.args)
			}
		}
	}
	for _, c := range rec.cmds {
		for _, a := range c.Argv {
			if strings.Contains(a, "pwn") {
				t.Errorf("a tile string reached a run's argv: %q", a)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// confineGitLists reads gitFlags and gitEnv from internal/confine/git.go.
func confineGitLists(t *testing.T) (flags, env []string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "confine", "git.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	lists := map[string]*[]string{"gitFlags": &flags, "gitEnv": &env}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 || lists[vs.Names[0].Name] == nil {
			return true
		}
		cl, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, e := range cl.Elts {
			if bl, ok := e.(*ast.BasicLit); ok {
				s, _ := strconv.Unquote(bl.Value)
				*lists[vs.Names[0].Name] = append(*lists[vs.Names[0].Name], s)
			}
		}
		return true
	})
	if len(flags) == 0 || len(env) == 0 {
		t.Fatal("couldn't read confine's gitFlags and gitEnv: the parser needs updating")
	}
	return flags, env
}
