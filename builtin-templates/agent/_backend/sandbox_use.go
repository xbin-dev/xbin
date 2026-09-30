// sandbox_use.go — the one check every sandbox tool makes before it touches
// a sandbox (D115), on every call: a binding is only as good as the rights
// behind it right now.
package main

import (
	"context"
	"fmt"
	"strings"
)

// sbxUse is a sandbox a conversation's tool may work in, right now.
type sbxUse struct {
	Conn    *sbxConn       // its manager, called for the person who bound it (Sbx-User)
	ID      string         // the sandbox's id at the manager
	Binding SandboxBinding // as the conversation stores it
	Box     *sbxSandbox    // the sandbox as the manager just described it
	Hello   *sbxHello      // what the manager offers (caps, limits)
	Cwd     string         // where to work: the binding's cwd, else the sandbox's workdir
}

// sandboxUse resolves the sandbox a tool call of a conversation works in —
// ref "" is the active one; another must be attached — and checks, every
// call, that it still may:
//
//   - the calling run's copy of the binding is still the conversation's:
//     the root still has the sandbox bound or attached — for a subagent, by
//     the same binder (a detach reaches the turn in flight and every
//     subagent at once),
//   - the class allows the sandbox toolset and the manager,
//   - the manager is still bound (and speaks protocol 1 with exec and files),
//   - the sandbox still exists (asked of the manager, fresh),
//   - the class allows its egress as it is now — the less restrictive of
//     what it has and what it takes at its next start (effectiveEgress); an
//     egress that changed since it was bound is recorded, and in Approve
//     mode may refuse the call once: egressChanged, sandbox_turn.go,
//   - the class firewall across conversations: a class that reaches outside
//     may not work in a sandbox that has held internal data, and one with
//     internal reach marks the sandbox it works in (sandbox_access.go) — as
//     does a conversation that has held internal data (the root's
//     HeldInternal), which working in a marked sandbox makes it,
//   - whoever bound it may still use it, and still takes part in the
//     conversation (anyone who may steer a conversation works in what it
//     has bound — under the binder's right, which is re-checked here).
//
// root is the conversation (whose members count); cfg is the calling run's
// config (a subagent's is its own copy; the calling run is the context's
// sbxCall). Errors are *sbxError, in words a model can act on (and a
// refusal for programs).
func (ag *Agent) sandboxUse(ctx context.Context, root int64, cfg Config, ref string) (*sbxUse, error) {
	var b SandboxBinding
	switch {
	case ref == "" && cfg.Sandbox == nil:
		return nil, &sbxError{Refusal: "none", Msg: "no sandbox is bound to this conversation — ask the user to pick one (the sandbox picker beside the model)"}
	case ref == "":
		b = *cfg.Sandbox
	default:
		var ok bool
		if b, ok = cfg.sandboxBinding(ref); !ok {
			return nil, &sbxError{Refusal: "not-attached", Msg: fmt.Sprintf("sandbox %s isn't attached to this conversation", ref)}
		}
	}
	provider, id, ok := splitSandboxRef(b.Ref)
	if !ok {
		return nil, &sbxError{Refusal: "invalid", Msg: fmt.Sprintf("%q is not a sandbox reference", b.Ref)}
	}
	if run := sbxCallOf(ctx).run; run != 0 {
		// the calling run's copy is as its turn began: the root's stored
		// bindings are what the conversation has now
		rc, err := ag.db.runConfig(root)
		rb, ok := rc.sandboxBinding(b.Ref)
		switch {
		case run == root && (err != nil || !ok):
			return nil, &sbxError{Refusal: "not-attached", Msg: fmt.Sprintf("the sandbox %q was detached from this conversation during this turn — ask the user to bind it again, or use another",
				orStr(b.Name, b.Ref))}
		case run != root && (err != nil || !ok || rb.By != b.By):
			return nil, &sbxError{Refusal: "not-attached", Msg: fmt.Sprintf("the sandbox %q is no longer attached to this conversation as it was when this subagent started (detached, or bound again by someone else) — tell your parent",
				orStr(b.Name, b.Ref))}
		}
	}
	if why := sandboxClassAllows(cfg, provider, ""); why != "" {
		return nil, &sbxError{Refusal: "not-allowed", Msg: why}
	}
	binder := binderWho(b.By)
	conn, err := sbxDial(provider, sbxUserOf(binder))
	if err != nil {
		return nil, err
	}
	hello, err := managerHello(ctx, conn.M)
	if err != nil {
		return nil, err
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		if sbxRefusal(err) == "not-found" {
			return nil, ag.sandboxGone(ctx, root, provider, b)
		}
		return nil, err
	}
	if why := partitionBoxRefusal(box); why != "" { // at every use, not only when bound (sandbox_partition.go)
		return nil, &sbxError{Provider: provider, Refusal: "not-allowed", Msg: why}
	}
	if why := hostedHarnessRefusal(root, b.Ref, box.Name); why != "" { // a coding agent's sign-in stays its person's (harness_partition.go)
		return nil, &sbxError{Provider: provider, Refusal: "not-allowed", Msg: why}
	}
	live := box.effectiveEgress()
	if why := sandboxClassAllows(cfg, provider, live); why != "" {
		return nil, &sbxError{Provider: provider, Refusal: "not-allowed", Msg: why + " (it has changed since it was bound)"}
	}
	cl := classOf(cfg)
	if why := taintRefusal(cl, box); why != "" {
		return nil, &sbxError{Provider: provider, Refusal: "not-allowed", Msg: why}
	}
	if !sandboxAccess(binder, box).Use {
		return nil, &sbxError{Provider: provider, Refusal: "not-allowed",
			Msg: fmt.Sprintf("%s, who bound the sandbox %q, may no longer use it — ask the user to bind it again, or another", byName(b.By), box.Name)}
	}
	if binder.kind != whoSystem {
		a, err := ag.aclOf(root)
		if err != nil {
			return nil, err
		}
		if a.level(binder) < lvParticipant {
			return nil, &sbxError{Provider: provider, Refusal: "not-allowed",
				Msg: fmt.Sprintf("%s, who bound the sandbox %q, no longer takes part in this conversation — ask the user to bind it again", byName(b.By), box.Name)}
		}
	}
	held := cfg.HeldInternal
	if !held && !cl.has(tsInternal) && !cl.egress() { // (a class that reaches outside never works in a marked one: taintRefusal)
		rc, err := ag.db.runConfig(root) // the root's, now: an earlier call may have set it
		if err != nil {
			return nil, err
		}
		held = rc.HeldInternal
		if !held && box.marked() {
			if err := ag.holdInternal(ctx, root, b.Ref); err != nil {
				return nil, err
			}
		}
	}
	if !box.marked() && (cl.has(tsInternal) || held) {
		if err := markInternal(ctx, conn, id, box); err != nil {
			return nil, &sbxError{Provider: provider, Refusal: orStr(sbxRefusal(err), "unavailable"),
				Msg: fmt.Sprintf("the sandbox %q must be marked as holding internal data before this conversation works in it, and marking it failed: %v", box.Name, err)}
		}
	}
	if live != b.Egress {
		if err := ag.egressChanged(ctx, root, cfg, b, ref, live); err != nil {
			return nil, err
		}
		b.Egress = live
	}
	cwd := b.Cwd
	if cwd == "" {
		cwd = box.Workdir
	}
	return &sbxUse{Conn: conn, ID: id, Binding: b, Box: box, Hello: hello, Cwd: cwd}, nil
}

// hasCap: the manager offers capability c — asked again (managerHelloFresh)
// before answering no, so a cached hello from before an update never
// refuses; u.Hello becomes what it said.
func (u *sbxUse) hasCap(ctx context.Context, c string) bool {
	if u.Hello.has(c) {
		return true
	}
	if h, err := managerHelloFresh(ctx, u.Conn.M); err == nil {
		u.Hello = h
	}
	return u.Hello.has(c)
}

// sandboxGone: the manager says a bound sandbox is not found (deleted
// elsewhere — an operator, another tile — or no longer shared with its
// binder), so the binding can never work again. It comes off the
// conversation at once, in one transaction: were it left, every later tool
// call would hit the same wall, and a model can't switch the active
// sandbox. When it was the active one, the first other attached sandbox
// becomes active (the sandbox tools need an active one — sandboxToolsOn).
// The refusal says what changed.
func (ag *Agent) sandboxGone(ctx context.Context, root int64, provider string, b SandboxBinding) error {
	name := orStr(b.Name, b.Ref)
	var was, now bool
	var next *SandboxBinding
	var rest []string
	err := ag.db.Tx(func(t *DB) error {
		was, now, next, rest = false, false, nil, nil
		err := storeBinding(t, root, func(cfg *Config) error {
			was = cfg.Sandbox != nil && cfg.Sandbox.Ref == b.Ref
			if now = detachSandbox(cfg, b.Ref); !now {
				return nil
			}
			if was && len(cfg.Attached) > 0 {
				a := cfg.Attached[0]
				cfg.Sandbox = &a
			}
			if cfg.Sandbox != nil {
				n := *cfg.Sandbox
				next = &n
			}
			for _, a := range cfg.Attached {
				rest = append(rest, fmt.Sprintf("%q", orStr(a.Name, a.Ref)))
			}
			return nil
		})
		if err == nil && now && ag.eng != nil {
			ag.eng.emitRun(t, root)
		}
		return err
	})
	e := &sbxError{Provider: provider, Refusal: "not-found"}
	switch {
	case err != nil:
		e.Msg = fmt.Sprintf("the sandbox %q is gone (deleted, or no longer shared with this agent), and detaching it failed (%v) — ask the user to bind another", name, err)
	case !now: // already off the conversation (another call took it off): the root's config says what is bound
		e.Msg = fmt.Sprintf("the sandbox %q is gone (deleted, or no longer shared with this agent) and is no longer attached — sandbox_info says what is", name)
	case sbxCallOf(ctx).run != 0 && sbxCallOf(ctx).run != root:
		e.Msg = fmt.Sprintf("the sandbox %q is gone (deleted, or no longer shared with this agent) and was detached from the conversation — tell your parent", name)
	case next == nil:
		e.Msg = fmt.Sprintf("the sandbox %q is gone (deleted, or no longer shared with this agent) and was detached; no sandbox is bound now — ask the user to bind one, or sandbox_create", name)
	case was:
		e.Msg = fmt.Sprintf("the sandbox %q is gone (deleted, or no longer shared with this agent) and was detached; the active sandbox is now %q from your next step (attached: %s)",
			name, orStr(next.Name, next.Ref), strings.Join(rest, ", "))
	default:
		e.Msg = fmt.Sprintf("the sandbox %q is gone (deleted, or no longer shared with this agent) and was detached; the active sandbox is still %q", name, orStr(next.Name, next.Ref))
	}
	return e
}

// holdInternal: the conversation root works in a marked sandbox (ref), so
// it has held internal data from now on — recorded on the root (every tool
// call of it and its subagents marks the sandbox it works in first), and
// every other sandbox it has attached is marked now (markAttached).
func (ag *Agent) holdInternal(ctx context.Context, root int64, ref string) error {
	var cfg Config
	err := ag.db.Tx(func(t *DB) error {
		return storeBinding(t, root, func(c *Config) error {
			c.HeldInternal, cfg = true, *c
			return nil
		})
	})
	if err != nil {
		return fmt.Errorf("recording that this conversation has worked in a sandbox holding internal data: %w", err)
	}
	markAttached(ctx, cfg, ref)
	return nil
}

// byName is a binding's By for people to read.
func byName(by string) string {
	switch {
	case by == "":
		return "the agent itself"
	case len(by) > 3 && by[:3] == "el:":
		return by[3:]
	}
	return by
}
