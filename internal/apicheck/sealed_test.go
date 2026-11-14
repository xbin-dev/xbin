package apicheck

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sealed"
	"github.com/xbin-dev/xbin/internal/server"
)

// TestSealedNamesShadowNoRoute walks the whole route table (D227): a sealed
// component at xbin/<name> answers at /api/xbin/<name>/…, inside xbind's own
// API, so its name may never be the first segment of a route — and every
// first segment there is must be refused as a name, or a route added later
// under a sealed name would be shadowed silently. The compiled-in table's
// names, and the name the workspace agent takes (xbin/wsagent; xbin/agent
// is xbind's own /agent/providers and /agent/history), are checked against
// it.
func TestSealedNamesShadowNoRoute(t *testing.T) {
	inv := inventory(t)
	if len(inv) < 100 {
		t.Fatalf("an inventory of %d routes — is the fixture wiring intact?", len(inv))
	}
	var api []string
	for p := range inv {
		api = append(api, p)
	}
	sort.Strings(api)
	core := mount(t).CoreRoutes()

	nameRe := regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	firsts := map[string]bool{}
	for _, pat := range api {
		path := pat
		if i := strings.IndexByte(pat, ' '); i >= 0 {
			path = pat[i+1:]
		}
		seg, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if strings.HasPrefix(seg, "{") {
			if seg != "{$}" {
				t.Errorf("%s (%s): a route whose first segment is a wildcard matches every sealed component's API prefix", pat, inv[pat])
			}
			continue
		}
		firsts[seg] = true
	}
	if len(firsts) < 40 {
		t.Fatalf("only %d first segments in the route table", len(firsts))
	}
	for seg := range firsts {
		if nameRe.MatchString(seg) && server.SealedNameClash(seg, api, core) == "" {
			t.Errorf("a sealed component named %q would shadow /api/xbin/%s… and nothing refuses it", seg, seg)
		}
	}
	if !firsts["agent"] {
		t.Error("the route table has no /agent/… route: is xbin/agent free now? (the workspace agent is xbin/wsagent because it wasn't)")
	}
	for _, page := range []string{"limits", "devices", "partitions"} {
		if server.SealedNameClash(page, api, core) == "" {
			t.Errorf("a sealed component named %q would sit beside xbind's page /xbin/%s unrefused", page, page)
		}
	}
	names := []string{"wsagent"}
	for _, sp := range sealed.Table() {
		names = append(names, sp.Name)
	}
	for _, n := range names {
		if what := server.SealedNameClash(n, api, core); what != "" {
			t.Errorf("sealed component name %q shadows %s", n, what)
		}
	}
}
