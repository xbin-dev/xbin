package boot

// delegation.go — people's workspace agents (docs/auth.md §Delegation;
// D228): the wiring of what the server's delegate gate, the broker's Route
// and the terminal manager ask.
//
// Two things are left to the packages that own them, each one assignment:
//
//   - WHO HOLDS DELEGATION: auth.SetDelegateHolders, pointed at the
//     sealed-component table (internal/sealed, Capabilities.Delegate) when
//     it is in the tree. Until then no lookup is installed and no component
//     of any workspace delegates: every route below answers, and no
//     credential passes the gate.
//   - THE AGENT RULES: st.AgentPolicy, a delegation.Policy (the orgs'
//     Org.Agents rules and people's agent preferences). nil is
//     delegation.NoPolicy: every tile is refused, nothing starts unasked.

import (
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/server"
)

func (st *State) setupDelegation(srv *server.Server) {
	store := delegation.OpenStore(filepath.Join(st.WS, "data", "delegation"))
	srv.Delegation = store
	// the off switches, asked on every delegate call: sandboxes on (--dev
	// and --no-auth excepted), the workspace's switch, the person's pause
	st.Auth.SetDelegationGate(server.DelegationGate(store, st.Cfg.Isolate, st.Cfg.Dev || st.Cfg.NoAuth))
	// people or their access changed: running delegate calls and standing
	// approvals are judged again at once (tiles: serve.go's watch loop)
	st.Broker.OnPeopleChanged = srv.SweepDelegation
	srv.DelegationDev = st.Cfg.Dev || st.Cfg.NoAuth // developing an agent: no step-up; --no-auth's owner answers as the person
	srv.AgentPolicy = st.AgentPolicy
	st.Broker.AgentPolicy = st.AgentPolicy
	if st.Term != nil {
		st.Term.AgentPolicy = st.AgentPolicy
	}
}
