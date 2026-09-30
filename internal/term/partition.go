package term

// partition.go — terminal and agent sessions on partitioned tiles
// (plans/partitions/06 §1-§3; PD-09, PD-10, PD-22). A tile whose recorded
// mode has user partitions keeps each person's data apart, and a session
// opened on it belongs to its opener's partition:
//
//   - its terminal token needs no change: the broker's addressedPartition
//     puts a person's terminal in `user:<id>` (the owner token's in
//     `global`), so every API call from the shell reaches that partition;
//     XBIN_PARTITION says which (sessionEnv), and a session with no person
//     on a tile without a global instance opens without the tile API
//     (PD-10);
//   - it starts in $HOME, not in the tile directory, which is shared code
//     (S16);
//   - a person's persistent layer is their own, .xbin/term-part/<TileKey>/
//     <pkey>, never the tile's (PD-22), and their agent sessions' history
//     goes to data/agent-history/.partitions/<pkey>/<TileKey>/ (history.go);
//   - admins keep no pass into other people's sessions there: no reattach,
//     no drive, no names in listings — they may still end them (PD-09).
//
// The broker answers through hooks boot installs (SessionPartition,
// TilePartitioned, PersonPartitionKey); without them no tile is partitioned
// and every session is today's, byte for byte. A partition mode switch
// stops the tile's sessions and deletes the person layers and partition
// history (StopTileSessions, WipePartitionTile: the broker's wipe hook).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// ErrPartitionSwitching refuses a session while a partition mode switch of
// its tile runs (409): the switch is stopping the tile's sessions and
// deleting their layers.
var ErrPartitionSwitching = errors.New("a partition mode switch is running")

// Partition is the broker's answer about a new session's tile
// (SessionPartitionFunc).
type Partition struct {
	// Partitioned: the tile keeps each person's data apart (its recorded
	// mode has user partitions, or its record can't be read). false: every
	// other field is empty and the session is today's.
	Partitioned bool
	// Tile is the partitioned tile holding the session's cwd: the key of
	// the person layer and the partition history.
	Tile string
	// Part is what the session's tile API reaches: "user:<id>" for a
	// person, "global" for a session with no person (or one targeting a
	// non-primary deployment) when there is a global instance; "" when
	// NoAPI.
	Part string
	// Key is the opener's partition id (util.PartitionKey) — their layer's
	// and history's storage key; "" for a session with no person.
	Key string
	// NoAPI says why a session with no person opens without the tile API:
	// the tile has no global instance (PD-10). "" otherwise.
	NoAPI string
}

// SessionPartitionFunc answers Partition for a session p opens on tile
// targeting deployment dep ("" follows the primary). An error refuses the
// session: ErrPartitionSwitching (409), or a person who reaches no
// partition of the tile (403).
type SessionPartitionFunc func(p auth.Principal, tile, dep string) (Partition, error)

// partLayerDir is where people's layers live under .xbin (PD-22).
const partLayerDir = "term-part"

// partHistoryDir is where partition agent history lives under
// data/agent-history. No user id starts with a dot, so no person's own
// history directory is ever called this.
const partHistoryDir = ".partitions"

// sessionPart is what a session keeps of its partition, fixed at start.
// The zero value is a session on a tile that isn't partitioned: today's.
type sessionPart struct {
	on     bool   // the tile was partitioned when the session opened
	tile   string // that tile
	part   string // XBIN_PARTITION and the session frame's partition ("" none)
	key    string // the opener's partition id: their layer and history
	note   string // the session frame's partitionNote
	apiOff bool   // the session lost its tile API to PD-10: the frame echoes api:false
}

// person reports a session acting in a person's partition.
func (sp sessionPart) person() bool { return strings.HasPrefix(sp.part, "user:") }

// pickPartition asks the broker about a new session's tile (both kinds,
// after pickTarget) and keeps the answer in o. The int is the HTTP status
// of a refusal.
func (m *Manager) pickPartition(p auth.Principal, o *openOpts, rel string) (int, error) {
	if m.SessionPartition == nil {
		return 0, nil
	}
	ans, err := m.SessionPartition(p, rel, o.target.Deployment)
	switch {
	case errors.Is(err, ErrPartitionSwitching):
		return 409, err
	case err != nil:
		return 403, err
	case !ans.Partitioned:
		return 0, nil
	}
	sp := sessionPart{on: true, tile: ans.Tile, part: ans.Part, key: ans.Key}
	if sp.tile == "" {
		sp.tile = rel
	}
	switch {
	case ans.NoAPI != "" && o.api:
		o.api, sp.apiOff = false, true
		sp.note = "tile API off: " + ans.NoAPI
	case ans.NoAPI != "":
	case sp.person():
		sp.note = "partition: yours (" + sp.part + ") · the tile directory is shared code — keep your own files in $HOME"
	case sp.part != "":
		sp.note = "partition: " + sp.part + " · the tile directory is shared code"
	}
	o.part = sp
	return 0, nil
}

// partitionEnv is the session's XBIN_PARTITION: only on a partitioned tile,
// for a session that has a partition; nil otherwise, so every other
// session's env is today's.
func (o openOpts) partitionEnv() []string {
	if o.part.part == "" {
		return nil
	}
	return []string{"XBIN_PARTITION=" + o.part.part}
}

// startDir is where a new session starts: $HOME on a partitioned tile (the
// tile directory is shared code, S16), else the tile directory.
func (o openOpts) startDir(tileDir, homeDir string) string {
	if o.part.on {
		return homeDir
	}
	return tileDir
}

// layerKey is the persistent layer a new session asks for: its person's
// own on a partitioned tile (PD-22), else the tile's.
func (o openOpts) layerKey(rel string) string {
	if o.part.key != "" {
		return partLayerKey(o.part.tile, o.part.key)
	}
	return termKey(rel)
}

// partLayerKey is a person's layer key on tile: a path under .xbin. A
// tile's own key (termKey) never holds a '/'.
func partLayerKey(tile, pkey string) string {
	return partLayerDir + "/" + util.TileKey(tile) + "/" + pkey
}

// layerDir is where layer key lives.
func (m *Manager) layerDir(key string) string {
	if strings.HasPrefix(key, partLayerDir+"/") {
		return filepath.Join(m.Root, ".xbin", filepath.FromSlash(key))
	}
	return filepath.Join(m.Root, ".xbin", "term", key)
}

// partitioned reports whether s's tile keeps each person's data apart —
// when s opened, or now (a tile an empty manifest partitioned since): the
// admin gates (PD-09) fail closed on either.
func (m *Manager) partitioned(s *Session) bool {
	if s.part.on {
		return true
	}
	if m.TilePartitioned == nil {
		return false
	}
	_, on := m.TilePartitioned(s.Cwd)
	return on
}

// adminPass reports whether admin p may act on s, another person's
// session, as an admin: never on a partitioned tile (PD-09).
func (m *Manager) adminPass(s *Session, p auth.Principal) bool {
	return p.IsAdmin() && (s.homeKey == HomeKey(p) || !m.partitioned(s))
}

// notYours is the refusal of an admin on another person's session of a
// partitioned tile.
func notYours(tile string) string {
	return "session belongs to another user: " + tile + " keeps each person's data apart, so an admin can't open other people's sessions there (ending one is allowed)"
}

// MayKill is the gate on ending a session through the API (DELETE
// /term/sessions/<id>): the creator (the drive rule), or an admin — on a
// partitioned tile too (PD-09: governance keeps kill). ErrNoSession for an
// unknown id.
func (m *Manager) MayKill(id string, p auth.Principal) error {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return ErrNoSession
	}
	if p.IsAdmin() {
		return nil
	}
	return m.MayDrive(id, p)
}

// MayRename is the rename gate (PATCH /term/sessions/<id>): the creator, or
// an admin where the admin pass holds (never another person's session on a
// partitioned tile). Unknown ids pass, so the handler answers 404.
func (m *Manager) MayRename(id string, p auth.Principal) bool {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	return s == nil || s.homeKey == HomeKey(p) || m.adminPass(s, p)
}

// PartitionModeChanged ends the sessions on tile that opened under the
// other mode (a tile whose recorded mode gained or lost user partitions
// without a switch — an empty tile's manifest decides at once — or right
// around one): their layer, working directory and tile API were chosen for
// the mode they opened in. Returns how many were ended. Boot calls it from
// the registry's partition-change hook; it doesn't wait.
func (m *Manager) PartitionModeChanged(tile string, partitioned bool) int {
	n := 0
	for _, s := range m.sorted() {
		if (s.Cwd == tile || s.part.tile == tile) && s.part.on != partitioned {
			s.kill()
			n++
		}
	}
	return n
}

// stopWait bounds how long StopTileSessions waits for sessions to end.
var stopWait = 15 * time.Second

// StopTileSessions ends every session on tile — a partition mode switch's
// stop (plans/partitions/01 §2.5) — and waits until each has torn down: an
// agent session's history saved, every layer released, the row gone. So
// nothing the switch deletes next is written again. An error: a session
// didn't end in time (the switch then fails before deleting anything of
// the terminals').
func (m *Manager) StopTileSessions(tile string) error {
	return m.stopWhere(func(s *Session) bool { return s.Cwd == tile || s.part.tile == tile }, tile)
}

// stopWhere ends every session on matches and waits for their teardown;
// what names them in the error.
func (m *Manager) stopWhere(on func(*Session) bool, what string) error {
	deadline := time.Now().Add(stopWait)
	killed := map[*Session]bool{}
	for {
		var live []*Session
		for _, s := range m.sorted() {
			if on(s) {
				live = append(live, s)
			}
		}
		if len(live) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d terminal or agent session(s) of %s did not end in time", len(live), what)
		}
		for _, s := range live {
			if !killed[s] {
				killed[s] = true
				s.kill()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// StopPartitionSessions ends the sessions acting in partition id pkey — on
// tile, or on every tile when tile is "" — and waits for their teardown,
// as StopTileSessions does: the stop before WipePartitionKey (a partition's
// reset or purge, a deleted person's sweep).
func (m *Manager) StopPartitionSessions(tile, pkey string) error {
	return m.stopWhere(func(s *Session) bool {
		return pkey != "" && s.part.key == pkey && (tile == "" || s.part.tile == tile)
	}, pkey)
}

// WipePartitionKey deletes partition id pkey's person layer and partition
// agent history — of tile, or of every tile when tile is "" — as
// WipePartitionTile does for a whole tile; dry only counts. For a
// partition's reset or purge and a deleted person's sweep (F7b's people
// and partitions hooks); the caller has stopped its sessions
// (StopPartitionSessions).
func (m *Manager) WipePartitionKey(tile, pkey string, dry bool) (PartitionTileWipe, error) {
	var out PartitionTileWipe
	if pkey == "" || strings.ContainsAny(pkey, `/\.`) {
		return out, fmt.Errorf("%q is no partition id", pkey)
	}
	var layers, hists []string
	if tile != "" {
		tk := util.TileKey(tile)
		layers = []string{filepath.Join(m.Root, ".xbin", partLayerDir, tk, pkey)}
		hists = []string{filepath.Join(m.Root, "data", "agent-history", partHistoryDir, pkey, tk)}
	} else {
		tiles, _ := os.ReadDir(filepath.Join(m.Root, ".xbin", partLayerDir)) // walk-ok: .xbin is xbind's own
		for _, e := range tiles {
			if e.IsDir() {
				layers = append(layers, filepath.Join(m.Root, ".xbin", partLayerDir, e.Name(), pkey))
			}
		}
		hists = []string{filepath.Join(m.Root, "data", "agent-history", partHistoryDir, pkey)}
	}
	for _, d := range layers {
		if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
			continue
		}
		out.Layers++
		if !dry {
			if err := m.removeLayer(d); err != nil {
				return out, fmt.Errorf("a person's terminal layer: %w", err)
			}
		}
	}
	for _, d := range hists {
		if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
			continue
		}
		out.Histories++
		if !dry {
			if err := os.RemoveAll(d); err != nil {
				return out, fmt.Errorf("a person's agent history: %w", err)
			}
		}
	}
	if out.Layers+out.Histories > 0 {
		out.Keys = []string{pkey}
	}
	return out, nil
}

// PartitionTileWipe is what WipePartitionTile removed (or, dry, would):
// the person layers and the partition histories of a tile, and whose
// (partition ids).
type PartitionTileWipe struct {
	Layers, Histories int
	Keys              []string // the partition ids that had either, sorted
}

// WipePartitionTile deletes every person's layer of tile
// (.xbin/term-part/<TileKey>/) and every person's partition agent-session
// history of it (data/agent-history/.partitions/*/<TileKey>/) — a switch's
// wipe (plans/partitions/01 §2.6, PD-22); dry only counts. The layers are
// trees a sandbox wrote, removed confined (removeLayer, D78); the history
// is xbind's own. The tile's own layer and people's own history stay. The
// caller has stopped the tile's sessions (StopTileSessions).
func (m *Manager) WipePartitionTile(tile string, dry bool) (PartitionTileWipe, error) {
	var out PartitionTileWipe
	keys := map[string]bool{}
	tk := util.TileKey(tile)
	layers := filepath.Join(m.Root, ".xbin", partLayerDir, tk)
	ents, err := os.ReadDir(layers) // walk-ok: .xbin is xbind's own, masked from every sandbox
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		out.Layers++
		keys[e.Name()] = true
		if !dry {
			if err := m.removeLayer(filepath.Join(layers, e.Name())); err != nil {
				return out, fmt.Errorf("a person's terminal layer of %s: %w", tile, err)
			}
		}
	}
	if !dry && len(ents) > 0 {
		if err := os.Remove(layers); err != nil && !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
	}
	parts := filepath.Join(m.Root, "data", "agent-history", partHistoryDir)
	people, err := os.ReadDir(parts) // walk-ok: data/agent-history is xbind's own
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	for _, e := range people {
		dir := filepath.Join(parts, e.Name(), tk)
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
			continue
		}
		out.Histories++
		keys[e.Name()] = true
		if !dry {
			if err := os.RemoveAll(dir); err != nil {
				return out, fmt.Errorf("a person's agent history of %s: %w", tile, err)
			}
		}
	}
	for k := range keys {
		out.Keys = append(out.Keys, k)
	}
	sort.Strings(out.Keys)
	return out, nil
}
