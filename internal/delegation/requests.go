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
//   - a STANDING APPROVAL: a record the person can list and revoke, for an
//     hour or until revoked. By default it is of ONE CALL — this method on
//     this path of the tile, whatever the body ("this call, always");
//     "every write to this tile" is a separate, wider choice the person
//     makes explicitly. It covers a tile's API only: never a session start
//     or a tile create.
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
	"strings"
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
	KindWeb      = "web"      // POST /delegate/web/fetch: a URL the conversation may read (D239)
	KindMCP      = "mcp"      // POST /delegate/mcp/call: a tool of an MCP server (D239)
)

// Standing approval scopes.
const (
	// ScopeCall: one call — this method on this path of the tile ("this
	// call, always"). The default.
	ScopeCall = "call"
	// ScopeTile: every write to the tile's API. The wider choice, made
	// explicitly.
	ScopeTile = "tile"
	// ScopeTool: one tool of an MCP tile — "always for this tool", any
	// arguments (D239). Method is MCPMethod and Path the tool's name.
	ScopeTool = "tool"
)

// MCPMethod is the method a standing approval of an MCP tool names: never
// an HTTP method, so it covers no call of the tile's API.
const MCPMethod = "MCP"

// Limits.
const (
	MaxPending     = 20             // pending requests a person holds; the 21st is refused
	MaxRequests    = 200            // requests kept for a person, decided ones included: the oldest decided go first
	MaxBody        = 256 << 10      // a request's body, kept to show and to bind
	MaxTarget      = 2048           // a request's path with its query
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
	// ContentType is the media type the call's body will be sent as ("" for
	// none): bound like the body — the same bytes under another type are
	// another call to the tile that parses them.
	ContentType string `json:"contentType,omitempty"`
	// Body is the body the call will carry, kept while the request is
	// pending or approved so the person sees what they approve; BodySHA256
	// binds the ticket to it. A body that isn't text is kept out of the
	// listing (BodyShown false) and still bound.
	Body      string `json:"body,omitempty"`
	BodyShown bool   `json:"bodyShown"`
	// BodyBinary: the call carries a body that can't be shown as text (not
	// UTF-8, or with control characters): the person sees its size and
	// hash, and approves it only by saying they approve it unseen —
	// ApprovedUnseen records that they did.
	BodyBinary     bool   `json:"bodyBinary,omitempty"`
	ApprovedUnseen bool   `json:"approvedUnseen,omitempty"`
	BodySize       int    `json:"bodySize"`
	BodySHA256     string `json:"bodySha256"`
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
	// "app" ("dev" on an xbind run with --no-auth).
	DecidedVia string `json:"decidedVia,omitempty"`
	// WithdrawnBy is "xbin" for a request xbind withdrew itself because its
	// person's agent stopped for good (WithdrawPending); "" when the agent
	// or the person took it back.
	WithdrawnBy string `json:"withdrawnBy,omitempty"`
}

// WithdrawnByXbin marks a request or change set xbind withdrew because the
// person's agent stopped for good: workspace agents switched off, the
// person no longer given an agent, disabled or deleted.
const WithdrawnByXbin = "xbin"

// Path is the request's path without its query: what a standing approval
// of "this call" is for.
func (r Request) Path() string {
	p, _, _ := strings.Cut(r.Target, "?")
	return p
}

// Approval is one standing approval: scope "call" lets the agent make
// Method on Path of Tile without a ticket; scope "tile" every write to
// Tile's API.
type Approval struct {
	ID      string    `json:"id"`
	User    string    `json:"user"`
	Tile    string    `json:"tile"`
	Scope   string    `json:"scope"`
	Method  string    `json:"method,omitempty"` // scope call
	Path    string    `json:"path,omitempty"`   // scope call: the path under /api/, no query
	Until   time.Time `json:"until,omitempty"`  // zero: until revoked
	Created time.Time `json:"created"`
	Via     string    `json:"via,omitempty"`
	Request string    `json:"request,omitempty"` // the request it was given on
	// Pin: scope tool — the tool's definition the person allowed
	// (toolpins.go); a served definition that differs isn't covered
	Pin *ToolPin `json:"pin,omitempty"`
}

// covers reports whether a allows method on path of tile.
func (a Approval) covers(tile, method, path string) bool {
	if a.Tile != tile {
		return false
	}
	switch a.Scope {
	case ScopeTile:
		return true
	case ScopeCall: // a method and a path — never an MCP tool's call
		return method != MCPMethod && a.Method == method && a.Path == path
	case ScopeTool: // a tool, by its name — never an HTTP call
		return method == MCPMethod && a.Method == MCPMethod && a.Path == path
	}
	return false
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
	ErrNoRequest   = errors.New("no such request")
	ErrNotPending  = errors.New("the request is no longer pending")
	ErrTooMany     = fmt.Errorf("%d requests are already waiting for this person: they answer or the agent withdraws some first", MaxPending)
	ErrNoTicket    = errors.New("no such ticket: the request doesn't exist, isn't approved, expired or was already used")
	ErrTicketBound = errors.New("the ticket was approved for another call: its method, path, body and content type must be exactly what the person saw")
	ErrBadGrant    = errors.New(`grant is "once", "hour" or "always"`)
	ErrBadScope    = errors.New(`scope is "call" (this method and path) or "tile" (every write to the tile)`)
	ErrNoStore     = errors.New("this xbind keeps no delegation state")
	// ErrUnseenBody: the request's body can't be shown, and the approval
	// didn't say it approves it unseen.
	ErrUnseenBody   = errors.New("this request carries a body that can't be shown as text: approving it means approving bytes nobody read — say so ({unseen: true}) or deny it")
	ErrStandingKind = errors.New("a standing approval covers a tile's API or one MCP tool: a session start, a tile create or a web page is approved each time")
)

// BodyHash is the hex SHA-256 a ticket binds a body by.
func BodyHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// requests reads user's requests under p's lock, expiring what ran out,
// dropping what was decided long ago and keeping the list bounded;
// changed says the file should be rewritten.
func (s *Store) requests(p *person, user string) (doc requestsDoc, changed bool, err error) {
	cur, err := load(&p.reqs, s.userFile(user, "requests.json"))
	if err != nil {
		return requestsDoc{}, false, err
	}
	now := s.now()
	doc.Schema = cur.Schema
	doc.Requests = make([]Request, 0, len(cur.Requests))
	for _, r := range cur.Requests {
		if (r.State == Pending || r.State == Approved) && !now.Before(r.Expires) {
			r.State, r.Body, changed = Expired, "", true
			r.Decided = now.UTC().Truncate(time.Second)
		}
		if r.State != Pending && r.State != Approved && now.Sub(r.Decided) > keepDecided {
			changed = true
			continue
		}
		doc.Requests = append(doc.Requests, r)
	}
	// bounded: an agent that asks and withdraws in a loop grows nothing
	for i := 0; len(doc.Requests) > MaxRequests && i < len(doc.Requests); {
		if st := doc.Requests[i].State; st != Pending && st != Approved {
			doc.Requests = slices.Delete(doc.Requests, i, i+1)
			changed = true
			continue
		}
		i++
	}
	return doc, changed, nil
}

func (s *Store) saveRequests(p *person, user string, doc requestsDoc) error {
	doc.Schema = requestsSchema
	return save(&p.reqs, s.userFile(user, "requests.json"), doc)
}

// NewRequest stores a pending request for r.User: r's Agent, Kind, Method,
// Target, Tile, ContentType, Summary and Preview, and body as the call's
// body.
func (s *Store) NewRequest(r Request, body []byte) (Request, error) {
	if s == nil {
		return Request{}, ErrNoStore
	}
	switch {
	case len(body) > MaxBody:
		return Request{}, fmt.Errorf("the body is %d bytes: an approval shows and binds at most %d", len(body), MaxBody)
	case len(r.Target) > MaxTarget:
		return Request{}, fmt.Errorf("the path is %d bytes: an approval binds at most %d", len(r.Target), MaxTarget)
	case len(r.ContentType) > 256 || len(r.Method) > 16 || len(r.Tile) > MaxTarget:
		return Request{}, errors.New("not a call an approval can bind")
	}
	p := s.lock(r.User)
	defer p.mu.Unlock()
	doc, _, err := s.requests(p, r.User)
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
	r.Body, r.BodyShown, r.BodyBinary, r.ApprovedUnseen = "", false, false, false
	switch {
	case len(body) == 0:
		r.BodyShown = true // nothing to show is shown
	case TextBody(body):
		r.Body, r.BodyShown = string(body), true
	default:
		r.BodyBinary = true
	}
	if len(r.Summary) > maxSummaryLen {
		r.Summary = r.Summary[:maxSummaryLen]
	}
	doc.Requests = append(doc.Requests, r)
	return r, s.saveRequests(p, r.User, doc)
}

// Requests lists user's requests, oldest first; pendingOnly leaves out the
// decided ones.
func (s *Store) Requests(user string, pendingOnly bool) ([]Request, error) {
	if s == nil {
		return []Request{}, nil
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, changed, err := s.requests(p, user)
	if err != nil {
		return nil, err
	}
	if changed {
		_ = s.saveRequests(p, user, doc)
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
// always grant on a tile call also records a standing approval, which is
// returned: of that one call (scope "call", or "": the default) or of
// every write to the tile (scope "tile").
func (s *Store) Decide(user, id, grant, scope, via string) (Request, *Approval, error) {
	return s.DecideWith(user, id, grant, scope, via, false)
}

// TextBody reports a body a person can read on an approval card: valid
// UTF-8 without control characters (tabs and line breaks apart) — a NUL or
// an escape sequence hides what follows it as surely as other bytes do.
func TextBody(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	for _, c := range string(body) {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' || c == 0x7f || c >= 0x80 && c < 0xa0 {
			return false
		}
	}
	return true
}

// DecideWith is Decide where the person may approve a body they can't be
// shown: unseen says they know (ErrUnseenBody without it), and the request
// records it (ApprovedUnseen).
func (s *Store) DecideWith(user, id, grant, scope, via string, unseen bool) (Request, *Approval, error) {
	if s == nil {
		return Request{}, nil, ErrNoStore
	}
	if grant != "" && grant != GrantOnce && grant != GrantHour && grant != GrantAlways {
		return Request{}, nil, ErrBadGrant
	}
	if scope != "" && scope != ScopeCall && scope != ScopeTile && scope != ScopeTool {
		return Request{}, nil, ErrBadScope
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.requests(p, user)
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
	tool, _ := r.Preview["tool"].(string)
	switch {
	case grant != GrantHour && grant != GrantAlways:
	case r.Kind == KindCall && r.Tile != "":
		if scope == "" {
			scope = ScopeCall
		}
		if scope == ScopeTool {
			return *r, nil, ErrBadScope
		}
	case r.Kind == KindMCP && r.Tile != "" && tool != "":
		// "always for this tool": that server and that tool, any arguments
		if scope != "" && scope != ScopeTool {
			return *r, nil, ErrBadScope
		}
		scope = ScopeTool
	default:
		return *r, nil, ErrStandingKind
	}
	if grant != "" && r.BodyBinary && !unseen {
		return *r, nil, ErrUnseenBody
	}
	now := s.now().UTC().Truncate(time.Second)
	r.Decided, r.DecidedVia, r.Grant = now, via, grant
	r.ApprovedUnseen = grant != "" && r.BodyBinary
	var ap *Approval
	if grant == "" {
		r.State, r.Body = Denied, ""
	} else {
		r.State, r.Expires = Approved, now.Add(TicketTTL)
		if grant != GrantOnce {
			give := Approval{User: user, Tile: r.Tile, Scope: scope, Method: r.Method, Path: r.Path(), Via: via, Request: r.ID}
			if scope == ScopeTool {
				give.Method, give.Path, give.Pin = MCPMethod, tool, PinFromPreview(r.Preview["pin"])
			}
			a, err := s.addApproval(p, give, grant)
			if err != nil {
				return Request{}, nil, err
			}
			ap = &a
		}
	}
	out := *r
	return out, ap, s.saveRequests(p, user, doc)
}

// Withdraw takes back user's pending or approved request id (the agent no
// longer wants it).
func (s *Store) Withdraw(user, id string) (Request, error) {
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
	if r.State != Pending && r.State != Approved {
		return *r, ErrNotPending
	}
	r.State, r.Body, r.Decided = Withdrawn, "", s.now().UTC().Truncate(time.Second)
	out := *r
	return out, s.saveRequests(p, user, doc)
}

// Redeem spends user's ticket id for a call: the request must be approved
// and unexpired, and the call exactly the one the person approved — its
// method, its path with the query, its body and the type it is sent as —
// reaching the tile it resolved to when they approved it. A ticket is spent
// by the attempt that matches it, never by one that doesn't: a wrong guess
// burns nothing.
func (s *Store) Redeem(user, id, method, target, tile, contentType string, body []byte) (Request, error) {
	if s == nil {
		return Request{}, ErrNoTicket
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.requests(p, user)
	if err != nil {
		return Request{}, ErrNoTicket
	}
	i := slices.IndexFunc(doc.Requests, func(r Request) bool { return r.ID == id })
	if i < 0 || doc.Requests[i].State != Approved {
		return Request{}, ErrNoTicket
	}
	r := &doc.Requests[i]
	if r.Method != method || r.Target != target || r.ContentType != contentType || r.BodySize != len(body) || r.BodySHA256 != BodyHash(body) ||
		r.Kind == KindCall && r.Tile != tile {
		return Request{}, ErrTicketBound
	}
	r.State, r.Body, r.Decided = Used, "", s.now().UTC().Truncate(time.Second)
	out := *r
	if err := s.saveRequests(p, user, doc); err != nil {
		return Request{}, err // not recorded as spent: not honoured
	}
	return out, nil
}

// ---- standing approvals ----

func (s *Store) approvals(p *person, user string) (doc approvalsDoc, changed bool, err error) {
	cur, err := load(&p.aps, s.userFile(user, "approvals.json"))
	if err != nil {
		return approvalsDoc{}, false, err
	}
	now := s.now()
	doc.Schema = cur.Schema
	doc.Approvals = make([]Approval, 0, len(cur.Approvals))
	for _, a := range cur.Approvals {
		if !a.Until.IsZero() && !now.Before(a.Until) {
			changed = true
			continue
		}
		if a.Scope == "" { // a record without one never widens: it covers nothing
			changed = true
			continue
		}
		doc.Approvals = append(doc.Approvals, a)
	}
	return doc, changed, nil
}

func (s *Store) saveApprovals(p *person, user string, doc approvalsDoc) error {
	doc.Schema = requestsSchema
	return save(&p.aps, s.userFile(user, "approvals.json"), doc)
}

func (s *Store) addApproval(p *person, a Approval, grant string) (Approval, error) {
	switch {
	case a.Tile == "" || a.User == "":
		return Approval{}, errors.New("a standing approval names a tile")
	case a.Scope == ScopeTile:
		a.Method, a.Path = "", ""
	case a.Scope == ScopeCall && a.Method != "" && a.Path != "" && len(a.Path) <= MaxTarget:
	case a.Scope == ScopeTool && a.Method == MCPMethod && a.Path != "" && len(a.Path) <= MaxTarget:
	default:
		return Approval{}, ErrBadScope
	}
	doc, _, err := s.approvals(p, a.User)
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
	// one approval of the same thing: the new one replaces the old
	doc.Approvals = slices.DeleteFunc(doc.Approvals, func(e Approval) bool {
		return e.Tile == a.Tile && e.Scope == a.Scope && e.Method == a.Method && e.Path == a.Path
	})
	if len(doc.Approvals) >= MaxApprovals {
		return Approval{}, fmt.Errorf("%d standing approvals: revoke some first", MaxApprovals)
	}
	doc.Approvals = append(doc.Approvals, a)
	return a, s.saveApprovals(p, a.User, doc)
}

// AddApproval records a standing approval of a.User: a.Tile with scope
// "tile", or a.Method on a.Path of it with scope "call"; grant "hour" or
// "always".
func (s *Store) AddApproval(a Approval, grant, via string) (Approval, error) {
	if s == nil {
		return Approval{}, ErrNoStore
	}
	p := s.lock(a.User)
	defer p.mu.Unlock()
	a.Via, a.Request = via, ""
	return s.addApproval(p, a, grant)
}

// Approvals lists user's standing approvals that hold now.
func (s *Store) Approvals(user string) ([]Approval, error) {
	if s == nil {
		return []Approval{}, nil
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, changed, err := s.approvals(p, user)
	if err != nil {
		return nil, err
	}
	if changed {
		_ = s.saveApprovals(p, user, doc)
	}
	return append([]Approval{}, doc.Approvals...), nil
}

// RevokeApproval removes user's standing approval id; false when there is
// none.
func (s *Store) RevokeApproval(user, id string) (bool, error) {
	n, err := s.DropApprovals(user, func(a Approval) bool { return a.ID == id })
	return n > 0, err
}

// DropApprovals removes user's standing approvals that drop reports — a
// tile that is gone, or that the person can no longer read — and answers
// how many went.
func (s *Store) DropApprovals(user string, drop func(Approval) bool) (int, error) {
	if s == nil {
		return 0, ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, changed, err := s.approvals(p, user)
	if err != nil {
		return 0, err
	}
	n := len(doc.Approvals)
	doc.Approvals = slices.DeleteFunc(doc.Approvals, drop)
	if len(doc.Approvals) == n && !changed {
		return 0, nil
	}
	return n - len(doc.Approvals), s.saveApprovals(p, user, doc)
}

// Covers reports the standing approval under which user's agent may make
// method on path (under /api/, no query) of tile now. A store that can't be
// read covers nothing.
func (s *Store) Covers(user, tile, method, path string) (Approval, bool) {
	if s == nil || tile == "" {
		return Approval{}, false
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.approvals(p, user)
	if err != nil {
		return Approval{}, false
	}
	for _, a := range doc.Approvals {
		if a.covers(tile, method, path) {
			return a, true
		}
	}
	return Approval{}, false
}

// VoidTickets ends every ticket of user that was approved and not used:
// what a person approved before their agent was stopped — paused, switched
// off, or the person themselves disabled — is not waiting when it runs
// again. A pending request stays (the person may still decide it). It
// answers how many were voided.
func (s *Store) VoidTickets(user string) (int, error) {
	if s == nil {
		return 0, ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.requests(p, user)
	if err != nil {
		return 0, err
	}
	now, n := s.now().UTC().Truncate(time.Second), 0
	for i := range doc.Requests {
		if r := &doc.Requests[i]; r.State == Approved {
			r.State, r.Body, r.Expires = Expired, "", now
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.saveRequests(p, user, doc)
}

// WithdrawPending withdraws every pending request of user (WithdrawnBy
// "xbin"): their agent stopped for good — workspace agents switched off,
// the person no longer given an agent, disabled or deleted — so nothing
// is left waiting that a person could approve the moment it runs again.
// A pause or a hold keeps them (the person decides after resuming). It
// answers the ids it withdrew.
func (s *Store) WithdrawPending(user string) ([]string, error) {
	if s == nil {
		return nil, ErrNoStore
	}
	p := s.lock(user)
	defer p.mu.Unlock()
	doc, _, err := s.requests(p, user)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Second)
	var ids []string
	for i := range doc.Requests {
		if r := &doc.Requests[i]; r.State == Pending {
			r.State, r.Body, r.Decided, r.WithdrawnBy = Withdrawn, "", now, WithdrawnByXbin
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, s.saveRequests(p, user, doc)
}
