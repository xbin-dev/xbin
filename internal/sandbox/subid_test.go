//go:build linux

package sandbox

import "testing"

// The eval-box regression: /etc/subgid is keyed by the USER (login name or
// uid), never by gid. For a `useradd --system` account where uid != gid
// (e.g. xbin uid 999, gid 988), matching subgid by gid found nothing and the
// sandbox silently fell back to single-uid mode — breaking apt/dpkg installs
// that chown to system users. parseSubID must match by owner name or uid.
func TestParseSubIDByUserNotGid(t *testing.T) {
	subgid := []byte("magik6k:100000:65536\nxbin:200000:65536\n")

	// Keyed by the user (name "xbin", uid 999) — must find the range even though
	// the account's gid (988) never appears in the file.
	if s, c, ok := parseSubID(subgid, "xbin", 999); !ok || s != 200000 || c != 65536 {
		t.Errorf("by owner name: got (%d,%d,%v), want (200000,65536,true)", s, c, ok)
	}
	// By uid when the line is keyed numerically.
	if s, _, ok := parseSubID([]byte("999:300000:65536\n"), "xbin", 999); !ok || s != 300000 {
		t.Errorf("by uid: got (%d,_,%v), want start 300000", s, ok)
	}
	// The old bug: looking this up by gid 988 must NOT match (there is no 988
	// line, and the account name isn't "988").
	if _, _, ok := parseSubID(subgid, "", 988); ok {
		t.Error("gid 988 must not match a user-keyed subgid line (the bug)")
	}
	// No delegation for an unknown user.
	if _, _, ok := parseSubID(subgid, "nobody", 4242); ok {
		t.Error("unknown user must not match")
	}
	// A malformed empty-owner line must not be matched by an empty owner.
	if _, _, ok := parseSubID([]byte("::\n"), "", 999); ok {
		t.Error("empty owner must not match a malformed line")
	}
}
