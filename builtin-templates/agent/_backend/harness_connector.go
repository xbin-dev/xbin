// harness_connector.go — a coding agent on the workspace's LLM connector
// (D226; the platform's docs/llm.md "Sandboxes made by a sandbox manager").
//
// A coding agent signs in with a saved sign-in of the person's
// (harness_creds.go) or with what its CLI keeps in the sandbox's home. Where
// neither exists it used to stop at "not signed in". Now, when this agent's
// `llm` slot is bound to xbind's connector (xbin:llm) and the sandbox was
// created with the connector asked for (its row names the address,
// llm.url), the agent starts on the connector instead: its CLI is pointed
// at it from its environment (sdk/acp's table, the same one xbind's own
// agent sessions use) with an LLM key xbind minted for the person this
// instance acts for — good for the connector's model routes only, metered
// to them and this tile.
//
// Precedence: a saved sign-in first, then a sign-in in the sandbox, then
// the connector — a person's own keeps its priority, except in a sandbox
// with no egress: there nothing but the connector can be reached, so it
// serves, or the agent doesn't start. The connector serves only with a
// model to start on (the workspace's harness model): without one the agent
// would fall back to whatever its CLI picks by itself, so no key is asked
// for and the conversation says why.
//
// The key is asked for under a label that names the run, so no id is
// stored: the run's keys are ended by label before a new start (which
// doesn't happen until xbind acknowledged it), when the adapter exits or is
// stopped (retried until acknowledged), and — after this backend restarts —
// by whichever process sees that. A key lives a day; a run that goes on is
// renewed well before (connectorRenewEvery). It is redacted from everything
// the adapter prints, like a saved sign-in's secret (harness_redact.go,
// which also masks any xbl_… by shape).
package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/acp"
)

// connectorRef is the binding ref of xbind's LLM connector.
const connectorRef = "xbin:llm"

// The SDK's calls, as variables for the tests.
var (
	llmKeyFor     = xbin.LLMKeyFor
	revokeLLMKeys = xbin.RevokeLLMKeysLabeled
	renewLLMKey   = xbin.RenewLLMKey
)

// connectorProvider is the connector among the providers bound to this
// agent's llm slot (false: not bound).
func connectorProvider(ctx context.Context) (llmProvider, bool) {
	for _, p := range llmProvidersIn(ctx) {
		if p.Path == connectorRef {
			return p, true
		}
	}
	return llmProvider{}, false
}

// wantLLM reports whether a sandbox made at manager m should ask for the
// connector: this agent is bound to it, and the manager offers the
// capability (the contract's llm; a manager from before it is never sent
// the member).
func wantLLM(ctx context.Context, m sbxManager) bool {
	if _, ok := connectorProvider(ctx); !ok {
		return false
	}
	h, err := managerHello(ctx, m)
	return err == nil && h != nil && hasStr(h.Caps, "llm")
}

// llmURL is where the sandbox reaches the connector ("" = it doesn't).
func (s *sbxSandbox) llmURL() string {
	if s == nil || s.LLM == nil {
		return ""
	}
	return s.LLM.URL
}

// connectorWires are the wires xbind's connector serves.
var connectorWires = []string{acp.WireChat, acp.WireResponses}

// connectorCan reports whether prov's CLI could run on the connector in
// box: bound, reachable from the sandbox, and a wire the connector serves.
func connectorCan(ctx context.Context, box *sbxSandbox, prov acp.Provider) bool {
	if prov.Connector == nil || !hasStr(connectorWires, prov.Connector.Wire) || box.llmURL() == "" {
		return false
	}
	_, ok := connectorProvider(ctx)
	return ok
}

// connectorLabel names a run's keys.
func connectorLabel(run int64) string { return "harness run " + strconv.FormatInt(run, 10) }

// ownLoginScript exits 0 when one of its arguments — shell words of the
// sdk's catalog ($HOME, ${CODEX_HOME:-…}) — names a non-empty file.
const ownLoginScript = `for w in "$@"; do f=$(eval "printf '%s' \"$w\""); [ -s "$f" ] && exit 0; done; exit 1`

// sandboxSignedIn reports whether prov's CLI is signed in in the sandbox on its
// own (one of its login files is there). A probe that can't run counts as
// signed in: the connector never takes the place of a sign-in it couldn't
// rule out.
func sandboxSignedIn(ctx context.Context, u *sbxUse, prov acp.Provider) bool {
	if len(prov.LoginFiles) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := u.Conn.Run(ctx, u.ID, sbxRunReq{Argv: append([]string{"sh", "-c", ownLoginScript, "probe"}, prov.LoginFiles...),
		TimeoutMs: 10000, MaxOutput: 1024})
	if err != nil || res.ExitCode == nil {
		return true
	}
	return *res.ExitCode == 0
}

// connectorMight reports whether prov's CLI could run on the connector at
// all — this agent is bound to it and it serves the CLI's wire — before any
// sandbox is known (connectorCan is the answer for one).
func connectorMight(ctx context.Context, prov acp.Provider) bool {
	if prov.Connector == nil || !hasStr(connectorWires, prov.Connector.Wire) {
		return false
	}
	_, ok := connectorProvider(ctx)
	return ok
}

// noEgressFail is why a coding agent can't start in a sandbox without
// egress: nil when the workspace's LLM connector is reachable there for it
// (the one thing such a sandbox reaches).
func noEgressFail(ctx context.Context, box *sbxSandbox, prov acp.Provider, name string) error {
	if connectorCan(ctx, box, prov) {
		return nil
	}
	if connectorMight(ctx, prov) {
		return fmt.Errorf("%s must reach its provider — %s's egress is none, and it was created without the workspace's LLM connector (a new sandbox gets it)", prov.Name, name)
	}
	return fmt.Errorf("%s must reach its provider — %s's egress is none", prov.Name, name)
}

// connectorEnv points a new generation of run's coding agent at the
// connector, when it is the one to serve it: no saved sign-in went in (the
// caller's check), the connector can (connectorCan), it has a model to
// start the agent on, and the CLI isn't signed in in the sandbox on its
// own. must — a sandbox with no egress, where nothing else is reachable —
// skips the last and turns every "it can't" into an error: the agent
// doesn't start. It adds the variables to env and answers the key — the
// generation's secret, for the redactor ("" when the agent starts as
// before) — and a note for the conversation: what it runs on, or why not
// on the connector where it would otherwise have been.
func (e *Engine) connectorEnv(ctx context.Context, run *Run, u *sbxUse, prov acp.Provider, env map[string]string, must bool) (secret, note string, err error) {
	cant := func(why string) (string, string, error) {
		if must {
			return "", "", fmt.Errorf("%s can't start in %s: its egress is none, and %s", prov.Name, orStr(u.Box.Name, u.ID), why)
		}
		return "", "", nil
	}
	if !connectorCan(ctx, u.Box, prov) {
		return cant("the workspace's LLM connector is not reachable there for it")
	}
	if !must && sandboxSignedIn(ctx, u, prov) {
		return "", "", nil
	}
	p, _ := connectorProvider(ctx)
	label := connectorLabel(run.ID)
	connectorForget(run.ID) // an earlier generation's renewals stop here
	// an earlier generation's keys end before a new one is made — and the
	// start waits for xbind's word on it: two live keys for one run never
	if err := revokeLLMKeys(ctx, label); err != nil && !errors.Is(err, xbin.ErrLLMUnavailable) {
		return "", "", fmt.Errorf("%s's previous LLM connector key couldn't be ended (%v) — try again", prov.Name, err)
	}
	model := askPreferred(ctx, p, "harness")
	if model == "" || !acp.ValidConnectorModel(model) {
		// no model to start on: the CLI would pick one by itself (opencode:
		// its vendor's free models) while everything said "connector"
		why := "the workspace's LLM connector has no model set for coding agents (a workspace admin sets one: bx llm default set)"
		if _, _, err := cant(why); err != nil {
			return "", "", err
		}
		return "", fmt.Sprintf("%s runs on its own sign-in: %s", prov.Name, why), nil
	}
	k, err := llmKeyFor(ctx, xbin.LLMKeyOpts{Label: label, Sandbox: u.ID})
	if err != nil {
		logf("run #%d: no LLM key for %s (%v)", run.ID, prov.Name, err)
		return cant(fmt.Sprintf("no LLM connector key could be made for it (%v)", err))
	}
	params := acp.ConnectorParams{URL: u.Box.llmURL(), Key: k.Token, Model: model, Serves: acp.ServeConnector, Only: must}
	ce, ok := prov.ConnectorEnv(params)
	if !ok || !ce.UsesKey {
		connectorEnded(run.ID)
		return cant("nothing could be rendered to point it at the workspace's LLM connector")
	}
	for _, name := range ce.Unset {
		delete(env, name)
	}
	for name, v := range ce.Set {
		env[name] = v
	}
	connectorStarted(run.ID, k.ID)
	return k.Token, fmt.Sprintf("%s runs on this workspace's LLM connector (%s), metered to you — sign it in to use your own account instead", prov.Name, acp.ConnectorName), nil
}

// ---- a run's key: kept alive while it runs, ended when it doesn't ---------------

// A key lives a day (xbind's cap for a backend's key); a run that goes on is
// renewed every connectorRenewEvery, and sooner again when xbind didn't
// answer. An end is retried from connectorEndFirst, doubling to
// connectorEndMax, until xbind acknowledged it — or the day is over, when
// the key is dead whatever anyone did.
var (
	connectorRenewEvery = 6 * time.Hour
	connectorRenewRetry = 5 * time.Minute
	connectorEndFirst   = 2 * time.Second
	connectorEndMax     = time.Minute
	connectorKeyLife    = 24 * time.Hour
)

// connectorRuns are the runs this process started on the connector: each
// one's key id and renew timer, and a sequence that a newer start or an end
// moves — a renewal or a retried end from before it does nothing.
var connectorRuns = struct {
	mu  sync.Mutex
	seq map[int64]int
	key map[int64]*time.Timer
}{seq: map[int64]int{}, key: map[int64]*time.Timer{}}

// connectorForget stops run's renewals and outdates what was pending for
// it; the new sequence.
func connectorForget(run int64) int {
	c := &connectorRuns
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.key[run]; t != nil {
		t.Stop()
		delete(c.key, run)
	}
	c.seq[run]++
	return c.seq[run]
}

func connectorCurrent(run int64, seq int) bool {
	connectorRuns.mu.Lock()
	defer connectorRuns.mu.Unlock()
	return connectorRuns.seq[run] == seq
}

// connectorStarted keeps the key of run's new generation alive for as long
// as the run is this process's: one-shot timers, re-armed by each renewal.
func connectorStarted(run int64, keyID string) {
	seq := connectorForget(run)
	var arm func(after time.Duration)
	renew := func() {
		if !connectorCurrent(run, seq) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		switch _, err := renewLLMKey(ctx, keyID, connectorKeyLife); {
		case err == nil:
			arm(connectorRenewEvery)
		case errors.Is(err, xbin.ErrLLMKeyGone), errors.Is(err, xbin.ErrLLMKeyForbidden):
			logf("run #%d: its LLM connector key is gone (%v) — its agent's next call is refused; a new start gets a new key", run, err)
		default:
			arm(connectorRenewRetry)
		}
	}
	arm = func(after time.Duration) {
		c := &connectorRuns
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.seq[run] == seq {
			c.key[run] = time.AfterFunc(after, renew)
		}
	}
	arm(connectorRenewEvery)
}

// connectorEnded ends run's connector keys: its adapter exited or was
// stopped. Not best effort — it is retried until xbind acknowledged it, a
// newer start of the run took over (which ends them itself before it makes
// its own), or a day passed (no key lives longer unrenewed, and the
// renewals stopped here). A run that never had a key costs a call that ends
// nothing; nothing is asked while the connector isn't bound.
func connectorEnded(run int64) {
	seq := connectorForget(run)
	if _, ok := connectorProvider(context.Background()); !ok {
		return
	}
	deadline := time.Now().Add(connectorKeyLife)
	var try func(wait time.Duration)
	try = func(wait time.Duration) {
		if !connectorCurrent(run, seq) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := revokeLLMKeys(ctx, connectorLabel(run))
		cancel()
		if err == nil || errors.Is(err, xbin.ErrLLMUnavailable) || errors.Is(err, xbin.ErrLLMKeyForbidden) || time.Now().After(deadline) {
			if err != nil {
				logf("run #%d: its LLM connector key was not ended by this backend (%v)", run, err)
			}
			return // acknowledged; or no connector, or no longer bound: xbind ended them with the binding
		}
		time.AfterFunc(wait, func() { try(min(wait*2, connectorEndMax)) })
	}
	go try(connectorEndFirst)
}
