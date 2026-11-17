package server

// planstart.go — a plan's agent.start line at Apply (plan.go describes it):
// the open itself, authorised as a ticket authorises a single start.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
)

// planStart applies an agent.start line: POST /term/sessions as the
// conversation that proposed it, authorised as a ticket authorises a
// single start — the harness and mode its person was shown, and no other.
// → the status, the refusal, and {session} for the line's Out.
//
// approved is the line's start as its person was shown it when it was
// proposed (ChangeItem.Start), and the saved sign-in is read from THAT
// (D230): the session gets the sign-in the approved line OFFERED only
// where the person ticked it when they applied (use: "Use my … sign-in
// for this session", unticked by default) — the open uses exactly that
// one or refuses — and none otherwise. Never the one a fresh preview
// resolves at Apply: the person's default may have moved since. A tile an
// earlier line creates for the person is offered their default where it
// will be theirs alone (plannedSignIn), and judged for real by the open,
// once it exists. trusted: they ticked the line's trust line too ("I
// trust this tile's source").
func (s *Server) planStart(ctx context.Context, a planAgent, pv delegation.ChangePreview, approved json.RawMessage, use, trusted bool) (int, string, json.RawMessage) {
	if a.gone != "" || a.p.Agent == "" {
		return http.StatusConflict, cmpOr(a.gone, "a tile agent is started for the conversation that asks for it"), nil
	}
	var shown struct{ Provider, Mode, Net string }
	if json.Unmarshal(pv.Start, &shown) != nil || shown.Provider == "" || shown.Mode == "" && !noDefaultMode(shown.Provider) {
		return http.StatusConflict, "the start names no coding agent and mode: ask your agent to propose it again", nil
	}
	var named struct {
		SignInID    string `json:"signInId"` // the saved sign-in the approved line offered
		SignInTrust *struct {
			Basis string `json:"basis"`
		} `json:"signInTrust"` // …and the trust line it carried
	}
	_ = json.Unmarshal(approved, &named)
	trustAsked := named.SignInTrust != nil
	if !use {
		named.SignInID = "" // not ticked: this session has no saved sign-in
	}
	if ok, wait := s.delegateRT.allowAct(a.p.UserID); !ok {
		return http.StatusTooManyRequests, fmt.Sprintf("your agent is acting too fast: try again in %d s", int(wait/time.Second)+1), nil
	}
	// registered as the gate registers a single start (serveDelegate, step
	// 7): a pause, the switch going off or the tile turning the agent away
	// cuts it, and it is judged once more after it is registered
	tile := shownTile(pv)
	still := func() string {
		if _, why := s.decideRefusal(a.p.UserID, a.p.Agent); why != "" {
			return why
		}
		return s.lineAgentRefusal(a, []string{tile})
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.delegateRT.track(a.p.UserID, &runningCall{cancel: cancel, still: still})()
	if why := still(); why != "" {
		return http.StatusForbidden, why, nil
	}
	if use && trustAsked && trusted { // the line's trust tick: the person's own answer, kept before the open — for the tile as the line showed it
		if _, _, err := s.trustTile(a.p.UserID, tile, "plan", named.SignInTrust.Basis); err != nil {
			slog.Info("a plan's trust line could not be kept: the start runs without the sign-in", "tile", tile, "err", err)
		}
	}
	ctx = context.WithValue(auth.WithPrincipal(ctx, a.p), delegateStartKey{}, &term.DelegateStart{Approved: true, Provider: shown.Provider, Mode: shown.Mode, Net: shown.Net,
		Signin: named.SignInID, TrustAsked: trustAsked})
	if s.PlanStarter != nil { // (tests: the open itself, authorised as above)
		id, status, refusal := s.PlanStarter(ctx, a.p, pv.Call.Body)
		if refusal != "" {
			return status, refusal, nil
		}
		s.taint(a.p, tile)
		out, _ := json.Marshal(map[string]string{"session": id})
		return http.StatusOK, "", out
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/xbin/term/sessions", bytes.NewReader(pv.Call.Body))
	if err != nil {
		return http.StatusBadRequest, err.Error(), nil
	}
	req.Header.Set("Content-Type", "application/json")
	rec := &memWriter{header: http.Header{}, status: http.StatusOK}
	s.handleAPI(rec, req)
	if rec.status >= 300 {
		return rec.status, errorOf(rec), nil
	}
	var info struct{ ID string }
	_ = json.Unmarshal(rec.body.Bytes(), &info)
	s.taint(a.p, tile) // the conversation now follows a session on that tile
	out, _ := json.Marshal(map[string]string{"session": info.ID})
	return rec.status, "", out
}

// noDefaultMode: the coding agent's catalog entry names no default mode
// (OpenCode: it opens as it opens itself), so a start of it that names no
// mode is shown, approved and opened with none. For every other one a
// start with no mode is a line that was never resolved.
func noDefaultMode(harness string) bool {
	prov, ok := agent.Lookup(harness)
	if !ok {
		return false
	}
	mode, err := prov.ResolveMode("")
	return err == nil && mode == ""
}

func shownTile(pv delegation.ChangePreview) string {
	var v struct{ Tile string }
	_ = json.Unmarshal(pv.Start, &v)
	return v.Tile
}

// A conversation's share of its person's acts for plan dry runs: a burst,
// then one more every planDryShareRefill.
const (
	planDryShareBurst  = 10
	planDryShareRefill = 6 * time.Second
)

// allowDry takes one plan dry run from the share of user's conversation
// conv. A dry run keeps nothing, so the cap on open plans never meets it.
// It is an act at the gate like the proposal it rehearses (one from the
// person's act bucket, delegateHandler step 5); this is the conversation's
// own share on top, so one conversation's loop of dry runs can't spend
// what the person's other conversations need.
func (d *delegateRuntime) allowDry(user, conv string) (ok bool, wait time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.net == nil {
		d.net = map[string]*actBucket{}
	}
	key := "plan\x00" + user + "\x00" + conv
	share := d.net[key]
	if share == nil {
		share = &actBucket{}
		d.net[key] = share
	}
	return share.take(d.clock(), planDryShareBurst, planDryShareRefill)
}
