//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"testing"
)

// IDMapStatus must flag the single-uid fallback (the mode where apt/dpkg chown
// to system users fails with EINVAL) for a uid that has no /etc/subuid range —
// the eval-box condition — while reporting range-OK for a delegated one.
func TestIDMapStatus(t *testing.T) {
	if _, err := exec.LookPath("newuidmap"); err != nil {
		t.Skip("newuidmap not installed; range mapping unavailable here regardless")
	}
	// A uid extremely unlikely to have a delegated sub-id range → single-uid.
	if ok, reason := IDMapStatus(4242424, 4242424); ok {
		t.Errorf("uid 4242424 should have no sub-id delegation, got rangeOK; reason=%q", reason)
	} else if reason == "" {
		t.Error("single-uid fallback must carry a reason for the startup warning")
	}
	// The current user is range-capable iff a sub-id range is delegated to it;
	// assert only the reason/ok agreement, not a specific value (CI varies).
	ok, reason := IDMapStatus(os.Getuid(), os.Getgid())
	if ok && reason != "" {
		t.Errorf("rangeOK must carry no reason, got %q", reason)
	}
	if !ok && reason == "" {
		t.Error("non-range must carry a reason")
	}
}
