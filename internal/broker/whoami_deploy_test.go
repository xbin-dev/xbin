package broker

// whoami_deploy_test.go — whoami's deployment (11-contract §7.7).

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D127j D127g D119c — whoami reports deployment for one of a tile's own
// credentials bound to a deployment other than the tile's primary (the
// role rule): a frame token's claim, an instance token's deployment, a
// terminal session's named target, main's credentials once another
// deployment is the primary, and a deployment since removed. The primary's
// credentials, a terminal that follows the primary (even a protected one,
// bound to nothing), xbind's cron principal, people and every credential of
// a tile without a record answer as before: no deployment key.
func TestWhoamiDeployment(t *testing.T) {
	b := testBroker(t)
	primary := map[string]string{"apps/crm": "main"} // tiles with a record → their primary
	deps := map[string][]string{"apps/crm": {"main", "dev"}}
	protected := false
	b.DeploymentAnswers.PrimaryOf = func(tile string) string {
		if p, ok := primary[tile]; ok {
			return p
		}
		return util.MainDeployment
	}
	// Addressed as the plane answers it (deployments.Plane.Addressed).
	b.DeploymentAnswers.AddressedDeployment = func(p auth.Principal, tile string) (string, error) {
		prim := b.DeploymentAnswers.PrimaryOf(tile)
		if p.Component != tile {
			return prim, nil
		}
		session, dep := p.Via == "terminal", p.Deployment
		switch {
		case dep == "" && !session:
			return util.MainDeployment, nil
		case dep == "":
			dep = prim
		default:
			found := dep == util.MainDeployment && deps[tile] == nil
			for _, n := range deps[tile] {
				found = found || n == dep
			}
			if !found {
				return "", util.NoDeployment(tile, dep)
			}
		}
		if session && protected && dep == prim {
			return "", fmt.Errorf("the primary of %s is protected: terminal and agent sessions can't target it", tile)
		}
		return dep, nil
	}
	whoami := func(p auth.Principal) map[string]any {
		r := httptest.NewRequest("GET", "/whoami", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiWhoami(w, r)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("whoami: %v: %s", err, w.Body)
		}
		return out
	}
	el := func(tile, via, dep string) auth.Principal {
		return auth.Principal{Component: tile, Via: via, Deployment: dep}
	}
	check := func(label string, p auth.Principal, want string) {
		t.Helper()
		got, ok := whoami(p)["deployment"]
		switch {
		case want == "" && ok:
			t.Errorf("%s: deployment %v, want none", label, got)
		case want != "" && got != want:
			t.Errorf("%s: deployment %v, want %s", label, got, want)
		}
	}

	check("dev's frame", el("apps/crm", "frame", "dev"), "dev")
	check("dev's instance", el("apps/crm", "instance", "dev"), "dev")
	check("a dev terminal", el("apps/crm", "terminal", "dev"), "dev")
	check("main's frame", el("apps/crm", "frame", ""), "")
	check("main's instance", el("apps/crm", "instance", ""), "")
	check("a main terminal", el("apps/crm", "terminal", "main"), "")
	check("a following terminal", el("apps/crm", "terminal", ""), "")
	check("a zero-state tile's frame", el("apps/x", "frame", ""), "")
	check("a zero-state tile's terminal", el("apps/x", "terminal", ""), "")
	check("the cron principal", auth.Principal{Component: CronPrincipal, Via: "cron", Deployment: "dev"}, "")
	check("the owner", auth.Principal{Owner: true}, "")
	check("a person", auth.Principal{UserID: "ann", User: &users.User{ID: "ann", Role: "user"}}, "")

	primary["apps/crm"] = "dev"
	check("main's frame, dev primary", el("apps/crm", "frame", ""), "main")
	check("a main terminal, dev primary", el("apps/crm", "terminal", "main"), "main")
	check("dev's frame, dev primary", el("apps/crm", "frame", "dev"), "")
	check("a following terminal, dev primary", el("apps/crm", "terminal", ""), "")

	primary["apps/crm"], protected = "main", true
	check("a following terminal, protected primary", el("apps/crm", "terminal", ""), "")
	deps["apps/crm"] = []string{"main"}
	check("dev's frame, dev removed", el("apps/crm", "frame", "dev"), "dev")
}
