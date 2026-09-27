// sandbox_turn.go — what a call's sandbox tool changes in the conversation's
// bindings, while the turn goes on (D115).
//
// A turn reads its config once (a rebind from outside applies from the next
// turn), but two changes come from inside a step and must reach the very
// next one: a sandbox sandbox_create just bound (sandbox_create.go), and a
// sandbox whose network access changed since it was bound (egressChanged:
// the stored bindings take the live value, and Approve mode re-asks rather
// than run a side effect on an egress nobody approved it for). The tool
// notes them in sbxTurn; execTools applies them to the turn's config after
// the batch.
package main

import (
	"context"
	"fmt"
	"sync"
)

// sbxTurn carries what a step's sandbox tools changed in the conversation's
// bindings back to its turn (turnState.sbx).
type sbxTurn struct {
	mu     sync.Mutex
	added  []sbxAdded
	egress map[string]string // ref → its live egress
}

type sbxAdded struct {
	b      SandboxBinding
	active bool
}

type sbxTurnKey struct{}

func withSbxTurn(ctx context.Context, n *sbxTurn) context.Context {
	if n == nil {
		return ctx
	}
	return context.WithValue(ctx, sbxTurnKey{}, n)
}

func sbxTurnOf(ctx context.Context) *sbxTurn {
	n, _ := ctx.Value(sbxTurnKey{}).(*sbxTurn)
	return n
}

// noteSandbox: b was bound into the conversation (active, or attached).
func noteSandbox(ctx context.Context, b SandboxBinding, active bool) {
	if n := sbxTurnOf(ctx); n != nil {
		n.mu.Lock()
		n.added = append(n.added, sbxAdded{b, active})
		n.mu.Unlock()
	}
}

// noteEgress: ref's egress is now live.
func noteEgress(ctx context.Context, ref, live string) {
	if n := sbxTurnOf(ctx); n != nil {
		n.mu.Lock()
		if n.egress == nil {
			n.egress = map[string]string{}
		}
		n.egress[ref] = live
		n.mu.Unlock()
	}
}

// apply brings what the step changed into cfg (and forgets it).
func (n *sbxTurn) apply(cfg *Config) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.added) == 0 && len(n.egress) == 0 {
		return
	}
	cfg.Sandbox, cfg.Attached = copySandboxes(*cfg) // never write through to a copy another run holds
	for _, a := range n.added {
		if a.active {
			_ = attachSandbox(cfg, a.b)
		} else if _, err := addSandbox(cfg, a.b); err != nil {
			logf("sandbox %s: not in this turn's config: %v", a.b.Ref, err)
		}
	}
	for ref, e := range n.egress {
		setEgress(cfg, ref, e)
	}
	n.added, n.egress = nil, nil
}

// setEgress records ref's egress in cfg's bindings; false when none changed.
func setEgress(cfg *Config, ref, egress string) bool {
	changed := false
	if cfg.Sandbox != nil && cfg.Sandbox.Ref == ref && cfg.Sandbox.Egress != egress {
		b := *cfg.Sandbox
		b.Egress, cfg.Sandbox, changed = egress, &b, true
	}
	for i := range cfg.Attached {
		if cfg.Attached[i].Ref == ref && cfg.Attached[i].Egress != egress {
			cfg.Attached[i].Egress, changed = egress, true
		}
	}
	return changed
}

// --- the call ------------------------------------------------------------------------

// sbxCall is the tool call a sandboxUse is made for (runSandboxTool).
type sbxCall struct {
	run     int64  // the calling run (a subagent keeps its own copy of the bindings)
	name    string // the tool
	approve bool   // the conversation is in Approve mode
}

type sbxCallKey struct{}

func withSbxCall(ctx context.Context, c sbxCall) context.Context {
	return context.WithValue(ctx, sbxCallKey{}, c)
}

func sbxCallOf(ctx context.Context) sbxCall {
	c, _ := ctx.Value(sbxCallKey{}).(sbxCall)
	return c
}

// egressChanged handles a sandbox whose live egress isn't the one its
// binding recorded: the stored bindings — the conversation's, and the
// calling run's own copy — take the live value (and say so on the run
// events), the turn has it from its next step, and in Approve mode a call
// that would park under the new egress is refused, once: called again, it
// is parked and asked for. ref is what the call asked sandboxUse for ("" =
// the active sandbox: what the changing tools work in).
func (ag *Agent) egressChanged(ctx context.Context, root int64, cfg Config, b SandboxBinding, ref, live string) error {
	call := sbxCallOf(ctx)
	runs := []int64{root}
	if call.run != 0 && call.run != root {
		runs = append(runs, call.run)
	}
	err := ag.db.Tx(func(t *DB) error {
		for _, id := range runs {
			changed := false
			if err := storeBinding(t, id, func(c *Config) error {
				changed = setEgress(c, b.Ref, live)
				return nil
			}); err != nil {
				return err
			}
			if changed && ag.eng != nil {
				ag.eng.emitRun(t, id)
			}
		}
		return nil
	})
	if err != nil {
		logf("sandbox %s: recording its egress %s in #%d: %v", b.Ref, live, root, err)
	}
	noteEgress(ctx, b.Ref, live)
	if !call.approve || (ref != "" && call.name != "sandbox_copy") {
		return nil
	}
	now := cfg
	now.Sandbox, now.Attached = copySandboxes(cfg)
	setEgress(&now, b.Ref, live)
	if !sandboxSideEffect(call.name, now) {
		return nil
	}
	return &sbxError{Refusal: "egress-changed", Msg: fmt.Sprintf("the sandbox's network access changed from %s to %s; call the tool again to ask for approval",
		orStr(b.Egress, "unknown"), live)}
}
