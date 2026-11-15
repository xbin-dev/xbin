package auth

// delegate.go — delegation (plans/wsagent/s2-delegation.md; the workspace
// agent, issue #51): a person's partition of a SEALED system component may
// call xbind and other tiles as that person.
//
//   - Who holds it: the sealed-component table, asked through one function
//     boot installs (SetDelegateHolders). Nothing in a workspace — no
//     xbin.json, grant, binding, policy row or approval — adds to it, and
//     no API reads it from a request.
//   - The credential: the instance token of that person's partition of the
//     component (partition.go), sent under its own scheme,
//     "Authorization: XBin-Delegate <token>". The same token as a Bearer is
//     the component itself (its own kv, vault, bus …); under this scheme it
//     is the person. The scheme is the opt-in per request, and the token —
//     registered by the runner for (tile, partition, uid) — pins who the
//     person is. Nothing a caller sends names the person.
//   - What it resolves to: the person's principal — Component "", their
//     live record and access — with Via "delegate" and Agent the component.
//     Never an admin (Principal.IsAdmin), never read-only, no generation
//     (it can't be a device's, and nothing it mints is bound to a login).
//   - Where it resolves: only on the gateway socket (WithGateway), and only
//     where the server asks DelegateFromRequest (the /api/ plane's delegate
//     gate). FromRequest doesn't know the scheme, so
//     everywhere else — /c/, /ws/term, /ws/events, /docs/, the pages, the
//     tile origins — the credential is no credential at all: 401.
//   - Off switches: the hook SetDelegationGate installs is asked on every
//     request (the workspace setting, the person's pause); without a hook
//     nothing delegates (fail closed).
//
// A user partition's instance token already stops authenticating when its
// person is deleted, disabled, re-created under the id or loses read on the
// component (lookupInstance → partitionCovered), so "who has an agent" is
// ordinary read access on the sealed component.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
)

// noAuthPerson is who a holder's single instance acts for under --no-auth
// (delegateOf): the delegation API's own name for the caller there.
const noAuthPerson = "owner"

// DelegateScheme is the Authorization scheme of a delegate call.
const DelegateScheme = "XBin-Delegate"

// delegateHolders answers whether a component is a sealed system component
// that holds delegation. One function, set once at boot from the
// sealed-component table (internal/sealed, Capabilities.Delegate); nil — no
// table installed — means no component of any workspace delegates.
var delegateHolders atomic.Pointer[func(component string) bool]

// SetDelegateHolders installs the holder lookup (boot). The table behind it
// is compiled in: nothing in a workspace — no xbin.json, grant, binding,
// policy row or approval — adds to it, and no API reads it from a request.
func SetDelegateHolders(f func(component string) bool) {
	if f == nil {
		delegateHolders.Store(nil)
		return
	}
	delegateHolders.Store(&f)
}

// HoldsDelegation reports whether component is a sealed system component
// that holds delegation.
func HoldsDelegation(component string) bool {
	f := delegateHolders.Load()
	return component != "" && f != nil && (*f)(component)
}

type gatewayKey struct{}

// WithGateway marks a connection as one that arrived on xbind's gateway
// socket — the unix socket only sandboxes on this host are given (boot:
// the gateway server's ConnContext; nothing a client sends can set it).
func WithGateway(ctx context.Context) context.Context {
	return context.WithValue(ctx, gatewayKey{}, true)
}

// OnGateway reports whether r arrived on the gateway socket.
func OnGateway(r *http.Request) bool {
	v, _ := r.Context().Value(gatewayKey{}).(bool)
	return v
}

// Delegation refusals. ErrNotDelegate is "this credential delegates
// nothing" (401: indistinguishable from a bad token); the others are 403s
// whose text the person's agent can show.
var (
	ErrNotDelegate = errors.New("not a delegate credential")
	ErrDelegateOff = errors.New("delegation is off")
)

// SetDelegationGate installs the off switches: f answers, on every delegate
// request, why component may not act for userID now (the workspace setting
// is off, the person paused their agent), or nil. Without it nothing
// delegates.
func (a *Auth) SetDelegationGate(f func(component, userID string) error) {
	a.mu.Lock()
	a.delegation = f
	a.mu.Unlock()
}

// delegateToken is the token of a delegate call, when r is one.
func delegateToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) <= len(DelegateScheme) || !strings.EqualFold(h[:len(DelegateScheme)], DelegateScheme) || h[len(DelegateScheme)] != ' ' {
		return "", false
	}
	return strings.TrimSpace(h[len(DelegateScheme)+1:]), true
}

// IsDelegateRequest reports whether r carries the delegate scheme, valid or
// not: such a request is judged as a delegate's or refused, never as
// whatever else it carries (a cookie, a frame token).
func IsDelegateRequest(r *http.Request) bool {
	_, ok := delegateToken(r)
	return ok
}

// DelegateFromRequest resolves a delegate call. present is false when r
// doesn't carry the delegate scheme (the caller authenticates r as usual).
// Otherwise err is nil and p is the person's delegate principal, or err
// says why not: ErrNotDelegate for a token that delegates nothing (unknown,
// not a user partition's, not a sealed holder's, a non-primary
// deployment's, its person gone), or the gate's refusal (wrapped
// ErrDelegateOff).
func (a *Auth) DelegateFromRequest(r *http.Request) (p Principal, present bool, err error) {
	tok, ok := delegateToken(r)
	if !ok {
		return Principal{}, false, nil
	}
	if !OnGateway(r) || TransportOf(r).Tunnel {
		// the scheme is honoured on the gateway socket alone: a token that
		// left the host is worth nothing on the console listener, an
		// ingress or a device's tunnel
		return Principal{}, true, ErrNotDelegate
	}
	p, err = a.delegateOf(tok)
	return p, true, err
}

// DelegateRecheck returns a function that resolves r's delegate credential
// again, later: for a call that runs long (a followed stream), so that it
// ends when its person is disabled or deleted, loses the holder, or
// switches delegation off. It captures the credential now — a handler
// below may strip it from r. nil when r is no delegate call.
func (a *Auth) DelegateRecheck(r *http.Request) func() (Principal, error) {
	tok, ok := delegateToken(r)
	if !ok || !OnGateway(r) || TransportOf(r).Tunnel {
		return nil
	}
	return func() (Principal, error) { return a.delegateOf(tok) }
}

// delegateOf resolves a delegate token (DelegateFromRequest's rules).
func (a *Auth) delegateOf(tok string) (Principal, error) {
	id, ok := a.lookupInstance(tok) // a user partition's: 401 once it isn't covered (partition.go)
	userID, isUser := id.partition.User()
	if ok && a.noAuth && !isUser && id.deployment == "" && HoldsDelegation(id.component) {
		// --no-auth: no people sign in, so nobody has a partition, and a
		// holder runs as one instance (sealed.Options.NoAuth). That instance
		// is the delegate of the one person there is — the implicit owner,
		// as a person: named, never the root token's principal, never an
		// admin, so the cap, tickets and the audit log apply as for anyone.
		// Where people sign in, a global instance stays nobody's delegate.
		a.mu.RLock()
		gate := a.delegation
		a.mu.RUnlock()
		if gate == nil {
			return Principal{}, ErrDelegateOff
		}
		if why := gate(id.component, noAuthPerson); why != nil {
			return Principal{}, errors.Join(ErrDelegateOff, why)
		}
		return Principal{UserID: noAuthPerson, Via: "delegate", Agent: id.component}, nil
	}
	switch {
	case !ok, !isUser, id.uid == "":
		return Principal{}, ErrNotDelegate // the global instance and every unpartitioned tile: nobody's delegate
	case !HoldsDelegation(id.component):
		return Principal{}, ErrNotDelegate
	case id.deployment != "":
		return Principal{}, ErrNotDelegate // a person's partition runs on the primary only (PD-17)
	}
	u, found := a.userSnapshot(userID) // live: disabled or deleted is nobody
	if !found {
		return Principal{}, ErrNotDelegate
	}
	a.mu.RLock()
	gate := a.delegation
	a.mu.RUnlock()
	if gate == nil {
		return Principal{}, ErrDelegateOff
	}
	if why := gate(id.component, userID); why != nil {
		return Principal{}, errors.Join(ErrDelegateOff, why)
	}
	return Principal{UserID: userID, User: u, Access: a.accessSnapshot(userID), Via: "delegate", Agent: id.component}, nil
}
