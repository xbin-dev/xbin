package users

import (
	"slices"
	"testing"
)

// Chrome approvals (D118) persist in users.json, list sorted, withdraw, and
// count as a leftover a new tile at the path must not inherit.
func TestChromeApprovals(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.ChromeApproved("apps/b") || len(s.ChromeTiles()) != 0 {
		t.Fatal("a fresh store approves nothing")
	}
	for _, p := range []string{"apps/b", "/apps/a/"} {
		if err := s.SetChromeApproved(p, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetChromeApproved("", true); err == nil {
		t.Fatal("an empty path was approved")
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.ChromeTiles(); !slices.Equal(got, []string{"apps/a", "apps/b"}) {
		t.Fatalf("after reopen: %v", got)
	}
	if left := s2.PathLeftovers("apps/a", "user:bob"); !slices.Contains(left, "approved as trusted chrome") {
		t.Fatalf("leftovers: %v", left)
	}
	if err := s2.SetChromeApproved("apps/a", false); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s3.ChromeApproved("apps/a") || !s3.ChromeApproved("apps/b") {
		t.Fatalf("withdraw: %v", s3.ChromeTiles())
	}
}
