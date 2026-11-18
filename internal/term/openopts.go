package term

import (
	"slices"

	"github.com/xbin-dev/xbin/internal/auth"
)

// openOptsFor is the "user" half of opening a session on a tile the
// principal may open one on (tile: the registered tile cwd is or lies in,
// sessionTile; the caller has passed the CanTerminalTile gate on it): the
// scope clamps, the effective network, and the
// restricted tier — shared by the shell (ServeWS) and agent (agent.go)
// kinds. Non-admin users get the restricted tier: the D18 kernel lockdown
// (no nested user/mount namespaces, dangerous caps dropped — apt still
// works), the D17 scope clamps (api/net), source visibility cut to their
// allow-list, and resource limits. Admins and the owner keep full caps +
// full view for dev work.
func (m *Manager) openOptsFor(p auth.Principal, tile, cwd, netMode, gpuMode string, apiAccess bool) openOpts {
	o := openOpts{
		cwd: cwd, tile: tile, net: netMode, gpu: gpuMode,
		homeKey: HomeKey(p), userID: p.UserID,
		api: apiAccess, restricted: !p.IsAdmin(),
		netGrant: m.termNetFor(p, tile), // the tile's org's network sets (D54)
	}
	o.noLLM = !p.IsAdmin() && !p.CanTermAPI() // no tile API token in their terminals (D17): no LLM key either
	asked := o.net
	o.api, o.net = clampTermScopes(p, o.api, o.net, o.netGrant)
	o.scopes, _ = ScopesFor(p, o.netGrant)
	if asked != "" && asked != o.net {
		o.netNote = clampNote(asked, o.net, o.netGrant)
	}
	o.netHost, o.netRules, o.label = resolveNet(o.net, o.netGrant)
	m.setHome(p, &o, m.homePlanFor(p, tile, false)) // their real home, or this tile's copy of it (home.go, D245)
	return o
}

// setHome gives o its home and what follows from it: the restricted tier
// for every non-admin — and for an admin (and the root token) whose home
// here is the tile's copy (homePlan.confines: with an unrestricted
// sandbox's capabilities the tile's code could mount its way back to the
// real home) — and that tier's view of the workspace.
//
// The host's own network stack is an admin's too (scopes.go): an admin's
// confined session asked onto it lands where a member's would — the
// owner's network or offline, never silently plain internet — and is
// told; it is not in the session's list of scopes. (Also where the owner's
// network sets grant `host` to members: the grant an admin holds doesn't
// say which of the two it is from, and the narrower answer is the safe one.)
func (m *Manager) setHome(p auth.Principal, o *openOpts, pl homePlan) {
	o.home = pl
	o.restricted = !p.IsAdmin() || pl.confines()
	if pl.confines() && p.IsAdmin() {
		if o.net == NetHost {
			o.net = NetNone
			if o.netGrant.OrgOK {
				o.net = o.netGrant.ownerScope()
			}
			o.netHost, o.netRules, o.label = resolveNet(o.net, o.netGrant)
			o.netNote = "host networking isn't offered to a session that is confined like a member's — trust this tile for an admin's — running as " + o.label
			o.home.netInstead = o.label // said in the session's first lines and its home dialog too
		}
		o.scopes = slices.DeleteFunc(slices.Clone(o.scopes), func(s Scope) bool { return s.ID == NetHost })
	}
	o.readable, o.rootFiles, o.hide = nil, nil, nil
	if o.restricted {
		if m.TermView != nil { // D40 allow-list view
			o.readable, o.rootFiles = m.TermView(p)
		} else if m.HiddenTiles != nil { // deny-list fallback
			o.hide = m.HiddenTiles(p)
		}
	}
}
