package boot

// deploywire.go — the wiring of the broker's and the terminal manager's M2
// acts into the deployments plane (wave 2.3 onward): boot.go keeps one call
// per step. Every hook it installs answers a zero-state workspace exactly as
// today.

import (
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/term"
)

// wireDeploymentData installs the broker's per-deployment data and
// governance acts into the plane, and the plane's records into the broker's
// disk monitor (stepBroker).
func wireDeploymentData(dp *deployments.Plane, brk *broker.Broker) {
	// The disk monitor measures each namespace beyond main as its own quota
	// bucket, at its claimants' lowest limit, and each tile's deployment
	// state against the per-tile quota (08-data §12; D127n, D127q).
	brk.SetDeploymentQuota(dp.Lookup)
	// Per-deployment backups (08-data §11): the ops in ops_backup.go.
	dp.BackupData, dp.RestoreData = brk.BackupDeploymentData, brk.RestoreDeploymentData
	dp.SetBackupSchedule, dp.BackupScheduleOf = brk.SetDeploymentBackupSchedule, brk.DeploymentBackupSchedule
	dp.DataBackups = func(tile, dep string) (any, error) { return brk.DeploymentBackups(tile, dep) }
	// Partition modes (plans/partitions/01 §2.5, §2.7): a move onto the
	// primary warns when it will pause the tile; a switch logs a line.
	dp.PartitionHolds = brk.PartitionHoldsData
	dp.ProtectRequired = brk.ReviewedOnlyRequires // reviewed code only keeps primaries protected (06 §4)
	brk.SetPartitionDeployLog(dp.LogPartitionSwitch)
	// The governance acts' broker half (ops_gov.go): the edge check and the
	// restarts an edge change needs, a reassignment's consumer and ingress
	// fan-out, diskGiB's ceiling, and the seed.
	dp.SetGovHooks(func(h *deployments.GovHooks) {
		h.ValidateEdge, h.EdgeRestarts = brk.ValidateEdgePolicy, brk.EdgeChangeRestarts
		h.RoutesReassigned = brk.PrimaryReassigned
		h.DiskCeiling = brk.DiskQuota
		h.SeedData = func(p auth.Principal, req deployments.SeedRequest, authorize func(string) error, stop func(string, string)) (deployments.SeedFacts, error) {
			f, err := brk.SeedDeploymentData(p, req, authorize, stop)
			return deployments.SeedFacts(f), err
		}
	})
}

// wireDeploymentSessions installs the terminal manager's half of protect and
// reassignment (D127p): the sessions targeting the primary restart onto their
// new default, or end (stepTerminals).
func wireDeploymentSessions(dp *deployments.Plane, tm *term.Manager) {
	dp.SetGovHooks(func(h *deployments.GovHooks) {
		h.SessionsProtected, h.SessionsReassigned = tm.PrimaryProtected, tm.PrimaryReassigned
	})
}
