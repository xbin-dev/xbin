package llm

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// caller.go — who is calling, and whether they may (docs/llm.md "Who may
// call", docs/auth.md "The LLM connector").
//
// Three kinds of caller reach the OpenAI-compatible routes:
//
//   - a person: a browser session, a device, bx with the owner token.
//     Admitted by the workspace's people switch (admins | everyone |
//     nobody). Metered to that person.
//   - a component (an element principal: a tile's backend, frame, terminal
//     or agent session). Default-deny: admitted only when one of the tile's
//     openai slots is bound to the connector ("xbin:llm"), asked per call.
//     Metered to the component and to the person acting through it: the
//     person of a partition's backend, the person driving a frame or a
//     terminal; nobody for a backend that serves everyone.
//   - the holder of an LLM key (tokens.go): admitted by the key, metered to
//     the person and component it was minted for.
//
// The identity always comes from the credential xbind verified, never from
// a header or the body.

// Caller is who a call is attributed to.
type Caller struct {
	// User is the acting person: a user id, OwnerUser for the root token,
	// "" when nobody acts (a tile's backend that isn't a person's
	// partition).
	User string
	// Component is the calling tile ("" for a person's direct call).
	Component string
	// Via says how the caller was identified: person, component, key.
	Via string
	// Admin: a workspace admin's own call (sees upstream detail in errors).
	Admin bool
	// Label is an LLM key's label ("" otherwise), for the log.
	Label string
	// Deployment is the tile deployment a component's credential acts in,
	// or an LLM key was minted in or for ("" = main, or a session that
	// follows the primary).
	Deployment string
	// ReadOnly, when set, is why this caller may read (models, status) and
	// not call: a tile's non-primary deployment under the read clamp of its
	// edge policy (docs/auth.md, the edge policy).
	ReadOnly string
}

// refusal is an answer that stops a call, in OpenAI's error shape.
type refusal struct {
	status int
	err    sdkllm.Error
	header http.Header
}

func (r *refusal) write(w http.ResponseWriter) {
	copyHeader(w.Header(), r.header)
	sdkllm.WriteError(w, r.status, r.err)
}

func refuse(status int, code, msg string) *refusal {
	return &refusal{status: status, err: sdkllm.Error{Message: msg, Code: code}}
}

// bearerKey is the LLM key a request presents, if it presents one.
func bearerKey(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(h[7:])
	return tok, strings.HasPrefix(tok, KeyPrefix)
}

// ResolveKey is the server's gate for an LLM key (server.Server.LLMKey): a
// live key answers the principal its request runs as — a marker only
// (Via "llm"); identify reads the grant from the key itself.
func (s *Service) ResolveKey(token string) (auth.Principal, bool) {
	g, ok := s.keySource.Resolve(token)
	if !ok {
		return auth.Principal{}, false
	}
	p := auth.Principal{Via: "llm"}
	if g.User != OwnerUser {
		p.UserID = g.User
	}
	return p, true
}

// identify names the caller of r and says whether they may call, under
// snap.
func (s *Service) identify(r *http.Request, snap *snapshot) (Caller, *refusal) {
	p := auth.PrincipalOf(r)
	if tok, ok := bearerKey(r); ok || p.Via == "llm" {
		g, live := s.keySource.Resolve(tok)
		if !ok || !live || !s.keyHolds(g) {
			return Caller{}, refuse(http.StatusUnauthorized, "invalid_api_key",
				"this LLM key is not valid (it expired or was revoked, its session ended, or its owner no longer has the access it was made under)")
		}
		c := Caller{User: g.User, Component: g.Component, Via: "key", Label: g.Label, Deployment: g.Deployment}
		// a key a tile's backend asked for is held to the tile's edge policy
		// for the deployment it was minted in, on every call: a policy that
		// changes, or a deployment that goes, takes its keys along. (A
		// person's session key is theirs: it only names its deployment.)
		if g.Bound && s.opt.Edge != nil {
			switch mode, why := s.opt.Edge(auth.Principal{Component: g.Component, Deployment: g.Deployment, Via: "instance"}); mode {
			case EdgeFull:
			case EdgeRead:
				c.ReadOnly = why
			default:
				return Caller{}, refuse(http.StatusForbidden, "xbin_llm_edge", why)
			}
		}
		return c, nil
	}
	if p.Component != "" {
		if s.opt.Bound == nil || !s.opt.Bound(p.Component) {
			return Caller{}, refuse(http.StatusForbidden, "xbin_llm_not_bound",
				p.Component+" is not bound to the workspace's LLM connector: a workspace admin binds its llm slot to xbin:llm (bx bind "+p.Component+" <slot>=xbin:llm)")
		}
		c := Caller{Component: p.Component, Via: "component", User: actor(p), Deployment: p.Deployment}
		if s.opt.Edge != nil { // a non-primary deployment uses the binding by the tile's edge policy
			switch mode, why := s.opt.Edge(p); mode {
			case EdgeFull:
			case EdgeRead:
				c.ReadOnly = why
			default:
				return Caller{}, refuse(http.StatusForbidden, "xbin_llm_edge", why)
			}
		}
		return c, nil
	}
	c := Caller{User: p.UserID, Via: "person", Admin: s.opt.IsAdmin != nil && s.opt.IsAdmin(p)}
	if p.Owner {
		c.User = OwnerUser
	}
	if c.User == "" {
		return Caller{}, refuse(http.StatusForbidden, "xbin_llm_forbidden", "the LLM connector needs a person, a bound component or an LLM key")
	}
	switch snap.people {
	case PeopleEveryone:
	case PeopleNobody:
		return Caller{}, refuse(http.StatusForbidden, "xbin_llm_people_off",
			"people don't call this workspace's LLM connector directly (its people switch is \"nobody\"): use a tile bound to it")
	default:
		if !c.Admin {
			return Caller{}, refuse(http.StatusForbidden, "xbin_llm_people_off",
				"calling this workspace's LLM connector directly is for workspace admins (its people switch is \"admins\"): an admin opens it to everyone, or you use a tile bound to it")
		}
	}
	return c, nil
}

// ready refuses a call the connector can't serve at all: nothing
// configured, or switched off.
func ready(snap *snapshot) *refusal {
	switch {
	case !snap.configured():
		return refuse(http.StatusServiceUnavailable, "xbin_llm_not_configured",
			"this workspace's LLM connector is not configured: a workspace admin adds an upstream (bx llm upstream add, /docs/llm.md)")
	case !snap.enabled:
		why := "a workspace admin turned this workspace's LLM connector off"
		if snap.locked("enabled") {
			why = "this workspace's LLM connector is turned off by your host"
		}
		return refuse(http.StatusServiceUnavailable, "xbin_llm_disabled", why)
	}
	return nil
}

// admit is ready, then identify.
func (s *Service) admit(r *http.Request, snap *snapshot) (Caller, *refusal) {
	if ref := ready(snap); ref != nil {
		return Caller{}, ref
	}
	return s.identify(r, snap)
}

// actor is the person acting through an element principal: the person of a
// partition's backend, the person driving a frame, terminal or agent
// session, the owner for the root token's own frames and terminals, nobody
// for a backend that serves everyone.
func actor(p auth.Principal) string {
	switch id, isPart := p.Partition.User(); {
	case isPart:
		return id
	case p.UserID != "":
		return p.UserID
	case p.Via == "frame" || p.Via == "terminal":
		return OwnerUser
	}
	return ""
}

// The edge modes Options.Edge answers.
const (
	EdgeFull  = "full"  // the tile's primary: the binding as it is
	EdgeRead  = "read"  // a non-primary deployment under the read clamp: models and status only
	EdgeBlock = "block" // a non-primary deployment whose edge policy blocks the binding
)

// keyHolds reports whether a live key's grant still stands: its person
// exists, is enabled and can still open the tile it was minted for, and —
// a key minted under a tile's binding — the tile is still bound. Asked on
// every call, so a disabled person's keys stop at once, whoever forgot to
// revoke them.
func (s *Service) keyHolds(g KeyGrant) bool {
	// (a key for nobody — a bound component's, for a backend that serves
	// everyone — stands on its binding alone)
	if g.User != "" && g.User != OwnerUser && s.opt.KeyOwner != nil && !s.opt.KeyOwner(g.User, g.Component) {
		return false
	}
	if g.Bound && (s.opt.Bound == nil || !s.opt.Bound(g.Component)) {
		return false
	}
	return true
}

// UserGone ends every LLM key of a person who was signed out everywhere,
// disabled or deleted (the broker's OnUserSignedOut).
func (s *Service) UserGone(user string) int {
	if s.llmKeys == nil {
		return 0
	}
	return s.llmKeys.RevokeWhere(func(g KeyGrant) bool { return g.User == user })
}

// BindingChanged ends the keys minted under component's binding once the
// binding is gone (the broker's OnGrantChange, which a bind or unbind
// fires).
func (s *Service) BindingChanged(component string) int {
	if s.llmKeys == nil || (s.opt.Bound != nil && s.opt.Bound(component)) {
		return 0
	}
	return s.llmKeys.RevokeWhere(func(g KeyGrant) bool { return g.Bound && g.Component == component })
}
