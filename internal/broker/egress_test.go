package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// D35: the filtered-internet binding vocabulary.
func TestValidateFilteredInternet(t *testing.T) {
	good := []string{
		"internet:api.stripe.com",
		"internet:api.stripe.com:443",
		"internet:203.0.113.7",
		"internet:203.0.113.0/24:443",
		"internet:api.stripe.com,files.stripe.com:443",
	}
	for _, ref := range good {
		if err := validateFilteredInternet(ref); err != nil {
			t.Errorf("%q must validate: %v", ref, err)
		}
	}
	bad := []string{
		"internet:",                   // empty
		"internet:api.example.com,,x", // empty spec
		"internet:internet",           // redundant
		"internet:192.168.1.5",        // private — that's lan:'s job
		"internet:10.0.0.0/8:443",     // private CIDR
		"internet:127.0.0.1",          // loopback
		"internet:*.stripe.com",       // globs are allowance grammar, not bindings
		"internet:bad port:99999",     // unparsable
	}
	for _, ref := range bad {
		if err := validateFilteredInternet(ref); err == nil {
			t.Errorf("%q must be refused", ref)
		}
	}
}

// EgressFor maps a filtered binding to per-spec sandbox rules.
func TestEgressForFiltered(t *testing.T) {
	b := testBroker(t)
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{
			"apps/email": {"net": registry.BindTo("internet:api.stripe.com:443,203.0.113.0/24")},
		}
	}); err != nil {
		t.Fatal(err)
	}
	// apps/email needs a net interface for netBinding to resolve the slot.
	c, ok := b.Reg.Component("apps/email")
	if !ok {
		t.Fatal("fixture component missing")
	}
	c.Manifest.Interfaces = map[string]registry.Iface{"net": {Kind: "net"}}
	pol := b.EgressFor(c)
	if len(pol.Rules) != 2 {
		t.Fatalf("want 2 rules, got %v", pol.Strings())
	}
	if !pol.HasHostRules() {
		t.Fatal("hostname spec must yield a host rule (relay pinning)")
	}
	joined := strings.Join(pol.Strings(), " ")
	if !strings.Contains(joined, "net:api.stripe.com:443") || !strings.Contains(joined, "net:203.0.113.0/24") {
		t.Fatalf("unexpected rules: %s", joined)
	}
}
