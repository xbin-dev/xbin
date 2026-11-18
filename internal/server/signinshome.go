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
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
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
	user := s.signinsPerson(w, r)
	if user == "" {
		return
	}
	if s.Term == nil {
		WriteError(w, http.StatusNotFound, term.ErrNoHomeCopy.Error(), agentSigninsDocs)
		return
	}
	p := auth.PrincipalOf(r)
	tile, ended, err := s.Term.ForgetHomeCopy(term.HomeKey(p), r.PathValue("id"),
		"You had xbin forget what this tile wrote to your home, so this session — which was on that copy — was ended. Start it again: it gets a fresh copy.")
	switch {
	case errors.Is(err, term.ErrNoHomeCopy):
		WriteError(w, http.StatusNotFound, err.Error(), agentSigninsDocs)
	case err != nil:
		WriteError(w, http.StatusConflict, err.Error(), agentSigninsDocs)
	default:
		WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tile": tile, "ended": ended})
	}
}
