package acptest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// Options are the scripted agent's switches — Main reads them from flags
// (ParseArgs). The zero value is the plain agent.
type Options struct {
	Steer        bool // --steer: serve _session/steering
	AutoMode     bool // --auto-mode: an "auto" mode that skips an edit's permission
	RequireLogin bool // --require-login: signed in only with $HOME/.fakeacp/credentials
	Persist      bool // --persist: sessions in $HOME/.fakeacp/sessions, replayed by session/load
	// DeviceDelay is how long after the client accepts the device-code URL
	// the sign-in completes (--device-ms): 0 = 1 s, negative = at once.
	DeviceDelay time.Duration
	// Getenv is the environment the scripts read: HOME (where .fakeacp/
	// lives, and what env reports) and FAKE_API_KEY. nil = os.Getenv.
	Getenv func(string) string
	// Self is the command that starts this agent (a binary, plus
	// AdapterArg for a test binary): fake-login's terminal-auth _meta runs
	// Self + "login". Main sets it; empty leaves the _meta out.
	Self []string

	wait func(time.Duration) // tests: stands in for the scripts' timers
}

// AdapterArg is os.Args[1] of a test binary started as the agent (Command).
const AdapterArg = "acptest"

// ExitError is a script's exit (crash): Serve returns it, Main exits with Code.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return "acptest: the agent exited " + strconv.Itoa(e.Code) }

// Serve plays the agent on r (the client's frames) and w (the agent's)
// until r ends, or a script exits (*ExitError). Once it returns nothing
// more is written; after an exit the caller closes r to release the reader.
func Serve(r io.Reader, w io.Writer, o Options) error {
	f := newFake(o)
	out := &gate{w: w}
	f.conn = acp.NewConn(r, out)
	f.conn.OnRequest = f.onRequest
	f.conn.OnNotify = f.onNotify
	served := make(chan error, 1)
	go func() { served <- f.conn.Serve() }()
	var err error
	select {
	case err = <-served:
	case code := <-f.crash:
		err = &ExitError{Code: code}
	}
	out.close()
	close(f.done)
	return err
}

// gate is the agent's output, shut when Serve returns.
type gate struct {
	mu     sync.Mutex
	w      io.Writer
	closed bool
}

func (g *gate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return 0, io.ErrClosedPipe
	}
	return g.w.Write(p)
}

func (g *gate) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// ParseArgs reads the flags (--steer, --auto-mode, --require-login,
// --persist, --device-ms=N; one dash works too); anything else is ignored.
func ParseArgs(args []string) Options {
	var o Options
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		on := true
		if hasVal {
			if b, err := strconv.ParseBool(val); err == nil {
				on = b
			}
		}
		switch name {
		case "steer":
			o.Steer = on
		case "auto-mode":
			o.AutoMode = on
		case "require-login":
			o.RequireLogin = on
		case "persist":
			o.Persist = on
		case "device-ms":
			if !hasVal && i+1 < len(args) {
				if _, err := strconv.Atoi(args[i+1]); err == nil {
					val = args[i+1]
					i++
				}
			}
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				o.DeviceDelay = time.Duration(n) * time.Millisecond
				if n == 0 {
					o.DeviceDelay = -1
				}
			}
		}
	}
	return o
}

// Main is the agent as a program: `[acptest] [flags…]` serves stdio (a
// greeting on stderr), `[acptest] login` is the terminal sign-in. It exits
// the process: 0 when stdin ends, a script's code when it exits.
func Main() {
	args := os.Args[1:]
	self := []string{os.Args[0]}
	if exe, err := os.Executable(); err == nil {
		self[0] = exe
	}
	if len(args) > 0 && args[0] == AdapterArg {
		self = append(self, AdapterArg)
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "login" {
		os.Exit(Login(os.Stdin, os.Stdout, os.Getenv("HOME")))
	}
	o := ParseArgs(args)
	o.Self = self
	fmt.Fprintln(os.Stderr, "fakeacp: up")
	err := Serve(os.Stdin, os.Stdout, o)
	var exit *ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.Code)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeacp:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// MainIfAdapter runs Main (which exits) when this test binary was started
// as the agent — os.Args[1] is AdapterArg, as Command starts it — and
// returns otherwise. Call it first in TestMain.
func MainIfAdapter() {
	if len(os.Args) > 1 && os.Args[1] == AdapterArg {
		Main()
	}
}

// Command is the argv that starts this test binary as the agent with flags
// (its TestMain calls MainIfAdapter): for a harness entry, XBIN_AGENT_FAKE
// or a fake sandbox's exec.
func Command(flags ...string) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return append([]string{exe, AdapterArg}, flags...)
}

// Login is the terminal sign-in (`fakeacp login`): it asks for a code on
// out and reads a line from in; "fake-code" writes <home>/.fakeacp/credentials
// and answers 0, anything else 1.
func Login(in io.Reader, out io.Writer, home string) int {
	fmt.Fprintln(out, "Open https://example.invalid/login and paste the code:")
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) != "fake-code" {
		fmt.Fprintln(out, "Wrong code.")
		return 1
	}
	if err := writeCredentials(home, "fake-login"); err != nil {
		fmt.Fprintln(out, "Could not save the sign-in:", err)
		return 1
	}
	fmt.Fprintln(out, "Signed in.")
	return 0
}
