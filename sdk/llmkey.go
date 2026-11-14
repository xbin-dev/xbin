package xbin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// llmkey.go — LLM keys for the coding tools a tile runs (D226, docs/llm.md
// "Sandboxes made by a sandbox manager"). A tile whose llm slot is bound to
// xbind's LLM connector (xbin:llm) calls the connector with its own
// credential; a coding agent it runs in a sandbox must not hold that
// credential, so the tile asks xbind for a narrow key instead.

// LLMKeyOpts say what an LLM key is for.
type LLMKeyOpts struct {
	// User is the person the run acts for. It must be the person this
	// backend's credential acts for — a partition's person; "" takes them
	// (and, in a backend that serves everyone, makes a key metered to the
	// tile alone). Naming anyone else is refused: ErrLLMKeyForbidden.
	User string
	// TTL is how long the key lives (0: a day; xbind caps it at 30 days).
	// Revoke it when the run ends, long before.
	TTL time.Duration
	// Label says what it is for — the harness and the run — and Sandbox
	// names the sandbox it is handed to. Both are kept for xbind's log
	// (at most 200 characters each).
	Label, Sandbox string
}

// LLMKey is a narrow credential for xbind's LLM connector: good for its
// OpenAI-compatible routes and nothing else, metered to User and to this
// tile, dead at Expires, at RevokeLLMKey or when xbind restarts. Token is
// a secret: hand it to the tool in its environment (acp.ConnectorParams.Key),
// keep it out of logs and stores, and redact it from what the tool prints.
type LLMKey struct {
	ID        string    // names the key for RevokeLLMKey; not secret
	Token     string    // "xbl_…", sent as Authorization: Bearer
	User      string    // the person it is metered to ("" = nobody: the tile alone)
	Component string    // this tile
	Expires   time.Time //
}

// ErrLLMKeyForbidden: xbind refused the key — the tile's llm slot isn't
// bound to xbin:llm, or User is not the person this credential acts for.
// ErrLLMUnavailable: this workspace has no LLM connector to give keys for
// (an xbind from before it, none configured, or switched off).
var (
	ErrLLMKeyForbidden = errors.New("llm key: refused")
	ErrLLMUnavailable  = errors.New("llm key: no LLM connector")
)

// LLMKeyFor asks xbind for an LLM key for a coding tool this tile runs for
// a person (POST /api/xbin/llm/keys, from the backend: its instance
// token). The tile's llm slot must be bound to xbin:llm — the binding is
// the grant. Where the tool reaches the connector is the sandbox's to say:
// a sandbox created with the connector asked for names the address in its
// row (the sandbox-manager contract's llm.url; SandboxNetInfo.LLM on
// xbind's own runtime).
//
//	k, err := xbin.LLMKeyFor(ctx, xbin.LLMKeyOpts{User: person, Label: "codex run 12", Sandbox: box.ID})
//	if err != nil { /* run it on its own sign-in */ }
//	defer xbin.RevokeLLMKey(context.Background(), k.ID)
//	env, _ := provider.ConnectorEnv(acp.ConnectorParams{URL: box.LLM.URL, Key: k.Token, Serves: acp.ServeConnector})
func LLMKeyFor(ctx context.Context, o LLMKeyOpts) (LLMKey, error) {
	body, _ := json.Marshal(map[string]any{"user": strings.TrimPrefix(strings.TrimSpace(o.User), "user:"),
		"ttlSec": int64(o.TTL / time.Second), "label": o.Label, "sandbox": o.Sandbox})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://xbin/api/xbin/llm/keys", bytes.NewReader(body))
	if err != nil {
		return LLMKey{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	raw, status, err := llmKeyCall(req)
	if err != nil {
		return LLMKey{}, err
	}
	if status != http.StatusOK {
		return LLMKey{}, llmKeyErr(status, raw)
	}
	var out struct {
		ID, Token, User, Component, Expires string
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" || out.ID == "" {
		return LLMKey{}, fmt.Errorf("llm key: a garbled answer")
	}
	k := LLMKey{ID: out.ID, Token: out.Token, User: out.User, Component: out.Component}
	k.Expires, _ = time.Parse(time.RFC3339, out.Expires)
	return k, nil
}

// RevokeLLMKey ends a key this tile asked for (DELETE
// /api/xbin/llm/keys/<id>). Ending one that is already dead is no error.
func RevokeLLMKey(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://xbin/api/xbin/llm/keys/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	raw, status, err := llmKeyCall(req)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return llmKeyErr(status, raw)
	}
	return nil
}

func llmKeyCall(req *http.Request) ([]byte, int, error) {
	resp, err := Client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return raw, resp.StatusCode, err
}

// llmKeyErr is xbind's refusal as an error: its words, wrapping
// ErrLLMKeyForbidden (403) or ErrLLMUnavailable (404: no such route on an
// older xbind; 503: not configured, off).
func llmKeyErr(status int, raw []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	text := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		text = e.Error
	}
	if len(text) > 300 {
		text = text[:300]
	}
	switch status {
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrLLMKeyForbidden, text)
	case http.StatusNotFound, http.StatusServiceUnavailable, http.StatusMethodNotAllowed:
		return fmt.Errorf("%w: %s", ErrLLMUnavailable, text)
	}
	return fmt.Errorf("llm key: %d: %s", status, text)
}
