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
// does for its person. Shared, like Client; no overall timeout. It follows
// no redirect: a 3xx comes back as the response (DelegateRefusal reports
// it as an error).
func AsPerson() *http.Client {
	personOnce.Do(func() {
		tr := &http.Transport{
			DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return GatewayDial(ctx) },
			MaxIdleConns:    16,
			IdleConnTimeout: 90 * time.Second,
		}
		personClient = &http.Client{
			Transport: &delegateTransport{base: tr, token: os.Getenv("XBIN_TOKEN")},
			// A redirect is an answer, never an instruction: what the agent
			// asked for, and its person approved, is one call. Following a
			// tile's 307 would send the same method, body and credential to
			// a path the tile chose — another tile's write under a standing
			// approval, a session's /cancel. The caller gets the 3xx.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
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
	if id, _ := r.Context().Value(agentSessionKey{}).(string); id != "" && r.Header.Get(agentSessionHeader) == "" {
		r.Header.Set(agentSessionHeader, id) // the conversation this call is for (InAgentSession)
	}
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
//
// Deprecated: xbind ignores the header. A conversation's model is its
// agent session's (OpenAgentSession, SetSessionModel); make the call with
// InAgentSession's context.
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
	// ErrNoSession: the call carries no live agent session — open one
	// (OpenAgentSession) and make the call with InAgentSession's context.
	ErrNoSession = errors.New("no agent session")
	// ErrHiddenByScope: the tile is outside what the person let this
	// conversation see ("hidden by you") — unlike ErrPolicyRefused, where an
	// org's rule keeps the agent out and Message says which and why.
	ErrHiddenByScope = errors.New("hidden by the session's scope")
	// ErrModelTaint: the session can't change to that model — orgs whose
	// data the conversation has seen don't allow it (DelegateError.Orgs);
	// open a new session.
	ErrModelTaint = errors.New("the session's taint doesn't allow that model")
	// ErrRateLimited: too many acts in a minute; DelegateError.RetryAfter
	// says when to try again.
	ErrRateLimited = errors.New("acting too fast")
	// ErrTaintedBrief: a start, a prompt or a queued message was refused
	// because the conversation holds data of an org whose model rule the
	// tile agent session's model source doesn't satisfy (Message says which
	// org and why). Telling that session less doesn't help: the taint is
	// the conversation's.
	ErrTaintedBrief = errors.New("the conversation's taint keeps it from briefing that tile agent")
	// ErrSessionMode: the tile agent session is in a mode that doesn't ask
	// before it acts, and its person didn't approve it in that mode: it
	// takes no messages from their agent until they switch it back.
	ErrSessionMode = errors.New("the session's mode takes no messages from the agent")
	// ErrHarnessCommand: the text begins with "/", which the coding agent
	// reads as one of its own commands — those are the person's to give.
	ErrHarnessCommand = errors.New("a harness command is the person's to give")
)

// DelegateError is a refused delegate call.
type DelegateError struct {
	Status  int    // the HTTP status
	Refusal string // xbind's refusal code ("" when the route itself refused)
	Class   string // the delegation class the call was judged by
	Message string // xbind's words: fit to show the person
	// RetryAfter: when a rate-limited call may be made again.
	RetryAfter time.Duration
	// Orgs: for ErrModelTaint, the orgs whose data the session has seen and
	// that don't allow the model; Reason the first one's rule.
	Orgs   []string
	Reason string
	// Said: what an outside MCP server itself wrote behind the refusal
	// (D240) — an outsider's words, never xbind's: show it as data.
	Said string
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
	case "no-session":
		return target == ErrNoSession
	case "hidden":
		return target == ErrHiddenByScope
	case "model_taint":
		return target == ErrModelTaint
	case "tile":
		return target == ErrPolicyRefused
	case "paused":
		return target == ErrAgentPaused
	case "workspace-off", "no-sandbox", "off", "no-agent", "held":
		return target == ErrAgentOff
	case "taint":
		return target == ErrTaintedBrief
	case "mode":
		return target == ErrSessionMode
	case "command":
		return target == ErrHarnessCommand
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
		Error   string   `json:"error"`
		Refusal string   `json:"refusal"`
		Class   string   `json:"class"`
		Code    string   `json:"code"`
		Orgs    []string `json:"orgs"`
		Reason  string   `json:"reason"`
		Said    string   `json:"said"`
	}
	_ = json.Unmarshal(raw, &b)
	e := &DelegateError{Status: resp.StatusCode, Refusal: b.Refusal, Class: b.Class, Message: b.Error, Orgs: b.Orgs, Reason: b.Reason, Said: b.Said}
	if b.Refusal == "" && b.Code == "model_taint" {
		e.Refusal = b.Code
	}
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
	ID     string `json:"id"`
	Kind   string `json:"kind"`   // call | session | template
	Method string `json:"method"` //
	Target string `json:"target"` // the path with its query, as approved
	Tile   string `json:"tile,omitempty"`
	// ContentType is the media type the approval is of: the ticketed call
	// carries this Content-Type or is refused (ErrBadTicket).
	ContentType string         `json:"contentType,omitempty"`
	State       string         `json:"state"`           // pending | approved | denied | used | expired | withdrawn
	Grant       string         `json:"grant,omitempty"` // once | hour | always
	Preview     map[string]any `json:"preview,omitempty"`
	Expires     time.Time      `json:"expires"`
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
//
// A body is approved as JSON: send the ticketed call with Content-Type
// application/json (parameters such as charset don't matter), or ask with
// RequestApprovalAs — the content type is part of what was approved.
func RequestApproval(ctx context.Context, method, path string, body []byte, summary string) (ApprovalRequest, error) {
	return RequestApprovalAs(ctx, method, path, "", body, summary)
}

// RequestApprovalAs is RequestApproval for a body that isn't JSON:
// contentType is the Content-Type the ticketed call will carry ("" is
// application/json when there is a body).
func RequestApprovalAs(ctx context.Context, method, path, contentType string, body []byte, summary string) (ApprovalRequest, error) {
	var out ApprovalRequest
	in := map[string]string{"method": method, "path": path, "summary": summary}
	if len(body) > 0 {
		in["bodyBase64"] = base64.StdEncoding.EncodeToString(body)
	}
	if contentType != "" {
		in["contentType"] = contentType
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

// StandingApproval is one standing approval: of one call — Method on Path
// of Tile's API, any body and query (Scope "call") — or of every write to
// Tile's API (Scope "tile").
type StandingApproval struct {
	ID     string    `json:"id"`
	Tile   string    `json:"tile"`
	Scope  string    `json:"scope"`
	Method string    `json:"method,omitempty"`
	Path   string    `json:"path,omitempty"`
	Until  time.Time `json:"until,omitempty"`
}

// Covers reports whether the approval lets method on path (a tile API
// path, "/api/<tile>/…", without its query) through without a ticket.
func (a StandingApproval) Covers(method, path string) bool {
	switch a.Scope {
	case "tile":
		return path == "/api/"+a.Tile || strings.HasPrefix(path, "/api/"+a.Tile+"/")
	case "call":
		return strings.EqualFold(a.Method, method) && a.Path == path
	}
	return false
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
	// Plan: proposed with ProposePlan — its lines may create tiles and
	// start tile agents, and are the person's own to apply or an admin's.
	Plan bool `json:"plan,omitempty"`
}

// ChangeSetItem is one operation as xbind described it — Line, Effects,
// Warnings and Blocked are xbind's words — and, once decided, its Result:
// applied | failed | stale | skipped | rejected | blocked — and, for a
// plan's line an admin was asked to apply, asked.
type ChangeSetItem struct {
	N        int      `json:"n"`
	Kind     string   `json:"kind"`
	Line     string   `json:"line"`
	Effects  []string `json:"effects,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Blocked  string   `json:"blocked,omitempty"`
	Result   string   `json:"result,omitempty"`
	Error    string   `json:"error,omitempty"`
	// A plan's line: Admin — applying it takes an admin's rights;
	// NeedsAdmin — and its person is no admin (they can ask one); Needs —
	// the earlier lines whose tiles it names; Out — what applying it made
	// ({"path": …} for tile.create, {"session": …} for agent.start).
	Admin      bool            `json:"admin,omitempty"`
	NeedsAdmin bool            `json:"needsAdmin,omitempty"`
	Needs      []int           `json:"needs,omitempty"`
	Out        json.RawMessage `json:"out,omitempty"`
	// Wait: the line waits for Tile to provide Service (result "waiting"
	// once its person approved it): xbind applies it by itself then, and
	// only if the tile declares exactly that.
	Wait *ChangeSetWait `json:"wait,omitempty"`
	// Later: xbind's words for a line that would have to wait and may not
	// (it takes an admin's rights): propose it again once its tile
	// provides what it binds.
	Later string `json:"later,omitempty"`
}

// ChangeSetWait is what a plan's waiting line waits for.
type ChangeSetWait struct {
	Tile    string    `json:"tile"`
	Service string    `json:"service"`
	Kind    string    `json:"kind"`
	Role    string    `json:"role"`
	Until   time.Time `json:"until,omitempty"`
	// Held: set while the tile declares something other than what was
	// approved, so the line is not applied — one fixed sentence of
	// xbind's; what the tile declared is the person's to read.
	Held string `json:"held,omitempty"`
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

// ProposePlan submits multi-step work as one change set for the person to
// review: the kinds of ProposeChanges, and tile.create {template, path,
// owner?} and agent.start {tile, brief, harness?, mode?}. A later line may
// name a tile an earlier tile.create line makes. Nothing is applied; any
// person's agent may propose one (docs/protocol/delegation.md §Plans).
func ProposePlan(ctx context.Context, title string, lines []Change) (ChangeSet, error) {
	var out ChangeSet
	return out, delegateCall(ctx, http.MethodPost, "/api/xbin/delegate/plans", map[string]any{"title": title, "items": lines}, &out)
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

// ---- agent sessions: the conversation's handle (docs/sdk.md) ----

const agentSessionHeader = "X-XBin-Agent-Session"

type agentSessionKey struct{}

// InAgentSession is ctx for one conversation: every AsPerson() call made
// with it (and every helper here) carries the agent session id — the
// handle xbind issued for the conversation's scope and model. Set it once,
// where the conversation's work starts; pass the context down. A call
// without one is ErrNoSession (opening a session and the docs reads apart).
func InAgentSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentSessionKey{}, id)
}

// AgentSessionID is the session id ctx carries ("" none).
func AgentSessionID(ctx context.Context) string {
	id, _ := ctx.Value(agentSessionKey{}).(string)
	return id
}

// AgentSessionHeader is the request header that carries the handle — for
// the holder's own model calls to the LLM connector (Client(), not
// AsPerson()): xbind holds them to the session's model.
const AgentSessionHeader = agentSessionHeader

// SessionScope is what a conversation may see: orgs (every tile under
// them; "workspace" for the tiles in no org) and single tiles. A nil
// *SessionScope is everything the person can see.
type SessionScope struct {
	Orgs  []string `json:"orgs"`
	Tiles []string `json:"tiles"`
}

// ConnectionCounts are a connection view's totals.
type ConnectionCounts struct {
	Tiles        int    `json:"tiles"`
	Orgs         int    `json:"orgs"`
	NotConnected int    `json:"notConnected"`
	Hidden       int    `json:"hidden"`
	Blocked      int    `json:"blocked"`
	NoAccess     int    `json:"noAccess"`
	Summary      string `json:"summary"`
}

// AgentSession is a handle as xbind holds it.
type AgentSession struct {
	ID         string        `json:"id"`
	Scope      *SessionScope `json:"scope"`
	Model      string        `json:"model"` // "<upstream>/<model>"
	Tainted    []string      `json:"tainted"`
	Connection struct {
		Counts ConnectionCounts `json:"counts"`
	} `json:"connection"`
}

// OpenAgentSession opens a handle for one conversation: scope (nil: all
// the person can see) and the model it is on.
func OpenAgentSession(ctx context.Context, scope *SessionScope, model string) (AgentSession, error) {
	var out AgentSession
	return out, delegateCall(ctx, http.MethodPost, "/api/xbin/delegate/sessions", map[string]any{"scope": scope, "model": model}, &out)
}

// AgentSessionOf reads a handle.
func AgentSessionOf(ctx context.Context, id string) (AgentSession, error) {
	var out AgentSession
	return out, delegateCall(ctx, http.MethodGet, "/api/xbin/delegate/sessions/"+url.PathEscape(id), nil, &out)
}

// SetSessionModel changes a session's model: ErrModelTaint when an org
// whose data the conversation has seen doesn't allow it (open a new
// session instead).
func SetSessionModel(ctx context.Context, id, model string) (AgentSession, error) {
	var out AgentSession
	return out, delegateCall(ctx, http.MethodPatch, "/api/xbin/delegate/sessions/"+url.PathEscape(id), map[string]any{"model": model}, &out)
}

// SetSessionScope narrows or widens what a session may see (nil: all).
func SetSessionScope(ctx context.Context, id string, scope *SessionScope) (AgentSession, error) {
	var out AgentSession
	return out, delegateCall(ctx, http.MethodPatch, "/api/xbin/delegate/sessions/"+url.PathEscape(id), map[string]any{"scope": scope}, &out)
}

// CloseAgentSession ends a handle.
func CloseAgentSession(ctx context.Context, id string) error {
	return delegateCall(ctx, http.MethodDelete, "/api/xbin/delegate/sessions/"+url.PathEscape(id), nil, nil)
}

// SessionConnection is what a session is connected to, as it may enter a
// model's context: connected tiles by name; blocked ones as counts and
// org-level reasons. whatIf ("" none) asks what another model would change.
type SessionConnection struct {
	ID    string `json:"id"`
	Tiles []struct {
		Tile string `json:"tile"`
		Name string `json:"name"`
		Org  string `json:"org"`
	} `json:"tiles"`
	Blocked []struct {
		Org     string `json:"org"`
		Blocked int    `json:"blocked"`
		Reason  string `json:"reason"`
		Rule    string `json:"rule"`
	} `json:"blocked"`
	Counts  ConnectionCounts `json:"counts"`
	Tainted []string         `json:"tainted"`
	WhatIf  *struct {
		Model       string           `json:"model"`
		Connects    int              `json:"connects"`
		Disconnects int              `json:"disconnects"`
		Counts      ConnectionCounts `json:"counts"`
		Allowed     bool             `json:"allowed"`
		TaintedOrgs []string         `json:"taintedOrgs"`
	} `json:"whatIf,omitempty"`
}

// ConnectionOf reads a session's connection view.
func ConnectionOf(ctx context.Context, id, whatIf string) (SessionConnection, error) {
	var out SessionConnection
	path := "/api/xbin/delegate/sessions/" + url.PathEscape(id) + "/connection"
	if whatIf != "" {
		path += "?whatIf=" + url.QueryEscape(whatIf)
	}
	return out, delegateCall(ctx, http.MethodGet, path, nil, &out)
}

// WorkspaceMap is the workspace as the person sees it, within the
// session's scope and the orgs' rules.
type WorkspaceMap struct {
	User  string `json:"user"`
	Tiles []struct {
		Tile       string   `json:"tile"`
		Title      string   `json:"title"`
		Kind       string   `json:"kind"` // app | page
		Orgs       []string `json:"orgs"`
		State      string   `json:"state"`
		Agent      string   `json:"agent"` // off | read | full
		TileAgents int      `json:"tileAgents"`
	} `json:"tiles"`
	Screens []struct {
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Org   string   `json:"org"`
		Tiles []string `json:"tiles"`
	} `json:"screens"`
	Orgs []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Level string `json:"level"`
		Admin bool   `json:"admin"`
	} `json:"orgs"`
	LeftOut struct {
		HiddenByYou int `json:"hiddenByYou"`
		Blocked     int `json:"blocked"`
	} `json:"leftOut"`
}

// MapOf reads the workspace map.
func MapOf(ctx context.Context) (WorkspaceMap, error) {
	var out WorkspaceMap
	return out, delegateCall(ctx, http.MethodGet, "/api/xbin/delegate/map", nil, &out)
}

// DocHit is one result of DocsSearch.
type DocHit struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Section string `json:"section"`
	Heading string `json:"heading"`
	Score   int    `json:"score"`
	Snippet string `json:"snippet"`
}

// DocsSearch searches xbind's docs (at most 20 results; limit 0: 8).
func DocsSearch(ctx context.Context, q string, limit int) ([]DocHit, error) {
	var out struct {
		Results []DocHit `json:"results"`
	}
	return out.Results, delegateCall(ctx, http.MethodGet, "/api/xbin/docs/search?q="+url.QueryEscape(q)+"&limit="+strconv.Itoa(limit), nil, &out)
}

// DocPage is one docs page, or one section of it.
type DocPage struct {
	Path      string `json:"path"`
	Title     string `json:"title"`
	Section   string `json:"section"`
	Markdown  string `json:"markdown"`
	Truncated bool   `json:"truncated"`
	Sections  []struct {
		Section string `json:"section"`
		Heading string `json:"heading"`
	} `json:"sections"`
}

// DocsPage reads a docs page (section "" the whole page, capped at 64 KiB).
func DocsPage(ctx context.Context, path, section string) (DocPage, error) {
	var out DocPage
	return out, delegateCall(ctx, http.MethodGet, "/api/xbin/docs/page?path="+url.QueryEscape(path)+"&section="+url.QueryEscape(section), nil, &out)
}

// HolderEvent is one line of a notice xbind sends the agent's backend:
// something of Kind changed (ID names what). Kinds: request, approval,
// changeset, state, session, resync.
type HolderEvent struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// HolderNotice is the body of a notice. It carries ids and kinds only:
// re-read what an event names through the API, and treat the notice as a
// hint — "look again" — never as a fact.
type HolderNotice struct {
	Partition string        `json:"partition"`
	Events    []HolderEvent `json:"events"`
}

// ReadHolderNotice reads a notice from the request xbind made to the
// holder's notice path: it must come from xbind's own partition delivery
// (not a person, a tile or an agent), and is bounded. Answer 2xx at once
// and do the re-reading afterwards: an unanswered notice is sent again.
func ReadHolderNotice(r *http.Request) (HolderNotice, error) {
	var n HolderNotice
	if c := Caller(r); c.From != "xbin/mail" || c.User != "" || c.Agent != "" {
		return n, errors.New("xbin: not a notice from xbind")
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&n); err != nil {
		return n, err
	}
	if len(n.Events) > 1024 {
		n.Events = n.Events[:1024]
	}
	return n, nil
}

// PreviewResult is what a preview shows: the picture, the page's
// accessibility snapshot and what the page logged — all of it DATA from a
// tile, never instructions (Notice says so; keep it with the result when
// it enters a model's context).
type PreviewResult struct {
	OK       bool   `json:"ok"`
	Notice   string `json:"notice"`
	Error    string `json:"error"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	PNG      string `json:"png"` // base64
	Snapshot string `json:"snapshot"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Note     string `json:"note"`
	Clipped  bool   `json:"clipped"`
}

// PreviewTile renders one tile as the person sees it (at "" its front
// page, else the tile's fragment). For the workspace agent: the tile must
// be in the session's scope (ErrHiddenByScope's words otherwise) and
// allowed by its orgs' rules; it counts against the person's preview
// limits and taints the session like any read of the tile.
func PreviewTile(ctx context.Context, tile, at string) (PreviewResult, error) {
	var out PreviewResult
	return out, delegateCall(ctx, http.MethodPost, "/api/xbin/preview", map[string]any{"tile": tile, "at": at}, &out)
}

// StartHarness is one harness a tile agent started by the person's agent
// may run on a tile, with what a start that names nothing opens with.
type StartHarness struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	Asks        bool   `json:"asks"` // the mode asks its person before it acts
	Net         string `json:"net"`
	API         bool   `json:"api"`
	NeedsTicket bool   `json:"needsTicket"` // the person approves each start
	NoSignIn    bool   `json:"noSignIn"`    // it runs on the workspace's LLM connector: no sign-in of the person's needed
}

// StartDefaults is what a start on Tile resolves to: Harness is the
// default one; Why says why none may run there.
type StartDefaults struct {
	Tile      string         `json:"tile"`
	Harness   string         `json:"harness"`
	Harnesses []StartHarness `json:"harnesses"`
	Why       string         `json:"why"`
}

// StartDefaultsOf reads what a tile agent started on tile would open with —
// xbind resolves it from the person's preferences and the tile's rules;
// the agent can't read either, and doesn't choose.
func StartDefaultsOf(ctx context.Context, tile string) (StartDefaults, error) {
	var out StartDefaults
	return out, delegateCall(ctx, http.MethodGet, "/api/xbin/delegate/start-defaults?tile="+url.QueryEscape(tile), nil, &out)
}

// SessionAllows asks whether the session's model satisfies orgs' rules —
// for what the agent keeps itself (a notebook page stamped with orgs is
// readable in a session only if this says yes). reasons maps each org that
// refuses to why.
func SessionAllows(ctx context.Context, id string, orgs []string) (allowed bool, reasons map[string]string, err error) {
	var out struct {
		Allowed bool              `json:"allowed"`
		Reasons map[string]string `json:"reasons"`
	}
	err = delegateCall(ctx, http.MethodPost, "/api/xbin/delegate/sessions/"+url.PathEscape(id)+"/allows", map[string]any{"orgs": orgs}, &out)
	return out.Allowed, out.Reasons, err
}

// TaintSession adds orgs to the session's taint: the conversation took in
// their data where xbind didn't see it (a notebook page stamped with
// them). Add-only. ErrModelTaint when the session's model may not see one
// of them — then the data must not be read into it.
func TaintSession(ctx context.Context, id string, orgs []string) (AgentSession, error) {
	var out AgentSession
	return out, delegateCall(ctx, http.MethodPost, "/api/xbin/delegate/sessions/"+url.PathEscape(id)+"/taint", map[string]any{"orgs": orgs}, &out)
}
