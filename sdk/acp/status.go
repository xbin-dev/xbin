package acp

// Status events: the session's state (EvStatus) with what a client needs to
// render it — the modes, the config options, the slash commands, the
// agent's identity on idle, and the sign-in while the agent is signed out.

// setAuthNeeded records whether the agent says it is signed out and, on a
// change, re-emits the current status so the clients show (or clear) the
// sign-in prompt. The status carries the login command (Provider.Login).
func (c *Client) setAuthNeeded(need bool) {
	c.mu.Lock()
	changed := c.authNeeded != need
	c.authNeeded = need
	st := c.status
	c.mu.Unlock()
	if changed && st != "" {
		c.setStatus(st, "")
	}
}

func (c *Client) setStatus(status, detail string) {
	c.mu.Lock()
	c.status = status
	modes := c.modes
	opts := append([]ConfigOption(nil), c.options...)
	c.mu.Unlock()
	d := map[string]any{"status": status}
	if detail != "" {
		d["detail"] = detail
	}
	if modes != nil {
		d["currentMode"] = modes.CurrentModeID
		if status == StatusIdle || status == StatusStarting {
			d["modes"] = modes.AvailableModes
		}
	}
	if len(opts) > 0 && status == StatusIdle {
		d["options"] = opts
	}
	if status == StatusIdle && c.agentInfo != nil {
		d["agent"] = c.agentInfo
	}
	if status == StatusIdle {
		c.withCommands(d)
	}
	c.mu.Lock()
	if isAuthError(detail) {
		c.authNeeded = true // signed out until a turn succeeds (prompt.go)
	}
	need := c.authNeeded
	c.mu.Unlock()
	if need {
		d["login"] = c.login()
	}
	// a terminal status never blocks on a pump that is gone
	c.send(NewEvent(EvStatus, d), status != StatusExited && status != StatusError)
}

// partialStatus is a status event saying only what changed (fields) and the
// current status — plus the login while the agent is signed out, which rides
// every status then (docs/protocol.md), so a client's sign-in prompt doesn't
// vanish with the next commands/usage/mode/options/title update.
func (c *Client) partialStatus(fields map[string]any) Event {
	c.mu.Lock()
	fields["status"] = c.status
	need := c.authNeeded
	c.mu.Unlock()
	if need {
		fields["login"] = c.login()
	}
	return NewEvent(EvStatus, fields)
}

// login is the sign-in a status carries while the agent is signed out.
func (c *Client) login() map[string]any {
	p := c.cfg.Provider
	return map[string]any{"needed": true, "provider": p.Name, "command": p.Login}
}
