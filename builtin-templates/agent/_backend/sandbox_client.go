// sandbox_client.go — typed calls to a sandbox manager (docs/sandbox-manager.md,
// protocol 1): one method per contract route the agent uses. Every call
// names the person the agent acts for in Sbx-User when there is one (never an
// X-XBin-* header — xbind strips those and sets X-XBin-From itself), and every
// refusal comes back as an *sbxError: the contract's refusal for programs,
// and an Error() text a model can act on.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	sbxCallTimeout  = 60 * time.Second // one plain call
	sbxHelloTimeout = 10 * time.Second
	sbxSlack        = 30 * time.Second // on top of a call's own wait or timeout
	sbxJSONMax      = 32 << 20         // a JSON answer (a run's two streams are ≤ 1 MiB each)
)

// sbxConn is one manager, called for one person.
type sbxConn struct {
	M    sbxManager
	User string // Sbx-User: the person the agent acts for ("" = the agent itself)
}

// sbxDial is a bound manager, called for user. A provider that is no longer
// bound is an *sbxError with refusal "unbound".
func sbxDial(provider, user string) (*sbxConn, error) {
	m, ok := boundManager(provider)
	if !ok {
		return nil, &sbxError{Provider: provider, Refusal: "unbound", Msg: "this sandbox manager is no longer bound to the agent"}
	}
	return &sbxConn{M: m, User: user}, nil
}

// sbxDialRef is the manager a sandbox reference names, and the sandbox's id
// there.
func sbxDialRef(ref, user string) (*sbxConn, string, error) {
	provider, id, ok := splitSandboxRef(ref)
	if !ok {
		return nil, "", &sbxError{Refusal: "invalid", Msg: fmt.Sprintf("%q is not a sandbox reference (<manager>|<id>)", ref)}
	}
	c, err := sbxDial(provider, user)
	return c, id, err
}

// --- errors --------------------------------------------------------------------

// sbxError is a manager's refusal (the contract's error body), or the agent's
// own for a manager it can't reach or no longer has bound.
type sbxError struct {
	Provider     string
	Status       int    // the HTTP status (0: no answer)
	Refusal      string // the contract's enum, or unbound | unreachable
	Msg          string // the manager's own words
	State        string // refusal state: where the sandbox stands
	ETag         string // refusal precondition: the file's current etag
	RetryAfterMs int
}

// Error is the refusal in words a model can act on.
func (e *sbxError) Error() string {
	var b strings.Builder
	if e.Provider != "" {
		b.WriteString("sandbox manager " + e.Provider + ": ")
	}
	msg := strings.TrimSpace(e.Msg)
	if msg == "" {
		msg = e.Refusal
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(e.Status)
		}
	}
	b.WriteString(msg)
	hint := map[string]string{
		"not-found":    "it may have been deleted, or it isn't shared with this agent",
		"not-allowed":  "the person this conversation acts for may not do that",
		"precondition": "the file changed since it was read — read it again, then edit",
		"too-large":    "over the manager's limit — read a range, or move it as a tar",
		"limit":        "too many sandboxes or running commands — stop some, or wait",
		"unsupported":  "this manager doesn't offer that",
		"lost":         "the command is gone (its sandbox restarted)",
		"unavailable":  "the manager is down or still starting — try again shortly",
		"unreachable":  "the manager didn't answer — it may be stopped or unbound",
		"unbound":      "bind it again, or pick another sandbox",
	}[e.Refusal]
	if e.Refusal == "state" && e.State != "" {
		hint = "the sandbox is " + e.State
		switch e.State {
		case "archived":
			hint += " — thaw it first"
		case "stopped", "stopping":
			hint += " — start it first"
		}
	}
	// a manager's answer gets a hint; the agent's own words stand alone
	if e.Status == 0 && e.Refusal != "unbound" && e.Refusal != "unreachable" {
		hint = ""
	}
	if hint != "" && !strings.Contains(msg, hint) {
		b.WriteString(" (" + hint + ")")
	}
	if e.RetryAfterMs > 0 {
		fmt.Fprintf(&b, "; retry in %.1fs", float64(e.RetryAfterMs)/1000)
	}
	return b.String()
}

// sbxRefusal is err's refusal ("" when it isn't a manager's).
func sbxRefusal(err error) string {
	var e *sbxError
	if errors.As(err, &e) {
		return e.Refusal
	}
	return ""
}

// statusRefusal names the refusal a status means when the body didn't say
// (xbind's own answers: a missing grant, a stopped backend).
func statusRefusal(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "not-allowed"
	case http.StatusNotFound:
		return "not-found"
	case http.StatusConflict:
		return "state"
	case http.StatusPreconditionFailed:
		return "precondition"
	case http.StatusRequestEntityTooLarge:
		return "too-large"
	case http.StatusTooManyRequests:
		return "limit"
	case http.StatusNotImplemented:
		return "unsupported"
	case http.StatusGone:
		return "lost"
	}
	if status >= 500 {
		return "unavailable"
	}
	return "invalid"
}

func decodeSbxError(provider string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Error        string `json:"error"`
		Detail       string `json:"detail"`
		Refusal      string `json:"refusal"`
		State        string `json:"state"`
		ETag         string `json:"etag"`
		RetryAfterMs int    `json:"retryAfterMs"`
	}
	_ = json.Unmarshal(raw, &body)
	e := &sbxError{Provider: provider, Status: resp.StatusCode, Refusal: body.Refusal, Msg: body.Error,
		State: body.State, ETag: strings.Trim(body.ETag, `"`), RetryAfterMs: body.RetryAfterMs}
	if e.Refusal == "" {
		e.Refusal = statusRefusal(resp.StatusCode)
	}
	if e.Msg == "" {
		e.Msg = strings.TrimSpace(body.Detail)
	}
	if e.Msg == "" && body.Error == "" && len(raw) > 0 && raw[0] != '{' {
		e.Msg = strings.TrimSpace(clip(string(raw), 300))
	}
	return e
}

// --- plumbing ------------------------------------------------------------------

// open sends one request and hands back a successful response (the caller
// closes its body). Anything but 2xx is an *sbxError; a context the caller
// ended is its own error.
func (c *sbxConn) open(ctx context.Context, method, route string, q url.Values, body io.Reader, ctype string) (*http.Response, error) {
	u := c.M.URL + "/sbx" + route
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, &sbxError{Provider: c.M.Provider, Refusal: "invalid", Msg: err.Error()}
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if c.User != "" {
		req.Header.Set("Sbx-User", c.User)
	}
	resp, err := sbxClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, &sbxError{Provider: c.M.Provider, Refusal: "unreachable", Msg: err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, decodeSbxError(c.M.Provider, resp)
	}
	return resp, nil
}

// call is one JSON round trip: in (nil: no body) as the body, the answer
// decoded into out (nil: dropped), within timeout (0: the caller's context).
func (c *sbxConn) call(ctx context.Context, method, route string, q map[string]string, in, out any, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var body io.Reader
	ctype := ""
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ctype = bytes.NewReader(raw), "application/json"
	}
	resp, err := c.open(ctx, method, route, sbxQuery(q), body, ctype)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, sbxJSONMax)).Decode(out); err != nil {
		return &sbxError{Provider: c.M.Provider, Status: resp.StatusCode, Refusal: "unavailable", Msg: "a garbled answer: " + err.Error()}
	}
	return nil
}

// sbxQuery builds a query string, leaving out empty values.
func sbxQuery(q map[string]string) url.Values {
	if len(q) == 0 {
		return nil
	}
	v := url.Values{}
	for k, x := range q {
		if x != "" {
			v.Set(k, x)
		}
	}
	return v
}

func sbxPath(id string, rest ...string) string {
	p := "/sandboxes/" + url.PathEscape(id)
	for _, r := range rest {
		p += "/" + url.PathEscape(r)
	}
	return p
}

// --- the sandbox resource ------------------------------------------------------

// sbxSandbox is the contract's sandbox. Raw keeps it as the manager sent it,
// so fields this agent doesn't know yet still reach its UI (protocol 1 grows
// by addition).
type sbxSandbox struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	StateDetail string `json:"stateDetail"`
	Image       struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"image"`
	Size         sbxSize `json:"size"`
	Isolation    string  `json:"isolation"`
	Egress       string  `json:"egress"`
	EgressNext   string  `json:"egressNext,omitempty"` // a PATCHed egress for its next start
	EgressDetail string  `json:"egressDetail"`
	Owner        struct {
		User     string `json:"user"`
		Via      string `json:"via"`
		Asserted bool   `json:"asserted"`
	} `json:"owner"`
	Visibility    string            `json:"visibility"`
	Members       []string          `json:"members"`
	Shares        json.RawMessage   `json:"shares"`
	Shared        bool              `json:"shared"`
	Labels        map[string]string `json:"labels"`
	Workdir       string            `json:"workdir"`
	Home          string            `json:"home"`
	User          string            `json:"user"`
	Shell         string            `json:"shell"`
	Caps          []string          `json:"caps"`
	Created       int64             `json:"created"`
	LastActive    int64             `json:"lastActive"`
	AutoStopMin   int               `json:"autoStopMin"`
	Version       int               `json:"version"`
	RestartNeeded bool              `json:"restartNeeded,omitempty"`

	Raw json.RawMessage `json:"-"`
}

func (s *sbxSandbox) UnmarshalJSON(b []byte) error {
	type plain sbxSandbox
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = sbxSandbox(p)
	s.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// view is the resource as a map (the manager's fields, all of them).
func (s *sbxSandbox) view() map[string]any {
	out := map[string]any{}
	if len(s.Raw) > 0 && json.Unmarshal(s.Raw, &out) == nil {
		return out
	}
	raw, _ := json.Marshal(struct {
		*sbxSandbox
		Raw json.RawMessage `json:"-"`
	}{sbxSandbox: s})
	_ = json.Unmarshal(raw, &out)
	return out
}

// egressRank orders egress by what it reaches; anything unknown (or
// missing) counts as open.
func egressRank(e string) int {
	switch e {
	case "none":
		return 0
	case "internet":
		return 1
	}
	return 2
}

// effectiveEgress is the egress the firewall checks: the less restrictive of
// what the sandbox has now and what it takes at its next start (a stopped
// sandbox starts on an exec) — none, internet or open, an unknown or
// missing one counting as open.
func (s *sbxSandbox) effectiveEgress() string {
	e := s.Egress
	if s.EgressNext != "" && egressRank(s.EgressNext) > egressRank(e) {
		e = s.EgressNext
	}
	if egressRank(e) == 2 {
		return "open"
	}
	return e
}

// hasCap: a sandbox may offer fewer capabilities than its manager.
func (s *sbxSandbox) hasCap(capName string) bool {
	if s.Caps == nil {
		return true // the manager's apply
	}
	for _, c := range s.Caps {
		if c == capName {
			return true
		}
	}
	return false
}

// --- sandboxes -------------------------------------------------------------------

// sbxCreate is POST /sbx/sandboxes.
type sbxCreate struct {
	Name       string            `json:"name"`
	Image      string            `json:"image,omitempty"`
	Size       string            `json:"size,omitempty"`
	Egress     string            `json:"egress,omitempty"`
	Visibility string            `json:"visibility,omitempty"`
	Members    []string          `json:"members,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	ClientID   string            `json:"clientId,omitempty"`
	Start      *bool             `json:"start,omitempty"`
	From       *sbxFrom          `json:"from,omitempty"`
}

type sbxFrom struct {
	Sandbox  string `json:"sandbox"`
	Snapshot string `json:"snapshot,omitempty"`
}

// sbxPatch is PATCH /sbx/sandboxes/{id}: nil fields are left alone.
type sbxPatch struct {
	Name        *string            `json:"name,omitempty"`
	Visibility  *string            `json:"visibility,omitempty"`
	Members     *[]string          `json:"members,omitempty"`
	Shares      json.RawMessage    `json:"shares,omitempty"`
	Labels      *map[string]string `json:"labels,omitempty"`
	Egress      *string            `json:"egress,omitempty"`
	Size        *string            `json:"size,omitempty"`
	AutoStopMin *int               `json:"autoStopMin,omitempty"`
	Version     *int               `json:"version,omitempty"`
}

// List is this agent's partition at the manager.
func (c *sbxConn) List(ctx context.Context) ([]*sbxSandbox, error) {
	var out struct {
		Sandboxes []*sbxSandbox `json:"sandboxes"`
	}
	err := c.call(ctx, "GET", "/sandboxes", nil, nil, &out, sbxCallTimeout)
	return out.Sandboxes, err
}

func (c *sbxConn) Create(ctx context.Context, req sbxCreate) (*sbxSandbox, error) {
	var out sbxSandbox
	return &out, c.call(ctx, "POST", "/sandboxes", nil, req, &out, sbxCallTimeout)
}

func (c *sbxConn) Get(ctx context.Context, id string) (*sbxSandbox, error) {
	var out sbxSandbox
	return &out, c.call(ctx, "GET", sbxPath(id), nil, nil, &out, sbxCallTimeout)
}

func (c *sbxConn) Patch(ctx context.Context, id string, p sbxPatch) (*sbxSandbox, error) {
	var out sbxSandbox
	return &out, c.call(ctx, "PATCH", sbxPath(id), nil, p, &out, sbxCallTimeout)
}

func (c *sbxConn) Delete(ctx context.Context, id string) error {
	return c.call(ctx, "DELETE", sbxPath(id), nil, nil, nil, sbxCallTimeout)
}

// Lifecycle is start | stop | archive | thaw, waiting up to wait seconds for
// the transition (0: not at all); startAfterThaw is thaw's {start}.
func (c *sbxConn) Lifecycle(ctx context.Context, id, action string, wait int, startAfterThaw bool) (*sbxSandbox, error) {
	switch action {
	case "start", "stop", "archive", "thaw":
	default:
		return nil, &sbxError{Provider: c.M.Provider, Refusal: "invalid", Msg: "no action " + action}
	}
	q := map[string]string{}
	if wait > 0 {
		q["wait"] = strconv.Itoa(wait)
	}
	var in any
	if action == "thaw" && startAfterThaw {
		in = map[string]bool{"start": true}
	}
	var out sbxSandbox
	return &out, c.call(ctx, "POST", sbxPath(id, action), q, in, &out, time.Duration(wait)*time.Second+sbxCallTimeout)
}

// --- commands ----------------------------------------------------------------------

// sbxRunReq is POST /sbx/sandboxes/{id}/run.
type sbxRunReq struct {
	Cmd       string            `json:"cmd,omitempty"`
	Argv      []string          `json:"argv,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Stdin     string            `json:"stdin,omitempty"`
	TimeoutMs int               `json:"timeoutMs,omitempty"`
	MaxOutput int               `json:"maxOutput,omitempty"`
	Merge     bool              `json:"merge,omitempty"`
}

// sbxOutput is a stream of a run: its head and tail, elided bytes between.
type sbxOutput struct {
	Head   string `json:"head"`
	Tail   string `json:"tail"`
	Elided int64  `json:"elided"`
	Bytes  int64  `json:"bytes"`
}

type sbxRunResult struct {
	ExitCode *int       `json:"exitCode"` // nil: a signal ended it (Signal)
	Signal   string     `json:"signal"`
	TimedOut bool       `json:"timedOut"`
	Ms       int64      `json:"ms"`
	Stdout   *sbxOutput `json:"stdout,omitempty"`
	Stderr   *sbxOutput `json:"stderr,omitempty"`
	Output   *sbxOutput `json:"output,omitempty"` // with merge
}

// Run runs a command and waits for it; the manager kills its process group
// if ctx ends first.
func (c *sbxConn) Run(ctx context.Context, id string, req sbxRunReq) (*sbxRunResult, error) {
	var out sbxRunResult
	// TERM, then KILL 5 s later, at the command's timeout: wait that out too
	limit := time.Duration(req.TimeoutMs)*time.Millisecond + 5*time.Second + sbxSlack
	if req.TimeoutMs <= 0 {
		limit = 0 // the manager's default applies; ctx bounds it
	}
	return &out, c.call(ctx, "POST", sbxPath(id, "run"), nil, req, &out, limit)
}

// sbxExecReq is POST /sbx/sandboxes/{id}/execs.
type sbxExecReq struct {
	Cmd       string            `json:"cmd,omitempty"`
	Argv      []string          `json:"argv,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TTY       bool              `json:"tty,omitempty"`
	Rows      int               `json:"rows,omitempty"`
	Cols      int               `json:"cols,omitempty"`
	Stdin     bool              `json:"stdin,omitempty"`
	TimeoutMs int               `json:"timeoutMs,omitempty"`
	Label     string            `json:"label,omitempty"`
	ClientID  string            `json:"clientId,omitempty"`
}

// sbxExec is a background command.
type sbxExec struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Cmd      string   `json:"cmd"`
	Argv     []string `json:"argv"`
	Cwd      string   `json:"cwd"`
	TTY      bool     `json:"tty"`
	State    string   `json:"state"` // running | exited | killed | lost
	ExitCode *int     `json:"exitCode"`
	Signal   string   `json:"signal"`
	Started  int64    `json:"started"`
	Ended    int64    `json:"ended"`
	Total    int64    `json:"total"`
	ClientID string   `json:"clientId,omitempty"`
}

// sbxChunk is a piece of an exec's combined output.
type sbxChunk struct {
	Start     int64  `json:"start"` // > since: the bytes between were dropped from the ring
	End       int64  `json:"end"`   // resume from here
	Total     int64  `json:"total"`
	RingStart int64  `json:"ringStart"`
	Data      string `json:"data"`
	Encoding  string `json:"encoding"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exitCode"`
	Signal    string `json:"signal"`
}

func (c *sbxConn) ExecStart(ctx context.Context, id string, req sbxExecReq) (*sbxExec, error) {
	var out sbxExec
	return &out, c.call(ctx, "POST", sbxPath(id, "execs"), nil, req, &out, sbxCallTimeout)
}

func (c *sbxConn) ExecList(ctx context.Context, id string) ([]*sbxExec, error) {
	var out struct {
		Execs []*sbxExec `json:"execs"`
	}
	err := c.call(ctx, "GET", sbxPath(id, "execs"), nil, nil, &out, sbxCallTimeout)
	return out.Execs, err
}

func (c *sbxConn) ExecGet(ctx context.Context, id, eid string) (*sbxExec, error) {
	var out sbxExec
	return &out, c.call(ctx, "GET", sbxPath(id, "execs", eid), nil, nil, &out, sbxCallTimeout)
}

// ExecOutput reads from byte offset since, up to maxBytes (0: the manager's
// 64 KiB), long-polling up to waitMs (≤ 30000) while there is nothing new
// and the command still runs. base64 asks for exact bytes.
func (c *sbxConn) ExecOutput(ctx context.Context, id, eid string, since int64, maxBytes, waitMs int, base64 bool) (*sbxChunk, error) {
	q := map[string]string{"since": strconv.FormatInt(since, 10)}
	if maxBytes > 0 {
		q["max"] = strconv.Itoa(maxBytes)
	}
	if waitMs > 0 {
		q["waitMs"] = strconv.Itoa(waitMs)
	}
	if base64 {
		q["encoding"] = "base64"
	}
	var out sbxChunk
	return &out, c.call(ctx, "GET", sbxPath(id, "execs", eid, "output"), q, nil, &out,
		time.Duration(waitMs)*time.Millisecond+sbxSlack)
}

// ExecStdin writes to a command started with {stdin: true}; eof closes it.
func (c *sbxConn) ExecStdin(ctx context.Context, id, eid string, data []byte, eof bool) error {
	q := url.Values{}
	if eof {
		q.Set("eof", "1")
	}
	ctx, cancel := context.WithTimeout(ctx, sbxCallTimeout)
	defer cancel()
	resp, err := c.open(ctx, "POST", sbxPath(id, "execs", eid, "stdin"), q, bytes.NewReader(data), "application/octet-stream")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ExecSignal sends INT | TERM | KILL | HUP to the command's process group
// (group false: the process alone).
func (c *sbxConn) ExecSignal(ctx context.Context, id, eid, signal string, group bool) error {
	return c.call(ctx, "POST", sbxPath(id, "execs", eid, "signal"), nil,
		map[string]any{"signal": signal, "group": group}, nil, sbxCallTimeout)
}

// ExecDelete kills the command's group and forgets it.
func (c *sbxConn) ExecDelete(ctx context.Context, id, eid string) error {
	return c.call(ctx, "DELETE", sbxPath(id, "execs", eid), nil, nil, nil, sbxCallTimeout)
}
