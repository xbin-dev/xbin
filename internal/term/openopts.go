package term

import "github.com/xbin-dev/xbin/internal/auth"

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
func (m *Manager) setHome(p auth.Principal, o *openOpts, pl homePlan) {
	o.home = pl
	o.restricted = !p.IsAdmin() || pl.confines()
	o.readable, o.rootFiles, o.hide = nil, nil, nil
	if o.restricted {
		if m.TermView != nil { // D40 allow-list view
			o.readable, o.rootFiles = m.TermView(p)
		} else if m.HiddenTiles != nil { // deny-list fallback
			o.hide = m.HiddenTiles(p)
		}
	}
}
