package server

// partitionrow.go — the partition members of a /components row
// (plans/partitions/01 §Wire; docs/protocol.md): present only for a tile
// whose code asks for a partition mode, or whose recorded mode is
// partitioned or has a request open or declined. Every other row is
// byte-identical to one from before partitions.

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/registry"
)

// partitionInfo is a row's "partition": the settled state, the recorded mode
// (user, global) and, when the code asks for another, the request — and,
// while that request waits for a manager, the tile's partitionNote (its
// code's own words, sandbox-writable, as the in-frame switch page shows them:
// a client shows it as text, attributed to the tile).
type partitionInfo struct {
	State   string            `json:"state"` // partitioned | unpartitioned | pending | invalid
	User    bool              `json:"user"`
	Global  bool              `json:"global"`
	Request *partitionRequest `json:"request,omitempty"`
	Note    string            `json:"note,omitempty"`
}

type partitionRequest struct {
	User     bool `json:"user"`
	Global   bool `json:"global"`
	Declined bool `json:"declined"`
}

// partitionOf fills ci's partition members from c.
func partitionOf(ci *componentInfo, c *registry.Component) {
	if !c.PartitionShown() {
		return
	}
	st, r, req := c.PartitionState()
	ci.Partition = &partitionInfo{State: st.String(), User: r.User, Global: r.Global}
	if req != nil {
		q := registry.SpecOf(req.Spec)
		ci.Partition.Request = &partitionRequest{User: q.User, Global: q.Global, Declined: req.Declined}
		if st == registry.PartitionPending {
			ci.Partition.Note = strings.TrimSpace(c.Manifest.PartitionNote)
		}
	}
	ci.PartitionErr = c.PartitionErr
}
