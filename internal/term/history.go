package term

// AGENT SESSION HISTORY: an agent session's transcript outlives it. When the
// session ends (or the daemon stops), its event log and a little metadata are
// written to data/agent-history/<user>/<tile>/<id>.json — per user and per
// tile, like the prefs store — so a finished conversation can be read back
// (GET /agent/history/{id}/events renders it exactly like a live one) and,
// where the agent advertised loadSession, reopened (POST /term/sessions with
// resume:<id> → session/load). Retention keeps the newest historyKeep per
// tile; a resumed session supersedes the entry it reopened. A session that
// never took a prompt (an eager-created tab closed unused) is not kept.
// History stays per tile, whichever deployment a session targeted: it is a
// conversation about the tile's one work tree. Each entry names its
// session's target deployment, when it had one, in `deployment`.
//
// On a partitioned tile a person's session is their partition's, and so is
// its history (PD-22): data/agent-history/.partitions/<pkey>/<TileKey>/,
// keyed by the person's partition id — a person deleted and recreated
// under the same id never reads the old one's — and deleted with the
// partition (a mode switch: WipePartitionTile). The routes below merge it
// with the person's own (partitionHistoryRoot).

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

const historyKeep = 20 // past sessions kept per (user × tile)

// HistoryMeta is one past session (GET /api/xbin/agent/history).
type HistoryMeta struct {
	ID           string `json:"id"`
	Cwd          string `json:"cwd"`
	Provider     string `json:"provider"`
	Mode         string `json:"mode,omitempty"`
	Name         string `json:"name,omitempty"`
	Created      string `json:"created"`
	Ended        string `json:"ended"`
	Turns        uint64 `json:"turns"`
	Preview      string `json:"preview,omitempty"`      // the first prompt, one line
	ACPSessionID string `json:"acpSessionId,omitempty"` // the agent's own id for it
	Loadable     bool   `json:"loadable"`               // the agent can reopen it (resume)
	Deployment   string `json:"deployment,omitempty"`   // the session's named target (D127p); absent: it followed the primary
}

type historyFile struct {
	Meta   HistoryMeta   `json:"meta"`
	Events []agent.Event `json:"events"`
}

func (m *Manager) historyRoot(homeKey string) string {
	return filepath.Join(m.Root, "data", "agent-history", homeKey)
}

func (m *Manager) historyDir(homeKey, cwd string) string {
	return filepath.Join(m.historyRoot(homeKey), util.CompKey(cwd))
}

// partitionHistoryRoot is homeKey's partition history root: their
// partition id's, "" while they have none (or no hook is installed).
// homeKey is the person's id (validated ids are their own home keys).
func (m *Manager) partitionHistoryRoot(homeKey string) string {
	if m.PersonPartitionKey == nil || homeKey == ownerHomeKey {
		return ""
	}
	if key := m.PersonPartitionKey(homeKey); key != "" {
		return filepath.Join(m.Root, "data", "agent-history", partHistoryDir, key)
	}
	return ""
}

// sessionHistoryDir is where s's transcript goes: its partition's on a
// partitioned tile, else its person's own per tile.
func (m *Manager) sessionHistoryDir(s *Session) string {
	if s.part.key != "" {
		return filepath.Join(m.Root, "data", "agent-history", partHistoryDir, s.part.key, util.TileKey(s.part.tile))
	}
	return m.historyDir(s.homeKey, s.Cwd)
}

// saveHistory persists a session's transcript and meta. Called when the
// session ends (agentPump) and on shutdown (FlushAgents).
func (m *Manager) saveHistory(s *Session) {
	st := s.agent
	if st == nil {
		return
	}
	evs, _ := st.log.Since(0)
	st.mu.Lock()
	turns, prov, mode, acpID, loadable, resumed := st.turn, st.provider.ID, st.mode, st.acpID, st.loadable, st.resumed
	st.mu.Unlock()
	if turns == 0 {
		return // never prompted: nothing worth keeping
	}
	s.mu.Lock()
	name, born := s.name, s.born
	s.mu.Unlock()
	f := historyFile{Meta: HistoryMeta{
		ID: s.ID, Cwd: s.Cwd, Provider: prov, Mode: mode, Name: name,
		Created: born.UTC().Format(time.RFC3339), Ended: time.Now().UTC().Format(time.RFC3339),
		Turns: turns, Preview: firstPrompt(evs), ACPSessionID: acpID, Loadable: loadable && acpID != "",
		Deployment: s.target.Deployment,
	}, Events: evs}
	dir := m.sessionHistoryDir(s)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("agent history: mkdir", "id", s.ID, "err", err)
		return
	}
	bts, _ := json.Marshal(f)
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, s.ID+".json"), bts, 0o644); err != nil {
		slog.Warn("agent history: save", "id", s.ID, "err", err)
		return
	}
	if resumed != "" && resumed != s.ID { // this session continued that one: one entry, not two
		_ = m.DeleteHistory(s.homeKey, resumed)
	}
	m.pruneHistory(dir)
}

// firstPrompt is the transcript's first user line, for the listing.
func firstPrompt(evs []agent.Event) string {
	for _, e := range evs {
		if e.Type != agent.EvMessageDelta {
			continue
		}
		var d struct {
			Role, Text  string
			Attachments []agent.AttachmentInfo
		}
		if json.Unmarshal(e.Data, &d) != nil || d.Role != "user" {
			continue
		}
		line := strings.TrimSpace(d.Text)
		if line == "" && len(d.Attachments) > 0 { // a prompt of files only: name them
			names := make([]string, len(d.Attachments))
			for i, a := range d.Attachments {
				names[i] = a.Name
			}
			line = "[" + strings.Join(names, ", ") + "]"
		}
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if len(line) > 120 {
			line = line[:120] + "…"
		}
		return line
	}
	return ""
}

// readHistoryMeta reads a past session's meta — the file's head: saveHistory
// writes {"meta":…,"events":[…]} in that order, so listing a tile's twenty
// sessions reads twenty metas, not twenty transcripts of up to 8 MiB each
// (D130). A file with its events first still reads, the slow way.
func readHistoryMeta(path string) (HistoryMeta, bool) {
	f, err := os.Open(path)
	if err != nil {
		return HistoryMeta{}, false
	}
	defer f.Close()
	return historyMetaFrom(f)
}

func historyMetaFrom(r io.Reader) (HistoryMeta, bool) {
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return HistoryMeta{}, false
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return HistoryMeta{}, false
		}
		if k != "meta" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return HistoryMeta{}, false
			}
			continue
		}
		var meta HistoryMeta
		if dec.Decode(&meta) != nil || meta.ID == "" {
			return HistoryMeta{}, false
		}
		return meta, true
	}
	return HistoryMeta{}, false
}

// ListHistory is a user's past sessions, newest first — all tiles, or one
// when cwd != "" — minus the tiles they may no longer open a terminal on
// (`may` as in ListFor; nil = no filter). Never nil.
func (m *Manager) ListHistory(homeKey, cwd string, may func(rel string) bool) []HistoryMeta {
	out := []HistoryMeta{}
	var dirs []string
	part := m.partitionHistoryRoot(homeKey)
	if cwd != "" {
		dirs = []string{m.historyDir(homeKey, cwd)}
		if part != "" {
			tile := cwd
			if m.TilePartitioned != nil {
				if t, on := m.TilePartitioned(cwd); on {
					tile = t
				}
			}
			dirs = append(dirs, filepath.Join(part, util.TileKey(tile)))
		}
	} else {
		for _, root := range []string{m.historyRoot(homeKey), part} {
			if root == "" {
				continue
			}
			ents, _ := os.ReadDir(root)
			for _, e := range ents {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(root, e.Name()))
				}
			}
		}
	}
	for _, dir := range dirs {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			meta, ok := readHistoryMeta(filepath.Join(dir, e.Name()))
			if !ok || (may != nil && !may(meta.Cwd)) {
				continue
			}
			out = append(out, meta)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ended > out[j].Ended })
	return out
}

// historyPath finds a past session's file by id across the user's tiles:
// their own history, then their partition history (partitionHistoryRoot).
func (m *Manager) historyPath(homeKey, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\.") { // ids are hex tokens
		return "", ErrNoSession
	}
	for _, root := range []string{m.historyRoot(homeKey), m.partitionHistoryRoot(homeKey)} {
		if root == "" {
			continue
		}
		if matches, _ := filepath.Glob(filepath.Join(root, "*", id+".json")); len(matches) > 0 {
			return matches[0], nil
		}
	}
	return "", ErrNoSession
}

// HistoryMeta is one past session's meta alone (the file's head).
func (m *Manager) HistoryMeta(homeKey, id string) (HistoryMeta, error) {
	path, err := m.historyPath(homeKey, id)
	if err != nil {
		return HistoryMeta{}, err
	}
	meta, ok := readHistoryMeta(path)
	if !ok {
		return HistoryMeta{}, ErrNoSession
	}
	return meta, nil
}

// ReadHistory is one past session: its meta and full transcript.
func (m *Manager) ReadHistory(homeKey, id string) (HistoryMeta, []agent.Event, error) {
	path, err := m.historyPath(homeKey, id)
	if err != nil {
		return HistoryMeta{}, nil, err
	}
	bts, err := os.ReadFile(path)
	if err != nil {
		return HistoryMeta{}, nil, ErrNoSession
	}
	var f historyFile
	if err := json.Unmarshal(bts, &f); err != nil {
		return HistoryMeta{}, nil, err
	}
	if f.Events == nil {
		f.Events = []agent.Event{}
	}
	return f.Meta, f.Events, nil
}

// DeleteHistory removes a past session.
func (m *Manager) DeleteHistory(homeKey, id string) error {
	path, err := m.historyPath(homeKey, id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// pruneHistory keeps the newest historyKeep entries in a tile's dir.
func (m *Manager) pruneHistory(dir string) {
	ents, _ := os.ReadDir(dir)
	type ent struct{ path, ended string }
	var all []ent
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if meta, ok := readHistoryMeta(p); ok {
			all = append(all, ent{p, meta.Ended})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ended > all[j].ended })
	for _, e := range all[min(len(all), historyKeep):] {
		_ = os.Remove(e.path)
	}
}

// FlushAgents persists every live agent session — on shutdown, so an
// update/restart turns open conversations into history, not losses — and
// drops what each keeps outside its sandbox: the private snapshot dir and,
// with isolation off, the attachments dir (the agent hosts go with xbind,
// and the sessions' own teardown will not run).
func (m *Manager) FlushAgents() {
	for _, s := range m.sorted() {
		if s.agent != nil {
			m.saveHistory(s)
			s.agent.snap.discard(2 * time.Second)
			if s.agent.attachDir != "" {
				_ = os.RemoveAll(s.agent.attachDir)
			}
		}
	}
}
