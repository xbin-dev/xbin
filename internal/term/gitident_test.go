package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// covers D248 — a sandboxed session carries its person's fallback address
// for git (EMAIL): a commit — a template's merge, a tile agent's git am —
// works where git can make none up, and an identity the person set wins
// over it (EMAIL is git's last resort).
func TestGitIdentityIsAFallback(t *testing.T) {
	if got := gitIdentityEnv("ana"); !slices.Equal(got, []string{"EMAIL=ana@xbin.invalid"}) {
		t.Fatalf("ana: %q", got)
	}
	for _, bad := range []string{"", "a b", "a@b", "a<b>"} {
		if got := gitIdentityEnv(bad); got != nil {
			t.Errorf("%q: %q", bad, got)
		}
	}
	m := &Manager{Root: t.TempDir()}
	if env := m.sessionEnv("apps/x", false, "/w/homes/ana", "TOK", openOpts{homeKey: "ana"}); !slices.Contains(env, "EMAIL=ana@xbin.invalid") {
		t.Errorf("the session's env: %q", env)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	home := t.TempDir()
	// the sandbox's user has a name (root's, in the rootfs's passwd); this
	// host's may not: a system file gives it one, the lowest of git's scopes
	sys := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(sys, []byte("[user]\n\tname = root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ident := func() string {
		t.Helper()
		cmd := exec.Command("git", "var", "GIT_COMMITTER_IDENT")
		cmd.Env = []string{"HOME=" + home, "EMAIL=ana@xbin.invalid", "GIT_CONFIG_SYSTEM=" + sys, "PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git var: %v %s", err, out)
		}
		return string(out)
	}
	if got := ident(); !strings.Contains(got, "<ana@xbin.invalid>") {
		t.Errorf("no identity set: %q", got)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = Ana R\n\temail = ana@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ident(); !strings.Contains(got, "Ana R <ana@example.com>") {
		t.Errorf("the person's own identity must win: %q", got)
	}
}
