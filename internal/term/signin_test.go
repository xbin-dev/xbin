package term

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/signins"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// A person's saved sign-in in their agent sessions (signin.go, D230),
// non-isolated (a development workspace: SigninDev): the real `bx
// __agent-host` puts the credential into the scripted agent's environment,
// and the agent — run with --require-login, so it signs in the way Claude
// Code does — says which sign-in a turn used (`whoami`) and prints its own
// environment (`printenv`). The store is a recording double.

// Test credentials: obvious fakes in the shapes the catalog knows.
const (
	fakeSavedToken   = "sk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE-term-9f3e"
	fakeRefusedToken = "sk-ant-oat01-FAKEFAKEFAKEFAKE-refused-term-0002"
	fakeSavedKey     = "sk-proj-FAKEFAKEFAKEFAKEFAKE-term-key-7c1d"
)

type fakeSignins struct {
	mu      sync.Mutex
	by      map[string]*signins.Credential // user\x00harness → the default
	uses    []signins.UseRow
	refused []string // id: words
	useErr  error
}

func (f *fakeSignins) put(user, harness, name, env, kind, secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.by == nil {
		f.by = map[string]*signins.Credential{}
	}
	f.by[user+"\x00"+harness] = &signins.Credential{Secret: secret,
		Signin: signins.Signin{ID: "si_" + name, Harness: harness, Name: name, Env: env, Kind: kind, Default: true, CreatedAt: time.Now().UnixMilli()}}
}

func (f *fakeSignins) get(user, harness, id string) *signins.Credential {
	c := f.by[user+"\x00"+harness]
	if c == nil || (id != "" && c.ID != id) {
		return nil
	}
	return c
}

func (f *fakeSignins) Peek(user, harness, id string) (*signins.Signin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.get(user, harness, id); c != nil {
		sg := c.Signin
		return &sg, nil
	}
	return nil, nil
}

func (f *fakeSignins) Use(user, harness, id string, u signins.UseFor) (*signins.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.get(user, harness, id)
	if c == nil {
		return nil, nil
	}
	if f.useErr != nil {
		return nil, f.useErr
	}
	f.uses = append(f.uses, signins.UseRow{ID: c.ID, Name: c.Name, Harness: harness, Tile: u.Tile, Session: u.Session, Origin: u.Origin})
	out := *c
	return &out, nil
}

func (f *fakeSignins) MarkRefused(user, id, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused = append(f.refused, id+": "+why)
}

func (f *fakeSignins) used() []signins.UseRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]signins.UseRow(nil), f.uses...)
}

// signinRig is an agent rig whose manager keeps sign-ins: a development
// workspace, alice alone on apps/x unless others says otherwise.
func signinRig(t *testing.T, fakeArgs string) (*agentRig, *fakeSignins, *[]string) {
	t.Helper()
	t.Setenv(agent.FakeEnv, strings.TrimSpace(fakeBin+" "+fakeArgs))
	r := newAgentRig(t)
	f := &fakeSignins{}
	others := &[]string{}
	r.m.Signins, r.m.SigninDev = f, true
	r.m.SigninOthers = func(user, tile string) []string { return *others }
	return r, f, others
}

// seen collects every event a session logged so far, as bytes.
func seen(t *testing.T, m *Manager, id string) []byte {
	t.Helper()
	evs, _, _, err := m.AgentEvents(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, e := range evs {
		b.WriteString(e.Type + " ")
		b.Write(e.Data)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func (r *agentRig) whoami(t *testing.T, id string) string {
	t.Helper()
	if _, err := r.m.AgentPrompt(context.Background(), id, "whoami"); err != nil {
		t.Fatal(err)
	}
	e := r.until(t, func(e SessionEvent) bool {
		text, _ := edata(e.Event)["text"].(string)
		return e.ID == id && e.Type == agent.EvMessageDelta && strings.HasPrefix(text, "account: ")
	})
	text, _ := edata(e.Event)["text"].(string)
	r.until(t, func(e SessionEvent) bool { return e.ID == id && e.Type == agent.EvTurnEnd })
	return text
}

// The saved sign-in reaches the agent's process alone, the session says
// what it runs on, and nothing xbind keeps of the session holds the secret.
func TestSigninInjected(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	alice := termAdmin("alice")
	info := r.openLLM(t, alice, AgentOpen{})
	id := info.ID

	if got := r.whoami(t, id); got != "account: token …9f3e" {
		t.Fatalf("the agent signed in with %q", got)
	}
	row, _ := r.m.Info(id)
	if row.Signin == nil || row.Signin.Source != SigninSaved || row.Signin.Name != "Personal" || row.Signin.ID != "si_Personal" || row.Signin.Why != "" {
		t.Fatalf("the row's sign-in: %+v", row.Signin)
	}
	if uses := f.used(); len(uses) != 1 || uses[0].Tile != "apps/x" || uses[0].Session != id || uses[0].Origin != "person" || uses[0].Name != "Personal" {
		t.Fatalf("the use: %+v", uses)
	}
	// the agent prints its own environment — a tool's output, a hook on
	// stderr, its message: every copy xbind keeps is masked
	if got := r.agentEnv(t, id, "CLAUDE_CODE_OAUTH_TOKEN"); got != signinMask {
		t.Fatalf("the agent's own word on its variable, as logged: %q", got)
	}
	all := seen(t, r.m, id)
	if bytes.Contains(all, []byte("FAKEFAKE")) {
		t.Fatalf("an event holds the secret:\n%s", all)
	}
	if !bytes.Contains(all, []byte("Using your saved sign-in Personal.")) || bytes.Count(all, []byte(signinMask)) < 2 {
		t.Fatalf("the notice, and the masked copies:\n%s", all)
	}
	if log, _ := r.m.AgentLog(id); strings.Contains(log, "FAKEFAKE") || !strings.Contains(log, "hook: CLAUDE_CODE_OAUTH_TOKEN="+signinMask) {
		t.Fatalf("the session's text log: %q", log)
	}
	// the host — PID 1 of the session's sandbox — doesn't hold it, nor does
	// a terminal the agent opens through the host
	s, _, _ := r.m.agentOf(id)
	if env, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(s.cmd.Process.Pid), "environ")); err != nil || bytes.Contains(env, []byte("CLAUDE_CODE_OAUTH_TOKEN")) {
		t.Fatalf("the agent host's own environment holds the variable (or can't be read: %v)", err)
	}
	if _, err := r.m.AgentPrompt(context.Background(), id, "run: printenv CLAUDE_CODE_OAUTH_TOKEN | wc -c"); err != nil {
		t.Fatal(err)
	}
	r.until(t, func(e SessionEvent) bool { return e.ID == id && e.Type == agent.EvTurnEnd })
	if all := seen(t, r.m, id); bytes.Contains(all, []byte("FAKEFAKE")) {
		t.Fatal("a terminal's output holds the secret")
	}

	// ended: the transcript on disk is redacted too, and the session's copy is gone
	r.endSession(t, id)
	hist := filepath.Join(r.root, "data", "agent-history")
	_ = filepath.Walk(hist, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("FAKEFAKE")) {
				t.Errorf("history file %s holds the secret", p)
			}
		}
		return nil
	})
	if s.signin.carries() {
		t.Fatal("the ended session kept its copy")
	}
}

// The rule: who gets a saved sign-in put into a session, and what each
// refusal says. signinPlanFor is where POST /term/sessions and the
// launcher's preview both ask.
func TestSigninRule(t *testing.T) {
	r, f, others := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	prov, _ := agent.Lookup("fake")
	m := r.m
	alice := termAdmin("alice")
	plan := func(p auth.Principal, o openOpts, forced, chosen bool) signinPlan {
		return m.signinPlanFor(p, o, "apps/x", prov, "", forced, chosen)
	}
	if pl := plan(alice, openOpts{}, false, false); pl.saved == nil || pl.why != "" || pl.origin != "person" {
		t.Fatalf("alice alone on the tile: %+v", pl)
	}
	// her workspace agent, through the delegation gate: for her
	agentP := auth.Principal{UserID: "alice", Via: "delegate", Agent: "xbin/wsagent", User: alice.User}
	if pl := plan(agentP, openOpts{}, false, false); pl.saved == nil || pl.why != "" || pl.origin != "agent" {
		t.Fatalf("her workspace agent: %+v", pl)
	}
	// never anyone or anything else: nothing of hers is even looked up
	for name, p := range map[string]auth.Principal{
		"the root token":               {Owner: true},
		"an admin viewing as her":      {UserID: "alice", User: alice.User, Impersonator: "bob"},
		"a terminal's token":           {UserID: "alice", User: alice.User, Via: "terminal", Component: "apps/x"},
		"a tile's frame":               {UserID: "alice", User: alice.User, Via: "frame", Component: "apps/x"},
		"a tile's backend":             {Via: "instance", Component: "apps/x"},
		"another person":               termAdmin("bob"),
		"a preview of her":             {UserID: "alice", User: alice.User, Impersonator: auth.PreviewViewer},
		"an owner-driven session":      {UserID: "alice", User: alice.User, Owner: true},
		"a cron tick in her name":      {UserID: "alice", Via: "cron", Role: "admin"},
		"a workspace terminal's token": {UserID: "alice", User: alice.User, Via: "terminal"},
	} {
		if pl := plan(p, openOpts{}, false, false); pl.saved != nil {
			t.Errorf("%s was offered her sign-in: %+v", name, pl)
		}
	}
	// someone else can open a terminal on the tile: left out, and it says who
	*others = []string{"bob"}
	if pl := plan(alice, openOpts{}, false, false); pl.saved == nil || !strings.Contains(pl.why, "bob can also open terminals on apps/x") {
		t.Fatalf("a shared tile: %+v", pl)
	}
	*others = []string{"bob", "carol", "dave"}
	if pl := plan(alice, openOpts{}, false, false); !strings.Contains(pl.why, "bob, carol and 1 more can also") {
		t.Fatalf("a tile shared with three: %q", pl.why)
	}
	// …unless the tile is partitioned and the session is in her partition
	mine := openOpts{part: sessionPart{on: true, tile: "apps/x", part: "user:alice", key: "k-alice"}}
	if pl := plan(alice, mine, false, false); pl.why != "" {
		t.Fatalf("her own partition of a shared tile: %q", pl.why)
	}
	global := openOpts{part: sessionPart{on: true, tile: "apps/x", part: "global"}} // no person's partition
	if pl := plan(alice, global, false, false); pl.why == "" {
		t.Fatal("a session in the tile's global partition got it")
	}
	*others = nil
	// the connector alone may serve, or she chose it
	if pl := plan(alice, openOpts{}, true, false); !strings.Contains(pl.why, "LLM connector only") {
		t.Fatalf("forced: %q", pl.why)
	}
	if pl := plan(alice, openOpts{}, false, true); !strings.Contains(pl.why, "started on the workspace's LLM connector") {
		t.Fatalf("chosen: %q", pl.why)
	}
	// no sandboxes, and not a development workspace
	m.SigninDev = false
	if pl := plan(alice, openOpts{}, false, false); !strings.Contains(pl.why, "without sandboxes") {
		t.Fatalf("no sandboxes: %q", pl.why)
	}
	m.Isolate = true
	if pl := plan(alice, openOpts{}, false, false); pl.why != "" {
		t.Fatalf("with sandboxes: %q", pl.why)
	}
	m.Isolate, m.SigninDev = false, true
	// who else is unknown: default-deny
	m.SigninOthers = nil
	if pl := plan(alice, openOpts{}, false, false); !strings.Contains(pl.why, "can't tell who else") {
		t.Fatalf("no answer about the others: %q", pl.why)
	}
	// the session's own choice
	if pl := m.signinPlanFor(alice, openOpts{}, "apps/x", prov, SigninNone, false, false); pl.saved != nil {
		t.Fatalf("signin none: %+v", pl)
	}
	// the launcher's preview is the same answer
	m.SigninOthers = func(string, string) []string { return []string{"bob"} }
	if sg, why := m.SigninPreview(alice, "apps/x", prov, false); sg == nil || !strings.Contains(why, "bob can also") {
		t.Fatalf("preview: %+v %q", sg, why)
	}
}

// Left out: the session starts without the saved sign-in, says why, and
// runs on the tile's own login when $HOME has one.
func TestSigninLeftOut(t *testing.T) {
	r, f, others := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	signIn(t, r.root, "alice") // the tile's own login, in her $HOME
	*others = []string{"bob"}
	alice := termAdmin("alice")
	info := r.openLLM(t, alice, AgentOpen{})
	defer r.endSession(t, info.ID)
	if got := r.whoami(t, info.ID); got != "account: home" {
		t.Fatalf("a shared tile's session signed in with %q", got)
	}
	row, _ := r.m.Info(info.ID)
	if row.Signin == nil || row.Signin.Source != SigninHome || row.Signin.Name != "Personal" || !strings.Contains(row.Signin.Why, "bob can also open terminals") {
		t.Fatalf("the row: %+v", row.Signin)
	}
	if len(f.used()) != 0 {
		t.Fatalf("a sign-in that was left out was taken from the store: %+v", f.used())
	}
	if got := r.agentEnv(t, info.ID, "CLAUDE_CODE_OAUTH_TOKEN"); got != "" {
		t.Fatalf("the variable is set: %q", got)
	}
	if all := seen(t, r.m, info.ID); !bytes.Contains(all, []byte("isn't used here")) || !bytes.Contains(all, []byte("this tile's own login")) {
		t.Fatalf("the session didn't say why:\n%s", all)
	}

	// a start that names a sign-in she doesn't have is refused; none is none
	if _, code, err := r.m.OpenAgentWith(alice, AgentOpen{Cwd: "apps/x", Provider: "fake", Signin: "si_nope"}); code != 404 || err == nil {
		t.Fatalf("an unknown sign-in: %d %v", code, err)
	}
	*others = nil
	none := r.openLLM(t, alice, AgentOpen{Signin: SigninNone})
	defer r.endSession(t, none.ID)
	if got := r.whoami(t, none.ID); got != "account: home" {
		t.Fatalf("signin none signed in with %q", got)
	}
	if row, _ := r.m.Info(none.ID); row.Signin == nil || row.Signin.Source != SigninHome || row.Signin.Name != "" || row.Signin.Why != "" {
		t.Fatalf("signin none's row: %+v", row.Signin)
	}
	// the store can't hand it out (the vault sealed): said, and the session runs without
	f.useErr = signins.ErrSealed
	sealed := r.openLLM(t, alice, AgentOpen{})
	defer r.endSession(t, sealed.ID)
	if row, _ := r.m.Info(sealed.ID); row.Signin == nil || row.Signin.Source != SigninHome || !strings.Contains(row.Signin.Why, "vault is sealed") {
		t.Fatalf("the vault sealed: %+v", row.Signin)
	}
}

// A session that carries a saved sign-in is its person's alone: an admin
// may end it, never drive or read it; a session without one is today's.
func TestSigninSessionIsItsPersons(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	signIn(t, r.root, "carol")
	alice, bob, carol := termAdmin("alice"), termAdmin("bob"), termAdmin("carol")
	hers := r.openLLM(t, alice, AgentOpen{})
	defer r.endSession(t, hers.ID)
	plain := r.openLLM(t, carol, AgentOpen{}) // carol has no saved sign-in
	defer r.endSession(t, plain.ID)

	if err := r.m.MayDrive(hers.ID, alice); err != nil {
		t.Fatalf("alice on her own session: %v", err)
	}
	if err := r.m.MayDrive(hers.ID, bob); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an admin on a session that carries a saved sign-in: %v", err)
	}
	viewAs := auth.Principal{UserID: "alice", User: alice.User, Impersonator: "bob"}
	if err := r.m.MayDrive(hers.ID, viewAs); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "saved sign-in") {
		t.Fatalf("an admin viewing as alice: %v", err)
	}
	if err := r.m.MayKill(hers.ID, bob); err != nil {
		t.Fatalf("an admin ends it: %v", err)
	}
	if err := r.m.MayDrive(plain.ID, bob); err != nil { // without one: an admin's pass, as before
		t.Fatalf("an admin on a session without one: %v", err)
	}
}

// The agent itself refuses the credential: the store is told (its words,
// the credential masked), and nothing logged holds it.
func TestSigninRefused(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Old", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeRefusedToken)
	info := r.openLLM(t, termAdmin("alice"), AgentOpen{})
	defer r.endSession(t, info.ID)
	if _, err := r.m.AgentPrompt(context.Background(), info.ID, "whoami"); err != nil {
		t.Fatal(err)
	}
	r.until(t, func(e SessionEvent) bool { return e.ID == info.ID && e.Type == agent.EvTurnEnd })
	f.mu.Lock()
	refused := append([]string(nil), f.refused...)
	f.mu.Unlock()
	if len(refused) != 1 || !strings.HasPrefix(refused[0], "si_Old: ") || !strings.Contains(refused[0], "revoked") || strings.Contains(refused[0], "FAKEFAKE") {
		t.Fatalf("the store was told: %q", refused)
	}
	if all := seen(t, r.m, info.ID); bytes.Contains(all, []byte("FAKEFAKE")) || !bytes.Contains(all, []byte(signinMask)) {
		t.Fatalf("the refusal as logged:\n%s", all)
	}
}

// A sign-in removed, or someone gaining terminal access to the tile, ends
// the sessions that carry it — and only those.
func TestSigninSessionsEnd(t *testing.T) {
	r, f, others := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	signIn(t, r.root, "carol")
	alice, carol := termAdmin("alice"), termAdmin("carol")
	gone := func(id string) {
		t.Helper()
		_, st, err := r.m.agentOf(id)
		if err != nil {
			return
		}
		select {
		case <-st.gone:
		case <-time.After(10 * time.Second):
			t.Fatalf("session %s did not end", id)
		}
	}
	a := r.openLLM(t, alice, AgentOpen{})
	c := r.openLLM(t, carol, AgentOpen{})
	defer r.endSession(t, c.ID)
	if n := r.m.EndSigninSessions("alice", "si_other", "x"); n != 0 {
		t.Fatalf("another sign-in's removal ended %d", n)
	}
	if n := r.m.EndSigninSessions("carol", "", "x"); n != 0 {
		t.Fatalf("carol's sessions carry none, yet %d ended", n)
	}
	if n := r.m.EndSigninSessions("alice", "si_Personal", "Your saved sign-in Personal was removed, so this session was ended."); n != 1 {
		t.Fatalf("ended %d", n)
	}
	gone(a.ID)

	a = r.openLLM(t, alice, AgentOpen{})
	if n := r.m.SigninSweep(); n != 0 {
		t.Fatalf("nothing changed, yet the sweep ended %d", n)
	}
	*others = []string{"bob"}
	if n := r.m.SigninSweep(); n != 1 {
		t.Fatalf("bob gained terminal access: the sweep ended %d", n)
	}
	gone(a.ID)
	if _, _, err := r.m.agentOf(c.ID); err != nil {
		t.Fatalf("carol's session went too: %v", err)
	}
}

// Codex's way (the fake with --codex-auth): the adapter reads no key from
// its environment, so the key is handed over through authenticate, and the
// file it then writes lands in a key directory of the session's own — never
// in the shared $HOME. A CLI already signed in on its own there keeps its
// login, and the saved key is left out.
func TestSigninKeyThroughAuthenticate(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login --codex-auth")
	f.put("alice", "fake", "Work key", "CODEX_API_KEY", signins.KindAPIKey, fakeSavedKey)
	alice := termAdmin("alice")
	home := HomeDir(r.root, "alice")
	own := filepath.Join(home, ".codex")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "config.toml"), []byte("model = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := r.openLLM(t, alice, AgentOpen{})
	if got := r.whoami(t, info.ID); got != "account: key …7c1d" {
		t.Fatalf("signed in with %q", got)
	}
	if row, _ := r.m.Info(info.ID); row.Signin == nil || row.Signin.Source != SigninSaved || row.Signin.Name != "Work key" {
		t.Fatalf("the row: %+v", row.Signin)
	}
	if _, err := os.Stat(filepath.Join(own, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("the key file is in the shared $HOME (%v)", err)
	}
	private := r.agentEnv(t, info.ID, "CODEX_HOME")
	if private == "" || private == own || strings.HasPrefix(private, home) {
		t.Fatalf("CODEX_HOME: %q", private)
	}
	if b, err := os.ReadFile(filepath.Join(private, "auth.json")); err != nil || !bytes.Contains(b, []byte(fakeSavedKey)) {
		t.Fatalf("the key file in the private directory: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(private, "config.toml")); err != nil || !bytes.Contains(b, []byte("model")) {
		t.Fatalf("the person's own config isn't reachable there: %v", err)
	}
	if len(f.refused) != 0 {
		t.Fatalf("the hand-over was taken for a refusal: %q", f.refused)
	}
	if all := seen(t, r.m, info.ID); bytes.Contains(all, []byte("FAKEFAKE")) {
		t.Fatal("an event holds the key")
	}
	r.endSession(t, info.ID)
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatalf("the private key directory outlived the session (%v)", err)
	}
	if b, err := os.ReadFile(filepath.Join(own, "config.toml")); err != nil || len(b) == 0 {
		t.Fatalf("removing the private directory took the person's own files: %v", err)
	}

	// signed in on its own in this home: its login stays, the saved key is left out
	if err := os.WriteFile(filepath.Join(own, "auth.json"), []byte(`{"auth_mode": "apikey", "OPENAI_API_KEY": "sk-proj-FAKE-own-login-0000aaaa"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info = r.openLLM(t, alice, AgentOpen{})
	defer r.endSession(t, info.ID)
	if got := r.whoami(t, info.ID); got != "account: key …aaaa" {
		t.Fatalf("with its own login it signed in with %q", got)
	}
	row, _ := r.m.Info(info.ID)
	if row.Signin == nil || row.Signin.Source != SigninHome || !strings.Contains(row.Signin.Why, "signed in on its own") {
		t.Fatalf("the row: %+v", row.Signin)
	}
	if s, _, _ := r.m.agentOf(info.ID); s.signin.carries() {
		t.Fatal("the session kept the key it left out")
	}
}

// The connector by the person's standing choice: the saved sign-in is not
// taken, and the session says so.
func TestSigninConnectorChosen(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	r.m.LLM = newFakeLLM()
	r.m.Env = func() []string { return []string{"XBIN_URL=http://127.0.0.1:1"} }
	alice := termAdmin("alice")
	// own-first with a saved sign-in: her own serves
	own := r.openLLM(t, alice, AgentOpen{})
	defer r.endSession(t, own.ID)
	if row, _ := r.m.Info(own.ID); row.LLM != sdkacp.ServeOwn || row.Signin == nil || row.Signin.Source != SigninSaved {
		t.Fatalf("own-first with a saved sign-in: llm %q, %+v", row.LLM, row.Signin)
	}
	// she starts one on the connector: the sign-in stays in the store
	before := len(f.used())
	conn := r.openLLM(t, alice, AgentOpen{LLM: sdkacp.UseConnector})
	defer r.endSession(t, conn.ID)
	row, _ := r.m.Info(conn.ID)
	if row.LLM != sdkacp.ServeConnector || row.Signin == nil || row.Signin.Source != SigninConnector || !strings.Contains(row.Signin.Why, "LLM connector") {
		t.Fatalf("on the connector: llm %q, %+v", row.LLM, row.Signin)
	}
	if len(f.used()) != before {
		t.Fatal("a session on the connector took the sign-in")
	}
	if s, _, _ := r.m.agentOf(conn.ID); s.signin.carries() {
		t.Fatal("a session on the connector carries the sign-in")
	}
}
