// harness_unattended.go — the modes coding agents start in (API.md §Coding
// agents, "Modes"):
//
//   - a person's "auto" (their setting for a coding agent, auto unless they
//     chose approve): the provider's own Auto mode where it has one (Claude
//     Code's auto: it decides most asks itself and asks now and then), else
//     its auto-edit mode — autoModeOf;
//   - a project task's coding agent starts on its own, whatever the task or
//     its person asked: no one watches a task's turn. By default
//     (policy.taskPermissions "ask") it starts in the provider's own Auto
//     mode where it has one (Claude Code's auto), else in its one
//     full-access mode (Codex's agent-full-access, Gemini's yolo), else as
//     it would; a permission request that comes all the same parks the turn,
//     and Needs you lists the task as action required (needAction,
//     conversations.go). A project whose owner set taskPermissions "allow"
//     — allow without asking — starts it in the full-access mode for every
//     provider (Claude Code's bypassPermissions), and a request that comes
//     all the same (an adapter that won't take that mode — Claude Code as
//     root — or one with none, OpenCode) is allowed at once, a note in its
//     conversation saying what was. Either way it works in the project's
//     sandbox; the agent pushes no default branch for it (the task's branch
//     is the prompt's rule; the repo's branch protection is the hard stop).
//
// Both are decided when the adapter (re)starts (newHsess): a changed policy
// reaches a running task at its next (re)start.
package main

import (
	"fmt"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// Project policy taskPermissions: what a permission request a task's coding
// agent still makes does.
const (
	taskPermAsk   = "ask"   // it parks: action required (the default)
	taskPermAllow = "allow" // allow without asking: full access, allowed at once with a note
)

// ownAutoOf is p's own Auto mode — a mode "auto" that isn't explicit
// (Claude Code's) — or "".
func ownAutoOf(p acp.Provider) string {
	for _, m := range p.Modes {
		if m.ID == "auto" && !m.Explicit {
			return m.ID
		}
	}
	return ""
}

// autoModeOf is a person's "auto" for p ("" when p has none).
func autoModeOf(p acp.Provider) string {
	if m := ownAutoOf(p); m != "" {
		return m
	}
	return p.AutoMode
}

// fullAccessOf is p's one explicit (full-access) mode, else its auto.
func fullAccessOf(p acp.Provider) string {
	var full []string
	for _, m := range p.Modes {
		if m.Explicit {
			full = append(full, m.ID)
		}
	}
	if len(full) == 1 {
		return full[0]
	}
	return autoModeOf(p)
}

// taskModeOf is the mode a project task's coding agent starts in: with
// allow (allow without asking) p's full-access mode; else p's own Auto
// where it has one, else its full-access mode.
func taskModeOf(p acp.Provider, allow bool) string {
	if !allow {
		if m := ownAutoOf(p); m != "" {
			return m
		}
	}
	return fullAccessOf(p)
}

// taskMode is taskModeOf (a var: a test has a task start in a mode that
// still asks, as an adapter that won't take full access does).
var taskMode = taskModeOf

// startMode is the mode s's adapter (re)starts in: a project task's own,
// else the conversation's.
func (s *hsess) startMode(asked string) string {
	if s.task {
		if m := taskMode(s.prov, s.allowAll); m != "" {
			return m
		}
	}
	return asked
}

// isProjectTask: run is a project task's conversation.
func (d *DB) isProjectTask(run *Run) bool {
	return run != nil && run.Origin == originProject && d.taskByRun(run.ID) != nil
}

// taskPermissionsOf: whether run is a project task's conversation, and
// whether its project lets its coding agent go ahead without asking
// (policy.taskPermissions "allow").
func (d *DB) taskPermissionsOf(run *Run) (task, allow bool) {
	if !d.isProjectTask(run) {
		return false, false
	}
	p, err := d.getProject(run.OriginID)
	return true, err == nil && policyOf(p.Policy).TaskPermissions == taskPermAllow
}

// allowOption is the option that lets a request go ahead — allow always
// before allow once (the same request won't ask again) — or nil.
func allowOption(opts []hOption) *hOption {
	var once *hOption
	for i := range opts {
		switch opts[i].Kind {
		case "allow_always":
			return &opts[i]
		case "allow_once":
			if once == nil {
				once = &opts[i]
			}
		}
	}
	return once
}

// autoAllow answers park — a permission request of a project task whose
// project allows without asking — with its allow option, and says so in
// the conversation. False when it may not or offers none (it parks as
// ever: action required). Called by the consumer while it applies the
// request: the note and the answer go after (a durable step waits for the
// event to be applied, applyMu), on their own goroutine.
func (s *hsess) autoAllow(park *hPark) bool {
	o := allowOption(park.Options)
	if !s.task || !s.allowAll || o == nil || park.PID == "" {
		return false
	}
	what := "a request"
	if park.Tool != nil {
		what = orStr(park.Tool.Command, orStr(park.Tool.Title, park.Tool.Kind))
	}
	text := fmt.Sprintf("allowed without asking (the project allows its tasks to go ahead without asking): %s — %s", clip(what, 300), o.Name)
	a := hAnswer{Kind: "approval", Park: park, Option: o.OptionID, By: "unattended"}
	go func() {
		_ = s.commit(nil, func(t *DB, _ *harnessSession) error {
			s.e.emitStep(t, s.root, t.journal(s.run, "note", map[string]string{"text": text}))
			return nil
		})
		s.answer(a)
	}()
	return true
}
