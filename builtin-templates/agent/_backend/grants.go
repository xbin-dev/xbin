// grants.go — what a conversation's owner let its agent do beyond its own
// reach, for a while (D111). The capabilities are a registry (grantDefs): each
// says when a step needs it and what the card, the push and the model are
// told. capThreads is reading the owner's other conversations and automations
// with the thread tools (threads_tools.go); capSandboxes is creating a coding
// sandbox for the conversation (sandbox_create.go). Adding a capability is
// one entry.
//
// A grant is asked for by parking the step like an approval (pendingState
// .Grant) and only the conversation's owner — the person whose threads they
// are — answers it: "once" runs the parked calls and keeps nothing; "hour"
// also stores a row that later calls in this conversation find until it
// expires. Expiry is read where a grant is used; nothing ticks. The owner
// can take it back at any time (DELETE /runs/{id}/grants/{cap}).
package main

import (
	"context"
	"net/http"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	capThreads   = "threads"
	capSandboxes = "sandboxes"
	// grantFor is how long "allow here for 1 hour" lasts.
	grantFor = time.Hour
)

// grantDef is one capability a conversation's owner can grant.
type grantDef struct {
	Cap string
	// Ask completes "The agent asks to …" (the approval card, the push).
	Ask string
	// Chip is a live grant's short label ("reads your threads").
	Chip string
	// Denied is the parked calls' result when the grant is denied.
	Denied string
	// Forbid is the backstop error when a call runs without the grant
	// (execTools parks for it first).
	Forbid string
	// Needed: do this step's calls need the grant, and don't have it yet?
	// Refusals and misses are not parked — the call runs and reports them.
	Needed func(ag *Agent, run *Run, cfg Config, calls []toolCall, own map[string]bool) bool
	// AskFor, when set, words this step's ask (what exactly the calls will
	// do) instead of Ask; it is stored with the parked step
	// (pendingState.GrantAsk), so it may ask a manager once.
	AskFor func(ag *Agent, run *Run, cfg Config, calls []toolCall, own map[string]bool) string
}

// grantDefs is every capability a grant can name, in the order a step's
// calls are checked against them.
var grantDefs = []grantDef{
	{
		Cap:    capThreads,
		Ask:    "read your other conversations and automations",
		Chip:   "reads your threads",
		Denied: "(denied: the owner did not allow reading their other conversations — scope mine still works)",
		Forbid: "reading the person's other conversations needs their permission — call it again to ask them",
		Needed: threadsGrantNeeded,
	},
	{
		Cap:    capSandboxes,
		Ask:    "create a coding sandbox for this conversation",
		Chip:   "creates sandboxes",
		Denied: "(denied: the owner did not allow creating a sandbox — ask them to bind one, or go on without it)",
		Forbid: "creating a sandbox needs the conversation owner's permission — call it again to ask them",
		Needed: sandboxesGrantNeeded,
		AskFor: sandboxesGrantAsk,
	},
}

// grantCaps are the capabilities a grant can name (the approve and revoke
// routes refuse anything else).
var grantCaps = func() map[string]bool {
	m := map[string]bool{}
	for _, g := range grantDefs {
		m[g.Cap] = true
	}
	return m
}()

// grantOf is capName's definition (the zero grantDef when unknown).
func grantOf(capName string) grantDef {
	for _, g := range grantDefs {
		if g.Cap == capName {
			return g
		}
	}
	return grantDef{Cap: capName}
}

// grantNeeded is the capability a step's calls need from the conversation's
// owner and don't have yet ("" = none).
func (ag *Agent) grantNeeded(run *Run, cfg Config, calls []toolCall, own map[string]bool) string {
	for _, g := range grantDefs {
		if g.Needed != nil && g.Needed(ag, run, cfg, calls, own) {
			return g.Cap
		}
	}
	return ""
}

// askFor is this step's ask: AskFor's words, else Ask's.
func (g grantDef) askFor(ag *Agent, run *Run, cfg Config, calls []toolCall, own map[string]bool) string {
	if g.AskFor != nil {
		if s := g.AskFor(ag, run, cfg, calls, own); s != "" {
			return s
		}
	}
	return g.Ask
}

// askText is how a push and a channel name a grant being asked for.
func (g grantDef) askText() string {
	if g.Ask != "" {
		return g.Ask
	}
	return "use “" + g.Cap + "”"
}

// grantView is a live grant as the conversation's view shows it.
type grantView struct {
	Cap       string `json:"cap"`
	GrantedBy string `json:"grantedBy"`
	ExpiresMs int64  `json:"expiresMs"`
	// Ask and Chip are the registry's words for it (the tile's chip).
	Ask  string `json:"ask,omitempty"`
	Chip string `json:"chip,omitempty"`
}

func nowMs() int64 { return time.Now().UnixMilli() }

// liveGrant: has root's owner granted capName, and not yet expired?
func (d *DB) liveGrant(root int64, capName string) bool {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM run_grants WHERE root_id=? AND cap=? AND expires_ms>?`, root, capName, nowMs()).Scan(&n)
	return n > 0
}

// setGrant stores (or extends) a grant until untilMs.
func (d *DB) setGrant(root int64, capName, by string, untilMs int64) error {
	_, err := d.q.Exec(`INSERT INTO run_grants (root_id, cap, granted_by, expires_ms, created_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(root_id, cap) DO UPDATE SET granted_by=excluded.granted_by, expires_ms=excluded.expires_ms, created_ms=excluded.created_ms`,
		root, capName, by, untilMs, nowMs())
	return err
}

// revokeGrant removes a grant; false when there was none live.
func (d *DB) revokeGrant(root int64, capName string) (bool, error) {
	live := d.liveGrant(root, capName)
	_, err := d.q.Exec(`DELETE FROM run_grants WHERE root_id=? AND cap=?`, root, capName)
	return live, err
}

// liveGrants is a conversation's grants still in force (never nil: a view
// that merges it must see an empty list replace an old one).
func (d *DB) liveGrants(root int64) []grantView {
	out := []grantView{}
	rows, err := d.q.Query(`SELECT cap, granted_by, expires_ms FROM run_grants WHERE root_id=? AND expires_ms>? ORDER BY cap`, root, nowMs())
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var g grantView
		if rows.Scan(&g.Cap, &g.GrantedBy, &g.ExpiresMs) == nil {
			d := grantOf(g.Cap)
			g.Ask, g.Chip = d.Ask, d.Chip
			out = append(out, g)
		}
	}
	return out
}

// A grant given once lives in the context of the parked calls it let run.
type grantOnceKey struct{}

func withGrantOnce(ctx context.Context, capName string) context.Context {
	return context.WithValue(ctx, grantOnceKey{}, capName)
}

func grantedOnce(ctx context.Context, capName string) bool {
	c, _ := ctx.Value(grantOnceKey{}).(string)
	return c != "" && c == capName
}

// The ask the owner answered (pendingState.GrantAsk) also travels with the
// parked calls it let run, once or for the hour: a call that would now do
// something else than it said can refuse (sandbox_create).
type grantAskedKey struct{}

type grantAsked struct{ cap, ask string }

func withGrantAsked(ctx context.Context, capName, ask string) context.Context {
	return context.WithValue(ctx, grantAskedKey{}, grantAsked{capName, ask})
}

// grantAskOf is the ask the owner allowed for capName ("", false: these
// calls weren't parked for it).
func grantAskOf(ctx context.Context, capName string) (string, bool) {
	g, ok := ctx.Value(grantAskedKey{}).(grantAsked)
	if !ok || g.cap != capName {
		return "", false
	}
	return g.ask, true
}

// grantOwner: may this caller answer a grant on root? Only the person who
// owns the conversation — whose threads they are. Not a manager, not the
// owner token, not an admin viewing as them.
func grantOwner(c who, root *Run) bool {
	return c.kind == whoUser && c.viewedBy == "" && person(root.Owner) && c.user == root.Owner
}

// handleRevokeGrant takes a grant back before it expires.
//
//	DELETE /runs/{id}/grants/{cap}
func handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	capName := r.PathValue("cap")
	if !grantCaps[capName] {
		xbin.WriteError(w, 404, "no such grant")
		return
	}
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	root := rootOf(run)
	was, err := agent.db.revokeGrant(root, capName)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if agent.eng != nil {
		agent.eng.publishRun(root)
	}
	xbin.WriteJSON(w, 200, map[string]any{"revoked": was})
}
