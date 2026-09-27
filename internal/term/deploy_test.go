package term

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// zsTokens is a terminal-token minter with a fixed token, so the env
// golden names it.
type zsTokens struct{ minted []string }

func (z *zsTokens) MintTerminal(component, userID string) string {
	z.minted = append(z.minted, component+"|"+userID)
	return "TERMTOKEN"
}
func (z *zsTokens) RevokeTerminal(string) {}

// zsEnvLines renders an env list one entry per line.
func zsEnvLines(env []string) string { return strings.Join(env, "\n") }

// covers PO-3 Z4 SC-ZERO — a zero-state tile's terminal and agent sessions
// get today's env: the sandboxed env (rootfs PATH, per-user HOME, the
// session's tile-scoped token, the relay's XBIN_URL rewrite) with exactly
// the two GIT_CONFIG_* pairs of today, nothing when the session has no
// API; and the host shell's env (isolation off) after the inherited
// process env. No XBIN_DEPLOYMENT, no third GIT_CONFIG pair. The
// shared Env closure is shaped like boot's (stepTerminals).
// Hand-maintained goldens: changing one is a compat change (12-compat.md).
func TestSessionEnvZeroState(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"apps/x", "notes+ideas"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shared := func() []string {
		return []string{
			"XBIN_URL=http://127.0.0.1:8642",
			"XBIN_WORKSPACE=/w",
			"XBIN_DOCS=/w/.xbin/docs",
			"PATH=/opt/xbin/bin:/usr/bin",
			"XBIN_TOKEN=OWNER-LEAK", // never passed on: only the session's token goes in
			"HOME=/root",            // never passed on: the per-user home wins
		}
	}
	toks := &zsTokens{}
	m := &Manager{Root: root, Listen: "127.0.0.1:8642", Env: shared, Tokens: toks,
		sessions: map[string]*Session{}, envHeld: map[string]bool{}, BxPath: "/opt/xbin/bin/bx"}

	// Sandboxed (isolation on): the env a rootfs session starts with.
	const sbxHead = "TERM=xterm-256color\nCOLORTERM=truecolor\n"
	const sbxMid = "IN_SANDBOX=1\nIS_SANDBOX=1\nLANG=C.UTF-8\n" +
		"PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n"
	sandboxed := []struct {
		name, rel, home, token string
		relay                  bool
		want                   string
	}{
		{"a tile terminal, own netns without relay", "apps/x", "/w/homes/ana", "TERMTOKEN", false,
			sbxHead + "XBIN_COMPONENT=apps/x\nHOME=/w/homes/ana\n" + sbxMid +
				"XBIN_URL=http://127.0.0.1:8642\nXBIN_WORKSPACE=/w\nXBIN_DOCS=/w/.xbin/docs\n" +
				"XBIN_TOKEN=TERMTOKEN\n" +
				"GIT_CONFIG_COUNT=2\n" +
				"GIT_CONFIG_KEY_0=url.http://127.0.0.1:8642/.insteadOf\nGIT_CONFIG_VALUE_0=http://xbin/\n" +
				"GIT_CONFIG_KEY_1=http.http://127.0.0.1:8642/.extraHeader\nGIT_CONFIG_VALUE_1=Authorization: Bearer TERMTOKEN"},
		{"a relay scope (internet, org): XBIN_URL through the gateway", "apps/x", "/w/homes/ana", "TERMTOKEN", true,
			sbxHead + "XBIN_COMPONENT=apps/x\nHOME=/w/homes/ana\n" + sbxMid +
				"XBIN_WORKSPACE=/w\nXBIN_DOCS=/w/.xbin/docs\n" +
				"XBIN_TOKEN=TERMTOKEN\nXBIN_URL=http://10.0.2.2:8642\n" +
				"GIT_CONFIG_COUNT=2\n" +
				"GIT_CONFIG_KEY_0=url.http://10.0.2.2:8642/.insteadOf\nGIT_CONFIG_VALUE_0=http://xbin/\n" +
				"GIT_CONFIG_KEY_1=http.http://10.0.2.2:8642/.extraHeader\nGIT_CONFIG_VALUE_1=Authorization: Bearer TERMTOKEN"},
		{"a code-only terminal (no API): no token, no git rewrite", "notes+ideas", "/w/homes/owner", "", false,
			sbxHead + "XBIN_COMPONENT=notes+ideas\nHOME=/w/homes/owner\n" + sbxMid +
				"XBIN_URL=http://127.0.0.1:8642\nXBIN_WORKSPACE=/w\nXBIN_DOCS=/w/.xbin/docs"},
	}
	for _, c := range sandboxed {
		if got := zsEnvLines(m.sandboxEnv(c.rel, c.relay, c.home, c.token)); got != c.want {
			t.Errorf("sandboxed, %s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	// With no shared Env at all (a daemon that set none): no URL, so no git
	// rewrite either.
	bare := &Manager{Root: root, Listen: "127.0.0.1:8642"}
	if got, want := zsEnvLines(bare.sandboxEnv("apps/x", false, "/w/homes/ana", "TERMTOKEN")),
		sbxHead+"XBIN_COMPONENT=apps/x\nHOME=/w/homes/ana\n"+sbxMid+"XBIN_TOKEN=TERMTOKEN"; got != want {
		t.Errorf("sandboxed without a shared env:\n%s\nwant\n%s", got, want)
	}

	// Host shells (isolation off): the process env minus HOME and
	// XBIN_TOKEN, then today's additions. LANG unset so the fallback shows.
	t.Setenv("XBIN_TOKEN", "HOST-LEAK")
	t.Setenv("LANG", "")
	os.Unsetenv("LANG")
	var inherited []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HOME=") && !strings.HasPrefix(e, "XBIN_TOKEN=") {
			inherited = append(inherited, e)
		}
	}
	host := []struct {
		name string
		o    openOpts
		want string
	}{
		{"a tile terminal with the API", openOpts{cwd: "apps/x", homeKey: "ana", userID: "ana", api: true},
			"TERM=xterm-256color\nCOLORTERM=truecolor\nXBIN_COMPONENT=apps/x\nLANG=C.UTF-8\n" +
				"XBIN_URL=http://127.0.0.1:8642\nXBIN_WORKSPACE=/w\nXBIN_DOCS=/w/.xbin/docs\nPATH=/opt/xbin/bin:/usr/bin\n" +
				"HOME=" + filepath.Join(root, "homes", "ana") + "\nXBIN_TOKEN=TERMTOKEN"},
		{"a code-only terminal", openOpts{cwd: "notes+ideas", homeKey: "owner"},
			"TERM=xterm-256color\nCOLORTERM=truecolor\nXBIN_COMPONENT=notes+ideas\nLANG=C.UTF-8\n" +
				"XBIN_URL=http://127.0.0.1:8642\nXBIN_WORKSPACE=/w\nXBIN_DOCS=/w/.xbin/docs\nPATH=/opt/xbin/bin:/usr/bin\n" +
				"HOME=" + filepath.Join(root, "homes", "owner")},
		{"an agent session's entry", openOpts{cwd: "apps/x", homeKey: "ana", userID: "ana", api: true, kind: KindAgent},
			"TERM=xterm-256color\nCOLORTERM=truecolor\nXBIN_COMPONENT=apps/x\nLANG=C.UTF-8\n" +
				"XBIN_URL=http://127.0.0.1:8642\nXBIN_WORKSPACE=/w\nXBIN_DOCS=/w/.xbin/docs\nPATH=/opt/xbin/bin:/usr/bin\n" +
				"HOME=" + filepath.Join(root, "homes", "ana") + "\nXBIN_TOKEN=TERMTOKEN"},
	}
	for _, c := range host {
		dir, rel, homeDir, token, revoke, err := m.prepare(c.o)
		if err != nil {
			t.Fatalf("%s: prepare: %v", c.name, err)
		}
		_, cleanup, _, envKey, env, err := m.shellCmd(dir, rel, homeDir, token, c.o)
		if err != nil {
			t.Fatalf("%s: shellCmd: %v", c.name, err)
		}
		cleanup()
		revoke()
		if envKey != "" {
			t.Errorf("%s: a host shell holds env layer %q", c.name, envKey)
		}
		if len(env) < len(inherited) || zsEnvLines(env[:len(inherited)]) != zsEnvLines(inherited) {
			t.Fatalf("%s: the env does not start with the inherited process env", c.name)
		}
		if got := zsEnvLines(env[len(inherited):]); got != c.want {
			t.Errorf("host, %s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	if got, want := strings.Join(toks.minted, ","), "apps/x|ana,apps/x|ana"; got != want {
		t.Errorf("terminal tokens minted for %s, want %s (none for a code-only terminal)", got, want)
	}
}

// tileDeps is a TileDeployments hook for one tile that a test changes as it
// goes (the plane's answer after an operation), safe for the session
// goroutines that read it; every other tile is in the zero state.
type tileDeps struct {
	mu   sync.Mutex
	tile string
	d    TileDeployments
}

func (h *tileDeps) set(d TileDeployments) { h.mu.Lock(); h.d = d; h.mu.Unlock() }

func (h *tileDeps) hook(tile string) TileDeployments {
	h.mu.Lock()
	defer h.mu.Unlock()
	if tile != h.tile {
		return zeroDeployments()
	}
	d := h.d
	d.Names = slices.Clone(d.Names)
	return d
}

// targetTokens records every terminal token minted, with the target a
// named one is bound to: auth's two minting calls.
type targetTokens struct {
	mu     sync.Mutex
	minted []string
}

func (z *targetTokens) MintTerminal(component, userID string) string {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.minted = append(z.minted, component+"|"+userID)
	return "TERMTOKEN"
}
func (z *targetTokens) MintTerminalTarget(component, userID, target string) string {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.minted = append(z.minted, component+"|"+userID+"|"+target)
	return "TERMTOKEN-" + target
}
func (z *targetTokens) RevokeTerminal(string) {}
func (z *targetTokens) take() string {
	z.mu.Lock()
	defer z.mu.Unlock()
	out := strings.Join(z.minted, ",")
	z.minted = nil
	return out
}

// frameKeys renders a session frame's keys, sorted, and the frame itself.
func frameKeys(t *testing.T, b []byte) (string, map[string]any) {
	t.Helper()
	var f map[string]any
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ","), f
}

// todaysFrame is the session frame's key set before targets: every session
// without one to state keeps exactly it.
const todaysFrame = "baseOutdated,echoAck,id,label,net,netNote,op,scopes,vm"

// covers P24 P17 P12 PO-3 SC-ZERO — a session's target (11-contract §7.4,
// §5, §8), for both kinds: the default is the primary; with the primary
// protected, the live reload target, named; with live reload paused too,
// "API off": no token, and the session frame echoes api:false with a note.
// A requested deployment is named, the primary's name follows the primary,
// an unknown one is a 404 and a string that is no name a 400. The token is
// minted for the target (today's MintTerminal while it follows the
// primary), XBIN_DEPLOYMENT is set in both env paths right after
// XBIN_COMPONENT only when the target isn't the primary at session start,
// and every answer echoes the target: the session frame (with targetNote),
// the directory row, GET /status's row, a term event, and the agent
// history entry — and a session that sent nothing keeps today's frame,
// row and env.
func TestSessionTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	deps := &tileDeps{tile: "apps/x", d: zeroDeployments()}
	toks := &targetTokens{}
	m := &Manager{Root: root, Listen: "127.0.0.1:8642", Tokens: toks, TileDeployments: deps.hook,
		Env:      func() []string { return []string{"XBIN_URL=http://127.0.0.1:8642"} },
		sessions: map[string]*Session{}, envHeld: map[string]bool{}}
	owner := auth.Principal{Owner: true}
	pick := func(requested string, api bool) (openOpts, int, error) {
		o := m.openOptsFor(owner, "apps/x", "apps/x", "", "", api)
		code, err := m.pickTarget(owner, &o, "apps/x", requested)
		return o, code, err
	}
	record := TileDeployments{Record: true, Primary: "main", LiveReload: "main", Names: []string{"main", "dev", "qa"}}
	protected := record
	protected.Protected, protected.LiveReload = true, "dev"
	paused := protected
	paused.LiveReload = ""
	reassigned := record
	reassigned.Primary, reassigned.LiveReload = "dev", "dev"

	for _, c := range []struct {
		name      string
		d         TileDeployments
		requested string
		api       bool
		code      int
		want      sessionTarget // opener aside
		wantAPI   bool
	}{
		{"zero state, the default", zeroDeployments(), "", true, 0, sessionTarget{}, true},
		{"zero state, main by name", zeroDeployments(), "main", true, 0, sessionTarget{askedPrimary: true}, true},
		{"zero state, dev", zeroDeployments(), "dev", true, 404, sessionTarget{}, true},
		{"zero state, no API", zeroDeployments(), "", false, 0, sessionTarget{Target: Target{NoAPI: true}}, false},
		{"not a deployment name", record, "Dev!", true, 400, sessionTarget{}, true},
		{"a record, the default", record, "", true, 0, sessionTarget{}, true},
		{"a record, dev", record, "dev", true, 0,
			sessionTarget{Target: Target{Deployment: "dev"}, env: "dev", note: "this terminal calls apps/x+dev"}, true},
		{"a record, unknown", record, "nope", true, 404, sessionTarget{}, true},
		{"protected, the default falls to live reload", protected, "", true, 0,
			sessionTarget{Target: Target{Deployment: "dev"}, env: "dev", note: "this terminal calls apps/x+dev"}, true},
		{"protected, main by name", protected, "main", true, 403, sessionTarget{}, true},
		{"protected and paused: API off", paused, "", true, 0,
			sessionTarget{Target: Target{NoAPI: true}, apiOff: true, note: "tile API off: main is protected and live reload is paused"}, false},
		{"protected and paused, qa", paused, "qa", true, 0,
			sessionTarget{Target: Target{Deployment: "qa"}, env: "qa", note: "this terminal calls apps/x+qa"}, true},
		{"the primary reassigned, main by name", reassigned, "main", true, 0,
			sessionTarget{Target: Target{Deployment: "main"}, env: "main", note: "this terminal calls apps/x+main"}, true},
		{"the primary reassigned, dev by name follows it", reassigned, "dev", true, 0, sessionTarget{askedPrimary: true}, true},
	} {
		deps.set(c.d)
		o, code, err := pick(c.requested, c.api)
		if code != c.code || (code == 0) != (err == nil) {
			t.Errorf("%s: %d %v, want %d", c.name, code, err, c.code)
			continue
		}
		if code == 403 && !errors.Is(err, ErrTargetProtected) {
			t.Errorf("%s: %v, want the protection refusal", c.name, err)
		}
		if code != 0 {
			continue
		}
		got := o.target
		got.opener = auth.Principal{}
		if got != c.want || o.api != c.wantAPI {
			t.Errorf("%s: %+v api=%v, want %+v api=%v", c.name, got, o.api, c.want, c.wantAPI)
		}
		if o.target.opener != owner {
			t.Errorf("%s: the opener isn't kept", c.name)
		}
	}

	// The env and the token, both session env paths. A session that follows
	// the primary gets today's env and today's MintTerminal; a named target
	// gets XBIN_DEPLOYMENT right after XBIN_COMPONENT and a token bound to it.
	// A host shell's env starts with xbind's own process env, as today; the
	// session's variables follow it.
	var inherited int
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HOME=") && !strings.HasPrefix(e, "XBIN_TOKEN=") {
			inherited++
		}
	}
	envOf := func(o openOpts) (host, sandboxed []string) {
		t.Helper()
		dir, rel, homeDir, token, revoke, err := m.prepare(o)
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		_, cleanup, _, _, env, err := m.shellCmd(dir, rel, homeDir, token, o)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		return env[inherited:], m.sessionEnv(rel, false, homeDir, token, o)
	}
	after := func(env []string, key string) string {
		i := slices.IndexFunc(env, func(e string) bool { return strings.HasPrefix(e, key) })
		if i < 0 || i+1 >= len(env) {
			return ""
		}
		return env[i+1]
	}
	deps.set(record)
	o, _, _ := pick("", true)
	host, sbx := envOf(o)
	if toks.take() != "apps/x|" || slices.ContainsFunc(append(host, sbx...), func(e string) bool { return strings.HasPrefix(e, "XBIN_DEPLOYMENT=") }) {
		t.Errorf("a session that follows the primary: minted, or got XBIN_DEPLOYMENT: %q %q", host, sbx)
	}
	if want := m.sandboxEnv("apps/x", false, HomeDir(root, "owner"), "TERMTOKEN"); !reflect.DeepEqual(sbx, want) {
		t.Errorf("a follower's sandboxed env:\n%q\nwant today's\n%q", sbx, want)
	}
	o, _, _ = pick("dev", true)
	host, sbx = envOf(o)
	if got := toks.take(); got != "apps/x||dev" {
		t.Errorf("a session targeting dev minted %q, want a token bound to dev", got)
	}
	for name, env := range map[string][]string{"host": host, "sandboxed": sbx} {
		if got := after(env, "XBIN_COMPONENT="); got != "XBIN_DEPLOYMENT=dev" {
			t.Errorf("%s env: after XBIN_COMPONENT comes %q, want XBIN_DEPLOYMENT=dev", name, got)
		}
		if n := len(slices.DeleteFunc(slices.Clone(env), func(e string) bool { return !strings.HasPrefix(e, "XBIN_DEPLOYMENT=") })); n != 1 {
			t.Errorf("%s env: %d XBIN_DEPLOYMENT entries", name, n)
		}
	}
	if after(sbx, "XBIN_TOKEN=") == "" || !slices.Contains(sbx, "XBIN_TOKEN=TERMTOKEN-dev") {
		t.Errorf("the sandboxed env carries the dev-bound token: %q", sbx)
	}
	deps.set(paused)
	o, _, _ = pick("", true)
	host, sbx = envOf(o)
	if got := toks.take(); got != "" || slices.ContainsFunc(append(host, sbx...), func(e string) bool {
		return strings.HasPrefix(e, "XBIN_TOKEN=") && !inProcessEnv(e) || strings.HasPrefix(e, "XBIN_DEPLOYMENT=")
	}) {
		t.Errorf("API off by the fallback: minted %q; env %q %q", got, host, sbx)
	}
	// A minter that can't bind a deployment mints nothing for a named target.
	deps.set(record)
	o, _, _ = pick("dev", true)
	plain := &Manager{Root: root, Tokens: &zsTokens{}, sessions: map[string]*Session{}, envHeld: map[string]bool{}}
	if tok := plain.mintTerminal("apps/x", o); tok != "" {
		t.Errorf("a minter without MintTerminalTarget minted %q for dev", tok)
	}

	// The echo: stub sessions (no process) with the targets above.
	stubOn := func(id string, st sessionTarget, api bool) *Session {
		s := stub(m, id, "owner", "apps/x", time.Unix(int64(len(m.sessions)), 0))
		s.target, s.api = st, api
		return s
	}
	deps.set(record)
	follower := stubOn("f", sessionTarget{}, true)
	byName := stubOn("n", sessionTarget{askedPrimary: true}, true)
	dev := stubOn("d", sessionTarget{Target: Target{Deployment: "dev"}, env: "dev", note: "this terminal calls apps/x+dev"}, true)
	off := stubOn("o", sessionTarget{Target: Target{NoAPI: true}, apiOff: true, note: "tile API off: main is protected and live reload is paused"}, false)
	if keys, _ := frameKeys(t, follower.hello(m.echoOf(follower))); keys != todaysFrame {
		t.Errorf("a follower's session frame has %s, want today's %s", keys, todaysFrame)
	}
	if keys, f := frameKeys(t, dev.hello(m.echoOf(dev))); keys != "baseOutdated,deployment,echoAck,id,label,net,netNote,op,scopes,targetNote,vm" ||
		f["deployment"] != "dev" || f["targetNote"] != "this terminal calls apps/x+dev" {
		t.Errorf("dev's session frame: %s %v", keys, f)
	}
	if keys, f := frameKeys(t, off.hello(m.echoOf(off))); keys != "api,baseOutdated,echoAck,id,label,net,netNote,op,scopes,targetNote,vm" || f["api"] != false {
		t.Errorf("an API-off session's frame: %s %v", keys, f)
	}
	if _, f := frameKeys(t, byName.hello(m.echoOf(byName))); f["deployment"] != "main" {
		t.Errorf("a follower that named main echoes %v, want main", f["deployment"])
	}
	echoes := func() map[string]string {
		out := map[string]string{}
		for _, r := range m.ListFor("owner", "apps/x", nil) {
			out[r.ID] = r.Deployment
		}
		for _, r := range m.List() {
			if d, ok := r["deployment"]; ok && d != out[r["id"].(string)] {
				t.Errorf("GET /status row %v disagrees with the directory's %q", r, out[r["id"].(string)])
			} else if !ok && out[r["id"].(string)] != "" {
				t.Errorf("GET /status row %v lacks the directory's %q", r, out[r["id"].(string)])
			}
		}
		return out
	}
	if got, want := echoes(), map[string]string{"f": "", "n": "main", "d": "dev", "o": ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("the directory's echoes = %v, want %v", got, want)
	}
	// The echo of a follower that named the primary is the current primary.
	deps.set(reassigned)
	if got := m.DeploymentOf("n"); got != "dev" {
		t.Errorf("after a reassignment the follower echoes %q, want the new primary", got)
	}
	if got := m.DeploymentOf("gone"); got != "" {
		t.Errorf("an unknown session echoes %q", got)
	}
	var row map[string]any
	if b, _ := json.Marshal(m.row(follower)); json.Unmarshal(b, &row) != nil || row["deployment"] != nil {
		t.Errorf("a follower's directory row names a deployment: %s", b)
	}
	for _, id := range []string{"f", "n", "d", "o"} {
		m.remove(id)
	}

	// An agent session: the target on the wire and in its history entry.
	t.Run("agent", func(t *testing.T) {
		r := newAgentRig(t)
		ah := &tileDeps{tile: "apps/x", d: record}
		r.m.TileDeployments = ah.hook
		ctx := context.Background()
		for _, c := range []struct{ requested, want string }{{"dev", "dev"}, {"", ""}} {
			info, code, err := r.m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: "fake", Deployment: c.requested})
			if err != nil || code != 200 || info.Deployment != c.want {
				t.Fatalf("open, requested %q: %+v %d %v", c.requested, info, code, err)
			}
			waitClose(t, r.change, "open:"+info.ID)
			r.until(t, func(e SessionEvent) bool {
				return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
			})
			if _, err := r.m.AgentPrompt(ctx, info.ID, "hello"); err != nil {
				t.Fatal(err)
			}
			r.until(t, func(e SessionEvent) bool { return e.ID == info.ID && e.Type == agent.EvTurnEnd })
			r.m.Kill(info.ID)
			waitClose(t, r.change, "close:"+info.ID)
			meta, _, err := r.m.ReadHistory("owner", info.ID)
			if err != nil || meta.Deployment != c.want {
				t.Fatalf("history of a session requested %q: %+v %v", c.requested, meta, err)
			}
			raw, _ := os.ReadFile(filepath.Join(r.m.historyDir("owner", "apps/x"), info.ID+".json"))
			if c.want == "" && strings.Contains(string(raw), `"deployment"`) {
				t.Errorf("a follower's history entry names a deployment: %s", raw)
			}
		}
		// A refused target opens nothing.
		if _, code, err := r.m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: "fake", Deployment: "nope"}); code != 404 || err == nil {
			t.Fatalf("an unknown target: %d %v, want 404", code, err)
		}
		if rows := r.m.ListFor("owner", "", nil); len(rows) != 0 {
			t.Fatalf("a refused open left %+v", rows)
		}
	})
}

// covers P21 P24 T16 — a protected primary is never a session's target
// (11-contract §7.4) (= TestProtectedPrimaryRefusesTerminalTokens): a
// shell or agent session naming it is refused with 403 and the catalogue's
// text before anything spawns; protecting the primary (PrimaryProtected,
// which the protect operation calls after its record commits) restarts
// the agent sessions that followed it onto the default (P24), the live
// reload target, resuming as …/restart does, and ends the shells; when the
// default is "API off" it ends every such session. Sessions with a named
// target elsewhere or without the API stay. A reassignment
// (PrimaryReassigned) moves the sessions named after either primary. That
// a token still bound to the protected primary is refused at once is the
// deployments plane's (Plane.Addressed).
func TestTargetProtectedPrimary(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	r := newAgentRig(t)
	m := r.m
	deps := &tileDeps{tile: "apps/x", d: TileDeployments{Record: true, Primary: "main", Protected: true, LiveReload: "dev", Names: []string{"main", "dev"}}}
	m.TileDeployments = deps.hook
	owner := auth.Principal{Owner: true}

	// Refused, both kinds, before anything spawns.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ws/term?cwd=apps/x&deployment=main", nil)
	m.ServeWS(w, req.WithContext(auth.WithPrincipal(req.Context(), owner)))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "the primary of apps/x is protected: terminal and agent sessions can't target it") {
		t.Fatalf("a shell naming the protected primary: %d %q", w.Code, w.Body.String())
	}
	if _, code, err := m.OpenAgentWith(owner, AgentOpen{Cwd: "apps/x", Provider: "fake", Deployment: "main"}); code != 403 || !errors.Is(err, ErrTargetProtected) {
		t.Fatalf("an agent naming the protected primary: %d %v", code, err)
	}
	if got := Entries(deps.hook("apps/x")); !reflect.DeepEqual(got, []string{"dev"}) {
		t.Fatalf("the dropdown offers %v", got)
	}
	if n := len(m.List()); n != 0 {
		t.Fatalf("a refused open started %d sessions", n)
	}

	// Unprotected: sessions that follow main, one named dev, one without the API.
	deps.set(TileDeployments{Record: true, Primary: "main", LiveReload: "dev", Names: []string{"main", "dev"}})
	openAgent := func(a AgentOpen) SessionInfo {
		t.Helper()
		a.Cwd, a.Provider = "apps/x", "fake"
		info, code, err := m.OpenAgentWith(owner, a)
		if err != nil || code != 200 {
			t.Fatalf("open %+v: %d %v", a, code, err)
		}
		waitClose(t, r.change, "open:"+info.ID)
		r.until(t, func(e SessionEvent) bool {
			return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
		})
		return info
	}
	follower := openAgent(AgentOpen{})
	named := openAgent(AgentOpen{Deployment: "dev"})
	codeOnly := openAgent(AgentOpen{NoAPI: true})
	o := m.openOptsFor(owner, "apps/x", "apps/x", "", "", true)
	if code, err := m.pickTarget(owner, &o, "apps/x", ""); err != nil {
		t.Fatalf("a shell's target: %d %v", code, err)
	}
	shell, err := m.create(o)
	if err != nil {
		t.Fatal(err)
	}
	waitClose(t, r.change, "open:"+shell.ID)
	if _, err := m.AgentPrompt(context.Background(), follower.ID, "remember this"); err != nil {
		t.Fatal(err)
	}
	r.until(t, func(e SessionEvent) bool { return e.ID == follower.ID && e.Type == agent.EvTurnEnd })

	// Protect: the agent follower restarts onto dev and resumes; the shell ends.
	deps.set(TileDeployments{Record: true, Primary: "main", Protected: true, LiveReload: "dev", Names: []string{"main", "dev"}})
	if re, end := m.PrimaryProtected("apps/x"); re != 1 || end != 1 {
		t.Fatalf("protecting restarted %d and ended %d, want 1 and 1", re, end)
	}
	moved := waitRow(t, m, func(si SessionInfo) bool { return si.ID != named.ID && si.Kind == KindAgent && si.Deployment == "dev" })
	r.until(t, func(e SessionEvent) bool {
		return e.ID == moved.ID && e.Type == agent.EvMessageDelta && strings.Contains(stringOf(edata(e.Event)["text"]), "resumed")
	})
	live := map[string]bool{}
	for _, si := range m.ListFor("owner", "apps/x", nil) {
		live[si.ID] = true
	}
	if live[follower.ID] || live[shell.ID] || !live[named.ID] || !live[codeOnly.ID] || !live[moved.ID] || len(live) != 3 {
		t.Fatalf("after protecting: %v (follower %s, shell %s, named %s, code-only %s, moved %s)",
			live, follower.ID, shell.ID, named.ID, codeOnly.ID, moved.ID)
	}

	// With live reload paused too, the default is "API off": a follower ends.
	deps.set(TileDeployments{Record: true, Primary: "main", LiveReload: "dev", Names: []string{"main", "dev"}})
	another := openAgent(AgentOpen{})
	deps.set(TileDeployments{Record: true, Primary: "main", Protected: true, Names: []string{"main", "dev"}})
	if re, end := m.PrimaryProtected("apps/x"); re != 0 || end != 1 {
		t.Fatalf("protecting with live reload paused restarted %d and ended %d, want 0 and 1", re, end)
	}
	waitClose(t, r.change, "close:"+another.ID)

	// Reassigning the primary to dev moves the sessions named dev: back to
	// following the primary (no XBIN_DEPLOYMENT: dev is the primary now).
	deps.set(TileDeployments{Record: true, Primary: "dev", LiveReload: "dev", Names: []string{"main", "dev"}})
	if re, end := m.PrimaryReassigned("apps/x", "main", "dev"); re != 2 || end != 0 {
		t.Fatalf("reassigning restarted %d and ended %d, want 2 (named, moved) and 0", re, end)
	}
	waitRows(t, m, func(rows []SessionInfo) bool {
		n := 0
		for _, si := range rows {
			if si.Kind == KindAgent && si.API && si.Deployment == "" {
				n++
			}
		}
		return n == 2
	})
	if live := m.ListFor("owner", "apps/x", nil); slices.ContainsFunc(live, func(si SessionInfo) bool { return si.ID == named.ID || si.ID == moved.ID }) {
		t.Fatalf("the sessions named dev are still live after the reassignment: %+v", live)
	}
	for _, si := range m.ListFor("owner", "apps/x", nil) {
		m.Kill(si.ID)
	}
}

// stringOf is v as a string ("" for anything else).
func stringOf(v any) string { s, _ := v.(string); return s }

// waitRow waits (8 s) for a directory row of the owner's on apps/x that
// satisfies pred, and returns it.
func waitRow(t *testing.T, m *Manager, pred func(SessionInfo) bool) SessionInfo {
	t.Helper()
	var hit SessionInfo
	waitRows(t, m, func(rows []SessionInfo) bool {
		for _, si := range rows {
			if pred(si) {
				hit = si
				return true
			}
		}
		return false
	})
	return hit
}

// waitRows waits (8 s) until the owner's rows on apps/x satisfy pred.
func waitRows(t *testing.T, m *Manager, pred func([]SessionInfo) bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !pred(m.ListFor("owner", "apps/x", nil)) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out; rows: %+v", m.ListFor("owner", "apps/x", nil))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
