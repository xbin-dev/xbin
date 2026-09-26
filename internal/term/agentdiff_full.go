package term

// FULL DIFFS of agent sessions (plans/native.md §13, §20 row 7): a
// files.changed event carries its patch capped (64 KiB per tool call,
// 192 KiB per turn — the event log is a ring in memory), which is enough
// for a card but not for a diff viewer. The snapshotter remembers which two
// trees each reported tool call and turn went between; GET
// /term/sessions/<id>/diff?toolCallId=|turn= diffs them again from the
// private git dir, uncapped up to fullPatchCap. Like every git run on tile
// data it is confined (D78): only the private dir is mounted, read-only —
// a diff of two trees reads objects and nothing else. The trees live as
// long as the session (the private dir is removed when it ends).

import (
	"bytes"
	"context"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
)

const (
	fullDiffTime  = 30 * time.Second
	maxDiffRanges = 1024 // tool calls + turns remembered per session, oldest dropped first
)

// fullPatchCap is a full patch's bytes; beyond it the response is cut at a
// line (X-Truncated). A var for the tests.
var fullPatchCap = 16 << 20

// ErrNoDiff: the session kept no snapshot for that tool call or turn (it
// changed nothing, the tile is not a git repo, diffs are off, or it was
// too long ago).
var ErrNoDiff = errors.New("no diff for that tool call or turn (nothing changed, or the session keeps no snapshots of it)")

// diffRange is what one tool call or turn went between.
type diffRange struct{ from, to string }

// remember records key's trees (the worker goroutine), dropping the oldest
// past maxDiffRanges. A reused key (tool ids may repeat across turns) is
// the latest.
func (s *snapper) remember(key, from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.ranges[key]; !seen {
		s.rorder = append(s.rorder, key)
	}
	s.ranges[key] = diffRange{from, to}
	for len(s.rorder) > maxDiffRanges {
		delete(s.ranges, s.rorder[0])
		s.rorder = s.rorder[1:]
	}
}

// fullDiff is key's complete git patch (file narrows it to one path, as git
// prints it), cut at a line past fullPatchCap (truncated).
func (s *snapper) fullDiff(ctx context.Context, key, file string) (patch []byte, truncated bool, err error) {
	if s == nil {
		return nil, false, ErrNoDiff
	}
	s.mu.Lock()
	r, ok := s.ranges[key]
	gone := s.closed || s.off
	s.mu.Unlock()
	if !ok || gone {
		return nil, false, ErrNoDiff
	}
	args := []string{"--git-dir=" + s.gitDir, "-c", "core.quotePath=false",
		"diff-tree", "-r", "-M", "-p", "--no-color", "--no-ext-diff", "--no-textconv", r.from, r.to}
	if file != "" {
		// literal: no pathspec magic (":(exclude)…", globs) from a query string
		args = append(args, "--", ":(literal)"+file)
	}
	out, err := confine.GitCmd(ctx, confine.Cmd{Dir: s.gitDir, ReadOnlyDir: true, Timeout: fullDiffTime, MaxOutput: fullPatchCap + 1}, args...)
	if err != nil {
		return nil, false, err
	}
	b := []byte(out)
	if len(b) > fullPatchCap {
		b, truncated = b[:fullPatchCap], true
		if i := bytes.LastIndexByte(b, '\n'); i > 0 {
			b = b[:i+1]
		}
	}
	return b, truncated, nil
}

// cleanDiffPath validates a ?path= for fullDiff: a relative, clean path in
// the tile (git prints them so), or "" for all.
func cleanDiffPath(p string) (string, bool) {
	if p == "" {
		return "", true
	}
	if strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// AgentDiff is the complete patch of what one tool call (toolCallID) or one
// turn (turn > 0) changed in the tile — the files.changed event's patch
// without its cap — optionally narrowed to one file. ErrNoDiff when the
// session kept no snapshot for it.
func (m *Manager) AgentDiff(ctx context.Context, id, toolCallID string, turn int64, file string) ([]byte, bool, error) {
	_, st, err := m.agentOf(id)
	if err != nil {
		return nil, false, err
	}
	file, ok := cleanDiffPath(file)
	if !ok {
		return nil, false, errors.New("path must be a clean path relative to the tile")
	}
	key := "tool:" + toolCallID
	if toolCallID == "" {
		key = "turn:" + strconv.FormatInt(turn, 10)
	}
	return st.snap.fullDiff(ctx, key, file)
}
