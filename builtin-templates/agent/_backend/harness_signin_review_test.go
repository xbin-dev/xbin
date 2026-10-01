package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp"
	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// The security review of saved sign-ins (D179's amendment): a regression
// test for each finding — M1–M3, L5–L10, L12–L13, N14, N18, N19 here and
// in harness_signin_test.go (TestCredGate: L9, L10, M1's gate;
// TestGuidedRemember: L12; TestSavedSigninRefused: N18).

// askWith starts a coding agent's conversation of harness provider in
// alice's partition.
func askWith(t *testing.T, h http.Handler, box *sbxSandbox, provider, text string) *Run {
	t.Helper()
	body := mustJSON(map[string]any{"text": text, "class": "coding", "harness": map[string]any{"provider": provider},
		"sandbox": map[string]any{"ref": sandboxRef("apps/cs", box.ID)}})
	var run Run
	serveJSON(t, h, as("POST", "/ask", body, alicesFrame("read")), 200, &run)
	return &run
}

// pasteSignin saves a pasted secret as alice's sign-in name for harness: its id.
func pasteSignin(t *testing.T, h http.Handler, harness, name, secret string) string {
	t.Helper()
	code, body := aliceCall(t, h, "POST", "/prefs/harness-signins", map[string]any{"harness": harness, "name": name, "secret": secret})
	var made struct{ Signin hSignin }
	_ = json.Unmarshal([]byte(body), &made)
	if code != 201 || made.Signin.ID == "" {
		t.Fatalf("pasting %s: %d %s", name, code, body)
	}
	return made.Signin.ID
}

// tell sends alice's next message to run.
func tell(t *testing.T, h http.Handler, run *Run, text string) {
	t.Helper()
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), mustJSON(map[string]any{"text": text}), alicesFrame("read")), 200, nil)
}

// pick picks the saved sign-in run's coding agent uses, and waits for the
// adapter to be down for it (one parked on its sign-in starts again at
// once, as a Retry does: no wait).
func pick(t *testing.T, ag *Agent, h http.Handler, run *Run, signin string, wait bool) {
	t.Helper()
	if code, body := aliceCall(t, h, "PUT", fmt.Sprintf("/runs/%d/harness/signin", run.ID), map[string]any{"signin": signin}); code != 200 {
		t.Fatalf("picking %s: %d %s", signin, code, body)
	}
	if wait {
		hwait(t, "the adapter down for the pick", func() bool {
			hs, _ := ag.db.harnessSession(run.ID)
			return hs != nil && !hsRunning(hs.State)
		})
	}
}

// execGone: the manager no longer has exec id in box (deleted: its group killed).
func execGone(t *testing.T, box *sbxSandbox, id string) bool {
	t.Helper()
	c, err := sbxDial("apps/cs", "alice")
	if err != nil {
		t.Fatal(err)
	}
	x, err := c.ExecGet(context.Background(), box.ID, id)
	return sbxRefusal(err) == "not-found" || (err == nil && x.State != "running")
}

// --- M1 -----------------------------------------------------------------------------------

// A hosted (non-secure) conversation that works in a sandbox of its host's
// flags it, before it does anything there: no saved sign-in ever goes into
// a coding agent there, nor does Remember mint one there — its members could
// have left something that reads the next process's environment.
func TestCredHostedSandbox(t *testing.T) {
	testClaude(t)
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	pasteSignin(t, h, "fake", "Personal", "sk-ant-oat01-hostedcase-AAAA")
	used := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "used", Egress: "internet"})
	ref := sandboxRef("apps/cs", used.ID)
	cfg := defaultConfig()
	bd := SandboxBinding{Ref: ref, Name: used.Name, Egress: "internet"}
	cfg.Class, cfg.Sandbox, cfg.Attached = "coding", &bd, []SandboxBinding{bd}
	if _, err := ag.sandboxUse(context.Background(), teamIDBase+7, cfg, ""); err != nil && strings.Contains(err.Error(), "non-secure") {
		t.Fatalf("a hosted conversation in a sandbox no coding agent used: %v", err)
	}
	if !ag.db.hostedUsed(ref) {
		t.Fatal("the hosted conversation's use wasn't recorded")
	}
	run := askIn(t, h, used, "whoami")
	parkOf(t, ag, run.ID, "login") // its own $HOME isn't signed in, and the saved one stays out
	if hs, _ := ag.db.harnessSession(run.ID); hs.Cred != "" {
		t.Fatalf("a sandbox a hosted conversation used got the saved sign-in: %q", hs.Cred)
	}
	if n := notesOf(t, ag.db, run.ID); !strings.Contains(n, "a non-secure (hosted) conversation has worked in used") {
		t.Fatalf("no word why: %s", n)
	}
	code, body := aliceCall(t, h, "POST", fmt.Sprintf("/runs/%d/harness/authenticate", run.ID), map[string]any{"method": "guided", "remember": true})
	if code != 409 || !strings.Contains(body, "non-secure (hosted)") {
		t.Fatalf("Remember there: %d %s", code, body)
	}
	// control: her own sandbox no hosted conversation used
	mine := askIn(t, h, box, "whoami")
	hAnswered(t, ag, mine.ID, "account: token …AAAA")
}

// --- M2: codex keeps a key it is handed in auth.json -------------------------------------

// codexIn adds a fake that signs in as codex 0.156 does (--codex-auth) to the
// manager as harness "codex": the catalog's Codex, its AuthFile.
func codexIn(t *testing.T, m *sbxTestManager) {
	t.Helper()
	m.Harnesses = append(m.Harnesses, fsbHarness{ID: "codex", Title: "Codex", Argv: acptest.Command("--require-login", "--codex-auth", "--persist")})
	forgetHarnessProbes()
}

// writeCodexAuth leaves key in the sandbox's auth.json, as codex writes it.
func writeCodexAuth(t *testing.T, file, key string) {
	t.Helper()
	b, _ := json.MarshalIndent(map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": key, "tokens": nil}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileGone(file string) bool { _, err := os.Stat(file); return errors.Is(err, os.ErrNotExist) }

// Codex takes a saved API key only through authenticate and writes it to
// its auth.json: the file goes at once (codex keeps the key in memory), and
// before each start with another sign-in — so a switch really switches
// account, and the sandbox's own sign-in is the sandbox's own. Codex signed
// in on its own is left as it is, and the saved sign-in isn't named as the
// one it uses.
func TestCodexKeySwitch(t *testing.T) {
	_ = memVault(t)
	ag, h, box, m, _, _ := harnessPartitionG(t)
	codexIn(t, m)
	auth := filepath.Join(box.Home, ".codex", "auth.json")
	personal := pasteSignin(t, h, "codex", "Personal", "sk-proj-personal-AAAA")
	work := pasteSignin(t, h, "codex", "Work", "sk-proj-company-BBBB")
	run := askWith(t, h, box, "codex", "whoami")
	hAnswered(t, ag, run.ID, "account: key …AAAA")
	if !fileGone(auth) {
		t.Fatal("the key stayed in auth.json after codex took it")
	}
	if hs, _ := ag.db.harnessSession(run.ID); hs.Cred != personal {
		t.Fatalf("the first start's sign-in: %q", hs.Cred)
	}
	// the switch, with the file left as codex wrote it (a removal that failed)
	writeCodexAuth(t, auth, "sk-proj-personal-AAAA")
	pick(t, ag, h, run, work, true)
	tell(t, h, run, "whoami now")
	hAnswered(t, ag, run.ID, "account: key …BBBB")
	if !fileGone(auth) {
		t.Fatal("Work's key stayed in auth.json")
	}
	// the sandbox's own: no saved key of hers left for codex to start with
	writeCodexAuth(t, auth, "sk-proj-company-BBBB")
	pick(t, ag, h, run, "sandbox", true)
	tell(t, h, run, "whoami again")
	parkOf(t, ag, run.ID, "login")
	if !fileGone(auth) {
		t.Fatal("Work's key outlived the pick of the sandbox's own sign-in")
	}
	// codex signed in on its own: that sign-in, said so — never overwritten, never named as hers
	writeCodexAuth(t, auth, "sk-proj-own-CCCC")
	pick(t, ag, h, run, personal, false) // parked: it starts again with the pick, the held prompt resent
	hAnswered(t, ag, run.ID, "account: key …CCCC")
	hs, _ := ag.db.harnessSession(run.ID)
	if hs.Cred != "" || !strings.Contains(notesOf(t, ag.db, run.ID), "is signed in in this sandbox on its own") {
		t.Fatalf("codex's own sign-in: cred %q\n%s", hs.Cred, notesOf(t, ag.db, run.ID))
	}
	if b, err := os.ReadFile(auth); err != nil || !strings.Contains(string(b), "own-CCCC") {
		t.Fatalf("codex's own sign-in was touched: %q %v", b, err)
	}
}

// Forget reaches a key codex kept in a file, and a share through the agent
// removes it before the sandbox is shared — or refuses the share when it
// can't (the review's M2, L8).
func TestCodexKeyForgetShare(t *testing.T) {
	_ = memVault(t)
	ag, h, box, m, _, _ := harnessPartitionG(t)
	codexIn(t, m)
	auth := filepath.Join(box.Home, ".codex", "auth.json")
	personal := pasteSignin(t, h, "codex", "Personal", "sk-proj-personal-AAAA")
	run := askWith(t, h, box, "codex", "whoami")
	hAnswered(t, ag, run.ID, "account: key …AAAA")
	writeCodexAuth(t, auth, "sk-proj-personal-AAAA") // a removal that failed
	if code, body := aliceCall(t, h, "DELETE", "/prefs/harness-signins/"+personal, nil); code != 200 {
		t.Fatalf("Forget: %d %s", code, body)
	}
	if !fileGone(auth) {
		t.Fatal("Forget left the key in auth.json")
	}
	work := pasteSignin(t, h, "codex", "Work", "sk-proj-company-BBBB")
	hwait(t, "the forgotten one's adapter stopped", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && !hsRunning(hs.State)
	})
	pick(t, ag, h, run, work, true)
	tell(t, h, run, "whoami now")
	hAnswered(t, ag, run.ID, "account: key …BBBB")
	hs, _ := ag.db.harnessSession(run.ID)
	writeCodexAuth(t, auth, "sk-proj-company-BBBB") // a removal that failed
	path := "/sandboxes/" + sandboxRef("apps/cs", box.ID)
	if os.Geteuid() != 0 { // root removes it whatever the mode
		_ = os.Chmod(filepath.Dir(auth), 0o500)
		code, body := aliceCall(t, h, "PATCH", path, map[string]any{"members": []string{"bob"}})
		_ = os.Chmod(filepath.Dir(auth), 0o700)
		if code != 502 || !strings.Contains(body, "isn't shared") {
			t.Fatalf("a share whose removal failed: %d %s", code, body)
		}
		c, _ := sbxDial("apps/cs", "alice")
		if b, err := c.Get(context.Background(), box.ID); err != nil || len(b.Members) != 0 {
			t.Fatalf("shared all the same: %+v %v", b, err)
		}
	}
	if code, body := aliceCall(t, h, "PATCH", path, map[string]any{"members": []string{"bob"}}); code != 200 {
		t.Fatalf("the share: %d %s", code, body)
	}
	if !fileGone(auth) || !execGone(t, box, hs.ExecID) {
		t.Fatalf("shared with the key in auth.json (gone %v) or its adapter up (gone %v)", fileGone(auth), execGone(t, box, hs.ExecID))
	}
}

// --- M3: the secret printed ----------------------------------------------------------------

// A tool (or a hook, or a prompt-injected command) prints the adapter's
// environment: the saved secret is masked in everything AgTT keeps or shows
// — the transcript's rows, the answers the page reads, the adapter's log,
// this process's log.
func TestSecretRedacted(t *testing.T) {
	_ = memVault(t)
	logs := captureLog(t)
	ag, h, box, _, _, _ := harnessPartitionG(t)
	const secret = "sk-ant-oat01-SECRETSECRETSECRET-redact"
	pasteSignin(t, h, "fake", "Personal", secret)
	run := askIn(t, h, box, "printenv CLAUDE_CODE_OAUTH_TOKEN")
	hAnswered(t, ag, run.ID, "printenv: "+redactMark)
	var seen []string
	for _, p := range []string{fmt.Sprintf("/runs/%d", run.ID), fmt.Sprintf("/runs/%d/view", run.ID), fmt.Sprintf("/runs/%d/harness", run.ID)} {
		code, body := aliceCall(t, h, "GET", p, nil)
		if code != 200 {
			t.Fatalf("GET %s: %d %s", p, code, body)
		}
		seen = append(seen, body)
	}
	hwait(t, "the hook's line in the adapter's log", func() bool {
		_, body := aliceCall(t, h, "GET", fmt.Sprintf("/runs/%d/harness/log", run.ID), nil)
		if strings.Contains(body, "hook: CLAUDE_CODE_OAUTH_TOKEN=") {
			seen = append(seen, body)
			return true
		}
		return false
	})
	if !strings.Contains(seen[len(seen)-1], "hook: CLAUDE_CODE_OAUTH_TOKEN="+redactMark) {
		t.Fatalf("the log's line isn't masked: %s", seen[len(seen)-1])
	}
	seen = append(seen, fullText(ag.db, run.ID), logs.String())
	for i, s := range seen {
		if strings.Contains(s, "SECRETSECRETSECRET") {
			t.Fatalf("%d holds the secret: %s", i, s)
		}
	}
	if at := dbHolds(t, ag.db, "SECRETSECRETSECRET"); at != "" {
		t.Fatalf("the secret is stored in %s", at)
	}
}

// The redaction keeps the stream's length and its lines whole: the exact
// secrets, and any Anthropic-token-shaped string; a line split across reads
// is redacted whole; a ring gap goes on once, after the bytes before it.
func TestRedactReader(t *testing.T) {
	const secret = "sk-proj-exact-0123456789"
	in := "{\"a\":\"" + secret + "\"}\n{\"b\":\"sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUV\"}\n{\"c\":\"plain\"}\n"
	for _, chunk := range []int{1, 3, 7, len(in)} {
		r := newRedactReader(&chunker{b: []byte(in), n: chunk}, func() *redactor { return newRedactor(secret) })
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != len(in) || strings.Contains(string(out), "exact-0123") || strings.Contains(string(out), "ABCDEFGHIJ") ||
			!strings.Contains(string(out), `{"c":"plain"}`) || strings.Count(string(out), redactMark) != 2 {
			t.Fatalf("chunks of %d: %q", chunk, out)
		}
	}
	// a gap: the bytes before it, then the gap once, then the rest
	g := &acp.Gap{Lost: 5}
	r := newRedactReader(&gapper{parts: []any{[]byte("{\"x\":\"" + secret), g, []byte("\"}\n")}}, func() *redactor { return newRedactor(secret) })
	var got bytes.Buffer
	var gaps int
	buf := make([]byte, 4)
	for {
		n, err := r.Read(buf)
		got.Write(buf[:n])
		var ge *acp.Gap
		if errors.As(err, &ge) {
			gaps++
			continue
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if gaps != 1 || strings.Contains(got.String(), "exact-0123") || got.Len() != len("{\"x\":\""+secret+"\"}\n") {
		t.Fatalf("across a gap: %d gaps, %q", gaps, got.String())
	}
	if s := redactText("a short one: abc, a token sk-ant-oat01-abcdefghijklmnopqrstu"); strings.Contains(s, "abcdefghijklmnop") ||
		!strings.Contains(s, "abc,") {
		t.Fatalf("redactText: %q", s)
	}
}

// chunker reads b n bytes at a time.
type chunker struct {
	b []byte
	n int
}

func (c *chunker) Read(p []byte) (int, error) {
	if len(c.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), c.n)], c.b)
	c.b = c.b[n:]
	return n, nil
}

// gapper reads its parts in order: bytes, or an error returned once.
type gapper struct{ parts []any }

func (g *gapper) Read(p []byte) (int, error) {
	if len(g.parts) == 0 {
		return 0, io.EOF
	}
	switch x := g.parts[0].(type) {
	case []byte:
		n := copy(p, x)
		if n == len(x) {
			g.parts = g.parts[1:]
		} else {
			g.parts[0] = x[n:]
		}
		return n, nil
	case error:
		g.parts = g.parts[1:]
		return 0, x
	}
	return 0, io.EOF
}

// --- L5, L6, L7, N19: the guided sign-in's exec ----------------------------------------------

// Remember checks its gate again before the code goes in: a sandbox shared
// at its manager meanwhile ends the mint, nothing saved. A share through the
// agent ends a sign-in under way there before it goes out, its exec deleted.
func TestMintGateRechecked(t *testing.T) {
	testClaude(t)
	vault := memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	run := askIn(t, h, box, "whoami")
	parkOf(t, ag, run.ID, "login")
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "remember": true}); code != 202 {
		t.Fatalf("the mint's start: %d %s", code, body)
	}
	c, err := sbxDial("apps/cs", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Patch(context.Background(), box.ID, sbxPatch{Members: &[]string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	invalidateSandboxCatalog()
	code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "good-L5L5#test-state"})
	if code != 409 || !strings.Contains(body, "that sign-in was ended") {
		t.Fatalf("the code after the share: %d %s", code, body)
	}
	hwait(t, "the mint's exec deleted", func() bool { return len(signinExecs(t, box)) == 0 })
	if len(vault.m) != 0 || len(ag.db.signins("alice", "")) != 0 {
		t.Fatalf("a mint in a shared sandbox was saved: %v", vault.m)
	}
	// private again; a share through the agent while one waits ends it first
	if _, err := c.Patch(context.Background(), box.ID, sbxPatch{Members: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	invalidateSandboxCatalog()
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "remember": true}); code != 202 {
		t.Fatalf("the mint's start again: %d %s", code, body)
	}
	if code, body := aliceCall(t, h, "PATCH", "/sandboxes/"+sandboxRef("apps/cs", box.ID), map[string]any{"members": []string{"bob"}}); code != 200 {
		t.Fatalf("the share: %d %s", code, body)
	}
	if ag.eng.guidedOf(run.ID) != nil || len(signinExecs(t, box)) != 0 {
		t.Fatalf("the share went out with a sign-in under way: %v", signinExecs(t, box))
	}
}

// A sign-in's exec is recorded before it starts and until it is deleted: a
// later start sweeps what an earlier process left — found by its clientId
// when its id never came back. One started for a request already gone is
// ended all the same, its record with it (N19).
func TestSigninExecSwept(t *testing.T) {
	testClaude(t)
	ag, _, box, _, _, _ := harnessPartitionG(t, "--require-login")
	c, err := sbxDial("apps/cs", "alice")
	if err != nil {
		t.Fatal(err)
	}
	ref := sandboxRef("apps/cs", box.ID)
	left, err := c.ExecStart(context.Background(), box.ID, sbxExecReq{Argv: []string{"sleep", "300"}, Label: "sign-in fake · #1",
		ClientID: "signin:1:1"})
	if err != nil {
		t.Fatal(err)
	}
	noteSigninExec("signin:1:1", ref, "", "alice") // its id never came back
	ag.eng.sweepSigninExecs(nowMs() + 1)
	if !execGone(t, box, left.ID) {
		t.Fatal("the sweep left the sign-in's exec")
	}
	if n := dbStrings(t, ag.db, `SELECT client FROM harness_signin_execs`); len(n) != 0 {
		t.Fatalf("the sweep left its record: %v", n)
	}
	// a request gone before its start returned
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := &Run{ID: partitionIDBase + 77, Owner: "alice", Engine: engineHarness}
	if _, err := ag.eng.guidedStart(ctx, run, harnessProvider("fake", nil), c, box, "", "alice", false, ""); err == nil {
		t.Fatal("a sign-in for a request gone answered")
	}
	hwait(t, "its exec and record gone", func() bool {
		return len(signinExecs(t, box)) == 0 && len(dbStrings(t, ag.db, `SELECT client FROM harness_signin_execs`)) == 0
	})
}

// A mint that ends without a token it knows never shows the CLI's last
// line — a token in a format the scan doesn't know would be that line (L7).
func TestMintErrorHidesLast(t *testing.T) {
	testClaude(t)
	vault := memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	run := askIn(t, h, box, "whoami")
	parkOf(t, ag, run.ID, "login")
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	if code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "remember": true}); code != 202 {
		t.Fatalf("the mint's start: %d %s", code, body)
	}
	code, body := aliceCall(t, h, "POST", path, map[string]any{"method": "guided", "code": "newformat#test-state"})
	if code != 502 || strings.Contains(body, "NEWFORMAT") || !strings.Contains(body, "without a token") {
		t.Fatalf("a mint's end without a token it knows: %d %s", code, body)
	}
	if len(vault.m) != 0 {
		t.Fatalf("saved: %v", vault.m)
	}
}

// --- L8: a share stops the adapters first ---------------------------------------------------

// A share through the agent's route kills the coding agents holding a saved
// sign-in there before the PATCH goes out: when it answers, they are gone.
func TestShareStopsFirst(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	pasteSignin(t, h, "fake", "Personal", "sk-ant-oat01-sharefirst-DDDD")
	run := askIn(t, h, box, "whoami")
	hAnswered(t, ag, run.ID, "account: token …DDDD")
	hs, _ := ag.db.harnessSession(run.ID)
	if code, body := aliceCall(t, h, "PATCH", "/sandboxes/"+sandboxRef("apps/cs", box.ID), map[string]any{"visibility": "team"}); code != 200 {
		t.Fatalf("the share: %d %s", code, body)
	}
	if !execGone(t, box, hs.ExecID) {
		t.Fatal("the adapter holding the saved sign-in was still up when the share answered")
	}
}

// --- L13: a person's partition stopping ------------------------------------------------------

// A person's partition stopping stops the adapters that rest with a saved
// sign-in in their environment (a deleted person's partition never comes
// back to stop them); one without is let go to the successor as ever.
func TestShutdownStopsRestingCreds(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t)
	plain := askIn(t, h, box, "whoami")
	hAnswered(t, ag, plain.ID, "account: ")
	pasteSignin(t, h, "fake", "Personal", "sk-ant-oat01-shutdown-EEEE")
	other := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "other", Egress: "internet"})
	run := askIn(t, h, other, "whoami")
	hAnswered(t, ag, run.ID, "account: token …EEEE")
	hwait(t, "both rest", func() bool { return turnOver(ag, run.ID)() && turnOver(ag, plain.ID)() })
	withCred, _ := ag.db.harnessSession(run.ID)
	without, _ := ag.db.harnessSession(plain.ID)
	if without.Cred != "" || withCred.Cred == "" {
		t.Fatalf("the fixture: %q %q", without.Cred, withCred.Cred)
	}
	ag.eng.BeginShutdown()
	if !execGone(t, other, withCred.ExecID) {
		t.Fatal("an adapter holding a saved sign-in outlived its partition's stop")
	}
	if hs, _ := ag.db.harnessSession(run.ID); hs.State != hsStopped {
		t.Fatalf("its state: %s", hs.State)
	}
	if execGone(t, box, without.ExecID) {
		t.Fatal("an adapter without one was stopped too (it is the successor's)")
	}
}

// --- N14: a secret replaced ------------------------------------------------------------------

// Replacing a saved sign-in's secret — pasted over it, or saved again under
// its name — stops the coding agents that started with the old one; the
// next message starts each with the new.
func TestReplacedSecretStops(t *testing.T) {
	_ = memVault(t)
	ag, h, box, _, _, _ := harnessPartitionG(t, "--require-login")
	id := pasteSignin(t, h, "fake", "Personal", "sk-ant-oat01-replace-AAAA")
	run := askIn(t, h, box, "whoami")
	hAnswered(t, ag, run.ID, "account: token …AAAA")
	stopped := func(what string) {
		t.Helper()
		hwait(t, "the adapter stopped: "+what, func() bool {
			hs, _ := ag.db.harnessSession(run.ID)
			return hs != nil && !hsRunning(hs.State) && strings.Contains(hs.Error, "was replaced")
		})
	}
	if code, body := aliceCall(t, h, "PUT", "/prefs/harness-signins/"+id, map[string]any{"secret": "sk-ant-oat01-replace-BBBB"}); code != 200 {
		t.Fatalf("pasting over it: %d %s", code, body)
	}
	stopped("pasted over")
	tell(t, h, run, "whoami now")
	hAnswered(t, ag, run.ID, "account: token …BBBB")
	if again := pasteSignin(t, h, "fake", "Personal", "sk-ant-oat01-replace-CCCC"); again != id {
		t.Fatalf("saved again under its name: a new one %q", again)
	}
	stopped("saved again")
	tell(t, h, run, "whoami last")
	hAnswered(t, ag, run.ID, "account: token …CCCC")
}

// A pasted secret holding a character a JSON file escapes is refused: a
// CLI's key file holds it verbatim, so its removal finds it.
func TestCleanSecretJSONSafe(t *testing.T) {
	for _, s := range []string{`sk-a"b`, `sk-a\b`, "sk-a<b", "sk-a>b", "sk-a&b"} {
		if cleanSecret(s) != "" {
			t.Errorf("%q was taken", s)
		}
	}
	if cleanSecret(" sk-proj-ok_-.0 ") != "sk-proj-ok_-.0" {
		t.Error("a plain key was refused")
	}
}
