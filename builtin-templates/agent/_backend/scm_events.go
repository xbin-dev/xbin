// scm_events.go — scm events into the agent (API.md §scm events and
// polling; /docs/agent-inbox.md "scm events"; the event v1 is
// /docs/scm.md's): a bound scm provider POSTs each event its subscriptions
// match to POST /adapter/scm/event at the agent's global instance (or an
// unpartitioned agent), with the channel role its `agents` binding gives
// it. The caller must be one of the providers bound in the agent's `scm`
// slot — the caller's own path is the provider, whatever the body says.
//
//   - Transport dedupe: the event's `for` and eventId in scm_seen (7 days) —
//     one event reaches a consumer once per `for`, so the same eventId for
//     two people is two deliveries; a repeat answers 200 and does nothing.
//   - `for: global` is handled here (the global instance, an unpartitioned
//     agent); `for: user:<id>` at a partitioned agent's global instance is
//     handed to that person's partition by partition mail `handoff/scm`
//     (scm_handoff.go), which dedupes again and handles it there;
//     `for: user:<id>` anywhere else is 404 (not ours).
//   - Handling (scmTake, in the home that owns it): scmEventHooks first (the
//     CI view's watches), then — but for the progress kinds (workflow, job,
//     check), which only those hooks use — project routing (scm_router.go).
//
// The tables of scm events and polling are made here: project_refs (what
// routes an event to a task: its branch, its PRs, their heads), scm_poll
// (the reads that stand in for events, scm_poll.go), scm_seen (event ids,
// semantic keys and when a repo last delivered) and scm_subs (the
// subscriptions this home keeps at its providers, scm_subs.go).
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	schemaAdds = append(schemaAdds, addSCMEventSchema)
	adapterRouteTables = append(adapterRouteTables, scmAdapterRoutes)
}

// scmEventMax is the largest event v1 body taken.
const scmEventMax = 1 << 20

// scmSeenTTL is how long scm_seen keeps a key.
const scmSeenTTL = 7 * 24 * time.Hour

const scmEventSchemaSQL = `
CREATE TABLE IF NOT EXISTS project_refs (
  scm        TEXT    NOT NULL DEFAULT '',
  repo       TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL DEFAULT '',
  value      TEXT    NOT NULL DEFAULT '',
  project_id INTEGER NOT NULL DEFAULT 0,
  n          INTEGER NOT NULL DEFAULT 0,
  created    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (scm, repo, kind, value, project_id, n)
);
CREATE INDEX IF NOT EXISTS idx_prefs_match ON project_refs(scm, repo, kind, value);
CREATE INDEX IF NOT EXISTS idx_prefs_task ON project_refs(project_id, n);
CREATE TABLE IF NOT EXISTS scm_poll (
  project_id    INTEGER NOT NULL DEFAULT 0,
  n             INTEGER NOT NULL DEFAULT 0,
  repo          TEXT    NOT NULL DEFAULT '',
  kind          TEXT    NOT NULL DEFAULT '',
  item          TEXT    NOT NULL DEFAULT '{}',
  etag          TEXT    NOT NULL DEFAULT '',
  due_ms        INTEGER NOT NULL DEFAULT 0,
  step          INTEGER NOT NULL DEFAULT 0,
  pending_since INTEGER NOT NULL DEFAULT 0,
  last_ms       INTEGER NOT NULL DEFAULT 0,
  nudge         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, n, repo, kind)
);
CREATE INDEX IF NOT EXISTS idx_scmpoll_due ON scm_poll(due_ms);
CREATE TABLE IF NOT EXISTS scm_seen (
  key TEXT    PRIMARY KEY,
  at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_scmseen_at ON scm_seen(at);
CREATE TABLE IF NOT EXISTS scm_subs (
  key        TEXT    PRIMARY KEY,
  scm        TEXT    NOT NULL DEFAULT '',
  repo       TEXT    NOT NULL DEFAULT '',
  project_id INTEGER NOT NULL DEFAULT 0,
  n          INTEGER NOT NULL DEFAULT 0,
  body       TEXT    NOT NULL DEFAULT '{}',
  sub_id     TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT 'post',
  next_ms    INTEGER NOT NULL DEFAULT 0,
  posted_ms  INTEGER NOT NULL DEFAULT 0,
  expires_ms INTEGER NOT NULL DEFAULT 0,
  tries      INTEGER NOT NULL DEFAULT 0,
  error      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_scmsubs_due ON scm_subs(state, next_ms);
`

// scmEventTables are the tables made here (the migration tests).
var scmEventTables = []string{"project_refs", "scm_poll", "scm_seen", "scm_subs"}

// addSCMEventSchema (schemaAdds) makes E's tables: additive, idempotent.
func addSCMEventSchema(d *DB) error {
	_, err := d.q.Exec(scmEventSchemaSQL)
	return err
}

// scmAdapterRoutes is E's adapterRouteTables entry: the provider's delivery
// route, behind adapterGuard (the channel role).
func scmAdapterRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{"POST /adapter/scm/event": handleSCMEvent}
}

// scmClockAt, when set, is the time scmClock says (the polling tests move
// it; an atomic, since a loop of an earlier test's engine may still read it).
var scmClockAt atomic.Int64

// scmClock is now in unix ms.
func scmClock() int64 {
	if at := scmClockAt.Load(); at != 0 {
		return at
	}
	return nowMs()
}

// --- the caller --------------------------------------------------------------------

// scmEventCaller is the bound provider calling (its scmBound name: the tile
// path, "#" and the deployment of a non-primary one), or why it may not
// deliver events. Only a provider's own backend at its global instance (or
// an unpartitioned provider) delivers: a call acting in one of its people's
// partitions — their frame or terminal there arrives as the provider too —
// or carrying a person is refused, so no person can forge an event for
// someone else's task.
func scmEventCaller(r *http.Request) (string, string) {
	c := xbin.Caller(r)
	name := c.From
	if c.Deployment != "" {
		name += "#" + c.Deployment
	}
	bound := false
	for _, b := range scmBound() {
		bound = bound || b == name
	}
	switch {
	case !bound:
		return "", "only a bound scm provider delivers scm events (bind it in the agent's scm slot)"
	case c.Partition != "" && c.Partition != "global":
		return "", "scm events come from the provider's global instance, not from a person's partition of it"
	case c.User != "" || c.ViewedBy != "":
		return "", "scm events are the provider's own deliveries, never a person's"
	}
	return name, ""
}

// scmPersonID: the id in `for: user:<id>` (xbin's ids: no spaces, no
// slashes).
var scmPersonID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,127}$`)

// --- the route --------------------------------------------------------------------

// handleSCMEvent is POST /adapter/scm/event: an event v1 from a bound
// provider. 200 {taken: true} (a repeat: also duplicate: true); 404 when it
// isn't this instance's to take; 400 a body that isn't an event v1; 403 a
// caller that isn't a bound provider's global instance.
func handleSCMEvent(w http.ResponseWriter, r *http.Request) {
	prov, why := scmEventCaller(r)
	if why != "" {
		xbin.WriteError(w, http.StatusForbidden, why)
		return
	}
	if userMode() {
		xbin.WriteError(w, http.StatusNotFound, "a person's space takes scm events through its agent's global instance")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, scmEventMax)
	var ev scmEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			xbin.WriteError(w, http.StatusRequestEntityTooLarge, "an scm event is at most 1 MiB")
			return
		}
		xbin.WriteError(w, http.StatusBadRequest, "not an scm event v1: "+err.Error())
		return
	}
	if ev.Protocol != scmProtocol {
		xbin.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "this agent speaks scm protocol 1",
			"refusal": scmRefProtocol, "protocols": []int{scmProtocol}})
		return
	}
	if ev.EventID == "" || ev.Kind == "" || len(ev.EventID) > 300 {
		xbin.WriteError(w, http.StatusBadRequest, "an scm event needs its eventId and kind")
		return
	}
	ev.SCM.Provider = prov // the caller is the provider, whatever the body says
	person, toPerson := strings.CutPrefix(ev.For, "user:")
	switch {
	case ev.For == "global":
	case toPerson && scmPersonID.MatchString(person) && globalMode():
	case toPerson && scmPersonID.MatchString(person):
		scmDrop("not-ours")
		xbin.WriteError(w, http.StatusNotFound, "no such subscriber here: this agent keeps no people's partitions")
		return
	default:
		xbin.WriteError(w, http.StatusBadRequest, "an scm event's for is global or user:<id>")
		return
	}
	ag := projAg()
	if ag == nil || ag.db == nil || !ag.db.features {
		xbin.WriteError(w, http.StatusServiceUnavailable, "the agent is starting")
		return
	}
	dup := false
	err := ag.db.Tx(func(t *DB) error {
		// keyed per recipient: the provider delivers one event once per
		// `for` with the same eventId (a shared repo's subscribers each get
		// it), so only a repeat to the same `for` is a duplicate here; a
		// person's partition dedupes on the eventId alone (it sees only its
		// own `for`)
		if !scmSeenOnce(t, scmTransportKey(&ev)) {
			dup = true
			return nil
		}
		if toPerson {
			return scmHandOn(t, person, &ev) // scm_handoff.go
		}
		return scmTake(t, &ev)
	})
	if err != nil {
		logf("scm event %s from %s: %v", ev.EventID, prov, err)
		xbin.WriteError(w, http.StatusInternalServerError, "the event wasn't taken; deliver it again")
		return
	}
	out := map[string]any{"taken": true}
	if dup {
		out["duplicate"] = true
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}

// --- handling ---------------------------------------------------------------------

// scmTake handles an event in the home that owns it (in t): the time it
// arrived for its repo (and sha), scmEventHooks, then — but for progress
// events — the projects' routing.
func scmTake(t *DB, ev *scmEvent) error {
	scmNoteDelivery(t, ev)
	for _, h := range scmEventHooks {
		h(t, ev)
	}
	if scmProgress(ev.Kind) {
		return nil
	}
	return scmRoute(t, ev) // scm_router.go
}

// scmProgress: a progress kind — shown, never acted on alone.
func scmProgress(kind string) bool {
	return kind == scmKindWorkflow || kind == scmKindJob || kind == scmKindCheck
}

// --- scm_seen ------------------------------------------------------------------------

// scmTransportKey is the scm_seen key of a delivery where it arrives: its
// `for` and its eventId.
func scmTransportKey(ev *scmEvent) string { return ev.For + "|" + ev.EventID }

// scmSeenOnce records key (an eventId or a semantic key) and says whether
// it is new: false — seen within scmSeenTTL — means do nothing.
func scmSeenOnce(t *DB, key string) bool {
	now := scmClock()
	res, err := t.q.Exec(`INSERT INTO scm_seen (key, at) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET at=excluded.at
		WHERE scm_seen.at < ?`, key, now, now-scmSeenTTL.Milliseconds())
	return err == nil && rowsAffected(res) == 1
}

// scmSeen: key was recorded within scmSeenTTL (read only).
func scmSeen(d *DB, key string) bool {
	var at int64
	return d.q.QueryRow(`SELECT at FROM scm_seen WHERE key=?`, key).Scan(&at) == nil && at >= scmClock()-scmSeenTTL.Milliseconds()
}

// scmSeenAt is when key was last recorded (0: never).
func scmSeenAt(d *DB, key string) int64 {
	var at int64
	_ = d.q.QueryRow(`SELECT at FROM scm_seen WHERE key=?`, key).Scan(&at)
	return at
}

// scmDeliveryKey is the scm_seen key of the last delivery for a repo (sha
// "") or for one of its commits.
func scmDeliveryKey(scm, repo, sha string) string {
	k := "dlv:" + scm + "|" + strings.ToLower(repo)
	if sha != "" {
		k += "@" + strings.ToLower(sha)
	}
	return k
}

// scmNoteDelivery records that an event for ev's repo (and its sha) came:
// polling eases off while deliveries arrive (scm_poll.go).
func scmNoteDelivery(t *DB, ev *scmEvent) {
	if ev.Repo == "" {
		return
	}
	now := scmClock()
	keys := []string{scmDeliveryKey(ev.SCM.Provider, ev.Repo, "")}
	if ev.Ref.SHA != "" {
		keys = append(keys, scmDeliveryKey(ev.SCM.Provider, ev.Repo, ev.Ref.SHA))
	}
	for _, k := range keys {
		_, _ = t.q.Exec(`INSERT INTO scm_seen (key, at) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET at=excluded.at`, k, now)
	}
}

// scmPruneSeen forgets what scm_seen holds past its 7 days.
func scmPruneSeen(d *DB) {
	_, _ = d.q.Exec(`DELETE FROM scm_seen WHERE at < ?`, scmClock()-scmSeenTTL.Milliseconds())
}

// --- counters -------------------------------------------------------------------------

// scmDrops counts the events dropped, by reason (not-ours, for-pid,
// not-bound, malformed…): logged, and read by the tests.
var scmDrops = struct {
	sync.Mutex
	n map[string]int
}{n: map[string]int{}}

func scmDrop(reason string) {
	scmDrops.Lock()
	scmDrops.n[reason]++
	scmDrops.Unlock()
}

func scmDropped(reason string) int {
	scmDrops.Lock()
	defer scmDrops.Unlock()
	return scmDrops.n[reason]
}
