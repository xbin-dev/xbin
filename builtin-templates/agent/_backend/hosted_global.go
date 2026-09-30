// hosted_global.go — the global instance's side of non-secure (hosted)
// conversations (hosted.go): the team view it reads them with, fanning the
// host's live run out to the members (POST /hosted/events {events}, batched),
// and ringing the host when members write (partition mail hosted/input).
// Moving one into team and back out of it is hosted_move.go's.
//
// The global instance's engine never drives a team row: it only reads team
// (hosted_serve.go) and writes what members do into it.
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	switch {
	case state == hostPaused && time.Since(time.Unix(h.Since, 0)) > hostPauseTTL:
		state, reason = hostDropped, "expired" // the host never answered: whatever its partition says
	case state == hostPending && time.Since(time.Unix(h.Since, 0)) > hostPendingTTL:
		state, reason = hostDropped, "unclaimed" // its host's partition never took it up (hosted_move.go)
	}
	out := map[string]any{"host": hostOf(h), "state": state, "reason": reason, "resources": h.Resources,
		"since": h.Since, "movedFrom": h.MovedFrom}
	if state == hostPaused {
		var p teamPending
		_ = json.Unmarshal([]byte(h.Pending), &p)
		if p.Audience.Members == nil {
			p.Audience.Members = map[string]string{}
		}
		out["pending"] = append([]string{}, p.New...) // who is new
		out["pendingKey"] = p.Audience.key()          // what the host confirms (POST /hosting/{id}/confirm {seen})
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

// --- the host's live run, fanned out ----------------------------------------------------

// handleHostedEvents: POST /hosted/events {events} — the host's engine's
// events, which the global instance publishes on its own hub for the
// members' streams (each subscribed under the conversation's ACL). Only
// events of conversations team says the caller hosts, and of runs in that
// conversation's tree, go out; what is durable — a run's summary, a message,
// a step, the queue, a link — is re-read from team by its id rather than
// taken from the post (a person's frame may post here too: xbind attributes
// it to the same person); only a model call in flight, which exists nowhere
// else, is taken as posted.
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
	hosts, trees := map[int64]bool{}, map[int64]int64{}
	published, refused := 0, 0
	for _, ev := range body.Events {
		ok, seen := hosts[ev.Root]
		if !seen {
			h, err := tv.db.teamHost(ev.Root)
			ok = err == nil && h.Host == host
			hosts[ev.Root] = ok
		}
		root, seen := trees[ev.Run]
		if !seen {
			if run, err := tv.db.getRun(ev.Run); err == nil {
				root = rootOf(run)
			}
			trees[ev.Run] = root
		}
		if !ok || root != ev.Root || !publishHostedEvent(tv, ev) {
			refused++
			continue
		}
		published++
	}
	if refused > 0 {
		logf("hosted events from %s: %d refused (not runs of conversations it hosts, or not what they say)", host, refused)
	}
	xbin.WriteJSON(w, 200, map[string]int{"published": published, "refused": refused})
}

// publishHostedEvent publishes one of the host's events for the members
// (false: not a run's own event, or not what team holds).
func publishHostedEvent(tv *Agent, ev fwdEvent) bool {
	e := tv.eng
	out := &Event{Type: ev.Type, Run: ev.Run, Root: ev.Root}
	switch ev.Type {
	case "hosted":
		if ev.Run != ev.Root {
			return false
		}
		publishHosted(tv, ev.Root)
		return true
	case evRun:
		e.publishRun(ev.Run)
		return true
	case evMessage:
		m, err := tv.db.messageByID(eventID(ev.Data))
		if err != nil || m.RunID != ev.Run {
			return false
		}
		out.Data = messageView(m)
	case evStep:
		st, err := hostedStep(tv.db, eventID(ev.Data))
		if err != nil || st.RunID != ev.Run {
			return false
		}
		out.Data = st
	case evInbox:
		out.Data = map[string]any{"queued": tv.db.queuedView(ev.Run)}
	case evLink:
		l, err := tv.db.getLink(eventID(ev.Data))
		if err != nil || l.ParentID != ev.Run {
			return false
		}
		out.Data = e.linkView(l)
	case evText, evThinking, evToolArgs, evDraftEnd: // a model call in flight: only its host has it
		if !noteHostedDraft(e, ev) {
			return false
		}
		out.Data, out.key = ev.Data, hostedDraftKey(ev)
	default: // a stream's own words (reset, bye, revoked…) are the global instance's to say
		return false
	}
	e.hub.publish(out)
	return true
}

// eventID is the id in a posted event's data ({id: …}).
func eventID(data any) int64 {
	d, _ := data.(map[string]any)
	f, _ := d["id"].(float64)
	return int64(f)
}

// hostedStep is one journal step of team by its id.
func hostedStep(tr *DB, id int64) (*Step, error) {
	s := &Step{}
	err := tr.q.QueryRow(`SELECT id, run_id, seq, kind, detail, created FROM steps WHERE id=?`, id).
		Scan(&s.ID, &s.RunID, &s.Seq, &s.Kind, &s.Detail, &s.Created)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// hostedDraftKey is a posted draft's coalescing key, made here (draft.go's).
func hostedDraftKey(ev fwdEvent) string {
	switch ev.Type {
	case evText:
		return "text:" + itoa(ev.Run)
	case evThinking:
		return "thinking:" + itoa(ev.Run)
	case evToolArgs:
		d, _ := ev.Data.(map[string]any)
		idx, _ := d["index"].(float64)
		return "tool:" + itoa(ev.Run) + ":" + itoa(int64(idx))
	}
	return ""
}

// hostedDraftTTL: a draft of the host's with no word for this long is gone
// (its host stopped mid-answer, or its end was lost).
var hostedDraftTTL = 60 * time.Second

var hostedDraftTimers = struct {
	sync.Mutex
	m map[int64]*time.Timer
}{m: map[int64]*time.Timer{}}

// noteHostedDraft keeps the host's model call in flight on the team view's
// engine, so a member who connects mid-answer gets the text so far (the
// stream's and /view's drafts) — never touching another conversation's
// draft (false), and letting one go that hears nothing for hostedDraftTTL.
func noteHostedDraft(e *Engine, ev fwdEvent) bool {
	d, _ := ev.Data.(map[string]any)
	e.mu.Lock()
	dr := e.drafts[ev.Run]
	if dr != nil && dr.root != ev.Root {
		e.mu.Unlock()
		return false
	}
	switch ev.Type {
	case evDraftEnd:
		delete(e.drafts, ev.Run)
	case evText, evThinking:
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
	e.mu.Unlock()
	hostedDraftTimers.Lock()
	if t := hostedDraftTimers.m[ev.Run]; t != nil {
		t.Stop()
		delete(hostedDraftTimers.m, ev.Run)
	}
	if ev.Type != evDraftEnd {
		run, root := ev.Run, ev.Root
		hostedDraftTimers.m[run] = time.AfterFunc(hostedDraftTTL, func() { expireHostedDraft(e, run, root) })
	}
	hostedDraftTimers.Unlock()
	return true
}

// expireHostedDraft lets a silent draft go, and tells the streams.
func expireHostedDraft(e *Engine, run, root int64) {
	hostedDraftTimers.Lock()
	delete(hostedDraftTimers.m, run)
	hostedDraftTimers.Unlock()
	e.mu.Lock()
	dr := e.drafts[run]
	gone := dr != nil && dr.root == root
	if gone {
		delete(e.drafts, run)
	}
	e.mu.Unlock()
	if gone {
		e.hub.publish(&Event{Type: evDraftEnd, Run: run, Root: root})
	}
}

// resetHosted drops conversation root's drafts here and has its streams
// re-read it (what the host's lost posts carried is in team).
func resetHosted(tv *Agent, root int64) {
	e := tv.eng
	e.mu.Lock()
	for run, dr := range e.drafts {
		if dr.root == root {
			delete(e.drafts, run)
		}
	}
	e.mu.Unlock()
	e.hub.publishTo(func(s *subscriber) bool { return s.root == root }, &Event{Type: evReset, Run: root, Root: root})
}

// handleHostedChangedMail (global): the host's live post didn't get through
// — its drafts here are stale and its streams missed commits: they re-read
// the conversation from team.
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
			resetHosted(tv, d.Conversation)
			publishHosted(tv, d.Conversation)
		}
	})
	return nil
}

// --- ringing the host ----------------------------------------------------------------

// wakeRetries is how the global instance retries ringing a host; what it
// rang for is in team either way (the host's next start takes it up).
var wakeRetries = []time.Duration{0, time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute}

// ringing counts rings in flight (tests wait for them).
var ringing sync.WaitGroup

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
	ringHost(tv, h, root, in)
}

// ringHost mails h's host about conversation root whatever team says now
// (deleted or continued: its partition finds it gone and stops).
func ringHost(tv *Agent, h *teamHost, root int64, in hostedInput) {
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
	retries := wakeRetries
	ringing.Add(1)
	go func() {
		defer ringing.Done()
		defer func() {
			if plain {
				wakes.Lock()
				delete(wakes.busy, h.Host)
				wakes.Unlock()
			}
		}()
		for _, wait := range retries {
			time.Sleep(wait)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := sendMail(ctx, h.Host, topicHostedInput, in, "")
			cancel()
			switch {
			case err == nil:
				return
			case errors.Is(err, errMailRefused):
				logf("hosted conversation #%d: its host %s can't be reached (%v) — hosting is gone", root, h.Host, err)
				_, _ = tv.db.q.Exec(`UPDATE team_hosts SET state='gone', reason='left', pending='', since=? WHERE run_id=? AND state IN ('pending','active','paused')`, now(), root)
				publishHosted(tv, root)
				return
			}
			logf("hosted conversation #%d: ringing %s: %v", root, h.Host, err)
		}
	}()
}
