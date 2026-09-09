package xbin

import "testing"

// The SDK's role check must agree with the broker's (internal/broker
// roleSatisfies has the same table): the broker forwards the granted role
// verbatim, so a tile granted a bus alias must pass the matching guard.
func TestRoleSatisfies(t *testing.T) {
	cases := []struct {
		have, want string
		ok         bool
	}{
		{"admin", "reader", true},
		{"writer", "reader", true},
		{"reader", "writer", false},
		{"reader", "reader", true},
		{"publisher", "writer", true}, // bus alias
		{"publisher", "reader", true},
		{"subscriber", "reader", true},
		{"subscriber", "writer", false},
		{"writer", "publisher", true}, // a guard written with the alias
		{"admin", "subscriber", true},
		{"custom", "custom", true},
		{"custom", "reader", false},
		{"", "reader", false},
		{"reader", "", false},
	}
	for _, c := range cases {
		if got := RoleSatisfies(c.have, c.want); got != c.ok {
			t.Errorf("RoleSatisfies(%q, %q) = %v, want %v", c.have, c.want, got, c.ok)
		}
	}
}
