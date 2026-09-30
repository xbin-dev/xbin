package acp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// handshakeTimeout bounds initialize and session/new: an adapter that never
// answers (a broken install, a hung login) becomes a status error instead of
// a session stuck at "starting".
const handshakeTimeout = 90 * time.Second

func (c *Client) handshake() error {
	c.setStatus(StatusStarting, "")
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	caps := c.opts.Caps
	if caps == nil {
		d := DefaultCaps()
		caps = &d
	}
	var init InitializeResult
	if err := c.conn.CallCtx(ctx, MInitialize, InitializeParams{
		ProtocolVersion:    ProtocolVersion,
		ClientCapabilities: *caps,
		ClientInfo:         &Info{Name: "xbin", Version: c.cfg.Version},
	}, &init); err != nil {
		return fmt.Errorf("initialize: %w", deadlineHint(err, "answer initialize"))
	}
	c.mu.Lock()
	c.agentInfo = init.AgentInfo
	c.loadable = init.AgentCapabilities != nil && init.AgentCapabilities.LoadSession
	if init.AgentCapabilities != nil && init.AgentCapabilities.PromptCapabilities != nil {
		c.promptCaps = *init.AgentCapabilities.PromptCapabilities
	}
	c.steering = steeringSupported(init.Meta)
	c.authMethods = init.AuthMethods
	c.initialized = true
	c.mu.Unlock()
	if init.ProtocolVersion != ProtocolVersion {
		c.logf("agent speaks protocol version %d, we speak %d — continuing", init.ProtocolVersion, ProtocolVersion)
	}
	// No authenticate call: the CLI authenticates itself from its own $HOME
	// (in xbind the same per-user home a shell terminal gets), so a login
	// done once in a terminal serves every agent session. If the home holds
	// no login, session/new returns -32000 and we surface how to sign in
	// (an embedder may sign in through the agent instead: Authenticate).
	return c.openSession(ctx)
}

// openSession is session/new (or session/load for cfg.ResumeID), then the
// requested mode and options; idle once open.
func (c *Client) openSession(ctx context.Context) error {
	var sess SessionNewResult
	if c.cfg.ResumeID != "" {
		// resume: reopen the agent's earlier session — it streams the prior
		// turns back as session/update (they land in the log like live ones,
		// Wire.Replay set), then the session continues. Only an agent that
		// said loadSession.
		if !c.loadable {
			return ErrResumeUnsupported
		}
		var ld SessionLoadResult
		c.replaying.Store(true) // cleared by the read loop on the load's answer
		err := c.conn.CallCtx(ctx, MSessionLoad, SessionLoadParams{SessionID: c.cfg.ResumeID, Cwd: c.cfg.Cwd, MCPServers: []any{}, Meta: c.cfg.Provider.SessionMeta}, &ld)
		if err != nil {
			c.replaying.Store(false)
			return fmt.Errorf("session/load: %w", c.authHint(deadlineHint(err, "reopen the session")))
		}
		sess = SessionNewResult{SessionID: c.cfg.ResumeID, Modes: ld.Modes, ConfigOptions: ld.ConfigOptions}
	} else if err := c.conn.CallCtx(ctx, MSessionNew, SessionNewParams{Cwd: c.cfg.Cwd, MCPServers: []any{}, Meta: c.cfg.Provider.SessionMeta}, &sess); err != nil {
		return fmt.Errorf("session/new: %w", c.authHint(deadlineHint(err, "open a session")))
	}
	c.mu.Lock()
	c.sessionID = sess.SessionID
	c.modes = sess.Modes
	c.options = sess.ConfigOptions
	c.mu.Unlock()
	if want := c.cfg.Mode; want != "" && (sess.Modes == nil || sess.Modes.CurrentModeID != want) && !modeOptionIs(sess.ConfigOptions, want) {
		if err := c.conn.Call(MSessionSetMode, SetModeParams{SessionID: sess.SessionID, ModeID: want}, nil); err != nil {
			c.logf("set_mode %s: %v", want, err)
		} else {
			c.mu.Lock()
			if c.modes != nil {
				c.modes.CurrentModeID = want
			}
			// an agent that speaks its modes as a config option of category
			// mode (opencode) takes set_mode too: its option says so now
			for i := range c.options {
				if c.options[i].Category == "mode" && optionOffers(c.options[i], want) {
					c.options[i].CurrentValue = want
				}
			}
			c.mu.Unlock()
		}
	}
	// requested settings (a model, an effort) — applied after the CLI has
	// loaded its own config, so they win over the home's defaults
	for _, id := range sortedOptionIDs(c.cfg.Options) {
		if cur, ok := c.option(id); !ok || cur.CurrentValue == c.cfg.Options[id] {
			if !ok {
				c.logf("option %s: the agent does not offer it", id)
			}
			continue
		} else if cur.Category == "mode" && c.cfg.SkipModeOptions {
			c.logf("option %s: it is the agent's mode — not set as an option", id)
			continue
		}
		if err := c.setOption(ctx, id, c.cfg.Options[id]); err != nil {
			c.logf("set option %s=%s: %v", id, c.cfg.Options[id], err)
		}
	}
	c.setStatus(StatusIdle, "")
	return nil
}

// modeOptionIs: the session has no modes of its own, and its config option
// of category mode is already at mode.
func modeOptionIs(opts []ConfigOption, mode string) bool {
	for _, o := range opts {
		if o.Category == "mode" && optionOffers(o, mode) {
			return o.CurrentValue == mode
		}
	}
	return false
}

// optionOffers: value is one of o's values.
func optionOffers(o ConfigOption, value string) bool {
	for _, v := range o.Options {
		if v.Value == value {
			return true
		}
	}
	return false
}

// option finds one of the agent's config options by id.
func (c *Client) option(id string) (ConfigOption, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range c.options {
		if o.ID == id {
			return o, true
		}
	}
	return ConfigOption{}, false
}

// setOption is the wire call, bounded by ctx; the response carries the
// refreshed list.
func (c *Client) setOption(ctx context.Context, id, value string) error {
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	var res SetConfigResult
	if err := c.conn.CallCtx(ctx, MSessionSetConfig, SetConfigParams{SessionID: sid, ConfigID: id, Value: value}, &res); err != nil {
		return err
	}
	c.mu.Lock()
	if len(res.ConfigOptions) > 0 {
		c.options = res.ConfigOptions
	} else { // an agent that answers {} — apply locally
		for i := range c.options {
			if c.options[i].ID == id {
				c.options[i].CurrentValue = value
			}
		}
	}
	c.mu.Unlock()
	return nil
}

// SetOption changes a session setting for the clients: the refreshed
// options ride a status event. ctx bounds the agent's answer.
func (c *Client) SetOption(ctx context.Context, id, value string) error {
	if _, ok := c.option(id); !ok {
		if id == "mode" && c.hasMode(value) { // no "mode" config option: the older session/set_mode
			return c.setModeLive(ctx, value)
		}
		return fmt.Errorf("the agent offers no option %q", id)
	}
	if err := c.setOption(ctx, id, value); err != nil {
		return err
	}
	c.emit(c.optionsEvent())
	return nil
}

func (c *Client) hasMode(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.modes == nil {
		return false
	}
	for _, m := range c.modes.AvailableModes {
		if m.ID == id {
			return true
		}
	}
	return false
}

// setModeLive switches the permission mode mid-session (session/set_mode)
// and tells the clients through a status {currentMode}.
func (c *Client) setModeLive(ctx context.Context, mode string) error {
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if err := c.conn.CallCtx(ctx, MSessionSetMode, SetModeParams{SessionID: sid, ModeID: mode}, nil); err != nil {
		return err
	}
	c.mu.Lock()
	if c.modes != nil {
		c.modes.CurrentModeID = mode
	}
	c.mu.Unlock()
	c.emit(c.partialStatus(map[string]any{"currentMode": mode}))
	return nil
}

// optionsEvent is a status event with the current options.
func (c *Client) optionsEvent() Event {
	c.mu.Lock()
	opts := append([]ConfigOption(nil), c.options...)
	c.mu.Unlock()
	return c.partialStatus(map[string]any{"options": opts})
}

// steeringSupported reads initialize's _meta.steering.supported (the
// steering extension's advertisement: claude-agent-acp, codex-acp).
func steeringSupported(meta map[string]any) bool {
	st, _ := meta["steering"].(map[string]any)
	ok, _ := st["supported"].(bool)
	return ok
}

// signedOut: initialize answered, no session, the agent said it is not
// signed in — what Authenticate recovers from (ClientOptions.AwaitLogin).
func (c *Client) signedOut() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialized && c.sessionID == "" && c.authNeeded
}

func sortedOptionIDs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// deadlineHint names a handshake timeout for the operator.
func deadlineHint(err error, what string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("the agent did not %s within %s — its log (GET …/log) has the adapter's output", what, handshakeTimeout)
	}
	return err
}

// isAuthError reports whether a status detail is a "sign in" message (an
// agent that fails with its own wording rather than -32000).
func isAuthError(detail string) bool {
	return strings.Contains(detail, "Authentication required") || strings.Contains(detail, "isn't signed in")
}

// authError is the agent's auth-required error with the person's next step:
// its text is the hint, and it unwraps to the agent's *Error.
type authError struct {
	text string
	err  *Error
}

func (e *authError) Error() string { return e.text }
func (e *authError) Unwrap() error { return e.err }

// authHint turns the agent's -32000 into the person's next step
// (ClientOptions.AuthHint, else how to sign the CLI in) and marks the
// session signed out; any other error is returned as is.
func (c *Client) authHint(err error) error {
	var re *Error
	if !errors.As(err, &re) || re.Code != CodeAuthRequired {
		return err
	}
	c.mu.Lock()
	c.authNeeded = true // signed out until a turn succeeds (prompt.go)
	c.mu.Unlock()
	hint := defaultAuthHint
	if c.opts.AuthHint != nil {
		hint = c.opts.AuthHint
	}
	return &authError{text: hint(c.cfg, re.Message), err: re}
}

func defaultAuthHint(cfg Config, msg string) string {
	how := cfg.Provider.LoginCmd
	if how == "" {
		how = cfg.Provider.Login
	}
	if how == "" {
		how = "the CLI's own login"
	}
	return msg + ": the agent isn't signed in — run: " + how
}
