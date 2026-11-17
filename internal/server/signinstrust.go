package server

// signinstrust.go — "I trust this tile's source" (D230, the owner's rule of
// 2026-11-17; internal/signins/trust.go, internal/term/signin.go). A
// person's saved sign-in waits, on a tile whose source isn't only their
// own, for their own answer — given here, in their own session, app or
// device: on the tile's sign-in card (POST /agent/signins/trust), or by
// ticking the trust line of a start they approve (a request's approval, a
// plan's start line). Nothing else is trust: these routes refuse a tile's
// credentials, a person's agent, view-as and the root token like every
// other /agent/signins route, and the agent's page can open the card, never
// answer it.

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/term"
)

// trustRow is one of the caller's trusted tiles, as their list shows it.
type trustRow struct {
	ID   string `json:"id"`
	Tile string `json:"tile"`
	Name string `json:"name,omitempty"`
	At   int64  `json:"at"`
	Via  string `json:"via,omitempty"`
	// Holds: the answer covers the tile as it stands now. false: xbin asks
	// again before it uses a sign-in there — Why says what changed.
	Holds bool   `json:"holds"`
	Why   string `json:"why,omitempty"`
}

func (s *Server) trustedRows(user string) []trustRow {
	out := []trustRow{}
	for _, t := range s.Signins.Trusted(user) {
		row := trustRow{ID: t.ID, Tile: t.Tile, Name: t.Name, At: t.At, Via: t.Via}
		src, ok := signins.Source{}, false
		if s.Term != nil && s.Term.SigninSource != nil {
			src, ok = s.Term.SigninSource(user, t.Tile)
		}
		switch {
		case !ok:
			row.Why = "the tile is gone"
		case src.ID != t.Ident:
			row.Why = "another tile was made at this path since"
		case s.Signins.Trusts(user, src):
			row.Holds = true
		case !s.Signins.Policy().Trust.Allowed:
			row.Why = "trusting a tile's source is switched off for this workspace"
		case src.Owner != t.Owner:
			row.Why = "the tile has another owner now"
		default:
			row.Why = "someone new can change the tile"
		}
		out = append(out, row)
	}
	return out
}

// withoutSignin is what a session of prov that p opens on tile runs on
// without a saved sign-in: the tile's own login where a session saw one,
// the workspace's model where the connector serves, else nothing.
func (s *Server) withoutSignin(p auth.Principal, tile string, prov agent.Provider) string {
	row := s.Term.AgentLLM(p.UserID, term.HomeKey(p), tile, []agent.Provider{prov})[prov.ID]
	signedIn, known := s.Term.HomeLogin(term.HomeKey(p), prov.ID)
	switch {
	case known && signedIn && (row == nil || row.Use != "connector"):
		return "own"
	case noSignIn(row):
		return "connector"
	case known && signedIn:
		return "own"
	}
	return "none"
}

// trustTile records that user trusts the source of the tile path lies in,
// as it stands now. via: "card" | "approval" | "plan".
func (s *Server) trustTile(user, path, via string) (signins.Trust, signins.Source, error) {
	if s.Term == nil || s.Term.SigninSource == nil || s.Signins == nil {
		return signins.Trust{}, signins.Source{}, &signins.Error{Status: http.StatusNotImplemented, Msg: "this xbind keeps no coding-agent sign-ins"}
	}
	tile, ok := s.Term.TileOf(strings.Trim(path, "/"))
	var src signins.Source
	if ok {
		src, ok = s.Term.SigninSource(user, tile)
	}
	if !ok {
		return signins.Trust{}, src, &signins.Error{Status: http.StatusNotFound, Msg: "no such tile"}
	}
	if !src.NeedsTrust() {
		return signins.Trust{}, src, &signins.Error{Status: http.StatusConflict, Msg: src.Tile + " is yours alone: your saved sign-in is used there without asking"}
	}
	t, err := s.Signins.TrustTile(user, src, via)
	if err != nil {
		return signins.Trust{}, src, err
	}
	slog.Info("audit", "event", "signin.trust", "user", user, "tile", src.Tile, "owner", src.Owner, "editors", len(src.Editors), "via", via)
	return t, src, nil
}

// apiSigninsTrust — POST /agent/signins/trust {tile}: "I trust this tile's
// source". The caller's own answer for a tile they may open a terminal on,
// kept for the tile as it stands (its identity, its owner, who can change
// it). → 201 {trust}. 409 when the tile is theirs alone (nothing to
// trust); 403 when the workspace doesn't let people trust.
func (s *Server) apiSigninsTrust(w http.ResponseWriter, r *http.Request) {
	user := s.signinsWritable(w, r)
	if user == "" {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct {
		Tile string `json:"tile"`
	}
	if err := DecodeJSON(r, &body); err != nil || strings.Trim(body.Tile, "/") == "" {
		WriteError(w, http.StatusBadRequest, "need {tile}", agentSigninsDocs)
		return
	}
	if p := auth.PrincipalOf(r); !p.CanTerminalTile(s.sessionTile(body.Tile)) { // the level of the tile the path lies in
		WriteError(w, http.StatusNotFound, "no such tile", agentSigninsDocs)
		return
	}
	t, _, err := s.trustTile(user, body.Tile, "card")
	if err != nil {
		writeSigninsErr(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"trust": trustRow{ID: t.ID, Tile: t.Tile, Name: t.Name, At: t.At, Via: t.Via, Holds: true}})
}

// apiSigninsUntrust — DELETE /agent/signins/trust/{id}: the caller no
// longer trusts that tile's source. Their sessions running there on a saved
// sign-in are ended → {ok, ended}.
func (s *Server) apiSigninsUntrust(w http.ResponseWriter, r *http.Request) {
	user := s.signinsPerson(w, r)
	if user == "" {
		return
	}
	t, err := s.Signins.Untrust(user, r.PathValue("id"))
	if err != nil && !errors.Is(err, signins.ErrNoTrust) {
		writeSigninsErr(w, err)
		return
	}
	if errors.Is(err, signins.ErrNoTrust) {
		writeSigninsErr(w, err)
		return
	}
	ended := 0
	if s.Term != nil {
		ended = s.Term.EndSigninSessionsOn(user, t.Tile, "You no longer trust the source of "+t.Tile+", so this session — which ran on your saved sign-in — was ended.")
	}
	slog.Info("audit", "event", "signin.untrust", "user", user, "tile", t.Tile, "ended", ended)
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "ended": ended})
}

// trustLine is need as a preview carries it: nothing — not a typed nil —
// where no trust is asked.
func trustLine(need *term.SigninTrust) any {
	if need == nil {
		return nil
	}
	return need
}

// approvalTrust keeps the trust line of a tile agent's start the person
// just approved with it ticked ({trust: true}; the sheet carried the line:
// preview.session.signInTrust): their own answer for that tile, kept like
// one given on the tile's card, before the ticket is used. Without the
// tick — or where it can't be kept (out says why) — the start they
// approved is one without the sign-in.
func (s *Server) approvalTrust(out map[string]any, user string, req delegation.Request) {
	sess, _ := req.Preview["session"].(map[string]any)
	if sess["signInTrust"] == nil {
		return
	}
	tile, _ := sess["tile"].(string)
	if t, _, err := s.trustTile(user, tile, "approval"); err != nil {
		out["trustError"] = err.Error()
	} else {
		out["trust"] = t.ID
	}
}
