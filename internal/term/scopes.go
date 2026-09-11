package term

import (
	"regexp"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Network scope for a terminal session (query ?net=). The netns/relay is fixed
// at spawn, so switching scope restarts the session (the frontend opens a fresh
// WS with a new ?net=). Default is internet: own netns, no host interfaces.
const (
	NetInternet = "internet" // own netns + egress relay, net:internet only (default)
	NetHost     = "host"     // share the host network (LAN + host services visible)
	NetNone     = "none"     // isolated netns, no egress (airgapped; xbind unreachable)
	NetOrg      = "org"      // the tile's owning org's network sets as the egress policy (D54)

	// ScopeSetPrefix + a set name is the scope of ONE named network set
	// (D65): "set:infra-net" — the relay under that set's rules (or host
	// networking when it says host). The broker spells the binding ref the
	// same way (broker.NetRefSet).
	ScopeSetPrefix = "set:"
)

// setNameRe is the users id charset. A set scope's name is echoed back in the
// clamp note, which is written into the PTY — nothing else may reach it.
var setNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// normalizeNet maps an incoming ?net= value to a known scope; "" (absent or
// unknown) lets clampTermScopes pick the principal's default on this tile. A
// set scope passes on shape alone — whether it exists here is the clamp's
// question (an unknown set lands on the default, with a note).
func normalizeNet(s string) string {
	switch s {
	case NetHost, NetNone, NetInternet, NetOrg:
		return s
	}
	if n, ok := strings.CutPrefix(s, ScopeSetPrefix); ok && setNameRe.MatchString(n) {
		return s
	}
	return ""
}

// TermNet is what the broker knows about a principal's terminal egress on
// ONE tile (D54): the org network's relay rules, which scopes the principal
// may pick, and the named sets they may pick (D65). Manager.TermNet supplies
// it; nil keeps the pre-D54 rules (termNet → internet, host admin-only).
type TermNet struct {
	Rules      []string // sandbox grant targets (net:…) for the org scope
	HostOK     bool     // net=host permitted (admin, or the org's sets carry `host`)
	InternetOK bool     // net=internet permitted (admin, or no org sets + termNet)
	OrgOK      bool     // the org scope exists (org-owned tile with sets that reach something)
	OrgHost    bool     // the org scope is host networking
	OrgLabel   string   // "org network (devs-net + infra-net)"
	OrgDesc    string   // the rules, one per line — the picker's tooltip
	// Sets are the named network sets pickable here (D65): every workspace
	// set for a workspace admin on any tile; for everyone else the sets
	// attached to the owning org, when they would get the org scope at all.
	// Never a default — a set is a narrowing someone chooses.
	Sets []NetSetScope
}

// NetSetScope is one named network set as a pickable terminal scope.
type NetSetScope struct {
	Name  string   // the set; the scope id is ScopeSetPrefix + Name
	Rules []string // sandbox grant targets (net:…) — the relay policy
	Host  bool     // the set carries `host`: host networking, no relay
	Label string   // "net set: infra-net"
	Desc  string   // the rules, one per line — the picker's tooltip
}

func (g TermNet) findSet(name string) *NetSetScope {
	for i := range g.Sets {
		if g.Sets[i].Name == name {
			return &g.Sets[i]
		}
	}
	return nil
}

// Scope is one network scope the client may pick, as the session frame
// lists them.
type Scope struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Desc  string `json:"desc,omitempty"`
}

func legacyTermNet(p auth.Principal) TermNet {
	return TermNet{InternetOK: p.CanTermNet() || p.IsAdmin(), HostOK: p.IsAdmin()}
}

func (m *Manager) termNetFor(p auth.Principal, rel string) TermNet {
	if m.TermNet == nil {
		return legacyTermNet(p)
	}
	return m.TermNet(p, rel)
}

// defaultScope is the tile's default for this principal: org where it
// exists, else internet where allowed, else offline. Never a named set —
// D65 adds pickable scopes, it moves nobody's default (the client keeps
// its own last pick per tile).
func defaultScope(p auth.Principal, g TermNet) string {
	switch {
	case g.OrgOK:
		return NetOrg
	case g.InternetOK || p.IsAdmin():
		return NetInternet
	}
	return NetNone
}

// ScopesFor lists the scopes a principal may pick on a tile — the org
// network, the named sets, internet, host, offline — and the default.
// Admins get the same list (plus internet/host) so they see what members see.
func ScopesFor(p auth.Principal, g TermNet) (scopes []Scope, def string) {
	if g.OrgOK {
		label := g.OrgLabel
		if label == "" {
			label = "org network"
		}
		scopes = append(scopes, Scope{ID: NetOrg, Label: label, Desc: g.OrgDesc})
	}
	for _, s := range g.Sets {
		scopes = append(scopes, Scope{ID: ScopeSetPrefix + s.Name, Label: s.Label, Desc: s.Desc})
	}
	if g.InternetOK || p.IsAdmin() {
		scopes = append(scopes, Scope{ID: NetInternet, Label: "internet", Desc: "public internet through the egress relay (no LAN)"})
	}
	if g.HostOK || p.IsAdmin() {
		scopes = append(scopes, Scope{ID: NetHost, Label: "host net", Desc: "the host's own network stack — LAN and host services, no relay, no metering"})
	}
	scopes = append(scopes, Scope{ID: NetNone, Label: "offline", Desc: "no network at all (xbind unreachable)"})
	return scopes, defaultScope(p, g)
}

// clampTermScopes applies the non-admin terminal defaults (D17 b+c): no live
// tile-API token without the TermAPI grant, no internet egress without the
// TermNet grant, and host networking stays admin-only. Clamped rather than
// rejected — the session still opens, and its banner reports the effective
// scope, so an ungranted user gets a working (airgapped, code-only) shell
// instead of an error.
//
// With organisation network sets (D54) the net half is a matrix over what
// the tile's org grants (g): org where it exists, internet where allowed
// (admin, or no org sets + termNet), host where allowed (admin, or the sets
// carry `host`). A named set (D65) is honoured when it is in g.Sets. An
// empty request means "the default here" — org, else internet, else none —
// for admins too, so they see what members see.
func clampTermScopes(p auth.Principal, api bool, net string, g TermNet) (bool, string) {
	admin := p.IsAdmin()
	if !admin && !p.CanTermAPI() {
		api = false
	}
	internetOK := g.InternetOK || admin
	hostOK := g.HostOK || admin
	switch net {
	case NetHost:
		// A refused host request lands on the tile's org network or offline —
		// never silently upgraded to plain internet (D17's clamp, kept).
		if !hostOK {
			if g.OrgOK {
				net = NetOrg
			} else {
				net = NetNone
			}
		}
	case NetInternet:
		if !internetOK {
			net = defaultScope(p, g)
		}
	case NetOrg:
		if !g.OrgOK {
			if internetOK {
				net = NetInternet
			} else {
				net = NetNone
			}
		}
	case NetNone:
	default: // "" — the tile's default; or a named set, kept when it is pickable here
		if n, ok := strings.CutPrefix(net, ScopeSetPrefix); !ok || g.findSet(n) == nil {
			net = defaultScope(p, g)
		}
	}
	return api, net
}

// clampNote explains a clamp to the person in the terminal.
func clampNote(asked, got string, g TermNet) string {
	why := ""
	switch asked {
	case NetHost:
		why = "host networking is admin-only here"
		if g.OrgOK {
			why += " (the org's network sets don't grant host)"
		}
	case NetInternet:
		why = "internet egress needs term-net here"
		if g.OrgOK {
			why = "on an org-owned tile the org's network sets replace plain internet"
		}
	case NetOrg:
		why = "this tile's owner has no org network"
	default:
		if n, ok := strings.CutPrefix(asked, ScopeSetPrefix); ok { // charset-checked by normalizeNet
			why = "network set " + n + " isn't available on this tile"
		}
	}
	_, _, label := resolveNet(got, g)
	return why + " — running as " + label
}

// resolveNet turns an already-clamped scope into what the spawn needs: host
// networking?, the relay's grant targets, and the human label of the scope.
func resolveNet(net string, g TermNet) (host bool, rules []string, label string) {
	switch net {
	case NetHost:
		return true, nil, "host net"
	case NetNone:
		return false, nil, "offline"
	case NetOrg:
		label = g.OrgLabel
		if label == "" {
			label = "org network"
		}
		return g.OrgHost, g.Rules, label
	}
	if n, ok := strings.CutPrefix(net, ScopeSetPrefix); ok {
		if s := g.findSet(n); s != nil {
			return s.Host, s.Rules, s.Label
		}
	}
	return false, []string{"net:internet"}, "internet"
}
