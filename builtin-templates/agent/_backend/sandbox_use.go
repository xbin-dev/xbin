// sandbox_use.go — the one check every sandbox tool makes before it touches
// a sandbox (D115), on every call: a binding is only as good as the rights
// behind it right now.
package main

import (
	"context"
	"fmt"
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
//   - a subagent's copy of the binding is still the conversation's: the
//     root still has the sandbox bound or attached, by the same binder (a
//     detach reaches every subagent at once),
//   - the class allows the sandbox toolset and the manager,
//   - the manager is still bound (and speaks protocol 1 with exec and files),
//   - the sandbox still exists (asked of the manager, fresh),
//   - the class allows its egress as it is now — the less restrictive of
//     what it has and what it takes at its next start (effectiveEgress); an
//     egress that changed since it was bound is recorded, and in Approve
//     mode may refuse the call once: egressChanged, sandbox_turn.go,
//   - the class firewall across conversations: a class that reaches outside
//     may not work in a sandbox that has held internal data, and one with
//     internal reach marks the sandbox it works in (sandbox_access.go),
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
	if run := sbxCallOf(ctx).run; run != 0 && run != root {
		rc, err := ag.db.runConfig(root)
		rb, ok := rc.sandboxBinding(b.Ref)
		if err != nil || !ok || rb.By != b.By {
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
			return nil, &sbxError{Provider: provider, Refusal: "not-found",
				Msg: fmt.Sprintf("the sandbox %q is gone (deleted, or no longer shared with this agent) — ask the user to bind another", b.Name)}
		}
		return nil, err
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
	if cl.has(tsInternal) {
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
