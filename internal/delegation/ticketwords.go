package delegation

// ticketwords.go — what a refused ticket says (D250). "The approval no
// longer covered the call" cost the owner's test session four rounds: the
// agent tried a 15-minute expiry, then a brief edit, and both were wrong.
// Every refusal of a ticket now names, in xbind's words, which part of the
// call differed from what the person approved — or which state the
// request is in, and since when — and what to do; the reason code rides
// beside refusal "ticket" in the answer (`reason`).
//
// A start that was approved and made, and failed to open for a reason
// that isn't the agent's, is Failed (MarkFailed): with Failure.Retry its
// person may try it again (Retry), which makes it a ticket once more — no
// new request from the agent.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// The reasons a ticket is refused (RedeemError.Reason; the API's `reason`).
const (
	ReasonMissing   = "missing"   // no such request for this person
	ReasonPending   = "pending"   // not decided yet
	ReasonDenied    = "denied"    // the person denied it
	ReasonUsed      = "used"      // spent already
	ReasonWithdrawn = "withdrawn" // the agent, the person, or xbin took it back
	ReasonExpired   = "expired"   // a pending request's 24 hours, or a ticket's 15 minutes, ran out
	ReasonVoided    = "voided"    // ended unused when the agent was stopped after the approval
	ReasonFailed    = "failed"    // used, and the open failed (the person may try it again)
	ReasonBound     = "bound"     // the call isn't the one approved (method, path, type, body)
	ReasonBrief     = "brief"     // a start's brief isn't the one approved
	ReasonTile      = "tile"      // the call reaches another tile than the one approved
	ReasonChoice    = "choice"    // what the person picked can't open now
	ReasonSession   = "session"   // a message's session isn't as it was approved
)

// RedeemError is a ticket that doesn't allow a call: why, in words fit for
// the agent and the person's card.
type RedeemError struct {
	Reason string
	Msg    string
}

func (e *RedeemError) Error() string { return e.Msg }

// Is keeps the errors of before D250: a ticket that can't be used is
// ErrNoTicket, one approved for another call ErrTicketBound.
func (e *RedeemError) Is(target error) bool {
	switch e.Reason {
	case ReasonBound, ReasonBrief, ReasonTile:
		return target == ErrTicketBound
	case ReasonChoice, ReasonSession:
		return false
	}
	return target == ErrNoTicket
}

// at is a time as a refusal says it: "10:47 UTC" — the agent and the
// person may be anywhere; a day apart, the date too.
func at(t, now time.Time) string {
	t = t.UTC()
	if now.Sub(t) > 20*time.Hour || t.Sub(now) > 20*time.Hour {
		return t.Format("2 Jan 15:04 UTC")
	}
	return t.Format("15:04 UTC")
}

// notApproved is why request r (not in state Approved) is no ticket.
func notApproved(r Request, now time.Time) *RedeemError {
	switch r.State {
	case Pending:
		return &RedeemError{ReasonPending, fmt.Sprintf("request %s still waits for the person's decision (asked %s, it waits until %s): there is nothing to use yet — you are told when they decide", r.ID, at(r.Created, now), at(r.Expires, now))}
	case Denied:
		return &RedeemError{ReasonDenied, fmt.Sprintf("the person denied request %s at %s: nothing runs, and don't ask again unless they say so", r.ID, at(r.Decided, now))}
	case Used:
		return &RedeemError{ReasonUsed, fmt.Sprintf("request %s was used at %s: an approval runs once — ask again for another", r.ID, at(r.Decided, now))}
	case Withdrawn:
		by := "by the agent or the person"
		if r.WithdrawnBy == WithdrawnByXbin {
			by = "by xbin, when the agent was stopped"
		}
		return &RedeemError{ReasonWithdrawn, fmt.Sprintf("request %s was withdrawn at %s, %s: file it again if it is still wanted", r.ID, at(r.Decided, now), by)}
	case Failed:
		why := "it failed"
		retry := ""
		if r.Failure != nil {
			why = r.Failure.Why
			if r.Failure.Retry {
				retry = " — the person can try it again from the card; you are told when they do"
			}
		}
		return &RedeemError{ReasonFailed, fmt.Sprintf("request %s was used and didn't start (%s)%s", r.ID, why, retry)}
	case Expired:
		switch {
		case r.Voided:
			return &RedeemError{ReasonVoided, fmt.Sprintf("the approval of request %s (%s) was voided at %s, unused, when the agent was paused or stopped: file it again", r.ID, at(r.ApprovedAt, now), at(r.Expires, now))}
		case !r.ApprovedAt.IsZero():
			return &RedeemError{ReasonExpired, fmt.Sprintf("the approval of request %s, given at %s, expired at %s: an approval is used within %d minutes — file it again", r.ID, at(r.ApprovedAt, now), at(r.Expires, now), int(TicketTTL/time.Minute))}
		}
		return &RedeemError{ReasonExpired, fmt.Sprintf("request %s expired at %s after %d hours without a decision: file it again if it is still wanted", r.ID, at(r.Expires, now), int(PendingTTL/time.Hour))}
	}
	return &RedeemError{ReasonMissing, fmt.Sprintf("request %s is %s: it is no ticket", r.ID, r.State)}
}

// callDiffers is how call c differs from what request r approved (nil: it
// is that call).
func callDiffers(r Request, c TicketCall) *RedeemError {
	switch {
	case r.Method != c.Method:
		return &RedeemError{ReasonBound, fmt.Sprintf("the call's method changed after approval (approved %s, made %s): make exactly the approved call", r.Method, c.Method)}
	case r.Target != c.Target:
		rp, _, _ := strings.Cut(r.Target, "?")
		cp, _, _ := strings.Cut(c.Target, "?")
		if rp == cp {
			return &RedeemError{ReasonBound, "the call's query changed after approval: make exactly the approved call"}
		}
		return &RedeemError{ReasonBound, fmt.Sprintf("the call's path changed after approval (approved %s, made %s): make exactly the approved call", r.Target, c.Target)}
	case r.ContentType != c.ContentType:
		return &RedeemError{ReasonBound, fmt.Sprintf("the call's Content-Type changed after approval (approved %q, made %q): make exactly the approved call", r.ContentType, c.ContentType)}
	case r.Kind == KindCall && r.Tile != c.Tile:
		return &RedeemError{ReasonTile, fmt.Sprintf("the tile moved: %s now reaches another tile than the one approved (%s) — ask again", r.Path(), r.Tile)}
	case r.BodySize != len(c.Body) || r.BodySHA256 != BodyHash(c.Body):
		return bodyDiffers(r, c.Body)
	}
	return nil
}

// bodyDiffers says which part of a body changed: for a start, its brief
// (the prompt, or the brief xbind writes) or the fields that moved; for a
// message, its text; else the body.
func bodyDiffers(r Request, sent []byte) *RedeemError {
	var was, now map[string]json.RawMessage
	jsonBoth := r.Body != "" && json.Unmarshal([]byte(r.Body), &was) == nil && json.Unmarshal(sent, &now) == nil
	switch {
	case r.Kind == KindSession && jsonBoth:
		if !sameRaw(was["prompt"], now["prompt"]) || !sameRaw(was["brief"], now["brief"]) {
			return &RedeemError{ReasonBrief, "the brief's text changed after approval: file the start again with the current brief (tile_agent_start) — the person approves exactly what is sent"}
		}
		if !sameRaw(was["cwd"], now["cwd"]) {
			return &RedeemError{ReasonTile, "the start names another tile than the one approved: file it again for the tile you mean"}
		}
		return &RedeemError{ReasonBound, fmt.Sprintf("the start changed after approval (%s): file it again", strings.Join(movedKeys(was, now), ", "))}
	case r.Kind == KindSession:
		return &RedeemError{ReasonBrief, "the start's body changed after approval (its brief, or what it names): file the start again"}
	case r.Kind == KindMessage:
		return &RedeemError{ReasonBound, "the message's text changed after approval: send exactly the approved text, or file the new one for approval"}
	}
	return &RedeemError{ReasonBound, "the body changed after approval: make exactly the approved call, or ask again"}
}

func sameRaw(a, b json.RawMessage) bool {
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

func movedKeys(a, b map[string]json.RawMessage) []string {
	var out []string
	for k := range a {
		if !sameRaw(a[k], b[k]) {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// ErrNotRetryable: the request is no start that failed for a reason the
// person may try again.
var ErrNotRetryable = errors.New("only a start that was approved and failed to open for a reason that isn't the agent's can be tried again, within its 24 hours")

// MarkFailed records that user's request id was used and its call failed
// for a reason that isn't the agent's: why in xbind's words; retry: the
// person may try it again (Retry). Only a used request.
func (s *Store) MarkFailed(user, id, why string, retry bool) (Request, error) {
	if s == nil {
		return Request{}, ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.requests(p, user)
	if err != nil {
		return Request{}, err
	}
	i := slices.IndexFunc(doc.Requests, func(r Request) bool { return r.ID == id })
	if i < 0 {
		return Request{}, ErrNoRequest
	}
	r := &doc.Requests[i]
	if r.State != Used {
		return *r, ErrNotPending
	}
	now := s.now().UTC().Truncate(time.Second)
	if len(why) > 1000 {
		why = why[:1000]
	}
	retry = retry && now.Before(r.Created.Add(PendingTTL))
	r.State, r.Failure = Failed, &Failure{At: now, Why: why, Retry: retry}
	out := *r
	return out, s.saveRequests(p, user, doc)
}

// RetryDecision is the person's Try again: what they picked on it (nil:
// as before) and the preview redrawn for it, and whether they ticked the
// sign-in line on it.
type RetryDecision struct {
	Chosen    *StartChoice
	Preview   map[string]any
	UseSignin bool
}

// Retry makes user's failed start id a ticket again, for TicketTTL: the
// person's own Try again. Only while its 24 hours from when it was asked
// last; ErrNotRetryable otherwise.
func (s *Store) Retry(user, id string, d RetryDecision) (Request, error) {
	if s == nil {
		return Request{}, ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.requests(p, user)
	if err != nil {
		return Request{}, err
	}
	i := slices.IndexFunc(doc.Requests, func(r Request) bool { return r.ID == id })
	if i < 0 {
		return Request{}, ErrNoRequest
	}
	r := &doc.Requests[i]
	now := s.now().UTC().Truncate(time.Second)
	if r.State != Failed || r.Failure == nil || !r.Failure.Retry || !now.Before(r.Created.Add(PendingTTL)) {
		return *r, ErrNotRetryable
	}
	r.State, r.Expires, r.ApprovedAt, r.Decided = Approved, now.Add(TicketTTL), now, now
	r.Retries++
	// the person's Try again is a decision of its own (D250 review L3): their picks on it, and their own sign-in
	// tick — never the earlier one's carried over
	if d.Chosen != nil {
		r.Chosen = d.Chosen
	}
	if d.Preview != nil {
		r.Preview = d.Preview
	}
	r.UseSignin = d.UseSignin
	out := *r
	return out, s.saveRequests(p, user, doc)
}
