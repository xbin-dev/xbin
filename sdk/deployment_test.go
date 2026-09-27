package xbin

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// covers P17 — Deployment() is XBIN_DEPLOYMENT: the deployment's name for a
// backend that is not its tile's primary, "" when the variable is absent (the
// primary, and every backend of an xbind without tile deployments). Self()
// stays the bare tile path in both.
func TestDeploymentFromEnv(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "apps/crm")

	t.Setenv("XBIN_DEPLOYMENT", "dev")
	if got := Deployment(); got != "dev" {
		t.Errorf("Deployment() = %q with XBIN_DEPLOYMENT=dev, want %q", got, "dev")
	}
	if got := Self(); got != "apps/crm" {
		t.Errorf("Self() = %q in a non-primary deployment, want the bare tile path", got)
	}

	// Absent, not empty: the primary gets no variable at all. t.Setenv above
	// restores the original value when the test ends.
	if err := os.Unsetenv("XBIN_DEPLOYMENT"); err != nil {
		t.Fatal(err)
	}
	if got := Deployment(); got != "" {
		t.Errorf("Deployment() = %q without XBIN_DEPLOYMENT, want \"\" (the primary)", got)
	}
	if got := Self(); got != "apps/crm" {
		t.Errorf("Self() = %q for the primary, want the bare tile path", got)
	}
}

// covers P17 — CallerInfo.Deployment is read from X-XBin-Deployment, which
// xbind sets only for callers bound to a non-primary deployment; without the
// header every field reads exactly as before (the zero state), and the header
// changes no other field. The module keeps zero dependencies (compat rule 8):
// sdk/go.mod carries no require directive.
func TestCallerDeployment(t *testing.T) {
	r := httptest.NewRequest("GET", "/things", nil)
	r.Header.Set("X-XBin-From", "apps/crm")
	r.Header.Set("X-XBin-Role", "reader")
	r.Header.Set("X-XBin-User", "alice")
	r.Header.Set("X-XBin-User-Level", "write")

	today := Caller(r)
	want := CallerInfo{From: "apps/crm", Role: "reader", User: "alice", UserLevel: "write"}
	if today != want {
		t.Errorf("Caller without X-XBin-Deployment = %+v, want %+v", today, want)
	}
	if today.Deployment != "" {
		t.Errorf("Deployment = %q without the header, want \"\" (the primary)", today.Deployment)
	}

	r.Header.Set("X-XBin-Deployment", "dev")
	c := Caller(r)
	if c.Deployment != "dev" {
		t.Errorf("Deployment = %q with X-XBin-Deployment: dev, want %q", c.Deployment, "dev")
	}
	if c.From != "apps/crm" {
		t.Errorf("From = %q from a non-primary deployment, want the bare tile path", c.From)
	}
	c.Deployment = ""
	if c != today {
		t.Errorf("the header changed another field: %+v, want %+v", c, today)
	}

	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(mod), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "require" {
			t.Errorf("sdk/go.mod:%d: %q — the SDK must stay zero-dependency (compat rule 8)", i+1, line)
		}
	}
}
