package deployments

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// govMatrixFx is a static tile whose code declares alwaysOn, main following
// the work tree and two pinned deployments, dev and exp; protected protects
// main. Its manager gate admits admins and mia.
func govMatrixFx(t *testing.T, protected bool) *govFx {
	t.Helper()
	f := newGovFx(t, false)
	f.p.MayManage = func(pr auth.Principal, tile string) bool {
		return pr.Component == "" && (pr.IsAdmin() || pr.UserID == "mia" && tile == opSite)
	}
	f.write(opSite+"/xbin.json", `{"alwaysOn":true}`)
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	f.write(opSite+"/index.html", "<h1>exp</h1>")
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "exp"})
	if protected {
		protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true)}))
	}
	f.took()
	f.evs.take()
	return f
}

// covers P4 P11 P21 P26 T9 C4 — the authority matrix of the governance and
// data acts through the dispatcher and the acts themselves, one subtest per
// cell: reassigning and protecting the primary, unprotecting it, an edge's
// policy, deliveries, alwaysOn, limits, seeding and copying vault values
// pass only tile managers in a person's own session — every tile credential
// (a manager's terminal token, the tile's frame and instance tokens, other
// tiles' and xbin-granted tiles' credentials) answers the human-session 403
// and every other person the manager's 403 — while resetting a non-primary
// deployment's data and running its job now are terminal level. A refusal
// changes nothing and calls no plane.
func TestDeployAuthzMatrixGov(t *testing.T) {
	manager := func(w codeWho) string {
		switch {
		case w.group == "manager":
			return azOK
		case w.p.Component != "":
			return azSession
		}
		return azManager
	}
	terminal := func(w codeWho) string {
		return map[string]string{"manager": azOK, "term": azOK, "token": azOK, "person": azTerminal, "cred": azCredential}[w.group]
	}
	cells := []struct {
		name      string
		protected bool
		op        Op
		req       any
		want      func(codeWho) string
	}{
		{"reassign", false, OpPrimary, &PrimaryRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmDataStays}, manager},
		{"protect", false, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true)}, manager},
		{"unprotect", true, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(false)}, manager},
		{"edge policy", false, OpEdge, &EdgeRequest{Tile: opSite, Edge: "grant:apps/leads", Policy: EdgeBlock}, manager},
		{"deliveries", false, OpDeliveries, &SwitchRequest{Tile: opSite, Deployment: "dev", On: ptr(false)}, manager},
		{"alwaysOn", false, OpAlwaysOn, &SwitchRequest{Tile: opSite, Deployment: "dev", On: ptr(true)}, manager},
		{"limits", false, OpLimits, &LimitsRequest{Tile: opSite, Deployment: "dev", Limits: LimitsPatch{Pids: Override{Set: true, Value: ptr(int64(64))}}}, manager},
		{"seed", false, OpSeed, &SeedRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmCopyData}, manager},
		{"vault copy", false, OpVaultCopy, &VaultCopyRequest{Tile: opSite, Deployment: "dev", All: true}, manager},
		{"reset", false, OpReset, &ResetRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmEraseData}, terminal},
		{"run now", true, OpRunNow, &RunNowRequest{Tile: opSite, Deployment: "exp", Job: "nightly"}, terminal},
	}
	for _, c := range cells {
		for _, w := range codePrincipals() {
			t.Run(c.name+"/"+w.name, func(t *testing.T) {
				f := govMatrixFx(t, c.protected)
				before := f.rec(opSite).Seq
				_, err := f.do(w.p, c.op, c.req)
				if want := c.want(w); want != azOK {
					if bad := azCheckCode(err, want, opSite); bad != "" {
						t.Fatalf("want the %s refusal: %s", want, bad)
					}
					if f.rec(opSite).Seq != before || len(f.took()) != 0 {
						t.Error("a refusal changed the record or called a plane")
					}
					return
				}
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if f.rec(opSite).Seq == before && len(f.took()) == 0 {
					t.Error("passed and did nothing")
				}
			})
		}
	}
}
