package broker

// partitionnotify.go — what tells people a tile's partition mode is waiting
// for a decision (plans/partitions/01 §2.4, §6; PD-44): a request pushes to
// the tile's managers (at most once per tile per partitionPushQuiet, so a
// writer toggling the manifest's partition key can't burn the managers'
// push budget), every mode change reloads the tile's frames (the switch
// page appears or goes), and GET /alerts carries a pending tile's request
// and an invalid tile's error for admins and the tile's readers.

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// partitionPushQuiet is how long after a tile's request push another
// request of the same tile pushes nothing (the alert, the page and the
// frames' reload still say it at once).
const partitionPushQuiet = 15 * time.Minute

var (
	partitionPushedMu sync.Mutex
	partitionPushed   = map[switchKey]time.Time{} // (broker, tile) → its last request push
	partitionNow      = time.Now                  // tests stand in
)

// requestPushDue reports whether a new request of tile may push now, and
// if so marks it pushed.
func (b *Broker) requestPushDue(tile string) bool {
	partitionPushedMu.Lock()
	defer partitionPushedMu.Unlock()
	k, now := switchKey{b, tile}, partitionNow()
	if at, ok := partitionPushed[k]; ok && now.Sub(at) < partitionPushQuiet {
		return false
	}
	for key, at := range partitionPushed { // keep the map small: forget quiet ones
		if now.Sub(at) >= partitionPushQuiet {
			delete(partitionPushed, key)
		}
	}
	partitionPushed[k] = now
	return true
}

// partitionModeChanged is told each history entry a rescan records (auto,
// request, withdrawn): the tile's frames reload — the switch page appears
// or goes — and a new request pushes to the tile's managers, unless the
// tile's last request pushed within partitionPushQuiet. Called off the
// settle's locks.
func (b *Broker) partitionModeChanged(tile string, h modeHistory) {
	b.Hub.Publish(events.Event{Type: "reload", Component: tile})
	b.publishPartitionMode(tile) // the partitions event, op mode (partitiontrust.go)
	if h.Op != modeOpRequest || !b.requestPushDue(tile) {
		return
	}
	from, to := registry.SpecOf(h.From), registry.SpecOf(h.To)
	title := tile + " is paused: a partition mode switch is requested"
	body := fmt.Sprintf("%s → %s. Switching deletes %s; keeping the current mode deletes nothing. Until a manager decides, %s doesn't run.",
		from, to, registry.SwitchDeletes(from, to), tile)
	for _, user := range b.tileManagers(tile) {
		b.pushPerson(user, "tile.partition-switch", title, body, consentPage, "partition-switch:"+tile) // where managers decide
	}
}

// tileManagers are the people who decide for tile: every enabled user
// mayManageTile passes, and the root token's own devices ("owner", the
// push plane's key for it).
func (b *Broker) tileManagers(tile string) []string {
	out := []string{"owner"}
	if b.Users == nil {
		return out
	}
	for _, u := range b.Users.List() {
		if u.Disabled {
			continue
		}
		acc, _ := b.Users.Access(u.ID)
		if b.mayManageTile(auth.Principal{UserID: u.ID, User: userRef(u), Access: acc}, tile) {
			out = append(out, u.ID)
		}
	}
	return out
}

func userRef(u users.User) *users.User { return &u }

// partitionAlerts are the /alerts rows of the tiles their partition mode
// pauses (01 §6): kind partition-switch for a request waiting for a manager,
// partition-invalid for a request that can't run (or a mode record that
// can't be read). Admins and each tile's readers see them; the old shells
// show them as the top banner.
func (b *Broker) partitionAlerts(p auth.Principal, admin bool) []Alert {
	var out []Alert
	for _, c := range b.Reg.Components() {
		st, r, req := c.PartitionState()
		pending := st == registry.PartitionPending && req != nil
		if !pending && st != registry.PartitionInvalid || !admin && !p.CanReadTile(c.Path) {
			continue
		}
		if !pending {
			why := cmp.Or(c.PartitionErr, "its partition request is invalid") + " — fix the tile's partition key"
			if c.PartitionRecordUnknown() {
				why = "its partition mode record can't be read (" + c.PartitionErr + ") — an admin repairs it"
			}
			out = append(out, Alert{Level: "warn", Kind: "partition-invalid", Tile: c.Path,
				Message: fmt.Sprintf("%s doesn't run: %s (docs/partitions.md).", c.Path, why)})
			continue
		}
		q := registry.SpecOf(req.Spec)
		out = append(out, Alert{Level: "warn", Kind: "partition-switch", Tile: c.Path,
			Message: fmt.Sprintf("A partition mode switch is requested for %s (%s → %s): switching deletes %s. Until a manager of %s switches or keeps the current mode (bx partition switch|keep %s, or on %s), it doesn't run.",
				c.Path, r, q, registry.SwitchDeletes(r, q), c.Path, c.Path, "/"+consentPage)})
	}
	slices.SortFunc(out, func(a, b Alert) int { return strings.Compare(a.Tile, b.Tile) })
	return out
}
