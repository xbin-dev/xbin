package acp

import (
	"encoding/json"

	"github.com/xbin-dev/xbin/internal/agent"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// Client is the ACP client side for one session (sdk/acp): it owns the
// agent process, runs the handshake, turns prompts into turns and the
// agent's updates into agent.Events, and holds permission requests open
// until answered. It is the package's agent.Driver.
type Client = sdkacp.Client

// Command is one slash command as the events carry it.
type Command = sdkacp.Command

// New returns an unstarted client, configured for xbind.
func New() *Client { return sdkacp.NewWith(xbindOptions) }

// Driver is the agent.Driver constructor for provider.Driver == "acp".
func Driver() agent.Driver { return New() }

var _ agent.Driver = (*Client)(nil)

// xbindOptions are sdk/acp's seams as xbind fills them: the default
// capabilities (the host serves fs/* and terminal/*), a prompt's files
// dropped by the host (prompt.go), the host's log lines, and the sign-in
// hint that names the terminal login on the tile.
var xbindOptions = sdkacp.ClientOptions{Drop: dropFile, OnExt: hostLog, AuthHint: authHint}

// hostLog puts the host's _xbin/log lines (a spawn failure, a line the
// agent printed that was not a frame) in the session's text log.
func hostLog(cfg agent.Config, m *Message) bool {
	if m.Method != MXbinLog {
		return false
	}
	var p LogParams
	if json.Unmarshal(m.Params, &p) == nil && cfg.Log != nil {
		cfg.Log(p.Text)
	}
	return true
}

// authHint turns the agent's -32000 into the operator's next step: sign the
// CLI in from a terminal, whose $HOME the agent shares.
func authHint(cfg agent.Config, msg string) string {
	return msg + ": " + agent.LoginHint(cfg.Provider, tileOf(cfg))
}

func tileOf(cfg agent.Config) string {
	if t := cfg.Meta["tile"]; t != "" {
		return t
	}
	return "<tile>"
}
