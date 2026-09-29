package proxy

// partition.go — the partition gate (plans/partitions/01 §2.3; PD-50): while
// a tile's partition mode switch is pending, or its request is invalid, no
// instance of its primary runs. A call that would reach the primary answers
// 409 with why and the state, and never reaches the runner. Non-primary
// deployments keep D127's behaviour; ingress answers today's 503.

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/xbin-dev/xbin/internal/registry"
)

// partitionBody is the "partition" member of the 409.
type partitionBody struct {
	State string                  `json:"state"`
	From  *registry.PartitionSpec `json:"from,omitempty"`
	To    *registry.PartitionSpec `json:"to,omitempty"`
	Error string                  `json:"error,omitempty"`
}

// PartitionPaused answers why comp's primary doesn't run because of its
// partition mode — the error text and the "partition" member of the 409 —
// or ok false when it may run.
func PartitionPaused(comp *registry.Component) (msg string, body any, ok bool) {
	st, r, req := comp.PartitionState()
	switch st {
	case registry.PartitionPending:
		to := registry.PartitionSpec{}
		if req != nil {
			to = registry.SpecOf(req.Spec)
		}
		return fmt.Sprintf("%s is paused: a partition mode switch is requested (%s → %s); a manager of %s must switch (deleting all its data) or keep the current mode",
			comp.Path, r, to, comp.Path), partitionBody{State: st.String(), From: &r, To: &to}, true
	case registry.PartitionInvalid:
		return fmt.Sprintf("%s doesn't run: %s", comp.Path, comp.PartitionErr),
			partitionBody{State: st.String(), Error: comp.PartitionErr}, true
	}
	return "", nil, false
}

// writePartitionPaused writes the 409 for a paused tile: the proxy's error
// shape plus "partition".
func writePartitionPaused(w http.ResponseWriter, msg string, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "docs": "/docs/protocol.md", "partition": body})
}
