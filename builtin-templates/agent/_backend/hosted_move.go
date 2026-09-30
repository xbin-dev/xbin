// hosted_move.go — moving a shared conversation into team for its host and
// back out of it (hosted.go; API.md "Non-secure conversations"), so that a
// stop anywhere, a page closed mid-move or two people at once never strand
// it nor leave two copies:
//
//  1. the person's partition writes its intent (hosting_moves: the
//     conversation and the audience the person was shown) and asks the
//     global instance with a context of its own — a page closed while it
//     moves cancels nothing;
//  2. the global instance copies it into team with team_hosts "pending",
//     then deletes the original. It is idempotent by (moved_from, host): a
//     retry — or the partition's next start finding its intent (POST
//     /hosted {conversation, lookup: true}) — gets the copy there is, and
//     finishes deleting the original if a stop came between the two;
//  3. the partition records it in its own hosted table, acks in team
//     ("pending" → active or paused, only if it is still its and not being
//     continued) and drops its intent;
//  4. a copy nobody acked for hostPendingTTL shows as dropped ("unclaimed"):
//     any participant may continue it — and one whose original is still at
//     the global instance (it stopped between 2's steps) is deleted there
//     instead: the original stays.
//
// Continuing without the host — and un-sharing a hosted conversation, which
// ends hosting (90 §I10: no private conversation stays in the shared space)
// — claims it in team first ("continuing", one at a time; a claim a stopped
// process left is taken again after continueClaimTTL), imports it at the
// global instance, and leaves a tombstone ("continued", its new id) that
// answers a retry, before the team copy is deleted.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// More hosting states (team_hosts.state; team_runs.go has the others).
const (
	hostPending    = "pending"    // moved into team; its host's partition hasn't taken it up yet
	hostContinuing = "continuing" // being continued without its host (claimed)
	hostContinued  = "continued"  // continued without its host: a tombstone naming its new id
)

var (
	// hostPendingTTL: a copy its host's partition never took up is continuable after this.
	hostPendingTTL = 10 * time.Minute
	// continueClaimTTL: a continue claim older than this was left by a stopped process.
	continueClaimTTL = 2 * time.Minute
	// continuedKept: how long a tombstone answers where a continued conversation went.
	continuedKept = 30 * 24 * time.Hour
	// hostMoveTimeout bounds a move (and a lookup) at the global instance.
	hostMoveTimeout = 2 * time.Minute
)

var (
	errHostingLost = errors.New("it was continued without you, or someone else hosts it, meanwhile")
	errContinuing  = errors.New("someone is continuing it without its host right now — try again in a moment")
)

// --- a person's partition ----------------------------------------------------------

const hostingMovesSQL = `CREATE TABLE IF NOT EXISTS hosting_moves (
	from_id INTEGER PRIMARY KEY,
	seen TEXT NOT NULL DEFAULT '',
	created INTEGER NOT NULL DEFAULT 0
)`

// hostingMovesWait: a move this partition asked for isn't settled yet.
func (d *DB) hostingMovesWait() bool {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM hosting_moves`).Scan(&n)
	return n > 0
}

func dropHostingMove(from int64) {
	_, _ = agent.db.q.Exec(`DELETE FROM hosting_moves WHERE from_id=?`, from)
}

// movedReply is POST /hosted's answer.
type movedReply struct {
	Conversation int64    `json:"conversation"`
	Audience     audience `json:"audience"`
}

func parseMoved(b []byte) (movedReply, bool) {
	var m movedReply
	if json.Unmarshal(b, &m) != nil || !hostedID(m.Conversation) {
		return m, false
	}
	if m.Audience.Members == nil {
		m.Audience.Members = map[string]string{}
	}
	return m, true
}

// handleHostingStart: POST /hosting {conversation, seen} — the person lets a
// shared conversation use their resources (after the warning: seen is the
// audience the page showed them). The global instance moves it into team;
// this partition records it and drives it. A wider audience than seen (it
// changed meanwhile) is recorded paused: they are asked again.
func handleHostingStart(w http.ResponseWriter, r *http.Request) {
	if !hostOnly(w, r) {
		return
	}
	var body struct {
		Conversation int64     `json:"conversation"`
		Seen         *audience `json:"seen"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Conversation <= 0 || body.Seen == nil {
		xbin.WriteError(w, 400, "need {conversation, seen: the audience you were shown}")
		return
	}
	if body.Conversation >= partitionIDBase {
		xbin.WriteError(w, 409, "a conversation in your own space is yours alone — hosting is for a shared one (share a copy of this one first)")
		return
	}
	seen := *body.Seen
	if seen.Members == nil {
		seen.Members = map[string]string{}
	}
	if _, err := agent.db.q.Exec(`INSERT INTO hosting_moves (from_id, seen, created) VALUES (?, ?, ?)
		ON CONFLICT(from_id) DO UPDATE SET seen=excluded.seen, created=excluded.created`, body.Conversation, seen.key(), now()); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostMoveTimeout) // not the page's: closing it cancels nothing
	defer cancel()
	req, _ := json.Marshal(map[string]any{"conversation": body.Conversation, "resources": hostResources})
	res, err := callGlobal(ctx, http.MethodPost, "/hosted", req, "application/json")
	if err != nil || res.Status >= 500 {
		msg := "the shared instance didn't answer"
		if err != nil {
			msg += ": " + err.Error()
		}
		xbin.WriteError(w, 502, msg+" — try again (if it moved meanwhile, your partition takes it up at its next start)")
		return
	}
	if res.Status != 200 {
		dropHostingMove(body.Conversation) // refused: nothing moved
		w.Header().Set("Content-Type", orStr(res.Type, "application/json"))
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		return
	}
	moved, ok := parseMoved(res.Body)
	if !ok {
		xbin.WriteError(w, 502, "the shared instance's answer: "+clip(string(res.Body), 200))
		return
	}
	row, err := adoptHosted(body.Conversation, moved.Conversation, moved.Audience, seen)
	switch {
	case errors.Is(err, errHostingLost):
		xbin.WriteError(w, 409, err.Error())
	case err != nil:
		xbin.WriteError(w, 500, err.Error())
	default:
		xbin.WriteJSON(w, 200, row)
	}
}

// adoptHosted records conversation id (moved from from) as this
// partition's, acks it in team and starts driving it — paused at once when
// its audience is wider than seen. errHostingLost: it isn't this person's to
// take up any more (continued without them, or another host's).
func adoptHosted(from, id int64, moved, seen audience) (*hostedRow, error) {
	tr := teamRuns()
	if tr == nil {
		return nil, errors.New("the shared space is being upgraded — try again in a minute")
	}
	snap, state, pending, reason, teamPend := moved, hostActive, "", "", ""
	if len(moved.beyond(seen)) > 0 { // it widened after the warning: ask again
		snap, state, pending, reason = seen, hostPaused, moved.key(), "confirm"
		b, _ := json.Marshal(teamPending{Audience: moved, New: moved.beyond(seen)})
		teamPend = string(b)
	}
	resJSON, _ := json.Marshal(hostResources)
	t := now()
	pausedAt := int64(0)
	if state == hostPaused {
		pausedAt = t
	}
	if _, err := agent.db.q.Exec(`INSERT INTO hosted (conversation, state, snapshot, resources, pending, confirmed_at, paused_at, created)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(conversation) DO UPDATE SET state=excluded.state, snapshot=excluded.snapshot,
		resources=excluded.resources, pending=excluded.pending, confirmed_at=excluded.confirmed_at, paused_at=excluded.paused_at`,
		id, state, snap.key(), string(resJSON), pending, t, pausedAt, t); err != nil {
		return nil, err
	}
	if !tr.ackTeamHost(id, "user:"+runUser, state, reason, teamPend) {
		_, _ = agent.db.q.Exec(`UPDATE hosted SET state='dropped', pending='' WHERE conversation=?`, id)
		dropHostingMove(from)
		return nil, errHostingLost
	}
	dropHostingMove(from)
	if state == hostPaused {
		time.AfterFunc(hostPauseTTL+time.Second, expireHostPauses)
	}
	hostedChangedAtGlobal(id)
	if e := ensureHostEngine(); e != nil {
		e.recover() // inputs that waited in team (a re-adopted conversation)
	}
	return agent.db.hostedRow(id)
}

// ackTeamHost is the host's partition taking up a conversation in team: its
// state there becomes what the partition recorded — only while team still
// names this host and nobody continues it without them.
func (d *DB) ackTeamHost(root int64, host, state, reason, pending string) bool {
	res, err := d.q.Exec(`UPDATE team_hosts SET state=?, reason=?, pending=?, since=? WHERE run_id=? AND host=? AND state IN ('pending','active','paused')`,
		state, reason, pending, now(), root, host)
	return err == nil && rowsAffected(res) == 1
}

// reconcileHostingMoves (a person's partition's start): a move asked for
// before a stop — the global instance may have made it — is looked up
// there (never made now): taken up if it moved, forgotten if it didn't.
func reconcileHostingMoves() {
	type intent struct {
		from    int64
		seen    string
		created int64
	}
	var list []intent
	rows, err := agent.db.q.Query(`SELECT from_id, seen, created FROM hosting_moves`)
	if err != nil {
		return
	}
	for rows.Next() {
		var it intent
		if rows.Scan(&it.from, &it.seen, &it.created) == nil {
			list = append(list, it)
		}
	}
	rows.Close()
	for _, it := range list {
		if time.Since(time.Unix(it.created, 0)) > hostPauseTTL {
			dropHostingMove(it.from)
			continue
		}
		seen, _ := parseAudience(it.seen)
		ctx, cancel := context.WithTimeout(context.Background(), hostMoveTimeout)
		req, _ := json.Marshal(map[string]any{"conversation": it.from, "lookup": true})
		res, err := callGlobal(ctx, http.MethodPost, "/hosted", req, "application/json")
		cancel()
		switch {
		case err != nil || res.Status >= 500:
			logf("hosting #%d: the shared instance didn't answer (%v) — looked at again at the next start", it.from, err)
		case res.Status != 200:
			dropHostingMove(it.from) // it never moved (or isn't this person's to take up)
		default:
			moved, ok := parseMoved(res.Body)
			if !ok {
				continue
			}
			if _, err := adoptHosted(it.from, moved.Conversation, moved.Audience, seen); err != nil {
				logf("hosting #%d → #%d: %v", it.from, moved.Conversation, err)
			}
		}
	}
}

// --- the global instance: moving in ---------------------------------------------------

// handleHostedMove: POST /hosted {conversation, lookup?} from a person's
// partition (handleHostingStart): the person — a participant — hosts it.
// Idempotent for its host; lookup only answers a copy there already is.
func handleHostedMove(w http.ResponseWriter, r *http.Request) {
	if !globalMode() {
		xbin.WriteError(w, 404, "hosting is arranged at the agent's shared instance")
		return
	}
	if !personFromPartition(r) {
		xbin.WriteError(w, 403, "a person hosts a conversation from their own partition (POST /hosting there)")
		return
	}
	if teamUnavailable(w) {
		return
	}
	tv := teamView()
	c := callerOf(r)
	var body struct {
		Conversation int64 `json:"conversation"`
		Lookup       bool  `json:"lookup"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := body.Conversation
	settleHostedMoves(tv)
	if hostedID(id) {
		readoptHosted(w, tv, c, id, body.Lookup)
		return
	}
	if h, err := tv.db.teamHostMovedFrom(id); err == nil { // a copy of it there is: a retry, or its partition's start
		switch {
		case h.Host != "user:"+c.user:
			xbin.WriteError(w, 409, hostOf(h)+" is moving it or hosts it: one host per conversation")
			return
		case h.State == hostContinuing || h.State == hostContinued:
			xbin.WriteError(w, 409, errHostingLost.Error())
			return
		}
		if _, err := agent.db.getRun(id); err == nil {
			dropConversation(agent, id, "hosted: it moved to the non-secure space") // a stop came between the two steps
		}
		acl, err := tv.db.loadACL(h.RunID)
		if err != nil {
			xbin.WriteError(w, 404, "no such conversation")
			return
		}
		xbin.WriteJSON(w, 200, map[string]any{"conversation": h.RunID, "from": id, "audience": audienceOf(acl), "state": h.State})
		return
	}
	if body.Lookup {
		xbin.WriteError(w, 404, "it never moved")
		return
	}
	run, lv, err := agent.runAccess(c, id)
	switch {
	case err != nil || lv == lvNone:
		xbin.WriteError(w, 404, "no such conversation")
		return
	case run.ParentID != 0:
		xbin.WriteError(w, 400, "host the conversation (its first run), not a subagent")
		return
	case lv < lvParticipant:
		xbin.WriteError(w, 403, "only someone who may talk in it can let it use their resources")
		return
	case automationOrigins[run.Origin]:
		xbin.WriteError(w, 409, "an automation's conversation can't be hosted")
		return
	case !resting(run.Status) || len(agent.db.undelivered(id)) > 0:
		xbin.WriteError(w, 409, "the agent is working in it: wait until it finishes (or stop it), then try again")
		return
	case movingRefused(w, id): // homes_move.go: on its way to its owner's own space (90 §I10) — not taken into team
		return
	}
	if err := agent.db.harnessStays(id); err != nil { // a coding agent's conversation isn't hosted (harness_partition.go)
		writeHarnessMoveErr(w, err)
		return
	}
	moved, left, err := moveIntoTeam(r.Context(), tv, run, c)
	if err != nil {
		writeImportErr(w, err)
		return
	}
	acl, _ := tv.db.loadACL(moved)
	xbin.WriteJSON(w, 200, map[string]any{"conversation": moved, "from": id, "audience": audienceOf(acl), "left": left, "state": hostPending})
}

// readoptHosted: POST /hosted on a hosted id — its host taking up a
// conversation it hosts already (answered as it is) or hosted before (a
// dropped one becomes pending again, for its partition to ack). lookup
// changes nothing.
func readoptHosted(w http.ResponseWriter, tv *Agent, c who, id int64, lookup bool) {
	h, err := tv.db.teamHost(id)
	_, lv, lerr := hostedLevel(tv.db, c, id)
	switch {
	case err != nil || lerr != nil || lv == lvNone:
		xbin.WriteError(w, 404, "no such conversation")
		return
	case h.Host != "user:"+c.user:
		xbin.WriteError(w, 409, hostOf(h)+" hosts it: one host per conversation")
		return
	case h.State == hostDropped && lookup:
		xbin.WriteError(w, 404, "you don't host it")
		return
	case h.State == hostDropped:
		if lv < lvParticipant {
			xbin.WriteError(w, 403, "only someone who may talk in it can let it use their resources")
			return
		}
		res, err := tv.db.q.Exec(`UPDATE team_hosts SET state='pending', reason='', pending='', since=? WHERE run_id=? AND state='dropped'`, now(), id)
		if err != nil || rowsAffected(res) != 1 {
			xbin.WriteError(w, 409, "it changed meanwhile — look again")
			return
		}
		publishHosted(tv, id)
	case h.State != hostPending && h.State != hostActive && h.State != hostPaused:
		xbin.WriteError(w, 409, "hosting it ended: continue it without the host first")
		return
	}
	acl, _ := tv.db.loadACL(id)
	xbin.WriteJSON(w, 200, map[string]any{"conversation": id, "audience": audienceOf(acl)})
}

// moveIntoTeam copies conversation run into team — its transcript, task
// ledger, text session files, audience and the members' pins — pending for
// c, then deletes it here (its share links with it). Binary session files
// stay behind (they live in this instance's blob store): named in left.
func moveIntoTeam(ctx context.Context, tv *Agent, run *Run, c who) (int64, []string, error) {
	b, err := agent.exportConv(ctx, run.ID, true)
	if err != nil {
		return 0, nil, err
	}
	var left []string
	files := b.Files[:0]
	for _, f := range b.Files {
		if f.Binary {
			left = append(left, f.Path)
			continue
		}
		files = append(files, f)
	}
	b.Files, left = files, append(left, b.Left...)
	acl, err := agent.db.loadACL(run.ID)
	if err != nil {
		return 0, nil, err
	}
	cls, err := importClass(c, b)
	if err != nil {
		return 0, nil, err
	}
	note := fmt.Sprintf("%s hosts this conversation now: the agent may use %s's private resources in it "+
		"(their sandboxes and anything signed in inside them, though never one where a coding agent of theirs signed in or worked; "+
		"their data in other tiles; their vault). It is not private: its members, the agent's managers, "+
		"workspace admins and anyone who can change the agent's code can read it.", c.user, c.user)
	if len(left) > 0 {
		note += " Left behind: " + strings.Join(left, ", ") + "."
	}
	moved, err := tv.importConv(ctx, b, c, runStamp{Owner: acl.owner, Visibility: acl.visibility, TeamRole: acl.teamRole}, cls, specOf(acl), note, false)
	if err != nil {
		return 0, nil, err
	}
	// its own settings as its viewers may see them (no MCP headers: team is readable by every partition's code)
	var raw string
	_ = agent.db.q.QueryRow(`SELECT config FROM runs WHERE id=?`, run.ID).Scan(&raw)
	if clean, _ := confConfig(raw); clean != "" {
		_, _ = tv.db.q.Exec(`UPDATE runs SET config=? WHERE id=?`, clean, moved.ID)
		_, _ = tv.db.q.Exec(`UPDATE messages SET content=? WHERE run_id=? AND role='system'`, parseConfig(clean).System, moved.ID)
	}
	for _, s := range agent.db.userStatesOf(run.ID) {
		_, _ = tv.db.q.Exec(`INSERT OR REPLACE INTO run_user_state (run_id, user, pinned_at, archived_at, read_ms) VALUES (?, ?, ?, ?, ?)`,
			moved.ID, s.user, s.PinnedAt, s.ArchivedAt, s.ReadMs)
	}
	res, _ := json.Marshal(hostResources)
	if _, err := tv.db.q.Exec(`INSERT INTO team_hosts (run_id, host, state, resources, moved_from, since, created) VALUES (?, ?, 'pending', ?, ?, ?, ?)`,
		moved.ID, "user:"+c.user, string(res), run.ID, now(), now()); err != nil {
		_ = tv.deleteRunTree(moved.ID)
		return 0, nil, err
	}
	dropConversation(agent, run.ID, "hosted: it moved to the non-secure space")
	tv.aclChanged(moved.ID)
	tv.eng.publishRun(moved.ID)
	return moved.ID, left, nil
}

// specOf is a conversation's members as a share spec (an import's audience).
func specOf(acl *rootACL) *shareSpec {
	spec := &shareSpec{}
	for u, role := range acl.members {
		spec.Members = append(spec.Members, shareMember{User: u, Role: role})
	}
	return spec
}

// teamHostMovedFrom is the team copy made of conversation from (at global).
func (d *DB) teamHostMovedFrom(from int64) (*teamHost, error) {
	var id int64
	if err := d.q.QueryRow(`SELECT run_id FROM team_hosts WHERE moved_from=? ORDER BY created DESC LIMIT 1`, from).Scan(&id); err != nil {
		return nil, err
	}
	return d.teamHost(id)
}

// settleHostedMoves (the global instance): a pending copy older than
// hostPendingTTL whose original is still here never finished moving — the
// original stays, the copy goes; tombstones past continuedKept go.
func settleHostedMoves(tv *Agent) {
	if tv == nil {
		return
	}
	cut := time.Now().Add(-hostPendingTTL).Unix()
	rows, err := tv.db.q.Query(`SELECT run_id, moved_from FROM team_hosts WHERE state='pending' AND since<? AND moved_from>0`, cut)
	if err != nil {
		return
	}
	var stale [][2]int64
	for rows.Next() {
		var id, from int64
		if rows.Scan(&id, &from) == nil {
			stale = append(stale, [2]int64{id, from})
		}
	}
	rows.Close()
	for _, s := range stale {
		if _, err := agent.db.getRun(s[1]); err != nil {
			continue // the original is gone: the copy is the conversation (continuable, "unclaimed")
		}
		res, err := tv.db.q.Exec(`DELETE FROM team_hosts WHERE run_id=? AND state='pending'`, s[0])
		if err != nil || rowsAffected(res) != 1 {
			continue
		}
		logf("hosted conversation #%d: its move from #%d never finished — the original stays", s[0], s[1])
		dropConversation(tv, s[0], "its move never finished")
	}
	_, _ = tv.db.q.Exec(`DELETE FROM team_hosts WHERE state='continued' AND since<?`, time.Now().Add(-continuedKept).Unix())
}

// --- the global instance: continuing without the host ----------------------------------

// handleHostedContinue: POST /hosted/{id}/continue — a participant takes a
// conversation whose hosting ended (declined, taken back, 7 days unanswered,
// its host gone, never taken up) back into the shared space, without the
// host's resources: a plain shared conversation again (a new id), the team
// copy deleted. A retry answers the id it got.
func handleHostedContinue(w http.ResponseWriter, r *http.Request) {
	if !globalMode() {
		xbin.WriteError(w, 404, "hosted conversations are the agent's shared instance's")
		return
	}
	if teamUnavailable(w) {
		return
	}
	tv := teamView()
	c := callerOf(r)
	id := pathID(r)
	settleHostedMoves(tv)
	if h, err := tv.db.teamHost(id); err == nil && h.State == hostContinued { // a retry, or someone else was first
		if _, lv, err := agent.runAccess(c, h.ContinuedTo); err == nil && lv >= lvViewer {
			xbin.WriteJSON(w, 200, map[string]any{"conversation": h.ContinuedTo, "from": id})
			return
		}
		xbin.WriteError(w, 404, "no such conversation")
		return
	}
	run, lv, err := hostedLevel(tv.db, c, id)
	if err != nil || lv == lvNone {
		xbin.WriteError(w, 404, "no such conversation")
		return
	}
	if lv < lvParticipant {
		xbin.WriteError(w, 403, "only someone who may talk in it can continue it")
		return
	}
	root := rootOf(run)
	h, err := tv.db.teamHost(root)
	if err != nil {
		xbin.WriteError(w, 404, "no such conversation")
		return
	}
	if st := hostedInfo(h)["state"]; st != hostDropped && st != hostGone && st != hostContinuing { // continuing: continueAtGlobal tells a fresh claim from a stale one
		xbin.WriteError(w, 409, fmt.Sprintf("%s still hosts it (%s): it continues without their resources once they stop hosting it", hostOf(h), st))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostMoveTimeout)
	defer cancel()
	note := fmt.Sprintf("Continued by %s without %s's private resources: a shared conversation again.", orStr(c.user, "the agent"), orStr(hostOf(h), "its host"))
	back, err := continueAtGlobal(ctx, tv, root, h, c, note, "continued without its host")
	switch {
	case errors.Is(err, errContinuing):
		xbin.WriteError(w, 409, err.Error())
	case err != nil:
		writeImportErr(w, err)
	default:
		xbin.WriteJSON(w, 200, map[string]any{"conversation": back.ID, "from": root})
	}
}

// continueAtGlobal makes hosted conversation root a plain conversation at
// the global instance again (as it is in team, without its binary files —
// they are the host's) and deletes the team copy, leaving a tombstone. prev
// is team_hosts as the caller read it: the claim is taken from exactly that
// (errContinuing when someone else got there first). The host is rung: its
// partition finds the conversation gone and stops.
func continueAtGlobal(ctx context.Context, tv *Agent, root int64, prev *teamHost, c who, note, why string) (*Run, error) {
	if prev.State == hostContinued || (prev.State == hostContinuing && time.Since(time.Unix(prev.Since, 0)) < continueClaimTTL) {
		return nil, errContinuing
	}
	res, err := tv.db.q.Exec(`UPDATE team_hosts SET state='continuing', reason=?, since=? WHERE run_id=? AND state=? AND reason=? AND since=?`,
		c.user, now(), root, prev.State, prev.Reason, prev.Since)
	if err != nil || rowsAffected(res) != 1 {
		return nil, errContinuing
	}
	undo := func(err error) (*Run, error) {
		_, _ = tv.db.q.Exec(`UPDATE team_hosts SET state=?, reason=?, since=? WHERE run_id=? AND state='continuing'`, prev.State, prev.Reason, prev.Since, root)
		return nil, err
	}
	b, err := tv.exportConv(ctx, root, true)
	if err != nil {
		return undo(err)
	}
	files := b.Files[:0]
	for _, f := range b.Files {
		if !f.Binary { // the host's blob store: not readable here
			files = append(files, f)
		}
	}
	b.Files = files
	acl, err := tv.db.loadACL(root)
	if err != nil {
		return undo(err)
	}
	cls, err := importClass(c, b)
	if err != nil {
		return undo(err)
	}
	back, err := agent.importConv(ctx, b, c, runStamp{Owner: acl.owner, Visibility: acl.visibility, TeamRole: acl.teamRole}, cls, specOf(acl), note, false)
	if err != nil {
		return undo(err)
	}
	_, _ = tv.db.q.Exec(`UPDATE team_hosts SET state='continued', continued_to=?, since=? WHERE run_id=?`, back.ID, now(), root)
	dropConversationTo(tv, root, why, back.ID)
	if prev.State == hostActive || prev.State == hostPaused || prev.State == hostPending {
		ringHost(tv, prev, root, hostedInput{Signal: "audience"}) // its partition finds it gone and stops
	}
	return back, nil
}

// ownerOnly: nobody but its owner can read root any more (un-shared).
func ownerOnly(tv *Agent, root int64) bool {
	acl, err := tv.db.loadACL(root)
	return err == nil && acl.visibility == visPrivate && len(acl.members) == 0
}

// unshareHosted: a hosted conversation that stopped being shared (its last
// member left or was removed, it was made private) stops being hosted — no
// private conversation stays in the shared space (90 §I10): it goes back to
// the global instance as its owner's plain conversation, from where an
// un-shared one moves on to its owner's own partition.
func unshareHosted(tv *Agent, root int64, c who) (*Run, error) {
	h, err := tv.db.teamHost(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostMoveTimeout)
	defer cancel()
	note := fmt.Sprintf("No longer shared: it doesn't use %s's private resources any more, and left the non-secure space.", orStr(hostOf(h), "its host"))
	back, err := continueAtGlobal(ctx, tv, root, h, c, note, "no longer shared")
	if err != nil {
		return nil, err
	}
	// …and on to its owner's own partition, as any un-shared chat there
	// (homes_move.go; it was shared — hosted — until this act). One that
	// isn't a person's chat stays at the global instance, private.
	if err := agent.db.Tx(func(t *DB) error { return t.moveIfUnshared(back.ID, true) }); err != nil {
		logf("hosted conversation #%d: back at the global instance as #%d; its move to its owner's own space wasn't asked: %v", root, back.ID, err)
	}
	return back, nil
}

// unshareIfOwnerOnly: after a change of who shares root — only its owner
// left, it stops being hosted and goes back to the global instance
// (unshareHosted; its new conversation there); otherwise the host is rung
// (its engine pauses it if it is wider than confirmed, stops if the host is
// no longer in it). nil: still shared.
func unshareIfOwnerOnly(tv *Agent, root int64, c who) *Run {
	if !ownerOnly(tv, root) {
		audienceChanged(tv, root)
		return nil
	}
	back, err := unshareHosted(tv, root, c)
	if err != nil {
		logf("hosted conversation #%d: un-shared, but it stays hosted for now: %v", root, err)
		audienceChanged(tv, root)
		return nil
	}
	return back
}

// --- deleting -------------------------------------------------------------------------

// personState is one person's pin/archive/read of a conversation.
type personState struct {
	user string
	userState
}

func (d *DB) userStatesOf(root int64) []personState {
	rows, err := d.q.Query(`SELECT user, pinned_at, archived_at, read_ms FROM run_user_state WHERE run_id=?`, root)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []personState
	for rows.Next() {
		var s personState
		if rows.Scan(&s.user, &s.PinnedAt, &s.ArchivedAt, &s.ReadMs) == nil {
			out = append(out, s)
		}
	}
	return out
}

// dropConversation deletes conversation root from ag's store as DELETE
// /runs/{id} does (its members, links and people's states too), telling the
// list streams.
func dropConversation(ag *Agent, root int64, why string) { dropConversationTo(ag, root, why, 0) }

// dropConversationTo is dropConversation for one that lives on as another
// (movedTo, on its `run` event: a page follows it there).
func dropConversationTo(ag *Agent, root int64, why string, to int64) {
	acl, _ := ag.db.loadACL(root)
	_ = ag.db.Tx(func(t *DB) error {
		ag.cancelRuns(t, root, true, why)
		return nil
	})
	_ = ag.deleteRunTree(root)
	ag.acl.flush(root)
	for _, q := range []string{`DELETE FROM run_members WHERE run_id=?`, `DELETE FROM share_links WHERE run_id=?`,
		`DELETE FROM run_user_state WHERE run_id=?`} {
		_, _ = ag.db.q.Exec(q, root)
	}
	if ag.eng != nil {
		data := map[string]any{"id": root, "deleted": true}
		if to != 0 {
			data["movedTo"] = to
		}
		ag.eng.hub.publish(&Event{Type: evRun, Run: root, Root: root, Data: data, acl: acl})
	}
}
