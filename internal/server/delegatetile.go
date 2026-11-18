package server

// delegatetile.go — GET /delegate/tile?tile=: where one tile stands, for a
// person's agent, in one read (D246; plans/wsagent/access-and-status.md §2;
// docs/protocol/delegation.md §Agent sessions).
//
// Nothing here is new reach: each part is a read the agent — or, where
// noted, its person — makes today, under that read's own gate, put in one
// answer so a conversation stops spending 3–10 calls a tile and guessing:
//
//   - the gate (delegate.go, byTileQuery) holds the tile as for any read
//     that names one: the person can read it, it is in the conversation's
//     scope, its orgs' rules let the agent in; the conversation is then
//     tainted with its orgs;
//   - what it is: the map's row (GET /delegate/map) and the component's
//     runtime (GET /components/<path>); the person's level;
//   - its backend: GET /tile-status's row of the primary, with a crash loop
//     and a start's retries said in words;
//   - its owner, wiring, jobs and log: the broker's TileStanding, each
//     under the gate of the read it replaces — GET /components' owner (a
//     name only where the account lookup would give it), this agent's own
//     GET /bindings and GET /grants (the admin-read class: an admin who
//     opted in; without them one bit, "unmet needs"), with each bound
//     provider the conversation can't see said as "another tile" and each
//     one it can tainting it (standingRefs), GET /logs at terminal level.
//     The scheduled jobs are the one part no delegate read gives (GET
//     /cron/jobs is never delegated): they are said where the person's
//     own terminal on the tile lists them — terminal level, the gate of
//     GET /logs, whose lines tell of the same runs;
//   - its coding-agent sessions: the person's GET /term/sessions?compact=1.
//
// TestTileStandingIsUnionOfReads holds it to that, for every agent level.

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/util"
)

// TileBackendRow is the runner's row of a tile's primary backend, as
// GET /tile-status gives it.
type TileBackendRow struct {
	State      string          `json:"state"`
	Error      string          `json:"error,omitempty"`
	Restarts   int             `json:"restarts,omitempty"`
	Retry      *TileStartRetry `json:"retry,omitempty"`
	UptimeSec  int64           `json:"uptimeSec,omitempty"`
	LastReqSec int64           `json:"lastReqSec,omitempty"`
	Flag       string          `json:"flag,omitempty"`  // "crash-loop" | "retrying" | "failed" | ""
	Words      string          `json:"words,omitempty"` // xbind's sentence for it
}

// TileStartRetry is a failed start's pending retry (the runner's StartRetry).
type TileStartRetry struct {
	Attempt int    `json:"attempt"`
	Of      int    `json:"of"`
	At      string `json:"at"`
}

// TileStandingSource is the broker's part of the read (the file comment).
type TileStandingSource interface {
	// adminRead: this agent's GET /bindings and GET /grants would answer
	// (the admin-read class: optedIn).
	TileStanding(person auth.Principal, tile string, adminRead bool) TileStanding
}

// TileStanding is what the broker answers of a tile for its person.
type TileStanding struct {
	Owner *StandingOwner `json:"owner,omitempty"`
	// Wiring: this agent's admin reads (GET /bindings, GET /grants) would
	// answer and take this tile in (a tile the person writes, owns, or
	// whose org they admin; an admin's every tile) — Slots' Bound and Uses'
	// Granted/Pending are said only then. Unmet, only without them: a
	// required slot unbound or a use not granted.
	Wiring   bool              `json:"wiring"`
	Unmet    bool              `json:"unmet,omitempty"`
	Slots    []StandingSlot    `json:"slots"`
	Uses     []StandingUse     `json:"uses"`
	Provides []StandingProvide `json:"provides"`
	Exposes  []StandingExpose  `json:"exposes"`
	// Cron: the tile's scheduled jobs, where a terminal on the tile would
	// read them (an admin, or terminal level); nil otherwise.
	Cron []StandingJob `json:"cron,omitempty"`
	Log  StandingLog   `json:"log"`
	// Report is what the tile says of itself (GET /tile-report); Alerts
	// what xbind flags about it (GET /tile-status' tile alerts).
	Report *StandingReport `json:"report,omitempty"`
	Alerts []string        `json:"alerts,omitempty"`
}

// StandingReport is a tile's own status line.
type StandingReport struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// StandingOwner is a tile's owner as its person may see it: the id, and a
// name only where they see accounts (or it is themselves, or an org they
// can see).
type StandingOwner struct {
	Kind string `json:"kind"` // user | org | workspace
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// StandingSlot is one slot the tile asks for.
type StandingSlot struct {
	Slot     string   `json:"slot"`
	Kind     string   `json:"kind"`
	Service  string   `json:"service,omitempty"`
	Required bool     `json:"required"`
	Multi    bool     `json:"multi,omitempty"`
	Bound    []string `json:"bound,omitempty"` // with Wiring: what it is bound to (empty: not bound)
}

// StandingUse is one call grant the tile's manifest asks for.
type StandingUse struct {
	Target  string `json:"target"`
	Role    string `json:"role"`
	Granted bool   `json:"granted,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}

// StandingProvide is one slot the tile offers.
type StandingProvide struct {
	Slot    string `json:"slot"`
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Role    string `json:"role,omitempty"`
}

// StandingExpose is one endpoint the tile offers the outside.
type StandingExpose struct {
	Slot  string   `json:"slot"`
	Kind  string   `json:"kind"`
	Paths []string `json:"paths,omitempty"`
}

// StandingJob is one scheduled job of the tile (GET /cron/jobs' row).
type StandingJob struct {
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	Path       string `json:"path"`
	Next       string `json:"next,omitempty"`
	LastRun    string `json:"lastRun,omitempty"`
	LastStatus int    `json:"lastStatus,omitempty"`
	Dormant    bool   `json:"dormant,omitempty"`
}

// StandingLog is the end of the tile's backend log, where the person may
// read it (terminal level).
type StandingLog struct {
	Shown bool     `json:"shown"`
	Lines []string `json:"lines,omitempty"`
	Why   string   `json:"why,omitempty"`
}

// standingSession is one of the person's coding-agent sessions on the tile.
type standingSession struct {
	ID       string `json:"id"`
	By       string `json:"by"` // "person" | "this agent" | "another agent"
	Status   string `json:"status"`
	Mode     string `json:"mode,omitempty"`
	Provider string `json:"provider,omitempty"`
	Waits    int    `json:"waits,omitempty"` // permission requests and questions waiting for the person
	Turn     int    `json:"turn,omitempty"`
	Last     string `json:"last,omitempty"`
}

func (s *Server) registerTileStandingAPI() {
	s.RegisterAPI("GET /delegate/tile", s.apiDelegateTile)
}

// apiDelegateTile — GET /delegate/tile?tile=: the person's agent (the file
// comment). The gate judged the tile; it is held again here as a listing
// holds its rows.
func (s *Server) apiDelegateTile(w http.ResponseWriter, r *http.Request) {
	a, ok := s.delegateCaller(w, r, false, false, true)
	if !ok {
		return
	}
	p := a.p
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	if tile == "" {
		WriteError(w, http.StatusBadRequest, "need ?tile=<path>", delegationDocs)
		return
	}
	c, found := s.Reg.Component(tile)
	if !found || util.SealedPath(tile) || c.IsTemplate() || isChrome(tile) || s.tileHidden(p, tile) || s.delegateHides(p, tile) {
		delegateRefuse(w, http.StatusForbidden, "tile", DelegateRead.String(), noTileFor(p.UserID))
		return
	}
	person := personOf(p)
	level, requested := s.agentLevelOf(c)
	kind := "page"
	if c.HasBackend() {
		kind = "app"
	}
	out := map[string]any{"tile": tile, "title": c.Manifest.DisplayTitle(), "kind": kind, "runtime": c.Manifest.Runtime,
		"orgs": s.orgsOf(tile), "level": levelOf(person, tile), "agent": level}
	if requested != "" {
		out["agentRequested"] = requested
	}
	if isMCPTile(c) {
		out["mcp"] = true
	}
	switch {
	case !c.HasBackend():
		out["backend"] = TileBackendRow{State: "static", Words: "a page with no backend: nothing runs"}
	case s.TileBackend != nil:
		row, running := s.TileBackend(tile)
		if !running {
			row = TileBackendRow{State: "idle"}
		}
		backendWords(&row, string(s.Reg.LifecycleState(tile)))
		out["backend"] = row
	}
	if src, ok := s.ChangeSets.(TileStandingSource); ok {
		st := src.TileStanding(person, tile, s.optedIn(p.UserID))
		if st.Owner != nil {
			out["owner"] = st.Owner
		}
		s.standingRefs(p, st.Slots)
		out["wiring"], out["slots"], out["uses"], out["provides"], out["exposes"], out["log"] = st.Wiring, st.Slots, st.Uses, st.Provides, st.Exposes, st.Log
		if st.Unmet {
			out["unmet"] = true
		}
		if st.Cron != nil {
			out["cron"] = st.Cron
		}
		if st.Report != nil {
			out["report"] = st.Report
		}
		if len(st.Alerts) > 0 {
			out["alerts"] = st.Alerts
		}
	}
	out["sessions"] = s.standingSessions(p, tile)
	WriteJSON(w, http.StatusOK, out)
}

// anotherTile stands for a bound provider the conversation can't see.
const anotherTile = "another tile"

// standingRefs holds the providers the slots are bound to as a listing
// holds its rows: a tile the conversation may see is named, and its orgs
// taint the conversation as reading it would; any other is "another tile".
// A builtin (internet, host, runtime, a network set, xbin:llm…) is no tile.
func (s *Server) standingRefs(p auth.Principal, slots []StandingSlot) {
	for i := range slots {
		for k, ref := range slots[i].Bound {
			prov, _, _ := strings.Cut(ref, "#")
			if _, isTile := s.Reg.Component(prov); !isTile {
				continue
			}
			// delegateHides: outside the conversation's scope, or its orgs'
			// rules keep the agent out — else shown, and tainting
			if s.tileHidden(p, prov) || s.delegateHides(p, prov) {
				slots[i].Bound[k] = anotherTile
			}
		}
	}
}

// levelOf is person's level on tile: read, write, terminal or "".
func levelOf(person auth.Principal, tile string) string {
	switch {
	case person.CanTerminalTile(tile):
		return "terminal"
	case person.CanWriteTile(tile):
		return "write"
	case person.CanReadTile(tile):
		return "read"
	}
	return ""
}

// backendWords says row in xbind's words: a crash loop and a start's
// retries first, plainly.
func backendWords(row *TileBackendRow, lifecycle string) {
	switch {
	case lifecycle != "" && lifecycle != string(registry.StateEnabled):
		row.Words = "the tile is " + lifecycle + ": its backend doesn't run until it is enabled again"
	case strings.Contains(row.Error, "crash-looping"):
		row.Flag, row.Words = "crash-loop", "keeps exiting: "+row.Error
	case row.Retry != nil:
		row.Flag, row.Words = "retrying", "crashing on start: "+row.Error+" — xbin starts it again by itself (attempt "+strconv.Itoa(row.Retry.Attempt)+" of "+strconv.Itoa(row.Retry.Of)+") at "+row.Retry.At
	case row.State == "failed":
		row.Flag, row.Words = "failed", "failed: "+row.Error+" — it stays failed until its code is saved, it is restarted or deployed"
	case row.State == "idle":
		row.Words = "idle: not running now — it starts on its first call, so this says nothing either way of whether it works"
		if row.Restarts > 0 {
			row.Words += "; it exited " + strconv.Itoa(row.Restarts) + " time(s) since it last started cleanly"
		}
	case row.Restarts > 0:
		row.Words = row.State + "; it exited " + strconv.Itoa(row.Restarts) + " time(s) since it last started cleanly"
	default:
		row.Words = row.State
	}
}

// standingSessions are the person's coding-agent sessions on tile, as
// their compact session listing gives them to this agent
// (apiTermSessions): their own sessions, at the levels they may open
// terminals with; who started each.
func (s *Server) standingSessions(p auth.Principal, tile string) []standingSession {
	out := []standingSession{}
	if s.Term == nil || !(p.CanTerminal() || p.Via == "terminal") {
		return out
	}
	rows := s.Term.ListFor(term.HomeKey(p), "", p.CanTerminalTileVia)
	rows = slices.DeleteFunc(rows, func(si term.SessionInfo) bool { return si.Kind != term.KindAgent || s.sessionTile(si.Cwd) != tile })
	for _, si := range rows {
		by := "person"
		if si.Origin == term.OriginAgent {
			by = "another agent"
			if s.Term.AgentOrigin(si.ID) == p.Agent {
				by = "this agent"
			}
		}
		row := standingSession{ID: si.ID, By: by, Status: si.Status, Mode: si.Mode, Provider: si.Provider, Waits: si.Pending + si.Questions}
		if turn, last, ok := s.Term.AgentGlance(si.ID); ok {
			row.Turn, row.Last = int(turn), last
		}
		out = append(out, row)
	}
	return out
}
