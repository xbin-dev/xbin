package broker

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/xbin-dev/xbin/internal/registry"
)

// Auth tier 2 (plans/auth.md §9): each scope's backends run under a
// dedicated uid allocated from uidBase. Requires xbind to stay root
// (--scope-uids). Effects:
//   - element identity is mechanical (/proc of siblings unreadable)
//   - elements cannot write source — theirs or anyone's (source dirs are
//     owned by the workspace owner; editing is terminal-only)
//   - vault and per-scope data dirs are enforced by file permissions
//
// The uid→scope mapping persists in .xbin/uids.json so uids stay stable
// across restarts (sqlite files etc. keep their owner).

const uidBase = 20000

type uidAllocator struct {
	path string

	mu    sync.Mutex
	next  int
	byKey map[string]int // scope path → uid
}

func newUIDAllocator(root string) (*uidAllocator, error) {
	a := &uidAllocator{
		path:  filepath.Join(root, ".xbin", "uids.json"),
		next:  uidBase,
		byKey: map[string]int{},
	}
	if b, err := os.ReadFile(a.path); err == nil {
		_ = json.Unmarshal(b, &a.byKey)
		for _, uid := range a.byKey {
			if uid >= a.next {
				a.next = uid + 1
			}
		}
	}
	slog.Info("auth tier 2: per-scope uids enabled", "base", uidBase, "allocated", len(a.byKey))
	return a, nil
}

func (a *uidAllocator) uidFor(scope string) int {
	if scope == "" {
		scope = "\x00workspace"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if uid, ok := a.byKey[scope]; ok {
		return uid
	}
	uid := a.next
	a.next++
	a.byKey[scope] = uid
	b, _ := json.MarshalIndent(a.byKey, "", "  ")
	_ = os.WriteFile(a.path, b, 0o644)
	return uid
}

// chownScopeData hands a scope's resource dir to its uid so backends can
// open their sqlite/blob files directly.
func (a *uidAllocator) chownScopeData(dir, scope string) {
	uid := a.uidFor(scope)
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			_ = os.Chown(p, uid, uid)
		}
		return nil
	})
}

// SpawnUser is installed into the runner when tier 2 is enabled.
func (b *Broker) SpawnUser(c *registry.Component) *syscall.Credential {
	if b.uids == nil {
		return nil
	}
	uid := uint32(b.uids.uidFor(c.Scope))
	return &syscall.Credential{Uid: uid, Gid: uid}
}
