package term

// Snapshot diffs for agent sessions (D77): what a tool call and a turn
// changed in the tile, from git trees. An adapter's own diff content covers
// only its edit tools; a shell write — `sed -i`, a python heredoc, a code
// generator — carries none, and a model asked to describe one guesses. So,
// like Cline, opencode and Codex, the daemon snapshots the work tree
// (`add -A` + `write-tree`) when a turn starts and whenever a tool call
// finishes, and diffs consecutive trees.
//
// The tile's own repository is never used: the sandboxed agent can write its
// .git/config, and a git reading it runs whatever core.fsmonitor or filter it
// names. Everything lives in a private git dir (index, objects, config) with
// no system or global config; the tile is only the work tree (its .gitignore
// files still apply — data, not config). The user's repo, index and HEAD are
// untouched. And like every tool xbind runs on tile data, git runs confined
// (D78, internal/confine): the private dir read-write, the tile read-only.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// EvFilesChanged is the snapshot diff event: {toolCallId? | turn?, changes,
// patch} (docs/protocol.md §Agent session events).
const EvFilesChanged = "files.changed"

const (
	diffTimeout  = 8 * time.Second // a snapshot slower than this turns diffs off for the session
	toolPatchCap = 64 << 10        // a tool's patch text; the log ring is 8 MiB
	turnPatchCap = 192 << 10
	maxChanges   = 200
)

// defaultExcludes are skipped on top of the tile's .gitignore: dependency
// and cache trees nobody wants hashed on every tool call.
const defaultExcludes = "node_modules/\n.venv/\nvenv/\n__pycache__/\n*.pyc\n.next/\n.cache/\n.pnpm-store/\ntarget/\n.gradle/\n.DS_Store\n"

// FileChange is one path in a files.changed event.
type FileChange struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"` // a rename's source
	Status  string `json:"status"`            // added | modified | deleted | renamed | typechange
	Add     int    `json:"add"`
	Del     int    `json:"del"`
	Binary  bool   `json:"binary,omitempty"`
}

type diffJob struct {
	what string // "base" | "tool" | "turn" | "sync"
	id   string // tool call id
	emit bool   // tool: report it (an edit tool reports its own diff)
	turn int64
	done chan struct{} // sync: closed when the jobs before it ran
}

// snapper is one session's snapshotter: an ordered worker, so snapshots
// happen in event order off the pump.
type snapper struct {
	work    string // the tile (the work tree)
	gitDir  string // private
	emit    func(agent.Event)
	jobs    chan diffJob
	mu      sync.Mutex
	closed  bool
	kinds   map[string]string // tool id → kind (pump goroutine only)
	settled map[string]bool   // tool ids already snapshotted
	prev    string            // the last tree
	base    string            // the turn's starting tree
}

// newSnapper starts a snapshotter for a tile that is a git repo (each tile
// is its own, plans/lifecycle.md); nil when it isn't or git is unusable.
func newSnapper(work string, emit func(agent.Event)) *snapper {
	if _, err := os.Stat(filepath.Join(work, ".git")); err != nil {
		return nil
	}
	gd, err := os.MkdirTemp("", "xbin-agentdiff-")
	if err != nil {
		return nil
	}
	s := &snapper{work: work, gitDir: gd, emit: emit, jobs: make(chan diffJob, 128), kinds: map[string]string{}, settled: map[string]bool{}}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	if _, err := s.git(ctx, "init", "-q", "--bare", gd); err != nil || os.WriteFile(filepath.Join(gd, "xbin-excludes"), []byte(defaultExcludes), 0o600) != nil {
		os.RemoveAll(gd)
		return nil
	}
	go s.run()
	s.enqueue(diffJob{what: "base"})
	return s
}

// git runs git on the private dir with the tile as work tree, confined and
// hardened (confine.Git: no system/global config, no fsmonitor, no hooks);
// no external diff or textconv either — the private config defines none.
func (s *snapper) git(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"--git-dir=" + s.gitDir, "--work-tree=" + s.work,
		"-c", "core.excludesFile=" + filepath.Join(s.gitDir, "xbin-excludes"), "-c", "core.quotePath=false",
		"-c", "core.autocrlf=false", "-c", "gc.auto=0"}, args...)
	if len(args) > 0 && args[0] == "init" {
		full = args
	}
	out, err := confine.GitCmd(ctx, confine.Cmd{Dir: s.gitDir, Binds: []sandbox.Bind{confine.RO(s.work)}, Timeout: diffTimeout}, full...)
	return []byte(out), err
}

func (s *snapper) enqueue(j diffJob) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.jobs <- j:
	default: // backed up: this job's changes fold into the next one's diff
	}
}

// turnStart snapshots the base of a turn (the user may have edited between
// turns — that is not the agent's) before the prompt is sent, waiting for it
// up to wait (a snapshot of a small tile takes milliseconds; a slow one must
// not hold the prompt).
func (s *snapper) turnStart(wait time.Duration) {
	if s == nil {
		return
	}
	s.enqueue(diffJob{what: "base"})
	s.syncFor(wait)
}

// observe watches the logged events (the pump goroutine): a tool call's
// terminal status snapshots, a turn's end snapshots and summarises.
func (s *snapper) observe(e agent.Event) {
	if s == nil {
		return
	}
	switch e.Type {
	case agent.EvToolCall, agent.EvToolUpdate:
		var d struct{ ID, Kind, Status string }
		if json.Unmarshal(e.Data, &d) != nil || d.ID == "" {
			return
		}
		if d.Kind != "" {
			s.kinds[d.ID] = d.Kind
		}
		switch d.Status {
		case "completed", "failed", "cancelled":
			if s.settled[d.ID] {
				return
			}
			s.settled[d.ID] = true
			k := s.kinds[d.ID]
			// edit/delete/move calls carry their own diff; a read or a search
			// changes nothing worth a card (still snapshotted: the tree advances)
			s.enqueue(diffJob{what: "tool", id: d.ID, emit: k != "edit" && k != "delete" && k != "move" && k != "read" && k != "search"})
		}
	case agent.EvTurnEnd:
		var d struct{ Turn int64 }
		_ = json.Unmarshal(e.Data, &d)
		s.kinds, s.settled = map[string]string{}, map[string]bool{} // ids may be reused next turn
		s.enqueue(diffJob{what: "turn", turn: d.Turn})
	}
}

// syncFor waits until the queued jobs ran, at most d.
func (s *snapper) syncFor(d time.Duration) {
	done := make(chan struct{})
	s.enqueue(diffJob{what: "sync", done: done})
	select {
	case <-done:
	case <-time.After(d):
	}
}

// close stops the worker (nothing is emitted after) and removes the private dir.
func (s *snapper) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.jobs)
	}
	s.mu.Unlock()
}

func (s *snapper) run() {
	defer os.RemoveAll(s.gitDir)
	off := false
	for j := range s.jobs {
		if j.what == "sync" {
			close(j.done)
			continue
		}
		if off {
			continue
		}
		tree, err := s.snapshot()
		if err != nil {
			slog.Warn("agent diffs off for this session: snapshot failed", "tile", s.work, "err", err)
			off = true
			continue
		}
		from := s.prev
		s.prev = tree
		switch j.what {
		case "base":
			s.base = tree
		case "tool":
			if j.emit && from != "" && from != tree {
				s.report(map[string]any{"toolCallId": j.id}, from, tree, toolPatchCap)
			}
		case "turn":
			if s.base != "" && s.base != tree {
				s.report(map[string]any{"turn": j.turn}, s.base, tree, turnPatchCap)
			}
			s.base = tree
		}
	}
}

func (s *snapper) snapshot() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	if _, err := s.git(ctx, "add", "-A", "--ignore-errors", "."); err != nil {
		// --ignore-errors: an unreadable file is skipped, but git still exits 1
		if ctx.Err() != nil {
			return "", err
		}
	}
	out, err := s.git(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// report emits files.changed for from→to: the per-file list (status and
// line counts, uncapped up to maxChanges) and the patch (capped).
func (s *snapper) report(d map[string]any, from, to string, capBytes int) {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	changes, err := s.changes(ctx, from, to)
	if err != nil || len(changes) == 0 {
		return
	}
	patch, _ := s.git(ctx, "diff-tree", "-r", "-M", "-p", "--no-color", "--no-ext-diff", "--no-textconv", from, to)
	truncated := false
	if len(patch) > capBytes {
		patch, truncated = patch[:capBytes], true
		if i := bytes.LastIndexByte(patch, '\n'); i > 0 {
			patch = patch[:i+1]
		}
	}
	d["changes"] = changes
	d["patch"] = map[string]any{"format": "git_patch", "text": string(patch), "truncated": truncated}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if !closed {
		s.emit(agent.New(EvFilesChanged, d))
	}
}

func (s *snapper) changes(ctx context.Context, from, to string) ([]FileChange, error) {
	ns, err := s.git(ctx, "diff-tree", "-r", "-M", "-z", "--name-status", from, to)
	if err != nil {
		return nil, err
	}
	var out []FileChange
	idx := map[string]int{}
	f := strings.Split(strings.TrimSuffix(string(ns), "\x00"), "\x00")
	for i := 0; i < len(f) && len(out) < maxChanges; i++ {
		st := f[i]
		if st == "" || i+1 >= len(f) {
			continue
		}
		c := FileChange{Path: f[i+1]}
		i++
		switch st[0] {
		case 'A':
			c.Status = "added"
		case 'D':
			c.Status = "deleted"
		case 'T':
			c.Status = "typechange"
		case 'R', 'C':
			if i+1 >= len(f) {
				continue
			}
			c.OldPath, c.Path, c.Status = c.Path, f[i+1], "renamed"
			i++
		default:
			c.Status = "modified"
		}
		idx[c.Path] = len(out)
		out = append(out, c)
	}
	num, err := s.git(ctx, "diff-tree", "-r", "-M", "-z", "--numstat", from, to)
	if err != nil {
		return out, nil
	}
	// "add\tdel\tpath\0", or "add\tdel\t\0old\0new\0" for a rename; "-" = binary
	f = strings.Split(string(num), "\x00")
	for i := 0; i < len(f); i++ {
		parts := strings.SplitN(f[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(f) {
			path = f[i+2]
			i += 2
		}
		j, ok := idx[path]
		if !ok {
			continue
		}
		if parts[0] == "-" {
			out[j].Binary = true
			continue
		}
		out[j].Add, _ = strconv.Atoi(parts[0])
		out[j].Del, _ = strconv.Atoi(parts[1])
	}
	return out, nil
}
