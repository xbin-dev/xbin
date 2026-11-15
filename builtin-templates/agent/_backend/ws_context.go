// ws_context.go — what a workspace agent's page says in fields instead of
// prose (API.md §Workspace mode): the context a person attaches to a
// message, the quota that stopped a model call, a refusal a tool call met,
// and the kind of question the agent asked. Each is additive beside the
// words the model reads: the page draws from the fields, the model from the
// sentences, and neither is parsed out of the other.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() { llmStatusHooks = append(llmStatusHooks, wsLLMStatus) }

// --- the context a person attaches to a message ------------------------------------

// msgAbout is what a person attached to a message: the tile it is about
// (Tile its path; Name what xbind calls it, Connected whether the session
// is connected to it — both resolved here, never the page's; Connected is
// absent when xbind couldn't be asked) or the screen.
type msgAbout struct {
	Tile      string `json:"tile,omitempty"`
	Name      string `json:"name,omitempty"`
	Screen    string `json:"screen,omitempty"`
	Connected *bool  `json:"connected,omitempty"`
}

var wsAboutPath = regexp.MustCompile(`^[^\x00-\x20\x7f"()\[\]]{1,512}$`)

// wsAboutOf is the context c as a workspace session's message carries it:
// the tile's path looked up in xbind's map of the session (its name and
// whether it is connected are xbind's answer), or the screen's name on one
// line. nil: no context, a malformed one, or not a workspace session.
func wsAboutOf(ctx context.Context, run *Run, c *msgAbout) *msgAbout {
	if c == nil || !wsMode() || run == nil || run.Origin != originWorkspace || run.ParentID != 0 {
		return nil
	}
	ag := projAg()
	if ag == nil {
		return nil
	}
	if tile := strings.Trim(strings.TrimSpace(c.Tile), "/"); tile != "" {
		if !wsAboutPath.MatchString(tile) {
			return nil
		}
		out := &msgAbout{Tile: tile}
		if s, err := ag.db.wsSession(run.ID); err == nil {
			var m wsMap
			if err := ag.wsWith(ctx, s, func(sid string) (err error) { m, err = wsX.Map(ctx, sid); return }); err == nil {
				connected := false // xbind answered: the map says whether it is
				for _, t := range m.Tiles {
					if t.Path == tile {
						connected, out.Name = true, clip(oneLineTitle(t.Title), 120)
					}
				}
				out.Connected = &connected
			}
		}
		return out
	}
	if screen := clip(oneLineTitle(c.Screen), 200); screen != "" {
		return &msgAbout{Screen: screen}
	}
	return nil
}

// wsAboutLine is the line the model reads before a message its person
// attached a context to: labelled as theirs, with the tile's name as data.
func wsAboutLine(m *Message) string {
	if m == nil || m.Role != "user" || len(m.Meta) == 0 || !strings.Contains(string(m.Meta), `"context"`) {
		return ""
	}
	var meta msgMeta
	if json.Unmarshal(m.Meta, &meta) != nil || meta.Context == nil {
		return ""
	}
	c := meta.Context
	q := func(s string) string { return strings.NewReplacer(`"`, "'", "[", "(", "]", ")").Replace(wsPlain(s)) }
	const head = "[context your person attached to this message: "
	switch {
	case c.Tile != "" && c.Connected != nil && !*c.Connected:
		return head + "the tile at " + q(c.Tile) + " — this session is not connected to it]"
	case c.Tile != "" && c.Name != "":
		return head + `the tile "` + q(c.Name) + `" (` + q(c.Tile) + ")]"
	case c.Tile != "":
		return head + "the tile " + q(c.Tile) + "]"
	case c.Screen != "":
		return head + `their screen "` + q(c.Screen) + `"]`
	}
	return ""
}

// --- the quota that stopped a model call ---------------------------------------------

const wsQuotaKey = "ws_quota"

// wsQuota is a stopping quota the connector refused a model call for, as
// GET /ws/state answers it: which limit (Quota: the connector's name, e.g.
// user.tokensPerDay; Source user | org | default | workspace, Org its id),
// what it counts and over what, the limit and what is used, when it
// resets, the limit in words, and who can raise it.
type wsQuota struct {
	Stopped    bool    `json:"stopped"`
	Quota      string  `json:"quota,omitempty"`
	Source     string  `json:"source,omitempty"`
	Org        string  `json:"org,omitempty"`
	Unit       string  `json:"unit,omitempty"`   // tokens | cost
	Window     string  `json:"window,omitempty"` // day | month
	Limit      float64 `json:"limit,omitempty"`
	Used       float64 `json:"used,omitempty"`
	Resets     string  `json:"resets,omitempty"` // RFC 3339
	LimitWords string  `json:"limitWords,omitempty"`
	Raise      string  `json:"raise,omitempty"`
	At         int64   `json:"atMs,omitempty"`
}

// wsQuotaOf reads the connector's 429 for a stopping quota (nil: it is
// something else).
func wsQuotaOf(body []byte) *wsQuota {
	var b struct {
		Error struct {
			Code, Type, Quota, Resets string
			Limit, Used               float64
		} `json:"error"`
	}
	if json.Unmarshal(body, &b) != nil || (b.Error.Code != "xbin_llm_quota_exceeded" && b.Error.Type != "insufficient_quota") {
		return nil
	}
	q := &wsQuota{Stopped: true, Quota: b.Error.Quota, Limit: b.Error.Limit, Used: b.Error.Used, Resets: b.Error.Resets, At: nowMs()}
	source, name, _ := strings.Cut(b.Error.Quota, ".")
	q.Source = source
	if org, ok := strings.CutPrefix(source, "org:"); ok {
		q.Source, q.Org = "org", org
	}
	switch {
	case strings.HasPrefix(name, "tokens"):
		q.Unit = "tokens"
	case strings.HasPrefix(name, "cost"):
		q.Unit = "cost"
	}
	switch {
	case strings.HasSuffix(name, "PerDay"):
		q.Window = "day"
	case strings.HasSuffix(name, "PerMonth"):
		q.Window = "month"
	}
	when := map[string]string{"day": " for today", "month": " for this month"}[q.Window]
	whose := map[string]string{"user": "your ", "org": "your organisation's ", "default": "your ", "workspace": "the workspace's "}[q.Source]
	switch q.Unit {
	case "tokens":
		q.LimitWords = whose + wsCount(q.Limit) + " tokens" + when
	case "cost":
		q.LimitWords = whose + fmt.Sprintf("$%.2f", q.Limit) + " budget" + when
	}
	q.Raise = "a workspace admin"
	if q.Source == "org" {
		q.Raise = "an admin of " + q.Org + ", or a workspace admin"
	}
	return q
}

// wsCount is a token count as people say it: 2M, 250k, 900.
func wsCount(n float64) string {
	switch {
	case n >= 1e6:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", n/1e6), "0"), ".") + "M"
	case n >= 1e3:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", n/1e3), "0"), ".") + "k"
	}
	return fmt.Sprintf("%.0f", n)
}

// wsLLMStatus (llmStatusHooks) keeps the quota that stopped a workspace
// conversation's model call, and forgets it when a call goes through.
func wsLLMStatus(req LLMRequest, status int, body []byte) {
	ag := projAg()
	if !wsMode() || ag == nil || ag.db == nil {
		return
	}
	switch {
	case status == 200:
		if ag.db.getSetting(wsQuotaKey) != "" {
			_ = ag.db.putSetting(wsQuotaKey, "")
		}
	case status == 429:
		if q := wsQuotaOf(body); q != nil {
			_ = ag.db.putSetting(wsQuotaKey, mustJSON(q))
		}
	}
}

// wsQuotaNow is the stopping quota in force (nil: none — never met, lifted
// by a call that went through, or past its reset).
func wsQuotaNow(d *DB) *wsQuota {
	raw := d.getSetting(wsQuotaKey)
	if raw == "" {
		return nil
	}
	var q wsQuota
	if json.Unmarshal([]byte(raw), &q) != nil || !q.Stopped {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, q.Resets); err == nil && !t.After(time.Now()) {
		return nil
	}
	return &q
}

// --- a refusal a tool call met -------------------------------------------------------

// wsNoTile: the session's map has no such tile. kind says why where xbind
// does (wsWhyNot): hidden, policy with the org's sentence, or
// not-connected — it isn't there or the person can't see it, which xbind
// answers alike.
type wsNoTile struct{ msg, kind, reason string }

func (e *wsNoTile) Error() string { return e.msg }

// wsWhyNot is why the tile at path is not in the session's map, as xbind
// describes a read of it without making one: outside the session's scope
// (hidden), kept out by an org's rule (policy: the sentence is that org's,
// as the connection view gives it), or neither said (not-connected).
func (ag *Agent) wsWhyNot(ctx context.Context, s *wsSession, path string) (kind, reason string) {
	kind = "not-connected"
	if !strings.Contains(path, "/") || !wsAboutPath.MatchString(path) {
		return kind, ""
	}
	var a wsAnswer
	err := ag.wsWith(ctx, s, func(sid string) (err error) {
		a, err = wsX.Call(ctx, sid, wsCall{Method: "GET", Path: "/api/" + path + "/", DryRun: true})
		return
	})
	code, why := "", ""
	var de *xbin.DelegateError
	switch {
	case errors.As(err, &de):
		code, why = de.Refusal, de.Message
	case err == nil && a.Dry != nil && !a.Dry.Allowed:
		code, why = a.Dry.Refusal, a.Dry.Error
	}
	switch code {
	case "hidden":
		return "hidden", why
	case "tile":
		for _, o := range s.Conn.Connection.Orgs {
			if o.State == "blocked" && o.Reason != "" && o.Reason == why {
				return "policy", why
			}
		}
	}
	return kind, ""
}

// wsRefusalKind is err as the page's card kind and xbind's sentence for
// it: hidden (outside the session's scope), policy (the tile's or its
// org's rule, with their sentence), cannot (not in the delegation cap),
// paused, off, slow (rate-limited), taint (the model may not hold that
// data), not-connected. "": not a refusal (a failure, a bad argument).
func wsRefusalKind(err error) (kind, reason string) {
	var te *wsTaintError
	var de *xbin.DelegateError
	var nt *wsNoTile
	msg := ""
	if errors.As(err, &de) {
		msg = de.Message
	}
	switch {
	case errors.As(err, &te):
		return "taint", orStr(te.Reason, te.Error())
	case errors.Is(err, xbin.ErrModelTaint):
		return "taint", orStr(de.Reason, msg)
	case errors.As(err, &nt):
		return orStr(nt.kind, "not-connected"), nt.reason
	case errors.Is(err, xbin.ErrAgentPaused):
		return "paused", msg
	case errors.Is(err, xbin.ErrAgentOff):
		return "off", msg
	case errors.Is(err, xbin.ErrHiddenByScope):
		return "hidden", msg
	case errors.Is(err, xbin.ErrPolicyRefused):
		return "policy", msg
	case errors.Is(err, xbin.ErrNotDelegated), errors.Is(err, xbin.ErrAdminRead):
		return "cannot", msg
	case errors.Is(err, xbin.ErrRateLimited):
		return "slow", msg
	}
	return "", ""
}

// wsDryKind is a dry run's refusal as a card kind.
func wsDryKind(d *wsDry) string {
	switch d.Refusal {
	case "hidden":
		return "hidden"
	case "not-delegated", "admin-read":
		return "cannot"
	case "paused":
		return "paused"
	case "workspace-off", "no-sandbox", "off":
		return "off"
	case "rate-limited":
		return "slow"
	case "model_taint":
		return "taint"
	}
	return "policy" // "tile": the tile's or its org's rule
}

// wsWords is a refusal in the words a tool answers (wsRefusal), still the
// error it was underneath: what it is stays readable to wsRefusalKind.
type wsWords struct {
	msg string
	err error
}

func (e *wsWords) Error() string { return e.msg }
func (e *wsWords) Unwrap() error { return e.err }

func wsWorded(err error) error { return &wsWords{msg: wsRefusal(err), err: err} }

// wsToolErr is err as the tool call in ctx answers it, recorded for the
// page's card when it is a refusal.
func (ag *Agent) wsToolErr(ctx context.Context, s *wsSession, err error, tile string) error {
	kind, reason := wsRefusalKind(err)
	ag.wsNoteRefusal(ctx, s, kind, reason, tile)
	return errors.New(wsRefusal(err))
}

// wsDryRefused records a dry run's refusal for the page's card.
func (ag *Agent) wsDryRefused(ctx context.Context, s *wsSession, d *wsDry, tile string) {
	if d != nil && !d.Allowed {
		ag.wsNoteRefusal(ctx, s, wsDryKind(d), orStr(d.Error, d.Refusal), orStr(d.Tile, tile))
	}
}

// wsNoteRefusal records a refusal the tool call in ctx met, for the page's
// card (GET …/cards → refusals); the model reads the tool's own words.
func (ag *Agent) wsNoteRefusal(ctx context.Context, s *wsSession, kind, reason, tile string) {
	if kind == "" {
		return
	}
	call := toolCallOf(ctx)
	tool := ag.db.wsCallName(s.Run, call)
	if _, err := ag.db.q.Exec(`INSERT INTO ws_refusals (run_id, call_id, tool, kind, reason, tile, created_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.Run, call, tool, kind, clip(oneLineTitle(reason), 400), tile, nowMs()); err == nil {
		ag.wsChanged(s.Run, "refusal")
	}
}

// wsCallName is the tool a call id of run names ("" when unknown).
func (d *DB) wsCallName(runID int64, call string) string {
	var raw string
	if call == "" || d.q.QueryRow(`SELECT tool_calls FROM messages WHERE run_id=? AND role='assistant' AND instr(tool_calls, ?) > 0 ORDER BY seq DESC LIMIT 1`, runID, call).Scan(&raw) != nil {
		return ""
	}
	var calls []toolCall
	_ = json.Unmarshal([]byte(raw), &calls)
	for _, tc := range calls {
		if tc.ID == call {
			return tc.Function.Name
		}
	}
	return ""
}

// --- the kind of question the agent asked --------------------------------------------

// The kinds of question a workspace agent asks (ask_user's kind): choices
// built from what it found; redirect — the request was about code, and the
// ways forward are the tile's own agent or an outcome; cant — it can't do
// what was asked, and the choices are the nearest things it can.
var wsAskKinds = []string{"choices", "redirect", "cant"}

// wsAsk is the question a session waits on, in fields.
type wsAsk struct {
	CallID   string   `json:"callId,omitempty"`
	Kind     string   `json:"kind"` // choices | redirect | cant | question (no choices)
	Question string   `json:"question"`
	Choices  []string `json:"choices,omitempty"`
	Tile     string   `json:"tile,omitempty"` // the tile a redirect concerns
}

// wsAskOf is the latest ask_user call of run (nil: none), its kind made
// one of the four: an unknown kind is read by what the call carries.
func wsAskOf(d *DB, runID int64) *wsAsk {
	var raw string
	if d.q.QueryRow(`SELECT tool_calls FROM messages WHERE run_id=? AND role='assistant' AND tool_calls LIKE '%"ask_user"%' ORDER BY seq DESC LIMIT 1`, runID).Scan(&raw) != nil {
		return nil
	}
	var calls []toolCall
	if json.Unmarshal([]byte(raw), &calls) != nil {
		return nil
	}
	for _, tc := range calls {
		if tc.Function.Name != "ask_user" {
			continue
		}
		var a struct {
			Question, Kind, Tile string
			Choices              []string
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &a)
		out := &wsAsk{CallID: tc.ID, Kind: a.Kind, Question: a.Question, Choices: a.Choices, Tile: strings.Trim(strings.TrimSpace(a.Tile), "/")}
		if !hasStr(wsAskKinds, out.Kind) {
			out.Kind = "choices"
		}
		if len(out.Choices) == 0 && out.Kind == "choices" {
			out.Kind = "question"
		}
		if out.Kind != "redirect" {
			out.Tile = ""
		}
		return out
	}
	return nil
}
