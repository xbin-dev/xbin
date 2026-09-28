package deployments

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers D127i D127t NP-08-7 — add and remove reach the broker's data namespace
// plane through the plane's DataHooks (08-data §6.2, §9.2): an add asks
// JoinData as a manager and gates the actor itself, so joining seeded data
// is refused to a terminal-level user (403) and a tile manager's add, dry or
// not, carries the namespace it joins; a remove asks DropData for its
// deployment, not dry, after the drops.
func TestCodeOpsReachTheNamespacePlane(t *testing.T) {
	f := newCodeFx(t, false)
	f.p.MayManage = func(pr auth.Principal, tile string) bool { return pr.Component == "" && pr.IsAdmin() }
	seeded := &Joins{Scope: "apps/shop", State: "seeded", By: "user:ana", At: "2026-09-27T10:00:00Z"}
	var asked []string
	f.p.JoinData = func(tile, dep string, manager bool) (*Joins, error) {
		asked = append(asked, tile+"+"+dep+" manager="+boolStr(manager))
		return seeded, nil
	}
	f.p.DropData = func(tile, dep string, dry bool) (bool, error) {
		f.note("drop-data " + tile + "/" + dep + " dry=" + boolStr(dry))
		return true, nil
	}
	term := userP("dev", opSite, users.LevelTerminal)
	_, err := f.do(term, OpAdd, &AddRequest{Tile: opSite, Deployment: "dev"})
	wantErr(t, "a terminal-level add joining seeded data", err, http.StatusForbidden, `apps/shop's "dev" data was seeded by user:ana`)
	if im := f.dry(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", DryRun: true}); !reflect.DeepEqual(im.Joins, seeded) {
		t.Errorf("the owner's dry add joins %+v", im.Joins)
	}
	if ans := f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"}); !reflect.DeepEqual(ans.Joins, seeded) {
		t.Errorf("the owner's add joins %+v", ans.Joins)
	}
	for _, a := range asked {
		if a != opSite+"+dev manager=true" {
			t.Errorf("JoinData asked %q: the plane judges the actor itself", a)
		}
	}
	f.took()
	f.must(ownerP, OpRemove, &RemoveRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmErase})
	got := f.took()
	if len(got) == 0 || got[len(got)-1] != "drop-data "+opSite+"/dev dry=false" {
		t.Errorf("remove called %v, want the namespace dropped last", got)
	}
}
