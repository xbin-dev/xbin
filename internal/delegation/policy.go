package delegation

// policy.go — the seams delegation leaves to the agent-policy package (the
// orgs' agent rules, Org.Agents, and each person's agent preferences; plans/
// workspace-agent.md §5.3, §5.6). Delegation asks; the policy package
// answers. One interface, installed at boot into the three places that ask
// (the broker's Route, the terminal manager's agent open, the server's
// delegate gate). Until one is installed NoPolicy answers, and it refuses
// everything: a workspace agent without its rules reaches no tile.

import "errors"

// Subject is one delegate call as the policy sees it.
type Subject struct {
	Holder string // the sealed component acting (the workspace agent)
	User   string // the person it acts for
	Tile   string // the tile the call, read or session is on
	// Model is the model the agent says its conversation is on now
	// (X-XBin-Agent-Model). It is the sealed component's own statement —
	// xbind doesn't verify it — and "" when it said nothing.
	Model string
}

// StartPrefs are a person's preferences for tile agent sessions their
// workspace agent starts without asking them each time.
type StartPrefs struct {
	// Mode is the provider's mode to open in; "" is the provider's default,
	// asking, mode.
	Mode string
	// Net is the network picker ("" the launcher's default); API whether
	// the session gets the tile's API token.
	Net string
	API bool
}

// Policy is what the agent-policy package answers.
type Policy interface {
	// TileAllows is nil when s.Holder, acting for s.User on s.Model, may
	// reach s.Tile now — by the rules of the org that owns the TILE (the
	// workspace agent on or off there, its model rule). The error's text is
	// shown to the person ("Finance allows private models only").
	TileAllows(s Subject) error
	// SessionAllows is TileAllows for opening a tile agent session of
	// provider on s.Tile, adding the org's harness list.
	SessionAllows(s Subject, provider string) error
	// StartPrefs answers whether user lets their agent start a provider
	// session on tile without asking each time, and with which pickers.
	// false: every start needs the person's approval.
	StartPrefs(user, tile, provider string) (StartPrefs, bool)
}

// ErrNoPolicy is NoPolicy's refusal.
var ErrNoPolicy = errors.New("the workspace agent's rules aren't set up on this xbind")

// NoPolicy is the default: nothing is allowed, nothing starts unasked.
type NoPolicy struct{}

func (NoPolicy) TileAllows(Subject) error                             { return ErrNoPolicy }
func (NoPolicy) SessionAllows(Subject, string) error                  { return ErrNoPolicy }
func (NoPolicy) StartPrefs(string, string, string) (StartPrefs, bool) { return StartPrefs{}, false }

// PolicyOr is p, or NoPolicy when p is nil.
func PolicyOr(p Policy) Policy {
	if p == nil {
		return NoPolicy{}
	}
	return p
}

// OpenPolicy is a Policy that allows everything and starts nothing unasked,
// for tests and for a boot that has no agent rules to enforce yet. Its
// fields narrow it: Deny refuses the tiles it names, Prefs answers
// StartPrefs for the people it names.
type OpenPolicy struct {
	Deny  map[string]error      // tile → refusal
	Prefs map[string]StartPrefs // user → preferences (start without asking)
}

func (o OpenPolicy) TileAllows(s Subject) error              { return o.Deny[s.Tile] }
func (o OpenPolicy) SessionAllows(s Subject, _ string) error { return o.Deny[s.Tile] }
func (o OpenPolicy) StartPrefs(user, _, _ string) (StartPrefs, bool) {
	p, ok := o.Prefs[user]
	return p, ok
}
