package server

// signinshome.go — which home a person's sessions mount on a tile, and the
// copies kept of it (D245; internal/term/home.go, homecopy.go). On a tile
// whose source isn't only theirs and that they have not trusted, a
// person's sessions read their real home and write to a copy kept for that
// tile. The answer is the trust's own (signinstrust.go): GET /agent/signins
// says which home a session on ?tile= would get now, and lists the copies;
// DELETE /agent/signins/homes/{id} is "forget what this tile wrote to my
// home". A person's own, like every /agent/signins route.

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/term"
)

// homeCopyRow is one copy of the caller's home, as their list shows it.
type homeCopyRow struct {
	term.HomeCopyInfo
	// Unused: the caller trusts the tile's source now (or it became theirs
	// alone), so their new sessions there use their real home — the copy is
	// kept as it is until they forget it, and is used again if the trust
	// stops holding. Nothing of it is ever merged into the real home.
	Unused bool `json:"unused"`
}

// homeHere is which home a session of the caller's on a tile would mount
// now, and the copy kept for that tile when there is one.
type homeHere struct {
	*term.SessionHome
	Copy *homeCopyRow `json:"copy,omitempty"`
}

// homeCopyRows lists p's copies.
func (s *Server) homeCopyRows(p auth.Principal) []homeCopyRow {
	out := []homeCopyRow{}
	if s.Term == nil {
		return out
	}
	for _, hc := range s.Term.HomeCopies(term.HomeKey(p)) {
		row := homeCopyRow{HomeCopyInfo: hc}
		if h := s.Term.HomeRule(p, hc.Tile); !hc.Gone && h != nil && h.Mode == term.HomeOwn {
			row.Unused = true
		}
		out = append(out, row)
	}
	return out
}

// homeHereFor answers the home of a session p would open on tile (the
// registered tile; the caller checked p's terminal level on it).
func (s *Server) homeHereFor(p auth.Principal, tile string, copies []homeCopyRow) *homeHere {
	h := s.Term.HomeRule(p, tile)
	if h == nil {
		return nil
	}
	out := &homeHere{SessionHome: h}
	for i := range copies {
		if copies[i].Tile == tile && !copies[i].Gone {
			out.Copy = &copies[i]
			break
		}
	}
	return out
}

// apiSigninsHomeForget — DELETE /agent/signins/homes/{id}: "forget what
// this tile wrote to my home". The caller's sessions on that copy are
// ended, and the copy is removed → {ok, tile, ended}. Their real home is
// not touched. 404 not theirs.
func (s *Server) apiSigninsHomeForget(w http.ResponseWriter, r *http.Request) {
	user := s.signinsTruster(w, r, false) // a person, or the root token for its own (signinsroot.go)
	if user == "" {
		return
	}
	if s.Term == nil {
		WriteError(w, http.StatusNotFound, term.ErrNoHomeCopy.Error(), agentSigninsDocs)
		return
	}
	p := auth.PrincipalOf(r)
	key, why := term.HomeKey(p), "You had xbin forget what this tile wrote to your home, so this session — which was on that copy — was ended. Start it again: it gets a fresh copy."
	if of := r.URL.Query().Get("of"); of != "" {
		// an admin forgets a copy kept from an account that no longer
		// exists — never a living account's, never the root token's
		if !s.settingsAdmin(r) || p.ReadOnly() || p.Agent != "" || !s.keptHomeKey(of) {
			WriteError(w, http.StatusNotFound, term.ErrNoHomeCopy.Error(), agentSigninsDocs)
			return
		}
		key, why = of, "The copy this session was on was removed by a workspace admin."
		slog.Info("audit", "event", "home.copy.forget-kept", "of", of, "copy", r.PathValue("id"), "by", p.From())
	}
	tile, ended, err := s.Term.ForgetHomeCopy(key, r.PathValue("id"), why)
	switch {
	case errors.Is(err, term.ErrNoHomeCopy):
		WriteError(w, http.StatusNotFound, err.Error(), agentSigninsDocs)
	case err != nil:
		WriteError(w, http.StatusConflict, err.Error(), agentSigninsDocs)
	default:
		WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tile": tile, "ended": ended})
	}
}

// keptHomeRow is a copy of a home kept from an account that no longer
// exists, as the workspace's admins see it.
type keptHomeRow struct {
	User string `json:"user"` // the deleted account's id (its home's key)
	term.HomeCopyInfo
}

// keptHomeKey reports a home key that has copies and is no account's now
// (nor the root token's).
func (s *Server) keptHomeKey(key string) bool {
	if s.Term == nil || key == "" || key == signins.RootTokenUser || s.Auth == nil || s.Auth.Users == nil {
		return false
	}
	if _, ok := s.Auth.Users.Get(key); ok {
		return false
	}
	for _, k := range s.Term.HomeCopyKeys() {
		if k == key {
			return true
		}
	}
	return false
}

// keptHomeRows lists the copies kept from deleted accounts.
func (s *Server) keptHomeRows() []keptHomeRow {
	out := []keptHomeRow{}
	if s.Term == nil {
		return out
	}
	for _, key := range s.Term.HomeCopyKeys() {
		if !s.keptHomeKey(key) {
			continue
		}
		for _, hc := range s.Term.HomeCopies(key) {
			out = append(out, keptHomeRow{User: key, HomeCopyInfo: hc})
		}
	}
	return out
}
