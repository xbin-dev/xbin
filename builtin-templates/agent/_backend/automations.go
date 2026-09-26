// automations.go — the non-UI agents in one list (D83): schedules and
// watchers now; channels and triggers register the same way when they land.
// Each automation owns runs (origin, origin_id); the Automations page shows
// an automation's runs and what is unread in them.
package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// AutomationItem is one automation as the page lists it.
type AutomationItem struct {
	Kind       string `json:"kind"`
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Owner      string `json:"owner"`
	Visibility string `json:"visibility"`
	Access     string `json:"access"` // owner | viewer | oversee (a manager's view of someone else's)
	Enabled    bool   `json:"enabled"`
	Summary    string `json:"summary"` // one line: when and what
	Mode       string `json:"mode,omitempty"`
	TargetRun  int64  `json:"targetRun,omitempty"`
	CurrentRun int64  `json:"currentRun,omitempty"` // a thread's run now
	LastRunID  int64  `json:"lastRunId,omitempty"`
	LastRunAt  int64  `json:"lastRunAt,omitempty"`
	LastStatus string `json:"lastStatus,omitempty"`
	Runs       int    `json:"runs"`
	Unread     int    `json:"unread"`
	Attention  int    `json:"attention,omitempty"` // things waiting on the caller: a channel to claim, pairing requests, failed replies
	Config     any    `json:"config,omitempty"`    // the kind's own fields (the detail view)
}

// automationKind is one kind of automation. Origin is the run origin its runs
// carry.
type automationKind struct {
	Kind, Origin string
	List         func(w who) []AutomationItem
	Reset        func(w who, id int64) error // nil: nothing to reset
}

var automationKinds = map[string]automationKind{}

func registerAutomationKind(k automationKind) { automationKinds[k.Kind] = k }

func init() {
	for _, kind := range []string{"schedule", "watcher"} {
		k := kind
		registerAutomationKind(automationKind{Kind: k, Origin: k,
			List: func(w who) []AutomationItem { return scheduleItems(w, k == "watcher") },
			Reset: func(w who, id int64) error {
				s, err := agent.db.getSchedule(id)
				if err != nil || s.Watcher != (k == "watcher") {
					return fmt.Errorf("no such %s", k)
				}
				if s.access(w) < lvOwner {
					return fmt.Errorf("only its owner can start it afresh")
				}
				key := "sched:"
				if s.Watcher {
					key = "watch:"
					agent.db.setScheduleRun(id, 0)
				}
				agent.db.resetSession(key + strconv.FormatInt(id, 10))
				return nil
			}})
	}
}

func scheduleItems(w who, watchers bool) []AutomationItem {
	list, _ := agent.db.listSchedules()
	var out []AutomationItem
	for _, s := range list {
		if s.Watcher != watchers {
			continue
		}
		access := s.access(w)
		it := AutomationItem{Kind: "schedule", ID: s.ID, Name: orStr(s.Name, clip(s.Goal, 60)), Owner: s.Owner, Visibility: s.Visibility,
			Enabled: s.Enabled, Mode: orStr(s.Mode, modeIsolated), TargetRun: s.TargetRun, LastRunID: s.LastRunID,
			LastRunAt: s.LastRun, LastStatus: s.LastStatus,
			Summary: s.Cron + " · " + clip(s.Goal, 120),
			Config:  map[string]any{"cron": s.Cron, "goal": s.Goal, "system": s.System, "toolset": s.Toolset, "createdByRun": s.CreatedByRun}}
		if watchers {
			it.Kind, it.Mode, it.CurrentRun = "watcher", "", s.RunID
		} else if s.Mode == modePersistent {
			it.CurrentRun, _ = agent.db.sessionRun("sched:" + strconv.FormatInt(s.ID, 10))
		}
		switch {
		case access >= lvOwner:
			it.Access = "owner"
		case access >= lvViewer:
			it.Access = "viewer"
		case w.manager() && w.viewedBy == "":
			// oversight: it exists, it runs, whose it is — not what it does
			it.Access, it.Summary, it.Config = "oversee", s.Cron, nil
		default:
			continue
		}
		out = append(out, it)
	}
	return out
}

// automationCounts is, per (origin, origin_id), how many runs the caller may
// see and how many are unread for them.
func automationCounts(w who) map[string][2]int {
	where, args := aclWhere(w)
	epoch := agent.convEpoch()
	q := `SELECT r.origin, r.origin_id, count(*), SUM(CASE WHEN r.activity_ms > max(COALESCE(us.read_ms,0), ?) THEN 1 ELSE 0 END)
		FROM runs r LEFT JOIN run_user_state us ON us.run_id=r.id AND us.user=?
		WHERE r.parent_id=0 AND r.origin NOT IN ('', 'chat', 'api') AND ` + where + ` GROUP BY r.origin, r.origin_id`
	rows, err := agent.db.q.Query(q, append([]any{epoch, w.user}, args...)...)
	out := map[string][2]int{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var origin string
		var id int64
		var n, unread int
		if rows.Scan(&origin, &id, &n, &unread) == nil {
			if w.kind != whoUser {
				unread = 0
			}
			out[origin+":"+strconv.FormatInt(id, 10)] = [2]int{n, unread}
		}
	}
	return out
}

func allAutomations(w who) []AutomationItem {
	counts := automationCounts(w)
	var out []AutomationItem
	for _, k := range automationKinds {
		for _, it := range k.List(w) {
			c := counts[k.Origin+":"+strconv.FormatInt(it.ID, 10)]
			it.Runs, it.Unread = c[0], c[1]
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// handleAutomations lists the automations the caller may see (and, for a
// manager, every automation's existence). ?summary=1 is just the counts the
// sidebar badge needs.
func handleAutomations(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	items := allAutomations(c)
	if r.URL.Query().Get("summary") == "1" {
		unread, failing, attention := 0, 0, 0
		for _, it := range items {
			if it.Access == "owner" || it.Access == "viewer" {
				unread += it.Unread
				if strings.HasPrefix(it.LastStatus, "error") {
					failing++
				}
			}
			if it.Access == "owner" || it.Access == "claim" {
				attention += it.Attention
			}
		}
		xbin.WriteJSON(w, 200, map[string]any{"count": len(items), "unread": unread, "failing": failing, "attention": attention})
		return
	}
	if items == nil {
		items = []AutomationItem{}
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items})
}

func automationFor(w http.ResponseWriter, r *http.Request) (*AutomationItem, automationKind, bool) {
	c := callerOf(r)
	k, ok := automationKinds[r.PathValue("kind")]
	id, _ := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if ok {
		counts := automationCounts(c)
		for _, it := range k.List(c) {
			if it.ID == id {
				cnt := counts[k.Origin+":"+strconv.FormatInt(id, 10)]
				it.Runs, it.Unread = cnt[0], cnt[1]
				return &it, k, true
			}
		}
	}
	xbin.WriteError(w, 404, "no such automation")
	return nil, k, false
}

func handleAutomation(w http.ResponseWriter, r *http.Request) {
	if it, _, ok := automationFor(w, r); ok {
		xbin.WriteJSON(w, 200, it)
	}
}

// handleAutomationRuns pages an automation's runs, newest activity first,
// as conversation rows (unread per caller).
//
//	GET /automations/{kind}/{aid}/runs?cursor=&limit=
func handleAutomationRuns(w http.ResponseWriter, r *http.Request) {
	it, k, ok := automationFor(w, r)
	if !ok {
		return
	}
	c := callerOf(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	where, args := aclWhere(c)
	q := `r WHERE r.parent_id=0 AND r.origin=? AND r.origin_id=? AND ` + where
	args = append([]any{k.Origin, it.ID}, args...)
	if cur := r.URL.Query().Get("cursor"); cur != "" {
		ms, id, ok := parseConvCursor(cur)
		if !ok {
			xbin.WriteError(w, 400, "bad cursor")
			return
		}
		q += ` AND (r.activity_ms<? OR (r.activity_ms=? AND r.id<?))`
		args = append(args, ms, ms, id)
	}
	runs, err := agent.db.queryRuns(q+` ORDER BY r.activity_ms DESC, r.id DESC LIMIT `+strconv.Itoa(limit+1), args...)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	next := ""
	if len(runs) > limit {
		runs = runs[:limit]
		next = fmt.Sprintf("%d.%d", runs[len(runs)-1].ActivityMs, runs[len(runs)-1].ID)
	}
	var ids []int64
	for _, x := range runs {
		ids = append(ids, x.ID)
	}
	states := agent.db.userStates(c.user, ids)
	items := []map[string]any{}
	for _, x := range runs {
		items = append(items, agent.convItem(x, c, states[x.ID]))
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items, "next": next})
}

// handleAutomationRead marks every run of an automation read for the caller.
func handleAutomationRead(w http.ResponseWriter, r *http.Request) {
	it, k, ok := automationFor(w, r)
	if !ok {
		return
	}
	c := callerOf(r)
	if c.kind == whoUser {
		ms := time.Now().UnixMilli()
		where, args := aclWhere(c)
		_, _ = agent.db.q.Exec(`INSERT INTO run_user_state (run_id, user, read_ms)
			SELECT r.id, ?, ? FROM runs r WHERE r.parent_id=0 AND r.origin=? AND r.origin_id=? AND `+where+`
			ON CONFLICT(run_id, user) DO UPDATE SET read_ms=excluded.read_ms`,
			append([]any{c.user, ms, k.Origin, it.ID}, args...)...)
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleAutomationReset starts a thread (a persistent schedule, a watcher)
// afresh: its next firing opens a new run; the old ones stay listed.
func handleAutomationReset(w http.ResponseWriter, r *http.Request) {
	it, k, ok := automationFor(w, r)
	if !ok {
		return
	}
	if k.Reset == nil {
		xbin.WriteError(w, 400, "nothing to reset")
		return
	}
	if err := k.Reset(callerOf(r), it.ID); err != nil {
		xbin.WriteError(w, 403, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}
