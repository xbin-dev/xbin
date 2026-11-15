package broker

// agentlevel.go — the level of a tile's API a person's workspace agent may
// use ("agent": off | read | full in the tile's manifest; docs/auth.md
// §Delegation), as xbind holds it (D228, the second review's N3).
//
// The manifest is the tile's own file: its terminals and tile agents write
// it, and a workspace agent can prompt a tile agent. So the manifest alone
// never RAISES the level. xbind keeps, in its own store
// (data/agent-levels.json), the level each tile last had approved:
//
//   - a tile that never stated a level is full (D228's first decision),
//     and nothing is stored for it;
//   - its first explicit value is taken as it is, and stored;
//   - a manifest that states LESS than the stored level lowers it at once;
//   - a manifest that states MORE — or drops the key after having stated
//     less — changes nothing: the stored level stays in force, the raise
//     is a pending request (GET /agent-levels, an alert for the people who
//     manage the tile), and a person who may manage the tile approves it
//     in their own session (PUT /agent-levels) — never a tile's
//     credential, a workspace agent or a view-as session;
//   - a record outlives its tile: one deleted and made again at the path
//     starts where the old one was.
//
// The consoles a workspace is created with are held at the level this
// release gives them (builtinAgentLevels), whatever the workspace's copy
// of their manifest says: a workspace made before those manifests said
// "off" has copies that say nothing.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
)

// builtinAgentLevels are the tiles of every workspace whose level is this
// release's, not their manifest's: the consoles.
var builtinAgentLevels = map[string]string{
	"tiles/admin":         registry.AgentOff,
	"tiles/organisations": registry.AgentOff,
	"tiles/manager":       registry.AgentOff,
}

func agentRank(level string) int {
	switch level {
	case registry.AgentFull:
		return 2
	case registry.AgentRead:
		return 1
	}
	return 0 // off, and any word this xbind doesn't know
}

type agentLevelRec struct {
	Level string `json:"level"`
	At    int64  `json:"at"`
	By    string `json:"by"` // "manifest" (stated or lowered there), or the person who approved a raise
}

type agentLevelDoc struct {
	Schema int                      `json:"schema"`
	Tiles  map[string]agentLevelRec `json:"tiles"`
}

// agentLevels is the store. A file that can't be read holds every stated
// tile at off: unknown is never wider.
type agentLevels struct {
	mu     sync.Mutex
	loaded bool
	broken error
	tiles  map[string]agentLevelRec
}

func (b *Broker) agentLevelsPath() string {
	return filepath.Join(b.Reg.Root, "data", "agent-levels.json")
}

// levelsLocked loads the store once; the caller holds b.agentLv.mu.
func (b *Broker) levelsLocked() *agentLevels {
	lv := &b.agentLv
	if lv.loaded {
		return lv
	}
	lv.loaded, lv.tiles = true, map[string]agentLevelRec{}
	raw, err := os.ReadFile(b.agentLevelsPath())
	switch {
	case os.IsNotExist(err):
	case err != nil:
		lv.broken = err
	default:
		var doc agentLevelDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			lv.broken = err
		} else if doc.Tiles != nil {
			lv.tiles = doc.Tiles
		}
	}
	return lv
}

func (b *Broker) saveLevelsLocked(lv *agentLevels) error {
	raw, err := json.MarshalIndent(agentLevelDoc{Schema: 1, Tiles: lv.tiles}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.agentLevelsPath()), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(b.agentLevelsPath(), raw, 0o600)
}

// agentLevel is the level in force for tile c, and the level its manifest
// asks for when that is more ("" none): the file comment's rules. It
// records a first statement and a lowering as it sees them.
func (b *Broker) agentLevel(c *registry.Component) (inForce, requested string) {
	if lvl, ok := builtinAgentLevels[c.Path]; ok {
		return lvl, ""
	}
	stated, want := c.Manifest.Agent != "", c.Manifest.AgentAccess()
	b.agentLv.mu.Lock()
	defer b.agentLv.mu.Unlock()
	lv := b.levelsLocked()
	if lv.broken != nil { // the record of what was approved can't be read
		if stated || len(lv.tiles) > 0 {
			return registry.AgentOff, ""
		}
		return want, ""
	}
	rec, known := lv.tiles[c.Path]
	switch {
	case !known && !stated:
		return registry.AgentFull, ""
	case !known, stated && agentRank(want) < agentRank(rec.Level):
		// the first explicit value, or a lower one: the manifest's, at once
		lv.tiles[c.Path] = agentLevelRec{Level: want, At: time.Now().Unix(), By: "manifest"}
		if err := b.saveLevelsLocked(lv); err != nil {
			// not recorded: a later raise would read as a first statement —
			// hold the tile at what is safe for both
			return registry.AgentOff, ""
		}
		return want, ""
	case agentRank(want) > agentRank(rec.Level):
		return rec.Level, want // a raise, or the key dropped: a person decides
	}
	return rec.Level, ""
}

// AgentLevelOf is the level in force for tile, and the raise its manifest
// asks for ("" none) — for the server's /components rows.
func (b *Broker) AgentLevelOf(c *registry.Component) (inForce, requested string) {
	return b.agentLevel(c)
}

// ObserveAgentLevels reads every tile's manifest against the store, now:
// first statements and lowerings are recorded as soon as xbind sees the
// tiles (boot, and each change of the workspace), not only when an agent
// next calls one.
func (b *Broker) ObserveAgentLevels() {
	for _, c := range b.Reg.Components() {
		if !c.IsTemplate() {
			b.agentLevel(c)
		}
	}
}

func (b *Broker) registerAgentLevels(srv *server.Server) {
	srv.RegisterAPI("GET /agent-levels", b.apiAgentLevelsGet)
	srv.RegisterAPI("PUT /agent-levels", b.apiAgentLevelsPut)
	srv.AgentLevel = b.AgentLevelOf
}

// agentLevelRow is one tile of GET /agent-levels.
type agentLevelRow struct {
	Tile      string `json:"tile"`
	Level     string `json:"level"`               // in force
	Requested string `json:"requested,omitempty"` // what its manifest asks for, when that is more: waits for approval
	Stated    string `json:"stated,omitempty"`    // what its manifest says ("" nothing)
	Builtin   bool   `json:"builtin,omitempty"`   // held by this release, whatever the manifest says
	By        string `json:"by,omitempty"`
	At        int64  `json:"at,omitempty"`
}

// personOwnSession reports a person acting in their own session, app or
// device (or the root token): not a tile's credential, a person's
// workspace agent or a view-as session.
func personOwnSession(p auth.Principal) bool {
	return p.Component == "" && p.Agent == "" && p.Via != "delegate" && p.Impersonator == "" && (p.UserID != "" || p.Owner)
}

func (b *Broker) agentLevelRows(p auth.Principal) []agentLevelRow {
	out := []agentLevelRow{}
	for _, c := range b.Reg.Components() {
		if c.IsTemplate() || c.Sealed || !b.mayManageTile(p, c.Path) { // (a sealed component has no agent level: no agent acts in it)
			continue
		}
		level, requested := b.agentLevel(c)
		_, builtin := builtinAgentLevels[c.Path]
		b.agentLv.mu.Lock()
		rec, known := b.levelsLocked().tiles[c.Path]
		b.agentLv.mu.Unlock()
		if !known && !builtin && requested == "" {
			continue // never stated: full, nothing to show
		}
		out = append(out, agentLevelRow{Tile: c.Path, Level: level, Requested: requested, Stated: c.Manifest.Agent, Builtin: builtin, By: rec.By, At: rec.At})
	}
	slices.SortFunc(out, func(a, b agentLevelRow) int { return strings.Compare(a.Tile, b.Tile) })
	return out
}

// apiAgentLevelsGet — GET /agent-levels: a person, in their own session →
// {tiles: [{tile, level, requested?, stated?, builtin?, by?, at?}]} for the
// tiles they may manage that have a level on record or a raise waiting.
func (b *Broker) apiAgentLevelsGet(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !personOwnSession(p) {
		server.WriteError(w, http.StatusForbidden, "what a person's agent may do in a tile is read and decided by the people who manage the tile, in their own session", "/docs/auth.md")
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"tiles": b.agentLevelRows(p)})
}

// apiAgentLevelsPut — PUT /agent-levels {tile, level}: approve the raise
// tile's manifest asks for. level is the level being approved and must be
// what the manifest asks for now (409 otherwise: it changed since the
// person looked). Only someone who may manage the tile (its owner, an
// admin of the owning org, a workspace admin), in their own session.
func (b *Broker) apiAgentLevelsPut(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var body struct {
		Tile  string `json:"tile"`
		Level string `json:"level"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.Tile == "" || body.Level == "" {
		server.WriteError(w, http.StatusBadRequest, "need {tile, level}", "/docs/auth.md")
		return
	}
	tile := strings.Trim(body.Tile, "/")
	if sealedRefused(w, sealedNone(tile, sealedNoAgent)) {
		return
	}
	c, ok := b.Reg.Component(tile)
	switch {
	case !personOwnSession(p):
		server.WriteError(w, http.StatusForbidden, "raising what a person's agent may do in a tile is approved by a person who manages the tile, in their own session — never by a tile, an agent or a view-as session", "/docs/auth.md")
		return
	case !ok || !b.mayManageTile(p, tile):
		server.WriteError(w, http.StatusForbidden, "a tile's agent level is approved by its owner, the owning org's admins, or a workspace admin", "/docs/auth.md")
		return
	}
	if _, builtin := builtinAgentLevels[tile]; builtin {
		server.WriteError(w, http.StatusConflict, tile+" is one of the workspace's consoles: its level is this xbind's, not its manifest's", "/docs/auth.md")
		return
	}
	level, requested := b.agentLevel(c)
	if requested == "" || requested != body.Level {
		server.WriteError(w, http.StatusConflict, fmt.Sprintf("%s's manifest doesn't ask for %q now (in force: %s; asked for: %s): look again", tile, body.Level, level, cmpOr(requested, "nothing more")), "/docs/auth.md")
		return
	}
	by := "owner"
	if p.UserID != "" {
		by = "user:" + p.UserID
	}
	b.agentLv.mu.Lock()
	lv := b.levelsLocked()
	lv.tiles[tile] = agentLevelRec{Level: requested, At: time.Now().Unix(), By: by}
	err := b.saveLevelsLocked(lv)
	b.agentLv.mu.Unlock()
	if err != nil {
		server.WriteError(w, http.StatusInternalServerError, "the approval couldn't be saved: "+err.Error(), "/docs/auth.md")
		return
	}
	if b.Hub != nil {
		b.Hub.Publish(events.Event{Type: "grants", Component: tile})
	}
	server.WriteJSON(w, http.StatusOK, agentLevelRow{Tile: tile, Level: requested, Stated: c.Manifest.Agent, By: by, At: time.Now().Unix()})
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// agentLevelAlerts are the raises that wait, for the people who may decide
// them.
func (b *Broker) agentLevelAlerts(p auth.Principal) []Alert {
	if !personOwnSession(p) && p.Via != "frame" {
		return nil
	}
	var out []Alert
	for _, c := range b.Reg.Components() {
		if c.IsTemplate() {
			continue
		}
		if _, builtin := builtinAgentLevels[c.Path]; builtin {
			continue
		}
		level, requested := b.agentLevel(c)
		if requested == "" || !b.mayManageTile(p, c.Path) {
			continue
		}
		out = append(out, Alert{Level: "warn", Kind: "agent-level", Tile: c.Path,
			Message: fmt.Sprintf("%s's manifest now asks that people's workspace agents may use it at %q; %q stays in force until someone who manages the tile approves the change (its manifest is the tile's own file: a tile agent can have written this)", c.Path, requested, level)})
	}
	return out
}
