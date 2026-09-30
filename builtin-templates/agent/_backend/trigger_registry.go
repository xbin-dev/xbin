// trigger_registry.go — private event triggers in a partitioned agent (API.md
// "Partitioned instances"). Pushes (the webhooks tile, any adapter) reach the
// global instance, so the global instance keeps the registry: today's
// `triggers` table, where a trigger's name is unique tile-wide. A person's
// trigger is two halves:
//
//   - its config (goal, class, mode, data class, delivery…) and its runs in
//     the person's own partition — created, edited, tested and deleted there
//     as unpartitioned;
//   - a registry row at global: name, source, match, owner, host
//     ("user:<id>"), enabled and the hourly cap — metadata, no goal. The
//     person's partition writes it (POST /triggers/registry, as the person)
//     whenever what it says changes; a manager sees it (oversight, D83) and
//     may switch it off or delete it there.
//
// A push a registry row matches is handed to the person's partition by mail
// (handoff.go), never run at global. So no one can quietly capture pushes:
// a private push trigger needs a non-empty topic prefix, and one that is a
// prefix of — or prefixed by — another owner's on the same source is
// refused. A bus trigger subscribes in the person's own partition (its
// events never pass global); its registry row only keeps the name taken and
// lets managers see it.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// regEntry is what a person's partition registers for one of its triggers.
type regEntry struct {
	Name       string `json:"name"`
	Prev       string `json:"prev,omitempty"` // a rename: the name it had
	Source     string `json:"source"`
	SourceRef  string `json:"sourceRef"`
	Match      string `json:"match"`
	Enabled    bool   `json:"enabled"`
	MaxPerHour int    `json:"maxPerHour"`
}

// triggerHost is a registry row's partition ("" for a trigger of this
// instance's own: every one unpartitioned, and at global a team one).
func (d *DB) triggerHost(id int64) string {
	if !partitioned() {
		return ""
	}
	var host string
	_ = d.q.QueryRow(`SELECT host FROM triggers WHERE id=?`, id).Scan(&host)
	return host
}

// hostAtGlobal is the partition of a registry row, at global ("" anywhere
// else, and for global's own triggers).
func (d *DB) hostAtGlobal(tr *Trigger) string {
	if !globalMode() {
		return ""
	}
	return d.triggerHost(tr.ID)
}

// hostedHere: a registry row of a person's trigger, at global.
func hostedHere(t *DB, tr *Trigger) bool { return t.hostAtGlobal(tr) != "" }

// --- global: the registry routes ----------------------------------------------------

// handleRegistryPut registers (or updates) the calling person's private
// trigger: POST /triggers/registry — from their own partition (F5,
// attributed to them).
func handleRegistryPut(w http.ResponseWriter, r *http.Request) {
	c, ok := registrant(w, r)
	if !ok {
		return
	}
	var e regEntry
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&e); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	e.Name, e.Prev = strings.TrimSpace(e.Name), strings.TrimSpace(e.Prev)
	probe := Trigger{Name: e.Name, Goal: "-", Source: e.Source, SourceRef: e.SourceRef, Match: e.Match, Mode: "isolated",
		DataClass: "private", MaxPerHour: e.MaxPerHour}
	if msg := probe.validateFields(); msg != "" {
		xbin.WriteError(w, 400, msg)
		return
	}
	host := "user:" + c.user
	var saved *Trigger
	code, msg := 200, ""
	err := agent.db.Tx(func(t *DB) error {
		if code, msg = registryRules(t, c.user, &e); code != 200 {
			return nil
		}
		mine := func(name string) (*Trigger, bool) {
			trs := t.listTriggers(`WHERE name=?`, name)
			if len(trs) == 0 {
				return nil, true
			}
			return trs[0], trs[0].Owner == c.user && t.triggerHost(trs[0].ID) == host
		}
		cur, ok := mine(e.Name)
		if !ok {
			code, msg = 409, "a trigger with that name exists"
			return nil
		}
		if e.Prev != "" && e.Prev != e.Name {
			if prev, ok := mine(e.Prev); ok && prev != nil {
				if cur != nil {
					code, msg = 409, "a trigger with that name exists"
					return nil
				}
				cur = prev
			}
		}
		tr := Trigger{Name: e.Name, Source: e.Source, SourceRef: e.SourceRef, Match: e.Match, Mode: "isolated", Toolset: "private",
			DataClass: "private", Owner: c.user, Visibility: visPrivate, Enabled: e.Enabled, MaxPerHour: probe.MaxPerHour}
		if cur != nil {
			tr.ID = cur.ID
		}
		if err := t.saveTrigger(&tr); err != nil {
			return err
		}
		if _, err := t.q.Exec(`UPDATE triggers SET host=?, owner=?, goal='' WHERE id=?`, host, c.user, tr.ID); err != nil {
			return err
		}
		saved = &tr
		return nil
	})
	switch {
	case err != nil:
		xbin.WriteError(w, 500, err.Error())
	case code != 200:
		xbin.WriteError(w, code, msg)
	default:
		emitAutomation("trigger", saved.ID)
		xbin.WriteJSON(w, 200, map[string]any{"id": saved.ID, "name": saved.Name, "host": host})
	}
}

// registryRules are the rules a private push trigger meets (S10): a topic
// prefix, and none that overlaps another owner's on the same source.
func registryRules(t *DB, user string, e *regEntry) (int, string) {
	if e.Source != "push" {
		return 200, ""
	}
	if e.Match == "" {
		return 400, "a private trigger on pushes needs a topic prefix (match): an empty one would take every push from " +
			e.SourceRef + ", everyone's included"
	}
	for _, o := range t.listTriggers(`WHERE source='push' AND source_ref=?`, e.SourceRef) {
		if o.Owner == user || o.Name == e.Name || (e.Prev != "" && o.Name == e.Prev && o.Owner == user) {
			continue
		}
		if strings.HasPrefix(e.Match, o.Match) || strings.HasPrefix(o.Match, e.Match) {
			return 409, "its topics overlap a trigger someone else has on " + e.SourceRef +
				" — a private trigger takes topics nobody else's does: choose a longer or different prefix"
		}
	}
	return 200, ""
}

// handleRegistryDelete removes the calling person's registry row:
// DELETE /triggers/registry/{name}.
func handleRegistryDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := registrant(w, r)
	if !ok {
		return
	}
	var id int64
	if err := agent.db.q.QueryRow(`SELECT id FROM triggers WHERE name=? AND owner=? AND host=?`, r.PathValue("name"), c.user, "user:"+c.user).
		Scan(&id); err != nil {
		xbin.WriteError(w, 404, "no such trigger of yours")
		return
	}
	_, _ = agent.db.q.Exec(`DELETE FROM triggers WHERE id=?`, id)
	_, _ = agent.db.q.Exec(`DELETE FROM trigger_events WHERE trigger_id=?`, id)
	emitAutomation("trigger", id)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// registrant: the registry is global's, and a row is written only by its
// person from their own partition.
func registrant(w http.ResponseWriter, r *http.Request) (who, bool) {
	c := callerOf(r)
	switch {
	case !globalMode():
		xbin.WriteError(w, 404, "only a partitioned agent's global instance keeps the trigger registry")
	case c.kind != whoUser || c.viewedBy != "" || !personFromPartition(r):
		xbin.WriteError(w, 403, "a person registers their own trigger, from their own partition")
	default:
		return c, true
	}
	return c, false
}

// hostedEditRefused (handleUpdateTrigger, at global): a registry row is
// switched on or off here; everything else about it is edited in its
// person's own partition.
func hostedEditRefused(w http.ResponseWriter, tr *Trigger, patch map[string]json.RawMessage) bool {
	if !hostedHere(agent.db, tr) {
		return false
	}
	if _, on := patch["enabled"]; on && len(patch) == 1 {
		return false
	}
	xbin.WriteError(w, 409, "this trigger runs in "+tr.Owner+"'s own space: it is changed there (here it can only be switched on or off)")
	return true
}

// --- global: a push for a registry row -------------------------------------------------

// handEvent is fireTrigger's step for a registry row (in its transaction,
// after the dedupe): the switch, the halt and the hourly cap are global's;
// the event is queued for the person's partition, which runs it.
func (ag *Agent) handEvent(t *DB, tr *Trigger, host string, ev trigEvent, v *trigVerdict, refuse func(string) error) error {
	switch {
	case !tr.Enabled:
		return refuse("disabled")
	case t.getSetting("halt") == "1":
		return refuse("halted")
	}
	var recent int
	_ = t.q.QueryRow(`SELECT count(*) FROM trigger_events WHERE trigger_id=? AND accepted=1 AND created>?`, tr.ID, now()-3600).Scan(&recent)
	if recent >= tr.MaxPerHour {
		return refuse("rate")
	}
	id := newHandoffID()
	e := eventHandoff{Handoff: id, Trigger: tr.Name, Source: tr.Source, SourceRef: tr.SourceRef,
		EventID: strings.TrimPrefix(ev.ID, ev.Source+":"), Topic: ev.Topic, Text: ev.Text, Data: ev.Data, DataClass: ev.Class}
	if err := t.queueHandoff("event", strings.TrimPrefix(host, "user:"), id, e, func(q *handoffRow) {
		q.trigger, q.source = tr.ID, tr.SourceRef
	}); err != nil {
		return err
	}
	_, _ = t.q.Exec(`UPDATE trigger_events SET accepted=1, reason='handed-off' WHERE trigger_id=? AND event_id=?`, tr.ID, clip(ev.ID, 200))
	_, _ = t.q.Exec(`UPDATE triggers SET last_event=? WHERE id=?`, now(), tr.ID)
	v.Accepted = true
	t.AfterCommit(kickHandoffs)
	return nil
}

// --- a person's partition: registering its triggers ----------------------------------------

// registerPrivate writes tr's registry row at global (in a person's
// partition; nothing elsewhere): false when global refused it or didn't
// answer — the reason is written to w.
func registerPrivate(w http.ResponseWriter, r *http.Request, tr *Trigger, prev string) bool {
	if !userMode() {
		return true
	}
	if prev != tr.Name { // a name this partition has already: the registry row is that trigger's
		var n int
		_ = agent.db.q.QueryRow(`SELECT count(*) FROM triggers WHERE name=?`, tr.Name).Scan(&n)
		if n > 0 {
			xbin.WriteError(w, http.StatusConflict, "a trigger with that name exists")
			return false
		}
	}
	e := regEntry{Name: tr.Name, Source: tr.Source, SourceRef: tr.SourceRef, Match: tr.Match, Enabled: tr.Enabled, MaxPerHour: tr.MaxPerHour}
	if prev != tr.Name {
		e.Prev = prev
	}
	body, _ := json.Marshal(e)
	return registryCall(w, r, http.MethodPost, "/triggers/registry", body)
}

// unregisterPrivate removes tr's registry row (a person's partition): false
// when global didn't answer — the trigger stays, so the two halves agree.
func unregisterPrivate(w http.ResponseWriter, r *http.Request, tr *Trigger) bool {
	if !userMode() {
		return true
	}
	return registryCall(w, r, http.MethodDelete, "/triggers/registry/"+url.PathEscape(tr.Name), nil)
}

func registryCall(w http.ResponseWriter, r *http.Request, method, path string, body []byte) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := callGlobal(ctx, method, path, body, "application/json")
	switch {
	case err != nil:
		xbin.WriteError(w, http.StatusBadGateway, "the agent's shared instance, which keeps the trigger registry, didn't answer: "+err.Error())
		return false
	case method == http.MethodDelete && res.Status == http.StatusNotFound:
		return true // gone there already (a manager deleted it)
	case res.Status/100 != 2:
		if res.Type != "" {
			w.Header().Set("Content-Type", res.Type)
		}
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		return false
	}
	return true
}

// seedTriggerIDs: a person's partition numbers its triggers from 2^40, like
// its conversations (seedPartitionIDs), so the registry rows a manager's
// own partition lists from the global instance (below 2^40) never share an
// id with its own. Idempotent.
func (d *DB) seedTriggerIDs() error {
	if _, err := d.q.Exec(`INSERT INTO sqlite_sequence (name, seq) SELECT 'triggers', ?
		WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='triggers')`, partitionIDBase-1); err != nil {
		return err
	}
	_, err := d.q.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='triggers' AND seq < ?`, partitionIDBase-1, partitionIDBase-1)
	return err
}

// globalTriggerOversight: on a manager's page in their own partition, the
// other people's registry rows the global instance keeps (oversight: that
// one exists, whose, what it listens to — never what it does). Nothing for
// anyone else, or when global doesn't answer within a few seconds.
func globalTriggerOversight(w who) []AutomationItem {
	if !userMode() || !w.manager() || w.viewedBy != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := callGlobal(ctx, http.MethodGet, "/automations", nil, "")
	if err != nil || res.Status != http.StatusOK {
		return nil
	}
	var all struct{ Items []AutomationItem }
	_ = json.Unmarshal(res.Body, &all)
	var out []AutomationItem
	for _, it := range all.Items {
		if it.Kind == "trigger" && it.Access == "oversee" && it.ID < partitionIDBase {
			out = append(out, it)
		}
	}
	return out
}

// forwardGlobalTrigger: in a person's partition a trigger id below 2^40 is
// a registry row at the global instance (a manager switching one off or
// deleting it from their own page): the call is forwarded there, attributed
// to them. true: answered.
func forwardGlobalTrigger(w http.ResponseWriter, r *http.Request) bool {
	if !userMode() || pathID(r) >= partitionIDBase {
		return false
	}
	if body, ok := forwardBody(w, r); ok {
		relay(w, r, body)
	}
	return true
}

// deliverInPartition is deliverOK in a person's partition: a channel
// session in this db is the person's own (their linked DM) — a trigger may
// announce into it.
func deliverInPartition(key string) string {
	var n int
	_ = agent.db.q.QueryRow(`SELECT count(*) FROM sessions WHERE key=? AND origin='channel' AND owner=?`, key, runUser).Scan(&n)
	if n == 0 {
		return "you can only announce into your own chats (a DM from your linked chat account)"
	}
	return ""
}
