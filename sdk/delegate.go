package xbin

// delegate.go — for the backend of a sealed xbind component that acts for
// people: the workspace agent (docs/auth.md §Delegation,
// docs/protocol/delegation.md). A person's partition of such a component
// calls xbind and other tiles AS THAT PERSON by sending its own instance
// token under another scheme; AsPerson is the client that does. Every
// other tile's backend can call these too, and gets 401: xbind decides who
// may delegate from its own table, never from the caller.
//
// What the person's agent may do is a closed list (the delegation cap);
// every side effect needs the person's approval, which xbind keeps:
//
//	resp, err := xbin.AsPerson().Do(req)            // a read: just make it
//	if err := xbin.DelegateRefusal(resp); errors.Is(err, xbin.ErrNeedsApproval) {
//		ask, _ := xbin.RequestApproval(ctx, "POST", "/api/apps/mail/send", body, "send the summary to Ada")
//		// … the person approves in their own session; poll ApprovalOf(ctx, ask.ID) …
//		req2, _ := http.NewRequestWithContext(ctx, "POST", "http://xbin/api/apps/mail/send", bytes.NewReader(body))
//		xbin.WithTicket(req2, ask.ID)               // exactly the method, path and body approved
//		resp, err = xbin.AsPerson().Do(req2)
//	}

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AsPerson is Client for calls made as the person this partition belongs
// to: the same gateway socket and instance token, under the delegate
// scheme ("Authorization: XBin-Delegate …"). Use Client for the
// component's own calls (its kv, vault, bus), AsPerson for everything it
// does for its person. Shared, like Client; no overall timeout.
func AsPerson() *http.Client {
	personOnce.Do(func() {
		tr := &http.Transport{
			DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return GatewayDial(ctx) },
			MaxIdleConns:    16,
			IdleConnTimeout: 90 * time.Second,
		}
		personClient = &http.Client{Transport: &delegateTransport{base: tr, token: os.Getenv("XBIN_TOKEN")}}
	})
	return personClient
}

var (
	personOnce   sync.Once
	personClient *http.Client
)

type delegateTransport struct {
	base  http.RoundTripper
	token string
}

func (t *delegateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "XBin-Delegate "+t.token)
	return t.base.RoundTrip(r)
}

// WithTicket marks r as the call an approved request allows: id is the
// request's (RequestApproval). The method, the path with its query and the
// body must be exactly what was approved; a ticket is good once, for 15
// minutes after the approval.
func WithTicket(r *http.Request, id string) { r.Header.Set("X-XBin-Ticket", id) }

// WithAgentModel tells xbind which model the agent's conversation is on
// now. The orgs' model rules are asked with it on every call that names a
// tile; a call without it is judged as on an unknown model.
func WithAgentModel(r *http.Request, model string) { r.Header.Set("X-XBin-Agent-Model", model) }

// WithDryRun asks xbind what r would do instead of doing it: the answer is
// 200 with a JSON description (DryRun reads it) and nothing runs.
func WithDryRun(r *http.Request) { r.Header.Set("X-XBin-Dry-Run", "1") }

// The refusals a delegate call can meet, for errors.Is on what
// DelegateRefusal (and every helper here) returns.
var (
	// ErrNotDelegated: the delegation cap doesn't include this route — no
	// approval changes that (answering a tile agent, the admin API, …).
	ErrNotDelegated = errors.New("not delegated to a person's agent")
	// ErrNeedsApproval: a side effect without the person's approval — ask
	// (RequestApproval) and send the ticket.
	ErrNeedsApproval = errors.New("needs the person's approval")
	// ErrBadTicket: the ticket doesn't exist, isn't approved, expired, was
	// used, or was approved for another method, path or body.
	ErrBadTicket = errors.New("the ticket doesn't allow this call")
	// ErrPolicyRefused: the tile, or the org that owns it, keeps the agent
	// out (or the person can't read it). The message says why — show it.
	ErrPolicyRefused = errors.New("refused for this tile")
	// ErrAgentPaused: the person paused their agent. Stop; don't retry.
	ErrAgentPaused = errors.New("the person paused their agent")
	// ErrAgentOff: workspace agents are off for this workspace, or this
	// xbind can't run them.
	ErrAgentOff = errors.New("workspace agents are off")
	// ErrAdminRead: reading admin state needs the person's opt-in to admin
	// changes (and the person to be an admin).
	ErrAdminRead = errors.New("admin state isn't open to this person's agent")
	// ErrRateLimited: too many acts in a minute; DelegateError.RetryAfter
	// says when to try again.
	ErrRateLimited = errors.New("acting too fast")
)

// DelegateError is a refused delegate call.
type DelegateError struct {
	Status  int    // the HTTP status
	Refusal string // xbind's refusal code ("" when the route itself refused)
	Class   string // the delegation class the call was judged by
	Message string // xbind's words: fit to show the person
	// RetryAfter: when a rate-limited call may be made again.
	RetryAfter time.Duration
}

func (e *DelegateError) Error() string {
	if e.Refusal != "" {
		return fmt.Sprintf("xbin: %s (%d %s)", e.Message, e.Status, e.Refusal)
	}
	return fmt.Sprintf("xbin: %s (%d)", e.Message, e.Status)
}

// Is maps xbind's refusal code onto the Err… values above.
func (e *DelegateError) Is(target error) bool {
	switch e.Refusal {
	case "not-delegated":
		return target == ErrNotDelegated
	case "needs-approval":
		return target == ErrNeedsApproval
	case "ticket":
		return target == ErrBadTicket
	case "tile":
		return target == ErrPolicyRefused
	case "paused":
		return target == ErrAgentPaused
	case "workspace-off", "no-sandbox", "off":
		return target == ErrAgentOff
	case "admin-read":
		return target == ErrAdminRead
	case "rate-limited":
		return target == ErrRateLimited
	}
	return false
}

// DelegateRefusal is nil for a 2xx answer; otherwise it reads and closes
// resp's body and returns the *DelegateError it describes. A refusal by the
// tile itself (its own 4xx, no refusal code) comes back with Refusal "".
func DelegateRefusal(resp *http.Response) error {
	if resp.StatusCode/100 == 2 {
		return nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var b struct {
		Error   string `json:"error"`
		Refusal string `json:"refusal"`
		Class   string `json:"class"`
	}
	_ = json.Unmarshal(raw, &b)
	e := &DelegateError{Status: resp.StatusCode, Refusal: b.Refusal, Class: b.Class, Message: b.Error}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(raw))
	}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	return e
}

// delegateCall makes one JSON call of the delegation API (path: the whole
// path, from /api on) as the person and decodes a 2xx answer into
// out (nil: discarded).
func delegateCall(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://xbin"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := AsPerson().Do(req)
	if err != nil {
		return err
	}
	if err := DelegateRefusal(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// DryRun asks xbind what the call (method, path under /api/, with its
// query) would be for this person now, running nothing: its class, whether
// it is a side effect and needs approval, the tile, the person's level and
// role there — or, with allowed false, why the tile refuses it
// (docs/protocol/delegation.md §Dry run). A call the cap refuses is an
// error, as it would be without the dry run.
func DryRun(ctx context.Context, method, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://xbin"+path, nil)
	if err != nil {
		return nil, err
	}
	WithDryRun(req)
	resp, err := AsPerson().Do(req)
	if err != nil {
		return nil, err
	}
	if err := DelegateRefusal(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// ApprovalRequest is one request for the person's approval, and — once
// State is "approved" — the ticket for exactly that call.
type ApprovalRequest struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`   // call | session | template
	Method  string         `json:"method"` //
	Target  string         `json:"target"` // the path with its query, as approved
	Tile    string         `json:"tile,omitempty"`
	State   string         `json:"state"`           // pending | approved | denied | used | expired | withdrawn
	Grant   string         `json:"grant,omitempty"` // once | hour | always
	Preview map[string]any `json:"preview,omitempty"`
	Expires time.Time      `json:"expires"`
}

// Approved reports a request the person approved and that isn't spent: its
// ID is a ticket now.
func (a ApprovalRequest) Approved() bool { return a.State == "approved" }

// Decided reports a request that will never become a ticket (denied,
// expired, withdrawn) or already was one (used).
func (a ApprovalRequest) Decided() bool { return a.State != "pending" && a.State != "approved" }

// RequestApproval asks the person to approve one side effect, exactly as
// it will be sent: method, path (under /api/, with its query) and body (at
// most 256 KiB). Only calls that need approval can be asked for: a non-GET
// call to a tile's API, POST /api/xbin/term/sessions (starting a tile
// agent), POST /api/xbin/templates/new. summary is the agent's own words
// for the person, shown as the agent's. The person is told (an event, a
// push); poll ApprovalOf until it is approved or decided.
func RequestApproval(ctx context.Context, method, path string, body []byte, summary string) (ApprovalRequest, error) {
	var out ApprovalRequest
	in := map[string]string{"method": method, "path": path, "summary": summary}
	if len(body) > 0 {
		in["bodyBase64"] = base64.StdEncoding.EncodeToString(body)
	}
	return out, delegateCall(ctx, http.MethodPost, "/api/xbin/delegate/requests", in, &out)
}

// ApprovalOf reads one of this person's requests.
func ApprovalOf(ctx context.Context, id string) (ApprovalRequest, error) {
	var out ApprovalRequest
	return out, delegateCall(ctx, http.MethodGet, "/api/xbin/delegate/requests/"+url.PathEscape(id), nil, &out)
}

// WithdrawApproval takes back a request the agent no longer wants.
func WithdrawApproval(ctx context.Context, id string) error {
	return delegateCall(ctx, http.MethodDelete, "/api/xbin/delegate/requests/"+url.PathEscape(id), nil, nil)
}

// StandingApprovals lists the tiles whose API this person lets their agent
// write to without a ticket (until, zero: until they revoke it).
func StandingApprovals(ctx context.Context) ([]StandingApproval, error) {
	var out struct {
		Approvals []StandingApproval `json:"approvals"`
	}
	return out.Approvals, delegateCall(ctx, http.MethodGet, "/api/xbin/delegate/approvals", nil, &out)
}

// StandingApproval is one standing approval.
type StandingApproval struct {
	ID    string    `json:"id"`
	Tile  string    `json:"tile"`
	Until time.Time `json:"until,omitempty"`
}

// Change is one admin operation of a change set. Kind is one of a closed
// list — grant.add, grant.revoke, binding.set, binding.remove, org.share,
// limits.set — and Args its arguments (docs/protocol/delegation.md §Change
// sets). Why is the agent's reason, shown to the person as the agent's.
type Change struct {
	Kind string `json:"kind"`
	Args any    `json:"args"`
	Why  string `json:"why,omitempty"`
}

// ChangeSet is a proposal and its fate.
type ChangeSet struct {
	ID    string          `json:"id"`
	Title string          `json:"title"`
	State string          `json:"state"` // proposed | applying | applied | partial | rejected | expired | withdrawn
	Items []ChangeSetItem `json:"items"`
}

// ChangeSetItem is one operation as xbind described it — Line, Effects,
// Warnings and Blocked are xbind's words — and, once decided, its Result:
// applied | failed | stale | skipped | rejected | blocked.
type ChangeSetItem struct {
	N        int      `json:"n"`
	Kind     string   `json:"kind"`
	Line     string   `json:"line"`
	Effects  []string `json:"effects,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Blocked  string   `json:"blocked,omitempty"`
	Result   string   `json:"result,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// ProposeChanges submits a batch of admin changes for the person to
// approve. Nothing is applied: the person approves all, some or none in
// their own session, and xbind applies those as them. Only for a person
// who is an admin and let their agent propose admin changes (403
// otherwise); an unknown kind or bad arguments is 400.
func ProposeChanges(ctx context.Context, title string, changes []Change) (ChangeSet, error) {
	var out ChangeSet
	return out, delegateCall(ctx, http.MethodPost, "/api/xbin/delegate/changesets", map[string]any{"title": title, "items": changes}, &out)
}

// ChangeSetOf reads one of this person's change sets.
func ChangeSetOf(ctx context.Context, id string) (ChangeSet, error) {
	var out ChangeSet
	return out, delegateCall(ctx, http.MethodGet, "/api/xbin/delegate/changesets/"+url.PathEscape(id), nil, &out)
}

// WithdrawChanges takes back a proposal nobody decided yet.
func WithdrawChanges(ctx context.Context, id string) error {
	return delegateCall(ctx, http.MethodDelete, "/api/xbin/delegate/changesets/"+url.PathEscape(id), nil, nil)
}
