// hosted.go — a person's partition hosting a non-secure conversation (API.md
// "Non-secure conversations"; the plan's 08 §4, PD-32, PD-33).
//
// A shared conversation lives at the global instance and runs there, where
// no one's private resources are. When a person chooses to let it use
// theirs — their sandboxes, their data in other partitioned tiles, their
// vault: everything their own partition reaches — the conversation becomes
// non-secure and hosted by them:
//
//   - the global instance moves it into `team` (team_runs.go), whose rows
//     every partition's code can read, and notes the host there
//     (team_hosts: a hint);
//   - the host's partition records it in its own `hosted` table — the
//     authority — with a snapshot of who could read it when the host said
//     yes, and drives it with an engine of its own over team
//     (hosted_engine.go) that takes up nothing else;
//   - the members keep talking to it at the global instance, which writes
//     their inputs into team and rings the host's partition (partition mail
//     hosted/input); the host's engine streams its run to the global
//     instance, which fans it out to every member's stream (90 §I4);
//   - a wider audience than the host confirmed — a member added, the team
//     let in, a viewer made a participant — pauses it until the host
//     confirms again; declining, taking the resources back or 7 days
//     without an answer drops hosting, and any member may then continue it
//     without the host's resources (the global instance takes it back).
//
// Unpartitioned and at the global instance nothing here runs (the routes
// answer 404 or 409).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	topicHostedInput   = "hosted/input"   // global → the host: something waits in team (a member wrote, stopped it, changed who is in it)
	topicHostedChanged = "hosted/changed" // the host → global: a change its live post couldn't carry (global re-reads team)
)

// hostResources is what a hosted conversation reaches: all the host's
// partition does. (A per-resource choice is future work: the engine runs in
// the host's partition, whose calls xbind attributes to it as a whole.)
var hostResources = []string{"sandboxes", "tiles", "vault"}

// hostPauseTTL is how long a paused conversation waits for its host before
// hosting drops.
var hostPauseTTL = 7 * 24 * time.Hour

func parseResources(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

// --- the audience ---------------------------------------------------------------

// audience is who can read and steer a conversation: what a host confirms.
type audience struct {
	Owner      string            `json:"owner"`
	Visibility string            `json:"visibility"`
	TeamRole   string            `json:"teamRole"`
	Members    map[string]string `json:"members"`
}

func audienceOf(a *rootACL) audience {
	m := map[string]string{}
	for u, r := range a.members {
		m[u] = r
	}
	return audience{Owner: a.owner, Visibility: a.visibility, TeamRole: a.teamRole, Members: m}
}

// key is the audience as one canonical string (what a person saw, to compare).
func (a audience) key() string {
	b, _ := json.Marshal(a) // encoding/json sorts map keys
	return string(b)
}

func parseAudience(s string) (audience, bool) {
	var a audience
	if s == "" || json.Unmarshal([]byte(s), &a) != nil {
		return audience{}, false
	}
	if a.Members == nil {
		a.Members = map[string]string{}
	}
	return a, true
}

// beyond lists how a reaches further than snap — people or the team who
// can read it and couldn't, or can now steer it and only read it before.
// Empty: within what snap allowed (narrower is fine).
func (a audience) beyond(snap audience) []string {
	var out []string
	if a.Owner != snap.Owner {
		out = append(out, a.Owner+" (the owner)")
	}
	switch {
	case a.Visibility == visTeam && snap.Visibility != visTeam:
		out = append(out, "the team")
	case a.Visibility == visTeam && a.TeamRole == roleParticipant && snap.TeamRole != roleParticipant:
		out = append(out, "the team (to talk to it)")
	}
	var names []string
	for u := range a.Members {
		names = append(names, u)
	}
	sort.Strings(names)
	for _, u := range names {
		was, ok := snap.Members[u]
		switch {
		case !ok && u != snap.Owner:
			out = append(out, u)
		case a.Members[u] == roleParticipant && was == roleViewer:
			out = append(out, u+" (to talk to it)")
		}
	}
	return out
}

// --- the host's own table ----------------------------------------------------------

func (d *DB) addHostedSchema() error {
	_, err := d.q.Exec(`CREATE TABLE IF NOT EXISTS hosted (
		conversation INTEGER PRIMARY KEY,
		state TEXT NOT NULL DEFAULT 'active',
		snapshot TEXT NOT NULL DEFAULT '',
		resources TEXT NOT NULL DEFAULT '[]',
		pending TEXT NOT NULL DEFAULT '',
		confirmed_at INTEGER NOT NULL DEFAULT 0,
		paused_at INTEGER NOT NULL DEFAULT 0,
		created INTEGER NOT NULL DEFAULT 0
	)`)
	return err
}

// hostedRow is one conversation this partition hosts.
type hostedRow struct {
	Conv        int64    `json:"conversation"`
	State       string   `json:"state"`
	Snapshot    audience `json:"audience"`
	Resources   []string `json:"resources"`
	Pending     []string `json:"pending,omitempty"` // who waits for the host's yes
	PendingKey  string   `json:"pendingKey,omitempty"`
	ConfirmedAt int64    `json:"confirmedAt"`
	PausedAt    int64    `json:"pausedAt,omitempty"`
	Created     int64    `json:"created"`
}

const hostedCols = `conversation, state, snapshot, resources, pending, confirmed_at, paused_at, created`

func scanHosted(sc interface{ Scan(...any) error }) (*hostedRow, error) {
	h := &hostedRow{}
	var snap, res, pending string
	if err := sc.Scan(&h.Conv, &h.State, &snap, &res, &pending, &h.ConfirmedAt, &h.PausedAt, &h.Created); err != nil {
		return nil, err
	}
	h.Snapshot, _ = parseAudience(snap)
	h.Resources = parseResources(res)
	if p, ok := parseAudience(pending); ok {
		h.Pending, h.PendingKey = p.beyond(h.Snapshot), p.key()
	}
	return h, nil
}

func (d *DB) hostedRow(conv int64) (*hostedRow, error) {
	return scanHosted(d.q.QueryRow(`SELECT `+hostedCols+` FROM hosted WHERE conversation=?`, conv))
}

func (d *DB) hostedRows() []*hostedRow {
	rows, err := d.q.Query(`SELECT ` + hostedCols + ` FROM hosted ORDER BY created DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*hostedRow
	for rows.Next() {
		if h, err := scanHosted(rows); err == nil {
			out = append(out, h)
		}
	}
	return out
}

// hostsAny: this partition hosts a conversation it may have to drive or ask about.
func (d *DB) hostsAny() bool {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM hosted WHERE state IN ('active','paused')`).Scan(&n)
	return n > 0
}

// --- the host's routes (a person's partition) ---------------------------------------------

// hostOnly: a call of this partition's own person (their frame here) — the
// only one who may host with their resources, confirm or decline.
func hostOnly(w http.ResponseWriter, r *http.Request) bool {
	if !userMode() {
		xbin.WriteError(w, http.StatusNotFound, "hosting is a person's own partition's (this agent isn't partitioned, or this is its shared instance)")
		return false
	}
	if c := callerOf(r); c.kind != whoUser || c.user != runUser || c.viewedBy != "" {
		xbin.WriteError(w, http.StatusForbidden, "only "+runUser+" decides what their own resources are used for")
		return false
	}
	return !teamUnavailable(w)
}

// handleHostingList: GET /hosting — the conversations this person hosts.
func handleHostingList(w http.ResponseWriter, r *http.Request) {
	if !hostOnly(w, r) {
		return
	}
	rows := agent.db.hostedRows()
	if rows == nil {
		rows = []*hostedRow{}
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": rows, "resources": hostResources})
}

// handleHostingStart: POST /hosting {conversation, seen} — the person lets a
// shared conversation use their resources (after the warning: seen is the
// audience the page showed them). The global instance moves it into team;
// this partition records it and drives it. A wider audience than seen
// (it changed meanwhile) is recorded paused: they are asked again.
func handleHostingStart(w http.ResponseWriter, r *http.Request) {
	if !hostOnly(w, r) {
		return
	}
	var body struct {
		Conversation int64     `json:"conversation"`
		Seen         *audience `json:"seen"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Conversation <= 0 {
		xbin.WriteError(w, 400, "need {conversation, seen: the audience you were shown}")
		return
	}
	if body.Conversation >= partitionIDBase {
		xbin.WriteError(w, 409, "a conversation in your own space is yours alone — hosting is for a shared one (share a copy of this one first)")
		return
	}
	req, _ := json.Marshal(map[string]any{"conversation": body.Conversation, "resources": hostResources})
	res, err := callGlobal(r.Context(), http.MethodPost, "/hosted", req, "application/json")
	if err != nil {
		xbin.WriteError(w, 502, "the shared instance didn't answer: "+err.Error())
		return
	}
	if res.Status != 200 {
		w.Header().Set("Content-Type", orStr(res.Type, "application/json"))
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		return
	}
	var moved struct {
		Conversation int64    `json:"conversation"`
		Audience     audience `json:"audience"`
	}
	if err := json.Unmarshal(res.Body, &moved); err != nil || !hostedID(moved.Conversation) {
		xbin.WriteError(w, 502, "the shared instance's answer: "+clip(string(res.Body), 200))
		return
	}
	if moved.Audience.Members == nil {
		moved.Audience.Members = map[string]string{}
	}
	snap, state, pending := moved.Audience, hostActive, ""
	if body.Seen != nil {
		seen := *body.Seen
		if seen.Members == nil {
			seen.Members = map[string]string{}
		}
		if len(moved.Audience.beyond(seen)) > 0 { // it widened after the warning: ask again
			snap, state, pending = seen, hostPaused, moved.Audience.key()
		}
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
		moved.Conversation, state, snap.key(), string(resJSON), pending, t, pausedAt, t); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if state == hostPaused {
		teamHostPaused(moved.Conversation, moved.Audience, snap)
	}
	if e := ensureHostEngine(); e != nil {
		e.recover() // inputs that waited in team (a re-adopted conversation)
	}
	row, _ := agent.db.hostedRow(moved.Conversation)
	xbin.WriteJSON(w, 200, row)
}

// handleHostingConfirm: POST /hosting/{id}/confirm {seen} — the host lets
// the audience it was asked about in. seen (the prompt's pendingKey) must
// still be what the conversation's audience is: another change meanwhile is
// 409 (look again).
func handleHostingConfirm(w http.ResponseWriter, r *http.Request) {
	if !hostOnly(w, r) {
		return
	}
	var body struct {
		Seen string `json:"seen"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := pathID(r)
	row, err := agent.db.hostedRow(id)
	if err != nil || row.State == hostDropped || row.State == hostGone {
		xbin.WriteError(w, 404, "you don't host that conversation")
		return
	}
	tr := teamRuns()
	acl, err := tr.loadACL(id)
	if err != nil {
		xbin.WriteError(w, 404, "the conversation is gone")
		return
	}
	cur := audienceOf(acl)
	if body.Seen != "" && body.Seen != cur.key() {
		xbin.WriteError(w, 409, "who is in it changed again — look at the new list first")
		return
	}
	if _, err := agent.db.q.Exec(`UPDATE hosted SET state='active', snapshot=?, pending='', confirmed_at=?, paused_at=0 WHERE conversation=?`,
		cur.key(), now(), id); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	_ = tr.setTeamHostState(id, hostActive, "", "")
	hostedChangedAtGlobal(id)
	if e := ensureHostEngine(); e != nil {
		e.recover() // what waited while it was paused
	}
	row, _ = agent.db.hostedRow(id)
	xbin.WriteJSON(w, 200, row)
}

// handleHostingDrop: POST /hosting/{id}/decline, DELETE /hosting/{id} — the
// host says no to a wider audience, or takes their resources back: hosting
// drops, what runs stops at once, and the members may continue it without
// the host's resources (at the global instance).
func handleHostingDrop(w http.ResponseWriter, r *http.Request) {
	if !hostOnly(w, r) {
		return
	}
	id := pathID(r)
	reason := "stopped"
	if strings.HasSuffix(r.URL.Path, "/decline") {
		reason = "declined"
	}
	if !dropHosting(id, reason) {
		xbin.WriteError(w, 404, "you don't host that conversation")
		return
	}
	row, _ := agent.db.hostedRow(id)
	xbin.WriteJSON(w, 200, row)
}

// dropHosting ends hosting of id (the host's table first: the engine stops
// driving it at its next look; the step in flight is abandoned — writing
// nothing).
func dropHosting(id int64, reason string) bool {
	res, err := agent.db.q.Exec(`UPDATE hosted SET state='dropped', pending='' WHERE conversation=? AND state IN ('active','paused')`, id)
	if err != nil {
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false
	}
	if tr := teamRuns(); tr != nil {
		_ = tr.setTeamHostState(id, hostDropped, reason, "")
	}
	haltHosted(id)
	hostedChangedAtGlobal(id)
	return true
}

// --- the audience check (the host's engine, each pass) -------------------------------

// hostDrives: the host's engine may take up run id — its conversation is
// in this partition's own hosted table, active, and read by no one the host
// didn't confirm. A wider audience pauses it here (and asks the host).
func hostDrives(tr *DB, id int64) bool {
	run, err := tr.getRun(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { // deleted, or continued without the host: nothing left to host
			_, _ = agent.db.q.Exec(`UPDATE hosted SET state='dropped', pending='' WHERE conversation=? AND state<>'dropped'`, id)
		}
		return false
	}
	root := rootOf(run)
	row, err := agent.db.hostedRow(root)
	if err != nil || row.State != hostActive {
		return false
	}
	acl, err := tr.loadACL(root)
	if err != nil {
		return false
	}
	cur := audienceOf(acl)
	if len(cur.beyond(row.Snapshot)) == 0 {
		return true
	}
	pauseHosting(tr, root, cur, row.Snapshot)
	return false
}

// pauseHosting records a wider audience than the host confirmed: paused in
// the host's table (with who waits) and in team, the step in flight
// abandoned.
func pauseHosting(tr *DB, root int64, cur, snap audience) {
	res, err := agent.db.q.Exec(`UPDATE hosted SET state='paused', pending=?, paused_at=? WHERE conversation=? AND state='active'`,
		cur.key(), now(), root)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	logf("hosted conversation #%d: %s joined — paused until %s confirms", root, strings.Join(cur.beyond(snap), ", "), runUser)
	teamHostPaused(root, cur, snap)
	go haltHosted(root)
	hostedChangedAtGlobal(root)
}

// teamHostPaused writes the pause into team, for the members' view.
func teamHostPaused(root int64, cur, snap audience) {
	if tr := teamRuns(); tr != nil {
		_ = tr.setTeamHostState(root, hostPaused, "confirm", cur.key())
	}
}

// expireHostPauses drops hosting of the conversations paused for longer
// than hostPauseTTL (at the host's engine start, and when a pause's timer
// fires while it runs).
func expireHostPauses() {
	ids := scanIDs(agent.db.q.Query(`SELECT conversation FROM hosted WHERE state='paused' AND paused_at>0 AND paused_at<?`,
		time.Now().Add(-hostPauseTTL).Unix()))
	for _, id := range ids {
		logf("hosted conversation #%d: %s didn't confirm within %s — hosting drops", id, runUser, hostPauseTTL)
		dropHosting(id, "expired")
	}
}

// --- mail --------------------------------------------------------------------------

func init() {
	mailHandlers[topicHostedInput] = handleHostedInputMail
	mailHandlers[topicHostedChanged] = handleHostedChangedMail
}

// hostedInput is hosted/input's data.
type hostedInput struct {
	Conversation int64  `json:"conversation"`
	Run          int64  `json:"run,omitempty"`    // the run the input is for (a subagent's, or the root)
	Signal       string `json:"signal,omitempty"` // interrupt | cancel: abort the step in flight
}

// handleHostedInputMail (the host): work waits in team for a conversation
// this partition hosts — look at it (the pass decides; a conversation it
// doesn't host is never taken up). Only the global instance rings it.
func handleHostedInputMail(_ context.Context, t *DB, it mailItem) error {
	if !userMode() || it.From != "global" {
		logf("mail %s: %s from %q ignored (only the shared instance rings a host)", it.ID, it.Topic, it.From)
		return nil
	}
	var in hostedInput
	if json.Unmarshal(it.Data, &in) != nil || !hostedID(in.Conversation) {
		return nil
	}
	t.AfterCommit(func() {
		e := ensureHostEngine()
		if e == nil {
			return
		}
		run := in.Run
		if !hostedID(run) {
			run = in.Conversation
		}
		switch in.Signal {
		case "interrupt":
			e.Signal(run, errInterrupt)
		case "cancel":
			e.Signal(run, errCancel)
		case "audience": // who is in it changed: look now, even with nothing to run (a wider one pauses it)
			hostDrives(e.db, in.Conversation)
		}
		e.recover() // every run of it with work — and the audience check at its pass
	})
	return nil
}

// --- the routes ---------------------------------------------------------------------

// hostedRoutes mounts the non-secure conversations' routes and "Add a copy
// of my …" in a partitioned agent (none unpartitioned): the host's in a
// person's partition, the members' at the global instance (each handler
// answers 404 in the other mode).
func hostedRoutes(mux *http.ServeMux) {
	if !partitioned() {
		return
	}
	for _, rt := range []routeDef{
		{"GET /hosting", needUser, handleHostingList},
		{"POST /hosting", needUser, handleHostingStart},
		{"POST /hosting/{id}/confirm", needUser, handleHostingConfirm},
		{"POST /hosting/{id}/decline", needUser, handleHostingDrop},
		{"DELETE /hosting/{id}", needUser, handleHostingDrop},
		{"POST /hosted", needUser, handleHostedMove},
		{"POST /hosted/events", needUser, handleHostedEvents},
		{"POST /hosted/{id}/continue", needUser, handleHostedContinue},
		{"POST /copyin", needUser, handleCopyIn},
	} {
		mux.Handle(rt.pattern, agentRole(guard(rt.need, rt.h)))
	}
	const copyIn = "POST /runs/{id}/copyin"
	mux.Handle(copyIn, agentRole(hostedRoute(copyIn, needParticipant, guard(needParticipant, handleCopyInAt))))
}

// --- errors shared with the global side --------------------------------------------------

var errNotHosted = errors.New("not a hosted conversation")

// hostedLevel is c's access to hosted conversation id (at the global
// instance: from team's ACL).
func hostedLevel(tr *DB, c who, id int64) (*Run, level, error) {
	run, err := tr.getRun(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, lvNone, errNotHosted
		}
		return nil, lvNone, err
	}
	a, err := tr.loadACL(rootOf(run))
	if err != nil {
		return nil, lvNone, err
	}
	return run, a.level(c), nil
}

// hostOf is the person team names as a conversation's host ("" none).
func hostOf(h *teamHost) string {
	if h == nil {
		return ""
	}
	return strings.TrimPrefix(h.Host, "user:")
}
