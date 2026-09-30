package agent

import (
	"context"
	"encoding/json"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// Errors a driver returns that the API maps to a status (the ACP client's,
// sdk/acp).
var (
	ErrBusy              = acp.ErrBusy
	ErrEnded             = acp.ErrEnded
	ErrResumeUnsupported = acp.ErrResumeUnsupported
	ErrNoElicitation     = acp.ErrNoElicitation
	ErrCancelled         = acp.ErrCancelled
)

// Driver speaks one agent protocol on behalf of a session. Start spawns
// (through the Spawner) and handshakes; Prompt starts a turn with the
// user's text and attachments (attachments.go); Events is the typed stream (closed when the agent is gone);
// RespondPermission answers a request the driver surfaced as a
// permission.request event; Cancel interrupts the running turn; Close ends
// the agent. One implementation today (sdk/acp's Client, configured for
// xbind by internal/agent/acp); a second is a second package implementing
// this and a Provider.Driver naming it.
type Driver interface {
	Start(ctx context.Context, cfg Config) error
	Prompt(ctx context.Context, p Prompt) error
	Events() <-chan Event
	RespondPermission(res *Resolution) error
	// RespondElicitation answers a question the driver surfaced as an
	// elicitation.request (action accept | decline | cancel; content the
	// form's values on accept). ErrNoElicitation once it is answered.
	RespondElicitation(eid, action string, content json.RawMessage, by string) error
	// PendingElicitations lists the questions still waiting for an answer,
	// oldest first (the elicitation.request payloads) — the session
	// snapshot's twin of the permissions list.
	PendingElicitations() []Elicitation
	Cancel() error
	Close() error
	// SetOption changes one of the agent's session settings (a config option
	// it advertised: model, effort, …); the driver emits a status event with
	// the refreshed options.
	SetOption(ctx context.Context, id, value string) error
	// Session reports the agent's own id for this session and whether the
	// agent could reopen it later (session/load) — persisted with the
	// transcript so a past session can be resumed (term/history.go).
	Session() (id string, loadable bool)
}

// The session model's types are the ACP client's (sdk/acp): one
// definition, shared with every tile that runs the same client.
type (
	// Elicitation is a question the agent is waiting on: an
	// elicitation.request's payload, until it is answered.
	Elicitation = acp.Elicitation
	// Config is what a session hands its driver.
	Config = acp.Config
	// Process is a spawned agent (or host) as the driver sees it: its stdio
	// and a way to end it.
	Process = acp.Process
	// Spawner starts the agent process for a Config. The term package's
	// spawner launches the sandbox with `bx __agent-host` as the entry and
	// hands the host {argv, env}; tests spawn a fake agent directly.
	Spawner = acp.Spawner
)
