package broker

import (
	"fmt"

	"github.com/xbin-dev/xbin/internal/util"
)

// SandboxMount is a resource a tile would mount into one of its tile
// sandboxes (D120, plans/tile-sandbox-runtime.md §5), resolved the way
// EnvFor resolves a backend's: the same path, the same grant.
type SandboxMount struct {
	Src       string // the resource's root on the host: its decrypted view (xbind-created)
	Role      string // the tile's effective role on it; a reader's mount is read-only
	Kind      string // the resource's type (only "filesystem" resolves)
	Encrypted bool   // file-backed resources always are: Src exists only while Ready
	Ready     bool   // the view is up now (the vault unsealed, gocryptfs mounted)
}

// ResourceMount resolves res for a sandbox of tile. It is refused unless
// res is a filesystem resource of the tile's own scope that the tile holds
// (declared in its uses and granted, the policy ceiling applied) — a
// cross-scope resource is never handed out as a path (docs/resources.md),
// and sqlite and every other kind don't mount. Create and start validate
// every mount through it, so a mount the tile no longer holds fails the
// next start.
func (b *Broker) ResourceMount(tile, res string) (SandboxMount, error) {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return SandboxMount{}, fmt.Errorf("no tile %s", tile)
	}
	rt, r, ok := b.parseRes(res)
	if !ok || r == nil {
		return SandboxMount{}, fmt.Errorf("%s is not a declared resource", res)
	}
	if r.Type != "filesystem" {
		return SandboxMount{}, fmt.Errorf("%s is a %s resource: only filesystem resources mount", res, r.Type)
	}
	if rt.Scope != c.Scope {
		return SandboxMount{}, fmt.Errorf("%s belongs to another scope: a tile mounts only its own scope's resources", res)
	}
	role, granted := b.grantedRole(tile, rt.String())
	if !granted {
		return SandboxMount{}, fmt.Errorf("the tile doesn't hold %s (declare it in uses)", rt.String())
	}
	sk := util.ScopeKey(rt.Scope)
	return SandboxMount{
		Src:       b.fsResPath(rt.Scope, rt.Name, false),
		Role:      role,
		Kind:      r.Type,
		Encrypted: true,
		Ready:     b.fsReady(sk, rt.Name),
	}, nil
}
