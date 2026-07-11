package broker

import (
	"fmt"
	"strings"

	"github.com/magik6k/xbin/internal/users"
)

// Policy ceiling (plans/orgs.md, D20). The rows and their evaluation live in
// internal/users (Store.Ceiling); this file only dispatches a grant target to
// the right capability class and phrases the refusal. It is asked at every
// evaluation chokepoint — grantedRole (covering explicit grants, interface
// bindings, and same-scope auto-grants alike) and netBinding — so a ceiling
// holds even against a hand-edited xbin.json; the approval APIs additionally
// reject up front with the blocking row named.

// ceilingBlockMsg reports why the policy ceiling blocks `from` reaching
// `target` ("" = allowed). from=="" (not a tile) is never blocked.
func (b *Broker) ceilingBlockMsg(from, target string) string {
	if b.Users == nil || from == "" {
		return ""
	}
	c := b.Users.Ceiling(from)
	deny := func(kind string) string {
		if row, ok := c.DenyRow(kind); ok {
			return fmt.Sprintf("a policy row for tiles matching %q denies %s (workspace/org policy — see /docs/auth.md)", row.Tiles, kind)
		}
		return ""
	}
	switch {
	case target == "xbin" || strings.HasPrefix(target, "xbin:"):
		return deny(users.PolicyDenyXbinCaps)
	case strings.HasPrefix(target, "gpu:"):
		return deny(users.PolicyDenyGPU)
	case strings.HasPrefix(target, "net:"): // legacy net grants (pre-bindings)
		return deny(users.PolicyDenyNet)
	default: // component paths and res:… targets
		if row, ok := c.MayCallBlocker(target); ok {
			return fmt.Sprintf("a policy row for tiles matching %q allow-lists call targets and %q is not covered (workspace/org policy — see /docs/auth.md)", row.Tiles, target)
		}
		return ""
	}
}

// ceilingAllows is the boolean form used on the evaluation hot paths.
func (b *Broker) ceilingAllows(from, target string) bool {
	return b.ceilingBlockMsg(from, target) == ""
}

// validateNewPath is the shared reserved-segment gate on every tile-creating
// entry point (create / clone / git import / builtin import / template
// instantiate): the `o` org marker must name an existing org, `u` is reserved
// (plans/orgs.md). Existing on-disk paths are never rejected — bx doctor
// warns instead.
func (b *Broker) validateNewPath(path string) error {
	if b.Users == nil {
		return nil
	}
	return b.Users.ValidateNewTilePath(path)
}
