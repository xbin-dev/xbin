// grants.go — what a conversation's owner let its agent do beyond its own
// reach, for a while (D111). One capability so far: capThreads, reading the
// owner's other conversations and automations with the thread tools
// (threads_tools.go).
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
	capThreads = "threads"
	// grantFor is how long "allow here for 1 hour" lasts.
	grantFor = time.Hour
)

// grantCaps are the capabilities a grant can name (the approve and revoke
// routes refuse anything else).
var grantCaps = map[string]bool{capThreads: true}

// grantView is a live grant as the conversation's view shows it.
type grantView struct {
	Cap       string `json:"cap"`
	GrantedBy string `json:"grantedBy"`
	ExpiresMs int64  `json:"expiresMs"`
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
