// harness_unattended.go — the modes coding agents start in (API.md §Coding
// agents, "Modes"):
//
//   - a person's "auto" (their setting for a coding agent, auto unless they
//     chose approve): the provider's own Auto mode where it has one (Claude
//     Code's auto: it decides most asks itself and asks now and then), else
//     its auto-edit mode — autoModeOf;
//   - a project task's coding agent runs unattended, always: no one watches
//     a task's turn, so a question it parks on is one nobody answers. It
//     starts in the provider's full-access mode (Claude Code's
//     bypassPermissions, Codex's agent-full-access, Gemini's yolo) whatever
//     the task asked, and a permission request that comes all the same (an
//     adapter that won't take that mode — Claude Code as root — or one with
//     none, OpenCode) is allowed at once, a note in its conversation saying
//     what was. It works in the project's sandbox, on the task's branch: the
//     agent pushes no default branch for it, whatever its mode.
package main

import (
	"fmt"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// autoModeOf is a person's "auto" for p ("" when p has none).
func autoModeOf(p acp.Provider) string {
	for _, m := range p.Modes {
		if m.ID == "auto" && !m.Explicit {
			return m.ID
		}
	}
	return p.AutoMode
}

// unattendedModeOf is the mode a project task's coding agent starts in: p's
// one explicit (full-access) mode, else its auto.
func unattendedModeOf(p acp.Provider) string {
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

// unattendedMode is unattendedModeOf (a var: a test has a task start in a
// mode that still asks, as an adapter that won't take full access does).
var unattendedMode = unattendedModeOf

// startMode is the mode s's adapter (re)starts in: a project task's
// unattended one, else the conversation's.
func (s *hsess) startMode(asked string) string {
	if s.unattended {
		if m := unattendedMode(s.prov); m != "" {
			return m
		}
	}
	return asked
}

// isProjectTask: run is a project task's conversation (its coding agent
// runs unattended).
func (d *DB) isProjectTask(run *Run) bool {
	return run != nil && run.Origin == originProject && d.taskByRun(run.ID) != nil
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

// autoAllow answers park — a permission request of an unattended session —
// with its allow option, and says so in the conversation. False when it
// offers none (it parks as ever). Called by the consumer while it applies
// the request: the note and the answer go after (a durable step waits for
// the event to be applied, applyMu), on their own goroutine.
func (s *hsess) autoAllow(park *hPark) bool {
	o := allowOption(park.Options)
	if !s.unattended || o == nil || park.PID == "" {
		return false
	}
	what := "a request"
	if park.Tool != nil {
		what = orStr(park.Tool.Command, orStr(park.Tool.Title, park.Tool.Kind))
	}
	text := fmt.Sprintf("allowed without asking (a project task runs unattended): %s — %s", clip(what, 300), o.Name)
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
