package broker

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers P13 NP-09-18 SC-DORMANT — main beside a primary that isn't main
// registers a cron job on a foreign resource its tile holds writer on: the
// edge policy's read clamp doesn't refuse it, since a job only ever
// schedules the deployment's own handler (09-fabric §6); it is stored
// dormant, like any non-primary deployment's registration.
func TestMainBesideAPrimaryStoresForeignCronDormant(t *testing.T) {
	f := newDormantFx(t, false)
	b := f.b
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: fxShop, Target: "res:apps/calendar/ticks", Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{"name": "f", "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": "/f"}
	if out := mustCode(t, regCall(t, b.apiCronPut, shopMain, "PUT", "/cron/jobs", "", job), 200, "shop main's foreign job"); out["dormant"] != true {
		t.Errorf("main beside a dev primary: answer %v, want dormant", out)
	}
}
