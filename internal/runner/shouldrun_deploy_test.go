package runner

import "testing"

// covers D127n PO-2 — a deployment's spawn is gated by its own namespaces'
// encryption hold once boot installs ShouldRunDeployment (08-data §3.6):
// the hook answers per deployment, whatever the tile's ShouldRun says;
// without it every deployment takes the tile's gate, today's.
func TestShouldRunPerDeployment(t *testing.T) {
	r := &Runner{ShouldRun: func(string) bool { return true }}
	for _, dep := range []string{"main", "dev"} {
		if !r.shouldRun("apps/x", dep) {
			t.Errorf("without the hook, %s doesn't take the tile's gate", dep)
		}
	}
	var asked []string
	r.ShouldRunDeployment = func(tile, dep string) bool {
		asked = append(asked, tile+"+"+dep)
		return dep != "dev"
	}
	if !r.shouldRun("apps/x", "main") || r.shouldRun("apps/x", "dev") {
		t.Error("the per-deployment hold isn't asked")
	}
	if want := "apps/x+main apps/x+dev"; len(asked) != 2 || asked[0]+" "+asked[1] != want {
		t.Errorf("asked %v; want %s", asked, want)
	}
}
