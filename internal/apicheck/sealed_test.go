package apicheck

import (
	"os"
	"path/filepath"
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

// The sealed table, pinned: what a path under xbin/ gets on every route. A
// route that moves from refused or self to another class, or loses a
// parameter the gate reads, shows up here as a diff to be decided, not as
// a quiet edit of the table. Regenerate with
// UPDATE_SEALED_CLASSES=1 go test ./internal/apicheck -run TestSealedRouteClassesGolden
func TestSealedRouteClassesGolden(t *testing.T) {
	rules := server.SealedRules()
	routes := make([]string, 0, len(rules))
	for p := range rules {
		routes = append(routes, p)
	}
	sort.Strings(routes)
	var b strings.Builder
	b.WriteString("# What a path under xbin/ gets on each /api/xbin route (internal/server/sealedclass.go).\n" +
		"# route<TAB>class[<TAB>where the route names a tile]. Regenerate:\n" +
		"# UPDATE_SEALED_CLASSES=1 go test ./internal/apicheck -run TestSealedRouteClassesGolden\n")
	count := map[string]int{}
	for _, p := range routes {
		r := rules[p]
		count[r.Class.String()]++
		b.WriteString(p + "\t" + r.Class.String())
		var where []string
		if r.Rest {
			where = append(where, "path")
		}
		for _, q := range r.Query {
			where = append(where, "?"+q)
		}
		for _, f := range r.Body {
			where = append(where, "body."+f)
		}
		if len(where) > 0 {
			b.WriteString("\t" + strings.Join(where, " "))
		}
		b.WriteString("\n")
	}
	want := b.String()
	golden := filepath.Join("testdata", "sealed-classes.golden")
	if os.Getenv("UPDATE_SEALED_CLASSES") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", golden)
		return
	}
	got, _ := os.ReadFile(golden)
	if string(got) == want {
		return
	}
	was := map[string]string{}
	for _, l := range strings.Split(string(got), "\n") {
		if route, rest, ok := strings.Cut(l, "\t"); ok && !strings.HasPrefix(l, "#") {
			was[route] = rest
		}
	}
	for _, l := range strings.Split(want, "\n") {
		route, rest, ok := strings.Cut(l, "\t")
		if !ok || strings.HasPrefix(l, "#") {
			continue
		}
		if old, known := was[route]; !known {
			t.Errorf("%s is classed %q and isn't in %s", route, rest, golden)
		} else if old != rest {
			t.Errorf("%s: %q in %s, now %q", route, old, golden, rest)
		}
		delete(was, route)
	}
	for route := range was {
		t.Errorf("%s is in %s and has no row any more", route, golden)
	}
	t.Errorf("the sealed table differs from %s (%d routes: %v): what a system component's path gets on a route changed. If that is the decision, say so in the change and regenerate (UPDATE_SEALED_CLASSES=1)", golden, len(routes), count)
}
