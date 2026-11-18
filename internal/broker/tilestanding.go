package broker

// tilestanding.go — the broker's part of GET /delegate/tile (D246; the
// server's delegatetile.go; plans/wsagent/access-and-status.md §2): what a
// person may see of one tile's owner, wiring, scheduled jobs and log, each
// under the gate of the read it replaces —
//
//   - the owner: GET /owner (any reader); a person's display name only to
//     someone who sees accounts (AccountsVisible) or to themselves, an
//     org's name where they see the org (AgentOrgVisible);
//   - the slots' bindings and the uses' grants: the person's own GET
//     /bindings and GET /grants views — a workspace admin's every row; for
//     anyone else the tiles they write, or whose owning org they admin
//     (apiBindingsList's scope, orgFilterGrants' rows);
//   - the scheduled jobs: GET /cron/jobs as a terminal on the tile reads its
//     own (an admin, or terminal level there);
//   - the log: GET /logs' rule (canReadLogs): terminal level.

import (
	"errors"
	"io/fs"
	"slices"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// The log's tail in a standing: at most this many bytes and lines.
const (
	standingLogBytes = 4 << 10
	standingLogLines = 30
)

// TileStanding answers what person may see of tile (the file comment).
func (b *Broker) TileStanding(person auth.Principal, tile string) server.TileStanding {
	st := server.TileStanding{Slots: []server.StandingSlot{}, Uses: []server.StandingUse{}, Provides: []server.StandingProvide{}, Exposes: []server.StandingExpose{}}
	c, ok := b.Reg.Component(tile)
	if !ok || c.Sealed {
		return st
	}
	st.Owner = b.standingOwner(person, tile)
	admin := b.IsAdmin(person)
	st.Wiring = admin || b.wiringVisible(person, tile)
	bound := b.Reg.Workspace().Bindings[tile]
	for _, slot := range sortedKeys(c.Manifest.Interfaces) {
		def := c.Manifest.Interfaces[slot]
		ss := server.StandingSlot{Slot: slot, Kind: def.Kind, Service: def.Service, Required: def.Required, Multi: def.Multi}
		if st.Wiring {
			ss.Bound = []string{}
			for _, e := range bound[slot] {
				ss.Bound = append(ss.Bound, e.Ref)
			}
		}
		st.Slots = append(st.Slots, ss)
	}
	var grants []registry.Grant
	var pending []registry.Grant
	if st.Wiring {
		grants, pending = b.standingGrants(person, admin, tile)
	}
	for _, u := range c.Manifest.Uses {
		su := server.StandingUse{Target: u.Target, Role: u.Role}
		if st.Wiring {
			su.Granted = slices.ContainsFunc(grants, func(g registry.Grant) bool { return g.Target == u.Target && g.Role == u.Role })
			su.Pending = !su.Granted && slices.ContainsFunc(pending, func(g registry.Grant) bool { return g.Target == u.Target })
		}
		st.Uses = append(st.Uses, su)
	}
	for _, slot := range sortedKeys(c.Manifest.Provides) {
		def := c.Manifest.Provides[slot]
		st.Provides = append(st.Provides, server.StandingProvide{Slot: slot, Kind: def.Kind, Service: def.Service, Role: def.Role})
	}
	for _, slot := range sortedKeys(c.Manifest.Exposes) {
		def := c.Manifest.Exposes[slot]
		st.Exposes = append(st.Exposes, server.StandingExpose{Slot: slot, Kind: def.Kind, Paths: def.Paths})
	}
	if admin || person.CanTerminalTile(tile) {
		st.Cron = b.standingJobs(tile)
	}
	st.Log = b.standingLog(person, tile)
	if b.obs != nil { // GET /tile-report's row (a reader's)
		if level, msg, ok := b.obs.Report(tile); ok {
			st.Report = &server.StandingReport{Level: level, Message: msg}
		}
	}
	for _, a := range b.TileAlerts(tile) { // GET /tile-status' alerts, the tile's own
		if a.Tile == tile && !a.System && a.Deployment == "" {
			st.Alerts = append(st.Alerts, strings.TrimSpace(a.Kind+": "+a.Message))
		}
	}
	return st
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// standingOwner is tile's owner as person may see it.
func (b *Broker) standingOwner(person auth.Principal, tile string) *server.StandingOwner {
	if b.Users == nil {
		return nil
	}
	kind, id, err := users.ParseOwner(b.Users.Owner(tile))
	switch {
	case err != nil:
		return nil
	case kind == "":
		return &server.StandingOwner{Kind: "workspace"}
	case kind == users.OwnerKindUser:
		o := &server.StandingOwner{Kind: kind, ID: id}
		if id == person.UserID || b.AccountsVisible(person) {
			if u, ok := b.Users.Get(id); ok {
				o.Name = u.Name
			}
		}
		return o
	}
	o := &server.StandingOwner{Kind: kind, ID: id}
	if b.AgentOrgVisible(person, id) {
		if org, ok := b.Users.Org(id); ok {
			o.Name = org.Name
		}
	}
	return o
}

// wiringVisible is apiBindingsList's scope for a person who is no
// workspace admin: a tile they write, or one an org they admin owns.
func (b *Broker) wiringVisible(p auth.Principal, tile string) bool {
	if b.Users == nil || p.Component != "" || p.User == nil {
		return false
	}
	if p.CanWriteTile(tile) {
		return true
	}
	if org, isOrg := b.Users.OwnerOrg(tile); isOrg {
		return slices.Contains(p.Access.AdminOrgs(), org)
	}
	return false
}

// standingGrants are tile's grants and pending uses as person's own
// GET /grants view has them: every row for a workspace admin, else the
// rows orgFilterGrants gives them.
func (b *Broker) standingGrants(p auth.Principal, admin bool, tile string) (grants, pending []registry.Grant) {
	if admin {
		for _, g := range b.Reg.Workspace().Grants {
			if g.From == tile {
				grants = append(grants, g)
			}
		}
		for _, pg := range b.Pending() {
			if pg.From == tile {
				pending = append(pending, pg.Grant)
			}
		}
		return grants, pending
	}
	rows, pend, ok := b.orgFilterGrants(p)
	if !ok {
		return nil, nil
	}
	for _, g := range rows {
		if g.From == tile {
			grants = append(grants, g.Grant)
		}
	}
	for _, pg := range pend {
		if pg.From == tile {
			pending = append(pending, pg.Grant)
		}
	}
	return grants, pending
}

// standingJobs are tile's main scheduled jobs, as GET /cron/jobs lists
// them to the tile's own terminal.
func (b *Broker) standingJobs(tile string) []server.StandingJob {
	out := []server.StandingJob{}
	if b.cron == nil {
		return out
	}
	b.cron.mu.Lock()
	var jobs []cronJob
	for _, j := range b.cron.jobs {
		if j.Component == tile {
			jobs = append(jobs, j)
		}
	}
	b.cron.mu.Unlock()
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].Name < jobs[k].Name })
	for _, j := range jobs {
		row := server.StandingJob{Name: j.Name, Schedule: j.Schedule, Path: j.Path, Dormant: !b.firing(j.Component, util.MainDeployment)}
		row.Next, row.LastRun, row.LastStatus = b.cron.runWords(j)
		out = append(out, row)
	}
	return out
}

// standingLog is the end of tile's log where person may read it.
func (b *Broker) standingLog(p auth.Principal, tile string) server.StandingLog {
	if util.SealedPath(tile) || !p.CanTerminalTile(tile) {
		return server.StandingLog{Why: "the log is read where your person may open a terminal on the tile (logs can hold secrets)"}
	}
	if b.obs == nil {
		return server.StandingLog{Why: "this xbind keeps no backend logs"}
	}
	text, err := b.obs.Tail(tile, standingLogBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return server.StandingLog{Shown: true, Why: "no log yet: its backend hasn't started since xbin did"}
	case err != nil:
		return server.StandingLog{Why: "the log can't be read now"}
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > standingLogLines {
		lines = lines[len(lines)-standingLogLines:]
	}
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	return server.StandingLog{Shown: true, Lines: lines}
}
