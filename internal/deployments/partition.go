package deployments

// partition.go — tile deployments × partitions (plans/partitions/01 §2.8;
// PD-17): user partitions run only on the primary in v1, keyed by it, so
// making another deployment the primary would leave every person's data
// with the old one. A tile whose recorded partition mode has user
// partitions refuses POST /deployments/primary; promote moves code onto the
// primary instead. A tile that never asked for partitions is untouched.

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/registry"
)

// partitionedPrimary refuses reassigning the primary of c, whose recorded
// mode has user partitions — pending or declined included: the data of
// people's partitions stays keyed by the old primary either way.
func partitionedPrimary(c *registry.Component, primary string) error {
	if c == nil {
		return nil
	}
	if _, r, _ := c.PartitionState(); !r.User {
		return nil
	}
	return &Error{Status: http.StatusConflict, Kind: KindPolicy, Msg: c.Path + " is partitioned: switching the primary would leave every person's data with " +
		primary + ": promote instead"}
}
