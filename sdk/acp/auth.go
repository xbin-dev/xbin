package acp

// Signing the agent in through the agent itself (ACP authenticate), for an
// embedder with no terminal to run the CLI's own login in: an API key
// (codex: _meta["api-key"].apiKey; gemini: _meta["api-key"], the key
// itself), or a device code (codex, only
// to a client that advertised url elicitations: elicit.go — the code
// arrives as a url elicitation.request while Authenticate waits).

import (
	"context"
	"encoding/json"
)

// AuthMethods is how the agent said it signs in (initialize's
// authMethods).
func (c *Client) AuthMethods() []AuthMethod {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]AuthMethod(nil), c.authMethods...)
}

// Authenticate signs the agent in with one of its AuthMethods: meta is
// the method's input (_meta; nil for none). On success the signed-out
// state clears (a status event without the login); an agent that refused
// to open the session signed out (ClientOptions.AwaitLogin) opens it now.
// A device-code method waits for the person, through a url
// elicitation.request answered with RespondElicitation — bound it with ctx.
func (c *Client) Authenticate(ctx context.Context, methodID string, meta map[string]any) error {
	c.mu.Lock()
	closed, init := c.closed, c.initialized
	c.mu.Unlock()
	if closed {
		return ErrEnded
	}
	if !init {
		return ErrNotReady
	}
	if err := c.conn.CallCtx(ctx, MAuthenticate, AuthenticateParams{MethodID: methodID, Meta: meta}, nil); err != nil {
		return err
	}
	if c.signedOut() { // no session yet: open it (a Start that awaited the login)
		c.mu.Lock()
		c.authNeeded = false // the idle status says so
		c.mu.Unlock()
		hctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
		defer cancel()
		if err := c.openSession(hctx); err != nil {
			c.setStatus(StatusError, err.Error())
			return err
		}
		return nil
	}
	c.setAuthNeeded(false)
	return nil
}

// unmarshalResult decodes a response's result (none: v untouched).
func unmarshalResult(resp *Message, v any) error {
	if resp == nil || len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil
	}
	return json.Unmarshal(resp.Result, v)
}
