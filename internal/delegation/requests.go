package delegation

// requests.go — xbind-verified approvals of a delegate's side effects.
//
// A side effect (a non-GET tile call, a tile agent session create, a tile
// create) passes the delegate gate only when the person approved it, and
// the approval lives here, not in the agent:
//
//   - a REQUEST: the agent says what it wants to do — method, path, body —
//     and xbind stores it, pending, with xbind's own description. The
//     person approves or denies it in their own session. An approved
//     request is a one-shot TICKET, bound to exactly that method, path
//     (with its query) and body: the agent names the request's id on the
//     call, the gate compares, and the ticket is spent.
//   - a STANDING APPROVAL: "this tile for an hour", "always for this tile".
//     A record the person can list and revoke; while it holds, the agent's
//     calls to that tile's API need no ticket. It covers a tile's API only:
//     never a session start or a tile create.
//
// So a credential stolen from the agent's sandbox is read-only: it can't
// approve, and what it can redeem is only what the person already approved,
// once.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/util"
)

// Request states.
const (
	Pending   = "pending"
	Approved  = "approved"
	Denied    = "denied"
	Used      = "used"
	Expired   = "expired"
	Withdrawn = "withdrawn"
)

// How a request is approved.
const (
	GrantOnce   = "once"
	GrantHour   = "hour"
	GrantAlways = "always"
)

// Request kinds.
const (
	KindCall     = "call"     // a tile API call
	KindSession  = "session"  // POST /term/sessions
	KindTemplate = "template" // POST /templates/new
)

// Limits.
const (
	MaxPending     = 20             // pending requests a person holds; the 21st is refused
	MaxBody        = 256 << 10      // a request's body, kept to show and to bind
	PendingTTL     = 24 * time.Hour // a request nobody decided
	TicketTTL      = 15 * time.Minute
	keepDecided    = 24 * time.Hour // decided requests stay listed this long
	MaxApprovals   = 200
	hourApproval   = time.Hour
	maxSummaryLen  = 500
	requestsSchema = 1
)

// Request is one approval request and, once approved, its ticket.
type Request struct {
	ID     string `json:"id"`
	User   string `json:"user"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Method string `json:"method"`
	// Target is the call's path as sent — escaped, with its query — under
	// /api/: what the ticket is bound to.
	Target string `json:"target"`
	Tile   string `json:"tile,omitempty"`
	// Body is the body the call will carry, kept while the request is
	// pending or approved so the person sees what they approve; BodySHA256
	// binds the ticket to it. A body that isn't text is kept out of the
	// listing (BodyShown false) and still bound.
	Body       string `json:"body,omitempty"`
	BodyShown  bool   `json:"bodyShown"`
	BodySize   int    `json:"bodySize"`
	BodySHA256 string `json:"bodySha256"`
	// Summary is the agent's own words, shown as such.
	Summary string `json:"summary,omitempty"`
	// Preview is xbind's description of the call, computed when it was
	// requested (the gate's dry run).
	Preview map[string]any `json:"preview,omitempty"`
	State   string         `json:"state"`
	Grant   string         `json:"grant,omitempty"`
	Created time.Time      `json:"created"`
	Decided time.Time      `json:"decided,omitempty"`
	// Expires: a pending request's, then an approved ticket's, deadline.
	Expires time.Time `json:"expires"`
	// DecidedVia names the credential that decided: "session", "device",
	// "app", or "frame" for the agent's own page under the person's login.
	DecidedVia string `json:"decidedVia,omitempty"`
}

// Approval is one standing approval.
type Approval struct {
	ID      string    `json:"id"`
	User    string    `json:"user"`
	Tile    string    `json:"tile"`
	Until   time.Time `json:"until,omitempty"` // zero: until revoked
	Created time.Time `json:"created"`
	Via     string    `json:"via,omitempty"`
	Request string    `json:"request,omitempty"` // the request it was given on
}

type requestsDoc struct {
	Schema   int       `json:"schema"`
	Requests []Request `json:"requests"`
}

type approvalsDoc struct {
	Schema    int        `json:"schema"`
	Approvals []Approval `json:"approvals"`
}

// Refusals.
var (
	ErrNoRequest    = errors.New("no such request")
	ErrNotPending   = errors.New("the request is no longer pending")
	ErrTooMany      = fmt.Errorf("%d requests are already waiting for this person: they answer or the agent withdraws some first", MaxPending)
	ErrNoTicket     = errors.New("no such ticket: the request doesn't exist, isn't approved, expired or was already used")
	ErrTicketBound  = errors.New("the ticket was approved for another call: its method, path and body must be exactly what the person saw")
	ErrBadGrant     = errors.New(`grant is "once", "hour" or "always"`)
	ErrNoStore      = errors.New("this xbind keeps no delegation state")
	ErrStandingKind = errors.New("a standing approval covers a tile's API: a session start or a tile create is approved each time")
)

// BodyHash is the hex SHA-256 a ticket binds a body by.
func BodyHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// requestsLocked reads user's requests, expiring what ran out and dropping
// what was decided long ago; changed says the file should be rewritten.
func (s *Store) requestsLocked(user string) (doc requestsDoc, changed bool, err error) {
	if _, err := readJSON(s.userFile(user, "requests.json"), &doc); err != nil {
		return requestsDoc{}, false, err
	}
	now := s.now()
	keep := doc.Requests[:0]
	for _, r := range doc.Requests {
		if (r.State == Pending || r.State == Approved) && !now.Before(r.Expires) {
			r.State, r.Body, changed = Expired, "", true
			r.Decided = now.UTC().Truncate(time.Second)
		}
		if r.State != Pending && r.State != Approved && now.Sub(r.Decided) > keepDecided {
			changed = true
			continue
		}
		keep = append(keep, r)
	}
	doc.Requests = keep
	return doc, changed, nil
}

func (s *Store) saveRequestsLocked(user string, doc requestsDoc) error {
	doc.Schema = requestsSchema
	return writeJSON(s.userFile(user, "requests.json"), doc)
}

// NewRequest stores a pending request for r.User: r's Agent, Kind, Method,
// Target, Tile, Summary and Preview, and body as the call's body.
func (s *Store) NewRequest(r Request, body []byte) (Request, error) {
	if s == nil {
		return Request{}, ErrNoStore
	}
	if len(body) > MaxBody {
		return Request{}, fmt.Errorf("the body is %d bytes: an approval shows and binds at most %d", len(body), MaxBody)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, _, err := s.requestsLocked(r.User)
	if err != nil {
		return Request{}, err
	}
	pending := 0
	for _, e := range doc.Requests {
		if e.State == Pending {
			pending++
		}
	}
	if pending >= MaxPending {
		return Request{}, ErrTooMany
	}
	now := s.now().UTC().Truncate(time.Second)
	r.ID, r.State, r.Grant = util.RandomToken(12), Pending, ""
	r.Created, r.Decided, r.Expires, r.DecidedVia = now, time.Time{}, now.Add(PendingTTL), ""
	r.BodySize, r.BodySHA256 = len(body), BodyHash(body)
	r.Body, r.BodyShown = "", false
	if utf8.Valid(body) {
		r.Body, r.BodyShown = string(body), true
	}
	if len(r.Summary) > maxSummaryLen {
		r.Summary = r.Summary[:maxSummaryLen]
	}
	doc.Requests = append(doc.Requests, r)
	return r, s.saveRequestsLocked(r.User, doc)
}

// Requests lists user's requests, oldest first; pendingOnly leaves out the
// decided ones.
func (s *Store) Requests(user string, pendingOnly bool) ([]Request, error) {
	if s == nil {
		return []Request{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, changed, err := s.requestsLocked(user)
	if err != nil {
		return nil, err
	}
	if changed {
		_ = s.saveRequestsLocked(user, doc)
	}
	out := []Request{}
	for _, r := range doc.Requests {
		if !pendingOnly || r.State == Pending {
			out = append(out, r)
		}
	}
	return out, nil
}

// Request is one of user's requests.
func (s *Store) Request(user, id string) (Request, bool) {
	rs, err := s.Requests(user, false)
	if err != nil {
		return Request{}, false
	}
	for _, r := range rs {
		if r.ID == id {
			return r, true
		}
	}
	return Request{}, false
}

// Decide approves (grant "once", "hour" or "always") or denies (grant "")
// user's pending request id, through a credential of kind via. An hour or
// always grant on a tile call also records the standing approval, which is
// returned.
func (s *Store) Decide(user, id, grant, via string) (Request, *Approval, error) {
	if s == nil {
		return Request{}, nil, ErrNoStore
	}
	if grant != "" && grant != GrantOnce && grant != GrantHour && grant != GrantAlways {
		return Request{}, nil, ErrBadGrant
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, _, err := s.requestsLocked(user)
	if err != nil {
		return Request{}, nil, err
	}
	i := slices.IndexFunc(doc.Requests, func(r Request) bool { return r.ID == id })
	if i < 0 {
		return Request{}, nil, ErrNoRequest
	}
	r := &doc.Requests[i]
	if r.State != Pending {
		return *r, nil, ErrNotPending
	}
	if (grant == GrantHour || grant == GrantAlways) && (r.Kind != KindCall || r.Tile == "") {
		return *r, nil, ErrStandingKind
	}
	now := s.now().UTC().Truncate(time.Second)
	r.Decided, r.DecidedVia, r.Grant = now, via, grant
	var ap *Approval
	if grant == "" {
		r.State, r.Body = Denied, ""
	} else {
		r.State, r.Expires = Approved, now.Add(TicketTTL)
		if grant != GrantOnce {
			a, err := s.addApprovalLocked(Approval{User: user, Tile: r.Tile, Via: via, Request: r.ID}, grant)
			if err != nil {
				return Request{}, nil, err
			}
			ap = &a
		}
	}
	out := *r
	return out, ap, s.saveRequestsLocked(user, doc)
}

// Withdraw takes back user's pending or approved request id (the agent no
// longer wants it).
func (s *Store) Withdraw(user, id string) (Request, error) {
	if s == nil {
		return Request{}, ErrNoStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, _, err := s.requestsLocked(user)
	if err != nil {
		return Request{}, err
	}
	i := slices.IndexFunc(doc.Requests, func(r Request) bool { return r.ID == id })
	if i < 0 {
		return Request{}, ErrNoRequest
	}
	r := &doc.Requests[i]
	if r.State != Pending && r.State != Approved {
		return *r, ErrNotPending
	}
	r.State, r.Body, r.Decided = Withdrawn, "", s.now().UTC().Truncate(time.Second)
	out := *r
	return out, s.saveRequestsLocked(user, doc)
}

// Redeem spends user's ticket id for the call (method, target, body): the
// request must be approved and unexpired, and the call exactly the one the
// person approved. A ticket is spent by the attempt that matches it — never
// by one that doesn't, so a wrong guess burns nothing.
func (s *Store) Redeem(user, id, method, target string, body []byte) (Request, error) {
	if s == nil {
		return Request{}, ErrNoTicket
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, _, err := s.requestsLocked(user)
	if err != nil {
		return Request{}, ErrNoTicket
	}
	i := slices.IndexFunc(doc.Requests, func(r Request) bool { return r.ID == id })
	if i < 0 || doc.Requests[i].State != Approved {
		return Request{}, ErrNoTicket
	}
	r := &doc.Requests[i]
	if r.Method != method || r.Target != target || r.BodySize != len(body) || r.BodySHA256 != BodyHash(body) {
		return Request{}, ErrTicketBound
	}
	r.State, r.Body, r.Decided = Used, "", s.now().UTC().Truncate(time.Second)
	out := *r
	if err := s.saveRequestsLocked(user, doc); err != nil {
		return Request{}, err // not recorded as spent: not honoured
	}
	return out, nil
}

// ---- standing approvals ----

func (s *Store) approvalsLocked(user string) (doc approvalsDoc, changed bool, err error) {
	if _, err := readJSON(s.userFile(user, "approvals.json"), &doc); err != nil {
		return approvalsDoc{}, false, err
	}
	now := s.now()
	keep := doc.Approvals[:0]
	for _, a := range doc.Approvals {
		if !a.Until.IsZero() && !now.Before(a.Until) {
			changed = true
			continue
		}
		keep = append(keep, a)
	}
	doc.Approvals = keep
	return doc, changed, nil
}

func (s *Store) addApprovalLocked(a Approval, grant string) (Approval, error) {
	if a.Tile == "" || a.User == "" {
		return Approval{}, errors.New("a standing approval names a tile")
	}
	doc, _, err := s.approvalsLocked(a.User)
	if err != nil {
		return Approval{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	a.ID, a.Created, a.Until = util.RandomToken(12), now, time.Time{}
	switch grant {
	case GrantHour:
		a.Until = now.Add(hourApproval)
	case GrantAlways:
	default:
		return Approval{}, ErrBadGrant
	}
	// one approval a tile: the new one replaces the old
	doc.Approvals = slices.DeleteFunc(doc.Approvals, func(e Approval) bool { return e.Tile == a.Tile })
	if len(doc.Approvals) >= MaxApprovals {
		return Approval{}, fmt.Errorf("%d standing approvals: revoke some first", MaxApprovals)
	}
	doc.Approvals = append(doc.Approvals, a)
	doc.Schema = requestsSchema
	return a, writeJSON(s.userFile(a.User, "approvals.json"), doc)
}

// AddApproval records a standing approval of user for tile: grant "hour" or
// "always".
func (s *Store) AddApproval(user, tile, grant, via string) (Approval, error) {
	if s == nil {
		return Approval{}, ErrNoStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addApprovalLocked(Approval{User: user, Tile: tile, Via: via}, grant)
}

// Approvals lists user's standing approvals that hold now.
func (s *Store) Approvals(user string) ([]Approval, error) {
	if s == nil {
		return []Approval{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, changed, err := s.approvalsLocked(user)
	if err != nil {
		return nil, err
	}
	if changed {
		doc.Schema = requestsSchema
		_ = writeJSON(s.userFile(user, "approvals.json"), doc)
	}
	return append([]Approval{}, doc.Approvals...), nil
}

// RevokeApproval removes user's standing approval id; false when there is
// none.
func (s *Store) RevokeApproval(user, id string) (bool, error) {
	if s == nil {
		return false, ErrNoStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, _, err := s.approvalsLocked(user)
	if err != nil {
		return false, err
	}
	n := len(doc.Approvals)
	doc.Approvals = slices.DeleteFunc(doc.Approvals, func(e Approval) bool { return e.ID == id })
	if len(doc.Approvals) == n {
		return false, nil
	}
	doc.Schema = requestsSchema
	return true, writeJSON(s.userFile(user, "approvals.json"), doc)
}

// Covers reports the standing approval under which user's agent may call
// tile's API now. A store that can't be read covers nothing.
func (s *Store) Covers(user, tile string) (Approval, bool) {
	if s == nil || tile == "" {
		return Approval{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, _, err := s.approvalsLocked(user)
	if err != nil {
		return Approval{}, false
	}
	for _, a := range doc.Approvals {
		if a.Tile == tile {
			return a, true
		}
	}
	return Approval{}, false
}
