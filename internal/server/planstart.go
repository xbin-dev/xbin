package server

// planstart.go — a plan's agent.start line: described (previewStart, for
// plan.go's previewLine) and, at Apply, the open itself, authorised as a
// ticket authorises a single start.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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
	var shown struct{ Provider, Mode, Model, Net string }
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
	// what the review showed is what opens (D250 review H1): the coding agent, mode and model the line resolved
	// — Picked, so the open takes them whatever the call body names (the agent's own suggestion, which the
	// chain may have outranked: the person's choice for the tile, a suggested mode that doesn't ask)
	ctx = context.WithValue(auth.WithPrincipal(ctx, a.p), delegateStartKey{}, &term.DelegateStart{Approved: true, Picked: true, Provider: shown.Provider, Mode: shown.Mode, Model: shown.Model, Net: shown.Net,
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

// previewStart describes agent.start {tile, brief, harness?, model?,
// mode?}: a tile agent started on tile with the brief as its first prompt —
// what a single start's request shows (sessionPreview), as a line. The
// harness, model and mode are the agent's suggestion, as on a single start
// (D250): the person's choice for the tile and their preference outrank
// them, and a suggested mode that doesn't ask is never taken (the line says
// so) — a plan's review has no pickers, so the line resolves by that chain
// and shows where each part came from. A tile an
// earlier line creates can't be asked yet: the start is described from the
// person's preferences and the harness catalog, and checked when applied.
// brief is the agent's text, or {"template": "update"}: xbind's update
// brief for the tile, which xbind writes here — so the review labels the
// one xbin's and the other the agent's, whatever the text says (D248
// review, finding 7).
func (s *Server) previewStart(a planAgent, op delegation.ChangeOp, planned []delegation.PlannedTile) (delegation.ChangePreview, error) {
	var in struct {
		Tile     string          `json:"tile"`
		BriefRaw json.RawMessage `json:"brief"`
		Harness  string          `json:"harness"`
		Model    string          `json:"model"`
		Mode     string          `json:"mode"`
		Brief    string          `json:"-"`
	}
	if err := StrictDecode(op.Args, &in); err != nil { // one reading of them only (D252)
		return delegation.ChangePreview{}, fmt.Errorf("its arguments don't parse: %w", err)
	}
	in.Tile = strings.Trim(in.Tile, "/")
	briefBy := BriefByAgent
	if err := json.Unmarshal(in.BriefRaw, &in.Brief); err != nil && len(in.BriefRaw) > 0 {
		if !nameOK(in.Tile) {
			return delegation.ChangePreview{}, errors.New("tile, harness and mode are names")
		}
		for _, t := range planned {
			if t.Path == in.Tile {
				return delegation.ChangePreview{}, errors.New(`the brief {"template": "update"} is for a tile that has an update: ` + in.Tile + " is one this plan makes")
			}
		}
		body, _ := json.Marshal(map[string]any{"cwd": in.Tile, "brief": in.BriefRaw})
		resolved, by, bad := s.startBrief(a.p, body)
		if bad != "" {
			return delegation.ChangePreview{}, errors.New(bad)
		}
		var r struct{ Prompt string }
		_ = json.Unmarshal(resolved, &r)
		in.Brief, briefBy = r.Prompt, by
	}
	switch {
	case in.Tile == "" || strings.TrimSpace(in.Brief) == "":
		return delegation.ChangePreview{}, errors.New("agent.start names a tile and the brief the tile agent is told first")
	case !nameOK(in.Tile) || !nameOK(in.Harness) || !nameOK(in.Mode) || len(in.Model) > 256:
		return delegation.ChangePreview{}, errors.New("tile, harness, model and mode are names")
	case len(in.Brief) > maxStartPrompt:
		return delegation.ChangePreview{}, fmt.Errorf("a start's brief is at most %d KiB", maxStartPrompt>>10)
	}
	call := map[string]any{"cwd": in.Tile, "kind": term.KindAgent, "prompt": in.Brief}
	if in.Harness != "" {
		call["provider"] = in.Harness
	}
	if in.Mode != "" {
		call["mode"] = in.Mode
	}
	if in.Model != "" {
		call["model"] = in.Model
	}
	body, _ := json.Marshal(call)
	pv := delegation.ChangePreview{Line: "Start a tile agent in " + in.Tile,
		Call:       delegation.AdminCall{Method: http.MethodPost, Path: "/term/sessions", Body: body},
		UndoByHand: "xbin doesn't undo what a tile agent did: ending its session, and reverting what it changed, is yours to do in " + in.Tile}
	if a.gone != "" {
		pv.Blocked = a.gone
		return pv, nil
	}
	p := a.p
	var shown map[string]any
	n, owner := 0, ""
	for _, t := range planned {
		if t.Path == in.Tile {
			n, owner = t.N, t.Owner
		}
	}
	if n == 0 {
		var why string
		if shown, why = s.sessionPreview(p, body); why != "" {
			pv.Blocked = why
			return pv, nil
		}
		delete(shown, "choices") // a plan's review picks nothing: the line is what the chain resolved (D250)
		pv.Tiles = []string{in.Tile}
	} else {
		pv.Uses, pv.Deferred = []int{n}, true
		ask := sessionAskOf(body)
		ask.planned = true
		if _, why := s.startRefusal(p, ask); why != "" {
			pv.Blocked = why
			return pv, nil
		}
		def := s.startDefaults(p, in.Tile)
		if _, ok := agent.Lookup(in.Harness); in.Harness != "" && !ok {
			return delegation.ChangePreview{}, fmt.Errorf("unknown coding agent %q", in.Harness)
		}
		pick, why := s.startPick(p, in.Tile, startSuggestion{Harness: in.Harness, Model: in.Model, Mode: in.Mode}, nil)
		if why != "" {
			pv.Blocked = why
			return pv, nil
		}
		harness, mode := pick.Harness, pick.Mode
		prov, ok := agent.Lookup(harness)
		if !ok {
			return delegation.ChangePreview{}, fmt.Errorf("unknown coding agent %q", harness)
		}
		mode, err := prov.ResolveMode(mode)
		if err != nil {
			return delegation.ChangePreview{}, err
		}
		shown = map[string]any{"tile": in.Tile, "provider": prov.ID, "providerName": prov.Name, "mode": mode, "asks": prov.Asks(mode),
			"options": map[string]string{}, "prompt": in.Brief, "net": def.netFor(harness), "api": true, "vm": false, "gpu": "", "model": pick.Model, "name": "", "resume": false,
			"from": pick.From}
		if pick.ModeNotTaken != "" {
			shown["modeNotTaken"] = pick.ModeNotTaken
		}
		if pick.Model != "" {
			shown["options"] = map[string]string{"model": pick.Model}
		}
		// what it runs on as things are, and — a tile this plan makes the
		// person's own — their default saved sign-in, OFFERED like any
		// start's (D230: the line's own tick): the open judges the tile
		// itself once it exists
		if h, _ := def.of(harness); h.SignIn != "" {
			shown["signIn"] = h.SignIn
		}
		if id := s.plannedSignIn(p, in.Tile, owner, prov); id != "" {
			shown["signInId"] = id
		}
	}
	name, _ := shown["providerName"].(string)
	mode, _ := shown["mode"].(string)
	asks, _ := shown["asks"].(bool)
	// a coding agent whose catalog entry names no default mode (OpenCode) opens as it opens itself: said so,
	// not as a mode ""
	inMode := fmt.Sprintf("in its mode %q", mode)
	if mode == "" {
		inMode = "in its own default mode"
	}
	pv.Line = fmt.Sprintf("Start a tile agent in %s — %s, %s", in.Tile, name, inMode)
	// what the mode means, beside its name (D246; the feedback's E: "read-only" was read as the file permissions) — a
	// fact either way, never a warning (D251)
	if asks {
		pv.Line += ", which asks before it edits or runs anything"
	} else {
		pv.Line += ", which acts without asking"
	}
	shown["briefBy"] = briefBy // who wrote what it is told first: the review's label, never the text's own claim
	pv.Start, _ = json.Marshal(shown)
	// what the start resolves to is part of what was shown: the network and
	// the API access too, which a tile's org rules decide once it exists —
	// and the brief xbind writes for a named one, at each look (an agent's
	// text is its arguments: the line's basis already)
	pinned := []any{shown["provider"], mode, asks, shown["net"], shown["api"], shown["vm"], shown["gpu"], shown["model"]}
	if briefBy == BriefByXbin {
		pinned = append(pinned, delegation.BodyHash([]byte(in.Brief)))
	}
	pin, _ := json.Marshal(pinned)
	pv.Pin = delegation.BodyHash(pin)
	// the review picks nothing: the line is what runs, and where another is picked (D250 review H1)
	pv.Effects = append(pv.Effects, "What runs is this line's coding agent, model and mode. To run others, ask your agent for a single start's approval, where you pick them, or change your choice for the tile in your preferences.")
	if nt, _ := shown["modeNotTaken"].(string); nt != "" {
		// the agent's suggestion never picks a bypass mode — the review has no picker to pick it
		pv.Effects = append(pv.Effects, fmt.Sprintf("Your agent suggested the mode %q, which skips every check: not taken. To start it that way, approve a single start where you can pick it.", nt))
	}
	if asks {
		pv.Effects = append(pv.Effects, "It asks you before it edits a file or runs a command: its questions and permission requests come to you, never to your workspace agent.")
	} else {
		// a fact, not a warning: an ordinary line, ticked like the others (D251)
		pv.Effects = append(pv.Effects, "It edits files and runs commands in this tile's sandbox without asking you. You approve what your agent tells it.")
	}
	return pv, nil
}
