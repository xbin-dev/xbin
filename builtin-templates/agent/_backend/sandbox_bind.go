// sandbox_bind.go — a sandbox bound to a conversation (D115): Config.Sandbox,
// the one its tools work in, and Config.Attached, every sandbox it has
// attached (≤ 8, the active one among them). Both live in the root's
// runs.config like D111's model pick: read every turn (a rebind applies from
// the next one) and copied into subagents (childConfig).
//
// Binding takes participant access to the conversation AND the right to use
// the sandbox (sandbox_access.go), and the conversation's class must allow it
// (the sandbox toolset, the manager, the sandbox's egress). The binding
// records who bound it: tools act for that person (Sbx-User), and every tool
// call re-checks that they still may (sandbox_use.go).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// SandboxBinding is a sandbox bound to a conversation.
type SandboxBinding struct {
	Ref     string `json:"ref"`               // <provider>[#inst]|<id>
	Cwd     string `json:"cwd,omitempty"`     // where the tools work ("" = the sandbox's workdir)
	Name    string `json:"name,omitempty"`    // the sandbox's name when bound (for display)
	Manager string `json:"manager,omitempty"` // its manager's title
	Image   string `json:"image,omitempty"`   // its image id
	// Tools are what its manager says the image has beyond a POSIX shell
	// (hello's images[].tools) when it was bound — the # Sandbox prompt
	// names them (D134). nil in a binding stored before.
	Tools  []string `json:"tools,omitempty"`
	Egress string   `json:"egress,omitempty"` // none | internet | open, when bound
	By     string   `json:"by,omitempty"`     // who bound it: a user id, "el:<component>", "" = the tile itself
	At     int64    `json:"at,omitempty"`     // when (unix ms)

	held bool // (not stored) it holds internal data: binding it sets the conversation's HeldInternal
}

// maxAttached bounds a conversation's attached sandboxes.
const maxAttached = 8

// sandboxPick is what a request names: a sandbox, and where in it to work.
type sandboxPick struct {
	Ref string `json:"ref"`
	Cwd string `json:"cwd"`
}

// copySandboxes deep-copies a config's bindings: subagents and workflows
// work in their root's sandbox, and changing theirs never changes the root's.
func copySandboxes(c Config) (*SandboxBinding, []SandboxBinding) {
	var active *SandboxBinding
	if c.Sandbox != nil {
		b := *c.Sandbox
		active = &b
	}
	if c.Attached == nil {
		return active, nil
	}
	return active, append([]SandboxBinding(nil), c.Attached...)
}

// sandboxBinding finds a ref among cfg's bindings (the active one, then the
// attached ones).
func (c Config) sandboxBinding(ref string) (SandboxBinding, bool) {
	if c.Sandbox != nil && c.Sandbox.Ref == ref {
		return *c.Sandbox, true
	}
	for _, b := range c.Attached {
		if b.Ref == ref {
			return b, true
		}
	}
	return SandboxBinding{}, false
}

// attachSandbox makes b the active sandbox, attaching it (or updating its
// attachment: a new cwd).
func attachSandbox(cfg *Config, b SandboxBinding) error {
	if b.held {
		cfg.HeldInternal = true
	}
	for i := range cfg.Attached {
		if cfg.Attached[i].Ref == b.Ref {
			cfg.Attached[i] = b
			cfg.Sandbox = &b
			return nil
		}
	}
	if len(cfg.Attached) >= maxAttached {
		return errBadRequest(fmt.Sprintf("a conversation attaches at most %d sandboxes — detach one first", maxAttached))
	}
	cfg.Attached = append(cfg.Attached, b)
	cfg.Sandbox = &b
	return nil
}

// detachSandbox takes ref off cfg (the active one too); false if it wasn't
// there.
func detachSandbox(cfg *Config, ref string) bool {
	found := false
	if cfg.Sandbox != nil && cfg.Sandbox.Ref == ref {
		cfg.Sandbox, found = nil, true
	}
	kept := cfg.Attached[:0:0]
	for _, b := range cfg.Attached {
		if b.Ref == ref {
			found = true
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) == 0 {
		kept = nil
	}
	cfg.Attached = kept
	return found
}

// --- refusals ------------------------------------------------------------------

// bindError is a refused binding or sandbox operation: a status and why.
type bindError struct {
	status int
	msg    string
}

func (e *bindError) Error() string { return e.msg }

func refuse(status int, format string, args ...any) error {
	return &bindError{status: status, msg: fmt.Sprintf(format, args...)}
}

// sbxStatus is the status a manager's refusal answers our caller with.
func sbxStatus(refusal string) int {
	switch refusal {
	case "invalid":
		return http.StatusBadRequest
	case "not-found", "unbound":
		return http.StatusNotFound
	case "not-allowed":
		return http.StatusForbidden
	case "state", "exists", "partitions": // partitions: an old manager in a person's partition (sandbox_partition.go)
		return http.StatusConflict
	case "precondition":
		return http.StatusPreconditionFailed
	case "too-large":
		return http.StatusRequestEntityTooLarge
	case "limit":
		return http.StatusTooManyRequests
	case "unsupported":
		return http.StatusNotImplemented
	case "lost":
		return http.StatusGone
	}
	return http.StatusBadGateway // unavailable, unreachable, protocol: the manager's trouble
}

// writeSbxErr answers a refusal: ours, a manager's (with its refusal and
// state for programs), or a transaction's.
func writeSbxErr(w http.ResponseWriter, err error) {
	var be *bindError
	var se *sbxError
	switch {
	case errors.As(err, &be):
		xbin.WriteError(w, be.status, be.msg)
	case errors.As(err, &se):
		body := map[string]any{"error": se.Error(), "refusal": se.Refusal}
		if se.State != "" {
			body["state"] = se.State
		}
		xbin.WriteJSON(w, sbxStatus(se.Refusal), body)
	default:
		writeTxErr(w, err)
	}
}

// --- binding -----------------------------------------------------------------------

// cleanCwd checks a working directory: absolute, bounded ("" = the
// sandbox's workdir).
func cleanCwd(p string) (string, bool) {
	if p == "" {
		return "", true
	}
	if !strings.HasPrefix(p, "/") || len(p) > 4096 || strings.ContainsRune(p, 0) {
		return "", false
	}
	return path.Clean(p), true
}

// sandboxClassAllows says why a conversation of cfg's class may not work in a
// sandbox of provider with egress ("" = it may). egress "" skips that check
// (asked before there is a sandbox); a sandbox's egress is its
// effectiveEgress, never "".
func sandboxClassAllows(cfg Config, provider, egress string) string {
	cl := classOf(cfg)
	name := cl.Name
	if name == "" {
		name = cl.ID
	}
	switch {
	case !cl.has("sandbox"):
		return fmt.Sprintf("this conversation's class (%s) has no sandbox toolset", name)
	case !cl.allowsManager(provider):
		return fmt.Sprintf("this conversation's class (%s) doesn't allow sandboxes from %s", name, provider)
	case egress != "" && !cl.allowsEgress(egress):
		return fmt.Sprintf("this conversation's class (%s) doesn't allow a sandbox with egress %q", name, egress)
	}
	return ""
}

// prepareBinding checks that w may bind pick to a conversation of cfg's
// class, and returns the binding (not yet stored). The caller has checked
// w's access to the conversation. No cwd keeps the one the conversation has
// the sandbox attached at (a re-pick), else it is the sandbox's workdir. A
// class with internal reach — or a conversation that has held internal data
// (cfg.HeldInternal) — marks the sandbox (markInternal) before it is bound;
// one that reaches outside may not bind a sandbox so marked. Binding a
// marked one into a conversation that hasn't held internal data yet marks
// every sandbox it has attached (markAttached), and the binding sets its
// HeldInternal when stored.
func prepareBinding(ctx context.Context, w who, cfg Config, pick sandboxPick) (SandboxBinding, error) {
	provider, id, ok := splitSandboxRef(pick.Ref)
	if !ok {
		return SandboxBinding{}, refuse(400, "sandbox.ref: a reference from GET /sandboxes (<manager>|<id>)")
	}
	cwd, ok := cleanCwd(pick.Cwd)
	if !ok {
		return SandboxBinding{}, refuse(400, "sandbox.cwd: an absolute path in the sandbox")
	}
	if why := sandboxClassAllows(cfg, provider, ""); why != "" {
		return SandboxBinding{}, refuse(403, "%s", why)
	}
	conn, err := sbxDial(provider, sbxUserOf(w))
	if err != nil {
		return SandboxBinding{}, err
	}
	hello, err := managerHello(ctx, conn.M)
	if err != nil {
		return SandboxBinding{}, err
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		return SandboxBinding{}, err
	}
	if why := partitionBoxRefusal(box); why != "" { // sandbox_partition.go
		return SandboxBinding{}, refuse(403, "%s", why)
	}
	if !sandboxAccess(w, box).Use {
		return SandboxBinding{}, refuse(403, "you may not use this sandbox (%s) — its owner can add you as a member", box.Name)
	}
	egress := box.effectiveEgress()
	if why := sandboxClassAllows(cfg, provider, egress); why != "" {
		return SandboxBinding{}, refuse(403, "%s", why)
	}
	cl := classOf(cfg)
	if why := taintRefusal(cl, box); why != "" {
		return SandboxBinding{}, refuse(403, "%s", why)
	}
	if !box.hasCap("exec") || !box.hasCap("files") {
		return SandboxBinding{}, refuse(409, "this sandbox offers no commands or files")
	}
	if cwd == "" {
		cwd = box.Workdir
		if old, ok := cfg.sandboxBinding(pick.Ref); ok && old.Cwd != "" {
			cwd = old.Cwd
		}
	} else if box.State == "running" {
		st, err := conn.Stat(ctx, id, cwd)
		switch {
		case sbxRefusal(err) == "not-found":
			return SandboxBinding{}, refuse(400, "sandbox.cwd: %s doesn't exist in the sandbox", cwd)
		case err != nil:
			return SandboxBinding{}, err
		case st.Type != "dir":
			return SandboxBinding{}, refuse(400, "sandbox.cwd: %s isn't a directory", cwd)
		}
	}
	if !box.marked() && (cl.has(tsInternal) || cfg.HeldInternal) {
		why := "this conversation's class has internal reach"
		if !cl.has(tsInternal) {
			why = "this conversation has had a sandbox holding internal data"
		}
		if err := markInternal(ctx, conn, id, box); err != nil {
			return SandboxBinding{}, refuse(sbxStatus(sbxRefusal(err)),
				"%s, so the sandbox must be marked as holding internal data (label %s) before it is bound, and marking it failed: %v",
				why, sbxInternalLabel, err)
		}
	}
	if box.marked() && !cfg.HeldInternal && !cl.has(tsInternal) {
		markAttached(ctx, cfg, pick.Ref) // what it holds could reach any of them from here
	}
	return SandboxBinding{Ref: pick.Ref, Cwd: cwd, Name: box.Name, Manager: hello.title(provider),
		Image: box.Image.ID, Tools: hello.imageTools(box.Image.ID), Egress: egress, By: w.tag(), At: nowMs(), held: box.marked()}, nil
}

// storeBinding applies a change to root's stored config inside t. A sandbox
// the change takes off the conversation stops the jobs it still runs there
// (stopDetachedJobs) — every detach path comes through here.
func storeBinding(t *DB, root int64, change func(*Config) error) error {
	cfg, err := t.runConfig(root)
	if err != nil {
		return err
	}
	before := cfg
	before.Sandbox, before.Attached = copySandboxes(cfg)
	if err := change(&cfg); err != nil {
		return err
	}
	raw, _ := json.Marshal(cfg)
	if _, err = t.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), root); err != nil {
		return err
	}
	t.stopDetachedJobs(root, before, cfg)
	return nil
}

// --- PATCH /runs/{id} {sandbox, detach} --------------------------------------------

// sandboxChange is PATCH /runs/{id}'s sandbox part, resolved (the manager
// asked) before the handler's transaction applies it.
type sandboxChange struct {
	bind   *SandboxBinding // the new active sandbox
	unbind bool            // sandbox: null — no active one (it stays attached)
	detach string          // a ref to take off
}

// patchSandbox reads {sandbox: {ref, cwd} | null, detach: <ref>}: nil when
// the request has neither; ok false once it has answered a refusal.
func patchSandbox(w http.ResponseWriter, r *http.Request, lv level, root int64, raw json.RawMessage, detach *string) (*sandboxChange, bool) {
	if raw == nil && detach == nil {
		return nil, true
	}
	if lv < lvParticipant {
		xbin.WriteError(w, 403, "only someone who may talk in it can change its sandbox")
		return nil, false
	}
	sc := &sandboxChange{}
	if detach != nil {
		sc.detach = *detach
	}
	if raw != nil {
		var pick *sandboxPick
		if err := json.Unmarshal(raw, &pick); err != nil {
			xbin.WriteError(w, 400, "sandbox: {ref, cwd?} or null")
			return nil, false
		}
		if pick == nil {
			sc.unbind = true
		} else {
			cfg, err := agent.db.runConfig(root)
			if err != nil {
				xbin.WriteError(w, 404, "no such run")
				return nil, false
			}
			b, err := prepareBinding(r.Context(), callerOf(r), cfg, *pick)
			if err != nil {
				writeSbxErr(w, err)
				return nil, false
			}
			sc.bind = &b
		}
	}
	return sc, true
}

// apply stores the change in root's config (inside the handler's
// transaction).
func (sc *sandboxChange) apply(t *DB, root int64) error {
	return storeBinding(t, root, func(cfg *Config) error {
		if sc.detach != "" {
			detachSandbox(cfg, sc.detach)
		}
		if sc.unbind {
			cfg.Sandbox = nil
		}
		if sc.bind != nil {
			return attachSandbox(cfg, *sc.bind)
		}
		return nil
	})
}

// --- POST /ask {sandbox} -------------------------------------------------------------

type askSandboxKey struct{}

// withAskSandbox lifts POST /ask's {sandbox} out of the body before the
// handler reads the rest; askSandbox binds it once the new conversation's
// config is known.
func withAskSandbox(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			xbin.WriteError(w, 400, "bad body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var b struct {
			Sandbox *sandboxPick `json:"sandbox"`
		}
		if json.Unmarshal(raw, &b) == nil && b.Sandbox != nil {
			r = r.WithContext(context.WithValue(r.Context(), askSandboxKey{}, b.Sandbox))
		}
		h(w, r)
	}
}

// askSandbox binds the ask's sandbox, if it named one, into the new
// conversation's config — the caller owns it, so their right to use the
// sandbox is what counts. False once it has answered a refusal.
func askSandbox(w http.ResponseWriter, r *http.Request, cfg *Config) bool {
	pick, _ := r.Context().Value(askSandboxKey{}).(*sandboxPick)
	if pick == nil || pick.Ref == "" {
		return true
	}
	b, err := prepareBinding(r.Context(), callerOf(r), *cfg, *pick)
	if err != nil {
		writeSbxErr(w, err)
		return false
	}
	cfg.Sandbox, cfg.Attached = &b, []SandboxBinding{b}
	cfg.HeldInternal = cfg.HeldInternal || b.held
	return true
}
