package term

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers P5 P21 P24 — a session's target (11-contract §7.4): without a
// record, or without the hook, every session follows the primary (main), a
// request naming main follows it too, "no API" mints nothing, the dropdown
// offers main alone and no XBIN_DEPLOYMENT is set: today's session. With a
// record: a named deployment is kept; an unknown one is a 404; a protected
// primary is never offered, a request naming it is refused, and the
// default falls to the live reload target, then to API off.
func TestChooseTargetZeroStateAndP24(t *testing.T) {
	const tile = "apps/crm"
	var hook TileDeploymentsFunc
	zero := hook.deploymentsOf(tile)
	if !reflect.DeepEqual(zero, zeroDeployments()) || zero.Record || zero.Primary != util.MainDeployment {
		t.Fatalf("a nil hook answers %+v", zero)
	}
	for _, requested := range []string{"", util.MainDeployment} {
		tg, err := ChooseTarget(tile, zero, true, requested)
		if err != nil || tg != (Target{}) || tg.DeploymentEnv(zero) != "" {
			t.Errorf("zero state, requested %q: %+v, %v", requested, tg, err)
		}
	}
	if tg, err := ChooseTarget(tile, zero, false, ""); err != nil || !tg.NoAPI {
		t.Errorf("zero state, no API: %+v, %v", tg, err)
	}
	if _, err := ChooseTarget(tile, zero, true, "dev"); !errors.Is(err, util.ErrNoDeployment) {
		t.Errorf("zero state, requested dev: %v; want no such deployment", err)
	}
	if got := Entries(zero); !reflect.DeepEqual(got, []string{"main"}) {
		t.Errorf("zero state entries = %v", got)
	}

	open := TileDeployments{Record: true, Primary: "main", LiveReload: "dev", Names: []string{"main", "dev", "qa"}}
	for requested, want := range map[string]Target{"": {}, "main": {}, "dev": {Deployment: "dev"}, "qa": {Deployment: "qa"}} {
		if tg, err := ChooseTarget(tile, open, true, requested); err != nil || tg != want {
			t.Errorf("unprotected, requested %q: %+v, %v; want %+v", requested, tg, err, want)
		}
	}
	if env := (Target{Deployment: "dev"}).DeploymentEnv(open); env != "dev" {
		t.Errorf("a session targeting dev gets XBIN_DEPLOYMENT=%q", env)
	}

	prot := open
	prot.Protected = true
	if got := Entries(prot); !reflect.DeepEqual(got, []string{"dev", "qa"}) {
		t.Errorf("protected entries = %v; a protected primary is never offered", got)
	}
	if tg, err := ChooseTarget(tile, prot, true, ""); err != nil || tg != (Target{Deployment: "dev"}) {
		t.Errorf("protected, default: %+v, %v; want the live reload target", tg, err)
	}
	if _, err := ChooseTarget(tile, prot, true, "main"); err == nil || errors.Is(err, util.ErrNoDeployment) ||
		!strings.Contains(err.Error(), "protected") {
		t.Errorf("protected, requested main: %v; want the protection refusal", err)
	}
	paused := prot
	paused.LiveReload = ""
	if tg, err := ChooseTarget(tile, paused, true, ""); err != nil || !tg.NoAPI {
		t.Errorf("protected and paused, default: %+v, %v; want API off", tg, err)
	}
}
