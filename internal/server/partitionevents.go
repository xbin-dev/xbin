package server

// partitionevents.go — /ws/events on partitioned tiles (plans/partitions/02
// §9; G2, S2, S12). A user partition's events — its bus traffic, its
// status, its runner and partitions events — carry events.Event.Partition,
// and reach only that partition: its own tile's principals acting in it and
// its person's own sockets. Admins lose the blanket pass for them, and for
// the term and session events of a partitioned tile, which reach only the
// session's own person (and their terminal on the tile). A tile that isn't
// partitioned keeps every filter as it was.

import (
	"fmt"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionEventFor reports whether e, a non-bus event of a user partition,
// is p's to see. tile is p's credential tile (eventFilter's). Only people
// (their own shell) and the event's tile's own principals ask; another
// tile's frame never sees a partition's events, whoever drives it. An
// event whose data is PersonOnly (a consent prompt or change) reaches the
// person's own sockets only, never the tile's principals.
func (s *Server) partitionEventFor(p auth.Principal, tile string, e events.Event) bool {
	owner := s.owningTile(e.Component)
	switch {
	case p.ReadOnly(), p.Component != "" && tile != owner, p.Component != "" && personOnlyEvent(e):
		return false
	case p.Component != "":
		p.Component = owner // an xbin.window sub-path acts as its tile (ownPartition)
	}
	part, err := s.addressedPartition(p, owner)
	return err == nil && string(part) == e.Partition
}

// personOnlyEvent reports an event for the person's own sockets only: its
// data says PersonOnly (the broker's consent events, partitionconsent.go).
func personOnlyEvent(e events.Event) bool {
	po, ok := e.Data.(interface{ PersonOnly() bool })
	return ok && po.PersonOnly()
}

// termEventVisible is termEventFor, without the admin pass on a partitioned
// tile (PD-09): only the session's own person, and their terminal on the
// tile, receive its term and session events there; never view-as.
func (s *Server) termEventVisible(p auth.Principal, e events.Event) bool {
	if !s.tilePartitioned(e.Component) {
		return termEventFor(p, e)
	}
	if p.ReadOnly() || p.Component != "" && !(p.Via == "terminal" && e.Component == p.Component) {
		return false
	}
	o, ok := e.Data.(owned)
	return ok && o.Owner() == term.HomeKey(p)
}

// partitionHead is the D4 injection's partition part for one of compPath's
// documents (02 §10): <meta name="xbin-partition" content="<key>"> when the
// tile is partitioned and the viewer resolves to a partition — theirs, or
// "global" for the root token and --no-auth. A non-primary deployment's
// document (/c/<tile>+<dep>/) says "global": its frame token is bound to
// that deployment, whose one instance every writer shares (PD-17). viewAs:
// an admin viewing as someone gets the document without a frame token, as
// the API would refuse it anyway (PD-08). Nothing for a tile that isn't
// partitioned.
func (s *Server) partitionHead(r *http.Request, compPath string) (meta string, viewAs bool) {
	if !s.tilePartitioned(compPath) {
		return "", false
	}
	p := auth.PrincipalOf(r)
	if p.ReadOnly() {
		return "", true
	}
	part := util.PartitionGlobal
	if sv := s.servedDeployment(r, compPath); sv.dep == sv.primary {
		var err error
		if part, err = s.addressedPartition(p, s.owningTile(compPath)); err != nil || part == "" {
			return "", false
		}
	}
	return fmt.Sprintf("<meta name=\"xbin-partition\" content=\"%s\">\n", htmlEscape(string(part))), false
}

// owningTile is the registered tile path holds (a session's cwd, an event's
// component): itself when nothing is registered there.
func (s *Server) owningTile(path string) string {
	if s.Reg == nil || path == "" {
		return path
	}
	if c, _, ok := s.Reg.Resolve(path); ok {
		return c.Path
	}
	return path
}

// tilePartitioned reports whether the tile holding path keeps each
// person's data apart: its recorded mode has user partitions (paused or
// not), or can't be read (fail closed).
func (s *Server) tilePartitioned(path string) bool {
	if s.Reg == nil || path == "" {
		return false
	}
	c, _, ok := s.Reg.Resolve(path)
	if !ok {
		return false
	}
	_, r, _ := c.PartitionState()
	return r.User || c.PartitionRecordUnknown()
}
