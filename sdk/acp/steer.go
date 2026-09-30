package acp

// Steering (the adapters' _session/steering extension — claude-agent-acp,
// codex-acp — advertised as initialize's _meta.steering.supported): a
// message for the turn that is running, taken in at the agent's next step
// instead of waiting for the turn's end. The client asks with idleBehavior
// promptRequired, so an agent with no turn running hands the message back
// ("promptRequired": send it as the next prompt) instead of starting a turn
// no session/prompt answers — claude honours that; codex-acp 1.13.1 ignores
// it and starts one ("startedNewTurn"), whose end no turn.end reports.

import (
	"context"
	"errors"
	"fmt"
)

// Steer outcomes.
const (
	SteerInjected       = "injected"       // taken into the running turn
	SteerPromptRequired = "promptRequired" // no turn runs: send it with Prompt
	SteerStartedNewTurn = "startedNewTurn" // the agent started a turn of its own with it (no turn.end follows)
)

// ErrSteeringUnsupported: the agent did not advertise _session/steering —
// hold the message for the next turn.
var ErrSteeringUnsupported = errors.New("this agent takes no messages mid-turn (it did not advertise _session/steering) — send it once the turn ends")

// Steer gives the running turn a message. Without a turn running it
// answers SteerPromptRequired at once (the agent is not asked). The agent's
// outcome otherwise: SteerInjected (the message joined the turn; a user
// message.delta with steered:true records it), SteerPromptRequired (the
// turn had just ended), SteerStartedNewTurn. Files go as in a prompt
// (ClientOptions.Drop).
func (c *Client) Steer(ctx context.Context, p Prompt) (string, error) {
	c.mu.Lock()
	closed, steering, busy, sid, caps := c.closed, c.steering, c.busy, c.sessionID, c.promptCaps
	c.mu.Unlock()
	switch {
	case closed:
		return "", ErrEnded
	case !steering:
		return "", ErrSteeringUnsupported
	case !busy:
		return SteerPromptRequired, nil
	}
	if err := c.refuseFiles(p, caps); err != nil {
		return "", err
	}
	blocks, infos, err := c.promptBlocks(ctx, p, caps)
	if err != nil {
		return "", err
	}
	id := c.conn.newID()
	resp, err := c.conn.callID(ctx, id, MSessionSteering, SteerParams{SessionID: sid, Prompt: blocks,
		Meta: map[string]any{"steering": map[string]any{"idleBehavior": SteerPromptRequired}}})
	if err != nil {
		return "", err
	}
	var res SteerResult
	if err := unmarshalResult(resp, &res); err != nil {
		return "", err
	}
	switch res.Outcome {
	case SteerInjected, SteerStartedNewTurn:
		echo := map[string]any{"role": "user", "text": p.Text, "steered": true}
		if len(infos) > 0 {
			echo["attachments"] = infos
		}
		c.emitW(&Wire{RPCID: id}, NewEvent(EvMessageDelta, echo))
		return res.Outcome, nil
	case SteerPromptRequired:
		return res.Outcome, nil
	}
	return res.Outcome, fmt.Errorf("the agent could not take the message mid-turn (outcome %q)", res.Outcome)
}
