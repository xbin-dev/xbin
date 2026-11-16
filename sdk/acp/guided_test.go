package acp

import (
	"context"
	"errors"
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
	x.say("\x1b]8;id=t;" + guidedURL + "\x07" + guidedURL + "\x1b]8;;\x07\r\n")
	x.say("\r\nPaste code here if prompted >\r\n")
	link, err := g.Start(context.Background(), 5*time.Second)
	if err != nil || link.URL != guidedURL || !link.Paste {
		t.Fatalf("Start: %+v, %v", link, err)
	}
	if again, err := g.Start(context.Background(), time.Second); err != nil || again != link {
		t.Fatalf("a second Start: %+v, %v", again, err)
	}

	// a malformed code: asked again, the sign-in still open
	go func() { <-x.typed; x.say("Invalid code. Please make sure the full code was copied.\r\n") }()
	_, err = g.Code(context.Background(), "half", 5*time.Second)
	var ge *GuidedError
	if !errors.As(err, &ge) || ge.Kind != GuidedInvalid || !ge.Open() {
		t.Fatalf("a malformed code: %v", err)
	}

	// the whole code: typed with a carriage return (a terminal), the token read
	go func() {
		if got := <-x.typed; got != "good#state\r" {
			t.Errorf("typed %q", got)
		}
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
