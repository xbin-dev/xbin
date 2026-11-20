package server

// The terminal session directory's HTTP face (D73, docs/protocol.md
// §/api/xbin): a user lists their own live sessions per tile and names a
// tab; the change stream rides /ws/events as `term` events, delivered to
// the owner (and admins) — never to a tile (termEventFor). Attach/kill
// stay on /ws/term.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
)

func (s *Server) registerTermAPI() {
	s.RegisterAPI("GET /term/sessions", s.apiTermSessions)
	s.RegisterAPI("PATCH /term/sessions/{id}", s.apiTermRename)
	s.registerAgentAPI()        // agent sessions (agentapi.go, D74)
	s.registerAgentPrefsAPI()   // a person's agent preferences (agentprefsapi.go, D229)
	s.registerAgentSigninsAPI() // their saved coding-agent sign-ins (agentsignins.go, D230)
}

// termChange is a `term` event's data: which session changed, whose, and
// the session's target deployment when it states one and its purpose when
// it has one (term.Manager.DeploymentOf/PurposeOf: an "open" or "rename"; a
// closed session is gone).
type termChange struct {
	Op         string `json:"op"`
	ID         string `json:"id"`
	User       string `json:"user"`
	Deployment string `json:"deployment,omitempty"`
	Purpose    string `json:"purpose,omitempty"` // "signin": a guided sign-in's shell (term/purpose.go)
}

func (c termChange) Owner() string { return c.User }

// termStatus is a `term` event's data for op "status": an agent session's
// status and unanswered counts changed (term/agentstatus.go) — what an
// inbox reacts to without following every session's log.
type termStatus struct {
	Op string `json:"op"`
	term.StatusChange
}

func (c termStatus) Owner() string { return c.User }

// TermStatus is the Manager.OnStatus hook: a `term` event, op "status",
// filtered like the other `term` events to the owner and admins.
func (s *Server) TermStatus(cwd string, st term.StatusChange) {
	if s.Hub == nil {
		return
	}
	s.Hub.Publish(events.Event{Type: "term", Component: cwd, Data: termStatus{Op: "status", StatusChange: st}})
}

// sessionTile is the registered tile a session's directory (cwd), or a past
// session's, is or lies in (term.Manager.TileOf): what every decision about
// that session is made on — the directory may be inside its tile, and its
// own path matches only patterns. The directory itself when no tile holds it.
func (s *Server) sessionTile(cwd string) string {
	if s.Term == nil {
		return strings.Trim(cwd, "/")
	}
	tile, _ := s.Term.TileOf(cwd)
	return tile
}

// owned is what the per-user event kinds (`term`, `session`) carry: whose
// they are, for the filter.
type owned interface{ Owner() string }

// TermChanged is the Manager.OnChange hook: one `term` event per change,
// filtered per subscriber in handleEventsWS to the owner and admins.
func (s *Server) TermChanged(op, homeKey, id, cwd string) {
	if s.Hub == nil {
		return
	}
	c := termChange{Op: op, ID: id, User: homeKey}
	if s.Term != nil {
		c.Deployment, c.Purpose = s.Term.DeploymentOf(id), s.Term.PurposeOf(id)
	}
	s.Hub.Publish(events.Event{Type: "term", Component: cwd, Data: c})
}

// termEventFor reports whether a per-user event (`term`, `session`) is p's
// to see: a human's own (a browser session, the owner's cookie or token)
// and admins' — and a shell's terminal token's, for sessions on its own
// tile (bx agent inside a terminal; the same reach MayDrive gives it).
// Never an element's: a tile's frame token carries the driving user's id
// and a tile backend's instance token none (which would read as the
// owner's home), and neither may follow a user's agent sessions — the
// transcripts, tool output and patches of tiles it cannot read
// (plans/auth.md: default-deny for element principals).
func termEventFor(p auth.Principal, e events.Event) bool {
	switch {
	case p.Component == "": // a human principal
		// (an admin follows everyone's — but for a session that carries its
		// person's saved sign-in, which is theirs alone: D230, term/signin.go)
		if pr, ok := e.Data.(interface{ Private() bool }); p.IsAdmin() && !(ok && pr.Private()) {
			return true
		}
		if p.UserID == "" { // no one in particular (never the owner's home by default)
			return false
		}
	case p.Via == "terminal" && e.Component == p.Component: // a shell's token, on its own tile
	default:
		return false
	}
	o, ok := e.Data.(owned)
	return ok && o.Owner() == term.HomeKey(p)
}

// apiTermSessions lists the caller's live sessions (?cwd= narrows to one
// tile). A principal that may not open terminals has none. An admin may
// pass ?user= for another user's — the same visibility GET /status gives,
// which leaves out the names of a person's sessions on a partitioned tile
// (PD-09: an agent names its tab after the conversation); so does an
// admin's view as that person.
func (s *Server) apiTermSessions(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if u := r.URL.Query().Get("user"); u != "" && !p.IsAdmin() {
		apiErr(w, http.StatusForbidden, "admin only")
		return
	}
	if s.Term == nil || !(p.CanTerminal() || p.Via == "terminal") {
		WriteJSON(w, http.StatusOK, []term.SessionInfo{})
		return
	}
	homeKey, may := term.HomeKey(p), p.CanTerminalTileVia // a terminal's own tile counts (D74)
	if u := r.URL.Query().Get("user"); u != "" {
		homeKey, may = u, nil
	} else if p.IsAdmin() {
		may = nil
	}
	rows := s.Term.ListFor(homeKey, r.URL.Query().Get("cwd"), may)
	if p.Agent != "" { // a person's agent: not the tiles whose org keeps it out (delegate.go)
		rows = slices.DeleteFunc(rows, func(si term.SessionInfo) bool { return s.delegateHides(p, s.sessionTile(si.Cwd)) })
	}
	q := r.URL.Query()
	if q.Get("origin") == term.OriginAgent {
		// the sessions a workspace agent started for this person — for the
		// agent itself, the ones IT started (another holder's are not its)
		rows = slices.DeleteFunc(rows, func(si term.SessionInfo) bool {
			return si.Origin != term.OriginAgent || p.Agent != "" && s.Term.AgentOrigin(si.ID) != p.Agent
		})
	}
	if q.Get("compact") != "" { // what a status poll needs, and no more
		// an agent session's glance — the prompts it took, what waits for an
		// answer, the last line it wrote — is transcript: the caller's own
		// sessions only, never an admin's listing of someone else's
		own := homeKey == term.HomeKey(p) && !p.ViewAs()
		out := make([]map[string]any, 0, len(rows))
		for _, si := range rows {
			row := map[string]any{"id": si.ID, "cwd": si.Cwd, "kind": si.Kind, "provider": si.Provider, "mode": si.Mode, "status": si.Status, "origin": si.Origin}
			if si.Kind == term.KindAgent {
				row["pending"], row["questions"] = si.Pending, si.Questions
				if p.Agent != "" {
					// its own held messages the session didn't take (D250 re-review 4a): the agent is told once
					if rs := s.Term.AgentReturned(si.ID, p.Agent); len(rs) > 0 {
						row["returned"] = rs
					}
				}
				if turn, last, ok := s.Term.AgentGlance(si.ID); ok {
					row["turn"] = turn
					if own {
						row["last"] = last
					}
				}
			}
			out = append(out, row)
		}
		// ?ids=a,b: sessions the caller knew of that aren't live any more,
		// with how they ended — "exit" when xbind recorded the end (its
		// history entry), "restart" when it has no record of one: the
		// session was running when xbind stopped
		for _, id := range strings.Split(q.Get("ids"), ",") {
			if id = strings.TrimSpace(id); id == "" || len(id) > 64 || len(out) > 500 || slices.ContainsFunc(rows, func(si term.SessionInfo) bool { return si.ID == id }) {
				continue
			}
			row := map[string]any{"id": id, "status": "ended", "endedBy": "restart"}
			if at, recorded := s.Term.AgentEnded(personOf(p), id); recorded {
				row["endedBy"], row["ended"] = "exit", at
			}
			out = append(out, row)
		}
		WriteJSON(w, http.StatusOK, out)
		return
	}
	if homeKey != term.HomeKey(p) || p.ViewAs() {
		for i := range rows {
			if rows[i].Personal() {
				rows[i].Name = ""
			}
		}
	}
	WriteJSON(w, http.StatusOK, rows)
}

// apiTermRename names a tab: {name}. The session's creator or an admin —
// not an admin in another person's session on a partitioned tile (PD-09).
// Never the sign-in's reserved name (400), never a sign-in's session (409).
func (s *Server) apiTermRename(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	id := r.PathValue("id")
	if s.Term == nil || !s.Term.MayRename(id, p) {
		apiErr(w, http.StatusForbidden, "session belongs to another user")
		return
	}
	var body struct{ Name string }
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		apiErr(w, http.StatusBadRequest, "need {name}")
		return
	}
	if len(body.Name) > 64 {
		body.Name = body.Name[:64]
	}
	// the sign-in's reserved name, or a sign-in's session (term/purpose.go)
	if code, why := s.Term.RenameRefused(id, body.Name); code != 0 {
		apiErr(w, code, why)
		return
	}
	if !s.Term.Rename(id, body.Name) {
		apiErr(w, http.StatusNotFound, "no such session")
		return
	}
	WriteOK(w)
}
