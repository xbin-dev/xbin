package boot

// sealedapi.go — GET /api/xbin/sealed, the admins' read of the sealed
// components of this xbind (D227; docs/protocol/sealed.md): what the system
// page (/xbin/system) and `bx system` show. Everything an admin may change
// about one goes through the routes they already use — access (PUT
// /access), enable and disable (POST /lifecycle), limits (POST /limits),
// the bindings of its declared slots (POST /bindings) — so this route only
// reads.

import (
	"net/http"
	"sort"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sealed"
	"github.com/xbin-dev/xbin/internal/server"
)

type sealedSlot struct {
	Slot    string   `json:"slot"`
	Kind    string   `json:"kind"`
	Service string   `json:"service,omitempty"`
	Multi   bool     `json:"multi,omitempty"`
	Bound   []string `json:"bound"` // provider refs; [] = unbound
}

type sealedRow struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Runtime string `json:"runtime,omitempty"`
	// Digest names the code this release runs for it (the short digest of
	// its embedded tree): it changes exactly when an upgrade changes it.
	Digest string `json:"digest"`
	// Build: "release" (the install's prebuilt backend), "development" (a
	// development xbind compiles it), "none" (held: no build), or absent
	// for a component without a compiled backend.
	Build string `json:"build,omitempty"`
	// State is its lifecycle: enabled, disabled or hidden.
	State string `json:"state"`
	// Hold is why it can't run, absent when it can.
	Hold *sealed.Hold `json:"hold,omitempty"`
	// Backend is its backend's state (idle, building, starting, healthy,
	// failed); absent without one.
	Backend string `json:"backend,omitempty"`
	// Partition is the table's partition mode ([] = one instance) and
	// whether people's partitions run in this boot.
	Partition   []string `json:"partition"`
	Partitioned bool     `json:"partitioned"`
	// DataFormat is the data format this release's code reads and writes;
	// Stamp what its data was last written as, and by which release.
	DataFormat int            `json:"dataFormat"`
	Stamp      *sealed.Stamp  `json:"stamp,omitempty"`
	Slots      []sealedSlot   `json:"slots"`
	Grants     []registry.Use `json:"grants"`
	Delegate   bool           `json:"delegate,omitempty"`
}

// registerSealedAPI mounts GET /sealed.
func (st *State) registerSealedAPI(srv *server.Server) {
	srv.RegisterAPI("GET /sealed", func(w http.ResponseWriter, r *http.Request) {
		if !st.Broker.IsAdmin(auth.PrincipalOf(r)) {
			server.WriteError(w, http.StatusForbidden, "admin only — needs the xbin:admin capability", "/docs/auth.md")
			return
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{"release": st.Cfg.Version, "components": st.sealedRows()})
	})
}

func (st *State) sealedRows() []sealedRow {
	rows := []sealedRow{}
	bindings := st.Reg.Workspace().Bindings
	for _, sc := range st.Sealed.Components() {
		row := sealedRow{
			Path: sc.Path, Name: sc.Name, Runtime: sc.Runtime, Digest: sc.ID(),
			State: st.Reg.LifecycleState(sc.Path), Partition: append([]string{}, sc.Partition...), Partitioned: sc.Partitioned,
			DataFormat: max(sc.Data, 1), Slots: []sealedSlot{}, Grants: append([]registry.Use{}, sc.Grants...),
			Delegate: sc.Capabilities.Delegate,
		}
		if h, held := st.Sealed.Hold(sc.Path); held {
			row.Hold = &h
		}
		if stamp, ok := st.Sealed.Stamp(sc.Path); ok {
			row.Stamp = &stamp
		}
		if sc.Runtime == "go" {
			switch {
			case sc.Prebuilt != "":
				row.Build = "release"
			case st.sealedDev():
				row.Build = "development"
			default:
				row.Build = "none"
			}
		}
		if c, ok := st.Reg.Component(sc.Path); ok {
			row.Title = c.Manifest.DisplayTitle()
			if c.HasBackend() {
				row.Backend = st.Run.PrimaryState(sc.Path)
			}
			for slot, def := range c.Manifest.Interfaces {
				row.Slots = append(row.Slots, sealedSlot{Slot: slot, Kind: def.Kind, Service: def.Service, Multi: def.Multi,
					Bound: append([]string{}, bindings[sc.Path][slot].Refs()...)})
			}
			sort.Slice(row.Slots, func(i, j int) bool { return row.Slots[i].Slot < row.Slots[j].Slot })
		}
		rows = append(rows, row)
	}
	return rows
}
