// share.go — sharing a conversation (D83): its members (people it was shared
// with, as viewers or participants) and join links (a secret that makes
// whoever opens it a member). Only the sha256 of a link's token is stored.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// userIDRe is xbind's user id shape (the login name).
var userIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

func tokenHash(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// shareRoot resolves the route's run to its root (sharing is per
// conversation).
func shareRoot(w http.ResponseWriter, r *http.Request) (*Run, bool) {
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return nil, false
	}
	root, err := agent.db.getRun(rootOf(run))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return nil, false
	}
	return root, true
}

// handleMembers lists who a conversation is shared with; its owner also sees
// the live join links.
func handleMembers(w http.ResponseWriter, r *http.Request) {
	root, ok := shareRoot(w, r)
	if !ok {
		return
	}
	members := []map[string]any{}
	if rows, err := agent.db.q.Query(`SELECT user, role, added_by, via, created FROM run_members WHERE run_id=? ORDER BY created, user`, root.ID); err == nil {
		for rows.Next() {
			var u, role, by, via string
			var created int64
			if rows.Scan(&u, &role, &by, &via, &created) == nil {
				members = append(members, map[string]any{"user": u, "role": role, "addedBy": by, "via": via, "created": created})
			}
		}
		rows.Close()
	}
	out := map[string]any{"owner": root.Owner, "visibility": root.Visibility, "teamRole": root.TeamRole, "members": members}
	if levelOf(r) >= lvOwner {
		links := []map[string]any{}
		if rows, err := agent.db.q.Query(`SELECT id, role, created_by, created, expires, max_uses, uses FROM share_links
			WHERE run_id=? AND revoked=0 ORDER BY id`, root.ID); err == nil {
			for rows.Next() {
				var id, created, expires int64
				var maxUses, uses int
				var role, by string
				if rows.Scan(&id, &role, &by, &created, &expires, &maxUses, &uses) == nil {
					links = append(links, map[string]any{"id": id, "role": role, "createdBy": by, "created": created,
						"expires": expires, "maxUses": maxUses, "uses": uses})
				}
			}
			rows.Close()
		}
		out["links"] = links
	}
	xbin.WriteJSON(w, 200, out)
}

// handleAddMember shares a conversation with a person, by user id.
//
//	POST /runs/{id}/members {user, role: viewer|participant}
func handleAddMember(w http.ResponseWriter, r *http.Request) {
	root, ok := shareRoot(w, r)
	if !ok {
		return
	}
	var body struct{ User, Role string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !userIDRe.MatchString(body.User) {
		xbin.WriteError(w, 400, "need {user: a user id (the login name)}")
		return
	}
	if body.Role == "" {
		body.Role = roleParticipant
	}
	if body.Role != roleViewer && body.Role != roleParticipant {
		xbin.WriteError(w, 400, "role is viewer or participant")
		return
	}
	if body.User == root.Owner {
		xbin.WriteError(w, 400, "that is the owner")
		return
	}
	c := callerOf(r)
	if _, err := agent.db.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, ?, ?, ?, 'invite', ?)
		ON CONFLICT(run_id, user) DO UPDATE SET role=excluded.role`, root.ID, body.User, body.Role, c.tag(), now()); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	agent.membersChanged(root.ID)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleRemoveMember takes someone off a conversation — the owner removes
// anyone; a member may remove themselves (leave).
func handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	root, ok := shareRoot(w, r)
	if !ok {
		return
	}
	user := r.PathValue("user")
	if c := callerOf(r); levelOf(r) < lvOwner && !(c.kind == whoUser && c.user == user) {
		xbin.WriteError(w, 403, "only the owner can remove someone else")
		return
	}
	if _, err := agent.db.q.Exec(`DELETE FROM run_members WHERE run_id=? AND user=?`, root.ID, user); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	_, _ = agent.db.q.Exec(`DELETE FROM run_user_state WHERE run_id=? AND user=?`, root.ID, user)
	agent.membersChanged(root.ID)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

func (ag *Agent) membersChanged(root int64) {
	ag.aclChanged(root)
	if ag.eng != nil {
		ag.eng.publishRun(root)
	}
}

// handleNewLink makes a join link: whoever opens it (and can open the tile)
// joins as role. The token is in this response only.
//
//	POST /runs/{id}/links {role, expiresIn? (seconds), maxUses?}
func handleNewLink(w http.ResponseWriter, r *http.Request) {
	root, ok := shareRoot(w, r)
	if !ok {
		return
	}
	var body struct {
		Role      string `json:"role"`
		ExpiresIn int64  `json:"expiresIn"`
		MaxUses   int    `json:"maxUses"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Role == "" {
		body.Role = roleParticipant
	}
	if body.Role != roleViewer && body.Role != roleParticipant {
		xbin.WriteError(w, 400, "role is viewer or participant")
		return
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	tok := base64.RawURLEncoding.EncodeToString(raw[:])
	expires := int64(0)
	if body.ExpiresIn > 0 {
		expires = now() + body.ExpiresIn
	}
	var id int64
	if err := agent.db.q.QueryRow(`INSERT INTO share_links (run_id, token_hash, role, created_by, created, expires, max_uses)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`, root.ID, tokenHash(tok), body.Role, callerOf(r).tag(), now(), expires, body.MaxUses).Scan(&id); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"id": id, "token": tok, "hash": "#join=" + tok})
}

func handleRevokeLink(w http.ResponseWriter, r *http.Request) {
	root, ok := shareRoot(w, r)
	if !ok {
		return
	}
	lid, _ := strconv.ParseInt(r.PathValue("lid"), 10, 64)
	_, _ = agent.db.q.Exec(`UPDATE share_links SET revoked=1 WHERE id=? AND run_id=?`, lid, root.ID)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleJoin redeems a join link: the caller becomes a member (never
// lowering a role they already have). Every failure looks the same.
//
//	POST /join {token}
func handleJoin(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	var body struct{ Token string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	gone := func() { xbin.WriteError(w, 404, "this link is invalid or has expired") }
	if body.Token == "" {
		gone()
		return
	}
	var root int64
	var role string
	err := agent.db.Tx(func(t *DB) error {
		var id int64
		if err := t.q.QueryRow(`SELECT id, run_id, role FROM share_links WHERE token_hash=? AND revoked=0
			AND (expires=0 OR expires>?) AND (max_uses=0 OR uses<max_uses)`, tokenHash(body.Token), time.Now().Unix()).
			Scan(&id, &root, &role); err != nil {
			return err
		}
		if res, err := t.q.Exec(`UPDATE share_links SET uses=uses+1 WHERE id=? AND (max_uses=0 OR uses<max_uses)`, id); err != nil || rowsAffected(res) != 1 {
			return errBadRequest("used up")
		}
		var owner string
		if err := t.q.QueryRow(`SELECT owner FROM runs WHERE id=?`, root).Scan(&owner); err != nil {
			return err
		}
		if owner == c.user {
			return nil // the owner opening their own link
		}
		_, err := t.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, ?, ?, '', ?, ?)
			ON CONFLICT(run_id, user) DO UPDATE SET role=CASE WHEN run_members.role='participant' THEN 'participant' ELSE excluded.role END`,
			root, c.user, role, "link:"+strconv.FormatInt(id, 10), now())
		return err
	})
	if err != nil {
		gone()
		return
	}
	agent.membersChanged(root)
	title := ""
	if run, err := agent.db.getRun(root); err == nil {
		title = run.Title
	}
	xbin.WriteJSON(w, 200, map[string]any{"runId": root, "role": role, "title": title})
}
