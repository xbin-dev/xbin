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
		if !ok || !live {
			return Caller{}, refuse(http.StatusUnauthorized, "invalid_api_key", "this LLM key is not valid (it expired, was revoked, or its session ended)")
		}
		return Caller{User: g.User, Component: g.Component, Via: "key", Label: g.Label}, nil
	}
	if p.Component != "" {
		if s.opt.Bound == nil || !s.opt.Bound(p.Component) {
			return Caller{}, refuse(http.StatusForbidden, "xbin_llm_not_bound",
				p.Component+" is not bound to the workspace's LLM connector: a workspace admin binds its llm slot to xbin:llm (bx bind "+p.Component+" <slot>=xbin:llm)")
		}
		c := Caller{Component: p.Component, Via: "component"}
		switch id, isPart := p.Partition.User(); {
		case isPart:
			c.User = id // a person's partition acts for that person
		case p.UserID != "":
			c.User = p.UserID // the person driving the frame, terminal or agent session
		case p.Via == "frame" || p.Via == "terminal":
			c.User = OwnerUser // the root token's own frames and terminals
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
