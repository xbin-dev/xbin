package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
)

// GET /agent/providers is a contract the shell's Agent tab and the native
// app read: byte for byte what it served before the ACP client moved to
// sdk/acp (captured on master 7b54e7fb) — the catalog's new fields
// (LoginCmd, Bins, AutoMode) never reach this JSON — plus what was added
// since: the guided sign-in's `signin` and claude's `claude auth login`
// (D178).
func TestAgentProvidersGolden(t *testing.T) {
	h, s := termServer(t)
	bob := s.Auth.NewSession("bob", "")
	get := func() string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("GET", "/api/xbin/agent/providers", "", bob))
		if w.Code != 200 {
			t.Fatalf("providers: %d %s", w.Code, w.Body.String())
		}
		return strings.TrimSpace(w.Body.String())
	}
	t.Setenv(agent.FakeEnv, "")
	if got := get(); got != wantProviders {
		t.Errorf("the providers JSON changed\n got %s\nwant %s", got, wantProviders)
	}
	t.Setenv(agent.FakeEnv, "/bin/fakeacp --x")
	if got := get(); got != wantProvidersFake {
		t.Errorf("the providers JSON with the fake changed\n got %s\nwant %s", got, wantProvidersFake)
	}
}

const wantProviders = `[{"id":"claude","name":"Claude Code","login":"claude auth login","modes":[{"id":"default","name":"Ask before acting"},{"id":"acceptEdits","name":"Accept edits"},{"id":"plan","name":"Plan"},{"id":"auto","name":"Auto"},{"id":"bypassPermissions","name":"Bypass permissions","explicit":true}],"defaultMode":"default","signin":{"command":"claude auth login --claudeai","argv":["claude","auth","login","--claudeai"],"tty":false,"fallback":"claude /exit","url":"https://(?:claude\\.com/cai|claude\\.ai)/oauth/authorize\\?\\S+","hosts":["claude.com","claude.ai"],"code":"Paste code here if prompted","invalid":"Invalid code","done":"Login successful","fail":"Login failed"}},{"id":"codex","name":"Codex","login":"codex login","modes":[{"id":"read-only","name":"Ask for approval"},{"id":"agent","name":"Approve for me"},{"id":"agent-full-access","name":"Full access","explicit":true}],"defaultMode":"read-only"},{"id":"gemini","name":"Gemini CLI","login":"gemini (then choose Login with Google)","modes":[{"id":"default","name":"Ask before acting"},{"id":"autoEdit","name":"Auto edit"},{"id":"plan","name":"Plan"},{"id":"yolo","name":"Auto-approve everything","explicit":true}],"defaultMode":""},{"id":"opencode","name":"OpenCode","login":"opencode auth login","modes":null,"defaultMode":""}]`

const wantProvidersFake = `[{"id":"claude","name":"Claude Code","login":"claude auth login","modes":[{"id":"default","name":"Ask before acting"},{"id":"acceptEdits","name":"Accept edits"},{"id":"plan","name":"Plan"},{"id":"auto","name":"Auto"},{"id":"bypassPermissions","name":"Bypass permissions","explicit":true}],"defaultMode":"default","signin":{"command":"claude auth login --claudeai","argv":["claude","auth","login","--claudeai"],"tty":false,"fallback":"claude /exit","url":"https://(?:claude\\.com/cai|claude\\.ai)/oauth/authorize\\?\\S+","hosts":["claude.com","claude.ai"],"code":"Paste code here if prompted","invalid":"Invalid code","done":"Login successful","fail":"Login failed"}},{"id":"codex","name":"Codex","login":"codex login","modes":[{"id":"read-only","name":"Ask for approval"},{"id":"agent","name":"Approve for me"},{"id":"agent-full-access","name":"Full access","explicit":true}],"defaultMode":"read-only"},{"id":"gemini","name":"Gemini CLI","login":"gemini (then choose Login with Google)","modes":[{"id":"default","name":"Ask before acting"},{"id":"autoEdit","name":"Auto edit"},{"id":"plan","name":"Plan"},{"id":"yolo","name":"Auto-approve everything","explicit":true}],"defaultMode":""},{"id":"opencode","name":"OpenCode","login":"opencode auth login","modes":null,"defaultMode":""},{"id":"fake","name":"Fake agent (tests)","login":"the fake needs no login","modes":[{"id":"ask","name":"Ask"},{"id":"yolo","name":"Yolo","explicit":true}],"defaultMode":"ask","signin":{"command":"claude auth login --claudeai","argv":["claude","auth","login","--claudeai"],"tty":false,"fallback":"claude /exit","url":"https://(?:claude\\.com/cai|claude\\.ai)/oauth/authorize\\?\\S+","hosts":["claude.com","claude.ai"],"code":"Paste code here if prompted","invalid":"Invalid code","done":"Login successful","fail":"Login failed"}}]`
