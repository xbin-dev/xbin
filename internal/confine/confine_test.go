package confine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "first"}} {
		if _, err := Git(context.Background(), dir, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return dir
}

// Direct mode (isolation off): git runs with the hardened environment — the
// daemon's own global config never reaches a repo — and a failure carries
// git's words and exit code.
func TestGitDirect(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if Isolated() {
		t.Fatal("confinement is on in a unit test")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[alias]\n\tlog = nope\n[core]\n\tpager = false\n"), 0o644)
	t.Setenv("GIT_DIR", "/nonexistent") // the daemon's GIT_* never steer a tool
	dir := testRepo(t)
	out, err := GitRead(context.Background(), dir, "log", "--format=%s")
	if err != nil || strings.TrimSpace(out) != "first" {
		t.Fatalf("log: %q %v", out, err)
	}
	_, err = GitRead(context.Background(), dir, "rev-parse", "--verify", "-q", "nope^{commit}")
	if code, ok := ExitCode(err); !ok || code != 1 {
		t.Fatalf("a failing git: %v", err)
	}
	_, err = Run(context.Background(), Cmd{Argv: []string{"sleep", "5"}, Timeout: 100 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}
}
