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
// the connector — a person's own keeps its priority. The key is asked for
// under a label that names the run, so no id is stored: the run's keys are
// ended by label before a new start, when the adapter exits or is stopped,
// and — after this backend restarts — by whichever process sees that. It is
// redacted from everything the adapter prints, like a saved sign-in's
// secret (harness_redact.go, which also masks any xbl_… by shape).
package main

import (
	"context"
	"fmt"
	"strconv"
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

// connectorEnv points a new generation of run's coding agent at the
// connector, when it is the one to serve it: no saved sign-in went in
// (the caller's check), the connector can (connectorCan) and the CLI isn't
// signed in in the sandbox on its own. It adds the variables to env,
// answers the key — the generation's secret, for the redactor — and a note
// for the conversation. "" when the agent starts as before.
func (e *Engine) connectorEnv(ctx context.Context, run *Run, u *sbxUse, prov acp.Provider, env map[string]string) (secret, note string) {
	if !connectorCan(ctx, u.Box, prov) || sandboxSignedIn(ctx, u, prov) {
		return "", ""
	}
	p, _ := connectorProvider(ctx)
	label := connectorLabel(run.ID)
	_ = revokeLLMKeys(ctx, label) // an earlier generation's
	k, err := llmKeyFor(ctx, xbin.LLMKeyOpts{Label: label, Sandbox: u.ID})
	if err != nil {
		logf("run #%d: no LLM key for %s (%v) — it starts on its own sign-in", run.ID, prov.Name, err)
		return "", ""
	}
	params := acp.ConnectorParams{URL: u.Box.llmURL(), Key: k.Token, Model: askPreferred(ctx, p, "harness"), Serves: acp.ServeConnector}
	ce, ok := prov.ConnectorEnv(params)
	if !ok || !ce.UsesKey {
		_ = revokeLLMKeys(ctx, label)
		return "", ""
	}
	for _, name := range ce.Unset {
		delete(env, name)
	}
	for name, v := range ce.Set {
		env[name] = v
	}
	return k.Token, fmt.Sprintf("%s runs on this workspace's LLM connector (%s), metered to you — sign it in to use your own account instead", prov.Name, acp.ConnectorName)
}

// connectorEnded ends run's connector keys: its adapter exited or was
// stopped. A run that never had one costs a call that ends nothing; nothing
// is asked while the connector isn't bound.
func connectorEnded(run int64) {
	if _, ok := connectorProvider(context.Background()); !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = revokeLLMKeys(ctx, connectorLabel(run))
	}()
}
