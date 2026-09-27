package boot

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/deployments"
)

// covers P21 P22 P24 P28 — boot installs wave 2.3's hooks into the plane: the
// broker's per-deployment backups, its edge check and restarts, diskGiB's
// ceiling and the seed (the governance and data ops answer 501 without
// them), and the terminal manager's session moves on protect and
// reassignment.
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
		"plane.BackupScheduleOf": dp.BackupScheduleOf != nil,
		"gov.ValidateEdge":       gov.ValidateEdge != nil, "gov.EdgeRestarts": gov.EdgeRestarts != nil,
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
