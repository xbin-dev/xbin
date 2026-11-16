// ws_xbin.go — everything the workspace agent asks of xbind, behind one
// interface (docs/workspace-agent.md; docs/protocol/delegation.md). The
// agent acts as its person through xbind's delegation: xbind decides what
// a call reaches, keeps every approval and applies every admin change —
// this backend asks, waits and reads. Each method below is one question or
// one request to xbind; the shapes on the wire are decoded in one place
// (ws_xbin_http.go), and the tests answer them from memory (the fake in
// ws_fake_test.go). Nothing else in the profile builds a URL.
//
// sid is the conversation's handle (wsHandle.ID): xbind judges every call
// by its scope and its model, and marks what the conversation has read.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wsXbin is xbind as the workspace agent uses it.
type wsXbin interface {
	// A conversation's handle: made with it, changed with its scope and
	// model, deleted with it. UpdateSession answers *wsTaintError when the
	// conversation holds data the new model may not see.
	OpenSession(ctx context.Context, scope *wsScope, model string) (wsHandle, error)
	Session(ctx context.Context, sid string) (wsHandle, error)
	UpdateSession(ctx context.Context, sid string, ch wsHandleChange) (wsHandle, error)
	CloseSession(ctx context.Context, sid string) error
	// Connection is what the handle is connected to, as it may enter a
	// model's context: totals and each org's state and reason — never a
	// blocked tile's name (the person's page reads the full view from xbind
	// itself). With whatIf, what moving to that model would change, as
	// counts, and whether the conversation's data allows the move.
	Connection(ctx context.Context, sid, whatIf string) (wsConnection, *wsWhatIf, error)
	// Allows asks whether the session's model is allowed by every one of
	// orgs (reasons: why not, by org); Taint records that the conversation
	// now holds data of orgs — what reading a notebook page stamped with
	// them does.
	Allows(ctx context.Context, sid string, orgs []string) (allowed bool, reasons map[string]string, err error)
	Taint(ctx context.Context, sid string, orgs []string) error
	// Models are the models the person may put a conversation on.
	Models(ctx context.Context) (wsModels, error)
	// State is delegation's switches for this person (paused, the admin opt-in).
	State(ctx context.Context) (wsState, error)

	// Looking: reads at the person's level, filtered by scope and policy.
	Map(ctx context.Context, sid string) (wsMap, error)
	Tile(ctx context.Context, sid, tile string) (wsTile, error)
	Logs(ctx context.Context, sid, tile string, lines int) (string, error)
	DocsSearch(ctx context.Context, sid, q string, limit int) ([]wsDocHit, error)
	DocsPage(ctx context.Context, sid, path, section string) (wsDocPage, error)
	CodeTree(ctx context.Context, sid, tile string) ([]wsCodeFile, error)
	CodeFile(ctx context.Context, sid, tile, file string) (wsCodeText, error)

	// One call, exactly as it will be sent: Call makes it (with c.DryRun it
	// describes it and runs nothing; with c.Ticket it runs under an approved
	// request), Ask files it for the person's approval. A tile's API, a
	// tile agent's start and a tile's creation all go this way.
	Call(ctx context.Context, sid string, c wsCall) (wsAnswer, error)
	Ask(ctx context.Context, sid string, c wsCall, summary string) (wsRequest, error)
	Request(ctx context.Context, sid, id string) (wsRequest, error)
	Withdraw(ctx context.Context, sid, id string) error
	// Standing are the person's standing approvals for this agent.
	Standing(ctx context.Context, sid string) ([]wsStanding, error)

	// Starting a tile agent and making a tile from a template are calls of
	// the same kind, built here so the call asked for and the call made are
	// the same bytes: AskX files it for approval; X makes it under a ticket
	// (or, with ticket "", where the person's preferences let it through),
	// and with dry describes it instead.
	// StartDefaults is what a tile agent started in tile would run on:
	// xbind resolves the harness, mode and network from the person's
	// preferences and the tile's org's rules. It also lists the harnesses
	// the person may pick instead, and says whether a start needs a ticket.
	StartDefaults(ctx context.Context, sid, tile string) (wsStartDefaults, error)
	AskAgentStart(ctx context.Context, sid string, a wsAgentStart, summary string) (wsRequest, error)
	AgentStart(ctx context.Context, sid string, a wsAgentStart, ticket string, dry bool) (wsAgent, *wsDry, error)
	AskTileCreate(ctx context.Context, sid string, t wsTileCreate, summary string) (wsRequest, error)
	TileCreate(ctx context.Context, sid string, t wsTileCreate, ticket string, dry bool) (string, *wsDry, error)

	// Tile agents the handle started (and the person's others, read-only).
	Agents(ctx context.Context, sid string) ([]wsAgent, error)
	Agent(ctx context.Context, sid, id string) (wsAgent, error)
	AgentSay(ctx context.Context, sid, id, text string) (outcome string, err error)
	AgentCancel(ctx context.Context, sid, id string) error
	AgentReport(ctx context.Context, sid, id string) (wsAgentReport, error)
	Templates(ctx context.Context, sid string) ([]wsTemplate, error)

	// Admin change sets (an admin who opted in) and previews.
	Propose(ctx context.Context, sid, title string, items []xbin.Change) (wsChangeSet, error)
	ChangeSet(ctx context.Context, sid, id string) (wsChangeSet, error)
	ChangeSets(ctx context.Context, sid string) ([]wsChangeSet, error)
	Preview(ctx context.Context, sid string, p wsPreviewAsk) (wsPreview, error)
}

// wsX is xbind for this process: the delegate client, or a test's fake.
var wsX wsXbin = wsHTTP{}

// --- shapes ----------------------------------------------------------------------

// wsScope is where a conversation looks: orgs and tiles its person picked.
// nil: everything the rules allow.
type wsScope struct {
	Orgs  []string `json:"orgs"`
	Tiles []string `json:"tiles"`
}

// wsHandle is a conversation's handle as xbind holds it.
type wsHandle struct {
	ID         string       `json:"id"`
	Scope      *wsScope     `json:"scope"`
	Model      string       `json:"model"`
	Tainted    []string     `json:"tainted,omitempty"` // the orgs whose data the conversation holds
	Connection wsConnection `json:"connection"`
}

// wsHandleChange is a change to a handle: its scope (SetScope with a nil
// Scope: everything allowed again), its model, or both.
type wsHandleChange struct {
	SetScope bool
	Scope    *wsScope
	Model    string
}

// wsConnection is the connection view the agent gets (xbind's redacted
// view: its totals, and one row an org).
type wsConnection struct {
	Counts wsCounts    `json:"counts"`
	Orgs   []wsOrgConn `json:"orgs,omitempty"`
	Model  *wsModelRef `json:"model,omitempty"` // the session's model, with xbind's private mark
}

// wsCounts are a connection's totals: Tiles and Orgs count what is
// connected, NotConnected every other tile (Hidden + Blocked + NoAccess).
type wsCounts struct {
	Tiles        int    `json:"tiles"`
	Orgs         int    `json:"orgs"`
	NotConnected int    `json:"notConnected"`
	Hidden       int    `json:"hidden"`
	Blocked      int    `json:"blocked"`
	NoAccess     int    `json:"noAccess"`
	Summary      string `json:"summary,omitempty"` // xbind's sentence: "14 tiles · 3 orgs connected · 6 not connected"
}

// wsOrgConn is one org's row: its own answer for this session — its rules
// on the session's model, and the session's scope — whatever its tiles.
// ID "workspace" is the tiles in no org.
type wsOrgConn struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`   // org | workspace
	State     string `json:"state,omitempty"`  // connected | hidden | blocked | no-access
	Reason    string `json:"reason,omitempty"` // xbind's words for a block
	Tiles     int    `json:"tiles"`
	Connected int    `json:"connected"`
}

// wsWhatIf is what moving a session to Model would change: counts only,
// and whether every org whose data the conversation holds allows it.
type wsWhatIf struct {
	Model       string   `json:"model"`
	Connects    int      `json:"connects"`
	Disconnects int      `json:"disconnects"`
	Counts      wsCounts `json:"counts"`
	Allowed     bool     `json:"allowed"`
	TaintedOrgs []string `json:"taintedOrgs,omitempty"` // the held orgs that don't allow it
}

// connects: org is connected in c — its row says so. An org xbind lists no
// row for is one the person has nothing to do with any more.
func (c wsConnection) connects(org string) bool {
	if org == "" {
		return true
	}
	for _, o := range c.Orgs {
		if o.ID == org {
			return o.State == "connected"
		}
	}
	return false
}

// wsTaintError: the conversation already holds data from Orgs, which the
// model asked for may not see (409 model_taint).
type wsTaintError struct {
	Model  string
	Orgs   []string
	Reason string
}

func (e *wsTaintError) Error() string {
	return fmt.Sprintf("this conversation can't move to %s: it already holds data from %s — %s", e.Model, strings.Join(e.Orgs, ", "), orStr(e.Reason, "that model isn't allowed there"))
}

type wsModels struct {
	Default string    `json:"default"` // the connector's model for the agent role
	Models  []wsModel `json:"models"`
}

// wsModel is a model the person may put a session on. Upstream is the
// connector's upstream it runs on. Private — the upstream keeps prompts
// inside the organisation — and Where ("your organisation's own server",
// "hosted") are set only when xbind says: nil and "" mean it didn't.
type wsModel struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Private  *bool  `json:"private,omitempty"`
	Where    string `json:"where,omitempty"`
}

// wsWhere is where a model runs, in the page's words.
func wsWhere(private bool) string {
	if private {
		return "your organisation's own server"
	}
	return "hosted"
}

// wsModelRef is the model a connection view was computed for, as xbind
// names it: its upstream, and whether that upstream is private.
type wsModelRef struct {
	Upstream string `json:"upstream,omitempty"`
	Model    string `json:"model,omitempty"`
	Private  bool   `json:"private"`
}

type wsState struct {
	User            string `json:"user"`
	Workspace       bool   `json:"workspace"`
	Paused          bool   `json:"paused"`
	PausedAt        string `json:"pausedAt,omitempty"` // RFC 3339, while paused
	AdminChanges    bool   `json:"adminChanges"`
	CanAdminChanges bool   `json:"canAdminChanges"`
}

// wsMap is the compact map of what the person sees.
type wsMap struct {
	Tiles   []wsTile   `json:"tiles"`
	Screens []wsScreen `json:"screens,omitempty"`
	Orgs    []wsOrg    `json:"orgs,omitempty"`
}

type wsTile struct {
	Path    string   `json:"path"`
	Title   string   `json:"title,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Orgs    []string `json:"orgs,omitempty"`
	Status  string   `json:"status,omitempty"`  // running | idle | failed | building | off …
	Report  string   `json:"report,omitempty"`  // the tile's own status line, when it reports one
	Alerts  []string `json:"alerts,omitempty"`  // what xbind flags about it
	Agent   string   `json:"agent,omitempty"`   // what an agent may do there: off | read | full
	Running bool     `json:"running,omitempty"` // a tile agent runs there now
	Level   string   `json:"level,omitempty"`   // the person's level on it
	Rev     string   `json:"rev,omitempty"`     // its code's revision, when xbind says
}

// wsNoOrg is the org xbind names for the tiles in no org.
const wsNoOrg = "workspace"

// name is the tile as people call it.
func (t wsTile) name() string { return orStr(t.Title, t.Path) }

// org is the org a page about the tile is stamped with (its first).
func (t wsTile) org() string {
	if len(t.Orgs) == 0 {
		return ""
	}
	return t.Orgs[0]
}

type wsScreen struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Tiles []string `json:"tiles,omitempty"`
}

type wsOrg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type wsDocHit struct {
	Path    string `json:"path"`
	Section string `json:"section,omitempty"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

type wsDocPage struct {
	Path     string   `json:"path"`
	Title    string   `json:"title,omitempty"`
	Text     string   `json:"text"`
	Sections []string `json:"sections,omitempty"`
}

type wsCodeFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type wsCodeText struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// wsCall is one call as it is sent: Path is under /api/ with its query.
type wsCall struct {
	Method      string
	Path        string
	Body        []byte
	ContentType string
	Ticket      string // the approved request it runs under
	DryRun      bool
}

// wsAnswer is what a call answered. Dry is xbind's description of a dry run.
type wsAnswer struct {
	Status int
	Type   string
	Body   []byte
	Under  string // "approval:<id>" when a standing approval let it through
	Dry    *wsDry
}

// wsDry is xbind's description of a call it did not make.
type wsDry struct {
	Allowed       bool   `json:"allowed"`
	Class         string `json:"class"`
	SideEffect    bool   `json:"sideEffect"`
	Tile          string `json:"tile,omitempty"`
	Level         string `json:"level,omitempty"`
	Role          string `json:"role,omitempty"`
	AgentAccess   string `json:"agentAccess,omitempty"`
	NeedsApproval bool   `json:"needsApproval,omitempty"`
	Under         string `json:"under,omitempty"`
	Refusal       string `json:"refusal,omitempty"`
	Error         string `json:"error,omitempty"`
}

// wsRequest is a request for the person's approval, as xbind keeps it.
type wsRequest struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`  // call | session | template
	State   string         `json:"state"` // pending | approved | denied | used | expired | withdrawn
	Grant   string         `json:"grant,omitempty"`
	Tile    string         `json:"tile,omitempty"`
	Preview map[string]any `json:"preview,omitempty"`
	Expires int64          `json:"expires,omitempty"` // unix seconds
	Decided int64          `json:"decided,omitempty"` // unix seconds
	Via     string         `json:"via,omitempty"`     // what decided it: session | app | device
}

func (r wsRequest) decided() bool { return r.State != "pending" && r.State != "approved" }

// wsStanding is a standing approval the person gave: of one call (Scope
// "call": Method on Path of Tile) or of every write to Tile ("tile"),
// until Until (0: until they take it back). Request is the request it was
// given on.
type wsStanding struct {
	ID      string `json:"id"`
	Tile    string `json:"tile"`
	Scope   string `json:"scope"`
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
	Until   int64  `json:"until,omitempty"` // unix seconds
	Request string `json:"request,omitempty"`
}

// wsAgent is a tile agent session.
type wsAgent struct {
	ID        string `json:"id"`
	Tile      string `json:"tile"`
	Provider  string `json:"provider,omitempty"`
	Name      string `json:"name,omitempty"` // the harness as people call it, by xbind's start-defaults for the tile ("": xbind doesn't list it there)
	Mode      string `json:"mode,omitempty"`
	Asks      bool   `json:"asks"`      // its mode asks before acting, by the same answer (false: not known to)
	Status    string `json:"status"`    // starting | running | idle | ended | failed
	Pending   int    `json:"pending"`   // permission requests waiting for the person
	Questions int    `json:"questions"` // questions waiting for the person
	Waiting   string `json:"waiting,omitempty"`
	Origin    string `json:"origin,omitempty"` // "agent": this person's agent started it
	Mine      bool   `json:"mine,omitempty"`   // this handle started it: it may be driven
	History   string `json:"history,omitempty"`
	Last      string `json:"last,omitempty"` // the last line it wrote since its last prompt (xbind's compact read)
	Turn      int    `json:"turn,omitempty"` // the prompts it took so far (the same read)
	// EndedBy is "restart" for a session an xbind restart ended (it did not
	// finish: its history can be resumed); "" otherwise.
	EndedBy string `json:"endedBy,omitempty"`
}

// waits: the session waits for its person (a permission, a question).
func (a wsAgent) waits() bool { return a.Pending > 0 || a.Questions > 0 }

// live: it can still take a message.
func (a wsAgent) live() bool { return a.Status != "ended" && a.Status != "failed" && a.Status != "" }

// wsAgentStart is a tile agent's start: where, and what it is told first
// — or the past session to continue. Prompt is the brief: it rides the
// start, so the ticket is of those words, xbind shows them on the
// approval, and xbind sends them. Harness is set only when the person
// picked one of the harnesses the tile allows; left empty xbind resolves
// it, as it always resolves the mode and the network, from the person's
// preferences and the tile's org's rules. Nothing here is the model's.
type wsAgentStart struct {
	Tile    string `json:"tile"`
	Harness string `json:"harness,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Name    string `json:"name,omitempty"`
	Resume  string `json:"resume,omitempty"`
}

// wsHarness is a coding agent a tile's rules allow, with what a session
// on it would open with: its mode (Asks: the mode asks before acting), its
// network, and whether xbind asks the person for each start.
type wsHarness struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mode        string `json:"mode,omitempty"`
	Asks        bool   `json:"asks"`
	Net         string `json:"net,omitempty"`
	NeedsTicket bool   `json:"needsTicket"`
	NoSignIn    bool   `json:"noSignIn,omitempty"` // it runs on the workspace's LLM connector: no sign-in needed (xbind's word)
}

// wsStartDefaults is what a start in a tile resolves to, for the brief's
// card: Harness is the default one, Harnesses every one the person may
// pick; Why says why none may run there. Mode, Asks, Net and Ticket are
// the default harness's, for a card that shows one line.
type wsStartDefaults struct {
	Tile      string      `json:"tile"`
	Harness   string      `json:"harness"`
	Harnesses []wsHarness `json:"harnesses"`
	Why       string      `json:"why,omitempty"`
	Mode      string      `json:"mode,omitempty"`
	Asks      bool        `json:"asks"`
	Net       string      `json:"net,omitempty"`
	Ticket    bool        `json:"ticket"`
}

// of is the harness id among the allowed ones (ok false: it isn't one).
func (d wsStartDefaults) of(id string) (wsHarness, bool) {
	for _, h := range d.Harnesses {
		if h.ID == id {
			return h, true
		}
	}
	return wsHarness{ID: id, Name: id, Asks: true, NeedsTicket: true}, false
}

// name is the harness id as people call it, by the defaults' list.
func (d wsStartDefaults) name(id string) string {
	h, _ := d.of(id)
	return orStr(h.Name, id)
}

// wsTileCreate is a tile made from a template.
type wsTileCreate struct {
	Template string `json:"template"`
	Path     string `json:"path"`
}

// wsAgentReport is what a session's last turn left: its final answer, the
// files it changed as a stat, and the commands it ran to check — never code.
type wsAgentReport struct {
	Status   string       `json:"status"`
	Answer   string       `json:"answer,omitempty"`
	Files    []wsFileStat `json:"files,omitempty"`
	Verified []string     `json:"verified,omitempty"` // commands it ran and how they ended
	Turn     int          `json:"turn,omitempty"`
}

type wsFileStat struct {
	Path string `json:"path"`
	Add  int    `json:"add"`
	Del  int    `json:"del"`
}

type wsTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// wsChangeSet is an admin proposal and its fate (xbin.ChangeSet, with what
// the conversation needs to say about it).
type wsChangeSet struct {
	ID    string               `json:"id"`
	Title string               `json:"title"`
	State string               `json:"state"`
	Items []xbin.ChangeSetItem `json:"items"`
}

func (c wsChangeSet) decided() bool { return c.State != "proposed" && c.State != "applying" }

// wsPreviewAsk is one tile to look at: At its fragment (#tab), Phone a
// phone's viewport instead of a desktop's.
type wsPreviewAsk struct {
	Tile  string
	At    string
	Phone bool
}

// wsPreview is what a preview shows a model: the page as text and what
// went wrong on it, with xbind's fixed notice that all of it is data.
// Picture says a picture was taken too (the person's page can show it).
type wsPreview struct {
	Notice   string
	Title    string
	Snapshot string
	Picture  bool
	Clipped  bool
	Note     string
	Errors   []string
	Failed   []string
}

// wsTaintedBriefWords is xbind's taint refusal of what the conversation
// would tell a tile agent (a start's brief, a message), with what to do
// instead: the taint is the conversation's, so no rewording helps.
func wsTaintedBriefWords(why string) string {
	return "xbin refused it — " + strings.TrimSuffix(orStr(why, "this conversation holds data of an org whose rules that tile agent's model doesn't meet"), ".") +
		". What this conversation tells a tile agent goes to that session's model, and the conversation holds data of an org whose model rule that model doesn't meet: telling it less, rewording it or asking again changes nothing. " +
		"The ways forward are your person's: the tile agent started on the workspace's LLM connector, on a model those orgs allow (their agent preferences, or an admin's agents policy), or this done from a clean conversation — a new session that hasn't read those orgs' data. Stop here and tell your person"
}

// wsHarnessCommandWords: what the model is told when a brief or a message
// began with "/".
const wsHarnessCommandWords = "xbin refused it: what you send a tile agent can't begin with \"/\" — the coding agent reads that as one of its own commands, and those are your person's to give. Never begin a brief or a message with \"/\": say what you mean in words"

// errWsUnavailable: this xbind doesn't serve the route yet.
var errWsUnavailable = errors.New("not available on this xbin yet")

// --- refusals, in words a model and a person can act on --------------------------

// wsRefusal is err as a tool answers it: xbind's own words where it gave
// them, and what to do about each kind of refusal.
func wsRefusal(err error) string {
	var ww *wsWords
	if errors.As(err, &ww) {
		return ww.msg
	}
	var he *wsHeldError
	if errors.As(err, &he) {
		return he.Error()
	}
	var te *wsTaintError
	if errors.As(err, &te) {
		return te.Error()
	}
	msg := err.Error()
	var de *xbin.DelegateError
	if errors.As(err, &de) && de.Message != "" {
		msg = de.Message
	}
	switch {
	case errors.Is(err, errWsUnavailable):
		return "this isn't available on this xbin yet: say so, and offer the nearest thing you can do"
	case errors.Is(err, xbin.ErrTaintedBrief):
		return wsTaintedBriefWords(msg)
	case errors.Is(err, xbin.ErrSessionMode):
		return "xbin refused it — " + strings.TrimSuffix(msg, ".") + ". Your person switched that tile agent's session to a mode that doesn't ask before it acts, so it takes no more messages from you: only they can drive it now. Don't send it again or look for another way in; tell your person — they can switch it back to a mode that asks"
	case errors.Is(err, xbin.ErrHarnessCommand):
		return wsHarnessCommandWords
	case errors.Is(err, xbin.ErrAgentOff) && de != nil && de.Refusal == "no-agent":
		return "your person hasn't been given a workspace agent — an admin chooses who has one: nothing can be read or changed. Stop here and tell them"
	case errors.Is(err, xbin.ErrAgentOff) && de != nil && de.Refusal == "held":
		return "you are held on this xbin (" + msg + "): nothing can be read or changed until an admin clears it. Stop here and tell your person"
	case errors.Is(err, xbin.ErrAgentPaused):
		return "your person paused you: nothing can be read or changed until they resume. Stop here and tell them"
	case errors.Is(err, xbin.ErrAgentOff):
		return "workspace agents are turned off for this workspace: stop here and tell your person (" + msg + ")"
	case errors.Is(err, xbin.ErrHiddenByScope):
		return "this tile is outside what your person let this session see (hidden by them, in the session's scope): say so — they can widen the scope, or use another session"
	case errors.Is(err, xbin.ErrNoSession):
		return "xbin no longer knows this conversation's session; try once more, and tell your person if it keeps failing"
	case errors.Is(err, xbin.ErrPolicyRefused):
		return "refused for this tile — " + msg + ". This is a rule (the tile's, its org's, or your person can't see it), not something to work around: tell your person"
	case errors.Is(err, xbin.ErrNotDelegated):
		return "an agent can't do this, whatever is approved — " + msg + ". Tell your person it is theirs to do, and offer the nearest thing you can do"
	case errors.Is(err, xbin.ErrAdminRead):
		return "admin state isn't open to you: your person must be an admin and turn on \"Let my agent propose admin changes\" in Preferences"
	case errors.Is(err, xbin.ErrRateLimited):
		return "too many actions in a minute: wait a little before the next one"
	case errors.Is(err, xbin.ErrBadTicket):
		return "the approval no longer covers this call (it expired, was used, or the call changed): ask again"
	case errors.Is(err, xbin.ErrNeedsApproval):
		return "this needs your person's approval first"
	}
	return msg
}
