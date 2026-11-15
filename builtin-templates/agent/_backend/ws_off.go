// ws_off.go — a workspace agent whose switch is off (docs/protocol/
// delegation.md §The switches): its person paused it, an admin turned
// workspace agents off, the person wasn't given one, or it is held on this
// xbin. xbind then refuses every call the agent makes, its model turns at
// the LLM connector included, so the agent answers its person without the
// model: a plain line in the transcript — which switch is on, who can
// change it and where — and nothing is queued. The person's words stay in
// their message box (409, kept), and run only when they send them again.
//
// Which switch is xbind's to say: the refusal of a delegate read (GET
// /delegate/state answers a paused or switched-off agent 403 {refusal}),
// or that read's own answer — never a guess from the page's state.
package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wsOffCodes are xbind's refusals that mean the agent's switch is off.
var wsOffCodes = map[string]bool{"paused": true, "workspace-off": true, "off": true, "no-agent": true, "held": true, "no-sandbox": true}

// wsOff is why the agent can't act now (Code "": it can), as xbind said.
type wsOff struct {
	Code string // paused | workspace-off | off | no-agent | held | no-sandbox
	Says string // xbind's own sentence, when it gave one
}

// wsOffNow asks xbind whether the agent may act: its state read, refused
// with an off code, or answered with the person's pause or the workspace's
// switch. An answer that says nothing (xbind unreachable, another error)
// is no switch: the call itself is refused if it must be.
func wsOffNow(ctx context.Context) wsOff {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := wsX.State(ctx)
	var de *xbin.DelegateError
	switch {
	case errors.As(err, &de) && wsOffCodes[de.Refusal]:
		return wsOff{Code: de.Refusal, Says: de.Message}
	case err != nil:
		return wsOff{}
	case st.Paused:
		return wsOff{Code: "paused"}
	case !st.Workspace:
		return wsOff{Code: "workspace-off"}
	}
	return wsOff{}
}

// wsOffCached is wsOffNow, asked at most once a second: what the engine's
// model calls check (a storm of steps doesn't become a storm of reads).
var wsOffCache struct {
	sync.Mutex
	at  time.Time
	off wsOff
}

func wsOffCached() wsOff {
	wsOffCache.Lock()
	defer wsOffCache.Unlock()
	if time.Since(wsOffCache.at) < time.Second {
		return wsOffCache.off
	}
	wsOffCache.off, wsOffCache.at = wsOffNow(context.Background()), time.Now()
	return wsOffCache.off
}

func wsOffForget() {
	wsOffCache.Lock()
	wsOffCache.at = time.Time{}
	wsOffCache.Unlock()
}

// switchWords is the switch in the person's words: what is on, who can
// change it, and where.
func (o wsOff) switchWords() string {
	var s string
	switch o.Code {
	case "paused":
		s = "Your agent is paused. You paused it, and only you can resume it: Resume in the workspace agent's dock (the agent chip in the top bar, or /xbin/agent)."
	case "no-agent":
		s = "You haven't been given a workspace agent. An admin chooses who has one: Admin → People → access, or the workspace agent's set-up in the dock."
	case "held":
		s = "Your agent is held on this xbin, and does nothing until that is cleared. An admin can see why and clear it: Admin → Runtime, or the workspace agent's dock."
	case "no-sandbox":
		s = "This xbin runs tiles without sandboxes, where a workspace agent can't act. An admin can run it with sandboxes (--isolate)."
	default: // workspace-off, off
		s = "Workspace agents are turned off for this workspace. A workspace admin can turn them on again: the workspace agent's set-up in the dock, or Admin."
	}
	if says := strings.TrimSpace(o.Says); says != "" {
		s += " (xbin: " + strings.TrimSuffix(says, ".") + ".)"
	}
	return s
}

// messageWords is what the transcript says when the person's message met
// the switch.
func (o wsOff) messageWords() string {
	return o.switchWords() + " Nothing was sent to the agent or its model. Your message is still in the message box: send it again once it runs."
}

// turnWords is what the transcript says when a turn the agent was woken
// for met the switch.
func (o wsOff) turnWords() string {
	return o.switchWords() + " Nothing was sent to the model."
}

// wsOffKeeps answers a person's message to a workspace session while the
// agent's switch is off (true: answered): a line in the transcript, 409
// {code: "agent-off", refusal, error, kept: true}, and nothing queued —
// the words stay with the person.
func wsOffKeeps(w http.ResponseWriter, r *http.Request, run *Run) bool {
	if !wsMode() || run == nil || run.Origin != originWorkspace || run.ParentID != 0 {
		return false
	}
	o := wsOffNow(r.Context())
	if o.Code == "" {
		return false
	}
	agent.wsOffNote(run.ID, o, o.messageWords())
	xbin.WriteJSON(w, http.StatusConflict, map[string]any{"error": o.messageWords(), "code": "agent-off", "refusal": o.Code, "kept": true})
	return true
}

// wsOffNote puts the switch's line in the transcript: a step of kind
// "off", which the page draws as a notice and the model never reads.
func (ag *Agent) wsOffNote(runID int64, o wsOff, text string) {
	_ = ag.db.Tx(func(t *DB) error {
		s := t.journal(runID, "off", map[string]string{"text": text, "switch": o.Code})
		if ag.eng != nil && s != nil {
			ag.eng.emitStep(t, runID, s)
		}
		return nil
	})
}

// wsOffRefuses (llmRefuseHooks) refuses a workspace conversation's model
// call while the switch is off — xbind's connector would — in the
// person's words.
func wsOffRefuses(req LLMRequest) error {
	if s := wsSessionOfCall(req); s == nil {
		return nil
	}
	if o := wsOffCached(); o.Code != "" {
		return errors.New(o.turnWords())
	}
	return nil
}

func init() { llmRefuseHooks = append(llmRefuseHooks, wsOffRefuses) }
