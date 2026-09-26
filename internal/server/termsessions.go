package server

// The terminal session directory's HTTP face (D73, docs/protocol.md
// §/api/xbin): a user lists their own live sessions per tile and names a
// tab; the change stream rides /ws/events as `term` events, delivered to
// the owner (and admins) — never to a tile (termEventFor). Attach/kill
// stay on /ws/term.

import (
	"encoding/json"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
)

func (s *Server) registerTermAPI() {
	s.RegisterAPI("GET /term/sessions", s.apiTermSessions)
	s.RegisterAPI("PATCH /term/sessions/{id}", s.apiTermRename)
	s.registerAgentAPI() // agent sessions (agentapi.go, D74)
}

// termChange is a `term` event's data: which session changed, whose.
type termChange struct {
	Op   string `json:"op"`
	ID   string `json:"id"`
	User string `json:"user"`
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

// owned is what the per-user event kinds (`term`, `session`) carry: whose
// they are, for the filter.
type owned interface{ Owner() string }

// TermChanged is the Manager.OnChange hook: one `term` event per change,
// filtered per subscriber in handleEventsWS to the owner and admins.
func (s *Server) TermChanged(op, homeKey, id, cwd string) {
	if s.Hub == nil {
		return
	}
	s.Hub.Publish(events.Event{Type: "term", Component: cwd, Data: termChange{Op: op, ID: id, User: homeKey}})
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
		if p.IsAdmin() {
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
// pass ?user= for another user's — the same visibility GET /status gives.
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
	WriteJSON(w, http.StatusOK, s.Term.ListFor(homeKey, r.URL.Query().Get("cwd"), may))
}

// apiTermRename names a tab: {name}. The session's creator or an admin.
func (s *Server) apiTermRename(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	id := r.PathValue("id")
	if s.Term == nil || !s.Term.CanTouch(id, p) {
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
	if !s.Term.Rename(id, body.Name) {
		apiErr(w, http.StatusNotFound, "no such session")
		return
	}
	WriteOK(w)
}
