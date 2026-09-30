// hosted_global.go — the global instance's side of non-secure (hosted)
// conversations (hosted.go): moving a shared conversation into team for its
// host and back out of it, fanning the host's live run out to the members,
// and ringing the host when members write (partition mail hosted/input).
//
//	POST /hosted {conversation, resources}  a person's partition hosts one of
//	                                        its shared conversations (it moves
//	                                        into team: a new id from 2^39)
//	POST /hosted/events {events}            the host's live run (batched)
//	POST /hosted/{id}/continue              a member takes a dropped one back
//	                                        (without the host's resources)
//
// The global instance's engine never drives a team row: it only reads team
// (hosted_serve.go) and writes what members do into it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// --- the team view: team read with the agent's own code -----------------------

var teamViewCache struct {
	mu sync.Mutex
	t  *teamDB
	ag *Agent
}

// teamView is team as an agent the global instance reads (and writes
// members' inputs into): its engine is never started — it drives nothing,
// Poke is a no-op — and shares the global instance's event hub, so the
// members' streams of a hosted conversation are the global instance's
// streams. nil: team isn't usable, or this isn't the global instance.
func teamView() *Agent {
	t := teamStore.Load()
	if !globalMode() || !t.usable() || agent == nil || agent.eng == nil {
		return nil
	}
	teamViewCache.mu.Lock()
	defer teamViewCache.mu.Unlock()
	if teamViewCache.t == t {
		return teamViewCache.ag
	}
	ag := &Agent{db: t.runs, repl: newReplRegistry(), toolSem: agent.toolSem, blobs: agent.blobs, blobCache: agent.blobCache, noGateway: true}
	e := newEngine(t.runs, ag, nil, "")
	e.hub = agent.eng.hub
	e.gate = agent.eng.gate
	e.decorate = func(r *Run, sum map[string]any) { // a list row's and a view's hosting, live
		if r.ParentID == 0 {
			if h, err := t.runs.teamHost(r.ID); err == nil {
				sum["hosted"] = hostedInfo(h)
			}
		}
	}
	teamViewCache.t, teamViewCache.ag = t, ag
	return ag
}

// hostedInfo is a hosted conversation's hosting as its members see it.
func hostedInfo(h *teamHost) map[string]any {
	state, reason := h.State, h.Reason
	if state == hostPaused && time.Since(time.Unix(h.Since, 0)) > hostPauseTTL {
		state, reason = hostDropped, "expired" // the host never answered: whatever its partition says
	}
	out := map[string]any{"host": hostOf(h), "state": state, "reason": reason, "resources": h.Resources,
		"since": h.Since, "movedFrom": h.MovedFrom}
	if state == hostPaused {
		p, _ := parseAudience(h.Pending)
		out["pending"] = p.Members
		out["pendingKey"] = h.Pending
		out["dropsAt"] = time.Unix(h.Since, 0).Add(hostPauseTTL).Unix()
	}
	return out
}

// publishHosted tells the conversation's streams its hosting changed.
func publishHosted(tv *Agent, root int64) {
	h, err := tv.db.teamHost(root)
	if err != nil {
		return
	}
	tv.eng.hub.publish(&Event{Type: "hosted", Run: root, Root: root, Data: hostedInfo(h)})
	tv.eng.publishRun(root)
}

// --- moving a conversation into team ---------------------------------------------

// handleHostedMove: POST /hosted {conversation} from a person's partition
// (hosted.go handleHostingStart): the person — a participant — hosts it.
// Idempotent for its host (a re-adopt answers the conversation as it is).
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
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := body.Conversation
	if hostedID(id) {
		h, err := tv.db.teamHost(id)
		_, lv, lerr := hostedLevel(tv.db, c, id)
		switch {
		case err != nil || lerr != nil || lv == lvNone:
			xbin.WriteError(w, 404, "no such conversation")
		case h.Host != "user:"+c.user:
			xbin.WriteError(w, 409, hostOf(h)+" hosts it: one host per conversation")
		case h.State == hostGone:
			xbin.WriteError(w, 409, "hosting it ended: continue it without the host first")
		default:
			if h.State == hostDropped {
				_ = tv.db.setTeamHostState(id, hostActive, "", "")
				publishHosted(tv, id)
			}
			acl, _ := tv.db.loadACL(id)
			xbin.WriteJSON(w, 200, map[string]any{"conversation": id, "audience": audienceOf(acl)})
		}
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
	}
	moved, left, err := moveIntoTeam(r.Context(), tv, run, c)
	if err != nil {
		writeImportErr(w, err)
		return
	}
	acl, _ := tv.db.loadACL(moved)
	xbin.WriteJSON(w, 200, map[string]any{"conversation": moved, "from": id, "audience": audienceOf(acl), "left": left})
}

// moveIntoTeam copies conversation run into team — its transcript, task
// ledger, text session files, audience and the members' pins — hosted by c,
// then deletes it here (its share links with it). Binary session files stay
// behind (they live in this instance's blob store): named in left.
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
	spec := &shareSpec{}
	for u, role := range acl.members {
		spec.Members = append(spec.Members, shareMember{User: u, Role: role})
	}
	st := runStamp{Owner: acl.owner, Visibility: acl.visibility, TeamRole: acl.teamRole}
	cls, err := importClass(c, b)
	if err != nil {
		return 0, nil, err
	}
	note := fmt.Sprintf("%s hosts this conversation now: the agent may use %s's private resources in it "+
		"(their sandboxes, their data in other tiles, their vault). It is not private: its members, the agent's managers, "+
		"workspace admins and anyone who can change the agent's code can read it.", c.user, c.user)
	if len(left) > 0 {
		note += " Left behind: " + strings.Join(left, ", ") + "."
	}
	moved, err := tv.importConv(ctx, b, c, st, cls, spec, note, false)
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
	if _, err := tv.db.q.Exec(`INSERT INTO team_hosts (run_id, host, state, resources, moved_from, since, created) VALUES (?, ?, 'active', ?, ?, ?, ?)`,
		moved.ID, "user:"+c.user, string(res), run.ID, now(), now()); err != nil {
		_ = tv.deleteRunTree(moved.ID)
		return 0, nil, err
	}
	dropConversation(agent, run.ID, "hosted: it moved to the non-secure space")
	tv.aclChanged(moved.ID)
	tv.eng.publishRun(moved.ID)
	return moved.ID, left, nil
}

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
func dropConversation(ag *Agent, root int64, why string) {
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
		ag.eng.hub.publish(&Event{Type: evRun, Run: root, Root: root, Data: map[string]any{"id": root, "deleted": true}, acl: acl})
	}
}

// --- continuing without the host -----------------------------------------------------

// handleHostedContinue: POST /hosted/{id}/continue — a participant takes a
// conversation whose hosting ended (declined, taken back, 7 days unanswered,
// its host gone) back into the shared space, without the host's resources:
// a plain shared conversation again (a new id), the team copy deleted.
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
	run, lv, err := hostedLevel(tv.db, c, id)
	if err != nil || lv == lvNone {
		xbin.WriteError(w, 404, "no such conversation")
		return
	}
	if lv < lvParticipant {
		xbin.WriteError(w, 403, "only someone who may talk in it can continue it")
		return
	}
	h, err := tv.db.teamHost(rootOf(run))
	if err != nil {
		xbin.WriteError(w, 404, "no such conversation")
		return
	}
	if st := hostedInfo(h)["state"]; st != hostDropped && st != hostGone {
		xbin.WriteError(w, 409, fmt.Sprintf("%s still hosts it (%s): it continues without their resources once they stop hosting it", hostOf(h), st))
		return
	}
	root := rootOf(run)
	b, err := tv.exportConv(r.Context(), root, true)
	if err != nil {
		writeBundleErr(w, err)
		return
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
		xbin.WriteError(w, 404, "no such conversation")
		return
	}
	spec := &shareSpec{}
	for u, role := range acl.members {
		spec.Members = append(spec.Members, shareMember{User: u, Role: role})
	}
	cls, err := importClass(c, b)
	if err != nil {
		writeClassErr(w, err)
		return
	}
	note := fmt.Sprintf("Continued by %s without %s's private resources: a shared conversation again.", orStr(c.user, "the agent"), orStr(hostOf(h), "its host"))
	back, err := agent.importConv(r.Context(), b, c, runStamp{Owner: acl.owner, Visibility: acl.visibility, TeamRole: acl.teamRole},
		cls, spec, note, false)
	if err != nil {
		writeImportErr(w, err)
		return
	}
	dropConversation(tv, root, "continued without its host")
	_, _ = tv.db.q.Exec(`DELETE FROM team_hosts WHERE run_id=?`, root)
	xbin.WriteJSON(w, 200, map[string]any{"conversation": back.ID, "from": root})
}

// --- the host's live run, fanned out ----------------------------------------------------

// handleHostedEvents: POST /hosted/events {events} — the host's engine's
// events, which the global instance publishes on its own hub for the
// members' streams (each subscribed under the conversation's ACL). Only
// events of conversations team says the caller hosts go out; a run's summary
// is re-read from team rather than taken from the post.
func handleHostedEvents(w http.ResponseWriter, r *http.Request) {
	if !globalMode() {
		xbin.WriteError(w, 404, "hosted conversations are the agent's shared instance's")
		return
	}
	if !personFromPartition(r) {
		xbin.WriteError(w, 403, "only a host's partition posts its run")
		return
	}
	tv := teamView()
	if tv == nil {
		xbin.WriteError(w, 503, "the shared space is being upgraded — try again in a minute")
		return
	}
	var body struct {
		Events []fwdEvent `json:"events"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&body); err != nil {
		xbin.WriteError(w, 400, "need {events: […]}")
		return
	}
	host := "user:" + callerOf(r).user
	hosts := map[int64]bool{}
	published, refused := 0, 0
	for _, ev := range body.Events {
		ok, seen := hosts[ev.Root]
		if !seen {
			h, err := tv.db.teamHost(ev.Root)
			ok = err == nil && h.Host == host
			hosts[ev.Root] = ok
		}
		if !ok {
			refused++
			continue
		}
		published++
		switch ev.Type {
		case "hosted":
			publishHosted(tv, ev.Root)
		case evRun:
			tv.eng.publishRun(ev.Run)
		default:
			noteHostedDraft(tv.eng, ev)
			tv.eng.hub.publish(&Event{Type: ev.Type, Run: ev.Run, Root: ev.Root, Data: ev.Data, key: ev.Key})
		}
	}
	if refused > 0 {
		logf("hosted events from %s: %d refused (not conversations it hosts)", host, refused)
	}
	xbin.WriteJSON(w, 200, map[string]int{"published": published, "refused": refused})
}

// noteHostedDraft keeps the host's model call in flight on the team view's
// engine, so a member who connects mid-answer gets the text so far (the
// stream's and /view's drafts).
func noteHostedDraft(e *Engine, ev fwdEvent) {
	d, _ := ev.Data.(map[string]any)
	e.mu.Lock()
	defer e.mu.Unlock()
	switch ev.Type {
	case evDraftEnd:
		delete(e.drafts, ev.Run)
	case evText, evThinking:
		dr := e.drafts[ev.Run]
		if dr == nil {
			dr = &draft{Run: ev.Run, Tools: map[int]*draftTool{}, root: ev.Root}
			e.drafts[ev.Run] = dr
		}
		text, _ := d["text"].(string)
		started, _ := d["started"].(float64)
		if ev.Type == evText {
			dr.Text = text
			dr.Model, _ = d["model"].(string)
			dr.Started = int64(started)
		} else {
			dr.Thinking, dr.ThinkStart = text, int64(started)
		}
	}
}

// handleHostedChangedMail (global): the host's live post didn't get through
// — re-read the conversation from team for its streams.
func handleHostedChangedMail(_ context.Context, t *DB, it mailItem) error {
	if !globalMode() || !strings.HasPrefix(it.From, "user:") {
		return nil
	}
	var d struct {
		Conversation int64 `json:"conversation"`
	}
	if json.Unmarshal(it.Data, &d) != nil || !hostedID(d.Conversation) {
		return nil
	}
	t.AfterCommit(func() {
		tv := teamView()
		if tv == nil {
			return
		}
		if h, err := tv.db.teamHost(d.Conversation); err == nil && h.Host == it.From {
			publishHosted(tv, d.Conversation)
		}
	})
	return nil
}

// --- ringing the host ----------------------------------------------------------------

// wakeRetries is how the global instance retries ringing a host; what it
// rang for is in team either way (the host's next start takes it up).
var wakeRetries = []time.Duration{0, time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute}

var wakes = struct {
	sync.Mutex
	busy map[string]bool // a plain wake of this host in flight
}{busy: map[string]bool{}}

// wakeHost mails the conversation's host that something waits for it
// (hosted/input). Plain wakes of one host coalesce; a signal always goes. A
// final refusal (the person is gone, disabled or can't read the tile any
// more) marks hosting gone: the members may continue without them.
func wakeHost(tv *Agent, root int64, in hostedInput) {
	h, err := tv.db.teamHost(root)
	if err != nil || (h.State != hostActive && h.State != hostPaused) {
		return
	}
	in.Conversation = root
	plain := in.Signal == ""
	if plain {
		wakes.Lock()
		if wakes.busy[h.Host] {
			wakes.Unlock()
			return
		}
		wakes.busy[h.Host] = true
		wakes.Unlock()
	}
	go func() {
		defer func() {
			if plain {
				wakes.Lock()
				delete(wakes.busy, h.Host)
				wakes.Unlock()
			}
		}()
		for _, wait := range wakeRetries {
			time.Sleep(wait)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := sendMail(ctx, h.Host, topicHostedInput, in, "")
			cancel()
			switch {
			case err == nil:
				return
			case errors.Is(err, errMailRefused):
				logf("hosted conversation #%d: its host %s can't be reached (%v) — hosting is gone", root, h.Host, err)
				_ = tv.db.setTeamHostState(root, hostGone, "left", "")
				publishHosted(tv, root)
				return
			}
			logf("hosted conversation #%d: ringing %s: %v", root, h.Host, err)
		}
	}()
}
