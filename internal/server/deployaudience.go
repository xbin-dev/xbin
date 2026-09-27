package server

import (
	"cmp"
	"encoding/json"
	"slices"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// Who receives a "deployments" event (P13). Non-primary activity rides only
// that type, so its audience is decided here, like pr's in eventFilter: a
// fact about a tile's primary reaches the tile's readers, as today's events
// do; a fact naming another deployment reaches only the tile's write
// audience and that deployment's own principals.

// deploymentsEventFor is the audience of a deployments event on tile T
// (e.Component), keyed on the deployment it names and judged at each
// delivery by the subscriber's current level; tile is the subscriber's own
// tile (credentialTile):
//   - a reader form (of op record, or of a deploy onto the primary) reaches
//     the rest of T's readers, those the full form doesn't reach;
//   - the full forms of ops record and deploy, op work-tree, and an event
//     naming no deployment or one it can't read reach T's write audience; a
//     deploy onto the primary also reaches the own principals of the
//     deployment it came from;
//   - the primary's reload, build, data, status and notify reach T's readers;
//   - anything naming a non-primary deployment D reaches the write audience
//     and D's own principals: never the primary's frame token, never another
//     tile's principal.
func (s *Server) deploymentsEventFor(p auth.Principal, tile string, e events.Event) bool {
	p, ok := s.currentLevel(p)
	if !ok {
		return false
	}
	t, prim, f := e.Component, s.primaryOf(e.Component), deployFactsOf(e.Data)
	write := p.IsAdmin() || (p.Component == "" && p.CanWriteTile(t)) ||
		(p.Via == "terminal" && p.Component == t && driverWrites(p, t))
	reader := p.IsAdmin() || p.CanReadTile(t)
	switch {
	case f.readerForm && (f.op == "record" || f.dep == prim):
		return reader && !write
	case f.op == "deploy" && f.dep == prim && f.from != prim:
		return write || ownPrincipal(p, tile, t, f.from)
	case f.dep == prim && slices.Contains([]string{"reload", "build", "data", "status", "notify"}, f.op):
		return reader
	case f.op == "record" || f.op == "work-tree" || f.dep == "" || f.dep == prim:
		return write
	}
	return write || ownPrincipal(p, tile, t, f.dep)
}

// ownPrincipal reports whether p, whose own tile is tile, is one of T's
// frame or instance principals bound to deployment dep whose user, if it
// carries one, holds write on T. The binding is the credential's own
// (11-contract §7.1): a frame token's claim, an instance token's
// generation, no binding meaning main (the name rule). A document of an
// xbin.window sub-path of T binds as T's own, as the deployment URL gate
// has it (ownPrincipalOf). Callers pass only a non-primary dep, so the
// primary's frame token is never an own principal here, nor is any
// credential bound to another deployment.
func ownPrincipal(p auth.Principal, tile, t, dep string) bool {
	own := p.Component == t || (p.Via == "frame" && tile == t)
	return dep != "" && own && (p.Via == "frame" || p.Via == "instance") &&
		cmp.Or(p.Deployment, util.MainDeployment) == dep && driverWrites(p, t)
}

// credentialTile is the tile p's credential belongs to: the registered
// component holding p's (the tile itself, or the tile of an xbin.window
// sub-path), else p's own. The event filter resolves it once, when the
// subscription starts: a delivery runs under the hub's lock, where no filter
// takes the registry's.
func (s *Server) credentialTile(p auth.Principal) string {
	if p.Component == "" || s.Reg == nil {
		return p.Component
	}
	if c, _, ok := s.Reg.Resolve(p.Component); ok {
		return c.Path
	}
	return p.Component
}

// driverWrites reports whether the user an element principal carries holds
// write on t; "" is the owner, or no user at all. It asks the user's levels,
// since tileLevel passes any element on its own tile.
func driverWrites(p auth.Principal, t string) bool {
	return p.UserID == "" || p.Access.CanWriteTile(t)
}

// currentLevel re-reads the user a subscriber carries, so a delivery judges
// the levels that user holds now rather than at connect time; false for a
// user deleted or disabled since, who receives nothing.
func (s *Server) currentLevel(p auth.Principal) (auth.Principal, bool) {
	if p.UserID == "" || s.Auth == nil || s.Auth.Users == nil {
		return p, true
	}
	u, ok := s.Auth.Users.Get(p.UserID)
	if !ok || u.Disabled {
		return p, false
	}
	if p.User != nil {
		p.User = u
	}
	p.Access, _ = s.Auth.Users.Access(p.UserID)
	return p, true
}

// PrimaryPolicy is a Policy that names each tile's primary deployment, which
// the deployments event filter keys on. Without it, or for a tile it answers
// "" for, the primary is main, as on every tile whose primary was never
// reassigned.
type PrimaryPolicy interface{ Primary(tile string) string }

func (s *Server) primaryOf(tile string) string {
	if pp, ok := s.policy().(PrimaryPolicy); ok {
		return cmp.Or(pp.Primary(tile), util.MainDeployment)
	}
	return util.MainDeployment
}

// deployFacts is what the audience keys on in a deployments event's data.
type deployFacts struct {
	op, dep, from string
	// readerForm: every key of the data is among its op's readerKeys. Any
	// other key makes it a full form, so an unexpected field narrows the
	// audience, never widens it.
	readerForm bool
}

// readerKeys are the keys a reader form may carry, per op.
var readerKeys = map[string][]string{
	"record": {"op", "what"},
	"deploy": {"op", "deployment", "checkpoint", "result", "phase", "by"},
}

// deployFactsOf reads data of any Go type through its JSON form, the bytes a
// subscriber would receive. Data that isn't a JSON object yields no op and no
// deployment, which reaches the write audience alone.
func deployFactsOf(data any) (f deployFacts) {
	var m map[string]json.RawMessage
	if b, err := json.Marshal(data); err != nil || json.Unmarshal(b, &m) != nil {
		return f
	}
	str := func(k string) (v string) { _ = json.Unmarshal(m[k], &v); return v }
	f.op, f.dep, f.from = str("op"), str("deployment"), str("from")
	keys, ok := readerKeys[f.op]
	f.readerForm = ok
	for k := range m {
		f.readerForm = f.readerForm && slices.Contains(keys, k)
	}
	return f
}
