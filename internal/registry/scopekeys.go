package registry

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// Scope data keys (D118). A scope's resource data lives under its
// util.ScopeKey: data/resources/<key>, data/resources-enc/<key>/<name> and
// .xbin/resenc/<key>/<name>, and the gocryptfs password derives from
// "fs:<key>/<name>". The key is the path with "/" turned into "~", which is
// not injective: apps~x and apps/x share a key, and a scope at "workspace"
// shares the workspace-level resources' key. Two scopes with one key would
// share encrypted volumes, quota, removal and backups. Rekeying would move
// every workspace's data, so instead each key has one holder and a
// colliding scope is refused. It stays a scope, so its tiles keep their
// scope and trust boundary, but it declares no resources (nothing is
// provisioned, mounted, backed up or removed for it), and its tiles carry
// the reason as a manifest error.
//
// Which scope holds a contested key:
//   - the workspace scope always holds "workspace";
//   - otherwise the holder recorded in data/scope-keys.json, written the
//     first time the key is contested, and kept while that holder's
//     directory exists (so a restart doesn't hand the key to a newcomer);
//   - otherwise the scope that held the key at the previous scan (the
//     existing scope beats the new one);
//   - when nothing tells (a collision that predates this check, at the first
//     boot, or two scopes that appeared in the same scan), every claimant is
//     refused until one is renamed.

const scopeKeysFile = "scope-keys.json" // under data/

// wsKey is the workspace scope's data key (util.ScopeKey("")).
var wsKey = util.ScopeKey("")

// scopeKeys is the registry's memory of who holds each data key.
type scopeKeys struct {
	held   map[string]string // key → the scope that held it at the last scan
	claims map[string]string // key → holder of a contested key (persisted)
	loaded bool
}

func (k *scopeKeys) claimsPath(root string) string {
	return filepath.Join(root, "data", scopeKeysFile)
}

func (k *scopeKeys) load(root string) {
	k.loaded = true
	k.claims = map[string]string{}
	b, err := os.ReadFile(k.claimsPath(root)) // xbind-owned: data/ is masked out of every sandbox
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &k.claims); err != nil {
		slog.Warn("scope keys: unreadable claims file, ignoring it", "file", k.claimsPath(root), "err", err)
		k.claims = map[string]string{}
	}
}

func (k *scopeKeys) persist(root string) {
	b, _ := json.MarshalIndent(k.claims, "", "  ")
	if err := fsutil.WriteFileAtomicIn(k.claimsPath(root), append(b, '\n'), 0o600); err != nil {
		slog.Warn("scope keys: can't record contested key holders", "err", err)
	}
}

// resolve decides the holder of every key in scopes and refuses the other
// claimants (refuseScope). Call with the registry lock held.
func (k *scopeKeys) resolve(root string, scopes map[string]*ScopeManifest) {
	if !k.loaded {
		k.load(root)
	}
	exists := func(p string) bool {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		return err == nil
	}
	byKey := map[string][]string{}
	for p := range scopes {
		key := util.ScopeKey(p)
		byKey[key] = append(byKey[key], p)
	}
	held := map[string]string{}
	changed := false
	for key, ps := range byKey {
		sort.Strings(ps)
		holder := ""
		switch c, claimed := k.claims[key]; {
		case key == wsKey: // the workspace scope's: every claimant refused
		case claimed && (slices.Contains(ps, c) || exists(c)):
			holder = c
		case claimed:
			delete(k.claims, key) // the recorded holder is gone
			changed = true
			fallthrough
		default:
			if len(ps) == 1 {
				holder = ps[0]
			} else if h, ok := k.held[key]; ok && slices.Contains(ps, h) {
				holder = h
				k.claims[key] = h
				changed = true
			}
		}
		for _, p := range ps {
			if p == holder {
				held[key] = p
				continue
			}
			var why string
			switch {
			case key == wsKey:
				why = fmt.Sprintf("its resource data key %q is the workspace-level resources' key", key)
			case holder != "":
				why = fmt.Sprintf(`its resource data key %q is held by scope %s ("/" and "~" in a path map to the same key)`, key, holder)
			default:
				why = fmt.Sprintf(`its resource data key %q is also claimed by %s, and neither held it before ("/" and "~" in a path map to the same key)`, key, others(ps, p))
			}
			refuseScope(scopes[p], p, why+" — its resources are not provisioned; rename the directory")
		}
	}
	for key, c := range k.claims {
		if _, live := byKey[key]; !live && !exists(c) {
			delete(k.claims, key)
			changed = true
		}
	}
	k.held = held
	if changed {
		k.persist(root)
	}
}

func others(ps []string, self string) string {
	var out []string
	for _, p := range ps {
		if p != self {
			out = append(out, "scope "+p)
		}
	}
	if len(out) == 1 {
		return out[0]
	}
	return fmt.Sprint(out)
}

// refuseScope drops every resource of a scope whose data key it doesn't
// hold and records why.
func refuseScope(sm *ScopeManifest, path, why string) {
	sm.Resources = nil
	sm.addErr(path, why)
}

// HoldsScopeKey reports whether scope may use its data key: the workspace
// scope always, a registered scope when it holds the key, an unregistered
// path when no scope and no recorded claim has the key. Resource removal and
// restore check it before touching data/resources*/<key>.
func (r *Registry) HoldsScopeKey(scope string) bool {
	if scope == "" {
		return true
	}
	key := util.ScopeKey(scope)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if h, ok := r.keys.held[key]; ok {
		return h == scope
	}
	if key == wsKey {
		return false
	}
	if c, ok := r.keys.claims[key]; ok && c != scope {
		return false
	}
	for p := range r.scopes {
		if util.ScopeKey(p) == key {
			return false // a refused claimant — this one or another
		}
	}
	return true
}

// ScopeKeyClash names what a new component at path would share its data key
// with — an existing scope, a recorded holder, or "the workspace" — or "".
// The creation paths refuse such a path (the tile could become a scope).
func (r *Registry) ScopeKeyClash(path string) string {
	key := util.ScopeKey(path)
	if key == wsKey {
		return "the workspace-level resources"
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for p := range r.scopes {
		if p != path && util.ScopeKey(p) == key {
			return "scope " + p
		}
	}
	if c, ok := r.keys.claims[key]; ok && c != path {
		return "scope " + c
	}
	return ""
}
