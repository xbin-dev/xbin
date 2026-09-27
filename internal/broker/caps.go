package broker

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/users"
)

// SandboxesCap is the reserved capability grant a **sandbox-manager tile**
// holds to drive xbind's tile-sandbox runtime (plans/tile-sandbox-runtime.md
// §8.1, D120): the `/api/xbin/sandboxes…` manager routes answer only an
// instance principal of a tile holding it. The manager declares
// `uses: [{target: "cap:sandboxes", role: "writer"}]` and the grant lands
// pending:
//
//   - it is never same-scope auto-granted (no component is named
//     "cap:sandboxes", so grantedRole's same-scope rule can't match it);
//   - only a workspace admin approves it — no allowance delegates it, not even
//     `cap:*` (users.NeverDelegable, the floor the xbin family has), so neither
//     an org admin (D26) nor a personal tile's owner (D88) can;
//   - the policy ceiling's `xbin-caps` deny class strips it (ceilingBlockWith);
//   - revoking it — or a ceiling change that strips it — fires OnCapChange,
//     which stops the tile's sandboxes and keeps their state; approving
//     restarts nothing (grantRestart leaves it out of the backend restarts:
//     nothing about it is materialized at spawn).
const SandboxesCap = users.SandboxesCap

// SandboxesFor reports whether a tile holds cap:sandboxes now. The runtime
// asks it on every manager call (with p.Component), so a revoke or a ceiling
// change takes effect on the next request whether or not OnCapChange is
// wired. grantedRole applies the policy ceiling; a grant row left behind by a
// removed tile holds nothing.
func (b *Broker) SandboxesFor(tile string) bool {
	if tile == "" {
		return false
	}
	if _, ok := b.Reg.Component(tile); !ok {
		return false
	}
	_, ok := b.grantedRole(tile, SandboxesCap)
	return ok
}

// capChanged reports a cap: grant's effective state for one tile to
// OnCapChange after an approve or a revoke. held is the state AFTER the
// change (grantedRole: rows, ceiling), not the action — revoking one of two
// rows for the same cap leaves it held.
func (b *Broker) capChanged(tile, capTarget string) {
	if b.OnCapChange == nil || !strings.HasPrefix(capTarget, "cap:") {
		return
	}
	_, held := b.grantedRole(tile, capTarget)
	b.OnCapChange(tile, capTarget, held)
}

// capSweep fires OnCapChange(tile, cap, false) for every cap: grant row the
// policy ceiling now strips. A ceiling moves without touching any grant — a
// workspace/org policy row, a permission set, a tile's transfer to a new
// owner — and each of those mutations ends in usersEvent, which calls this.
// It is stateless, so it repeats for a row that stays stripped; the hook is
// idempotent (stopping a tile with nothing running is a no-op).
func (b *Broker) capSweep() {
	if b.OnCapChange == nil || b.Reg == nil {
		return
	}
	type key struct{ tile, capTarget string }
	seen := map[key]bool{}
	for _, g := range b.Reg.Workspace().Grants {
		k := key{g.From, g.Target}
		if !strings.HasPrefix(g.Target, "cap:") || seen[k] {
			continue
		}
		seen[k] = true
		if _, held := b.grantedRole(g.From, g.Target); !held {
			b.OnCapChange(g.From, g.Target, false)
		}
	}
}
