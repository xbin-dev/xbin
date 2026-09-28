package broker

import (
	"cmp"
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D127k D127s T14 — the governance and code-read gates ask the edge
// policy's principal-aware helpers: apps/console holds xbin at admin, yet
// its non-primary deployment's principal manages no users, creates no
// component, passes no workspace-management gate and counts as no
// xbin-capable element, while its primary's does all four; apps/email's
// non-primary deployment reads apps/calendar's source and PRs through its
// code grant while the edge reads, and not once grant:code:apps/calendar is
// block, while its primary still does.
func TestGatesAskTheEdgePolicy(t *testing.T) {
	b := newEdgeBroker(t)
	edgePlane(t, b, edgeRec{tile: fxConsole, primary: util.MainDeployment}, edgeRec{tile: fxEmail, primary: util.MainDeployment})
	for _, c := range []struct {
		p    auth.Principal
		want bool
	}{{edgeMain(fxConsole), true}, {edgeDev(fxConsole), false}} {
		ok, _ := b.canCreateAt(c.p, "apps/newtile", "")
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/xbin/x", nil).WithContext(auth.WithPrincipal(t.Context(), c.p))
		if got := [4]bool{b.canManageUsers(c.p), ok, b.requireWriter(w, r), b.elementXbinCapable(c.p)}; got != [4]bool{c.want, c.want, c.want, c.want} {
			t.Errorf("%s+%s: manages users, creates, writes, xbin-capable = %v; want all %v", c.p.Component, cmp.Or(c.p.Deployment, util.MainDeployment), got, c.want)
		}
	}
	for _, c := range []struct {
		edges map[string]string
		dev   bool
	}{{nil, true}, {map[string]string{"grant:code:apps/calendar": "block"}, false}} {
		edgePlane(t, b, edgeRec{tile: fxEmail, primary: util.MainDeployment, edges: c.edges})
		for p, want := range map[auth.Principal]bool{edgeMain(fxEmail): true, edgeDev(fxEmail): c.dev} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/api/xbin/code/x", nil).WithContext(auth.WithPrincipal(t.Context(), p))
			if got := b.requireCodeRead(w, r, fxCalendar); got != want || b.canReadPRs(p, fxCalendar) != want {
				t.Errorf("edges %v: %s+%s reads apps/calendar's code %v, PRs %v; want %v", c.edges, p.Component, cmp.Or(p.Deployment, util.MainDeployment), got, b.canReadPRs(p, fxCalendar), want)
			}
		}
	}
}
