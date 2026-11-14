package auth

// delegate.go — delegation (plans/wsagent/s2-delegation.md; the workspace
// agent, issue #51): a person's partition of a SEALED system component may
// call xbind and other tiles as that person.
//
//   - Who holds it: a compiled-in table (sealedDelegates). Nothing in a
//     workspace — no xbin.json, grant, binding, policy row or approval —
//     adds to it, and no API reads it from a request.
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
//   - Where it resolves: only where the server asks DelegateFromRequest (the
//     /api/ plane's delegate gate). FromRequest doesn't know the scheme, so
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
	"errors"
	"net/http"
	"strings"
)

// DelegateScheme is the Authorization scheme of a delegate call.
const DelegateScheme = "XBin-Delegate"

// sealedDelegates names the sealed system components that hold delegation.
// Compiled in; the sealed-component table (spike S1) takes its place. Empty
// until the workspace agent ships: no component of any workspace delegates.
var sealedDelegates = map[string]bool{}

// HoldsDelegation reports whether component is a sealed system component
// that holds delegation.
func HoldsDelegation(component string) bool { return sealedDelegates[component] }

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
	if TransportOf(r).Tunnel {
		return Principal{}, true, ErrNotDelegate // a device's tunnel carries that device's credentials only (transport.go)
	}
	id, ok := a.lookupInstance(tok) // a user partition's: 401 once it isn't covered (partition.go)
	userID, isUser := id.partition.User()
	switch {
	case !ok, !isUser, id.uid == "":
		return Principal{}, true, ErrNotDelegate // the global instance and every unpartitioned tile: nobody's delegate
	case !HoldsDelegation(id.component):
		return Principal{}, true, ErrNotDelegate
	case id.deployment != "":
		return Principal{}, true, ErrNotDelegate // a person's partition runs on the primary only (PD-17)
	}
	u, found := a.userSnapshot(userID) // live: disabled or deleted is nobody
	if !found {
		return Principal{}, true, ErrNotDelegate
	}
	a.mu.RLock()
	gate := a.delegation
	a.mu.RUnlock()
	if gate == nil {
		return Principal{}, true, ErrDelegateOff
	}
	if why := gate(id.component, userID); why != nil {
		return Principal{}, true, errors.Join(ErrDelegateOff, why)
	}
	return Principal{UserID: userID, User: u, Access: a.accessSnapshot(userID), Via: "delegate", Agent: id.component}, true, nil
}
