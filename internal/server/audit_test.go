package server

import "testing"

func TestAuditable(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"POST", "/users", true},          // create a user — governance
		{"DELETE", "/users/bob", true},    // remove a user
		{"PATCH", "/auth-settings", true}, // sign-in policy
		{"POST", "/auth-rotate-token", true},
		{"POST", "/grants", true}, // approve a grant
		{"GET", "/users", false},  // reads are never audited
		{"GET", "/status", false},
		{"PUT", "/prefs/layout", false}, // data-plane noise
		{"DELETE", "/prefs/settings", false},
		{"PUT", "/kv/res/key", false},     // data-plane noise
		{"POST", "/blob/res/x", false},    // data-plane noise
		{"POST", "/bus/publish", false},   // data-plane noise
		{"POST", "/vault/apps~x/k", true}, // vault writes ARE governance
		{"POST", "/lifecycle", true},      // enable/disable/offload
	}
	for _, c := range cases {
		if got := auditable(c.method, c.path); got != c.want {
			t.Errorf("auditable(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
