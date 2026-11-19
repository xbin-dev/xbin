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
	if got := gitIdentityEnv("ana", nil); !slices.Equal(got, []string{"EMAIL=ana@xbin.invalid"}) {
		t.Fatalf("ana: %q", got)
	}
	if got := gitIdentityEnv("", nil); got != nil {
		t.Errorf("no person: %q", got)
	}
	// the review of 2026-11-20, (e): an id is escaped, never stripped — no
	// two ids share an address; and an EMAIL the session has already wins
	seen := map[string]string{}
	for _, id := range []string{"a.b", "a_b", "a=2Eb", "a@b", "a b", "a<b>", "a=b", "ab", "a=40b", "A.b"} {
		got := gitIdentityEnv(id, nil)
		if len(got) != 1 || !strings.HasSuffix(got[0], "@xbin.invalid") || strings.ContainsAny(strings.TrimSuffix(strings.TrimPrefix(got[0], "EMAIL="), "@xbin.invalid"), " <>@.") {
			t.Errorf("%q: %q", id, got)
			continue
		}
		if other, dup := seen[got[0]]; dup {
			t.Errorf("%q and %q share %s", id, other, got[0])
		}
		seen[got[0]] = id
	}
	if got := gitIdentityEnv("ana", []string{"EMAIL=ana@example.com"}); got != nil {
		t.Errorf("the session's own EMAIL: %q", got)
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
