package server

// delegatestart.go — a person's agent's tile agent start at the delegate
// gate (D250; docs/protocol/delegation.md §The person's pick):
// what a start's ticket opens — the person's pick, checked whole before the
// ticket is spent — the preview of a start with what may be picked, a start
// that failed for a reason that isn't the agent's, and its person's Try
// again. Split from delegate.go and delegateapi.go (the size budget, D219).

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
)

// ticketStart is the start a ticket authorises (D250): the coding agent,
// model and mode the person picked on the approval (Request.Chosen) — or,
// for an approval that picked nothing (an older client), the ones the
// preview showed; never the body's, which are the agent's suggestion. And
// the saved sign-in the sheet offered ONLY where they ticked it for this
// start (Request.UseSignin; D230): the open then uses that one, or none.
// trustAsked: the sheet carried the trust line too — ticked, the person's
// trust is on record by now; not, the session has no sign-in. A coding
// agent whose catalog names no default mode (OpenCode) is approved, and
// opens, in "its own" mode: no mode is no refusal for it.
func ticketStart(r delegation.Request) (*term.DelegateStart, string) {
	shown, _ := r.Preview["session"].(map[string]any)
	prov, _ := shown["provider"].(string)
	mode, _ := shown["mode"].(string)
	model, _ := shown["model"].(string)
	cont := ""
	if c := r.Chosen; c != nil {
		prov, mode, model, cont = c.Provider, c.Mode, c.Model, c.Continue
	}
	switch {
	case prov == "":
		return nil, "the approval names no coding agent: file the start again"
	case mode == "" && !noDefaultMode(prov):
		return nil, "the approval names no mode for " + prov + ": file the start again"
	}
	net, _ := shown["net"].(string)
	signin := ""
	if r.UseSignin {
		signin, _ = shown["signInId"].(string)
	}
	// a resume opens what its history entry says, and is refused when that is no longer what the card showed
	// (term.OpenAgentWith); any other start opens what the card showed or the person picked
	resume, _ := shown["resume"].(bool)
	return &term.DelegateStart{Approved: true, Picked: !resume, Provider: prov, Mode: mode, Model: model, Net: net, Signin: signin, TrustAsked: shown["signInTrust"] != nil, Continue: cont}, ""
}

// startStill is why an approved start can't open as its person approved
// it, now — before its ticket is spent (D250): the coding agent they
// picked no longer allowed there; the conversation's taint keeping it from
// briefing that coding agent on that model; the session they chose to
// continue ended, moved, or is no longer as they saw it. code is the
// refusal; reason the ticket's.
func (s *Server) startStill(p auth.Principal, r delegation.Request, st *term.DelegateStart, asked sessionAsk) (code, reason, why string) {
	// the agent's own call, wrong whatever its person approved: refused before the ticket is spent (D250 review L3)
	switch {
	case asked.Kind != term.KindAgent:
		return "ticket", delegation.ReasonBound, "a tile agent's start is {cwd, kind: \"agent\", …}: this one isn't — file it again as one"
	case asked.Signin != "":
		return "ticket", delegation.ReasonBound, "a workspace agent doesn't pick a saved sign-in: its person ticks one on the approval — file the start again without it"
	}
	tile := asked.Cwd
	if shown, _ := r.Preview["session"].(map[string]any); shown != nil {
		if t, _ := shown["tile"].(string); t != "" {
			tile = t
		}
	}
	if err := s.agentPolicy().SessionAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: tile, Model: p.AgentModel}, st.Provider); err != nil {
		return "ticket", delegation.ReasonChoice, "the coding agent your person picked, " + st.Provider + ", may no longer run on " + tile + " (" + err.Error() + "): file the start again"
	}
	eff := asked
	eff.Provider, eff.Model, eff.Options = st.Provider, st.Model, nil
	refuse := s.startRefusal
	if st.Continue != "" {
		refuse = s.startScopeRefusal // a follow-up into the session there: its live source is judged below (continuable)
	}
	if code, why := refuse(p, eff); why != "" {
		return code, "", why
	}
	if st.Continue != "" {
		approved, _ := r.Preview["session"].(map[string]any)
		was, _ := approved["continue"].(map[string]any)
		c, why := s.continuable(p, st.Continue, tile)
		switch {
		case why != "":
			return "ticket", delegation.ReasonSession, "the session your person chose to continue can't take it now (" + why + "): file the start again"
		case was == nil || c["provider"] != was["provider"] || c["mode"] != was["mode"]:
			return "ticket", delegation.ReasonSession, fmt.Sprintf("the session your person chose to continue is now %v in mode %q, not %v in mode %q as approved: file the start again", c["provider"], c["mode"], was["provider"], was["mode"])
		case continueSigninStill(c, approved, r.UseSignin) != "":
			// it carries the person's saved sign-in: only a follow-up they ticked that sign-in for goes in (D250 review L1)
			return "ticket", delegation.ReasonSession, continueSigninStill(c, approved, r.UseSignin)
		}
	}
	return "", "", ""
}

// unaskedStart is what a start the person lets their agent make without
// asking opens with (D250: the preference chain, without the agent's
// step): the coding agent and model their choice for the tile or their
// preference names, else what the start names, else the workspace's
// default. differs: the start names a coding agent or a model other than
// the person's own choice for it — asking for something else, which needs
// their approval, as naming a mode always did.
func (s *Server) unaskedStart(p auth.Principal, asked sessionAsk) (provider, model, differs string) {
	pick, why := s.startPick(p, asked.Cwd, startSuggestion{}, nil)
	if why != "" {
		return cmp.Or(asked.Provider, s.startDefaults(p, asked.Cwd).Harness), cmp.Or(asked.Model, asked.Options["model"]), ""
	}
	theirs := func(field string) bool { return pick.From[field] == fromTile || pick.From[field] == fromPreference }
	provider = cmp.Or(asked.Provider, pick.Harness)
	if asked.Provider != "" && theirs("harness") && asked.Provider != pick.Harness {
		return "", "", "your person's choice for " + asked.Cwd + " is " + pick.Harness + ", not " + asked.Provider + ": a start on another coding agent needs their approval"
	}
	model = cmp.Or(asked.Model, asked.Options["model"])
	if provider == pick.Harness {
		if model != "" && theirs("model") && model != pick.Model {
			return "", "", "your person's choice of model for " + provider + " is " + pick.Model + ", not " + model + ": another model needs their approval"
		}
		model = cmp.Or(model, pick.Model)
	}
	return provider, model, ""
}

// startOutcome sends an approved start's answer on (dw held it): as it
// was when the session opened; when it didn't, the request is Failed
// (D250) — xbind's words for why, and whether the person may try it again
// (a fault of xbin's or the coding agent's: 5xx, or a limit that may clear:
// 409) without a new request from the agent — and the agent is told it
// was that, never a lapse or a refusal of its own.
func (s *Server) startOutcome(w http.ResponseWriter, user, ticket string, dw *delegateWriter) {
	req, _ := s.Delegation.Request(user, ticket)
	notAgents := dw.status >= 500 || dw.status == http.StatusConflict || dw.status == http.StatusTooManyRequests
	if dw.status < 400 || !notAgents || !slices.Contains(req.Knows, "retry") {
		// it opened; or the open refused the call for a reason in it (the agent's: its answer says which); or a
		// backend that doesn't know Failed — the open's own answer, the request used, as before D250 (review L3)
		w.WriteHeader(dw.status)
		_, _ = w.Write(dw.hold.Bytes())
		return
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(dw.hold.Bytes(), &e)
	why := cmp.Or(strings.TrimSpace(e.Error), strings.TrimSpace(dw.hold.String()), http.StatusText(dw.status))
	retry := true // (not the agent's doing: the sandbox or the coding agent didn't start, or a limit that may clear)
	if _, err := s.Delegation.MarkFailed(user, ticket, why, retry); err == nil {
		s.delegateEvent(user, "request", ticket)
	}
	then := "it is not the agent's doing; file it again only if the person asks"
	if retry {
		then = "it is not the agent's doing: the person can try it again from the card without a new request, and you are told when it starts"
	}
	WriteJSON(w, dw.status, map[string]any{"error": "approved by the person, but the tile agent didn't start: " + why + " — " + then,
		"refusal": "start-failed", "reason": delegation.ReasonFailed, "retry": retry, "request": ticket, "docs": delegationDocs})
}

// reasoned carries a ticket refusal's reason code to the answer
// (delegateRefuse writes it as `reason`): "brief\x00the brief's text…".
func reasoned(reason, msg string) string {
	if reason == "" {
		return msg
	}
	return reason + "\x00" + msg
}

// sessionPreviewFor is sessionPreview with the person's pick on the
// approval (nil: none yet): the coding agent, model and mode the start
// opens with. A body's provider, model and mode are the agent's
// SUGGESTION, preselected below the person's own choices for the tile and
// their preference (startPick) — and a suggested mode that doesn't ask is
// never preselected.
func (s *Server) sessionPreviewFor(p auth.Principal, body []byte, person *delegation.StartChoice) (map[string]any, string) {
	var b struct {
		Cwd, Kind, Provider, Mode, Net, GPU, Name, Model, Resume, Continue string
		API                                                                *bool
		VM                                                                 bool
		Options                                                            map[string]string
		Prompt                                                             string
	}
	if json.Unmarshal(body, &b) != nil || b.Cwd == "" {
		return nil, "a session start names its tile: {cwd, kind:\"agent\", provider?, …}"
	}
	if len(b.Prompt) > maxStartPrompt {
		return nil, fmt.Sprintf("a start's first prompt is at most %d KiB", maxStartPrompt>>10)
	}
	if _, why := s.startRefusal(p, sessionAskOf(body)); why != "" {
		return nil, why // no approval makes it startable: don't put it in front of the person
	}
	options := map[string]string{}
	for k, v := range b.Options {
		options[k] = v
	}
	if b.Model != "" { // the body's shorthand for options.model
		options["model"] = b.Model
	}
	tile := strings.Trim(b.Cwd, "/")
	// what the request leaves out, xbind fills from the person's choices
	// for the tile, their preferences for sessions their agent starts and
	// the tile's orgs' rules — and shows: the card says the EFFECTIVE values
	var pick startPick
	sug := startSuggestion{Harness: b.Provider, Model: options["model"], Mode: b.Mode}
	if b.Resume == "" {
		var why string
		if pick, why = s.startPick(p, tile, sug, person); why != "" {
			return nil, why
		}
		b.Provider, b.Mode = pick.Harness, pick.Mode
		if pick.Model != "" {
			options["model"] = pick.Model
		} else {
			delete(options, "model")
		}
		if b.Net == "" {
			b.Net = s.startDefaults(p, tile).netFor(b.Provider)
		}
	}
	if s.Term == nil {
		return nil, "terminals are not enabled"
	}
	eff, _, err := s.Term.DelegateEffective(p, term.AgentOpen{Cwd: b.Cwd, Provider: b.Provider, Mode: b.Mode, Resume: b.Resume})
	if err != nil {
		return nil, err.Error()
	}
	if err := s.agentPolicy().SessionAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: eff.Tile, Model: p.AgentModel}, eff.Provider.ID); err != nil {
		return nil, err.Error()
	}
	// signIn: what it runs on as things are; signInId OFFERS the person's saved sign-in (the sheet's own line,
	// unticked; the label is drawn from their list); signInTrust: the trust line that goes with it (signinstrust.go)
	signIn, signInID, trust := s.approvalSignIn(p, eff.Tile, eff.Provider)
	out := map[string]any{
		"signIn": signIn, "signInId": signInID, "signInTrust": trustLine(trust),
		"tile": eff.Tile, "provider": eff.Provider.ID, "providerName": eff.Provider.Name, "mode": eff.Mode,
		// false: this mode lets the tile agent act without asking. The mode
		// alone decides it: a delegate's start is strict (acp.Config.Strict)
		// — the mode is in force or the session ends, and no option but a
		// model or a thinking level is set
		"asks":    eff.Provider.Asks(eff.Mode),
		"options": options, // every setting the start carries; none of them can change the mode
		// what the session will be told first: sent by xbind itself once the
		// session is up, and bound by the ticket with the rest of the body.
		// Nothing the agent sends the session later is approved one by one.
		"prompt": b.Prompt,
		"net":    b.Net, "api": b.API == nil || *b.API, "vm": b.VM, "gpu": b.GPU, "model": options["model"], "name": b.Name, "resume": eff.Resumes,
	}
	if b.Resume == "" {
		// D250: what may be picked, where each part came from, and what the agent suggested
		out["choices"] = map[string]any{"harnesses": pick.Harnesses}
		out["from"] = pick.From
		if sug.any() {
			sg := map[string]string{}
			for k, v := range map[string]string{"harness": sug.Harness, "model": sug.Model, "mode": sug.Mode} {
				if v != "" {
					sg[k] = v
				}
			}
			out["suggested"] = sg
		}
		if pick.ModeNotTaken != "" {
			out["modeNotTaken"] = pick.ModeNotTaken
		}
		if pick.HarnessNotTaken != "" {
			out["harnessNotTaken"] = pick.HarnessNotTaken
		}
		if b.Continue != "" { // the agent's own idle session there: continued unless the person starts a new one
			if c, why := s.continuable(p, b.Continue, eff.Tile); why == "" {
				out["continue"] = c
			}
		}
	}
	return out, ""
}

// apiDelegateRequestRetry — POST /delegate/requests/{id}/retry: the
// person, in their own session — never the agent or its page. A start that
// was approved and failed to open for a reason that isn't the agent's
// (state failed, failure.retry) is a ticket again, for 15 minutes: the
// agent's backend, told by a notice, makes the same start — no new request
// from the agent (D250). 409 for anything else, and while the agent is
// stopped (as Allow).
func (s *Server) apiDelegateRequestRetry(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, true, false, false)
	if !ok {
		return
	}
	// the person's Try again is a decision like an approval (D250 review L3): the picks they made on the sheet
	// that reopened it, validated as theirs by hand; their own sign-in tick, never the earlier one carried over
	var body struct {
		signinTickBody
		Harness, Model, Mode *string
		Knows                []string
		Seen                 *seenBasis
	}
	if r.ContentLength != 0 && DecodeJSON(r, &body) != nil {
		WriteError(w, http.StatusBadRequest, `need {harness?, model?, mode?, signin?: true, trust?: true, shown?, knows?, seen?}`, delegationDocs)
		return
	}
	cur, found := s.Delegation.Request(a.user, r.PathValue("id"))
	if !found {
		WriteError(w, http.StatusNotFound, delegation.ErrNoRequest.Error(), delegationDocs)
		return
	}
	if refusal, msg := s.decideRefusal(a.user, cur.Agent); refusal != "" {
		refuseDecision(w, refusal, msg)
		return
	}
	if cur.State != delegation.Failed || cur.Failure == nil || !cur.Failure.Retry {
		WriteError(w, http.StatusConflict, delegation.ErrNotRetryable.Error(), delegationDocs)
		return
	}
	var chosen *delegation.StartChoice
	var redrawn map[string]any
	if pb := (pickBody{Harness: body.Harness, Model: body.Model, Mode: body.Mode, Knows: body.Knows, Seen: body.Seen, Signin: body.Signin}); pb.picks() {
		var status int
		var why string
		if chosen, redrawn, status, why = s.startPickFor(a, cur, pb); why != "" {
			WriteError(w, status, why, delegationDocs)
			return
		}
	}
	basis, ok := s.approveTicksOn(w, a.user, cur.ID, redrawn, body.Signin, body.Trust, body.Shown)
	if !ok {
		return
	}
	req, err := s.Delegation.Retry(a.user, cur.ID, delegation.RetryDecision{Chosen: chosen, Preview: redrawn, UseSignin: body.Signin})
	if err != nil {
		WriteError(w, http.StatusConflict, err.Error(), delegationDocs)
		return
	}
	s.delegateAudit(delegation.Call{User: a.user, Agent: req.Agent, Method: req.Method, Path: req.Path(), Tile: req.Tile,
		Class: "decision:retry", Effect: true, Status: http.StatusOK, Ticket: req.ID, Under: "once"})
	out := map[string]any{"request": req}
	if body.Trust {
		s.approvalTrust(out, a.user, req, basis) // the trust line, ticked (signinstrust.go)
	}
	s.delegateEvent(a.user, "request", req.ID)
	s.notifyAt(req.Expires, a.user, req.ID, req.Agent)
	WriteJSON(w, http.StatusOK, out)
}
