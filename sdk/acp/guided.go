package acp

// guided.go — driving a provider's sign-in for a person who never sees its
// terminal (D178, D179, D230): the host runs the provider's Signin — or its
// Mint, which prints a credential to keep — somewhere of its choosing and
// hands the running command over as a GuidedExec; Guided reads what it
// prints with Signin.Scan and answers the two things a sign-in card needs:
//
//   - Start: the page to open and whether the CLI asks for the code the
//     page shows;
//   - Code: the code typed in, and the CLI's word on it — signed in (a
//     Mint: the credential it printed), a malformed code (ask again), a
//     refusal with its reason line, or an end that said none of these.
//
// It keeps no I/O of its own and no clock but the waits it is given, so one
// driver serves a host that runs the command in a sandbox of its own
// (xbind's sign-in store) and one that runs it as a manager's exec (the
// agent template). What a Mint prints is a secret: Guided never puts the
// CLI's last line in an error of a Mint (a credential in a format Scan
// doesn't know would be that line), masks credential-shaped text in every
// line it does show, and Close drops everything it read.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// GuidedExec is a sign-in command under way, as its driver sees it.
type GuidedExec interface {
	// Read waits until the command printed more, ended, or ctx is done, and
	// answers the new bytes, whether it still runs and — once it ended and
	// the host knows it — its exit code. An ended command with nothing more
	// to read answers (nil, false, exit, nil).
	Read(ctx context.Context) (data []byte, running bool, exit *int, err error)
	// Write types p into the command.
	Write(ctx context.Context, p []byte) error
	// Close ends the command, if it still runs, and drops what the host
	// holds of its output.
	Close() error
}

// The kinds of GuidedError.
const (
	GuidedNoLink  = "no-link" // it ended, or the wait ran out, before it printed a sign-in page
	GuidedInvalid = "invalid" // the CLI refused the code as malformed and asks again: the sign-in is still open
	GuidedFailed  = "failed"  // the CLI gave up: Msg is its reason line
	GuidedEnded   = "ended"   // it ended without saying it signed in
	GuidedTimeout = "timeout" // no word from the CLI within the wait
	GuidedIO      = "io"      // the command could not be read or written
	GuidedOver    = "over"    // the sign-in was closed
)

// GuidedError is why a guided sign-in did not go on. Open reports whether
// the sign-in can still take another code (GuidedInvalid only).
type GuidedError struct {
	Kind string
	Msg  string
	// Old: the CLI ended without a link and the spec names a Fallback — it
	// may be too old for the command.
	Old bool
}

func (e *GuidedError) Error() string { return e.Msg }

// Open reports whether the sign-in still waits for a code.
func (e *GuidedError) Open() bool { return e.Kind == GuidedInvalid }

// GuidedLink is what a sign-in card shows first.
type GuidedLink struct {
	URL   string // the sign-in page: https, on the provider's hosts (Signin.Allowed)
	Paste bool   // the CLI asked for the code the page shows
}

// GuidedDone is a finished sign-in. Token is the credential a Mint printed
// ("" for a sign-in the CLI keeps in its $HOME) — a secret: keep it as the
// person's own, show it to no one.
type GuidedDone struct{ Token string }

// guidedOutMax is how much of the command's output is kept (the tail).
const guidedOutMax = 256 << 10

// Guided drives one sign-in.
type Guided struct {
	// Name is the provider's, for the words of an error ("Claude Code").
	Name string
	Spec Signin
	Exec GuidedExec

	mu      sync.Mutex // one call at a time drives it
	out     []byte
	st      SigninState
	running bool
	started bool
	exit    *int
	closed  bool
}

// mint reports a sign-in that prints a credential.
func (g *Guided) mint() bool { return g.Spec.Token != "" }

// Start waits (at most wait) for the CLI to print its sign-in page and to
// ask for the code. A second call answers what is already known.
func (g *Guided) Start(ctx context.Context, wait time.Duration) (GuidedLink, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return GuidedLink{}, &GuidedError{Kind: GuidedOver, Msg: "that sign-in is over — start one"}
	}
	if !g.started {
		g.started, g.running = true, true
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for g.running && (g.st.URL == "" || !g.st.Code) {
		if err := g.read(ctx); err != nil {
			break
		}
	}
	if g.st.URL != "" {
		return GuidedLink{URL: g.st.URL, Paste: g.st.Code}, nil
	}
	if !g.running {
		e := &GuidedError{Kind: GuidedNoLink, Msg: fmt.Sprintf("%s's sign-in ended without a link: %s", g.Name, orWords(g.lastWords(), "it printed nothing"))}
		if g.Spec.Fallback != "" && !g.mint() {
			e.Old = true
			e.Msg += " — this CLI may be too old for it: sign in in a terminal (" + g.Spec.Fallback + ")"
		}
		return GuidedLink{}, e
	}
	return GuidedLink{}, &GuidedError{Kind: GuidedTimeout, Msg: g.Name + " didn't print its sign-in link"}
}

// Link is the page and the prompt as last read (Start's answer again).
func (g *Guided) Link() GuidedLink {
	g.mu.Lock()
	defer g.mu.Unlock()
	return GuidedLink{URL: g.st.URL, Paste: g.st.Code}
}

// Code types code (CleanCode's) and Enter — a newline over pipes, a
// carriage return on a terminal (Ink's return) — and waits (at most wait)
// for the CLI's word on it.
func (g *Guided) Code(ctx context.Context, code string, wait time.Duration) (GuidedDone, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || !g.started {
		return GuidedDone{}, &GuidedError{Kind: GuidedOver, Msg: "that sign-in is over — start one"}
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	invalid := g.st.Invalid
	enter := "\n"
	if g.Spec.TTY {
		enter = "\r"
	}
	if err := g.Exec.Write(ctx, []byte(code+enter)); err != nil {
		return GuidedDone{}, &GuidedError{Kind: GuidedIO, Msg: fmt.Sprintf("couldn't hand %s the code: %v", g.Name, err)}
	}
	for {
		st := g.st
		switch {
		case g.mint() && st.Token != "":
			tok := st.Token
			g.st.Token = ""
			return GuidedDone{Token: tok}, nil
		case !g.mint() && st.Done:
			return GuidedDone{}, nil
		case st.Invalid > invalid:
			return GuidedDone{}, &GuidedError{Kind: GuidedInvalid, Msg: g.Name + " says that isn't the whole code — copy it again from the sign-in page and paste it"}
		case st.Failed != "":
			return GuidedDone{}, &GuidedError{Kind: GuidedFailed, Msg: MaskCredentials(st.Failed)}
		case !g.running:
			if !g.mint() && g.exit != nil && *g.exit == 0 {
				return GuidedDone{}, nil
			}
			what := "it ended"
			if g.mint() {
				what = "it ended without a token"
			}
			return GuidedDone{}, &GuidedError{Kind: GuidedEnded, Msg: fmt.Sprintf("%s's sign-in didn't finish: %s (%s)", g.Name, orWords(g.lastWords(), "no word from it"), what)}
		}
		if err := g.read(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return GuidedDone{}, &GuidedError{Kind: GuidedTimeout, Msg: g.Name + " didn't answer the code"}
			}
			return GuidedDone{}, &GuidedError{Kind: GuidedIO, Msg: fmt.Sprintf("reading %s's sign-in: %v", g.Name, err)}
		}
	}
}

// Close ends the sign-in: the command killed, everything read dropped.
// Safe to call more than once and while a call waits (that call ends with
// the command).
func (g *Guided) Close() error {
	err := g.Exec.Close() // first: a Start or Code that waits returns
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for i := range g.out {
		g.out[i] = 0
	}
	g.out, g.st = nil, SigninState{}
	return err
}

// read takes the command's output on and scans it again (g.mu held).
func (g *Guided) read(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, running, exit, err := g.Exec.Read(ctx)
	if len(b) > 0 {
		g.out = append(g.out, b...)
		if len(g.out) > guidedOutMax {
			g.out = append([]byte(nil), g.out[len(g.out)-guidedOutMax:]...)
		}
		g.st = g.Spec.Scan(g.out)
	}
	if err != nil {
		return err
	}
	g.running = running
	if exit != nil {
		g.exit = exit
	}
	return nil
}

// lastWords is the CLI's last line as an error may show it (g.mu held): a
// Mint's never, anything credential-shaped masked.
func (g *Guided) lastWords() string {
	if g.mint() {
		return ""
	}
	return MaskCredentials(g.st.Last)
}

func orWords(s, or string) string {
	if strings.TrimSpace(s) == "" {
		return or
	}
	return s
}

// CleanCode is a pasted sign-in code as a CLI reads it: one line, nothing
// but what a code is made of (a paste may carry spaces, a newline, a
// control character).
func CleanCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// credentialShapes are what the catalog's credentials look like in text:
// Anthropic's tokens and keys, and the sk-… keys of the other vendors.
var credentialShapes = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{12,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`AIza[A-Za-z0-9_-]{20,}`),
}

// MaskCredentials is text with every credential-shaped string in it
// replaced by "[redacted]": for a CLI's own words shown to a person or
// kept in a log.
func MaskCredentials(text string) string {
	for _, re := range credentialShapes {
		text = re.ReplaceAllString(text, "[redacted]")
	}
	return text
}

// mintScript runs a credential-printing sign-in clean ($1 the PATH, $2 the
// CLI, the rest its arguments): the CLI found on $1 only and run by its
// absolute path, in an environment of PATH, HOME, TERM and LANG alone — no
// NODE_OPTIONS, no LD_PRELOAD, nothing a profile set — with a throwaway
// HOME removed afterwards. The host gives it a terminal 1000 columns wide
// when the spec wants one (TTY), so the page's address and the credential
// are one line each.
const mintScript = `p=$1; shift
b=$(PATH=$p; command -v "$1") || { printf 'OAuth error: %s is not installed here (%s)\n' "$1" "$p"; exit 127; }
shift
h=$(mktemp -d) || exit 1
env -i PATH="$p" HOME="$h" TERM="${TERM:-xterm-256color}" LANG="${LANG:-C.UTF-8}" "$b" "$@"
c=$?
rm -rf -- "$h"
exit $c`

// MintCommand is spec's Argv wrapped so it runs clean (mintScript): the
// CLI looked up on path alone — the image's own directories, never a PATH
// something else could have put a shim on. Its Env is not passed: a
// credential-printing sign-in takes none.
func MintCommand(spec Signin, path string) []string {
	return append([]string{"sh", "-c", mintScript, "mint", path}, spec.Argv...)
}
