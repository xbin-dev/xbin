package term

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
