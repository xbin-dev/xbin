// triggers_admin.go — the owner's side of triggers (D87): create, edit,
// switch, test-fire, their events; and the Automations page's kind.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	registerAutomationKind(automationKind{Kind: "trigger", Origin: "trigger", List: triggerItems,
		Reset: func(w who, id int64) error {
			tr, err := agent.db.getTrigger(id)
			if err != nil {
				return fmt.Errorf("no such trigger")
			}
			if tr.access(w) < lvOwner {
				return fmt.Errorf("only its owner can start it afresh")
			}
			agent.db.resetSession(trigKey(id))
			return nil
		}})
}

func triggerItems(w who) []AutomationItem {
	var out []AutomationItem
	for _, tr := range agent.db.listTriggers(``) {
		it := AutomationItem{Kind: "trigger", ID: tr.ID, Name: tr.Name, Owner: tr.Owner, Visibility: tr.Visibility, Enabled: tr.Enabled,
			Mode: tr.Mode, TargetRun: tr.TargetRun, LastRunID: tr.LastRunID, LastRunAt: tr.LastEvent, LastStatus: tr.Status,
			Summary: triggerSummary(tr), Config: tr}
		if tr.Mode == "persistent" {
			it.CurrentRun, _ = agent.db.sessionRun(trigKey(tr.ID))
		}
		switch lv := tr.access(w); {
		case lv >= lvOwner:
			it.Access = "owner"
			if strings.HasPrefix(tr.Status, "needs-grant") || strings.HasPrefix(tr.Status, "error") {
				it.Attention = 1
			}
		case lv >= lvViewer:
			it.Access = "viewer"
		case w.manager() && w.viewedBy == "":
			it.Access, it.Config, it.Summary = "oversee", nil, tr.Source+" · "+tr.SourceRef
		default:
			continue
		}
		out = append(out, it)
	}
	return out
}

func triggerSummary(tr *Trigger) string {
	what := "a push from " + tr.SourceRef
	if tr.Source == "bus" {
		what = "an event on " + tr.SourceRef
	}
	if tr.Match != "" {
		what += " (" + tr.Match + "…)"
	}
	return what + " · " + map[string]string{"isolated": "a new run each time", "persistent": "one ongoing thread",
		"conversation": "into a conversation"}[tr.Mode]
}

func triggerFor(w http.ResponseWriter, r *http.Request) (*Trigger, who, level, bool) {
	c := callerOf(r)
	tr, err := agent.db.getTrigger(pathID(r))
	if err != nil || (tr.access(c) == lvNone && !c.manager()) {
		xbin.WriteError(w, 404, "no such trigger")
		return nil, c, lvNone, false
	}
	return tr, c, tr.access(c), true
}

// deliverOK: announcing into a channel session needs that channel to be the
// trigger owner's (a trigger can't post into someone else's chat).
func deliverOK(c who, key string) string {
	if key == "" {
		return ""
	}
	var chID int64
	if err := agent.db.q.QueryRow(`SELECT origin_id FROM sessions WHERE key=? AND origin='channel'`, key).Scan(&chID); err != nil {
		return "no such channel session: " + key
	}
	ch, err := agent.db.getChannel(chID)
	if err != nil || ch.access(c) < lvOwner {
		return "you can only announce into your own channel's sessions"
	}
	return ""
}

// handleNewTrigger: POST /triggers — the caller owns it.
func handleNewTrigger(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	var tr Trigger
	if err := json.NewDecoder(r.Body).Decode(&tr); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	tr.ID, tr.Owner, tr.Enabled, tr.Status = 0, c.tag(), true, ""
	if tr.DataClass == "" {
		tr.DataClass = "private"
	}
	if msg := tr.validate(); msg != "" {
		xbin.WriteError(w, 400, msg)
		return
	}
	if msg := deliverOK(c, tr.Deliver); msg != "" {
		xbin.WriteError(w, 400, msg)
		return
	}
	if tr.Mode == "conversation" && !agent.targetOK(tr.Owner, tr.TargetRun) {
		xbin.WriteError(w, 400, "that conversation isn't one you can post in")
		return
	}
	if err := agent.db.saveTrigger(&tr); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			xbin.WriteError(w, 409, "a trigger with that name exists")
			return
		}
		xbin.WriteError(w, 500, err.Error())
		return
	}
	agent.syncTriggerBus(&tr)
	saved, _ := agent.db.getTrigger(tr.ID)
	emitAutomation("trigger", tr.ID)
	xbin.WriteJSON(w, 200, saved)
}

// handleUpdateTrigger: its owner changes anything; a manager only switches
// it on or off. A visibility change carries to its runs.
func handleUpdateTrigger(w http.ResponseWriter, r *http.Request) {
	tr, c, lv, ok := triggerFor(w, r)
	if !ok {
		return
	}
	var patch map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	if lv < lvOwner {
		if _, onlyEnabled := patch["enabled"]; !c.manager() || !onlyEnabled || len(patch) != 1 {
			xbin.WriteError(w, 403, "only its owner can change it (a manager may switch it on or off)")
			return
		}
	}
	prevVis := tr.Visibility
	raw, _ := json.Marshal(patch)
	next := *tr
	if err := json.Unmarshal(raw, &next); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	next.ID, next.Owner, next.Status, next.Created = tr.ID, tr.Owner, tr.Status, tr.Created
	if msg := next.validate(); msg != "" {
		xbin.WriteError(w, 400, msg)
		return
	}
	if next.Deliver != tr.Deliver {
		if msg := deliverOK(c, next.Deliver); msg != "" {
			xbin.WriteError(w, 400, msg)
			return
		}
	}
	if err := agent.db.saveTrigger(&next); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if next.Visibility != prevVis && next.Owner != "" {
		_, _ = agent.db.q.Exec(`UPDATE runs SET visibility=? WHERE origin='trigger' AND origin_id=?`, next.Visibility, next.ID)
		agent.acl.flush(0)
	}
	if next.Source != tr.Source || next.SourceRef != tr.SourceRef || next.Match != tr.Match || next.Enabled != tr.Enabled {
		if tr.Source == "bus" && !agent.noGateway && (next.Source != "bus" || next.SourceRef != tr.SourceRef) {
			_ = xbin.Unsubscribe(trigSubName(tr.ID))
		}
		agent.syncTriggerBus(&next)
	}
	saved, _ := agent.db.getTrigger(tr.ID)
	emitAutomation("trigger", tr.ID)
	xbin.WriteJSON(w, 200, saved)
}

func handleDeleteTrigger(w http.ResponseWriter, r *http.Request) {
	tr, c, lv, ok := triggerFor(w, r)
	if !ok {
		return
	}
	if lv < lvOwner && !c.manager() {
		xbin.WriteError(w, 403, "only its owner or a manager can remove it")
		return
	}
	if tr.Source == "bus" && !agent.noGateway {
		_ = xbin.Unsubscribe(trigSubName(tr.ID))
	}
	_, _ = agent.db.q.Exec(`DELETE FROM triggers WHERE id=?`, tr.ID)
	_, _ = agent.db.q.Exec(`DELETE FROM trigger_events WHERE trigger_id=?`, tr.ID)
	emitAutomation("trigger", tr.ID)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleTestTrigger fires it with the owner's sample event (its data counts
// as the trigger's own class).
//
//	POST /triggers/{id}/test {topic?, text?, data?}
func handleTestTrigger(w http.ResponseWriter, r *http.Request) {
	tr, _, lv, ok := triggerFor(w, r)
	if !ok {
		return
	}
	if lv < lvOwner {
		xbin.WriteError(w, 403, "only its owner can test it")
		return
	}
	var b struct {
		Topic string          `json:"topic"`
		Text  string          `json:"text"`
		Data  json.RawMessage `json:"data"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	v, err := agent.fireTrigger(tr, trigEvent{ID: "test:" + strconv.FormatInt(time.Now().UnixNano(), 10), Source: "test",
		Topic: orStr(b.Topic, tr.Match+"test"), Text: b.Text, Data: b.Data, Class: tr.DataClass})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, v)
}

// handleTriggerEvents: the last events it saw, newest first.
func handleTriggerEvents(w http.ResponseWriter, r *http.Request) {
	tr, _, lv, ok := triggerFor(w, r)
	if !ok {
		return
	}
	if lv < lvViewer {
		xbin.WriteError(w, 403, "only its owner and team can see its events")
		return
	}
	rows, err := agent.db.q.Query(`SELECT event_id, source, topic, accepted, reason, run_id, created FROM trigger_events
		WHERE trigger_id=? ORDER BY created DESC, rowid DESC LIMIT 50`, tr.ID)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, source, topic, reason string
		var accepted int
		var run, created int64
		if rows.Scan(&id, &source, &topic, &accepted, &reason, &run, &created) == nil {
			out = append(out, map[string]any{"eventId": id, "source": source, "topic": topic, "accepted": accepted != 0,
				"reason": reason, "runId": run, "at": created})
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{"events": out})
}

// handleUnmatched: pushes no trigger took (managers), to make one from.
func handleUnmatched(w http.ResponseWriter, r *http.Request) {
	unmatched.mu.Lock()
	defer unmatched.mu.Unlock()
	xbin.WriteJSON(w, 200, map[string]any{"items": append([]map[string]any{}, unmatched.list...)})
}
