package boot

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/deployments"
)

// covers P13 P21 P22 P24 P28 — boot installs wave 2.3's hooks into the
// plane: the broker's per-deployment backups, its edge check and restarts,
// diskGiB's ceiling and the seed (the governance and data ops answer 501
// without them), the terminal manager's session moves on protect and
// reassignment, and the push plane's held notifications.
func TestDeploymentsWiringW23(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	dp := zsBoot(t, zsWorkspace(t)).st.Deployments
	var gov deployments.GovHooks
	dp.SetGovHooks(func(h *deployments.GovHooks) { gov = *h }) // reads them back, unchanged
	for name, set := range map[string]bool{
		"plane.BackupData": dp.BackupData != nil, "plane.DataBackups": dp.DataBackups != nil,
		"plane.RestoreData": dp.RestoreData != nil, "plane.SetBackupSchedule": dp.SetBackupSchedule != nil,
		"plane.BackupScheduleOf": dp.BackupScheduleOf != nil, "plane.WouldNotify": dp.WouldNotify != nil,
		"gov.ValidateEdge": gov.ValidateEdge != nil, "gov.EdgeRestarts": gov.EdgeRestarts != nil,
		"gov.DiskCeiling": gov.DiskCeiling != nil, "gov.SeedData": gov.SeedData != nil,
		"gov.SessionsProtected": gov.SessionsProtected != nil, "gov.SessionsReassigned": gov.SessionsReassigned != nil,
	} {
		if !set {
			t.Errorf("the hook %s is not installed", name)
		}
	}
	if gov.DiskCeiling != nil && gov.DiskCeiling() <= 0 {
		t.Errorf("diskGiB's ceiling is %d", gov.DiskCeiling())
	}
}

// covers P17 NP-11-18 — in origins mode each row of GET /deployments names
// its deployment's own origin (11-contract §2.6), which the frames' origin
// check reads; outside it (no origin) the key is absent.
func TestDeploymentRowsNameTheirOrigin(t *testing.T) {
	f := newDplFix(t)
	f.api.origin = func(tile, dep string) string { return "http://t-" + dep + ".tiles.test" }
	crm := f.get(t, dplWriter, "/deployments?tile=apps/crm", 200)
	for _, r := range crm["deployments"].([]any) {
		row := r.(map[string]any)
		if want := "http://t-" + row["name"].(string) + ".tiles.test"; row["origin"] != want {
			t.Errorf("%s's origin = %v, want %s", row["name"], row["origin"], want)
		}
	}
	f.api.origin = func(string, string) string { return "" }
	for _, r := range f.get(t, dplWriter, "/deployments?tile=apps/crm", 200)["deployments"].([]any) {
		if o, ok := r.(map[string]any)["origin"]; ok {
			t.Errorf("an origin %v outside origins mode", o)
		}
	}
}
