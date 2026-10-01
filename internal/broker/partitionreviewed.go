package broker

// partitionreviewed.go — "reviewed code only", a per-tile admin switch
// (plans/partitions/06 §4; PD-23, decided). A partitioned tile's code runs
// on every person's data, and so does the code of every non-partitioned
// provider bound to it. With the switch on, only reviewed code runs there:
//
//   - turning it on needs the tile's primary protected (D127m: its code
//     moves only by a tile manager's act naming the reviewed checkpoint)
//     and every bound non-partitioned provider's primary protected too —
//     409 naming what isn't;
//   - while it is on, unprotecting any of them is refused (the deployments
//     plane asks ReviewedOnlyRequires), and so is binding into the tile a
//     provider that isn't partitioned and whose primary isn't protected;
//   - a gap that opens some other way (a provider's deployment record
//     removed) shows as a trust warning, in the listing's
//     reviewedOnly.unprotected and on /alerts.
//
//	POST /api/xbin/partitions/reviewed {tile, on: true|false}   (admins, a person's act)
//
// The switch lives beside the tile's mode record
// (data/partitions/<TileKey>/reviewed.json, only while on); a tile that
// never had it on has none. It binds only while the tile is partitioned: a
// tile switched back to unpartitioned is governed as before, whatever the
// file says. A record this xbind can't read counts as on (fail closed).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	reviewedFile   = "reviewed.json"
	reviewedSchema = 1
)

// reviewedDoc is the switch's record.
type reviewedDoc struct {
	Schema int       `json:"schema"`
	On     bool      `json:"on"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

func (b *Broker) reviewedPath(tile string) string {
	return filepath.Join(b.Reg.Root, "data", partitionsDir, util.TileKey(tile), reviewedFile)
}

// reviewedOf reads tile's switch: off without a record; an error for one
// this xbind can't read (callers treat it as on).
func (b *Broker) reviewedOf(tile string) (reviewedDoc, error) {
	var d reviewedDoc
	raw, err := os.ReadFile(b.reviewedPath(tile)) // data/ is xbind's own
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	} else if err != nil {
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil || d.Schema != reviewedSchema {
		return reviewedDoc{On: true}, fmt.Errorf("%s's reviewed-code-only record can't be read", tile)
	}
	return d, nil
}

// reviewedOnlyOn reports whether the switch binds tile now: on (or
// unreadable) while the tile is partitioned.
func (b *Broker) reviewedOnlyOn(tile string) bool {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return false
	}
	if _, on := c.Partitioned(); !on {
		return false
	}
	d, err := b.reviewedOf(tile)
	return err != nil || d.On
}

// reviewedOnlyGaps are the tiles whose code runs on tile's people's data
// and whose primary isn't protected: tile itself, and its bound providers
// that aren't partitioned.
func (b *Broker) reviewedOnlyGaps(tile string) []string {
	var gaps []string
	if !b.primaryProtected(tile) {
		gaps = append(gaps, tile)
	}
	for _, prov := range b.boundProviders(tile) {
		if !b.primaryProtected(prov) {
			gaps = append(gaps, prov)
		}
	}
	return gaps
}

// reviewedOnlyView is the switch as GET /partitions shows it: nil while off.
func (b *Broker) reviewedOnlyView(tile string) map[string]any {
	if !b.reviewedOnlyOn(tile) {
		return nil
	}
	d, err := b.reviewedOf(tile)
	v := map[string]any{"on": true}
	if err != nil {
		v["error"] = err.Error() + ": treated as on"
	} else {
		v["by"], v["at"] = d.By, d.At
	}
	if gaps := b.reviewedOnlyGaps(tile); len(gaps) > 0 {
		v["unprotected"] = gaps
	}
	return v
}

// reviewedOnlyWarnings are the trust warnings of a switch that is on while
// something whose code runs on the tile's people's data isn't protected.
func (b *Broker) reviewedOnlyWarnings(tile string) []string {
	if !b.reviewedOnlyOn(tile) {
		return nil
	}
	if gaps := b.reviewedOnlyGaps(tile); len(gaps) > 0 {
		return []string{fmt.Sprintf("%s is set to run reviewed code only, but the primary of %s isn't protected: protect it (bx deployment protect) or turn the switch off",
			tile, strings.Join(gaps, ", "))}
	}
	return nil
}

// ReviewedOnlyRequires is the deployments plane's question before it
// unprotects tile's primary (DataHooks.ProtectRequired): non-empty says why
// not — tile, or a tile it is bound into as a provider, runs reviewed code
// only.
func (b *Broker) ReviewedOnlyRequires(tile string) string {
	var by []string
	for _, c := range b.Reg.Components() {
		if !b.reviewedOnlyOn(c.Path) {
			continue
		}
		if c.Path == tile || slices.Contains(b.boundProviders(c.Path), tile) {
			by = append(by, c.Path)
		}
	}
	if len(by) == 0 {
		return ""
	}
	return fmt.Sprintf("%s runs on people's partition data of %s, set to run reviewed code only: its primary stays protected — an admin turns the switch off first (bx partition reviewed <tile> off)",
		tile, strings.Join(by, ", "))
}

// reviewedOnlyBindRefusal refuses binding into comp, while it runs reviewed
// code only, a provider that isn't partitioned and whose primary isn't
// protected (409).
func (b *Broker) reviewedOnlyBindRefusal(comp string, binding registry.Binding) error {
	if !b.reviewedOnlyOn(comp) {
		return nil
	}
	for _, ref := range binding.Refs() {
		prov, _ := splitRef(ref)
		if _, ok := b.Reg.Component(prov); !ok {
			continue // a builtin or a named set: no tile's code
		}
		if _, partitioned, _ := b.tilePartitioning(prov); partitioned || b.primaryProtected(prov) {
			continue
		}
		return &bindConflict{msg: fmt.Sprintf("%s runs reviewed code only: %s's primary isn't protected, so its code can't be bound into it — protect it first (bx deployment protect %s)", comp, prov, prov)}
	}
	return nil
}

// apiPartitionReviewed — POST /partitions/reviewed {tile, on}: an admin
// turns the switch on (the primary and every bound provider protected) or
// off.
func (b *Broker) apiPartitionReviewed(w http.ResponseWriter, r *http.Request) {
	person, ok := b.partitionActor(w, auth.PrincipalOf(r))
	if !ok {
		return
	}
	if !b.IsAdmin(person) {
		server.WriteError(w, http.StatusForbidden, "reviewed code only is an admin's switch", modeDocs)
		return
	}
	var body struct {
		Tile string `json:"tile"`
		On   *bool  `json:"on"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.Tile == "" || body.On == nil {
		server.WriteError(w, http.StatusBadRequest, "need {tile, on: true|false}", "/docs/protocol.md")
		return
	}
	tile := strings.Trim(body.Tile, "/")
	if _, ok := b.Reg.Component(tile); !ok {
		server.WriteError(w, http.StatusNotFound, "no such tile: "+tile, modeDocs)
		return
	}
	path := b.reviewedPath(tile)
	if !*body.On {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			server.WriteError(w, http.StatusInternalServerError, hostless(err).Error())
			return
		}
	} else {
		if status, msg := b.partitionedTile(tile); status != 0 {
			server.WriteError(w, status, msg, modeDocs)
			return
		}
		if gaps := b.reviewedOnlyGaps(tile); len(gaps) > 0 {
			server.WriteJSON(w, http.StatusConflict, map[string]any{"error": "reviewed code only needs the primary of " + strings.Join(gaps, ", ") +
				" protected first (bx deployment protect <tile>): their code runs on every person's data in " + tile, "unprotected": gaps, "docs": modeDocs})
			return
		}
		raw, _ := json.Marshal(reviewedDoc{Schema: reviewedSchema, On: true, By: deciderName(person), At: time.Now().UTC()})
		if err := writeFileIn(filepath.Dir(path), reviewedFile, raw); err != nil {
			server.WriteError(w, http.StatusInternalServerError, hostless(err).Error())
			return
		}
	}
	slog.Info("audit", "who", person.From(), "method", "POST", "path", "/partitions/reviewed", "status", http.StatusOK, "tile", tile, "on", *body.On)
	out := map[string]any{"ok": true, "tile": tile, "reviewedOnly": map[string]any{"on": false}}
	if v := b.reviewedOnlyView(tile); v != nil {
		out["reviewedOnly"] = v
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// dropReviewed removes a removed tile's switch with its mode record.
func (b *Broker) dropReviewed(tile string) {
	if err := os.Remove(b.reviewedPath(tile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("partitions: a removed tile's reviewed-code-only record", "tile", tile, "err", err)
	}
}
