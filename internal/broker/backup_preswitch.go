package broker

// backup_preswitch.go — the restores a partition mode switch guards
// (plans/partitions/11-backup-encryption.md §4; PD-56). An archive made
// before the tile's last partition mode switch that deleted data restores
// only after a typed confirmation naming that switch (its date): POST
// /restore's confirm (bx restore --confirm <date>). It then restores as
// ever — into the global instance's namespace, at today's keys; never into
// a person's partition, which no such archive holds. POST
// /deployments/restore has no confirmation: it refuses such an archive and
// names POST /restore, a restore of the whole tile.
//
// The switch is the mode record's lastWipe, which the history's trimming
// never drops (a record written before lastWipe was: the history's last
// deleting switch). A tile whose mode record this xbind can't read can't
// tell whether a switch deleted data since a backup: every restore of it
// then asks, the confirmation being the backup's own date.

import (
	"cmp"
	"fmt"
	"log/slog"
	"time"

	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// preSwitchError refuses an archive made before its tile's last partition
// mode switch that deleted data — or any archive, while the tile's mode
// record can't be read (unread) — until confirm names it.
type preSwitchError struct {
	tile, created string
	sw            modeWipe
	unread        string // why the tile's mode record can't be read
	deployments   bool   // POST /deployments/restore's refusal: it has no confirmation
}

// date is what confirm must be: the switch's date, or — the record
// unread — the backup's own (the tile's path when it names none).
func (e preSwitchError) date() string {
	if e.unread == "" {
		return e.sw.At.UTC().Format("2006-01-02")
	}
	if t, err := time.Parse(time.RFC3339, e.created); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return e.tile
}

func (e preSwitchError) Error() string {
	var what string
	if e.unread != "" {
		what = fmt.Sprintf("%s's partition mode record can't be read (%s): whether a partition mode switch since this backup (made %s) deleted data can't be told, and restoring it may bring such data back, into the global instance's namespace",
			e.tile, e.unread, e.created)
	} else {
		what = fmt.Sprintf("this backup (made %s) is older than %s's partition mode switch on %s (%s → %s): restoring it brings back data the switch deleted, into the global instance's namespace",
			e.created, e.tile, e.date(), registry.SpecOf(e.sw.From), registry.SpecOf(e.sw.To))
	}
	if e.deployments {
		return fmt.Sprintf("%s — a deployment's data restore can't be confirmed: only a restore of the whole tile can, POST /restore {component, version, confirm: %q} (bx restore %s [--version v] --confirm %s), which also restores the tile's source and the data of each deployment that backup lists",
			what, e.date(), e.tile, e.date())
	}
	return fmt.Sprintf("%s — confirm with %q (bx restore %s --confirm %s)", what, e.date(), e.tile, e.date())
}

// answer is the switch as POST /restore's 409 names it.
func (e preSwitchError) answer() map[string]any {
	if e.unread != "" {
		return map[string]any{"unknown": true, "error": e.unread, "confirm": e.date()}
	}
	return map[string]any{"at": e.sw.At.UTC().Format(time.RFC3339), "from": registry.SpecOf(e.sw.From), "to": registry.SpecOf(e.sw.To),
		"confirm": e.date()}
}

// lastDeletingSwitch is tile's last partition mode switch that deleted data
// (not one that only added "global"): ok false for none; unread says why
// the tile's record can't be read (then nothing can be told).
func (b *Broker) lastDeletingSwitch(tile string) (sw modeWipe, ok bool, unread string) {
	pm := b.parts
	if pm == nil {
		return modeWipe{}, false, ""
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if why := pm.unread[util.TileKey(tile)]; why != "" {
		return modeWipe{}, false, why
	}
	rec := pm.recs[tile]
	switch {
	case rec == nil:
		return modeWipe{}, false, ""
	case rec.LastWipe != nil:
		return *rec.LastWipe, true, ""
	}
	for i := len(rec.History) - 1; i >= 0; i-- { // a record written before lastWipe was
		h := rec.History[i]
		if h.Op == modeOpSwitch && wipeKindOf(registry.SpecOf(h.From), registry.SpecOf(h.To)) != wipeNone {
			return modeWipe{From: h.From, To: h.To, At: h.At}, true, ""
		}
	}
	return modeWipe{}, false, ""
}

// preSwitchRestore refuses an archive of tile's data in deployment dep made
// before the tile's last partition mode switch that deleted it (11 §4),
// unless confirm names the switch (its date): the restore brings back what
// the switch deleted — a plaintext archive's data (a sealed one's was
// erased with its key) and the registrations the archive lists — into the
// global instance's namespace, at today's keys. Removing "global" deleted
// main's data only, so a deployment's archive from before it restores as
// ever. An archive made within the switch's second counts as older. While
// the tile's mode record can't be read, every archive asks.
func (b *Broker) preSwitchRestore(tile string, m backup.Manifest, dep string, confirm string) error {
	sw, ok, unread := b.lastDeletingSwitch(tile)
	e := preSwitchError{tile: tile, created: m.Created, sw: sw, unread: unread}
	switch {
	case unread != "":
	case !ok:
		return nil
	case cmp.Or(dep, util.MainDeployment) != util.MainDeployment && wipeKindOf(registry.SpecOf(sw.From), registry.SpecOf(sw.To)) != wipeEverything:
		return nil
	default:
		created, err := time.Parse(time.RFC3339, m.Created)
		if err == nil && !created.Before(sw.At.Truncate(time.Second).Add(time.Second)) {
			return nil // made after the switch
		}
	}
	if confirm != "" && confirm == e.date() {
		slog.Warn("restore: a backup that may predate the tile's partition mode switch, confirmed", "tile", tile, "created", m.Created, "switch", sw.At, "unread", unread)
		return nil
	}
	return e
}
