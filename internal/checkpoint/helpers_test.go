package checkpoint

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
)

// needGit skips a unit-git test on a host without git or GNU find (the
// stores' confined runs use both; direct mode runs the host's).
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host")
	}
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !bytes.Contains(out, []byte("GNU")) {
		t.Skip("no GNU find on this host")
	}
	if confine.Isolated() {
		t.Fatal("confinement is on in a direct-mode test")
	}
}

// recorder records every confined run a Store makes, then makes it.
type recorder struct {
	mu   sync.Mutex
	cmds []confine.Cmd
}

func (r *recorder) run(ctx context.Context, c confine.Cmd) (confine.Result, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, c)
	r.mu.Unlock()
	return confine.Run(ctx, c)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cmds)
}

// scripts are the recorded runs' script bodies (after the preamble).
func (r *recorder) scripts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.cmds {
		if len(c.Argv) > 2 {
			out = append(out, strings.TrimPrefix(c.Argv[2], preamble))
		}
	}
	return out
}

// testStore is a Store over a fresh workspace, recording its runs.
func testStore(t *testing.T) (*Store, *recorder) {
	t.Helper()
	s := New(filepath.Join(t.TempDir(), "ws"))
	rec := &recorder{}
	s.run = rec.run
	return s, rec
}

// tile writes files (path → content; a trailing "/" makes a directory) into
// tile's directory under s.Root and returns its Source.
func tile(t *testing.T, s *Store, path string, files map[string]string) Source {
	t.Helper()
	dir := filepath.Join(s.Root, filepath.FromSlash(path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Source{Tile: path, WorkTree: dir}
}

// repo makes dir a git repository with one commit of what it holds, and
// returns the commit (host git, as the tile's own sessions would).
func repo(t *testing.T, dir string, add ...string) string {
	t.Helper()
	if len(add) == 0 {
		add = []string{"-A"}
	}
	hostGit(t, dir, "init", "-q", "-b", "main")
	hostGit(t, dir, append([]string{"add"}, add...)...)
	hostGit(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "first")
	return strings.TrimSpace(hostGit(t, dir, "rev-parse", "HEAD"))
}

// hostGit runs git in dir with no global or system config.
func hostGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// storeGit runs git on tile's store (reading it in a test is no D78 matter).
func storeGit(t *testing.T, s *Store, tilePath string, args ...string) string {
	t.Helper()
	return hostGit(t, s.Dir(tilePath), append([]string{"--git-dir=" + s.Dir(tilePath)}, args...)...)
}

type entry struct {
	mode string
	body string
}

// treeOf lists a tree of tile's store recursively: path → mode and content.
func treeOf(t *testing.T, s *Store, tilePath, tree string) map[string]entry {
	t.Helper()
	out := map[string]entry{}
	for _, rec := range strings.Split(storeGit(t, s, tilePath, "ls-tree", "-r", "-z", tree), "\x00") {
		head, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(head)
		body := ""
		if f[0] != "160000" {
			body = storeGit(t, s, tilePath, "cat-file", "blob", f[2])
		}
		out[path] = entry{mode: f[0], body: body}
	}
	return out
}

func capture(t *testing.T, s *Store, src Source, create bool) Result {
	t.Helper()
	res, err := s.Capture(context.Background(), CaptureRequest{Source: src, By: "user:ana", Create: create})
	if err != nil {
		t.Fatalf("capture of %s: %v", src.Tile, err)
	}
	return res
}

// looseObjects counts the store's loose objects.
func looseObjects(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(dir, "objects"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && len(filepath.Base(filepath.Dir(p))) == 2 {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// gitWrapper puts a git first on PATH that logs each invocation's argv and
// GIT_*/LC_ALL environment into the returned file, then runs the real git.
// Direct-mode runs inherit xbind's PATH, so every git a store script runs
// goes through it.
func gitWrapper(t *testing.T) (log string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "git.log")
	script := "#!/bin/sh\n{ for a in \"$@\"; do printf 'A%s\\000' \"$a\"; done; env | grep -E '^(GIT_|LC_ALL=)' | while IFS= read -r e; do printf 'E%s\\000' \"$e\"; done; printf '\\001\\n'; } >>" +
		shellQuote(log) + "\nexec " + shellQuote(real) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

type gitCall struct {
	args []string
	env  map[string]string
}

// gitCalls parses gitWrapper's log.
func gitCalls(t *testing.T, log string) []gitCall {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out []gitCall
	for _, inv := range strings.Split(string(b), "\x01\n") {
		if inv == "" {
			continue
		}
		c := gitCall{env: map[string]string{}}
		for _, f := range strings.Split(strings.TrimSuffix(inv, "\x00"), "\x00") {
			switch {
			case strings.HasPrefix(f, "A"):
				c.args = append(c.args, f[1:])
			case strings.HasPrefix(f, "E"):
				k, v, _ := strings.Cut(f[1:], "=")
				c.env[k] = v
			}
		}
		out = append(out, c)
	}
	return out
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
