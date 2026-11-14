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

// TestSealedRouteClasses is the guard of internal/server/sealedclass.go
// (D227): every /api/xbin route xbind mounts has a row saying what a
// sealed component's path gets on it, every row names a route that exists,
// and a row that refuses or reserves says where the route names a tile.
// A new route can't be added without deciding it. The answers themselves
// are driven through a real daemon by internal/boot's
// TestSealedRouteAnswers.
func TestSealedRouteClasses(t *testing.T) {
	inv := inventory(t)
	table := server.SealedRules()
	var problems []string
	for p, where := range inv {
		if table[p].Class == server.SealedUnclassified {
			problems = append(problems, where+" registers "+p+", which has no row in internal/server/sealedclass.go: "+
				"say what a sealed component's path gets on it (no-tile when it names no tile)")
		}
	}
	for p, r := range table {
		if _, ok := inv[p]; !ok {
			problems = append(problems, "internal/server/sealedclass.go has a row for "+p+", which no code registers: drop it")
		}
		named := len(r.Query) > 0 || len(r.Body) > 0 || r.Rest
		switch r.Class {
		case server.SealedRefused, server.SealedSelf:
			if !named {
				problems = append(problems, p+" is "+r.Class.String()+" but names no parameter that carries the tile: the gate would never refuse")
			}
			if r.What == "" {
				problems = append(problems, p+" is "+r.Class.String()+" without a reason (What)")
			}
			if r.Rest && !strings.Contains(p, "{rest...}") && !strings.Contains(p, "{path...}") {
				problems = append(problems, p+" reads the tile from the rest of its path, and has no rest wildcard")
			}
		case server.SealedHandled:
			if r.What == "" {
				problems = append(problems, p+" is handled without saying what the handler's rule is (What)")
			}
		case server.SealedNoTile:
			if named {
				problems = append(problems, p+" names a tile parameter and is classed no-tile")
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
