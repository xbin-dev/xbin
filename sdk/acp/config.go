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
}

// Process is a spawned agent as the client sees it: its stdio and a way to
// end it.
type Process struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stderr io.Reader // may be nil (already routed by the spawner)
	Wait   func() error
	Kill   func()
}

// Spawner starts the agent process for a Config: locally, in a sandbox, on
// another machine — anything that yields its stdio.
type Spawner func(ctx context.Context, cfg Config) (*Process, error)

// Elicitation is a question the agent is waiting on: an
// elicitation.request's payload, until it is answered.
type Elicitation struct {
	EID        string          `json:"eid"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Message    string          `json:"message"`
	Schema     json.RawMessage `json:"schema"`
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
	// IDPrefix and Attach are reserved for taking over a live session from
	// another process (string request ids that cannot collide with the
	// earlier process's, and the state to resume from). This version
	// supports neither: Start refuses a non-zero value.
	IDPrefix string
	Attach   *SessionState
}

// SessionState is reserved (ClientOptions.Attach): what a later process
// needs to take over a live session. It has no fields yet.
type SessionState struct{}

// DefaultCaps is what a client advertises by default — xbind's: text files
// and terminals for the agent (its in-sandbox host serves those, not the
// Client), the adapters' tool-call extensions it renders (terminal output
// on a call, subagent transcripts: toolmeta.go) and form questions
// (elicit.go). A fresh value each call.
func DefaultCaps() ClientCapabilities {
	return ClientCapabilities{FS: FSCapabilities{ReadTextFile: true, WriteTextFile: true}, Terminal: true, Meta: clientMeta(),
		Elicitation: &ElicitationCaps{Form: &struct{}{}}}
}
