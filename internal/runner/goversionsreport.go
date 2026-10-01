package runner

// goversionsreport.go — what the upgrade check of D166
// (goversionscheck.go) says: GET /go-build-versions's report, the admin
// alert, and its dismissals — of the tiles that are still Go tiles.

import (
	"slices"
	"sort"
	"time"
)

// GoVersionsReport is GET /go-build-versions's answer (protocol.md).
type GoVersionsReport struct {
	Since          string             `json:"since,omitempty"`
	Done           bool               `json:"done"`
	Fresh          bool               `json:"fresh,omitempty"`
	Running        bool               `json:"running"`
	CheckedAt      *time.Time         `json:"checkedAt,omitempty"`
	WorkspaceError string             `json:"workspaceError,omitempty"`
	Tiles          []GoVersionsTile   `json:"tiles"`
	Errors         []GoVersionsFailed `json:"errors"`
}

// GoVersionsTile is a tile in the report.
type GoVersionsTile struct {
	Tile      string          `json:"tile"`
	Require   []string        `json:"require"`
	Minimal   bool            `json:"minimal"`
	Changes   []VersionChange `json:"changes"`
	Dismissed bool            `json:"dismissed"`
	CheckedAt time.Time       `json:"checkedAt"`
}

// GoVersionsFailed is a tile the check couldn't compare, and why.
type GoVersionsFailed struct {
	Tile  string `json:"tile"`
	Error string `json:"error"`
}

// Dismiss hides the alert's line for tile ("" = every tile it names) until
// its lines change.
func (g *GoVersions) Dismiss(tile string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if tile != "" {
		e := g.st.Tiles[tile]
		if e == nil {
			return ErrGoVersionsUnknownTile
		}
		e.Dismissed = true
	} else {
		for _, e := range g.st.Tiles {
			e.Dismissed = true
		}
	}
	return g.saveLocked()
}

// Report is the check's latest result: of the tiles that are still Go
// tiles.
func (g *GoVersions) Report() GoVersionsReport {
	g.mu.Lock()
	rep := GoVersionsReport{Since: g.st.Since, Done: g.st.Done, Fresh: g.st.Fresh, Running: g.running,
		WorkspaceError: g.st.WorkspaceError, Tiles: []GoVersionsTile{}, Errors: []GoVersionsFailed{}}
	if !g.st.CheckedAt.IsZero() {
		at := g.st.CheckedAt
		rep.CheckedAt = &at
	}
	for t, e := range g.st.Tiles {
		rep.Tiles = append(rep.Tiles, GoVersionsTile{Tile: t, Require: e.Require, Minimal: e.Minimal,
			Changes: e.Changes, Dismissed: e.Dismissed, CheckedAt: e.CheckedAt})
	}
	for t, msg := range g.st.Errors {
		rep.Errors = append(rep.Errors, GoVersionsFailed{Tile: t, Error: msg})
	}
	g.mu.Unlock()
	rep.Tiles = slices.DeleteFunc(rep.Tiles, func(t GoVersionsTile) bool { return !g.live(t.Tile) })
	rep.Errors = slices.DeleteFunc(rep.Errors, func(f GoVersionsFailed) bool { return !g.live(f.Tile) })
	sort.Slice(rep.Tiles, func(i, j int) bool { return rep.Tiles[i].Tile < rep.Tiles[j].Tile })
	sort.Slice(rep.Errors, func(i, j int) bool { return rep.Errors[i].Tile < rep.Errors[j].Tile })
	return rep
}

// live reports whether tile is still a Go tile: a tile deleted, renamed or
// no longer Go since it was checked has nothing to say (the next pass
// drops it).
func (g *GoVersions) live(tile string) bool {
	if g.Run.Reg == nil {
		return true
	}
	_, ok := g.goTile(tile)
	return ok
}

// Alert is the admin alert's message, naming every live tile whose line
// isn't dismissed; ok=false when there is none.
func (g *GoVersions) Alert() (string, bool) {
	if g == nil {
		return "", false
	}
	g.mu.Lock()
	var affected []GoVersionsAffected
	for t, e := range g.st.Tiles {
		if !e.Dismissed && len(e.Require) > 0 {
			affected = append(affected, GoVersionsAffected{Tile: t, Require: e.Require})
		}
	}
	since := g.st.Since
	g.mu.Unlock()
	affected = slices.DeleteFunc(affected, func(a GoVersionsAffected) bool { return !g.live(a.Tile) })
	if len(affected) == 0 {
		return "", false
	}
	sort.Slice(affected, func(i, j int) bool { return affected[i].Tile < affected[j].Tile })
	return goVersionsMessage(since, affected), true
}
