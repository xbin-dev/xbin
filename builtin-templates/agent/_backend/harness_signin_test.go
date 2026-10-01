package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// The guided sign-in, saved sign-ins and the switch between them (D179;
// harness_guided.go, harness_creds.go). The sandbox is the fake manager's
// (host processes), the coding agent the scripted fake (acptest), which
// signs in as Claude Code does: a scripted `claude` first on the PATH the
// sandbox's commands get (testClaude), and Claude Code's environment
// credentials (CLAUDE_CODE_OAUTH_TOKEN outranks $HOME's sign-in).

// testClaudeURL is the sign-in page the scripted claude prints.
const testClaudeURL = "https://claude.com/cai/oauth/authorize?code=true&client_id=test-client&response_type=code" +
	"&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&state=test-state"

// testClaudeScript is a scripted Claude Code 2.1.280 sign-in:
//   - `claude auth login --claudeai` over pipes: the link line, then "Paste
//     code here if prompted > " and a code per line — one without '#' is
//     "Invalid code" (asked again); good…#… signs $HOME in (the fake agent's
//     credentials file) with "Login successful." and exit 0; any other is
//     "Login failed: …", exit 1;
//   - `claude setup-token` on a terminal only: the link as Ink draws it (an
//     OSC 8 link), the prompt, then good-XXXX#… prints a one-year token
//     ending in XXXX (exit 0, $HOME untouched); newformat… prints a token in
//     a format the scan doesn't know; refuse… is its OAuth error. It refuses
//     to run with anything of the sandbox's environment (IN_SANDBOX) or a
//     HOME that isn't empty (the mint runs clean: mintArgv).
const testClaudeScript = `#!/bin/sh
URL='` + testClaudeURL + `'
code() {
  while IFS= read -r c; do
    c=$(printf '%s' "$c" | tr -d '\r')
    case "$c" in *'#'*) ;; *) printf 'Invalid code. Please make sure the full code was copied.\n' >&2; printf 'Paste code here if prompted > ' >&2; continue ;; esac
    printf '%s\n' "$c"
    return 0
  done
  exit 1
}
case "$1" in
auth)
  printf 'Opening browser to sign in…\n'
  printf "If the browser didn't open, visit: %s\n" "$URL"
  printf 'Paste code here if prompted > '
  c=$(code)
  case "$c" in
  good*) mkdir -p "$HOME/.fakeacp"; printf '{"method":"claude auth login"}\n' > "$HOME/.fakeacp/credentials"; printf 'Login successful.\n'; exit 0 ;;
  esac
  printf 'Login failed: Request failed with status code 400\n'; exit 1 ;;
setup-token)
  [ -t 0 ] || { printf 'Error: Raw mode is not supported on the current process.stdin\n'; exit 2; }
  [ -z "$IN_SANDBOX" ] || { printf 'OAuth error: the sandbox environment leaked into the mint\n'; exit 1; }
  [ -d "$HOME" ] && [ -z "$(ls -A "$HOME")" ] || { printf 'OAuth error: the mint runs in a HOME of the sandbox (%s)\n' "$HOME"; exit 1; }
  printf '\033[2G\033[1mThis\033[7Gwill\033[12Gguide\033[18Gyou\033[22Gthrough\033[30Glong-lived\033[41G(1-year)\033[50Gauth\033[55Gtoken\033[61Gsetup\033[22m\n'
  printf "\033]8;id=t;%s\007\033[38;5;246m%s\033[39m\033]8;;\007\n" "$URL" "$URL"
  printf '\n\033[2GPaste\033[8Gcode\033[13Ghere\033[18Gif\033[21Gprompted\033[30G>\n'
  c=$(code)
  case "$c" in
  good-*) s=${c%%#*}; s=${s#good-}
    printf '\033[32m✓\033[39m Long-lived authentication token created successfully!\n\nYour OAuth token (valid for 1 year):\n\n'
    printf '\033[38;5;220msk-ant-oat01-0123456789abcdefghijABCDEFGHIJ-%s\033[39m\n\nStore this token securely.\n' "$s"; exit 0 ;;
  newformat*) printf 'Your OAuth token (valid for 1 year):\n\nsk-ant-oat09-NEWFORMATNEWFORMATNEWFORMAT-zz\n'; exit 0 ;;
  esac
  printf 'OAuth error: Request failed with status code 400\n'; exit 1 ;;
esac
printf 'error: unknown command %s\n' "$1"; exit 2
`

// testToken is the token the scripted setup-token prints for good-<s>#….
func testToken(s string) string { return "sk-ant-oat01-0123456789abcdefghijABCDEFGHIJ-" + s }

// testClaude puts the scripted claude first on the PATH the fake manager's
// commands get, and first among the image's directories a mint looks in
// (mintPath).
func testClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(testClaudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := mintPath
	mintPath = dir + ":" + old
	t.Cleanup(func() { mintPath = old })
}

// testClaudeShim puts a claude of the sandbox's own first on its PATH (a
// shim in ~/.local/bin, say) — one that would see the token a mint prints.
func testClaudeShim(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	shim := "#!/bin/sh\nprintf 'OAuth error: the shim on the sandbox PATH ran\\n'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// memSecrets is a vault for tests.
type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (v *memSecrets) get(k string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.m[k]
	if !ok {
		return "", fmt.Errorf("vault: 404 no such key")
	}
	return s, nil
}
func (v *memSecrets) set(k, s string) error { v.mu.Lock(); defer v.mu.Unlock(); v.m[k] = s; return nil }
func (v *memSecrets) del(k string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, k)
	return nil
}

func memVault(t *testing.T) *memSecrets {
	t.Helper()
	v := &memSecrets{m: map[string]string{}}
	old := signinVault
	signinVault = v
	t.Cleanup(func() { signinVault = old })
	return v
}

// captureLog collects the process's log lines for the test.
func captureLog(t *testing.T) *syncBuf {
	t.Helper()
	b := &syncBuf{}
	old := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(old) })
	return b
}

// dbHolds is where secret is stored in db ("" = nowhere): table.column.
func dbHolds(t *testing.T, db *DB, secret string) string {
	t.Helper()
	for _, tbl := range dbStrings(t, db, `SELECT name FROM sqlite_master WHERE type='table'`) {
		rows, err := db.sql.Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if strings.Contains(fmt.Sprintf("%s", v), secret) {
					rows.Close()
					return tbl + "." + cols[i]
				}
			}
		}
		rows.Close()
	}
	return ""
}

// aliceCall is alice through her partition's frame: the answer's status and body.
func aliceCall(t *testing.T, h http.Handler, method, path string, body any) (int, string) {
	t.Helper()
	b := ""
	if body != nil {
		b = mustJSON(body)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, as(method, path, b, alicesFrame("read")))
	return w.Code, w.Body.String()
}

// signinExecs are the guided sign-ins' execs the manager still has in box.
func signinExecs(t *testing.T, box *sbxSandbox) []string {
	t.Helper()
	c, err := sbxDial("apps/cs", "alice")
	if err != nil {
		t.Fatal(err)
	}
	xs, err := c.ExecList(context.Background(), box.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range xs {
		if strings.HasPrefix(x.Label, "sign-in ") {
			out = append(out, x.ID+":"+x.State)
		}
	}
	return out
}

// notesOf is the run's journal notes, one per line.
func notesOf(t *testing.T, db *DB, id int64) string {
	t.Helper()
	return strings.Join(dbStrings(t, db, `SELECT detail FROM steps WHERE run_id=? AND kind='note' ORDER BY seq`, id), "\n")
}

// hAnswered waits for the run's turn to end with want in its transcript.
func hAnswered(t *testing.T, ag *Agent, id int64, want string) {
	t.Helper()
	hwait(t, "an answer with "+want, func() bool {
		return turnOver(ag, id)() && strings.Contains(fullText(ag.db, id), want)
	})
}

// The guided sign-in, `claude auth login` over pipes: the link and the
// code prompt to its person (202); a refused code is the CLI's own reason
// (502), its exec gone; a malformed one is asked again (409); the right one
// signs the sandbox's $HOME in, and the run's Retry answers the held
// prompt. Only its person drives it; its exec is always deleted.
func TestGuidedSignin(t *testing.T) {
	testClaude(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	run := askIn(t, h, box, "whoami")
	parkOf(t, ag, run.ID, "login")
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	start := func() {
		t.Helper()
		code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided"})
		var a struct {
			Signin struct {
				URL   string
				Paste bool
			}
		}
		_ = json.Unmarshal([]byte(body), &a)
		if code != 202 || a.Signin.URL != testClaudeURL || !a.Signin.Paste {
			t.Fatalf("the guided sign-in's start: %d %s", code, body)
		}
	}
	start()
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided"}); code != 202 || !strings.Contains(body, "test-state") {
		t.Fatalf("its person asking again: %d %s", code, body)
	}
	// a refused code: the CLI's reason, the exec deleted
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "bad#test-state"}); code != 502 ||
		!strings.Contains(body, "Login failed: Request failed with status code 400") {
		t.Fatalf("a refused code: %d %s", code, body)
	}
	hwait(t, "the refused sign-in's exec deleted", func() bool { return len(signinExecs(t, box)) == 0 })
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "good#x"}); code != 409 || !strings.Contains(body, "start one") {
		t.Fatalf("a code after its end: %d %s", code, body)
	}
	// again: a malformed code is asked for again, the right one signs in
	start()
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "  garbage \n"}); code != 409 ||
		!strings.Contains(body, "isn't the whole code") {
		t.Fatalf("a malformed code: %d %s", code, body)
	}
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "good-1#test-state"}); code != 200 ||
		!strings.Contains(body, `"state":"ready"`) {
		t.Fatalf("the right code: %d %s", code, body)
	}
	hwait(t, "the sign-in's exec deleted", func() bool { return len(signinExecs(t, box)) == 0 })
	hAnswered(t, ag, run.ID, "account: home") // the held prompt, through the Retry, signed in by $HOME
	if hs, _ := ag.db.harnessSession(run.ID); hs.Cred != "" {
		t.Fatalf("no saved sign-in went in: %q", hs.Cred)
	}
	// no sign-in to run once signed in
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided"}); code != 409 || !strings.Contains(body, "is signed in") {
		t.Fatalf("signed in: %d %s", code, body)
	}
}

// A guided sign-in waiting for its person's code holds the person's
// partition up (it lives in this process only); once it is over the hold
// goes with it.
func TestGuidedHoldsPartition(t *testing.T) {
	testClaude(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	run := askIn(t, h, box, "whoami")
	parkOf(t, ag, run.ID, "login")
	open := holdCounter(ag.eng)
	hwait(t, "no hold while it waits on its person", func() bool { return open.Load() == 0 })
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided"}); code != 202 {
		t.Fatalf("the start: %d %s", code, body)
	}
	hwait(t, "the hold while the sign-in waits for the code", func() bool { return open.Load() == 1 })
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "bad#x"}); code != 502 {
		t.Fatalf("a refused code: %d %s", code, body)
	}
	hwait(t, "the hold let go with it", func() bool { return open.Load() == 0 && ag.eng.guidedOf(run.ID) == nil })
}

// Remember: `claude setup-token` on a terminal (the manager's tty), its
// token scraped by the backend and kept as the person's saved sign-in in
// their vault — never in an answer, a row, a log line — named, the
// harness's default as the first; the run's Retry starts the adapter with
// it in its environment (CLAUDE_CODE_OAUTH_TOKEN), where it wins over the
// sandbox's own (absent) sign-in, and the adapter's status probe calling it
// "none" parks nothing.
func TestGuidedRemember(t *testing.T) {
	testClaude(t)
	testClaudeShim(t) // the sandbox's PATH finds a shim first: the mint never runs it (L12)
	vault := memVault(t)
	logs := captureLog(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	run := askIn(t, h, box, "whoami")
	parkOf(t, ag, run.ID, "login")
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	var bodies []string
	code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "remember": true, "name": "Work"})
	bodies = append(bodies, body)
	if code != 202 || !strings.Contains(body, `"url":"`+strings.ReplaceAll(testClaudeURL, "&", `\u0026`)+`"`) || !strings.Contains(body, `"paste":true`) {
		t.Fatalf("the mint's start: %d %s", code, body)
	}
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "garbage"}); code != 409 {
		t.Fatalf("a malformed code: %d %s", code, body)
	}
	code, body = aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "good-W0rk#test-state"})
	bodies = append(bodies, body)
	var done struct {
		State string
		Saved hSignin
	}
	_ = json.Unmarshal([]byte(body), &done)
	s := done.Saved
	if code != 200 || done.State != "ready" || s.Name != "Work" || s.Harness != "fake" || s.Kind != "setup-token" ||
		s.Env != "CLAUDE_CODE_OAUTH_TOKEN" || !s.IsDefault || s.ExpiresAt-s.MintedAt != setupTokenLife.Milliseconds() {
		t.Fatalf("the minted sign-in: %d %s", code, body)
	}
	tok := testToken("W0rk")
	if got, _ := vault.get(signinKey(s.ID)); got != tok {
		t.Fatalf("the vault holds %q, want the minted token", got)
	}
	hwait(t, "the mint's exec deleted", func() bool { return len(signinExecs(t, box)) == 0 })
	hAnswered(t, ag, run.ID, "account: token …W0rk")
	hs, _ := ag.db.harnessSession(run.ID)
	if hs.Cred != s.ID || hs.State != hsLive {
		t.Fatalf("the adapter's sign-in: cred %q state %s", hs.Cred, hs.State)
	}
	if _, err := os.Stat(filepath.Join(box.Home, ".fakeacp", "credentials")); err == nil {
		t.Fatal("the mint signed the sandbox's $HOME in")
	}
	// the summary names it; the token is nowhere but the vault
	for _, p := range []string{fmt.Sprintf("/runs/%d", run.ID), fmt.Sprintf("/runs/%d/view", run.ID), fmt.Sprintf("/runs/%d/harness", run.ID),
		"/prefs/harness-signins", "/harnesses", "/needs"} {
		code, body := aliceCall(t, h, "GET", p, nil)
		bodies = append(bodies, body)
		if code != 200 {
			t.Fatalf("GET %s: %d %s", p, code, body)
		}
	}
	if !strings.Contains(bodies[4], `"signin":{"pick":"default","using":{"id":"`+s.ID+`","name":"Work"}}`) {
		t.Fatalf("the summary's sign-in: %s", bodies[4])
	}
	for i, b := range bodies {
		if strings.Contains(b, tok) || strings.Contains(b, "0123456789abcdefghij") {
			t.Fatalf("answer %d holds the token: %s", i, b)
		}
	}
	if at := dbHolds(t, ag.db, "0123456789abcdefghij"); at != "" {
		t.Fatalf("the token is stored in %s", at)
	}
	if strings.Contains(logs.String(), "0123456789abcdefghij") {
		t.Fatalf("the token is logged:\n%s", logs.String())
	}
}

// The gate: a saved sign-in goes into a coding agent only in its person's
// own partition, for their own conversation, in a sandbox of theirs no one
// else uses, and while it isn't refused or expired.
func TestCredGate(t *testing.T) {
	_ = memVault(t)
	ag, _, box, _, _, global := harnessPartitionG(t)
	own := &Run{ID: partitionIDBase + 1, Owner: "alice", Engine: engineHarness} // a root: credWhy reads it as it is
	mk := func(name string, def bool, edit func(*hSignin)) *hSignin {
		s, err := ag.db.saveSignin("alice", "fake", name, acp.Key{Env: "CLAUDE_CODE_OAUTH_TOKEN", Kind: "setup-token"}, "sk-ant-oat01-"+name, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if def {
			_ = ag.db.setDefaultSignin("alice", "fake", s.ID)
		}
		if edit != nil {
			edit(s)
			_ = ag.db.putSignin(s)
		}
		return s
	}
	work := mk("Work", true, nil)
	refused := mk("Old", false, func(s *hSignin) { s.RefusedAt = nowMs() })
	expired := mk("Gone", false, func(s *hSignin) { s.ExpiresAt = nowMs() - 1 })
	cfg := func(pick string) Config { return Config{Harness: &HarnessConfig{Provider: "fake", Signin: pick}} }
	ref := sandboxRef("apps/cs", box.ID)
	shared := *box
	shared.Members = []string{"bob"}
	bobs := *box
	bobs.Owner.User = "bob"
	team := atGlobal(t, global, "apps/cs", "alice", "team")
	org := *box // a visibility a newer manager adds: shared (L9 — an allow-list)
	org.Visibility = "org"
	hostedRef := sandboxRef("apps/cs", "sb-hosted")
	ag.db.noteHostedUse(hostedRef)
	gone := mk("Gone-soon", false, nil)
	if err := ag.db.dropSignin(gone.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what string
		run  *Run
		cfg  Config
		ref  string
		box  *sbxSandbox
		want string // the saved sign-in's name, "" none
		why  string
	}{
		{"her default, her own sandbox", own, cfg(""), ref, box, "Work", ""},
		{"the sandbox's own, picked", own, cfg(pickSandbox), ref, box, "", ""},
		{"a refused pick", own, cfg(refused.ID), ref, box, "", "it was refused"},
		{"an expired pick", own, cfg(expired.ID), ref, box, "", "it has expired"},
		// L10: a forgotten pick is never the default (another account): the sandbox's own, said so
		{"a forgotten pick", own, cfg(gone.ID), ref, box, "", "picked is gone (forgotten)"},
		{"a shared sandbox", own, cfg(""), ref, &shared, "", credNotOwnBox},
		{"someone else's sandbox", own, cfg(""), ref, &bobs, "", credNotOwnBox},
		{"a sandbox shared with the agent", own, cfg(""), ref, team, "", credNotOwnBox},
		{"a visibility it doesn't know", own, cfg(""), ref, &org, "", credNotOwnBox},
		// M1: a sandbox a hosted conversation used never gets one
		{"a sandbox a hosted conversation used", own, cfg(""), hostedRef, box, "", "a non-secure (hosted) conversation has worked in"},
		{"bob's conversation", &Run{ID: partitionIDBase + 9, Owner: "bob", Engine: engineHarness}, cfg(""), ref, box, "", credNotOwnRun},
	} {
		s, why := ag.db.credPick(c.run, c.cfg, c.ref, c.box)
		name := ""
		if s != nil {
			name = s.Name
		}
		if name != c.want || (c.why == "") != (why == "") || !strings.Contains(why, c.why) {
			t.Errorf("%s: %q (%q), want %q (%q)", c.what, name, why, c.want, c.why)
		}
	}
	if _, why := ag.db.credPick(own, cfg(work.ID), ref, box); why != "" {
		t.Errorf("her pick: %s", why)
	}
	if !sandboxShared(&org) || sandboxShared(box) {
		t.Errorf("sandboxShared: an unknown visibility %v, her own %v", sandboxShared(&org), sandboxShared(box))
	}
	setMode(t, modeGlobal, "")
	if s, _ := ag.db.credPick(own, cfg(""), ref, box); s != nil || credWhy(own, ref, box) != signinsAtGlobal {
		t.Errorf("at the global instance: %+v %q", s, credWhy(own, ref, box))
	}
	setMode(t, modeLegacy, "")
	if s, _ := ag.db.credPick(own, cfg(""), ref, box); s != nil || credWhy(own, ref, box) != signinsLegacy {
		t.Errorf("unpartitioned: %+v %q", s, credWhy(own, ref, box))
	}
}

// End to end: the saved sign-in goes into the adapter while the gate holds
// (whoami says its token), and a sandbox shared since stops the adapter it
// went into; the next start leaves it out.
func TestCredSharedMidRun(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	if code, body := aliceCall(t, h, "POST", "/prefs/harness-signins", map[string]any{"harness": "fake", "name": "Personal",
		"secret": "sk-ant-oat01-personal-AAAA"}); code != 201 || strings.Contains(body, "personal-AAAA") || !strings.Contains(body, `"isDefault":true`) {
		t.Fatalf("a pasted token: %d %s", code, body)
	}
	run := askIn(t, h, box, "whoami")
	hAnswered(t, ag, run.ID, "account: token …AAAA")
	c, err := sbxDial("apps/cs", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Patch(context.Background(), box.ID, sbxPatch{Members: &[]string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	invalidateSandboxCatalog()
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), `{"text":"whoami again"}`, alicesFrame("read")), 200, nil)
	hwait(t, "the adapter stopped for the share", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && hs.State == hsFailed && strings.Contains(hs.Error, credNotOwnBox)
	})
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), `{"text":"whoami third"}`, alicesFrame("read")), 200, nil)
	parkOf(t, ag, run.ID, "login") // its own $HOME isn't signed in, and the saved one stays out
	if hs, _ := ag.db.harnessSession(run.ID); hs.Cred != "" {
		t.Fatalf("a shared sandbox got the saved sign-in: %q", hs.Cred)
	}
}

// Shared through the agent's own route: the coding agent that holds a saved
// sign-in there stops at once — no message needed.
func TestCredSharedThroughAgent(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	if code, body := aliceCall(t, h, "POST", "/prefs/harness-signins", map[string]any{"harness": "fake", "secret": "sk-ant-oat01-personal-CCCC"}); code != 201 {
		t.Fatalf("a pasted token: %d %s", code, body)
	}
	run := askIn(t, h, box, "whoami")
	hAnswered(t, ag, run.ID, "account: token …CCCC")
	if code, body := aliceCall(t, h, "PATCH", "/sandboxes/"+sandboxRef("apps/cs", box.ID), map[string]any{"members": []string{"bob"}}); code != 200 {
		t.Fatalf("sharing it: %d %s", code, body)
	}
	hwait(t, "the adapter stopped by the share", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && hs.State == hsFailed && strings.Contains(hs.Error, "is shared now")
	})
}

// A saved sign-in the CLI refuses: the run parks on its sign-in, saying so,
// the sign-in is marked refused (GET /prefs/harness-signins), and the next
// start leaves it out. A status update alone (claude-agent-acp's probe
// calling an env token "none") refuses nothing.
func TestSavedSigninRefused(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	if code, body := aliceCall(t, h, "POST", "/prefs/harness-signins", map[string]any{"harness": "fake", "name": "Old",
		"secret": "sk-ant-oat01-refused-0001"}); code != 201 {
		t.Fatalf("a pasted token: %d %s", code, body)
	}
	run := askIn(t, h, box, "whoami")
	parkOf(t, ag, run.ID, "login")
	if !strings.Contains(notesOf(t, ag.db, run.ID), "saved sign-in refused") {
		t.Fatalf("no word of the refusal: %s", notesOf(t, ag.db, run.ID))
	}
	_, body := aliceCall(t, h, "GET", "/prefs/harness-signins", nil)
	var l struct{ Signins []hSignin }
	_ = json.Unmarshal([]byte(body), &l)
	if len(l.Signins) != 1 || l.Signins[0].RefusedAt == 0 || !strings.Contains(l.Signins[0].Refused, "revoked") {
		t.Fatalf("the refused sign-in: %s", body)
	}
	// the adapter's words echoed the token: kept masked (N18, M3)
	if strings.Contains(body, "refused-0001") || !strings.Contains(l.Signins[0].Refused, redactMark) {
		t.Fatalf("the refusal's words hold the token: %s", body)
	}
	if at := dbHolds(t, ag.db, "refused-0001"); at != "" {
		t.Fatalf("the token is stored in %s", at)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/resume", run.ID), `{}`, alicesFrame("read")), 200, nil)
	hwait(t, "a new generation without it", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && hs.Gen >= 2 && hs.State == hsLogin
	})
	if hs, _ := ag.db.harnessSession(run.ID); hs.Cred != "" || !strings.Contains(notesOf(t, ag.db, run.ID), "isn't used: it was refused") {
		t.Fatalf("the refused sign-in went in again: %q\n%s", hs.Cred, notesOf(t, ag.db, run.ID))
	}
}

// Forget: the secret leaves the vault, the row goes, and the adapters that
// started with it stop now (the next message starts each without it).
func TestForgetSignin(t *testing.T) {
	vault := memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	_, body := aliceCall(t, h, "POST", "/prefs/harness-signins", map[string]any{"harness": "fake", "secret": "sk-ant-oat01-forget-BBBB"})
	var made struct{ Signin hSignin }
	_ = json.Unmarshal([]byte(body), &made)
	if made.Signin.Name != "Personal" || made.Signin.ID == "" {
		t.Fatalf("the first sign-in's name: %s", body)
	}
	run := askIn(t, h, box, "whoami")
	hAnswered(t, ag, run.ID, "account: token …BBBB")
	code, body := aliceCall(t, h, "DELETE", "/prefs/harness-signins/"+made.Signin.ID, nil)
	if code != 200 || !strings.Contains(body, `"stopped":1`) {
		t.Fatalf("Forget: %d %s", code, body)
	}
	if _, err := vault.get(signinKey(made.Signin.ID)); err == nil || ag.db.signin(made.Signin.ID) != nil {
		t.Fatal("Forget left the secret or the row")
	}
	hwait(t, "its adapter stopped", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && hs.State == hsFailed && strings.Contains(hs.Error, "was forgotten")
	})
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), `{"text":"whoami again"}`, alicesFrame("read")), 200, nil)
	parkOf(t, ag, run.ID, "login")
	if hs, _ := ag.db.harnessSession(run.ID); hs.Cred != "" {
		t.Fatalf("a forgotten sign-in went in: %q", hs.Cred)
	}
	if code, _ := aliceCall(t, h, "DELETE", "/prefs/harness-signins/"+made.Signin.ID, nil); code != 404 {
		t.Fatalf("forgetting it again: %d", code)
	}
}

// Two accounts, one conversation: switching restarts the adapter with the
// other saved sign-in and resumes the same session (session/load — the
// transcript lives in the sandbox, not with the account).
func TestSwitchAccountResumes(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login", "--persist")
	ids := map[string]string{}
	for _, s := range []struct{ name, secret string }{{"Personal", "sk-ant-oat01-mine-AAAA"}, {"Work", "sk-ant-oat01-company-BBBB"}} {
		_, body := aliceCall(t, h, "POST", "/prefs/harness-signins", map[string]any{"harness": "fake", "name": s.name, "secret": s.secret})
		var made struct{ Signin hSignin }
		_ = json.Unmarshal([]byte(body), &made)
		ids[s.name] = made.Signin.ID
	}
	run := askIn(t, h, box, "whoami")
	hAnswered(t, ag, run.ID, "account: token …AAAA")
	before, _ := ag.db.harnessSession(run.ID)
	if before.Cred != ids["Personal"] || before.ACPSession == "" || !before.Loadable {
		t.Fatalf("the first account: %+v", before)
	}
	path := fmt.Sprintf("/runs/%d/harness/signin", run.ID)
	if code, body := aliceCall(t, h, "PUT", path, map[string]any{"signin": "nope"}); code != 400 {
		t.Fatalf("an unknown pick: %d %s", code, body)
	}
	code, body := aliceCall(t, h, "PUT", path, map[string]any{"signin": ids["Work"]})
	if code != 200 || !strings.Contains(body, `"pick":"`+ids["Work"]+`"`) {
		t.Fatalf("the switch: %d %s", code, body)
	}
	hwait(t, "the adapter stopped for the switch", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && hs.State == hsStopped
	})
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), `{"text":"whoami now"}`, alicesFrame("read")), 200, nil)
	hAnswered(t, ag, run.ID, "account: token …BBBB")
	after, _ := ag.db.harnessSession(run.ID)
	if after.Cred != ids["Work"] || after.ACPSession != before.ACPSession || after.Gen <= before.Gen {
		t.Fatalf("after the switch: cred %q session %q gen %d (before %q, %d) — want Work, the same session resumed",
			after.Cred, after.ACPSession, after.Gen, before.ACPSession, before.Gen)
	}
	if n := notesOf(t, ag.db, run.ID); strings.Contains(n, "couldn't reopen its earlier session") || !strings.Contains(n, "switched to Work") {
		t.Fatalf("the session wasn't resumed, or the switch not said: %s", n)
	}
	// back to the sandbox's own (signed out): the next message parks on its sign-in
	if code, body := aliceCall(t, h, "PUT", path, map[string]any{"signin": "sandbox"}); code != 200 {
		t.Fatalf("the sandbox's own: %d %s", code, body)
	}
	hwait(t, "stopped again", func() bool { hs, _ := ag.db.harnessSession(run.ID); return hs.State == hsStopped })
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), `{"text":"whoami last"}`, alicesFrame("read")), 200, nil)
	parkOf(t, ag, run.ID, "login")
}

// Saved sign-ins at the global instance: refused like every coding agent
// there (A-M1); unpartitioned: none, said so — the guided sign-in still,
// Remember not.
func TestSigninsElsewhere(t *testing.T) {
	_ = memVault(t)
	setMode(t, modeGlobal, "")
	_, mux := accessFixture(t)
	for _, c := range []struct{ method, path string }{{"GET", "/prefs/harness-signins"}, {"POST", "/prefs/harness-signins"},
		{"DELETE", "/prefs/harness-signins/x"}} {
		if w := callAs(t, mux, asAlice, c.method, c.path, map[string]any{"harness": "claude", "secret": "k"}); w.Code != 409 ||
			!strings.Contains(w.Body.String(), "shared space holds no one's credentials") {
			t.Errorf("at global %s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	setMode(t, modeLegacy, "")
	testClaude(t)
	ag, mux, box := harnessFixture(t, false, "--require-login")
	w := callAs(t, mux, asAlice, "GET", "/prefs/harness-signins", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), "need a partitioned agent") {
		t.Fatalf("unpartitioned: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "POST", "/prefs/harness-signins", map[string]any{"harness": "claude", "secret": "sk-ant-api03-x"}); w.Code != 409 {
		t.Fatalf("unpartitioned, a paste: %d %s", w.Code, w.Body)
	}
	run := askHarness(t, mux, box, "whoami")
	parkOf(t, ag, run.ID, "login")
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	if w := callAs(t, mux, asAlice, "POST", path, map[string]any{"method": "guided", "remember": true}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "need a partitioned agent") {
		t.Fatalf("unpartitioned, Remember: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "POST", path, map[string]any{"method": "guided"}); w.Code != 202 {
		t.Fatalf("unpartitioned, the guided sign-in: %d %s", w.Code, w.Body)
	}
	// someone else who may use it doesn't drive alice's
	if w := callAs(t, mux, asBob, "POST", path, map[string]any{"method": "guided", "code": "good#x"}); w.Code == 200 {
		t.Fatalf("bob drove alice's sign-in: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "POST", path, map[string]any{"method": "guided", "code": "good-2#x"}); w.Code != 200 {
		t.Fatalf("alice's code: %d %s", w.Code, w.Body)
	}
	hAnswered(t, ag, run.ID, "account: home")
}

// An API key goes to the adapter's authenticate in the shape it reads:
// gemini-cli takes _meta["api-key"] as the key itself (it read an object as
// no key), codex-acp and the fake an object {apiKey}.
func TestAPIKeyMetaShape(t *testing.T) {
	ag, mux, box, m := harnessFixtureWith(t, nil, false, "--require-login")
	argv := acptest.Command("--require-login")
	m.Harnesses = append(m.Harnesses, fsbHarness{ID: "gemini", Title: "Gemini CLI", Argv: argv})
	for _, c := range []struct{ provider, shape string }{{"gemini", "string"}, {"fake", "object"}} {
		box2 := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "box-" + c.provider, Egress: "internet"})
		w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "hello", "class": "coding",
			"harness": map[string]any{"provider": c.provider}, "sandbox": map[string]any{"ref": sandboxRef("apps/cs", box2.ID)}})
		if w.Code != 200 {
			t.Fatalf("%s: ask: %d %s", c.provider, w.Code, w.Body)
		}
		var run Run
		_ = json.Unmarshal(w.Body.Bytes(), &run)
		parkOf(t, ag, run.ID, "login")
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/harness/authenticate", run.ID),
			map[string]any{"method": "fake-api-key", "apiKey": "k-" + c.provider}); w.Code != 200 {
			t.Fatalf("%s: authenticate: %d %s", c.provider, w.Code, w.Body)
		}
		b, err := os.ReadFile(filepath.Join(box2.Home, ".fakeacp", "credentials"))
		if err != nil || !strings.Contains(string(b), `"fake-api-key:`+c.shape+`"`) || strings.Contains(string(b), "k-"+c.provider) {
			t.Fatalf("%s: the key's shape: %q (%v), want %s", c.provider, b, err, c.shape)
		}
	}
	_ = box
}
