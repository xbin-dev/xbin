package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptExec is a GuidedExec a test scripts: say prints, typed reads what
// was written, end ends it.
type scriptExec struct {
	mu     sync.Mutex
	out    [][]byte
	wake   chan struct{}
	ended  bool
	exit   int
	typed  chan string
	closed bool
}

func newScriptExec() *scriptExec {
	return &scriptExec{wake: make(chan struct{}, 64), typed: make(chan string, 8)}
}

func (x *scriptExec) say(s string) {
	x.mu.Lock()
	x.out = append(x.out, []byte(s))
	x.mu.Unlock()
	x.wake <- struct{}{}
}

func (x *scriptExec) end(code int) {
	x.mu.Lock()
	x.ended, x.exit = true, code
	x.mu.Unlock()
	x.wake <- struct{}{}
}

func (x *scriptExec) Read(ctx context.Context) ([]byte, bool, *int, error) {
	for {
		x.mu.Lock()
		if len(x.out) > 0 {
			b := x.out[0]
			x.out = x.out[1:]
			x.mu.Unlock()
			return b, true, nil, nil
		}
		if x.ended || x.closed {
			code := x.exit
			x.mu.Unlock()
			return nil, false, &code, nil
		}
		x.mu.Unlock()
		select {
		case <-x.wake:
		case <-ctx.Done():
			return nil, true, nil, ctx.Err()
		}
	}
}

func (x *scriptExec) Write(_ context.Context, p []byte) error {
	x.typed <- string(p)
	return nil
}

func (x *scriptExec) Close() error {
	x.mu.Lock()
	x.closed = true
	x.mu.Unlock()
	x.wake <- struct{}{}
	return nil
}

const guidedURL = "https://claude.com/cai/oauth/authorize?code=true&client_id=test&state=s"

// fakeToken is an obviously fake setup-token in the shape Scan knows.
const fakeToken = "sk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE-not-a-real-token"

func TestGuidedMint(t *testing.T) {
	x := newScriptExec()
	g := &Guided{Name: "Claude Code", Spec: *claudeSetupToken, Exec: x}
	// as Claude Code 2.1.280's setup-token does on a terminal: bracketed
	// paste on, then the page and the prompt
	x.say("\x1b[?2004h\x1b]8;id=t;" + guidedURL + "\x07" + guidedURL + "\x1b]8;;\x07\r\n")
	x.say("\r\nPaste code here if prompted >\r\n")
	link, err := g.Start(context.Background(), 5*time.Second)
	if err != nil || link.URL != guidedURL || !link.Paste {
		t.Fatalf("Start: %+v, %v", link, err)
	}
	if again, err := g.Start(context.Background(), time.Second); err != nil || again != link {
		t.Fatalf("a second Start: %+v, %v", again, err)
	}
	typed := func(want ...string) {
		t.Helper()
		for _, w := range want {
			select {
			case got := <-x.typed:
				if got != w {
					t.Errorf("typed %q, want %q", got, w)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("never typed %q", w)
				return
			}
		}
	}

	// a malformed code: the CLI says so and waits for Enter; Code presses
	// it, the CLI asks again, the sign-in is still open
	go func() {
		typed("\x1b[200~half\x1b[201~", "\r")
		x.say("OAuth error: Invalid code. Please make sure the full code was copied\r\n")
		x.say("Press \x1b[1mEnter\x1b[22m to retry.\r\n")
		typed("\r")
		x.say("Retrying…\r\n" + guidedURL + "\r\nPaste code here if prompted >\r\n")
	}()
	_, err = g.Code(context.Background(), "half", 5*time.Second)
	var ge *GuidedError
	if !errors.As(err, &ge) || ge.Kind != GuidedInvalid || !ge.Open() {
		t.Fatalf("a malformed code: %v", err)
	}

	// the whole code: a bracketed paste, then Enter in a write of its own
	// (in one write with the code, Ink takes the Enter as part of the paste
	// and waits), the token read — the earlier refusal is not a failure
	go func() {
		typed("\x1b[200~good#state\x1b[201~", "\r")
		x.say("Your OAuth token (valid for 1 year):\r\n\r\n\x1b[38;5;220m" + fakeToken + "\x1b[39m\r\n")
		x.end(0)
	}()
	done, err := g.Code(context.Background(), "good#state", 5*time.Second)
	if err != nil || done.Token != fakeToken {
		t.Fatalf("Code: token %d bytes, %v", len(done.Token), err)
	}
	_ = g.Close()
	if g.out != nil || g.st.Token != "" {
		t.Fatal("Close kept what it read")
	}
	if _, err := g.Code(context.Background(), "x#y", time.Second); !errors.As(err, &ge) || ge.Kind != GuidedOver {
		t.Fatalf("a code after Close: %v", err)
	}
}

// A CLI that never asks again after a malformed code ends the sign-in with
// words for it, not a wait.
func TestGuidedMintRetryNeverAsks(t *testing.T) {
	x := newScriptExec()
	g := &Guided{Name: "Claude Code", Spec: *claudeSetupToken, Exec: x}
	x.say(guidedURL + "\r\nPaste code here if prompted >\r\n")
	if _, err := g.Start(context.Background(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	go func() {
		<-x.typed
		<-x.typed
		x.say("OAuth error: Invalid code. Please make sure the full code was copied\r\nPress Enter to retry.\r\n")
	}()
	_, err := g.Code(context.Background(), "half", 300*time.Millisecond)
	var ge *GuidedError
	if !errors.As(err, &ge) || ge.Kind != GuidedTimeout || ge.Open() || !strings.Contains(ge.Msg, "start the sign-in again") {
		t.Fatalf("a retry the CLI never answered: %v", err)
	}
}

// How a code is typed: one line over pipes; on a terminal the code and
// Enter apart, the code a bracketed paste where the CLI turned that on —
// which PasteMode follows through reads cut anywhere.
func TestTypedAndPasteMode(t *testing.T) {
	str := func(ws [][]byte) []string {
		var out []string
		for _, w := range ws {
			out = append(out, string(w))
		}
		return out
	}
	eq := func(got, want []string) bool {
		return strings.Join(got, "|") == strings.Join(want, "|") && len(got) == len(want)
	}
	if got := str(claudeSignin.Typed("a#b", false)); !eq(got, []string{"a#b\n"}) {
		t.Errorf("over pipes: %q", got)
	}
	if got := str(claudeSetupToken.Typed("a#b", false)); !eq(got, []string{"a#b", "\r"}) {
		t.Errorf("a terminal: %q", got)
	}
	if got := str(claudeSetupToken.Typed("a#b", true)); !eq(got, []string{"\x1b[200~a#b\x1b[201~", "\r"}) {
		t.Errorf("a terminal with bracketed paste: %q", got)
	}
	var p PasteMode
	if p.On() {
		t.Fatal("on before anything was read")
	}
	for _, b := range []string{"abc\x1b[?20", "04h", "def"} { // cut inside the switch
		p.Feed([]byte(b))
	}
	if !p.On() {
		t.Fatal("a switch cut across reads was missed")
	}
	p.Feed([]byte(strings.Repeat("x", 300000))) // far more than a reader keeps
	if !p.On() {
		t.Fatal("forgotten after more output")
	}
	p.Feed([]byte("\x1b[?2004h … \x1b[?2004l"))
	if p.On() {
		t.Fatal("off was missed")
	}
	p.Feed([]byte("\x1b[?2004l\x1b[?2004h"))
	if !p.On() {
		t.Fatal("the last switch in a read decides")
	}
}

// Claude Code 2.1.280's setup-token at 1000 columns, captured live with
// codes no sign-in page gave: a malformed code reads as invalid and not as
// a failure, with the line that waits for Enter and the prompt it then
// shows again; a refused code is the failure, in the CLI's words.
func TestSigninScanMintCaptures(t *testing.T) {
	read := func(name string) SigninState {
		t.Helper()
		out, err := os.ReadFile(filepath.Join("testdata", "signin", "mint", name))
		if err != nil {
			t.Fatal(err)
		}
		var p PasteMode
		p.Feed(out)
		if !p.On() {
			t.Errorf("%s: the CLI turned bracketed paste on", name)
		}
		return claudeSetupToken.Scan(out)
	}
	if st := read("setup-token-invalid-retry.raw"); st.Invalid < 1 || st.Failed != "" || st.Retry < 1 || st.Prompts < 2 || st.Token != "" {
		t.Errorf("a malformed code, Enter, the prompt again: %+v", st)
	}
	if st := read("setup-token-refused.raw"); st.Invalid != 0 || st.Failed != "OAuth error: Request failed with status code 400" || st.Token != "" {
		t.Errorf("a refused code: %+v", st)
	}
}

// A mint's errors never carry the CLI's last line (a credential in a format
// the scan doesn't know would be it), and a refusal's line is masked.
func TestGuidedMintErrorsHoldNoSecret(t *testing.T) {
	x := newScriptExec()
	g := &Guided{Name: "Claude Code", Spec: *claudeSetupToken, Exec: x}
	x.say(guidedURL + "\nPaste code here if prompted > ")
	if _, err := g.Start(context.Background(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	go func() { <-x.typed; x.say("\nYour token:\n\nnewformat-FAKE-FAKE-FAKE-zz\n"); x.end(0) }()
	_, err := g.Code(context.Background(), "a#b", 5*time.Second)
	var ge *GuidedError
	if !errors.As(err, &ge) || ge.Kind != GuidedEnded || strings.Contains(ge.Msg, "newformat") {
		t.Fatalf("an unknown token format: %v", err)
	}

	x = newScriptExec()
	g = &Guided{Name: "Claude Code", Spec: *claudeSetupToken, Exec: x}
	x.say(guidedURL + "\nPaste code here if prompted > ")
	_, _ = g.Start(context.Background(), 5*time.Second)
	go func() {
		<-x.typed
		x.say("\nOAuth error: bad exchange for sk-ant-api03-FAKEFAKEFAKEFAKEFAKE\n")
		x.end(1)
	}()
	_, err = g.Code(context.Background(), "a#b", 5*time.Second)
	if !errors.As(err, &ge) || ge.Kind != GuidedFailed || strings.Contains(ge.Msg, "FAKEFAKE") || !strings.Contains(ge.Msg, "[redacted]") {
		t.Fatalf("a refusal: %v", err)
	}
}

func TestGuidedLoginOverPipes(t *testing.T) {
	x := newScriptExec()
	g := &Guided{Name: "Claude Code", Spec: *claudeSignin, Exec: x}
	x.say("If the browser didn't open, visit: " + guidedURL + "\nPaste code here if prompted > ")
	if link, err := g.Start(context.Background(), 5*time.Second); err != nil || link.URL != guidedURL || !link.Paste {
		t.Fatalf("Start: %+v %v", link, err)
	}
	go func() {
		if got := <-x.typed; got != "good#s\n" {
			t.Errorf("typed %q", got)
		}
		x.say("Login successful.\n")
	}()
	if done, err := g.Code(context.Background(), "good#s", 5*time.Second); err != nil || done.Token != "" {
		t.Fatalf("Code: %v", err)
	}
}

func TestGuidedNoLink(t *testing.T) {
	x := newScriptExec()
	g := &Guided{Name: "Claude Code", Spec: *claudeSignin, Exec: x}
	x.say("error: unknown option '--claudeai'\n")
	x.end(1)
	_, err := g.Start(context.Background(), 5*time.Second)
	var ge *GuidedError
	if !errors.As(err, &ge) || ge.Kind != GuidedNoLink || !ge.Old || !strings.Contains(ge.Msg, "claude /exit") {
		t.Fatalf("an old CLI: %v", err)
	}

	x = newScriptExec()
	g = &Guided{Name: "Claude Code", Spec: *claudeSetupToken, Exec: x}
	if _, err := g.Start(context.Background(), 50*time.Millisecond); !errors.As(err, &ge) || ge.Kind != GuidedTimeout {
		t.Fatalf("a silent CLI: %v", err)
	}
	// Close while a call waits: the call ends
	go func() { time.Sleep(20 * time.Millisecond); _ = g.Close() }()
	if _, err := g.Start(context.Background(), 5*time.Second); err == nil {
		t.Fatal("Start after Close answered a link")
	}
}

func TestCleanCodeAndMask(t *testing.T) {
	if got := CleanCode(" ab c#d\r\n\x07"); got != "abc#d" {
		t.Fatalf("CleanCode: %q", got)
	}
	if got := MaskCredentials("key " + fakeToken + " and sk-proj-FAKEFAKEFAKEFAKEFAKE00 end"); strings.Contains(got, "FAKE") {
		t.Fatalf("MaskCredentials left one: %q", got)
	}
	argv := MintCommand(*claudeSetupToken, "/usr/bin:/bin")
	if argv[0] != "sh" || argv[4] != "/usr/bin:/bin" || argv[5] != "claude" || argv[6] != "setup-token" {
		t.Fatalf("MintCommand: %q", argv)
	}
}
