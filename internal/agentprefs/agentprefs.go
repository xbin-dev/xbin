// Package agentprefs keeps each person's agent preferences on the server
// (D229): the coding-agent harness they start by default, what each
// harness starts with (mode, model, options, whose credentials), and what
// their workspace agent may start a tile agent session with. Before, the
// tile window's launcher kept its part of this in the browser.
//
// One JSON file per person, data/agent-prefs/<key>.json under the
// workspace root — xbind's own directory, which no sandbox mounts and no
// tile reads. The routes (GET and PUT /api/xbin/agent/prefs) answer a
// person's own session only: what a workspace agent may start without
// asking must never be something a tile, or the agent itself, can write.
//
// The file is a convenience for sessions people start themselves and a
// rule for sessions their workspace agent starts, so the second part is
// re-checked on every read against the harness catalog (AgentStart): a
// stored mode this xbind doesn't know to keep asking is never used. A user
// id can be given to a new person after the old one was deleted; the file
// records whose it is (the account's creation stamp), and another person's
// AgentSessions read as the defaults.
package agentprefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// Harness is what a person's sessions of one harness start with.
type Harness struct {
	// Mode is the permission mode a new session starts in; "" the
	// provider's default. Never a bypass mode: those are picked per session.
	Mode string `json:"mode,omitempty"`
	// Model is the model option a new session is set to; "" the agent's own.
	Model string `json:"model,omitempty"`
	// Options are the agent's other session settings (effort, …) by id.
	Options map[string]string `json:"options,omitempty"`
	// Credentials: "own" (my own sign-in) or "connector" (the workspace's
	// LLM connector) for this harness; "" follows the workspace's default.
	Credentials string `json:"credentials,omitempty"`
}

// TileStart is a remembered choice for one tile's agent-started sessions.
type TileStart struct {
	Harness string `json:"harness"`
	Model   string `json:"model,omitempty"`
	Mode    string `json:"mode,omitempty"` // never a bypass mode; one that doesn't ask is the approval's preselection only
	At      int64  `json:"at,omitempty"`   // when (unix seconds): the oldest go past MaxTiles
}

// MaxTiles bounds AgentSessions.Tiles.
const MaxTiles = 200

// The values of AgentSessions.Start.
const (
	StartAsk   = "ask"   // the workspace agent asks before it starts a session (the default)
	StartAllow = "allow" // it may start one without asking
)

// AgentSessions is what a person lets their workspace agent start tile
// agent sessions with.
type AgentSessions struct {
	Start string `json:"start"`
	// Harness is the coding agent such a session starts on, where the tile
	// allows it ("" the workspace's default) — the person's preference,
	// which outranks the agent's suggestion on the approval (D250).
	Harness string `json:"harness,omitempty"`
	// Tiles: per tile, the choice the person last approved there with
	// "Remember for this tile" — the coding agent, its model and its mode
	// (never a bypass mode). It outranks Harness, the models of Harnesses
	// and Modes on that tile's approvals (D250) — preselected there, with a
	// mode that doesn't ask warned as ever; a start without asking takes
	// its coding agent and model, and its mode only from Modes. At most
	// MaxTiles.
	Tiles map[string]TileStart `json:"tiles,omitempty"`
	// Modes: harness id → the mode such a session starts in. A harness not
	// named starts in its asking mode. Never a bypass mode.
	Modes map[string]string `json:"modes,omitempty"`
	// Net is the session's network picker value; "" the launcher's default.
	Net string `json:"net,omitempty"`
	// API: the session gets tile-API access (the launcher's default: yes).
	API *bool `json:"api,omitempty"`
}

// Prefs is one person's agent preferences.
type Prefs struct {
	// Harness is the harness a new agent session starts with by default
	// (the last one they picked); "" for none yet.
	Harness string `json:"harness"`
	// LastKind is the launcher's last choice: "shell", "agent" or "".
	LastKind      string             `json:"lastKind"`
	Harnesses     map[string]Harness `json:"harnesses"`
	AgentSessions AgentSessions      `json:"agentSessions"`
}

// file is Prefs on disk, with whose they are.
type file struct {
	Prefs
	Person int64 `json:"person,omitempty"` // the account's creation stamp
}

func (p Prefs) filled() Prefs {
	if p.Harnesses == nil {
		p.Harnesses = map[string]Harness{}
	}
	if p.AgentSessions.Start != StartAllow {
		p.AgentSessions.Start = StartAsk
	}
	return p
}

// Store is the people's preferences under one workspace root.
type Store struct {
	dir string
	// Person answers an account's creation stamp (users.User.Created),
	// false for no such person; nil ⇒ nobody's AgentSessions are theirs
	// (they read as the defaults).
	Person func(userID string) (created int64, ok bool)
	// Lookup finds a harness in this xbind's catalog; nil ⇒ agent.Lookup.
	Lookup func(id string) (agent.Provider, bool)

	mu sync.Mutex
}

// New is the store under the workspace root.
func New(root string) *Store { return &Store{dir: filepath.Join(root, "data", "agent-prefs")} }

func (s *Store) path(userID string) string {
	return filepath.Join(s.dir, util.CompKey(userID)+".json")
}

func (s *Store) lookup(id string) (agent.Provider, bool) {
	if s.Lookup != nil {
		return s.Lookup(id)
	}
	return agent.Lookup(id)
}

func (s *Store) stamp(userID string) int64 {
	if s.Person == nil {
		return -1
	}
	if c, ok := s.Person(userID); ok {
		return c
	}
	return -1
}

// read is userID's file: the zero file when there is none, an error when
// it can't be read (never overwritten then).
func (s *Store) read(userID string) (file, error) {
	var f file
	b, err := os.ReadFile(s.path(userID))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return file{}, fmt.Errorf("agent preferences of %s: %w", userID, err)
	}
	return f, nil
}

// Get is userID's preferences, defaults filled in. What can't be read is
// the defaults: the person is asked again rather than a guess acted on.
func (s *Store) Get(userID string) Prefs {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(userID)
	if err != nil {
		return Prefs{}.filled()
	}
	return s.own(userID, f)
}

// own is f's preferences as userID's: another person's AgentSessions (the
// id was reused) are not theirs.
func (s *Store) own(userID string, f file) Prefs {
	p := f.Prefs
	if stamp := s.stamp(userID); stamp < 0 || f.Person != stamp {
		p.AgentSessions = AgentSessions{}
	}
	return s.sane(p.filled())
}

// sane is p as the rules read it, whatever the file holds (D250 review M1;
// re-review item 1): no remembered or preferred mode that acts without
// asking — the predicate the approval's warning uses (acp.Provider.Asks) —
// and no choice of a coding agent this xbind doesn't know. A value that
// slipped past check (a file an earlier xbind wrote under looser rules:
// v0.3.83 took OpenCode's "plan" as an asking mode) is ignored, never an
// error — so it never blocks a later write of something else (the Agent
// tab's launcher writes on every by-hand start). A coding agent with no
// mode that asks stays the person's choice (re-review item 3): only its
// mode is never kept.
func (s *Store) sane(p Prefs) Prefs {
	a := &p.AgentSessions
	if _, ok := s.lookup(a.Harness); a.Harness != "" && !ok {
		a.Harness = ""
	}
	if len(a.Modes) > 0 {
		modes := make(map[string]string, len(a.Modes))
		for id, mode := range a.Modes {
			if prov, known := s.lookup(id); known && (!prov.Safe(mode) || !prov.Asks(mode)) {
				continue
			}
			modes[id] = mode
		}
		a.Modes = modes
	}
	if len(a.Tiles) > 0 {
		tiles := make(map[string]TileStart, len(a.Tiles))
		for tile, ts := range a.Tiles {
			prov, ok := s.lookup(ts.Harness)
			if !ok {
				continue
			}
			if ts.Mode != "" && !prov.Asks(ts.Mode) {
				ts.Mode = ""
			}
			tiles[tile] = ts
		}
		a.Tiles = tiles
	}
	return p
}

// Update applies patch — a JSON merge patch (RFC 7386): keys present
// replace, null removes, objects merge — to userID's preferences, checks
// the result and stores it. It answers the preferences now.
func (s *Store) Update(userID string, patch []byte) (Prefs, error) {
	if userID == "" {
		return Prefs{}, errors.New("agent preferences are a person's")
	}
	var pv any
	if err := json.Unmarshal(patch, &pv); err != nil {
		return Prefs{}, errors.New("the body is a JSON object of the preferences to change")
	}
	pm, ok := pv.(map[string]any)
	if !ok {
		return Prefs{}, errors.New("the body is a JSON object of the preferences to change")
	}
	for k := range pm {
		switch k {
		case "harness", "lastKind", "harnesses", "agentSessions":
		default:
			return Prefs{}, fmt.Errorf("unknown key %q (harness, lastKind, harnesses, agentSessions)", k)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(userID)
	if err != nil {
		return Prefs{}, err
	}
	cur := s.own(userID, f)
	cb, _ := json.Marshal(cur)
	var doc any
	_ = json.Unmarshal(cb, &doc)
	mb, _ := json.Marshal(merge(doc, pm))
	var next Prefs
	dec := json.NewDecoder(bytes.NewReader(mb))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return Prefs{}, fmt.Errorf("preferences: %s", shapeErr(err))
	}
	if err := s.check(&next); err != nil {
		return Prefs{}, err
	}
	out := file{Prefs: next}
	if stamp := s.stamp(userID); stamp >= 0 {
		out.Person = stamp
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := fsutil.WriteFileAtomicIn(s.path(userID), b, 0o600); err != nil {
		return Prefs{}, err
	}
	return s.own(userID, out), nil
}

func shapeErr(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return fmt.Sprintf("%s is a %s, not a %s", te.Field, te.Type, te.Value)
	}
	return err.Error()
}

// merge is RFC 7386: patch into target.
func merge(target any, patch map[string]any) any {
	t, ok := target.(map[string]any)
	if !ok {
		t = map[string]any{}
	}
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(t, k)
		case map[string]any:
			t[k] = merge(t[k], pv)
		default:
			t[k] = v
		}
	}
	return t
}

var (
	idRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	netRe = regexp.MustCompile(`^[a-z][a-z0-9:_.-]{0,79}$`)
)

const (
	maxHarnesses = 32
	maxOptions   = 16
	maxValue     = 256
)

// check validates p as written and drops what is empty.
func (s *Store) check(p *Prefs) error {
	if p.Harness != "" && !idRe.MatchString(p.Harness) {
		return fmt.Errorf("harness: %q isn't a harness id", p.Harness)
	}
	if p.Harness != "" {
		if _, ok := s.lookup(p.Harness); !ok {
			return fmt.Errorf("harness: no harness %q (GET /api/xbin/agent/providers lists them)", p.Harness)
		}
	}
	switch p.LastKind {
	case "", "shell", "agent":
	default:
		return fmt.Errorf(`lastKind: "shell" or "agent", not %q`, p.LastKind)
	}
	if len(p.Harnesses) > maxHarnesses {
		return fmt.Errorf("harnesses: at most %d", maxHarnesses)
	}
	for id, h := range p.Harnesses {
		if !idRe.MatchString(id) {
			return fmt.Errorf("harnesses: %q isn't a harness id", id)
		}
		prov, known := s.lookup(id)
		if h.Mode != "" {
			if len(h.Mode) > 64 {
				return fmt.Errorf("harnesses.%s.mode: too long", id)
			}
			// a harness this xbind doesn't run keeps what it was given: it
			// is asked again when it does (the launcher sends it, and the
			// session's own gates judge it)
			if known && !savable(prov, h.Mode) {
				return fmt.Errorf("harnesses.%s.mode: %s", id, modeRefusal(prov, h.Mode))
			}
		}
		if len(h.Model) > maxValue {
			return fmt.Errorf("harnesses.%s.model: too long", id)
		}
		switch h.Credentials {
		case "", agentpolicy.CredentialsOwn, agentpolicy.CredentialsConnector:
		default:
			return fmt.Errorf(`harnesses.%s.credentials: "own" or "connector", not %q`, id, h.Credentials)
		}
		if len(h.Options) > maxOptions {
			return fmt.Errorf("harnesses.%s.options: at most %d", id, maxOptions)
		}
		for k, v := range h.Options {
			switch {
			case k == "" || len(k) > 64 || len(v) > maxValue:
				return fmt.Errorf("harnesses.%s.options: %q is too long, or has no id", id, k)
			case k == "mode":
				return fmt.Errorf("harnesses.%s.options: the mode is harnesses.%s.mode", id, id)
			case k == "model":
				return fmt.Errorf("harnesses.%s.options: the model is harnesses.%s.model", id, id)
			}
		}
		if len(h.Options) == 0 {
			h.Options = nil
		}
		if h.Mode == "" && h.Model == "" && h.Options == nil && h.Credentials == "" {
			delete(p.Harnesses, id)
		} else {
			p.Harnesses[id] = h
		}
	}
	a := &p.AgentSessions
	switch a.Start {
	case "":
		a.Start = StartAsk
	case StartAsk, StartAllow:
	default:
		return fmt.Errorf(`agentSessions.start: "ask" or "allow", not %q`, a.Start)
	}
	for id, mode := range a.Modes {
		if !idRe.MatchString(id) {
			return fmt.Errorf("agentSessions.modes: %q isn't a harness id", id)
		}
		prov, known := s.lookup(id)
		switch {
		case mode == "":
			delete(a.Modes, id)
		case !known:
			return fmt.Errorf("agentSessions.modes: no harness %q", id)
		case !prov.Safe(mode): // a bypass mode, or one the catalog doesn't know
			return fmt.Errorf("agentSessions.modes.%s: %s", id, modeRefusal(prov, mode))
		case !prov.Asks(mode):
			// a session nobody watches start: only a mode that asks before
			// every edit and command (the harness's ask or plan mode)
			return fmt.Errorf("agentSessions.modes.%s: %s isn't a mode in which %s asks before every edit and command — a session your agent starts without asking you opens only in one that does (%s)", id, mode, prov.Name, strings.Join(askModes(prov), ", "))
		}
	}
	if len(a.Modes) == 0 {
		a.Modes = nil
	}
	if a.Harness != "" {
		if !idRe.MatchString(a.Harness) {
			return fmt.Errorf("agentSessions.harness: %q isn't a harness id", a.Harness)
		}
		if _, ok := s.lookup(a.Harness); !ok {
			return fmt.Errorf("agentSessions.harness: no harness %q (GET /api/xbin/agent/providers lists them)", a.Harness)
		}
		// (one with no mode that asks — OpenCode — may be the person's choice: preselected on each approval with
		// its warning, never started without one: D250 re-review item 3)
	}
	if len(a.Tiles) > MaxTiles {
		return fmt.Errorf("agentSessions.tiles: at most %d", MaxTiles)
	}
	for tile, ts := range a.Tiles {
		if tile == "" || len(tile) > 256 || strings.Contains(tile, "..") || strings.HasPrefix(tile, "/") {
			return fmt.Errorf("agentSessions.tiles: %q isn't a tile's path", tile)
		}
		if ts.Harness == "" && ts.Model == "" && ts.Mode == "" {
			delete(a.Tiles, tile)
			continue
		}
		prov, known := s.lookup(ts.Harness)
		switch {
		case !idRe.MatchString(ts.Harness) || !known:
			return fmt.Errorf("agentSessions.tiles.%s.harness: no harness %q", tile, ts.Harness)
		case len(ts.Model) > maxValue:
			return fmt.Errorf("agentSessions.tiles.%s.model: too long", tile)
		case ts.Mode != "" && !prov.Asks(ts.Mode):
			// only a mode that asks is remembered (D250 review M1): one that acts without asking is the person's
			// explicit pick on each approval, warned there — never a preselection
			return fmt.Errorf("agentSessions.tiles.%s.mode: %s", tile, modeRefusal(prov, ts.Mode))
		}
	}
	if len(a.Tiles) == 0 {
		a.Tiles = nil
	}
	if a.Net != "" && !netRe.MatchString(a.Net) {
		return fmt.Errorf("agentSessions.net: %q isn't a network choice", a.Net)
	}
	return nil
}

// RememberTile keeps ts as userID's choice for tile (D250: "Remember for
// this tile" on an approval — the person's own act, which only xbind's
// approval route makes, with the fields the person themselves changed): the
// oldest remembered tile goes past MaxTiles. A mode that acts without
// asking is dropped — for any coding agent, by the predicate the approval's
// warning uses (acp.Provider.Asks); the coding agent and model are kept,
// OpenCode's too (D250 review M1; re-review item 3). kept says what was.
func (s *Store) RememberTile(userID, tile string, ts TileStart, now int64) error {
	if userID == "" || tile == "" {
		return errors.New("a remembered choice is a person's, for a tile")
	}
	prov, ok := s.lookup(ts.Harness)
	if !ok {
		return fmt.Errorf("no harness %q", ts.Harness)
	}
	if ts.Mode != "" && !prov.Asks(ts.Mode) {
		ts.Mode = ""
	}
	ts.At = now
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read(userID)
	if err != nil {
		return err
	}
	next := s.own(userID, f)
	tiles := map[string]TileStart{}
	for k, v := range next.AgentSessions.Tiles {
		tiles[k] = v
	}
	tiles[tile] = ts // whole: a mode the last pick had no word on doesn't linger
	for len(tiles) > MaxTiles {
		oldest, at := "", int64(0)
		for k, v := range tiles {
			if k != tile && (oldest == "" || v.At < at) {
				oldest, at = k, v.At
			}
		}
		delete(tiles, oldest)
	}
	next.AgentSessions.Tiles = tiles
	if err := s.check(&next); err != nil {
		return err
	}
	out := file{Prefs: next}
	if stamp := s.stamp(userID); stamp >= 0 {
		out.Person = stamp
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return fsutil.WriteFileAtomicIn(s.path(userID), b, 0o600)
}

// TileChoice is userID's remembered choice for tile, and their preferred
// coding agent for sessions their agent starts ("" none).
func (s *Store) TileChoice(userID, tile string) (remembered *TileStart, harness string) {
	p := s.Get(userID)
	if ts, ok := p.AgentSessions.Tiles[tile]; ok {
		remembered = &ts
	}
	return remembered, p.AgentSessions.Harness
}

// savable: mode can be a harness's saved default — one of its modes that
// isn't a bypass mode (Explicit modes are asked for by name, per session).
func savable(prov agent.Provider, mode string) bool {
	if len(prov.Modes) == 0 {
		return true
	}
	return prov.Safe(mode)
}

func modeRefusal(prov agent.Provider, mode string) string {
	for _, m := range prov.Modes {
		if m.ID == mode && m.Explicit {
			return fmt.Sprintf("%s (%s) is picked per session, it can't be saved as a default", mode, m.Name)
		}
	}
	return fmt.Sprintf("%s has no mode %q that keeps asking", prov.ID, mode)
}

// AgentStart is what a person's workspace agent starts a tile agent
// session of one harness with.
type AgentStart struct {
	// Ask: the person is asked before the session starts (the default).
	Ask bool
	// Mode is the session's permission mode: the person's choice for the
	// harness when the catalog knows it keeps asking, else the provider's
	// asking mode.
	Mode string
	// Model is the person's saved model for the harness ("" the agent's own).
	Model string
	// Net is the network picker value ("" the default network) and API
	// whether the session gets tile-API access; the session's own clamps
	// still apply to both.
	Net string
	API bool
}

// AgentStart resolves p for a session of prov that the person's workspace
// agent starts. The stored mode is checked again here: a mode the catalog
// doesn't mark safe is never returned, whatever the file says.
func (p Prefs) AgentStart(prov agent.Provider) AgentStart {
	// the asking mode xbind's start-defaults says too (acp.Provider.AskingMode: one source); "" for a harness
	// with none, which never starts without asking (StartPrefs)
	a := AgentStart{Ask: p.AgentSessions.Start != StartAllow, Net: p.AgentSessions.Net, API: true,
		Mode: prov.AskingMode(), Model: p.Harnesses[prov.ID].Model}
	if m := p.AgentSessions.Modes[prov.ID]; m != "" && prov.Asks(m) {
		a.Mode = m
	}
	if p.AgentSessions.API != nil {
		a.API = *p.AgentSessions.API
	}
	return a
}

// StartPrefs is AgentStartFor in the shape the delegation package asks
// (its Policy.StartPrefs): the mode, network and tile-API access a session
// of harness that userID's workspace agent starts opens with, and ok —
// whether they let it start one without asking each time. ok is false for
// a harness this xbind doesn't run.
func (s *Store) StartPrefs(userID, harness string) (mode, net string, api, ok bool) {
	a, known := s.AgentStartFor(userID, harness)
	return a.Mode, a.Net, a.API, known && !a.Ask && a.Mode != "" // (no mode that asks: never unasked — D250 review M2)
}

// AgentStartFor is Get(userID).AgentStart for the harness, and whether
// this xbind runs that harness.
func (s *Store) AgentStartFor(userID, harness string) (AgentStart, bool) {
	prov, ok := s.lookup(harness)
	if !ok {
		return AgentStart{Ask: true}, false
	}
	return s.Get(userID).AgentStart(prov), true
}

// askModes are the modes of prov in which it asks before every edit and
// command.
func askModes(prov agent.Provider) []string {
	var out []string
	for _, m := range []string{prov.ApproveMode, prov.PlanMode} {
		if m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}
