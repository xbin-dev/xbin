package registry

// The manifest's "agent" key: how much of a tile's API a person's workspace
// agent may use when it acts for them (delegation, docs/auth.md).
const (
	AgentOff  = "off"  // none of it
	AgentRead = "read" // GET and HEAD only
	AgentFull = "full" // everything the person's level allows (the default)
)

// AgentAccess is the manifest's "agent" value as xbind enforces it: absent
// is AgentFull, and a word this xbind doesn't know is AgentOff — a later
// xbind's narrower word must never read as wider here.
func (m Manifest) AgentAccess() string {
	switch m.Agent {
	case "", AgentFull:
		return AgentFull
	case AgentRead:
		return AgentRead
	}
	return AgentOff
}
