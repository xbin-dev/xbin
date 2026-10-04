// ci_events.go — scm events keeping CI watches current (scmEventHooks: every
// event the home handles, deduped, before projects route it; API.md §CI in
// the conversation, "Keeping it current"). An event reaches the live
// watches of its provider and repo whose branch, head or subscription key
// it names:
//
//   - progress (workflow, job, check): the entry it carries replaces the
//     snapshot's of the same id (a job's steps as the host last reported
//     them); state and counts are computed again. For another head than the
//     watch's, the watch is read instead (it may have moved).
//   - checks.completed: the watch is read (conditional on its etag).
//   - pull: merged or closed → gone (the watch ends a day later); otherwise
//     its number is kept and the watch read.
//   - push: the branch's new head starts the snapshot over (and is read); a
//     deleted branch → gone.
//
// Events are never acted on for anything else here; the text they carry is
// cleaned as a read's is (ci_store.go).
package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

func init() { scmEventHooks = append(scmEventHooks, ciOnEvent) }

// ciOnEvent (scmEventHooks) applies ev to the watches it names.
func ciOnEvent(t *DB, ev *scmEvent) {
	if ev == nil || !t.features {
		return
	}
	switch ev.Kind {
	case scmKindChecks, scmKindWorkflow, scmKindJob, scmKindCheck, scmKindPull, scmKindPush:
	default:
		return
	}
	for _, w := range ciMatching(t, ev) {
		ciEvented.Store(ciKey{t.sql, w.ID}, nowMs())
		switch ev.Kind {
		case scmKindWorkflow, scmKindJob, scmKindCheck:
			ciProgress(t, w, ev)
		case scmKindChecks:
			ciReadLater(t, w.ID)
		case scmKindPull:
			ciPull(t, w, ev)
		case scmKindPush:
			ciPushEvent(t, w, ev)
		}
	}
}

// ciProvider is a provider's tile path without its instance.
func ciProvider(s string) string { return strings.SplitN(s, "#", 2)[0] }

// ciMatching is the live watches ev names: same provider and repo, and its
// branch, its head, or one of its subscription keys.
func ciMatching(t *DB, ev *scmEvent) []*ciWatch {
	subs := map[int64]bool{}
	for _, k := range ev.Subs {
		if id, err := strconv.ParseInt(strings.TrimPrefix(k, ciSubKeyPref), 10, 64); err == nil && strings.HasPrefix(k, ciSubKeyPref) {
			subs[id] = true
		}
	}
	sha := ciSHA(ev.Ref.SHA)
	var out []*ciWatch
	for _, w := range t.ciWatchesWhere(`WHERE ended_ms=0 AND lower(repo)=lower(?) ORDER BY id`, ev.Repo) {
		if ev.SCM.Provider != "" && ciProvider(w.SCM) != ciProvider(ev.SCM.Provider) {
			continue
		}
		pr := ev.Ref.PR > 0 && w.PR == ev.Ref.PR
		if subs[w.ID] || (ev.Ref.Branch != "" && w.Ref == ev.Ref.Branch) || (sha != "" && w.SHA == sha) || pr {
			out = append(out, w)
		}
	}
	return out
}

// ciProgress patches w's snapshot with a progress event's entry.
func ciProgress(t *DB, w *ciWatch, ev *scmEvent) {
	c := w.checks()
	head := ciSHA(ev.Ref.SHA)
	if c == nil || (head != "" && w.SHA != "" && head != w.SHA) {
		ciReadLater(t, w.ID) // nothing to patch yet, or another head: read what is there now
		return
	}
	switch ev.Kind {
	case scmKindWorkflow:
		var r scmWorkflowRun
		if json.Unmarshal(ev.Data[scmKindWorkflow], &r) != nil || r.ID == "" {
			return
		}
		found := false
		for i := range c.WorkflowRuns {
			if c.WorkflowRuns[i].ID == r.ID {
				r.Jobs = c.WorkflowRuns[i].Jobs // a workflow event carries no jobs
				c.WorkflowRuns[i], found = r, true
			}
		}
		if !found {
			if c.WorkflowRuns == nil {
				c.WorkflowRuns = []scmWorkflowRun{}
			}
			c.WorkflowRuns = append([]scmWorkflowRun{r}, c.WorkflowRuns...)
		}
	case scmKindJob:
		var x struct {
			RunID string `json:"runId"`
			Job   scmJob `json:"job"`
		}
		if json.Unmarshal(ev.Data[scmKindJob], &x) != nil || x.Job.ID == "" {
			return
		}
		placed := false
		for i := range c.WorkflowRuns {
			if c.WorkflowRuns[i].ID != x.RunID {
				continue
			}
			for j := range c.WorkflowRuns[i].Jobs {
				if c.WorkflowRuns[i].Jobs[j].ID == x.Job.ID {
					c.WorkflowRuns[i].Jobs[j], placed = x.Job, true
				}
			}
			if !placed {
				c.WorkflowRuns[i].Jobs, placed = append(c.WorkflowRuns[i].Jobs, x.Job), true
			}
		}
		if !placed {
			ciReadLater(t, w.ID) // a run the snapshot doesn't have yet
			return
		}
	case scmKindCheck:
		var k scmCheck
		if json.Unmarshal(ev.Data[scmKindCheck], &k) != nil || k.ID == "" {
			return
		}
		found := false
		for i := range c.Checks {
			if c.Checks[i].ID == k.ID {
				c.Checks[i], found = k, true
			}
		}
		if !found {
			c.Checks = append(c.Checks, k)
		}
	}
	if w.SHA == "" {
		w.SHA = head
	}
	w.setChecks(c)
	w.UpdatedMs = nowMs()
	if t.ciSave(w) == nil {
		ciChanged(t, w.RootRun, w.ID)
	}
}

// ciPull: a pull request of w's branch moved.
func ciPull(t *DB, w *ciWatch, ev *scmEvent) {
	var x struct {
		Pull struct {
			Number int    `json:"number"`
			State  string `json:"state"`
			Head   scmRef `json:"head"`
		} `json:"pull"`
	}
	_ = json.Unmarshal([]byte(mustJSON(ev.Data)), &x)
	n := orInt(x.Pull.Number, ev.Ref.PR)
	if x.Pull.Head.Ref != "" && x.Pull.Head.Ref != w.Ref {
		return // another branch's pull request (the head's sha matched)
	}
	switch {
	case ev.Action == "merged" || ev.Action == "closed" || x.Pull.State == "merged" || x.Pull.State == "closed":
		if w.PR != 0 && n != 0 && n != w.PR {
			return // another pull request of the branch closed; this one stays
		}
		w.State, w.PR = ciGone, orInt(w.PR, n)
		w.UpdatedMs = nowMs()
		if t.ciSave(w) == nil {
			ciChanged(t, w.RootRun, w.ID)
		}
	default:
		changed := false
		if n > 0 && w.PR != n {
			w.PR, changed = n, true
		}
		if w.State == ciGone {
			w.State, changed = ciNone, true // reopened
		}
		if sha := ciSHA(orStr(x.Pull.Head.SHA, ev.Ref.SHA)); sha != "" && sha != w.SHA {
			w.moveTo(sha)
			changed = true
		}
		if changed {
			w.UpdatedMs = nowMs()
			if t.ciSave(w) == nil {
				ciChanged(t, w.RootRun, w.ID)
			}
		}
		ciReadLater(t, w.ID)
	}
}

// ciPushEvent: w's branch has a new head (or was deleted).
func ciPushEvent(t *DB, w *ciWatch, ev *scmEvent) {
	if ev.Ref.Branch != "" && ev.Ref.Branch != w.Ref {
		return
	}
	var x struct {
		Push struct {
			After   string `json:"after"`
			Deleted bool   `json:"deleted"`
		} `json:"push"`
	}
	_ = json.Unmarshal([]byte(mustJSON(ev.Data)), &x)
	after := ciSHA(orStr(x.Push.After, ev.Ref.SHA))
	if x.Push.Deleted || ev.Action == "deleted" || after == "" || strings.Trim(after, "0") == "" {
		if w.State != ciGone {
			w.State, w.UpdatedMs = ciGone, nowMs()
			if t.ciSave(w) == nil {
				ciChanged(t, w.RootRun, w.ID)
			}
		}
		return
	}
	if after == w.SHA {
		return
	}
	w.moveTo(after)
	w.UpdatedMs = nowMs()
	if t.ciSave(w) == nil {
		ciChanged(t, w.RootRun, w.ID)
	}
	ciReadLater(t, w.ID)
}
