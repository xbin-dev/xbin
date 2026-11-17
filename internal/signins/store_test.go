package signins

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/vault"
)

// Test credentials: obvious fakes in the shapes the catalog knows.
const (
	fakeOAT  = "sk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE-test-0001"
	fakeOAT2 = "sk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE-test-0002"
	fakeKey  = "sk-ant-api03-FAKEFAKEFAKEFAKE-test-key1"
	fakeOAI  = "sk-proj-FAKEFAKEFAKEFAKEFAKE-test-oai1"
)

// fakeVault seals by reversing the bytes behind the label: enough to tell
// a sealed file from a plain one and one label's blob from another's.
type fakeVault struct{ sealed, uninit bool }

func (v *fakeVault) Initialized() bool { return !v.uninit }
func (v *fakeVault) Sealed() bool      { return v.sealed }
func (v *fakeVault) EncryptFor(label string, pt []byte) ([]byte, error) {
	out := []byte(label + "\x00")
	for i := len(pt) - 1; i >= 0; i-- {
		out = append(out, pt[i])
	}
	return out, nil
}
func (v *fakeVault) DecryptFor(label string, blob []byte) ([]byte, error) {
	rest, ok := bytes.CutPrefix(blob, []byte(label+"\x00"))
	if !ok {
		return nil, errors.New("wrong key")
	}
	out := make([]byte, 0, len(rest))
	for i := len(rest) - 1; i >= 0; i-- {
		out = append(out, rest[i])
	}
	return out, nil
}

type world struct {
	s         *Store
	dir       string
	vault     *fakeVault
	now       time.Time
	people    map[string]int64 // id → account stamp (absent: gone or disabled)
	host      *bool
	hostTrust *bool
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{dir: filepath.Join(t.TempDir(), "signins"), vault: &fakeVault{}, now: time.UnixMilli(1_800_000_000_000),
		people: map[string]int64{"alice": 1, "bob": 2}}
	w.s = New(Options{Dir: w.dir, Vault: w.vault,
		Person: func(id string) (int64, bool) { st, ok := w.people[id]; return st, ok },
		Host: func() (bool, bool) {
			if w.host == nil {
				return false, false
			}
			return true, *w.host
		},
		HostTrust: func() (bool, bool) {
			if w.hostTrust == nil {
				return false, false
			}
			return true, *w.hostTrust
		},
		Now: func() time.Time { return w.now }})
	return w
}

func save(t *testing.T, w *world, user, harness, name, secret string, def bool) Signin {
	t.Helper()
	sg, _, err := w.s.Save(user, harness, name, secret, def)
	if err != nil {
		t.Fatalf("Save %s/%s: %v", harness, name, err)
	}
	return sg
}

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// everything the store wrote, to prove no secret is on disk in the clear
func diskHolds(t *testing.T, dir, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			found = found || bytes.Contains(b, []byte(needle))
		}
		return nil
	})
	return found
}

func TestSaveListDefaultsAndNames(t *testing.T) {
	w := newWorld(t)
	a := save(t, w, "alice", "claude", "", fakeOAT, false)
	if a.Name != "Claude Code" || a.Kind != KindSetupToken || a.Env != "CLAUDE_CODE_OAUTH_TOKEN" || !a.Default ||
		a.ExpiresAt-a.CreatedAt != SetupTokenLife.Milliseconds() {
		t.Fatalf("first: %+v", a)
	}
	w.now = w.now.Add(time.Minute)
	b := save(t, w, "alice", "claude", "", fakeKey, false)
	if b.Name != "Claude Code 2" || b.Kind != KindAPIKey || b.Env != "ANTHROPIC_API_KEY" || b.Default || b.ExpiresAt != 0 {
		t.Fatalf("second: %+v", b)
	}
	c := save(t, w, "alice", "codex", "Work", fakeOAI, false)
	if c.Env != "CODEX_API_KEY" || !c.Default {
		t.Fatalf("codex: %+v", c)
	}
	all, _, err := w.s.List("alice")
	if err != nil || len(all) != 3 || all[0].ID != b.ID || all[1].ID != a.ID || all[2].ID != c.ID {
		t.Fatalf("List: %+v %v", all, err)
	}
	if other, _, _ := w.s.List("bob"); len(other) != 0 {
		t.Fatalf("bob sees %+v", other)
	}
	// sealed on disk: no secret in any file, and 0600
	for _, sec := range []string{fakeOAT, fakeKey, fakeOAI} {
		if diskHolds(t, w.dir, sec) {
			t.Fatal("a secret is on disk in the clear")
		}
	}
	if fi, _ := os.Stat(w.s.path("alice")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// rename, a taken name, make default
	nm := "Company"
	if got, _, err := w.s.Update("alice", b.ID, Patch{Name: &nm}); err != nil || got.Name != "Company" {
		t.Fatalf("rename: %+v %v", got, err)
	}
	taken := "claude code"
	if _, _, err := w.s.Update("alice", b.ID, Patch{Name: &taken}); status(err) != 409 {
		t.Fatalf("a taken name: %v", err)
	}
	yes, no := true, false
	if _, _, err := w.s.Update("alice", b.ID, Patch{Default: &no}); status(err) != 400 {
		t.Fatalf("default false: %v", err)
	}
	if got, _, err := w.s.Update("alice", b.ID, Patch{Default: &yes}); err != nil || !got.Default {
		t.Fatalf("make default: %+v %v", got, err)
	}
	if p, _ := w.s.Peek("alice", "claude", ""); p == nil || p.ID != b.ID {
		t.Fatalf("the default now: %+v", p)
	}
	if _, _, err := w.s.Update("bob", b.ID, Patch{Default: &yes}); status(err) != 404 {
		t.Fatalf("another person's id: %v", err)
	}

	// the same name again replaces the secret and keeps the id
	again, replaced, err := w.s.Save("alice", "claude", "company", fakeOAT2, false)
	if err != nil || !replaced || again.ID != b.ID || again.Kind != KindSetupToken || !again.Default {
		t.Fatalf("re-save: %+v %v %v", again, replaced, err)
	}

	// removing the default hands it to the newest left
	if _, err := w.s.Remove("alice", b.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.s.Peek("alice", "claude", ""); p == nil || p.ID != a.ID || !p.Default {
		t.Fatalf("after removing the default: %+v", p)
	}
	if _, err := w.s.Remove("alice", b.ID); status(err) != 404 {
		t.Fatalf("removed twice: %v", err)
	}
}

func TestSaveRefusals(t *testing.T) {
	w := newWorld(t)
	for _, c := range []struct {
		user, harness, name, secret string
		want                        int
	}{
		{"alice", "nope", "", fakeOAT, 400},
		{"alice", "claude", "", "two words", 400},
		{"alice", "claude", "", `quo"te`, 400},
		{"alice", "claude", "", "", 400},
		{"alice", "claude", strings.Repeat("n", NameMax+1), fakeOAT, 400},
		{"carol", "claude", "", fakeOAT, 403}, // no such account
		{"", "claude", "", fakeOAT, 403},
	} {
		if _, _, err := w.s.Save(c.user, c.harness, c.name, c.secret, false); status(err) != c.want {
			t.Errorf("Save(%q, %q, name %d, secret %d): %v, want %d", c.user, c.harness, len(c.name), len(c.secret), err, c.want)
		}
	}
	for i := 0; i < MaxPerHarness; i++ {
		save(t, w, "alice", "claude", "", fakeOAT, false)
	}
	if _, _, err := w.s.Save("alice", "claude", "", fakeOAT, false); status(err) != 409 {
		t.Fatalf("past the limit: %v", err)
	}
}

func TestUse(t *testing.T) {
	w := newWorld(t)
	if c, err := w.s.Use("alice", "claude", "", UseFor{}); c != nil || err != nil {
		t.Fatalf("none saved: %+v %v", c, err)
	}
	a := save(t, w, "alice", "claude", "Personal", fakeOAT, false)
	b := save(t, w, "alice", "claude", "Work", fakeKey, false)
	w.now = w.now.Add(time.Hour)
	c, err := w.s.Use("alice", "claude", "", UseFor{Tile: "apps/crm", Session: "s1", Origin: "person"})
	if err != nil || c == nil || c.Secret != fakeOAT || c.ID != a.ID || c.Env != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("the default: %+v %v", c != nil, err)
	}
	if c, err := w.s.Use("alice", "claude", b.ID, UseFor{Tile: "apps/x", Session: "s2", Origin: "agent"}); err != nil || c.Secret != fakeKey {
		t.Fatalf("by id: %v", err)
	}
	if c, err := w.s.Use("bob", "claude", a.ID, UseFor{}); c != nil || err != nil {
		t.Fatalf("another person's id: %+v %v", c, err)
	}
	if c, err := w.s.Use("alice", "codex", "", UseFor{}); c != nil || err != nil {
		t.Fatalf("another harness: %+v %v", c, err)
	}
	uses := w.s.Uses("alice")
	if len(uses) != 2 || uses[0].Session != "s2" || uses[0].Origin != "agent" || uses[1].Tile != "apps/crm" || uses[1].Name != "Personal" {
		t.Fatalf("uses: %+v", uses)
	}
	if all, _, _ := w.s.List("alice"); all[1].LastUsedAt != w.now.UnixMilli() {
		t.Fatalf("lastUsedAt: %+v", all)
	}
	if diskHolds(t, w.dir, fakeOAT) || diskHolds(t, w.dir, fakeKey) {
		t.Fatal("a use left a secret on disk")
	}

	// expired
	w.now = w.now.Add(SetupTokenLife)
	if _, err := w.s.Use("alice", "claude", "", UseFor{}); status(err) != 409 || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired: %v", err)
	}
	// the switch off
	w.now = w.now.Add(-SetupTokenLife)
	if _, err := w.s.SetPolicy(false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Use("alice", "claude", "", UseFor{}); !errors.Is(err, ErrOff) {
		t.Fatalf("switched off: %v", err)
	}
	if _, _, err := w.s.Save("alice", "claude", "New", fakeOAT, false); !errors.Is(err, ErrOff) {
		t.Fatalf("saving while off: %v", err)
	}
	if _, err := w.s.Remove("alice", b.ID); err != nil { // removing still works
		t.Fatalf("removing while off: %v", err)
	}
	_, _ = w.s.SetPolicy(true)
	// the account disabled
	delete(w.people, "alice")
	if _, err := w.s.Use("alice", "claude", "", UseFor{}); status(err) != 403 {
		t.Fatalf("a disabled account: %v", err)
	}
}

func TestVaultStates(t *testing.T) {
	w := newWorld(t)
	a := save(t, w, "alice", "claude", "Personal", fakeOAT, false)
	w.vault.sealed = true
	if w.s.VaultState() != VaultSealed {
		t.Fatal(w.s.VaultState())
	}
	if all, _, err := w.s.List("alice"); err != nil || len(all) != 1 { // metadata reads while sealed
		t.Fatalf("List while sealed: %v %v", all, err)
	}
	if _, err := w.s.Use("alice", "claude", "", UseFor{}); !errors.Is(err, ErrSealed) {
		t.Fatalf("Use while sealed: %v", err)
	}
	if _, _, err := w.s.Save("alice", "claude", "Other", fakeKey, false); !errors.Is(err, ErrSealed) {
		t.Fatalf("Save while sealed: %v", err)
	}
	if len(w.s.People()) != 1 {
		t.Fatal("the admins' counts while sealed")
	}
	if _, err := w.s.Remove("alice", a.ID); err != nil { // forgetting works while sealed
		t.Fatalf("Remove while sealed: %v", err)
	}
	w.vault.sealed = false
	if c, err := w.s.Use("alice", "claude", a.ID, UseFor{}); c != nil || err != nil {
		t.Fatalf("a sign-in removed while sealed is gone: %+v %v", c, err)
	}

	// no barrier set up: nothing is kept in the clear
	w.vault.uninit = true
	if _, _, err := w.s.Save("alice", "claude", "", fakeOAT, false); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked: %v", err)
	}
	// the plaintext vault mode keeps it in the clear, and seals it on the
	// first read after a barrier exists — a metadata-only read (a list), not
	// only the next save (review finding 7)
	p := New(Options{Dir: w.dir, AllowPlaintext: true, Now: func() time.Time { return w.now }})
	if _, _, err := p.Save("alice", "claude", "", fakeOAT, false); err != nil {
		t.Fatal(err)
	}
	if !diskHolds(t, w.dir, fakeOAT) {
		t.Fatal("plaintext mode didn't keep it in the clear")
	}
	w.vault.uninit = false
	if got, _, err := w.s.List("alice"); err != nil || len(got) != 1 {
		t.Fatalf("list after the barrier appeared: %v %v", got, err)
	}
	if diskHolds(t, w.dir, fakeOAT) {
		t.Fatal("still in the clear after a read with a barrier")
	}
	if c, err := w.s.Use("alice", "claude", "", UseFor{}); err != nil || c.Secret != fakeOAT {
		t.Fatalf("use after it was sealed: %v", err)
	}
	save(t, w, "alice", "claude", "Second", fakeKey, false)
	if diskHolds(t, w.dir, fakeOAT) || diskHolds(t, w.dir, fakeKey) {
		t.Fatal("in the clear after a write with a barrier")
	}
}

// Another vault (set up again): the secrets can't be opened; the sign-ins
// can still be removed.
func TestForeignVault(t *testing.T) {
	w := newWorld(t)
	a := save(t, w, "alice", "claude", "", fakeOAT, false)
	b, _ := os.ReadFile(w.s.path("alice"))
	_ = os.WriteFile(w.s.path("alice"), bytes.Replace(b, []byte(`"data": "`), []byte(`"data": "AAAA`), 1), 0o600)
	if _, err := w.s.Use("alice", "claude", "", UseFor{}); !errors.Is(err, ErrForeignVault) {
		t.Fatalf("Use: %v", err)
	}
	if _, _, err := w.s.Save("alice", "claude", "New", fakeKey, false); !errors.Is(err, ErrForeignVault) {
		t.Fatalf("Save: %v", err)
	}
	if _, err := w.s.Remove("alice", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.s.path("alice")); !os.IsNotExist(err) {
		t.Fatal("the emptied file stayed")
	}
	save(t, w, "alice", "claude", "New", fakeKey, false)
}

func TestLifecycle(t *testing.T) {
	w := newWorld(t)
	a := save(t, w, "alice", "claude", "Personal", fakeOAT, false)
	save(t, w, "alice", "codex", "", fakeOAI, false)
	bobs := save(t, w, "bob", "claude", "", fakeKey, false)
	_, _ = w.s.Use("alice", "claude", "", UseFor{Tile: "apps/a", Session: "s"})

	rows := w.s.People()
	if len(rows) != 2 || rows[0].User != "alice" || rows[0].Total != 2 || rows[1].Signins[0].ID != bobs.ID {
		t.Fatalf("People: %+v", rows)
	}

	// an admin revokes one: gone, and its person is told
	if gone, err := w.s.Revoke("alice", a.ID, "root"); err != nil || gone.ID != a.ID {
		t.Fatalf("Revoke: %v", err)
	}
	if _, n, _ := w.s.List("alice"); n == nil || !strings.Contains(n.Text, "removed by root") || !strings.Contains(n.Text, "Personal") {
		t.Fatalf("the notice: %+v", n)
	}
	w.s.DismissNotice("alice")
	if _, n, _ := w.s.List("alice"); n != nil {
		t.Fatal("dismissed notice stayed")
	}

	// a credential someone else made for the account: all of it goes
	if gone := w.s.Clear("alice", "Removed when root reset your sign-in to this workspace."); len(gone) != 1 {
		t.Fatalf("Clear: %+v", gone)
	}
	all, n, _ := w.s.List("alice")
	if len(all) != 0 || n == nil || !strings.Contains(n.Text, "reset") {
		t.Fatalf("after Clear: %+v %+v", all, n)
	}
	b, _ := os.ReadFile(w.s.path("alice"))
	if bytes.Contains(b, []byte(`"data"`)) || bytes.Contains(b, []byte(`"secrets"`)) {
		t.Fatalf("Clear kept a blob: %s", b)
	}

	// the account deleted: its files go
	w.s.PersonGone("alice")
	for _, p := range []string{w.s.path("alice"), w.s.usePath("alice")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s stayed", p)
		}
	}

	// an account made again under bob's id reads none of bob's
	w.people["bob"] = 99
	if all, _, _ := w.s.List("bob"); len(all) != 0 {
		t.Fatalf("a new account under the id reads the old one's: %+v", all)
	}
	if c, err := w.s.Use("bob", "claude", "", UseFor{}); c != nil || err != nil {
		t.Fatalf("…and uses it: %v %v", c, err)
	}
}

func TestPolicy(t *testing.T) {
	w := newWorld(t)
	if p := w.s.Policy(); !p.Enabled || p.Locked {
		t.Fatalf("default: %+v", p)
	}
	if p, err := w.s.SetPolicy(false); err != nil || p.Enabled {
		t.Fatalf("off: %+v %v", p, err)
	}
	on, off := true, false
	w.host = &on
	if p := w.s.Policy(); !p.Enabled || !p.Locked || p.Reason != HostLockReason {
		t.Fatalf("the host's on: %+v", p)
	}
	if _, err := w.s.SetPolicy(false); status(err) != 409 {
		t.Fatalf("a write past the host: %v", err)
	}
	w.host = &off
	if p := w.s.Policy(); p.Enabled || !p.Locked {
		t.Fatalf("the host's off: %+v", p)
	}
	w.host = nil
	if p := w.s.Policy(); p.Enabled { // the admins' own value is in force again
		t.Fatalf("after the host let go: %+v", p)
	}
}

func TestMarkRefusedMasks(t *testing.T) {
	w := newWorld(t)
	a := save(t, w, "alice", "claude", "", fakeOAT, false)
	w.s.MarkRefused("alice", a.ID, "Authentication required: token "+fakeOAT+" was revoked")
	all, _, _ := w.s.List("alice")
	if all[0].RefusedAt == 0 || strings.Contains(all[0].Refused, "FAKEFAKE") || !strings.Contains(all[0].Refused, "revoked") {
		t.Fatalf("refused: %+v", all[0])
	}
}

// The real barrier: the envelope opens with its own vault, under the
// person's own subkey only.
func TestRealBarrier(t *testing.T) {
	b, err := vault.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Init("a test passphrase, never a real one"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := New(Options{Dir: dir, Vault: b})
	if _, _, err := s.Save("alice", "claude", "", fakeOAT, false); err != nil {
		t.Fatal(err)
	}
	if diskHolds(t, dir, fakeOAT) {
		t.Fatal("in the clear")
	}
	if c, err := s.Use("alice", "claude", "", UseFor{}); err != nil || c.Secret != fakeOAT {
		t.Fatalf("Use: %v", err)
	}
	// alice's blob under bob's name doesn't open (a per-person subkey)
	raw, _ := os.ReadFile(s.path("alice"))
	_ = os.WriteFile(s.path("bob"), bytes.ReplaceAll(raw, []byte(`"user": "alice"`), []byte(`"user": "bob"`)), 0o600)
	if _, err := s.Use("bob", "claude", "", UseFor{}); !errors.Is(err, ErrForeignVault) {
		t.Fatalf("a blob moved to another person: %v", err)
	}
	b.Seal()
	if _, err := s.Use("alice", "claude", "", UseFor{}); !errors.Is(err, ErrSealed) {
		t.Fatalf("sealed: %v", err)
	}
}
