package users

import (
	"fmt"
	"maps"

	"github.com/xbin-dev/xbin/internal/limits"
)

// OrgLimits is an org's per-tile resource ceilings (D203, docs/auth.md
// §Ownership), set by workspace admins: an admin of the org may approve or
// raise a limit of kind k for a tile the org owns up to Limits[k], never
// past it, and never past what fits the workspace. A kind without a ceiling
// leaves the org's admins unable to raise it above the default. Per tile,
// not an org-wide budget (the owner's call, 2026-10-27).
type OrgLimits map[limits.Kind]int64

// maxOrgCeiling bounds a ceiling to something no unit overflows on (1 PiB
// of GiB, 2^40 MiB): a typo, not a policy.
const maxOrgCeiling = 1 << 40

// SetOrgLimits changes an org's ceilings: each kind in change to its value,
// 0 removing it; kinds not named keep theirs. It answers the ceilings now.
func (s *Store) SetOrgLimits(orgID string, change map[limits.Kind]int64) (OrgLimits, error) {
	for k, v := range change {
		switch {
		case !k.Valid():
			return nil, fmt.Errorf("unknown limit kind %q", k)
		case v < 0 || v > maxOrgCeiling:
			return nil, fmt.Errorf("%s: a ceiling is a positive number in %s (0 or null removes it)", k, k.Meta().Unit)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return nil, fmt.Errorf("no such org %q", orgID)
	}
	next := maps.Clone(org.Limits)
	if next == nil {
		next = OrgLimits{}
	}
	for k, v := range change {
		if v == 0 {
			delete(next, k)
		} else {
			next[k] = v
		}
	}
	if len(next) == 0 {
		next = nil
	}
	no := *org
	no.Limits = next // a fresh map: Org copies handed out share none of it
	s.orgs[no.ID] = &no
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	return maps.Clone(next), nil
}

// OrgLimitsOf is an org's ceilings (a copy; nil for none or no such org).
func (s *Store) OrgLimitsOf(orgID string) OrgLimits {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if org := s.orgs[normalizeID(orgID)]; org != nil {
		return maps.Clone(org.Limits)
	}
	return nil
}
