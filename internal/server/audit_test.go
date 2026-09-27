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
		{"PUT", "/branding", true},        // the workspace's title/icon (D76)
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
		// tile sandboxes (D120): definitions, lifecycle, snapshots and the
		// policy are audited; driving one is the data plane
		{"POST", "/sandboxes", true},
		{"PATCH", "/sandboxes/sb-1", true},
		{"DELETE", "/sandboxes/sb-1", true},
		{"POST", "/sandboxes/sb-1/start", true},
		{"POST", "/sandboxes/sb-1/reset", true},
		{"PUT", "/sandboxes/policy", true},
		{"POST", "/sandboxes/sb-1/snapshots", true},
		{"POST", "/sandboxes/sb-1/snapshots/s-1/restore", true},
		{"DELETE", "/sandboxes/sb-1/execs/x-1", true}, // killing an exec
		{"POST", "/sandboxes/sb-1/run", false},
		{"POST", "/sandboxes/sb-1/execs", false},
		{"POST", "/sandboxes/sb-1/execs/x-1/stdin", false},
		{"POST", "/sandboxes/sb-1/execs/x-1/signal", false},
		{"POST", "/sandboxes/sb-1/execs/x-1/resize", false},
		{"PUT", "/sandboxes/sb-1/files/content", false},
		{"POST", "/sandboxes/sb-1/files/mkdir", false},
		{"POST", "/sandboxes/sb-1/files/remove", false},
		{"POST", "/sandboxes/sb-1/files/move", false},
		{"PUT", "/sandboxes/sb-1/tar", false},
		{"POST", "/sandboxes/copy", false},
	}
	for _, c := range cases {
		if got := auditable(c.method, c.path); got != c.want {
			t.Errorf("auditable(%s %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
