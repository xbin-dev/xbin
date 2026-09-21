package term

import "github.com/xbin-dev/xbin/internal/auth"

// openOptsFor is the "user" half of opening a session on a tile the
// principal may open one on (rel: the tile; the caller has passed the
// CanTerminalTile gate): the scope clamps, the effective network, and the
// restricted tier — shared by the shell (ServeWS) and agent (agent.go)
// kinds. Non-admin users get the restricted tier: the D18 kernel lockdown
// (no nested user/mount namespaces, dangerous caps dropped — apt still
// works), the D17 scope clamps (api/net), source visibility cut to their
// allow-list, and resource limits. Admins and the owner keep full caps +
// full view for dev work.
func (m *Manager) openOptsFor(p auth.Principal, rel, cwd, netMode, gpuMode string, apiAccess bool) openOpts {
	o := openOpts{
		cwd: cwd, net: netMode, gpu: gpuMode,
		homeKey: HomeKey(p), userID: p.UserID,
		api: apiAccess, restricted: !p.IsAdmin(),
		netGrant: m.termNetFor(p, rel),
	}
	asked := o.net
	o.api, o.net = clampTermScopes(p, o.api, o.net, o.netGrant)
	o.scopes, _ = ScopesFor(p, o.netGrant)
	if asked != "" && asked != o.net {
		o.netNote = clampNote(asked, o.net, o.netGrant)
	}
	o.netHost, o.netRules, o.label = resolveNet(o.net, o.netGrant)
	if o.restricted {
		if m.TermView != nil { // D40 allow-list view
			o.readable, o.rootFiles = m.TermView(p)
		} else if m.HiddenTiles != nil { // deny-list fallback
			o.hide = m.HiddenTiles(p)
		}
	}
	return o
}
