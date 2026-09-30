package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

// Errors the Client returns that an API maps to a status.
var (
	ErrBusy              = errors.New("a turn is running — cancel it or wait for turn.end")
	ErrEnded             = errors.New("the agent session has ended")
	ErrResumeUnsupported = errors.New("this agent cannot reopen an earlier session (no loadSession capability) — start a new one")
	ErrNoElicitation     = errors.New("no such pending question")
	ErrCancelled         = errors.New("the prompt was cancelled before its turn started")
	ErrNotReady          = errors.New("the agent has not answered initialize yet")
)

// Config is one session's setup: which agent, where, and how it is started.
type Config struct {
	Provider Provider
	Mode     string            // requested mode ("" = the provider's default)
	Options  map[string]string // requested config options at start (model, effort, …), applied after session/new
	ResumeID string            // the agent's own earlier session id to reopen (session/load) instead of starting fresh
	Cwd      string            // the agent's working directory (as the agent sees it)
	Env      []string          // the agent process env
	Argv     []string          // the agent command (Provider.Argv unless overridden)
	Spawn    Spawner           // how the process is started
	Perms    *Permissions      // the session's pending requests (the client files into it; required)
	Version  string            // clientInfo.version
	Log      func(string)      // a line for the session's text log (stderr, notes)
	Meta     map[string]string // free-form, for the embedder's own knobs (xbind: "tile")
	// SkipModeOptions: Options never set the agent's config option of
	// category mode — the mode is Mode's alone (an embedder that keeps some
	// modes to some people); such an entry is skipped and logged.
	SkipModeOptions bool
}

// Process is a spawned agent as the client sees it: its stdio and a way to
// end it.
type Process struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stderr io.Reader // may be nil (already routed by the spawner)
	Wait   func() error
	Kill   func()
	// Off is Stdout's position in the agent's whole output: 0 for a fresh
	// process, where a reattached pipe resumes reading (ClientOptions.Attach).
	// Event.Wire.Off counts from it.
	Off int64
}

// Spawner starts the agent process for a Config: locally, in a sandbox, on
// another machine — anything that yields its stdio.
type Spawner func(ctx context.Context, cfg Config) (*Process, error)

// Elicitation is a question the agent is waiting on: an
// elicitation.request's payload, until it is answered. A form (the
// default) asks for values matching Schema; a url one (Mode "url", only
// with ElicitationCaps.URL advertised) asks the person to open URL — a
// device-code sign-in — and ends with the agent's elicitation/complete.
type Elicitation struct {
	EID           string          `json:"eid"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Message       string          `json:"message"`
	Schema        json.RawMessage `json:"schema"`
	Mode          string          `json:"mode,omitempty"`          // "" (form) | "url"
	URL           string          `json:"url,omitempty"`           // url mode: what to open
	ElicitationID string          `json:"elicitationId,omitempty"` // url mode: the agent's id, named by elicitation/complete
}

// ClientOptions are an embedder's seams (NewWith). The zero value of every
// field is the default, and New is NewWith(ClientOptions{}).
type ClientOptions struct {
	// Caps is the clientCapabilities initialize advertises; nil =
	// DefaultCaps(). The Client serves no fs/* or terminal/* request itself
	// (it answers method-not-found): without a proxy between it and the
	// agent that does (xbind's in-sandbox host), advertise them false.
	Caps *ClientCapabilities
	// Drop hands one of a prompt's files to where the agent runs and
	// returns the path the agent finds it at (the prompt links it as
	// file://<path>). It may call the agent side over conn. Bounded by 30 s
	// per file. nil = a prompt with attachments is refused
	// (ErrUnsupportedContent).
	Drop func(ctx context.Context, conn *Conn, a Attachment) (path string, err error)
	// AuthHint is the text of an error the agent answered "auth required"
	// (-32000) to: msg is the agent's own message. It tells the person how
	// to sign in. nil = msg plus "the agent isn't signed in — run: " and the
	// provider's LoginCmd.
	AuthHint func(cfg Config, msg string) string
	// OnExt sees a notification the client does not handle itself — an
	// extension such as xbind's _xbin/log from its in-sandbox host — and
	// reports whether it handled it. Runs on the read loop.
	OnExt func(cfg Config, m *Message) bool
	// InlineBudget is how many bytes of images one prompt sends inline (as
	// image blocks the model sees); an image past it is a file only. 0 =
	// MaxInlineImagesBytes; negative = none inline.
	InlineBudget int
	// IDPrefix makes the client's request ids strings, "<prefix>-N"
	// (ConnOptions.IDPrefix), so a later process taking over the same agent
	// with another prefix never reuses one the agent may still answer.
	// "" = numbers, 1, 2, … (xbind's).
	IDPrefix string
	// Attach takes over a live session another process started (its
	// State(), kept with the agent's output offset): Start connects to the
	// running agent through the Spawner — which reattaches rather than
	// starts, with Process.Off where its reader resumes — and rebuilds the
	// session from the state with no handshake; the in-flight prompt's
	// response still ends its turn (Conn.Expect). The permissions the state
	// was waiting on are the embedder's to restore (Permissions.Restore).
	Attach *SessionState
	// AwaitLogin keeps the agent running when it refuses to open a session
	// because it is not signed in (-32000 from session/new or
	// session/load): Start still returns that error, the status is error
	// with the login, and Authenticate then signs in and opens the session.
	// false = Start closes the agent (xbind's).
	AwaitLogin bool
}

// DefaultCaps is what a client advertises by default — xbind's: text files
// and terminals for the agent (its in-sandbox host serves those, not the
// Client), the adapters' tool-call extensions it renders (terminal output
// on a call, subagent transcripts: toolmeta.go) and form questions
// (elicit.go). A fresh value each call.
func DefaultCaps() ClientCapabilities {
	return ClientCapabilities{FS: FSCapabilities{ReadTextFile: true, WriteTextFile: true}, Terminal: true, Meta: clientMeta(),
		Elicitation: &ElicitationCaps{Form: &struct{}{}}}
}
