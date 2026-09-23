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

import (
	"encoding/json"
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
	}, Events: evs}
	dir := m.historyDir(s.homeKey, s.Cwd)
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
		var d struct{ Role, Text string }
		if json.Unmarshal(e.Data, &d) != nil || d.Role != "user" {
			continue
		}
		line := strings.TrimSpace(d.Text)
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

func readHistoryMeta(path string) (HistoryMeta, bool) {
	bts, err := os.ReadFile(path)
	if err != nil {
		return HistoryMeta{}, false
	}
	var f struct {
		Meta HistoryMeta `json:"meta"`
	}
	if json.Unmarshal(bts, &f) != nil || f.Meta.ID == "" {
		return HistoryMeta{}, false
	}
	return f.Meta, true
}

// ListHistory is a user's past sessions, newest first — all tiles, or one
// when cwd != "" — minus the tiles they may no longer open a terminal on
// (`may` as in ListFor; nil = no filter). Never nil.
func (m *Manager) ListHistory(homeKey, cwd string, may func(rel string) bool) []HistoryMeta {
	out := []HistoryMeta{}
	var dirs []string
	if cwd != "" {
		dirs = []string{m.historyDir(homeKey, cwd)}
	} else {
		ents, _ := os.ReadDir(m.historyRoot(homeKey))
		for _, e := range ents {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(m.historyRoot(homeKey), e.Name()))
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

// historyPath finds a past session's file by id across the user's tiles.
func (m *Manager) historyPath(homeKey, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\.") { // ids are hex tokens
		return "", ErrNoSession
	}
	matches, _ := filepath.Glob(filepath.Join(m.historyRoot(homeKey), "*", id+".json"))
	if len(matches) == 0 {
		return "", ErrNoSession
	}
	return matches[0], nil
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
// update/restart turns open conversations into history, not losses.
func (m *Manager) FlushAgents() {
	for _, s := range m.sorted() {
		if s.agent != nil {
			m.saveHistory(s)
		}
	}
}
