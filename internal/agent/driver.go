package agent

import (
	"context"
	"errors"
	"io"
)

// Errors a driver returns that the API maps to a status.
var (
	ErrBusy  = errors.New("a turn is running — cancel it or wait for turn.end")
	ErrEnded = errors.New("the agent session has ended")
)

// Driver speaks one agent protocol on behalf of a session. Start spawns
// (through the Spawner) and handshakes; Send starts a turn with the user's
// text; Events is the typed stream (closed when the agent is gone);
// RespondPermission answers a request the driver surfaced as a
// permission.request event; Cancel interrupts the running turn; Close ends
// the agent. One implementation today (internal/agent/acp); a second is a
// second package implementing this and a Provider.driver naming it.
type Driver interface {
	Start(ctx context.Context, cfg Config) error
	Send(ctx context.Context, text string) error
	Events() <-chan Event
	RespondPermission(res *Resolution) error
	Cancel() error
	Close() error
}

// Config is what a session hands its driver.
type Config struct {
	Provider Provider
	Mode     string            // requested mode ("" = the provider's default)
	Cwd      string            // the agent's working directory (as the agent sees it)
	Env      []string          // the agent process env (sandbox env + provider keys)
	Argv     []string          // the agent command (Provider.Argv unless overridden)
	Spawn    Spawner           // how the process is started
	Perms    *Permissions      // the session's pending requests (the driver files into it)
	Version  string            // clientInfo.version
	Log      func(string)      // a line for the session's text log (stderr, notes)
	Meta     map[string]string // free-form, for a driver's own knobs
}

// Process is a spawned agent (or host) as the driver sees it: its stdio
// and a way to end it.
type Process struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stderr io.Reader // may be nil (already routed by the spawner)
	Wait   func() error
	Kill   func()
}

// Spawner starts the agent process for a Config. The term package's spawner
// launches the sandbox with `bx __agent-host` as the entry and hands the
// host {argv, env}; tests spawn a fake agent directly.
type Spawner func(ctx context.Context, cfg Config) (*Process, error)
