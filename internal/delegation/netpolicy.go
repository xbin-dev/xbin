package delegation

// netpolicy.go — the workspace's policy for a person's workspace agent on
// the web and with MCP servers (D236; docs/protocol/delegation.md §Web and
// MCP): what a workspace admin set (Workspace.Web, Workspace.MCP, kept
// with the workspace's switch) and what the host's deployment file holds
// (hostfile.go), merged into what is in force (NetInForce). Each layer
// only narrows the one above: the host's file, then the admin, then the
// org rules and the person's own settings, which the server asks on top.

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/webfetch"
)

// The web modes, in order: each allows more than the one before it.
const (
	WebOff  = "off"
	WebRead = "read" // GET only, no body, no credentials
	WebFull = "full" // reserved: methods with a body, credentials — not built
)

// The MCP modes, in order.
const (
	MCPOff    = "off"
	MCPTiles  = "tiles"  // MCP tiles of this workspace the person can reach
	MCPListed = "listed" // reserved: also servers an admin listed by URL — not built
)

// ErrNotYet is the refusal of a mode this xbind reserves and doesn't run.
var ErrNotYet = errors.New("not available yet")

// MaxPatterns bounds an allow or deny list.
const MaxPatterns = 200

// WebPolicy is the web part of a policy layer (the admin's, the host's).
// Mode "" and a nil Search are unset: the layer above decides.
type WebPolicy struct {
	Mode   string   `json:"mode,omitempty"`
	Allow  []string `json:"allow,omitempty"`
	Deny   []string `json:"deny,omitempty"`
	Search *bool    `json:"search,omitempty"`
}

// MCPPolicy is the MCP part of a policy layer.
type MCPPolicy struct {
	Mode string `json:"mode,omitempty"`
}

func webRank(m string) int {
	switch m {
	case WebRead:
		return 1
	case WebFull:
		return 2
	}
	return 0
}

func mcpRank(m string) int {
	switch m {
	case MCPTiles:
		return 1
	case MCPListed:
		return 2
	}
	return 0
}

// checkMode: m is "" or a mode of the list; a reserved one is ErrNotYet.
func checkWebMode(m string) error {
	switch m {
	case "", WebOff, WebRead:
		return nil
	case WebFull:
		return fmt.Errorf(`web.mode %q: %w — a workspace agent reads the web (mode "read") and sends nothing`, m, ErrNotYet)
	}
	return fmt.Errorf(`web.mode: "off" or "read", not %q`, m)
}

func checkMCPMode(m string) error {
	switch m {
	case "", MCPOff, MCPTiles:
		return nil
	case MCPListed:
		return fmt.Errorf(`mcp.mode %q: %w — a workspace agent uses the MCP tiles of this workspace (mode "tiles"); servers listed by URL come later`, m, ErrNotYet)
	}
	return fmt.Errorf(`mcp.mode: "off" or "tiles", not %q`, m)
}

// normPatterns checks a list and puts it in its canonical form (sorted,
// each once).
func normPatterns(what string, in []string) ([]string, error) {
	if len(in) > MaxPatterns {
		return nil, fmt.Errorf("web.%s: at most %d hosts", what, MaxPatterns)
	}
	out := []string{}
	for _, s := range in {
		p, err := webfetch.ParsePattern(s)
		if err != nil {
			return nil, fmt.Errorf("web.%s: %v", what, err)
		}
		if !slices.Contains(out, string(p)) {
			out = append(out, string(p))
		}
	}
	slices.Sort(out)
	return out, nil
}

// Normalized is w checked: its mode one this xbind runs, its lists host
// patterns (canonical, sorted).
func (w WebPolicy) Normalized() (WebPolicy, error) {
	if err := checkWebMode(w.Mode); err != nil {
		return WebPolicy{}, err
	}
	var err error
	if w.Allow, err = normPatterns("allow", w.Allow); err != nil {
		return WebPolicy{}, err
	}
	if w.Deny, err = normPatterns("deny", w.Deny); err != nil {
		return WebPolicy{}, err
	}
	if len(w.Allow) == 0 {
		w.Allow = nil
	}
	if len(w.Deny) == 0 {
		w.Deny = nil
	}
	return w, nil
}

// Normalized is m checked.
func (m MCPPolicy) Normalized() (MCPPolicy, error) { return m, checkMCPMode(m.Mode) }

// WebInForce is the web policy in force: the mode, the lists of both
// layers (a URL must match each allow list that isn't empty, and no deny
// list), and whether the connector's native search may be offered.
type WebInForce struct {
	Mode      string   `json:"mode"`
	Allow     []string `json:"allow"`
	Deny      []string `json:"deny"`
	HostAllow []string `json:"hostAllow,omitempty"`
	HostDeny  []string `json:"hostDeny,omitempty"`
	Search    bool     `json:"search"`
	// ByHost: the host's file narrowed the mode (its ceiling is lower than
	// the admin's, or the admin set none and the host's is in force).
	ByHost bool `json:"byHost,omitempty"`
}

// MCPInForce is the MCP policy in force.
type MCPInForce struct {
	Mode   string `json:"mode"`
	ByHost bool   `json:"byHost,omitempty"`
}

// NetInForce is the whole policy in force.
type NetInForce struct {
	Web WebInForce `json:"web"`
	MCP MCPInForce `json:"mcp"`
}

// HostAllows reports whether the lists in force let a fetch reach host
// (ASCII, lower case, no port): every allow list that isn't empty names
// it, and no deny list does. The second answer says which list refused.
func (w WebInForce) HostAllows(host string) (bool, string) {
	pats := func(ss []string) []webfetch.Pattern {
		out := make([]webfetch.Pattern, len(ss))
		for i, s := range ss {
			out[i] = webfetch.Pattern(s)
		}
		return out
	}
	switch {
	case webfetch.MatchAny(pats(w.HostDeny), host):
		return false, "the host's deny list (set by whoever runs this xbin)"
	case webfetch.MatchAny(pats(w.Deny), host):
		return false, "this workspace's deny list"
	case len(w.HostAllow) > 0 && !webfetch.MatchAny(pats(w.HostAllow), host):
		return false, "the host's allow list (set by whoever runs this xbin), which doesn't name it"
	case len(w.Allow) > 0 && !webfetch.MatchAny(pats(w.Allow), host):
		return false, "this workspace's allow list, which doesn't name it"
	}
	return true, ""
}

// merge is what is in force of the admin's layer under the host's.
func merge(admin Workspace, host HostNet) NetInForce {
	var out NetInForce
	aw, hw := WebPolicy{}, WebPolicy{}
	if admin.Web != nil && !host.LockAll {
		aw = *admin.Web
	}
	if host.Web != nil {
		hw = *host.Web
	}
	// the mode: the admin's, else the host's; never past the host's
	mode := aw.Mode
	if mode == "" {
		mode = hw.Mode
		out.Web.ByHost = hw.Mode != ""
	}
	if hw.Mode != "" && webRank(mode) > webRank(hw.Mode) {
		mode, out.Web.ByHost = hw.Mode, true
	}
	if webRank(mode) > webRank(WebRead) { // a reserved mode is never in force
		mode = WebRead
	}
	if mode == "" {
		mode = WebOff
	}
	out.Web.Mode = mode
	out.Web.Allow, out.Web.Deny = orEmpty(aw.Allow), orEmpty(aw.Deny)
	out.Web.HostAllow, out.Web.HostDeny = hw.Allow, hw.Deny
	search := false
	switch {
	case aw.Search != nil:
		search = *aw.Search
	case hw.Search != nil:
		search = *hw.Search
	}
	if hw.Search != nil && !*hw.Search {
		search = false
	}
	out.Web.Search = search && mode != WebOff
	am, hm := "", ""
	if admin.MCP != nil && !host.LockAll {
		am = admin.MCP.Mode
	}
	if host.MCP != nil {
		hm = host.MCP.Mode
	}
	m := am
	if m == "" {
		m = hm
		out.MCP.ByHost = hm != ""
	}
	if hm != "" && mcpRank(m) > mcpRank(hm) {
		m, out.MCP.ByHost = hm, true
	}
	if mcpRank(m) > mcpRank(MCPTiles) {
		m = MCPTiles
	}
	if m == "" {
		m = MCPOff
	}
	out.MCP.Mode = m
	return out
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SetHost installs the host's deployment file (nil: none).
func (s *Store) SetHost(h *HostFile) {
	if s != nil {
		s.host = h
	}
}

// Host is the host's file in force now (the zero HostNet without one) and
// why the latest change to it was refused ("" when it wasn't).
func (s *Store) Host() (HostNet, string) {
	if s == nil || s.host == nil {
		return HostNet{}, ""
	}
	return s.host.Current()
}

// HostActive reports whether a host's file is installed.
func (s *Store) HostActive() bool { return s != nil && s.host != nil }

// NetPolicy is the web and MCP policy in force now.
func (s *Store) NetPolicy() NetInForce {
	h, _ := s.Host()
	return merge(s.Workspace(), h)
}

// NetPatch is a change of the admin's web and MCP settings: a nil field
// keeps what it was. Lists replace the list.
type NetPatch struct {
	WebMode   *string
	WebAllow  *[]string
	WebDeny   *[]string
	WebSearch *bool
	MCPMode   *string
}

// ErrHostLocked: the change names a setting the host's file holds.
type ErrHostLocked struct{ Settings []string }

func (e *ErrHostLocked) Error() string {
	return "set by your host: " + strings.Join(e.Settings, ", ") + " — the host's configuration (XBIN_WSAGENT_CONFIG) holds them"
}

// SetNet applies p to the admin's web and MCP settings, by. A mode past
// the host's ceiling is taken as asked and held to the ceiling (what is in
// force says so); under the host's lockAll every write is refused.
func (s *Store) SetNet(p NetPatch, by string) (Workspace, error) {
	if s == nil {
		return Workspace{}, ErrNoStore
	}
	if h, _ := s.Host(); h.LockAll {
		var named []string
		for name, set := range map[string]bool{"web.mode": p.WebMode != nil, "web.allow": p.WebAllow != nil, "web.deny": p.WebDeny != nil,
			"web.search": p.WebSearch != nil, "mcp.mode": p.MCPMode != nil} {
			if set {
				named = append(named, name)
			}
		}
		slices.Sort(named)
		if len(named) > 0 {
			return Workspace{}, &ErrHostLocked{Settings: named}
		}
	}
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	w, err := load(&s.ws, filepath.Join(s.dir, "workspace.json"))
	if err != nil {
		w = Workspace{} // an unreadable file reads as off; what the admin now sets replaces it
	}
	web := WebPolicy{}
	if w.Web != nil {
		web = *w.Web
	}
	if p.WebMode != nil {
		web.Mode = *p.WebMode
	}
	if p.WebAllow != nil {
		web.Allow = *p.WebAllow
	}
	if p.WebDeny != nil {
		web.Deny = *p.WebDeny
	}
	if p.WebSearch != nil {
		v := *p.WebSearch
		web.Search = &v
	}
	if web, err = web.Normalized(); err != nil {
		return Workspace{}, err
	}
	mcp := MCPPolicy{}
	if w.MCP != nil {
		mcp = *w.MCP
	}
	if p.MCPMode != nil {
		mcp.Mode = *p.MCPMode
	}
	if mcp, err = mcp.Normalized(); err != nil {
		return Workspace{}, err
	}
	w.Web, w.MCP = &web, &mcp
	w.NetBy, w.NetAt = by, s.now().UTC().Truncate(time.Second)
	return w, save(&s.ws, filepath.Join(s.dir, "workspace.json"), w)
}
