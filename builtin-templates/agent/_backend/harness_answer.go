// harness_answer.go — answering a coding agent that waits for a person
// (D-harness §3.5, §4.2.5, §4.2.9): a permission with one of its own
// options (POST /runs/{id}/approve {option?, feedback?} → an approve row),
// a question with the form's values (an hanswer row), and a message sent
// while either is parked, which rejects (declines) it first — the built-in
// agent's rule. The pass answers; the resolution event clears the park
// (harness_park.go).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/acp"
)

// approveHarness is POST /runs/{id}/approve on a coding agent's park: the
// verdict queued, then — with a rejection's feedback — the words as the
// caller's next message.
func approveHarness(w http.ResponseWriter, c who, run *Run, p pendingState, approve bool, option, feedback string) {
	verdict, cerr := harnessVerdict(c, run, p, approve, option, feedback)
	if cerr != nil {
		writeClassErr(w, cerr)
		return
	}
	if _, _, err := agent.queue(run.ID, inboxApprove, verdict, ""); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if fb := strings.TrimSpace(feedback); fb != "" {
		if _, _, err := agent.queue(run.ID, inboxHPrompt, inboxBody{Text: fb, Source: "human", Sender: c.user}, ""); err != nil {
			xbin.WriteError(w, 500, err.Error())
			return
		}
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// harnessVerdict is the approve row for a harness park (§4.2.9): option
// wins; approve picks the first non-explicit allow_once (else allow_*);
// a denial reject_once, then reject_always, then the cancelled outcome.
// An explicit option (it raises the session to a bypass mode) is the root
// conversation's owner's; feedback goes with a rejection only.
func harnessVerdict(c who, run *Run, p pendingState, approve bool, option, feedback string) (inboxBody, *errClass) {
	name := "the coding agent"
	if cfg, err := agent.db.runConfig(run.ID); err == nil && cfg.Harness != nil {
		name = harnessName(cfg.Harness.Provider)
	}
	opts := p.Harness.Options
	var ids []string
	byID := map[string]*hOption{}
	for i := range opts {
		ids = append(ids, opts[i].OptionID)
		byID[opts[i].OptionID] = &opts[i]
	}
	var pick *hOption
	switch {
	case option != "":
		if pick = byID[option]; pick == nil {
			return inboxBody{}, &errClass{400, "option: one of " + strings.Join(ids, ", ")}
		}
	default:
		pick = byID[pickOption(opts, approve)]
		if pick == nil && approve {
			for _, o := range opts {
				if strings.HasPrefix(o.Kind, "allow") {
					return inboxBody{}, &errClass{400, fmt.Sprintf("option: name one — every allow here raises %s to an explicit mode", name)}
				}
			}
			return inboxBody{}, &errClass{400, "option: one of " + strings.Join(ids, ", ")}
		}
	}
	reject := pick == nil || !strings.HasPrefix(pick.Kind, "allow")
	if strings.TrimSpace(feedback) != "" && !reject {
		return inboxBody{}, &errClass{400, "feedback goes with a rejection"}
	}
	if pick != nil && pick.Explicit {
		root := run
		if run.ParentID != 0 {
			if r, err := agent.db.getRun(rootOf(run)); err == nil {
				root = r
			}
		}
		if !grantOwner(c, root) {
			return inboxBody{}, &errClass{403, fmt.Sprintf("only %s can allow %s", orStr(root.Owner, "the conversation's owner"), pick.Name)}
		}
	}
	v := inboxBody{Approve: !reject, Park: p.Park, Sender: c.tag()}
	if pick != nil {
		v.Option = pick.OptionID
	}
	return v, nil
}

// pickOption is the option approve (or deny) chooses: allow_once, else any
// allow — never one that raises the session to an explicit mode; a denial
// is reject_once, else reject_always; "" = none (the cancelled outcome).
func pickOption(opts []hOption, approve bool) string {
	kinds := []string{acp.RejectOnce, acp.RejectAlways}
	if approve {
		kinds = []string{acp.AllowOnce, acp.AllowAlways}
	}
	for _, k := range kinds {
		for _, o := range opts {
			if o.Kind == k && !o.Explicit {
				return o.OptionID
			}
		}
	}
	return ""
}

// parkRow is the newest of rows answering the park in force of kind (nil:
// none — a verdict queued for an earlier park is never spent on this one).
func parkRow(run *Run, kind string, rows []*InboxRow) (*InboxRow, pendingState) {
	p := parsePending(run.Pending)
	if run.Status != statusWaiting || p.Kind != kind || p.Harness == nil {
		return nil, p
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Body.Park == p.Park {
			return rows[i], p
		}
	}
	return nil, p
}

// harnessApprove answers the parked permission with the option the verdict
// names (a row of an earlier build has none: approve's or deny's pick).
func (e *Engine) harnessApprove(ctx context.Context, run *Run, rows []*InboxRow) {
	v, p := parkRow(run, "approval", rows)
	e.consumeRows(rows)
	if v == nil {
		return
	}
	s, err := e.ensureHarness(ctx, run)
	if err != nil || e.harnessRights(ctx, run, s) != nil {
		return
	}
	opt := v.Body.Option
	if opt == "" {
		opt = pickOption(p.Harness.Options, v.Body.Approve)
	}
	s.answerPermission(p.Harness, opt, "user:"+v.Body.Sender)
}

// harnessAnswer answers the parked question (hanswer rows: POST
// /runs/{id}/harness/answer {park, action, content?}).
func (e *Engine) harnessAnswer(ctx context.Context, run *Run, rows []*InboxRow) {
	v, p := parkRow(run, "question", rows)
	e.consumeRows(rows)
	if v == nil {
		return
	}
	s, err := e.ensureHarness(ctx, run)
	if err != nil || e.harnessRights(ctx, run, s) != nil {
		return
	}
	action := orStr(v.Body.Action, "decline")
	if err := s.c.RespondElicitation(p.Harness.EID, action, v.Body.Content, "user:"+v.Body.Sender); err != nil {
		logf("run #%d: answering the question: %v", run.ID, err)
	}
}

// harnessReplyToPark answers the park in force for a message sent while it
// waits (§3.5): a permission is rejected (reject_once, else reject_always,
// else the cancelled outcome), a question declined; the message is then
// steered or waits for the turn's end. A sign-in park keeps it waiting.
func (e *Engine) harnessReplyToPark(ctx context.Context, run *Run, row *InboxRow) {
	p := parsePending(run.Pending)
	if p.Harness == nil || (p.Kind != "approval" && p.Kind != "question") {
		return
	}
	s, err := e.ensureHarness(ctx, run)
	if err != nil {
		return
	}
	by := "user:" + row.Body.Sender
	if p.Kind == "approval" {
		s.answerPermission(p.Harness, pickOption(p.Harness.Options, false), by)
		return
	}
	if err := s.c.RespondElicitation(p.Harness.EID, "decline", nil, by); err != nil && !errors.Is(err, acp.ErrNoElicitation) {
		logf("run #%d: declining the question: %v", run.ID, err)
	}
}

// answerPermission answers a parked permission with opt ("": the cancelled
// outcome) — once: one already answered (its resolution not applied yet)
// is left alone.
func (s *hsess) answerPermission(h *hPark, opt, by string) {
	var res *acp.Resolution
	if opt != "" {
		r, err := s.perms.Resolve(h.PID, opt, "", by)
		if err != nil {
			return // answered already
		}
		res = r
	} else if res = s.perms.CancelByRPC(json.RawMessage(h.RPCID)); res == nil {
		return
	} else {
		res.By = by
	}
	if err := s.c.RespondPermission(res); err != nil {
		logf("run #%d: answering the permission: %v", s.run, err)
	}
}

// consumeRows marks rows delivered (they caused nothing to write).
func (e *Engine) consumeRows(rows []*InboxRow) {
	if len(rows) == 0 {
		return
	}
	_ = e.fenced(func(t *DB) error {
		for _, r := range rows {
			t.consume(r.ID, 0)
		}
		return nil
	})
	for _, r := range rows {
		e.delivered(r.ID)
	}
}
