package server

// startchoice.go — what a tile agent start opens with, and who said so
// (D250; plans/wsagent/approvals-ux.md §2, §3). A start's ticket binds what
// the AGENT chose — the tile, the brief, the kind of act — and the coding
// agent, its model and its mode are the PERSON's: picked on xbin's sheet
// when they approve, among exactly what they could pick by hand there.
// Until they pick, the sheet preselects by one chain, per field:
//
//	this tile's remembered choice > the person's preference >
//	the agent's suggestion > the workspace's default
//
// and says which step each came from. Two rules hold whatever the chain
// says: a mode the agent suggests that doesn't ask before acting is never
// preselected (the asking default is, and the suggestion is shown beside
// it), and nothing here ever names a saved sign-in (D230: the person's own
// unticked line). A start nobody approves — the person's "start without
// asking" — takes the chain without the agent's step (startsUnasked).

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentprefs"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
)

// Where a start's choice came from (preview.session.from's values).
const (
	fromTile       = "tile"       // remembered for this tile
	fromPreference = "preference" // the person's preference
	fromAgent      = "agent"      // suggested by the agent
	fromDefault    = "default"    // the workspace's default
	fromPerson     = "you"        // picked on the approval
)

// startSuggestion is what a start's body names of its session: the agent's
// suggestion, never binding on what opens.
type startSuggestion struct{ Harness, Model, Mode string }

func (g startSuggestion) any() bool { return g.Harness != "" || g.Model != "" || g.Mode != "" }

// modeChoice is one mode a coding agent may start in.
type modeChoice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Asks     bool   `json:"asks"`               // it asks before every edit and command (acp.Provider.Asks)
	Explicit bool   `json:"explicit,omitempty"` // a bypass mode: picked by name, never a default
}

// modelChoice is one model a start may name ("" its default).
type modelChoice struct {
	ID   string `json:"id"`
	From string `json:"from,omitempty"` // connector | tile | preference | agent
}

// harnessChoice is one coding agent the approval offers, with what may be
// picked for it on that tile.
type harnessChoice struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	DefaultMode string        `json:"defaultMode"` // its work mode (D251): what it opens in when nothing else says
	Modes       []modeChoice  `json:"modes"`
	Models      []modelChoice `json:"models"`
	// ModelsKnown: the list is the connector's, whole. Otherwise its own
	// list shows once a session runs (said beside the picker).
	ModelsKnown bool `json:"modelsKnown"`
	NoSignIn    bool `json:"noSignIn,omitempty"`
	// Net is the network a session of it starts on here (start-defaults): what the sheet shows for it
	Net    string `json:"net"`
	SignIn string `json:"signIn,omitempty"`
	// SignInID, SignInTrust: the saved sign-in the approval OFFERS for a
	// session of it (unticked, D230) and the trust line that goes with it.
	SignInID    string `json:"signInId,omitempty"`
	SignInTrust any    `json:"signInTrust,omitempty"`
}

// startPick is what a start opens with as things stand, and where each
// part came from.
type startPick struct {
	Harness, Model, Mode string
	From                 map[string]string // harness | model | mode → from…
	// ModeNotTaken: a bypass (Explicit) mode the agent suggested — shown
	// beside the picker, never picked for the person: one is named by them.
	ModeNotTaken string
	Harnesses    []harnessChoice
}

// workMode is the mode prov opens in when nothing says otherwise: the one
// in which it works through a task in its sandbox without stopping to ask
// (acp.Provider.WorkMode: the one source; D251) — "" for one that opens as
// it opens itself (OpenCode).
func workMode(prov agent.Provider) string {
	return prov.WorkMode()
}

// modesOf are the modes prov may start in, as the Agent tab offers them:
// the catalog's own list, or — a coding agent that keeps its modes to its
// own configuration (OpenCode) — the ones the catalog knows of it; and,
// where its entry names no default mode and nothing else is known, "its
// own default".
func modesOf(prov agent.Provider) []modeChoice {
	var out []modeChoice
	for _, m := range prov.Modes {
		out = append(out, modeChoice{ID: m.ID, Name: cmp.Or(m.Name, m.ID), Asks: prov.Asks(m.ID), Explicit: m.Explicit})
	}
	if len(out) == 0 {
		for _, id := range append(append([]string{}, prov.AskModes...), prov.SafeModes...) {
			if id != "" && !slices.ContainsFunc(out, func(m modeChoice) bool { return m.ID == id }) {
				out = append(out, modeChoice{ID: id, Name: id, Asks: prov.Asks(id)})
			}
		}
	}
	if len(prov.Modes) == 0 && prov.DefaultMode == "" && !hasMode(out, "") {
		// the catalog lists no modes of its own and names no default: it opens as it opens itself
		out = append([]modeChoice{{ID: "", Name: "its own default", Asks: false}}, out...)
	}
	return out // (in the catalog's order: the default is the work mode, whichever, D251)
}

func hasMode(modes []modeChoice, id string) bool {
	return slices.ContainsFunc(modes, func(m modeChoice) bool { return m.ID == id })
}

func explicitMode(modes []modeChoice, id string) bool {
	return slices.ContainsFunc(modes, func(m modeChoice) bool { return m.ID == id && m.Explicit })
}

func hasModel(models []modelChoice, id string) bool {
	return slices.ContainsFunc(models, func(m modelChoice) bool { return m.ID == id })
}

// startPrefsOf is p's agent preferences (the zero value without a store).
func (s *Server) startPrefsOf(user string) agentprefs.Prefs {
	if s.AgentPrefs == nil {
		return agentprefs.Prefs{}
	}
	return s.AgentPrefs.Get(user)
}

// startPick resolves the start p's agent would make on tile — sug being
// what its body names, person what the person picked on the approval (nil:
// nothing yet). why is set when no coding agent may run there.
func (s *Server) startPick(p auth.Principal, tile string, sug startSuggestion, person *delegation.StartChoice) (startPick, string) {
	def := s.startDefaults(p, tile)
	if len(def.Harnesses) == 0 {
		return startPick{}, cmp.Or(def.Why, "no coding agent may run on "+tile)
	}
	prefs := s.startPrefsOf(p.UserID)
	var tileChoice *agentprefs.TileStart
	if ts, ok := prefs.AgentSessions.Tiles[tile]; ok {
		tileChoice = &ts
	}
	allowed := func(id string) bool { _, ok := def.of(id); return id != "" && ok }
	// a suggestion is of something that exists and may run there, or the start is refused as before D250
	if sug.Harness != "" {
		prov, ok := agent.Lookup(sug.Harness)
		switch {
		case !ok:
			return startPick{}, "unknown coding agent \"" + sug.Harness + "\" (GET /api/xbin/agent/providers lists them)"
		case !allowed(sug.Harness):
			return startPick{}, prov.Name + " may not run on " + tile + ": " + cmp.Or(def.Why, "the tile's rules don't allow it")
		case sug.Mode != "" && !hasMode(modesOf(prov), sug.Mode):
			if _, err := prov.ResolveMode(sug.Mode); err != nil {
				return startPick{}, err.Error()
			}
		}
	}
	out := startPick{From: map[string]string{}}
	// the coding agents, with what may be picked of each
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, h := range def.Harnesses {
		prov, ok := agent.Lookup(h.ID)
		if !ok {
			continue
		}
		c := harnessChoice{ID: h.ID, Name: h.Name, DefaultMode: workMode(prov), Modes: modesOf(prov), NoSignIn: h.NoSignIn, Net: def.netFor(h.ID)}
		c.Models = []modelChoice{{ID: ""}}
		if s.Term != nil && h.NoSignIn {
			if ms, _, ok := s.Term.ConnectorModels(ctx, personOf(p).UserID, tile, prov); ok {
				c.ModelsKnown = true
				for _, m := range ms {
					c.Models = append(c.Models, modelChoice{ID: m, From: "connector"})
				}
			}
		}
		// what the person keeps, and what the agent suggests where nothing narrower is known
		add := func(id, from string) {
			if id != "" && !hasModel(c.Models, id) && !c.ModelsKnown {
				c.Models = append(c.Models, modelChoice{ID: id, From: from})
			}
		}
		if tileChoice != nil && tileChoice.Harness == h.ID {
			add(tileChoice.Model, fromTile)
		}
		add(prefs.Harnesses[h.ID].Model, fromPreference)
		if sug.Harness == "" || sug.Harness == h.ID {
			add(sug.Model, fromAgent)
		}
		if s.Term != nil {
			signIn, offer, trust := s.approvalSignIn(p, tile, prov)
			c.SignIn, c.SignInID = signIn, offer
			if tl := trustLine(trust); tl != nil {
				c.SignInTrust = tl
			}
		}
		out.Harnesses = append(out.Harnesses, c)
	}
	// the coding agent
	switch {
	case person != nil && allowed(person.Provider):
		out.Harness, out.From["harness"] = person.Provider, fromPerson
	case tileChoice != nil && allowed(tileChoice.Harness):
		out.Harness, out.From["harness"] = tileChoice.Harness, fromTile
	case allowed(prefs.AgentSessions.Harness):
		out.Harness, out.From["harness"] = prefs.AgentSessions.Harness, fromPreference
	case allowed(sug.Harness):
		out.Harness, out.From["harness"] = sug.Harness, fromAgent
	default:
		out.Harness, out.From["harness"] = def.Harness, fromDefault
	}
	hc := out.Harnesses[slices.IndexFunc(out.Harnesses, func(c harnessChoice) bool { return c.ID == out.Harness })]
	prov, _ := agent.Lookup(out.Harness)
	sameSug := sug.Harness == "" || sug.Harness == out.Harness
	// its mode: any of its modes is a choice (D251) — a bypass one only by the person's own pick on the sheet
	choice := func(m string) bool { return m != "" && hasMode(hc.Modes, m) && !explicitMode(hc.Modes, m) }
	switch {
	case person != nil && person.Provider == out.Harness && hasMode(hc.Modes, person.Mode):
		out.Mode, out.From["mode"] = person.Mode, fromPerson
	case tileChoice != nil && tileChoice.Harness == out.Harness && choice(tileChoice.Mode):
		out.Mode, out.From["mode"] = tileChoice.Mode, fromTile
	case choice(prefs.AgentSessions.Modes[out.Harness]):
		out.Mode, out.From["mode"] = prefs.AgentSessions.Modes[out.Harness], fromPreference
	case sameSug && choice(sug.Mode):
		out.Mode, out.From["mode"] = sug.Mode, fromAgent
	default:
		out.Mode, out.From["mode"] = hc.DefaultMode, fromDefault
	}
	if sameSug && sug.Mode != "" && explicitMode(hc.Modes, sug.Mode) && out.From["mode"] != fromPerson {
		out.ModeNotTaken = sug.Mode
	}
	_ = prov
	// its model
	switch {
	case person != nil && person.Provider == out.Harness && hasModel(hc.Models, person.Model):
		out.Model, out.From["model"] = person.Model, fromPerson
	case tileChoice != nil && tileChoice.Harness == out.Harness && tileChoice.Model != "" && hasModel(hc.Models, tileChoice.Model):
		out.Model, out.From["model"] = tileChoice.Model, fromTile
	case prefs.Harnesses[out.Harness].Model != "" && hasModel(hc.Models, prefs.Harnesses[out.Harness].Model):
		out.Model, out.From["model"] = prefs.Harnesses[out.Harness].Model, fromPreference
	case sameSug && sug.Model != "" && hasModel(hc.Models, sug.Model):
		out.Model, out.From["model"] = sug.Model, fromAgent
	default:
		out.Model, out.From["model"] = "", fromDefault
	}
	return out, ""
}

// pickRefusal is why the person's pick on an approval isn't one they may
// make by hand on tile ("" it is): a coding agent the tile's orgs allow
// and they may start there, one of its modes, one of the models offered.
func pickRefusal(pick startPick, c delegation.StartChoice, tile string) string {
	i := slices.IndexFunc(pick.Harnesses, func(h harnessChoice) bool { return h.ID == c.Provider })
	if i < 0 {
		return "harness: " + c.Provider + " isn't a coding agent you may start on " + tile + " (the approval lists them)"
	}
	h := pick.Harnesses[i]
	if !hasMode(h.Modes, c.Mode) {
		return "mode: " + h.Name + " has no mode " + quoteOr(c.Mode, "(none)") + " here (the approval lists its modes)"
	}
	if !hasModel(h.Models, c.Model) {
		return "model: " + quoteOr(c.Model, "(none)") + " isn't a model offered for " + h.Name + " on " + tile + " (the approval lists them)"
	}
	return ""
}

// bodyOfPreview is a start's body as its card shows it — for a request
// whose body xbind no longer keeps (used: its hash is what the ticket binds)
// — with the agent's suggestion where the card says what it was.
func bodyOfPreview(shown map[string]any) []byte {
	m := map[string]any{"cwd": shown["tile"], "kind": term.KindAgent, "prompt": shown["prompt"]}
	for _, k := range []string{"net", "api", "vm", "gpu", "name"} {
		if v, ok := shown[k]; ok && v != "" && v != nil {
			m[k] = v
		}
	}
	if sg, _ := shown["suggested"].(map[string]any); sg != nil {
		m["provider"], m["model"], m["mode"] = sg["harness"], sg["model"], sg["mode"]
	}
	b, _ := json.Marshal(m)
	return b
}

// ownFields is what of pick c the person changed on the sheet that showed
// them shown: a coding agent other than the one shown, and a model or mode
// other than what came with it — the one shown, or for a coding agent they
// switched to, its default model and its default mode (D250 review M1: a
// field left at its preselection — the agent's suggestion included — is
// not theirs to have remembered).
func ownFields(shown map[string]any, harnesses []harnessChoice, c delegation.StartChoice) []string {
	sp, _ := shown["provider"].(string)
	baseMode, _ := shown["mode"].(string)
	baseModel, _ := shown["model"].(string)
	var own []string
	if c.Provider != sp {
		own = append(own, "harness")
		baseModel, baseMode = "", ""
		if i := slices.IndexFunc(harnesses, func(h harnessChoice) bool { return h.ID == c.Provider }); i >= 0 {
			baseMode = harnesses[i].DefaultMode
		}
	}
	if c.Model != baseModel {
		own = append(own, "model")
	}
	if c.Mode != baseMode {
		own = append(own, "mode")
	}
	return own
}

// rememberedOf is what "Remember for this tile" keeps of c on top of prev
// (D250 review M1): only the fields the person changed (c.Own) — a model or
// a mode with the coding agent it is of; any mode but a bypass one, which
// is named on each approval (D251; the store drops it again). ok false:
// nothing of theirs is left to keep.
func rememberedOf(prev *agentprefs.TileStart, c delegation.StartChoice) (agentprefs.TileStart, bool) {
	prov, _ := agent.Lookup(c.Provider)
	own := slices.DeleteFunc(slices.Clone(c.Own), func(f string) bool { return f == "mode" && !prov.Safe(c.Mode) })
	if len(own) == 0 {
		return agentprefs.TileStart{}, false
	}
	ts := agentprefs.TileStart{Harness: c.Provider}
	if prev != nil && prev.Harness == c.Provider {
		ts = *prev
	}
	if slices.Contains(own, "model") {
		ts.Model = c.Model
	}
	if slices.Contains(own, "mode") {
		ts.Mode = c.Mode
	}
	return ts, true
}

func quoteOr(v, none string) string {
	if v == "" {
		return none
	}
	return `"` + v + `"`
}

// continuable is session sid as a start of p's agent on tile may continue
// it (D250) — what the approval shows of it — or why it may not: only a
// live session of the person's that this agent started, on that tile,
// idle (not working, not waiting for its person).
func (s *Server) continuable(p auth.Principal, sid, tile string) (map[string]any, string) {
	if s.Term == nil || p.Agent == "" {
		return nil, "there is no session to continue here"
	}
	info, live := s.Term.Info(sid)
	if !live {
		return nil, "that session ended"
	}
	mine := false
	for _, si := range s.Term.ListFor(term.HomeKey(personOf(p)), "", nil) {
		mine = mine || si.ID == sid
	}
	switch {
	case !mine || s.Term.AgentOrigin(sid) != p.Agent:
		return nil, "that session isn't one this agent started for its person"
	case s.sessionTile(info.Cwd) != tile:
		return nil, "that session is on another tile"
	case info.Kind != "agent" || info.Status != "idle" || info.Pending > 0 || info.Questions > 0:
		return nil, "that session is busy (working, or waiting for its person)"
	}
	// what the follow-up tells it goes to THAT session's model: judged on its live source, as a message to it is
	// (D250 review M3), never on what a new start there would run on
	if why := s.sendRefusal(p, s.liveTarget(sid, info)); why != "" {
		return nil, why
	}
	prov, _ := agent.Lookup(info.Provider)
	out := map[string]any{"session": sid, "provider": info.Provider, "providerName": cmp.Or(prov.Name, info.Provider), "mode": info.Mode, "asks": prov.Asks(info.Mode),
		"model": info.Model, "name": info.Name, "started": info.Created, "since": info.LastActive, "signIn": ""}
	if si := info.Signin; si != nil && si.Source == "saved" && si.ID != "" {
		// it carries the person's saved sign-in: a fact the sheet shows — they ticked it when the session started, and a
		// follow-up is the same session doing more of what they approved (D251: no tick again)
		out["signIn"], out["signInId"], out["signInName"] = "saved", si.ID, si.Name
	}
	return out, ""
}

// continueSigninStill is why an approved follow-up can't go into live
// session as approved ("" it can): it carries another saved sign-in than
// the one its approval showed (approved: the approval's session preview) —
// no tick is asked for one it carried then (D251), but the session must
// still be the one shown.
func continueSigninStill(live, approved map[string]any) string {
	id, _ := live["signInId"].(string)
	if shown, _ := approved["signInId"].(string); id != shown {
		return "the session your person chose to continue carries another sign-in than the one they were shown: file the start again"
	}
	return ""
}

// pickBody is what an approval's body says of a start's choices (D250).
type pickBody struct {
	Harness, Model, Mode *string
	Remember             bool
	Continue             *bool
	Knows                []string
	Signin               bool // the sign-in line ticked (a new session's: D230)
	Seen                 *seenBasis
}

// seenBasis is what the sheet showed of what xbind resolves again when the
// approval is decided (D250 review L2): the saved sign-in it offered ("" none)
// and the network. A client that sends it is held to it.
type seenBasis struct {
	SignInID *string `json:"signInId"`
	Net      *string `json:"net"`
	// Harness and Mode: for a continue, the session's own as the sheet
	// showed them (D251) — it must still be that session
	Harness *string `json:"harness,omitempty"`
	Mode    *string `json:"mode,omitempty"`
}

// seenChanged is the refusal of an approval whose sheet showed something
// else than xbind resolves now: "seen\x00<what>\x00<words>" — the handler
// answers 409 {error, changed: <what>, reload: true}, nothing decided.
const seenChanged = "seen\x00"

// seenRefusal is why seen isn't what preview (the session preview of what
// the approval opens) says now ("" it is, or the client sent no basis).
func seenRefusal(seen *seenBasis, preview map[string]any) string {
	if seen == nil {
		return ""
	}
	id, _ := preview["signInId"].(string)
	net, _ := preview["net"].(string)
	harness, _ := preview["provider"].(string)
	mode, _ := preview["mode"].(string)
	switch {
	case seen.SignInID != nil && *seen.SignInID != id:
		return seenChanged + "signin\x00the saved sign-in this start offers changed since your sheet showed it: look again — nothing was decided"
	case seen.Net != nil && *seen.Net != net:
		return seenChanged + "net\x00the network this start would have changed since your sheet showed it: look again — nothing was decided"
	case seen.Harness != nil && *seen.Harness != harness, seen.Mode != nil && *seen.Mode != mode:
		return seenChanged + "session\x00the session this would continue is no longer as your sheet showed it (its coding agent or mode changed): look again — nothing was decided"
	}
	return ""
}

func (b pickBody) picks() bool {
	return b.Harness != nil || b.Model != nil || b.Mode != nil || b.Remember || b.Seen != nil
}

// startPickOf is the person's pick on the approval of their pending start
// id, validated as theirs by hand, and the request's preview redrawn for
// it — under the conversation that filed it (its scope, taint, model and
// the org rules for its agent), never under the person's own principal,
// which no org rule for agents holds. nil, nil: nothing to pick (not a
// pending start; or an older client that sent nothing — what was shown is
// what opens). why: refused, with status.
func (s *Server) startPickOf(a delegateActor, id string, b pickBody) (*delegation.StartChoice, map[string]any, int, string) {
	req, found := s.Delegation.Request(a.user, id)
	if !found || req.State != delegation.Pending || req.Kind != delegation.KindSession {
		if b.picks() && found && req.Kind != delegation.KindSession {
			return nil, nil, http.StatusBadRequest, "harness, model, mode and remember are a tile agent start's"
		}
		return nil, nil, 0, ""
	}
	return s.startPickFor(a, req, b)
}

// startPickFor is startPickOf for request req as it stands (a pending one;
// a failed one its person tries again, D250 review L3).
func (s *Server) startPickFor(a delegateActor, req delegation.Request, b pickBody) (*delegation.StartChoice, map[string]any, int, string) {
	if req.Kind != delegation.KindSession {
		return nil, nil, http.StatusBadRequest, "harness, model, mode and remember are a tile agent start's"
	}
	shown, _ := req.Preview["session"].(map[string]any)
	offered, _ := shown["continue"].(map[string]any)
	knows := slices.Contains(b.Knows, "continue")
	if !b.picks() && !(offered != nil && knows) {
		return nil, nil, 0, "" // an older client: the start as it was shown
	}
	if b.Seen == nil || b.Seen.SignInID == nil || b.Seen.Net == nil {
		// picks, and continuing, are new with D250: every client that makes them draws a sheet, and says what it
		// showed of what is resolved again now (D250 re-review item 5) — without it, what is decided isn't held to it
		return nil, nil, http.StatusBadRequest, `an approval that picks the coding agent, model or mode, or continues a session, says what its sheet showed: seen {signInId, net} — "" for none`
	}
	if resume, _ := shown["resume"].(bool); resume {
		if b.picks() {
			return nil, nil, http.StatusBadRequest, "a resumed session keeps its coding agent, model and mode: they aren't picked again"
		}
		return nil, nil, 0, ""
	}
	pp := a.p
	pp.Agent, pp.AgentSession = req.Agent, req.Session
	if h, ok := s.Delegation.AgentSession(a.user, req.Agent, req.Session); ok {
		pp.AgentModel = h.Model
	}
	var body struct {
		Cwd, Provider, Mode, Model string
		Options                    map[string]string
	}
	raw := []byte(req.Body)
	if len(raw) == 0 { // a used request keeps its body's hash only (a failed start its person tries again): from its card
		raw = bodyOfPreview(shown)
	}
	_ = json.Unmarshal(raw, &body)
	sug := startSuggestion{Harness: body.Provider, Model: cmp.Or(body.Model, body.Options["model"]), Mode: body.Mode}
	tile := strings.Trim(body.Cwd, "/")
	now, why := s.startPick(pp, tile, sug, nil) // what may be picked now, by the rules as they stand
	if why != "" {
		return nil, nil, http.StatusConflict, "this start can't open as things stand: " + why + " — deny it, and your agent asks again"
	}
	// continuing the agent's own idle session there — the first choice of a client that shows it — unless the
	// person starts a new one, or picks another coding agent: the session keeps its own coding agent, model, mode
	if offered != nil && knows && (b.Continue == nil || *b.Continue) && (b.Harness == nil || *b.Harness == offered["provider"]) {
		sid, _ := offered["session"].(string)
		prov, _ := offered["provider"].(string)
		if _, why := s.continuable(pp, sid, tile); why != "" {
			return nil, nil, http.StatusConflict, "the session this would continue can't be continued now (" + why + "): start a new session instead"
		}
		if !slices.ContainsFunc(now.Harnesses, func(h harnessChoice) bool { return h.ID == prov }) {
			return nil, nil, http.StatusConflict, "the session this would continue runs " + prov + ", which may no longer run on " + tile + ": start a new session instead"
		}
		live, _ := s.continuable(pp, sid, tile)
		mode, _ := offered["mode"].(string)
		model, _ := offered["model"].(string)
		c := delegation.StartChoice{Provider: prov, Mode: mode, Model: model, Continue: sid}
		out := maps.Clone(req.Preview)
		cont := maps.Clone(shown)
		cont["continuing"] = true
		// the sign-in the tick is held to is the session's own (no trust line: it was asked when the session began)
		cont["signInId"], cont["signIn"] = live["signInId"], live["signIn"]
		delete(cont, "signInTrust")
		if why := seenRefusal(b.Seen, map[string]any{"signInId": live["signInId"], "net": shown["net"], "provider": live["provider"], "mode": live["mode"]}); why != "" {
			return nil, nil, http.StatusConflict, why
		}
		out["session"] = cont
		return &c, out, 0, ""
	}
	c := delegation.StartChoice{Provider: now.Harness, Model: now.Model, Mode: now.Mode, Remember: b.Remember}
	if b.Harness != nil && *b.Harness != now.Harness {
		c.Provider, c.Model, c.Mode = *b.Harness, "", ""
		if i := slices.IndexFunc(now.Harnesses, func(h harnessChoice) bool { return h.ID == c.Provider }); i >= 0 {
			c.Mode = now.Harnesses[i].DefaultMode
		}
	}
	if b.Model != nil {
		c.Model = *b.Model
	}
	if b.Mode != nil {
		c.Mode = *b.Mode
	}
	if why := pickRefusal(now, c, tile); why != "" {
		return nil, nil, http.StatusBadRequest, why
	}
	c.Own = ownFields(shown, now.Harnesses, c)
	redrawn, why := s.sessionPreviewFor(pp, raw, &c)
	if why != "" {
		return nil, nil, http.StatusConflict, "this start can't open as you picked it: " + why
	}
	if by, ok := shown["briefBy"]; ok {
		redrawn["briefBy"] = by
	}
	if why := seenRefusal(b.Seen, redrawn); why != "" {
		return nil, nil, http.StatusConflict, why
	}
	out := maps.Clone(req.Preview)
	out["session"] = redrawn
	return &c, out, 0, ""
}
