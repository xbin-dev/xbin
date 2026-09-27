package users

import (
	"fmt"
	"sort"
	"strings"
)

// Admin-approved workspace chrome (D118). A tile whose documents run
// unsandboxed keeps the ambient session cookie: its frontend acts as whoever
// opens it, admins included (plans/auth.md §6). A tile's own xbin.json is
// writable from its terminals and coding agents (D40), so its `chrome: true`
// is only a request: beyond the implicit chrome (root, shell) and the
// shipped tiles/organisations, xbind honours it for a path listed here —
// set by a workspace admin, kept in users.json with the rest of the
// workspace policy (xbind-owned, masked out of every terminal).

// ChromeApproved reports whether a workspace admin approved path as
// trusted chrome.
func (s *Store) ChromeApproved(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chrome[path]
}

// ChromeTiles lists the approved paths, sorted.
func (s *Store) ChromeTiles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chromeTilesLocked()
}

// SetChromeApproved approves path as trusted chrome (on) or withdraws the
// approval. Workspace admins only — the caller checks.
func (s *Store) SetChromeApproved(path string, on bool) error {
	path = strings.Trim(path, "/")
	if path == "" {
		return fmt.Errorf("empty tile path")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chrome[path] == on {
		return nil
	}
	if on {
		if s.chrome == nil {
			s.chrome = map[string]bool{}
		}
		s.chrome[path] = true
	} else {
		delete(s.chrome, path)
	}
	return s.persistLocked()
}

func (s *Store) chromeTilesLocked() []string {
	out := make([]string, 0, len(s.chrome))
	for p := range s.chrome {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func chromeSet(paths []string) map[string]bool {
	if len(paths) == 0 {
		return nil
	}
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p = strings.Trim(p, "/"); p != "" {
			m[p] = true
		}
	}
	return m
}
