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
	// Connection is what the handle is connected to — totals and each org's
	// reason, never a blocked tile's name; whatIf asks for another model.
	Connection(ctx context.Context, sid, whatIf string) (wsConnection, error)
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

	// Starting a tile agent and making a tile from a template are calls of
	// the same kind, built here so the call asked for and the call made are
	// the same bytes: AskX files it for approval; X makes it under a ticket
	// (or, with ticket "", where the person's preferences let it through),
	// and with dry describes it instead.
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

// wsConnection is the connection view the agent gets.
type wsConnection struct {
	Summary string `json:"summary,omitempty"` // xbind's sentence: "14 tiles · 3 orgs connected · 6 not connected"
	Model   string `json:"model,omitempty"`
	Private bool   `json:"private,omitempty"`
	Counts  struct {
		Connected int `json:"connected"`
		Hidden    int `json:"hidden"`
		Blocked   int `json:"blocked"`
		NoAccess  int `json:"noAccess"`
		Orgs      int `json:"orgs"`
	} `json:"counts"`
	Orgs []wsOrgConn `json:"orgs,omitempty"`
}

// wsOrgConn is one org's part of it. ID "" is the workspace's own tiles.
type wsOrgConn struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Connected int    `json:"connected"`
	Tiles     int    `json:"tiles"`
	State     string `json:"state,omitempty"`  // connected | hidden | blocked | mixed
	Reason    string `json:"reason,omitempty"` // xbind's words for a block
}

// connects: org is connected in c (the workspace's own tiles always count
// as the person's).
func (c wsConnection) connects(org string) bool {
	if org == "" {
		return true
	}
	for _, o := range c.Orgs {
		if o.ID == org {
			return o.Connected > 0 && o.State != "blocked"
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

type wsModel struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Where   string `json:"where,omitempty"` // "hosted", "runs here", …
	Private bool   `json:"private,omitempty"`
}

type wsState struct {
	User            string `json:"user"`
	Workspace       bool   `json:"workspace"`
	Paused          bool   `json:"paused"`
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
}

func (r wsRequest) decided() bool { return r.State != "pending" && r.State != "approved" }

// wsAgent is a tile agent session.
type wsAgent struct {
	ID        string `json:"id"`
	Tile      string `json:"tile"`
	Provider  string `json:"provider,omitempty"`
	Name      string `json:"name,omitempty"` // the harness as people call it
	Mode      string `json:"mode,omitempty"`
	Asks      bool   `json:"asks"`      // its mode asks before acting
	Status    string `json:"status"`    // starting | running | idle | ended | failed
	Pending   int    `json:"pending"`   // permission requests waiting for the person
	Questions int    `json:"questions"` // questions waiting for the person
	Waiting   string `json:"waiting,omitempty"`
	Origin    string `json:"origin,omitempty"` // "agent": this person's agent started it
	Mine      bool   `json:"mine,omitempty"`   // this handle started it: it may be driven
	History   string `json:"history,omitempty"`
	Last      string `json:"last,omitempty"` // its last line
	Turn      int    `json:"turn,omitempty"`
}

// waits: the session waits for its person (a permission, a question).
func (a wsAgent) waits() bool { return a.Pending > 0 || a.Questions > 0 }

// live: it can still take a message.
func (a wsAgent) live() bool { return a.Status != "ended" && a.Status != "failed" && a.Status != "" }

// wsAgentStart is a tile agent's start as the agent may shape it: where,
// what it is told, or the past session to continue. The harness, its mode,
// its network and what it may call are the person's preferences and the
// tile's org's rules — xbind fills them in; nothing here names them.
type wsAgentStart struct {
	Tile   string `json:"tile"`
	Prompt string `json:"prompt,omitempty"`
	Name   string `json:"name,omitempty"`
	Resume string `json:"resume,omitempty"`
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

type wsPreviewAsk struct {
	Tile     string `json:"tile,omitempty"`
	Screen   string `json:"screen,omitempty"`
	Viewport string `json:"viewport,omitempty"`
}

// wsPreview is what a preview shows without the picture: the accessibility
// snapshot and what went wrong on the page.
type wsPreview struct {
	Snapshot string   `json:"snapshot,omitempty"`
	Errors   []string `json:"errors,omitempty"`
	Failed   []string `json:"failed,omitempty"`
}

// errWsUnavailable: this xbind doesn't serve the route yet.
var errWsUnavailable = errors.New("not available on this xbin yet")

// --- refusals, in words a model and a person can act on --------------------------

// wsRefusal is err as a tool answers it: xbind's own words where it gave
// them, and what to do about each kind of refusal.
func wsRefusal(err error) string {
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
	case errors.Is(err, xbin.ErrAgentPaused):
		return "your person paused you: nothing can be read or changed until they resume. Stop here and tell them"
	case errors.Is(err, xbin.ErrAgentOff):
		return "workspace agents are turned off for this workspace: stop here and tell your person (" + msg + ")"
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
