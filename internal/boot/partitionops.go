package boot

// partitionops.go — the operations on people's partitions
// (plans/partitions/06 §5-§10) as boot wires them: the runner's instance
// rows and a person's stops for the broker's partitions API and people
// hooks, the deployments plane's live-reload answer for the trust
// warnings, the users store's invite gate for held credentials, a users
// event stopping instances whose person is no longer live, and the 24 h
// rule's clock.

import (
	"context"
	"time"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/runner"
)

// credentialLapseEvery is how often held credentials whose 24 h passed are
// made to take effect.
const credentialLapseEvery = 5 * time.Minute

// wirePartitionOps installs the operations' side of the runner, the auth
// plane, the deployments plane and the users store in the broker.
func (st *State) wirePartitionOps() {
	run, brk := st.Run, st.Broker
	brk.SetPartitionOps(func() []broker.PartitionInstance { return partitionInstances(run.InspectPartitions()) },
		run.StopPartitionsOf, st.Auth.RevokeUserPartitionInstances)
	if dp := st.Deployments; dp != nil {
		brk.SetPartitionLiveReload(func(tile string) bool {
			dep, attached := dp.LiveReload(tile)
			return attached && dep == dp.Primary(tile)
		})
	}
	brk.InstallCredentialGate()
	if st.Hub != nil {
		go st.onUsersEvents(brk.PartitionPeopleChanged) // disabled, lost read: their instances stop (PD-20)
	}
}

// partitionInstances are the runner's partition rows as the broker takes
// them: metadata, never content.
func partitionInstances(bs []runner.Backend) []broker.PartitionInstance {
	out := make([]broker.PartitionInstance, 0, len(bs))
	for _, b := range bs {
		out = append(out, broker.PartitionInstance{Tile: b.Path, Dep: b.Deployment, Partition: b.Partition, State: b.State,
			Gen: b.Gen, UptimeSec: b.UptimeSec, RSSKB: b.RSSKB, Restarts: b.Restarts, Error: b.Error})
	}
	return out
}

// lapseHeldCredentials runs the 24 h rule until ctx is done (06 §9).
func (st *State) lapseHeldCredentials(ctx context.Context) {
	t := time.NewTicker(credentialLapseEvery)
	defer t.Stop()
	st.Broker.LapseHeldCredentials() // those whose 24 h passed while xbind was down
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st.Broker.LapseHeldCredentials()
		}
	}
}
