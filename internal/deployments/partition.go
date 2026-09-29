package deployments

// partition.go — tile deployments × partitions (plans/partitions/01 §2.7,
// §2.8; PD-17): user partitions run only on the primary in v1, keyed by it,
// so making another deployment the primary would leave every person's data
// with the old one. A tile whose recorded partition mode has user
// partitions refuses POST /deployments/primary; promote moves code onto the
// primary instead. A move onto the primary of code asking for another mode
// is a switch request like an edit: its dry run warns (Impact.partition). A
// tile manager's switch writes a deploy-log line. A tile that never asked
// for partitions is untouched.

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// partitionedPrimary refuses reassigning the primary of c, whose recorded
// mode has user partitions — pending or declined included: the data of
// people's partitions stays keyed by the old primary either way.
func partitionedPrimary(c *registry.Component, primary string) error {
	if c == nil {
		return nil
	}
	if _, r, _ := c.PartitionState(); !r.User && !c.PartitionRecordUnknown() {
		return nil // a record that can't be read may be partitioned: refused too
	}
	return &Error{Status: http.StatusConflict, Kind: KindPolicy, Msg: c.Path + " is partitioned: switching the primary would leave every person's data with " +
		primary + ": promote instead"}
}

// partitionPreflight is a move's dry-run warning (plans/partitions/01 §2.7):
// tree, put on o's primary, asks for another partition mode than the one
// the tile records. On a tile that holds data that pauses it for a tile
// manager's decision (switch, deleting all its data, or keep); on one that
// holds none the mode follows at once. "" when the move changes nothing of
// the mode: another deployment, the same request, or a tile without a
// registry entry.
func (p *Plane) partitionPreflight(o *op, dep, tree string) string {
	if o.c == nil || dep != o.rec.Primary {
		return ""
	}
	pc, err := p.readCode(o.tile, tree)
	if err != nil || pc.ManifestErr != "" {
		return "" // a checkpoint that can't start is refused on its own
	}
	q, verr := registry.ValidatePartition(pc.Manifest)
	_, r, req := o.c.PartitionState()
	switch {
	case verr != nil:
		return fmt.Sprintf("%s will pause: its code at this checkpoint asks for an invalid partition mode (%v)", o.tile, verr)
	case registry.SpecOf(q) == r:
		return ""
	case req != nil && req.Declined && registry.SpecOf(req.Spec) == registry.SpecOf(q):
		return fmt.Sprintf("its code at this checkpoint asks for partition mode %s, which a manager of %s declined: it keeps running %s", registry.SpecOf(q), o.tile, r)
	case p.PartitionHolds != nil && !p.PartitionHolds(o.tile):
		return fmt.Sprintf("its code at this checkpoint asks for partition mode %s (%s runs %s); it holds no data, so the mode follows at once", registry.SpecOf(q), o.tile, r)
	}
	return fmt.Sprintf("%s will pause for a partition-mode decision: its code at this checkpoint asks for %s, it runs %s and holds data — a tile manager must switch (deleting all its data) or keep the current mode",
		o.tile, registry.SpecOf(q), r)
}

// howPartitionSwitch is the deploy-log entry of a partition mode switch.
const howPartitionSwitch = "partition-switch"

// LogPartitionSwitch writes a line in tile's deploy log for a tile
// manager's partition mode switch (plans/partitions/01 §2.5 step 4): an
// entry on the primary, how partition-switch, by who decided, with no
// checkpoint. A tile without a deployment record has no deploy log; the
// mode record's history and the audit line keep the switch there.
func (p *Plane) LogPartitionSwitch(tile, by, via string) error {
	rec, err := p.record(tile)
	if err != nil || rec == nil {
		return err
	}
	a := &attempt{Deployment: rec.Primary, How: howPartitionSwitch, By: cmp.Or(by, "owner"), Via: via}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := p.accept(ctx, tile, rec, a); err != nil {
		return err
	}
	p.finish(a, resultOK, nil)
	return nil
}
