package server

// signinsroot.go — THE ROOT TOKEN'S SEAM, the server's share
// (internal/signins/roottoken.go says why, and what else to delete with
// it). The root token is no person: every /agent/signins route that keeps
// or uses a saved SIGN-IN refuses it (signinsPerson). But its sessions
// follow the home rule like anyone's — on a tile that isn't the
// workspace's they mount the tile's copy, confined — and someone who lives
// on the token must be able to answer "I trust this tile's source", see
// what they trusted, take it back, and forget a copy. So the token may
// call exactly these, as itself, under the key of its home:
//
//	GET    /agent/signins                 its trusted tiles, its home on ?tile=, its copies — no sign-ins
//	POST   /agent/signins/trust           the dialog's armed button, basis-bound, as a person's
//	DELETE /agent/signins/trust/{id}      take it back
//	DELETE /agent/signins/homes/{id}      forget a copy

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/term"
)

// rootTokenRoutes are the person-only routes the root token itself may
// call (the partition gate lets it through to signinsTruster).
var rootTokenRoutes = map[string]bool{
	"GET /agent/signins":               true,
	"POST /agent/signins/trust":        true,
	"DELETE /agent/signins/trust/{id}": true,
	"DELETE /agent/signins/homes/{id}": true,
}

// signinsTruster is who answers for a trust or home route: a person
// (signinsPerson's rule and refusal), or the root token itself — never a
// tile's credentials, a session's token, view-as or an agent. writable:
// the route records something (refused where no sign-ins are kept, as for
// a person).
func (s *Server) signinsTruster(w http.ResponseWriter, r *http.Request, writable bool) string {
	if p := auth.PrincipalOf(r); s.Signins != nil && term.RootTokenAsks(p) {
		if av := s.signinsAvail(); writable && !av.OK {
			WriteError(w, http.StatusConflict, av.Why, agentSigninsDocs)
			return ""
		}
		return signins.RootTokenUser
	}
	if writable {
		return s.signinsWritable(w, r)
	}
	return s.signinsPerson(w, r)
}
