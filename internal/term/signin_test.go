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
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
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
	r.m.SigninOthers = func(user, tile string) (string, []string, bool) { return tile, *others, true }
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
	return "account: " + r.turnSaid(t, id, "account: ")
}

// logWith is the session's text log once it holds want. The host's stderr
// reaches the log on its own reader, in no order with the events its stdout
// carries (AgentLog is a debugging aid and promises none), so a read right
// after the event a line came with can come before the line.
func logWith(t *testing.T, m *Manager, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		log, _ := m.AgentLog(id)
		if strings.Contains(log, want) {
			return log
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session's text log never held %q: %q", want, log)
		}
		time.Sleep(2 * time.Millisecond)
	}
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
	if log := logWith(t, r.m, id, "hook: CLAUDE_CODE_OAUTH_TOKEN="+signinMask); strings.Contains(log, "FAKEFAKE") {
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
	if s.sig().carries() {
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
		return m.signinPlanFor(p, o, "apps/x", prov, "", forced, chosen, nil)
	}
	if pl := plan(alice, openOpts{}, false, false); pl.saved == nil || pl.why != "" || pl.origin != "person" {
		t.Fatalf("alice alone on the tile: %+v", pl)
	}
	// her workspace agent: only where her approval of the start named the
	// sign-in (review finding 3a) — never by default, never on a start her
	// preference lets through unasked
	agentP := auth.Principal{UserID: "alice", Via: "delegate", Agent: "xbin/wsagent", User: alice.User}
	asAgent := func(d *DelegateStart) signinPlan {
		return m.signinPlanFor(agentP, openOpts{}, "apps/x", prov, "", false, false, d)
	}
	for name, d := range map[string]*DelegateStart{
		"no authorisation":                nil,
		"started unasked, by preference":  {Provider: "fake"},
		"approved, no sign-in was named":  {Approved: true, Provider: "fake", Mode: "ask"},
		"approved, another sign-in named": {Approved: true, Provider: "fake", Mode: "ask", Signin: "si_other"},
	} {
		if pl := asAgent(d); pl.why == "" || (d != nil && d.Signin == "" && !strings.Contains(pl.why, "approve")) {
			t.Errorf("her agent, %s: the sign-in would go in (%+v)", name, pl)
		}
	}
	if pl := asAgent(&DelegateStart{Approved: true, Provider: "fake", Mode: "ask", Signin: "si_Personal"}); pl.saved == nil || pl.why != "" || pl.origin != "agent" || !pl.bound {
		t.Fatalf("her agent, with her approval of a start that named it: %+v", pl)
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
	if pl := plan(alice, openOpts{}, false, false); pl.saved == nil || !strings.Contains(pl.why, "bob can also change apps/x") {
		t.Fatalf("a shared tile: %+v", pl)
	}
	*others = []string{"bob", "carol", "dave"}
	if pl := plan(alice, openOpts{}, false, false); !strings.Contains(pl.why, "bob, carol and 1 more can also") {
		t.Fatalf("a tile shared with three: %q", pl.why)
	}
	// a partitioned tile is no exception (review finding 2): its directory
	// is shared code, bound read-write into every partition's sessions
	mine := openOpts{part: sessionPart{on: true, tile: "apps/x", part: "user:alice", key: "k-alice"}}
	if pl := plan(alice, mine, false, false); !strings.Contains(pl.why, "bob, carol and 1 more can also change apps/x") {
		t.Fatalf("her own partition of a tile others can change: %q", pl.why)
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
	if pl := m.signinPlanFor(alice, openOpts{}, "apps/x", prov, SigninNone, false, false, nil); pl.saved != nil {
		t.Fatalf("signin none: %+v", pl)
	}
	// the launcher's preview is the same answer
	m.SigninOthers = func(_, tile string) (string, []string, bool) { return tile, []string{"bob"}, true }
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
	if row.Signin == nil || row.Signin.Source != SigninHome || row.Signin.Name != "Personal" || !strings.Contains(row.Signin.Why, "bob can also change apps/x") {
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
	if s, _, _ := r.m.agentOf(info.ID); s.sig().carries() {
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
	if s, _, _ := r.m.agentOf(conn.ID); s.sig().carries() {
		t.Fatal("a session on the connector carries the sign-in")
	}
}

// The rule is judged on the tile a session's directory lies in, never on
// the directory as written (review finding 1): bob has access to apps/x —
// which an exact lookup of "apps/x/src" would miss — and a session alice
// opens in apps/x/src runs apps/x's hooks all the same. The sweep and the
// launcher's answer resolve it too; a directory in no registered tile gets
// nothing.
func TestSigninRuleJudgesTheTileADirectoryLiesIn(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	prov, _ := agent.Lookup("fake")
	m := r.m
	var asked []string
	shared := true
	m.TileAt = func(rel string) (string, bool) { // the registry's resolver, as boot installs it
		if rel == "apps/x" || strings.HasPrefix(rel, "apps/x/") {
			return "apps/x", true
		}
		return "", false
	}
	m.SigninOthers = func(user, tile string) (string, []string, bool) { // asked about the tile TileOf named, never a directory
		asked = append(asked, tile)
		if tile != "apps/x" {
			return "", nil, false
		}
		if shared {
			return "apps/x", []string{"bob"}, true
		}
		return "apps/x", nil, true
	}
	alice := termAdmin("alice")
	for _, dir := range []string{"apps/x", "apps/x/src", "apps/x/src/deep/er"} {
		pl := m.signinPlanFor(alice, openOpts{}, dir, prov, "", false, false, nil)
		if pl.saved == nil || !strings.Contains(pl.why, "bob can also") || !strings.Contains(pl.why, "apps/x ") {
			t.Errorf("a session in %s: %+v", dir, pl)
		}
		if why := m.SigninRule(alice, dir, prov); !strings.Contains(why, "bob can also") {
			t.Errorf("the launcher's answer for %s: %q", dir, why)
		}
	}
	for _, a := range asked {
		if a != "apps/x" {
			t.Fatalf("the others were asked about %q, not the tile", a)
		}
	}
	if pl := m.signinPlanFor(alice, openOpts{}, "apps/nowhere/src", prov, "", false, false, nil); pl.why == "" {
		t.Fatal("a directory in no registered tile got the sign-in")
	}

	// the sweep: a session opened in a subdirectory while alice was alone
	// there is ended once bob can change the tile
	if err := os.MkdirAll(filepath.Join(r.root, "apps", "x", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	shared = false
	info, code, err := m.OpenAgentWith(alice, AgentOpen{Cwd: "apps/x/src", Provider: "fake"})
	if err != nil || code != 200 {
		t.Fatalf("open in a subdirectory: %d %v", code, err)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if row, _ := m.Info(info.ID); row.Signin == nil || row.Signin.Source != SigninSaved {
		t.Fatalf("alone on the tile: %+v", row.Signin)
	}
	shared = true
	if n := m.SigninSweep(); n != 1 {
		t.Fatalf("bob can change apps/x now: the sweep ended %d session(s) in apps/x/src", n)
	}
	_, st, err := m.agentOf(info.ID)
	if err == nil {
		select {
		case <-st.gone:
		case <-time.After(10 * time.Second):
			t.Fatal("the session did not end")
		}
	}
}

// A restart never widens what a session signs in with (review finding 6):
// one started with no saved sign-in stays without, one started on a named
// sign-in keeps that one (or none, once it is gone), and a session the
// person's workspace agent started — which the agent still steers — gets
// only what it carried.
func TestSigninRestartKeepsTheChoice(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	signIn(t, r.root, "alice")
	alice := termAdmin("alice")
	restart := func(id string) SessionInfo {
		t.Helper()
		info, _, code, err := r.m.RestartAgent(alice, id, "", "", true, nil)
		if err != nil || code != 200 {
			t.Fatalf("restart: %d %v", code, err)
		}
		r.until(t, func(e SessionEvent) bool {
			return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
		})
		row, _ := r.m.Info(info.ID)
		return row
	}
	// started with none: none after a restart
	none := r.openLLM(t, alice, AgentOpen{Signin: SigninNone})
	row := restart(none.ID)
	if row.Signin == nil || row.Signin.Source == SigninSaved {
		t.Fatalf("a session started with no saved sign-in got one at its restart: %+v", row.Signin)
	}
	r.endSession(t, row.ID)
	// started on the default: still the default
	def := r.openLLM(t, alice, AgentOpen{})
	row = restart(def.ID)
	if row.Signin == nil || row.Signin.Source != SigninSaved {
		t.Fatalf("a session on the default sign-in lost it at its restart: %+v", row.Signin)
	}
	r.endSession(t, row.ID)
	// a session her workspace agent started without one: none at a restart,
	// though she restarts it herself and has a default
	ag, code, err := r.m.OpenAgentWith(alice, AgentOpen{Cwd: "apps/x", Provider: "fake", Signin: SigninNone, origin: "xbin/wsagent"})
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.ID == ag.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	row = restart(ag.ID)
	if row.Origin != OriginAgent || row.Signin == nil || row.Signin.Source == SigninSaved {
		t.Fatalf("an agent-started session without a sign-in got her default at a restart: %+v (origin %q)", row.Signin, row.Origin)
	}
	r.endSession(t, row.ID)
}

// A finished session that ran on its person's saved sign-in is theirs
// alone in history too (review finding 4): its transcript holds whatever
// its tools printed, so an admin viewing as the person — who is refused on
// the live session — reads neither it nor its first prompt. A session that
// carried none stays readable as before.
func TestSigninHistoryIsThePersonsAlone(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	signIn(t, r.root, "alice")
	alice := termAdmin("alice")
	with := r.openLLM(t, alice, AgentOpen{})
	r.whoami(t, with.ID)
	r.endSession(t, with.ID)
	without := r.openLLM(t, alice, AgentOpen{Signin: SigninNone})
	r.whoami(t, without.ID)
	r.endSession(t, without.ID)

	own, viewAs := OwnHistory("alice"), HistoryScope{Home: "alice", ViewAs: true}
	meta, evs, err := r.m.ReadHistory(own, with.ID)
	if err != nil || !meta.Signin || len(evs) == 0 {
		t.Fatalf("her own read: %+v, %d events, %v", meta, len(evs), err)
	}
	if meta, _, err := r.m.ReadHistory(own, without.ID); err != nil || meta.Signin {
		t.Fatalf("a session without a sign-in: %+v %v", meta, err)
	}
	if _, evs, err := r.m.ReadHistory(viewAs, with.ID); !errors.Is(err, ErrSigninHistory) || len(evs) != 0 {
		t.Fatalf("view-as read the transcript of a session that ran on her sign-in: %d events, %v", len(evs), err)
	}
	if _, err := r.m.HistoryMeta(viewAs, with.ID); !errors.Is(err, ErrSigninHistory) {
		t.Fatalf("view-as read its meta: %v", err)
	}
	if err := r.m.DeleteHistory(viewAs, with.ID); !errors.Is(err, ErrSigninHistory) {
		t.Fatalf("view-as deleted it: %v", err)
	}
	if _, evs, err := r.m.ReadHistory(viewAs, without.ID); err != nil || len(evs) == 0 {
		t.Fatalf("view-as on a session without a sign-in, as before: %v", err)
	}
	rows := r.m.ListHistory(viewAs, "", nil)
	var seen int
	for _, row := range rows {
		if row.ID == with.ID {
			seen++
			if !row.Signin || row.Preview != "" {
				t.Fatalf("view-as lists it with its first prompt: %+v", row)
			}
		}
	}
	if seen != 1 || len(rows) != 2 {
		t.Fatalf("view-as lists %d rows, the sign-in one %d times", len(rows), seen)
	}
	for _, row := range r.m.ListHistory(own, "", nil) {
		if row.Preview == "" {
			t.Fatalf("her own list lost a preview: %+v", row)
		}
	}
}

// Switching saved sign-ins off ends the running sessions that carry one
// (review finding 7), as removing a sign-in does — by the sweep the route
// runs, and, where the host's file turned them off, the next time xbind
// judges any start.
func TestSigninsOffEndsRunningSessions(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	signIn(t, r.root, "alice")
	alice := termAdmin("alice")
	prov, _ := agent.Lookup("fake")
	var off atomic.Bool
	r.m.SigninsOff = off.Load
	ended := func(id string) {
		t.Helper()
		_, st, err := r.m.agentOf(id)
		if err != nil {
			return
		}
		select {
		case <-st.gone:
		case <-time.After(10 * time.Second):
			t.Fatal("the session kept running on a sign-in the workspace switched off")
		}
	}
	a := r.openLLM(t, alice, AgentOpen{})
	plain := r.openLLM(t, alice, AgentOpen{Signin: SigninNone})
	if n := r.m.SigninSweep(); n != 0 {
		t.Fatalf("on: the sweep ended %d", n)
	}
	off.Store(true)
	if n := r.m.SigninSweep(); n != 1 {
		t.Fatalf("off: the sweep ended %d session(s), want the one on a sign-in", n)
	}
	ended(a.ID)
	if _, _, err := r.m.agentOf(plain.ID); err != nil {
		t.Fatal("a session without a sign-in was ended too")
	}
	// the host's file: nobody calls the sweep — the next judgement notices
	off.Store(false)
	b := r.openLLM(t, alice, AgentOpen{})
	off.Store(true)
	r.m.SigninRule(alice, "apps/x", prov)
	ended(b.ID)
	r.endSession(t, plain.ID)
}

// What a session says is masked on the joined stream (review finding 3b):
// the sign-in split across the pieces of a message, written one character
// per line, as base64 in a tool's output and as hex in the session's log.
func TestSigninRedactionOnTheJoinedStream(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	info := r.openLLM(t, termAdmin("alice"), AgentOpen{})
	if _, err := r.m.AgentPrompt(context.Background(), info.ID, "spill CLAUDE_CODE_OAUTH_TOKEN"); err != nil {
		t.Fatal(err)
	}
	r.until(t, func(e SessionEvent) bool { return e.ID == info.ID && e.Type == agent.EvTurnEnd })
	evs, _, _, err := r.m.AgentEvents(info.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var said, all strings.Builder
	for _, e := range evs {
		all.Write(e.Data)
		if e.Type == agent.EvMessageDelta {
			text, _ := edata(e)["text"].(string)
			said.WriteString(text)
		}
	}
	if got := said.String(); leaks(got, fakeSavedToken) || !strings.Contains(got, "pieces: "+signinMask+" lines: "+signinMask+" spilled") {
		t.Fatalf("the message, joined: %q", got)
	}
	if got := all.String(); leaks(got, fakeSavedToken) || strings.Count(got, signinMask) < 3 {
		t.Fatalf("the events (the tool's base64 too):\n%s", got)
	}
	if log := logWith(t, r.m, info.ID, "hook: "+signinMask); leaks(log, fakeSavedToken) {
		t.Fatalf("the session's text log: %q", log)
	}
	r.endSession(t, info.ID)
}

// A workspace agent's approved start opens with exactly the sign-in the
// approval named, live (review finding 3a): with it the session runs on
// it; when it is gone since, the start is refused — not opened on another,
// nor without; a start whose approval named none runs without.
func TestSigninDelegateStartIsBoundToTheApprovedSignin(t *testing.T) {
	r, f, _ := signinRig(t, "--require-login")
	f.put("alice", "fake", "Personal", "CLAUDE_CODE_OAUTH_TOKEN", signins.KindSetupToken, fakeSavedToken)
	signIn(t, r.root, "alice")
	alice := termAdmin("alice")
	t.Cleanup(auth.TestSealDelegate("xbin/wsagent"))
	r.m.AgentPolicy = delegation.OpenPolicy{}
	agentP := auth.Principal{UserID: "alice", User: alice.User, Via: "delegate", Agent: "xbin/wsagent"}
	open := func(signin string) (SessionInfo, int, error) {
		info, code, err := r.m.OpenAgentWith(agentP, AgentOpen{Cwd: "apps/x", Provider: "fake", Mode: "ask",
			Delegate: &DelegateStart{Approved: true, Provider: "fake", Mode: "ask", Signin: signin}})
		if err == nil {
			r.until(t, func(e SessionEvent) bool {
				return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
			})
			info, _ = r.m.Info(info.ID)
		}
		return info, code, err
	}
	info, code, err := open("si_Personal")
	if err != nil || info.Signin == nil || info.Signin.Source != SigninSaved {
		t.Fatalf("approved with the sign-in named: %d %v %+v", code, err, info.Signin)
	}
	if rows := f.used(); len(rows) != 1 || rows[0].Origin != "agent" {
		t.Fatalf("the use log: %+v", rows)
	}
	r.endSession(t, info.ID)
	if _, code, err := open("si_gone"); err == nil || code != 403 || !strings.Contains(err.Error(), "ask again") {
		t.Fatalf("approved with a sign-in that is gone: %d %v", code, err)
	}
	info, code, err = open("")
	if err != nil || info.Signin == nil || info.Signin.Source == SigninSaved || !strings.Contains(info.Signin.Why, "approve a start that names it") {
		t.Fatalf("approved, none named: %d %v %+v", code, err, info.Signin)
	}
	r.endSession(t, info.ID)
}
