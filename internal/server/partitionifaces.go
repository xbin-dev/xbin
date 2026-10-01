package server

// partitionifaces.go — the xbin-interfaces meta of a partitioned tile's
// document (plans/partitions/05 §3): a person's frames also list their own
// personal binds, as multi-slot endpoints with personal: true. Every other
// document — a tile that isn't partitioned, a non-primary deployment's, the
// global view, view-as — gets Policy.Interfaces exactly, as before.

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// PartitionInterfacesPolicy is a Policy that also renders a component's http
// interface slots as partition part sees them: Interfaces' plus that
// person's personal binds.
type PartitionInterfacesPolicy interface {
	PartitionInterfaces(comp string, part util.Partition) map[string]any
}

// docInterfaces is the xbin-interfaces meta's content for one of compPath's
// documents: the viewer's partition's view when the tile is partitioned and
// the document is its primary's, reached in the viewer's own user partition
// (as partitionHead resolves it); Interfaces otherwise.
func (s *Server) docInterfaces(r *http.Request, compPath string) map[string]any {
	pp, ok := s.policy().(PartitionInterfacesPolicy)
	if !ok || !s.tilePartitioned(compPath) {
		return s.policy().Interfaces(compPath)
	}
	p := auth.PrincipalOf(r)
	if sv := s.servedDeployment(r, compPath); p.ReadOnly() || sv.dep != sv.primary {
		return s.policy().Interfaces(compPath)
	}
	part, err := s.addressedPartition(p, s.owningTile(compPath))
	if err != nil || !part.IsUser() {
		return s.policy().Interfaces(compPath)
	}
	return pp.PartitionInterfaces(compPath, part)
}
