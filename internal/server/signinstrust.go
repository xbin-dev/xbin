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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
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
	if s.Term != nil && s.Term.SigninSource != nil {
		s.Signins.Recheck(user, s.Term.SigninSource) // an answer whose tile is no longer what it was given for is void from here on
	}
	allowed := s.Signins.Policy().Trust.Allowed
	for _, t := range s.Signins.Trusted(user) {
		row := trustRow{ID: t.ID, Tile: t.Tile, Name: t.Name, At: t.At, Via: t.Via}
		switch {
		case t.Void():
			row.Why = t.VoidWhy
		case !allowed:
			row.Why = "trusting a tile's source is switched off for this workspace"
		default:
			row.Holds = true
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

// approveTicks is signinTicks for a request's approval: it answers the
// refusal itself (ok false).
func (s *Server) approveTicks(w http.ResponseWriter, user, id string, use, trust bool, shown string) (basis string, ok bool) {
	if !use && !trust {
		return "", true
	}
	if trust && !s.trustShown(w, shown) { // the sheet said what trusting means, or it is an old page's
		return "", false
	}
	cur, _ := s.Delegation.Request(user, id)
	basis, status, why := s.signinTicks(user, cur.Preview, use, trust)
	if why != "" {
		WriteJSON(w, status, map[string]any{"error": why, "changed": status == http.StatusConflict, "docs": agentSigninsDocs})
		return "", false
	}
	return basis, true
}

// planTicks is signinTicks for a plan's apply: signin and trust name the
// agent.start lines whose "use my sign-in" and trust lines the person
// ticked — only the person's own plan, only lines they chose to apply. →
// per ticked line, whether its trust line is ticked too.
func (s *Server) planTicks(user string, cs delegation.ChangeSet, chosen, signin, trust []int) (ticks map[int]bool, status int, why string) {
	ticks = map[int]bool{}
	for _, n := range slices.Concat(signin, trust) {
		if n < 1 || n > len(cs.Items) || !slices.Contains(chosen, n) || cs.From != "" || cs.Items[n-1].Kind != delegation.KindAgentStart {
			return nil, http.StatusBadRequest, fmt.Sprintf("line %d is not a tile agent's start you chose to apply: nothing to tick there", n)
		}
		var start map[string]any
		_ = json.Unmarshal(cs.Items[n-1].Start, &start)
		if _, status, why := s.signinTicks(user, map[string]any{"session": start}, slices.Contains(signin, n), slices.Contains(trust, n)); why != "" {
			return nil, status, fmt.Sprintf("line %d: %s", n, why)
		}
		ticks[n] = slices.Contains(trust, n)
	}
	return ticks, 0, ""
}

// tileSource is where the code of the tile path lies in comes from, for
// user, as it stands now.
func (s *Server) tileSource(user, path string) (signins.Source, error) {
	if s.Term == nil || s.Term.SigninSource == nil || s.Signins == nil {
		return signins.Source{}, &signins.Error{Status: http.StatusNotImplemented, Msg: "this xbind keeps no coding-agent sign-ins"}
	}
	tile, ok := s.Term.TileOf(strings.Trim(path, "/"))
	var src signins.Source
	if ok {
		src, ok = s.Term.SigninSource(user, tile)
	}
	if !ok {
		return src, &signins.Error{Status: http.StatusNotFound, Msg: "no such tile"}
	}
	return src, nil
}

// trustTile records that user trusts the source of the tile path lies in —
// as they were SHOWN it: basis is the token their question carried (a
// card's here.trust.basis, a start's signInTrust.basis), and where the
// tile is no longer that — another owner, someone else able to change it,
// moved, made again — nothing is recorded (409: look again). via: "card" |
// "approval" | "plan".
func (s *Server) trustTile(user, path, via, basis string) (signins.Trust, signins.Source, error) {
	src, err := s.tileSource(user, path)
	if err != nil {
		return signins.Trust{}, src, err
	}
	if !src.NeedsTrust() {
		return signins.Trust{}, src, &signins.Error{Status: http.StatusConflict, Msg: src.Tile + " is yours alone: your saved sign-in is used there without asking"}
	}
	t, err := s.Signins.TrustTile(user, src, via, basis)
	if err != nil {
		return signins.Trust{}, src, err
	}
	slog.Info("audit", "event", "signin.trust", "user", user, "tile", src.Tile, "owner", src.Owner, "editors", len(src.Editors), "via", via)
	return t, src, nil
}

// signinTicks holds the sign-in lines a person ticked on a start's
// approval to what its sheet showed (preview: a request's, or {session:
// a plan line's start}): the "use my sign-in" line only where the sheet
// offered one (signInId), the trust line only with it and only where the
// sheet carried one — and only while the tile is still as the line showed
// it (409 otherwise: nothing is decided). → the trust line's basis.
func (s *Server) signinTicks(user string, preview map[string]any, use, trust bool) (basis string, status int, why string) {
	sess, _ := preview["session"].(map[string]any)
	offered, _ := sess["signInId"].(string)
	switch {
	case (use || trust) && offered == "":
		return "", http.StatusBadRequest, "this start offered no saved sign-in to tick"
	case trust && !use:
		return "", http.StatusBadRequest, "the trust line goes with \"use my sign-in\": tick both, or neither"
	case !trust:
		return "", 0, ""
	}
	var line *struct { // (a stored preview holds the question as xbind made it, or as its JSON)
		Basis string `json:"basis"`
	}
	if raw, err := json.Marshal(sess["signInTrust"]); err != nil || json.Unmarshal(raw, &line) != nil || line == nil {
		return "", http.StatusBadRequest, "this start carried no trust line to tick"
	}
	basis = line.Basis
	tile, _ := sess["tile"].(string)
	src, err := s.tileSource(user, tile)
	if err != nil || src.Basis() != basis {
		return "", http.StatusConflict, signins.ErrTrustChanged.Msg + " (your agent can ask again: the new request shows the tile as it is)"
	}
	return basis, 0, ""
}

// apiSigninsTrust — POST /agent/signins/trust {tile}: "I trust this tile's
// source". The caller's own answer for a tile they may open a terminal on,
// kept for the tile as it stands (its identity, its owner, who can change
// it). → 201 {trust}. 409 when the tile is theirs alone (nothing to
// trust); 403 when the workspace doesn't let people trust.
func (s *Server) apiSigninsTrust(w http.ResponseWriter, r *http.Request) {
	user := s.signinsTruster(w, r, true)
	if user == "" {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct {
		Tile  string `json:"tile"`
		Basis string `json:"basis"` // the question's token (here.trust.basis): what the card showed
		Shown string `json:"shown"` // what the card said trusting means (signins.TrustShown)
	}
	if err := DecodeJSON(r, &body); err != nil || strings.Trim(body.Tile, "/") == "" || body.Basis == "" {
		WriteError(w, http.StatusBadRequest, "need {tile, basis, shown}: the basis of the question you were shown (GET /agent/signins?tile= → here.trust.basis), and what it said trusting means", agentSigninsDocs)
		return
	}
	if p := auth.PrincipalOf(r); !p.CanTerminalTile(s.sessionTile(body.Tile)) { // the level of the tile the path lies in
		WriteError(w, http.StatusNotFound, "no such tile", agentSigninsDocs)
		return
	}
	if body.Shown != signins.TrustShown {
		// a page from before trusting meant the home too (or a client that
		// doesn't say what it showed): refused like an answer for a tile
		// that changed, with the question as it stands — and told to reload
		src, err := s.tileSource(user, body.Tile)
		if err != nil {
			writeSigninsErr(w, err)
			return
		}
		WriteJSON(w, http.StatusConflict, map[string]any{"error": trustNotShown, "changed": true, "reload": true,
			"trust": s.Term.SigninQuestion(src, s.Signins.Policy().Trust), "docs": agentSigninsDocs})
		return
	}
	t, src, err := s.trustTile(user, body.Tile, "card", body.Basis)
	if errors.Is(err, signins.ErrTrustChanged) {
		// the tile is no longer what the card showed: the question as it
		// stands now, for the card to draw again (and arm again)
		WriteJSON(w, http.StatusConflict, map[string]any{"error": signins.ErrTrustChanged.Msg, "changed": true,
			"trust": s.Term.SigninQuestion(src, s.Signins.Policy().Trust), "docs": agentSigninsDocs})
		return
	}
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
	user := s.signinsTruster(w, r, false)
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
// just approved with it ticked ({signin: true, trust: true}; the sheet
// carried the line: preview.session.signInTrust, checked against the tile
// as it stands by signinTicks before the decision): their own answer for
// that tile, kept like one given on the tile's card, before the ticket is
// used. Where it can't be kept after all (out says why) the session has
// no sign-in.
func (s *Server) approvalTrust(out map[string]any, user string, req delegation.Request, basis string) {
	sess, _ := req.Preview["session"].(map[string]any)
	if sess["signInTrust"] == nil {
		return
	}
	tile, _ := sess["tile"].(string)
	if t, _, err := s.trustTile(user, tile, "approval", basis); err != nil {
		out["trustError"] = err.Error()
	} else {
		out["trust"] = t.ID
	}
}

// trustNotShown is the refusal of a trust answer that doesn't say what its
// page showed (signins.TrustShown): the page is older than what trusting
// means now.
const trustNotShown = "xbin was updated since this page was loaded: trusting a tile's source now means two things — your saved sign-in is used there, and that tile's code can change your home. Reload this page and answer again."

// signinTickBody is the sign-in part of a start's approval: its two lines,
// ticked, and — with the trust line — what the sheet said trusting means.
type signinTickBody struct {
	Signin, Trust bool
	Shown         string
}

// trustShown holds a ticked trust line (an approval's, a plan's) to what
// its page said trusting means; it answers the refusal itself.
func (s *Server) trustShown(w http.ResponseWriter, shown string) bool {
	if shown == signins.TrustShown {
		return true
	}
	WriteJSON(w, http.StatusConflict, map[string]any{"error": trustNotShown, "changed": true, "reload": true, "docs": agentSigninsDocs})
	return false
}
