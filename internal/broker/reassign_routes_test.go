package broker

import (
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers P7 P13 09-fabric-§8 — a reassignment's registrations step
// (reassignroutes.go): once the plane moved a provider's primary, its
// consumers get the grants event and restart when the active interface
// instances changed, so a consumer bound to prov#inst is re-injected with
// the new primary's URL; a tile whose active hosts changed gets the ingress
// reconcile; equal tables (a tile that registers neither) do nothing.
func TestPrimaryReassignedRebindsConsumers(t *testing.T) {
	f := newRoutesFx(t, map[string]string{fxAccts: util.MainDeployment, fxCMS: util.MainDeployment, fxGW: util.MainDeployment})
	b := f.b
	for _, reg := range []struct {
		name string
		call func() int
	}{
		{"main's instances", func() int {
			return regCall(t, b.apiIfaceInstancesSet, acctsMain, "PUT", "/iface-instances", "", map[string]any{"instances": map[string]string{"acme": "/m/acme"}}).Code
		}},
		{"dev's instances", func() int {
			return regCall(t, b.apiIfaceInstancesSet, acctsDev, "PUT", "/iface-instances", "", map[string]any{"instances": map[string]string{"acme": "/d/acme"}}).Code
		}},
		{"main's hosts", func() int {
			return regCall(t, b.apiIngressHosts, cmsMain, "PUT", "/ingress-hosts", "", map[string]any{"hosts": []string{"m.sites.example.com"}}).Code
		}},
		{"dev's hosts", func() int {
			return regCall(t, b.apiIngressHosts, cmsDev, "PUT", "/ingress-hosts", "", map[string]any{"hosts": []string{"d.sites.example.com"}}).Code
		}},
	} {
		if code := reg.call(); code != 200 {
			t.Fatalf("%s: %d", reg.name, code)
		}
	}
	f.effects(t)

	// the plane moved apps/accts' and apps/cms' primaries to dev
	dormantRecord(t, f.root, fxAccts, "dev", false)
	dormantRecord(t, f.root, fxCMS, "dev", false)
	dormantRecord(t, f.root, fxGW, "dev", false)
	f.installPlane(t, b)
	b.PrimaryReassigned(fxAccts, util.MainDeployment, "dev")
	if g, r, n := f.effects(t); !slices.Equal(g, []string{fxAccts}) || !slices.Equal(r, []string{fxMailer}) || n != 0 {
		t.Errorf("instances moved: grants %v, restarts %v, reconciles %d", g, r, n)
	}
	if got := mailerURL(b); got != "/api/apps/accts/d/acme" {
		t.Errorf("the consumer's slot after the move: %q", got)
	}
	b.PrimaryReassigned(fxCMS, util.MainDeployment, "dev")
	if g, r, n := f.effects(t); len(g) != 0 || len(r) != 0 || n != 1 {
		t.Errorf("hosts moved: grants %v, restarts %v, reconciles %d", g, r, n)
	}
	b.PrimaryReassigned(fxGW, util.MainDeployment, "dev")
	if g, r, n := f.effects(t); len(g) != 0 || len(r) != 0 || n != 0 {
		t.Errorf("nothing registered: grants %v, restarts %v, reconciles %d", g, r, n)
	}

	// and back
	dormantRecord(t, f.root, fxAccts, util.MainDeployment, false)
	f.installPlane(t, b)
	b.PrimaryReassigned(fxAccts, "dev", util.MainDeployment)
	if g, r, _ := f.effects(t); !slices.Equal(g, []string{fxAccts}) || !slices.Equal(r, []string{fxMailer}) {
		t.Errorf("back to main: grants %v, restarts %v", g, r)
	}
	if got := mailerURL(b); got != "/api/apps/accts/m/acme" {
		t.Errorf("the consumer's slot back on main: %q", got)
	}
}
