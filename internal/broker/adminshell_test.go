package broker

import (
	"encoding/json"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// covers D226 — who gets which admin notices from GET /alerts: AdminAlerts
// reach admins (the owner token, an admin's session, the admin console);
// AdminShellAlerts reach the shell an admin drives — a trusted chrome frame
// minted under that admin's own login — and nobody else: not another
// person's shell, not an ordinary tile's frame whoever drives it, not a
// terminal, not a view-as session. AdminShell is not IsAdmin.
func TestAdminShellAlerts(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	fx.b.AdminAlerts = func() []Alert { return []Alert{{Level: "warn", Kind: "for-admins", Message: "a"}} }
	fx.b.AdminShellAlerts = func() []Alert { return []Alert{{Level: "warn", Kind: "for-shells", Message: "s"}} }
	kinds := func(p auth.Principal) map[string]bool {
		w := call(t, fx.b.apiAlerts, p, "GET", "/alerts", "", nil)
		var out struct{ Alerts []Alert }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("alerts: %d %s", w.Code, w.Body)
		}
		got := map[string]bool{}
		for _, a := range out.Alerts {
			got[a.Kind] = true
		}
		return got
	}
	ps["adminFrame"] = auth.Principal{Component: "apps/email", Via: "frame", UserID: "root2", Gen: "s.1"} // an ordinary tile an admin has open
	ps["adminTerm"] = auth.Principal{Component: "shell", Via: "terminal", UserID: "root2", Gen: "u.root2"}
	ps["ownerShell"] = auth.Principal{Component: "shell", Via: "frame", Gen: "o.1"} // the root token's login
	for who, want := range map[string][2]bool{                                      // {AdminAlerts, AdminShellAlerts}
		"root": {true, false}, "root2": {true, false}, "consoleA": {true, false},
		"shellA": {false, true}, "ownerShell": {false, true},
		"shellC": {false, false}, "carol": {false, false}, "frame": {false, false}, "adminFrame": {false, false},
		"adminTerm": {false, false}, "instance": {false, false}, "viewAs": {false, false},
	} {
		got := kinds(ps[who])
		if got["for-admins"] != want[0] || got["for-shells"] != want[1] {
			t.Errorf("%s: admins' %v, shells' %v; want %v", who, got["for-admins"], got["for-shells"], want)
		}
		if shell := fx.b.AdminShell(ps[who]); shell != want[1] {
			t.Errorf("AdminShell(%s) = %v, want %v", who, shell, want[1])
		}
	}
	if fx.b.IsAdmin(ps["shellA"]) {
		t.Error("an admin's shell frame is an admin: its token would manage the workspace")
	}
	// no source: nothing changes for anyone
	fx.b.AdminShellAlerts = nil
	if got := kinds(ps["shellA"]); got["for-shells"] || got["for-admins"] {
		t.Errorf("without a source: %v", got)
	}
}
