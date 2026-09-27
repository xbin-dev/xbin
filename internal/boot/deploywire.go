package boot

// deploywire.go — stepBroker's wiring of the broker's M2 data acts into the
// deployments plane (wave 2.3 onward): boot.go keeps one call. Every hook it
// installs answers a zero-state workspace exactly as today.

import (
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/deployments"
)

// wireDeploymentData installs the broker's per-deployment data acts into the
// plane, and the plane's records into the broker's disk monitor.
func wireDeploymentData(dp *deployments.Plane, brk *broker.Broker) {
	// The disk monitor measures each namespace beyond main as its own quota
	// bucket, at its claimants' lowest limit, and each tile's deployment
	// state against the per-tile quota (08-data §12; P22, P25).
	brk.SetDeploymentQuota(dp.Lookup)
}
