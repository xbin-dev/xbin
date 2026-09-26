// conversations.go — the conversation list (D83): a person's chats, newest
// activity first, with their own pins, archive and read state; search across
// everything they may see; and what is waiting for them.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// chatOrigins are the runs that are conversations; automations' runs live
// on the Automations page.
const chatOrigins = `r.origin IN ('', 'chat', 'api')`

// userState is one person's view of one conversation.
type userState struct {
	PinnedAt   int64 `json:"pinnedAt"`
	ArchivedAt int64 `json:"archivedAt"`
	ReadMs     int64 `json:"readMs"`
}

func (d *DB) userStates(user string, roots []int64) map[int64]userState {
	out := map[int64]userState{}
	if user == "" || len(roots) == 0 {
		return out
	}
	q := `SELECT run_id, pinned_at, archived_at, read_ms FROM run_user_state WHERE user=? AND run_id IN (` +
		strings.TrimSuffix(strings.Repeat("?,", len(roots)), ",") + `)`
	args := []any{user}
	for _, id := range roots {
		args = append(args, id)
	}
	rows, err := d.q.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var s userState
		if rows.Scan(&id, &s.PinnedAt, &s.ArchivedAt, &s.ReadMs) == nil {
			out[id] = s
		}
	}
	return out
}

// setUserState applies a change to one person's state of a conversation.
func (d *DB) setUserState(root int64, user string, f func(s *userState)) userState {
	var s userState
	_ = d.q.QueryRow(`SELECT pinned_at, archived_at, read_ms FROM run_user_state WHERE run_id=? AND user=?`, root, user).
		Scan(&s.PinnedAt, &s.ArchivedAt, &s.ReadMs)
	f(&s)
	_, _ = d.q.Exec(`INSERT INTO run_user_state (run_id, user, pinned_at, archived_at, read_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(run_id, user) DO UPDATE SET pinned_at=excluded.pinned_at, archived_at=excluded.archived_at, read_ms=excluded.read_ms`,
		root, user, s.PinnedAt, s.ArchivedAt, s.ReadMs)
	return s
}

// markRead: the person has seen everything up to now.
func (ag *Agent) markRead(root int64, user string) {
	if user == "" {
		return
	}
	s := ag.db.setUserState(root, user, func(s *userState) {
		if ms := time.Now().UnixMilli(); ms > s.ReadMs {
			s.ReadMs = ms
		}
	})
	ag.emitUserState(root, user, s)
}

func (ag *Agent) emitUserState(root int64, user string, s userState) {
	if ag.eng == nil {
		return
	}
	ag.eng.hub.publishTo(func(sub *subscriber) bool { return sub.w.kind == whoUser && sub.w.user == user },
		&Event{Type: evUState, Run: root, Root: root, Data: map[string]any{"id": root, "pinnedAt": s.PinnedAt,
			"archivedAt": s.ArchivedAt, "readMs": s.ReadMs}})
}

func (ag *Agent) convEpoch() int64 {
	n, _ := strconv.ParseInt(ag.db.getSetting("conv_epoch_ms"), 10, 64)
	return n
}

// convItem is a list row: the run's summary plus what it is to this caller.
func (ag *Agent) convItem(r *Run, w who, st userState) map[string]any {
	it := runSummary(r)
	lv := lvNone
	if a, err := ag.aclOf(r.ID); err == nil {
		lv = a.level(w)
		it["mine"] = a.mine(w)
		it["members"] = len(a.members)
	}
	it["access"] = lv.String()
	it["pinnedAt"], it["archivedAt"], it["readMs"] = st.PinnedAt, st.ArchivedAt, st.ReadMs
	it["unread"] = w.kind == whoUser && r.ActivityMs > max(st.ReadMs, ag.convEpoch())
	return it
}

// handleConversations lists the caller's conversations, newest activity
// first, paged by a (activity, id) cursor so rows inserted meanwhile never
// shift a page. The first page also returns the pinned ones separately.
//
//	GET /conversations?limit=30&cursor=&q=&archived=0|1&scope=mine|team
func handleConversations(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	qs := r.URL.Query()
	if q := strings.TrimSpace(qs.Get("q")); q != "" {
		searchConversations(w, c, q)
		return
	}
	limit, _ := strconv.Atoi(qs.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	where, args := aclWhere(c)
	origins := chatOrigins
	if c.kind == whoUser {
		// a chat channel's conversation of a linked person is theirs (D86)
		origins = "(" + chatOrigins + " OR (r.origin='channel' AND r.owner=?))"
		args = append([]any{c.user}, args...)
	}
	cond := []string{"r.parent_id=0", origins, where}
	// my own list: what I own or joined, and the legacy runs everyone always
	// had — team conversations of others are the "shared with team" view
	stJoin := ""
	if c.kind == whoUser {
		stJoin = ` LEFT JOIN run_user_state us ON us.run_id=r.id AND us.user=?`
		args = append([]any{c.user}, args...)
		switch qs.Get("scope") {
		case "team":
			cond = append(cond, "r.owner<>'' AND r.owner<>? AND r.visibility='team'")
			args = append(args, c.user)
		default:
			cond = append(cond, "(r.owner=? OR r.owner='' OR EXISTS (SELECT 1 FROM run_members m WHERE m.run_id=r.id AND m.user=?) OR us.run_id IS NOT NULL)")
			args = append(args, c.user, c.user)
		}
		if qs.Get("archived") == "1" {
			cond = append(cond, "COALESCE(us.archived_at,0)<>0")
		} else {
			cond = append(cond, "COALESCE(us.archived_at,0)=0")
		}
	}
	first := qs.Get("cursor") == ""
	var pinned []*Run
	if first && c.kind == whoUser && qs.Get("archived") != "1" {
		pinned, _ = agent.db.queryRuns(`r`+stJoin+` WHERE `+strings.Join(cond, " AND ")+` AND COALESCE(us.pinned_at,0)<>0
			ORDER BY us.pinned_at DESC`, args...)
		cond = append(cond, "COALESCE(us.pinned_at,0)=0")
	} else if c.kind == whoUser {
		cond = append(cond, "COALESCE(us.pinned_at,0)=0")
	}
	if cur := qs.Get("cursor"); cur != "" {
		ms, id, ok := parseConvCursor(cur)
		if !ok {
			xbin.WriteError(w, 400, "bad cursor")
			return
		}
		cond = append(cond, "(r.activity_ms<? OR (r.activity_ms=? AND r.id<?))")
		args = append(args, ms, ms, id)
	}
	runs, err := agent.db.queryRuns(`r`+stJoin+` WHERE `+strings.Join(cond, " AND ")+
		` ORDER BY r.activity_ms DESC, r.id DESC LIMIT `+strconv.Itoa(limit+1), args...)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	next := ""
	if len(runs) > limit {
		runs = runs[:limit]
		last := runs[len(runs)-1]
		next = fmt.Sprintf("%d.%d", last.ActivityMs, last.ID)
	}
	var ids []int64
	for _, x := range append(append([]*Run{}, pinned...), runs...) {
		ids = append(ids, x.ID)
	}
	states := agent.db.userStates(c.user, ids)
	conv := func(list []*Run) []map[string]any {
		out := []map[string]any{}
		for _, x := range list {
			out = append(out, agent.convItem(x, c, states[x.ID]))
		}
		return out
	}
	xbin.WriteJSON(w, 200, map[string]any{"pinned": conv(pinned), "items": conv(runs), "next": next})
}

func parseConvCursor(c string) (ms, id int64, ok bool) {
	a, b, found := strings.Cut(c, ".")
	if !found {
		return 0, 0, false
	}
	ms, e1 := strconv.ParseInt(a, 10, 64)
	id, e2 := strconv.ParseInt(b, 10, 64)
	return ms, id, e1 == nil && e2 == nil
}

// searchConversations finds conversations by title or by anything said in
// them (every origin, archived included), among those the caller may see.
func searchConversations(w http.ResponseWriter, c who, q string) {
	type hit struct {
		root    int64
		snippet string
		msgID   int64
	}
	var hits []hit
	seen := map[int64]bool{}
	add := func(root int64, snip string, msgID int64) {
		if seen[root] || len(hits) >= 50 {
			return
		}
		a, err := agent.aclOf(root)
		if err != nil || a.level(c) < lvViewer {
			return
		}
		seen[root] = true
		hits = append(hits, hit{root, snip, msgID})
	}
	if rows, err := agent.db.q.Query(`SELECT id FROM runs WHERE parent_id=0 AND origin<>'held' AND title LIKE ? ORDER BY activity_ms DESC LIMIT 200`,
		"%"+strings.ReplaceAll(q, "%", "")+"%"); err == nil {
		var ids []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			add(id, "", 0)
		}
	}
	if rows, err := agent.db.q.Query(`SELECT CASE WHEN r.root_id<>0 THEN r.root_id ELSE r.id END, f.msg_id,
			snippet(messages_fts, 0, '«', '»', '…', 12)
		FROM messages_fts f JOIN runs r ON r.id=f.run_id JOIN messages m ON m.id=f.msg_id
		WHERE messages_fts MATCH ? AND m.role IN ('user','assistant') ORDER BY f.msg_id DESC LIMIT 500`, ftsQuery(q)); err == nil {
		type row struct {
			root, msg int64
			snip      string
		}
		var got []row
		for rows.Next() {
			var x row
			if rows.Scan(&x.root, &x.msg, &x.snip) == nil {
				got = append(got, x)
			}
		}
		rows.Close()
		for _, x := range got {
			add(x.root, x.snip, x.msg)
		}
	}
	var ids []int64
	for _, h := range hits {
		ids = append(ids, h.root)
	}
	states := agent.db.userStates(c.user, ids)
	items := []map[string]any{}
	for _, h := range hits {
		run, err := agent.db.getRun(h.root)
		if err != nil {
			continue
		}
		it := agent.convItem(run, c, states[h.root])
		if h.snippet != "" {
			it["match"] = map[string]any{"msgId": h.msgID, "snippet": h.snippet}
		}
		items = append(items, it)
	}
	xbin.WriteJSON(w, 200, map[string]any{"pinned": []any{}, "items": items, "next": ""})
}

// handlePatchRun changes a conversation: your own pin and archive (any
// viewer), or its title and who may see it (its owner).
//
//	PATCH /runs/{id} {title?, pinned?, archived?, visibility?, teamRole?}
func handlePatchRun(w http.ResponseWriter, r *http.Request) {
	c, lv := callerOf(r), levelOf(r)
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	root := rootOf(run)
	var body struct {
		Title      *string `json:"title"`
		Pinned     *bool   `json:"pinned"`
		Archived   *bool   `json:"archived"`
		Visibility *string `json:"visibility"`
		TeamRole   *string `json:"teamRole"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		xbin.WriteError(w, 400, "bad body")
		return
	}
	if (body.Title != nil || body.Visibility != nil || body.TeamRole != nil) && lv < lvOwner {
		xbin.WriteError(w, 403, "only the conversation's owner can rename or share it")
		return
	}
	if body.Pinned != nil || body.Archived != nil {
		if c.kind != whoUser {
			xbin.WriteError(w, 400, "pin and archive are a person's own")
			return
		}
		now := time.Now().UnixMilli()
		s := agent.db.setUserState(root, c.user, func(s *userState) {
			if body.Pinned != nil {
				s.PinnedAt = map[bool]int64{true: now, false: 0}[*body.Pinned]
			}
			if body.Archived != nil {
				s.ArchivedAt = map[bool]int64{true: now, false: 0}[*body.Archived]
			}
		})
		agent.emitUserState(root, c.user, s)
	}
	changedACL := false
	err = agent.db.Tx(func(t *DB) error {
		if body.Title != nil {
			title := strings.TrimSpace(*body.Title)
			if title == "" {
				return errBadRequest("the title can't be empty")
			}
			if _, err := t.q.Exec(`UPDATE runs SET title=?, title_src='user' WHERE id=?`, clip(title, 120), root); err != nil {
				return err
			}
		}
		if body.Visibility != nil || body.TeamRole != nil {
			vis, role := run.Visibility, run.TeamRole
			if cur, err := t.getRun(root); err == nil {
				vis, role = cur.Visibility, cur.TeamRole
			}
			if body.Visibility != nil {
				vis = *body.Visibility
			}
			if body.TeamRole != nil {
				role = *body.TeamRole
			}
			if (vis != visPrivate && vis != visTeam) || (role != roleViewer && role != roleParticipant) {
				return errBadRequest("visibility is private|team, teamRole viewer|participant")
			}
			// An unowned (legacy) run made private becomes the claimer's.
			if _, err := t.q.Exec(`UPDATE runs SET owner=CASE WHEN owner='' AND ?<>'' THEN ? ELSE owner END,
				visibility=?, team_role=? WHERE root_id=? OR id=?`, c.tag(), c.tag(), vis, role, root, root); err != nil {
				return err
			}
			changedACL = true
		}
		if agent.eng != nil {
			agent.eng.emitRun(t, root)
		}
		return nil
	})
	if err != nil {
		writeTxErr(w, err)
		return
	}
	if changedACL {
		agent.aclChanged(root)
	}
	run, _ = agent.db.getRun(root)
	st := agent.db.userStates(c.user, []int64{root})[root]
	xbin.WriteJSON(w, 200, agent.convItem(run, c, st))
}

// aclChanged re-reads a conversation's ACL and applies it to live streams.
func (ag *Agent) aclChanged(root int64) {
	ag.acl.flush(root)
	if a, err := ag.aclOf(root); err == nil && ag.eng != nil {
		ag.eng.hub.revalidate(root, a)
	}
}

// handleRead marks a conversation read up to now for the caller.
func handleRead(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	agent.markRead(rootOf(run), c.user)
	xbin.WriteJSON(w, 200, map[string]any{"readMs": time.Now().UnixMilli()})
}

// handleNeeds lists what is waiting for the caller: conversations they may
// talk to where the agent (or a subagent) asks a question or an approval,
// and automations they own whose last run failed and that they haven't
// looked at.
func handleNeeds(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	where, args := aclWhere(c)
	runs, err := agent.db.queryRuns(`r WHERE r.parent_id=0 AND `+where+` AND (
		EXISTS (SELECT 1 FROM runs x WHERE x.root_id=r.id AND x.status='waiting_input')
		OR (r.origin NOT IN ('', 'chat', 'api') AND r.status='error'))
		ORDER BY r.activity_ms DESC LIMIT 50`, args...)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	var ids []int64
	for _, x := range runs {
		ids = append(ids, x.ID)
	}
	states := agent.db.userStates(c.user, ids)
	items := []map[string]any{}
	for _, x := range runs {
		it := agent.convItem(x, c, states[x.ID])
		lv := lvNone
		if a, err := agent.aclOf(x.ID); err == nil {
			lv = a.level(c)
		}
		var subID int64
		var pend string
		waiting := agent.db.q.QueryRow(`SELECT id, pending FROM runs WHERE root_id=? AND status='waiting_input' ORDER BY id LIMIT 1`, x.ID).
			Scan(&subID, &pend) == nil
		switch {
		case waiting && lv >= lvParticipant: // only those who may answer are needed
			reason := "question"
			if parsePending(pend).Kind == "approval" {
				reason = "approval"
			}
			items = append(items, map[string]any{"run": it, "reason": reason, "subRun": subID})
		case !waiting && lv >= lvOwner && it["unread"] == true:
			items = append(items, map[string]any{"run": it, "reason": "failed", "subRun": 0})
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items})
}

type badRequest string

func (b badRequest) Error() string { return string(b) }

func errBadRequest(msg string) error { return badRequest(msg) }

func writeTxErr(w http.ResponseWriter, err error) {
	if b, ok := err.(badRequest); ok {
		xbin.WriteError(w, 400, string(b))
		return
	}
	if err == sql.ErrNoRows {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	xbin.WriteError(w, 500, err.Error())
}
