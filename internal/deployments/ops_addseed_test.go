package deployments

import (
	"net/http"
	"slices"
	"testing"
)

// covers D127i D127t T8 — add with data:"seed" adds the deployment and then
// seeds its data through the broker's seed, judged at the seed's row on the
// other claimants: a dry run says the data is seeded and seeds nothing; a
// workspace-scope tile, or an xbind without the seed, is refused before
// anything is added; a seed the broker refuses once the deployment is added
// leaves it added and says so.
func TestAddSeeded(t *testing.T) {
	f := newGovFx(t, false)
	f.write("apps/shop/scope.json", "{}")
	f.write("apps/shop/xbin.json", "{}")
	f.write("apps/shop/index.html", "<h1>shop</h1>")
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	add := func(dep string, dry bool) (any, error) {
		t.Helper()
		return f.do(ownerP, OpAdd, &AddRequest{Tile: "apps/shop", Deployment: dep, Data: DataSeed, Confirm: ConfirmCopyData, DryRun: dry})
	}

	if im := f.dry(ownerP, OpAdd, &AddRequest{Tile: "apps/shop", Deployment: "dev", Data: DataSeed, Confirm: ConfirmCopyData, DryRun: true}); im.Data != "seed" {
		t.Errorf("dry run = %+v", im)
	}
	if calls := f.took(); slices.ContainsFunc(calls, func(c string) bool { return len(c) > 4 && c[:5] == "seed " }) {
		t.Errorf("a dry run seeded: %q", calls)
	}

	res, err := add("dev", false)
	if _, ok := res.(AddAnswer); err != nil || !ok {
		t.Fatalf("seeded add = %#v, %v", res, err)
	}
	if f.rec("apps/shop").Deployments["dev"] == nil {
		t.Fatal("dev wasn't added")
	}
	if calls := f.took(); !slices.Contains(calls, "seed apps/shop/dev by=owner flag=false dry=false") {
		t.Errorf("calls = %q", calls)
	}

	f.seedErr = &Error{Status: http.StatusConflict, Kind: KindState, Msg: "the workspace disk is low"}
	_, err = add("exp", false)
	wantErr(t, "a refused seed", err, http.StatusConflict, "exp was added with empty data, but its seed was refused: the workspace disk is low")
	if f.rec("apps/shop").Deployments["exp"] == nil {
		t.Error("exp isn't there after its refused seed")
	}
	f.seedErr = nil

	_, err = f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", Data: DataSeed, Confirm: ConfirmCopyData})
	wantErr(t, "a workspace-scope tile", err, http.StatusConflict, "apps/site is in the workspace scope")
	f.p.SetGovHooks(func(h *GovHooks) { h.SeedData = nil })
	_, err = add("late", false)
	wantErr(t, "without the seed", err, http.StatusNotImplemented, "adding a seeded deployment isn't built")
	if r := f.rec(opSite); r != nil && r.Seq != 0 || f.rec("apps/shop").Deployments["late"] != nil {
		t.Error("a refusal added a deployment")
	}
}
