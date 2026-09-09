package server

import (
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
)

// Policy is what the broker decides for the server — the questions the HTTP
// plane asks about principals and components. The daemon installs the
// broker's implementation at boot (InstallPolicy); without one, NoopPolicy
// answers as the single-user server always did: the owner is the only
// admin, tiles are unowned, non-admin subscribers see no bus events,
// documents get no interface meta and no extra sandbox tokens, and code
// grants do not open the /c/ static plane. Every answer is wire-visible
// somewhere (/components, the injected meta, the CSP header), so an
// implementation changes what it returns only with the docs that describe
// it (docs/protocol.md, docs/elements.md).
type Policy interface {
	// IsAdmin reports whether a principal may use admin-capable endpoints.
	// The owner is admin upstream of this; this answers for users and for
	// elements granted xbin:admin.
	IsAdmin(p auth.Principal) bool
	// OwnerOf reports a component's owner ref (D24): "user:<id>",
	// "org:<id>", or "" when unowned or in single-user mode — callers
	// switch on "" (requestpage.go), so it stays a string.
	OwnerOf(path string) string
	// BusAllows authorizes one bus event for a non-admin subscriber.
	BusAllows(p auth.Principal, e events.Event) bool
	// Interfaces renders a component's http interface slots for the
	// xbin-interfaces meta: a single slot is {url, service[, instance]}, a
	// multi slot {service, multi, endpoints} (plans/interfaces.md). The map
	// is marshalled as-is into the document; nil or empty ⇒ no meta.
	Interfaces(comp string) map[string]any
	// SandboxExtras returns the `sandbox` tokens a component's grants unlock
	// beyond the fixed base set (ND11: cap:open-links → allow-popups
	// allow-popups-to-escape-sandbox). Consulted for sandboxed documents
	// only; composed into their CSP header and reported on /components.
	SandboxExtras(comp string) []string
	// CodeReadGrant reports whether element `from` holds a code[:<comp>]
	// source-read grant covering `target` — opens the /c/ static plane for
	// element principals beyond their own tile.
	CodeReadGrant(from, target string) bool
}

// NoopPolicy is the single-user default: owner-only and fail-closed.
type NoopPolicy struct{}

func (NoopPolicy) IsAdmin(auth.Principal) bool                 { return false }
func (NoopPolicy) OwnerOf(string) string                       { return "" }
func (NoopPolicy) BusAllows(auth.Principal, events.Event) bool { return false }
func (NoopPolicy) Interfaces(string) map[string]any            { return nil }
func (NoopPolicy) SandboxExtras(string) []string               { return nil }
func (NoopPolicy) CodeReadGrant(string, string) bool           { return false }

// InstallPolicy sets the Policy the server consults (the broker's, at boot).
func (s *Server) InstallPolicy(p Policy) { s.Pol = p }

// policy is the installed Policy, or NoopPolicy when none is.
func (s *Server) policy() Policy {
	if s.Pol == nil {
		return NoopPolicy{}
	}
	return s.Pol
}
