package broker

// partitionterm.go — terminals and agent sessions on partitioned tiles, the
// broker's side (plans/partitions/06 §1-§3; PD-09, PD-10, PD-22). The
// terminal manager (internal/term/partition.go) asks, through hooks boot
// installs:
//
//   - TermPartition: a new session's partition — the opener's person's
//     (addressedPartition, 02 §3), global for the owner token when the tile
//     declares it (API off without it, PD-10), global for a session
//     targeting a non-primary deployment (PD-17) — and the person's
//     partition id, which keys their layer and history. Sessions on a tile
//     whose mode switch runs are refused;
//   - TermTilePartitioned: whether the tile holding a path keeps each
//     person's data apart now (the admin gates, PD-09);
//   - PersonPartitionKey: a person's partition id from their stored uid,
//     never minted (the history routes).
//
// A mode switch stops the tile's sessions and deletes its people's layers
// and partition history (the wipe hook "person-terminals", 01 §2.6),
// through the terminal manager boot installs (SetPartitionTerminals).

import (
	"fmt"
	"sync"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/util"
)

func init() {
	registerWipeHook(wipeHook{name: "person-terminals", stop: stopPersonTerminals, wipe: wipePersonTerminals})
}

// TermPartition answers the terminal manager's question about a session p
// opens on the tile holding path, targeting deployment dep ("" follows the
// primary) (term.SessionPartitionFunc). A tile that isn't partitioned
// answers the zero Partition, so its sessions are today's. An error refuses
// the session: a mode switch of the tile runs (term.ErrPartitionSwitching),
// or a person reaches no partition of it (addressedPartition's reason).
func (b *Broker) TermPartition(p auth.Principal, path, dep string) (term.Partition, error) {
	c, _, ok := b.Reg.Resolve(path)
	if !ok {
		return term.Partition{}, nil
	}
	tile := c.Path
	if why := b.switchHold(tile); why != "" {
		return term.Partition{}, fmt.Errorf("%w: %s %s — open the session once it is done", term.ErrPartitionSwitching, tile, why)
	}
	_, partitioned, err := b.tilePartitioning(tile)
	switch {
	case err != nil && p.UserID != "":
		return term.Partition{}, err // the record can't be read: no person's layer can be keyed (fail closed)
	case err != nil:
		return term.Partition{Partitioned: true, Tile: tile, NoAPI: err.Error()}, nil
	case !partitioned:
		return term.Partition{}, nil
	}
	out := term.Partition{Partitioned: true, Tile: tile}
	if p.UserID != "" {
		// the person's partition id keys their layer and history, whichever
		// deployment the session targets; PartitionIdent checks they are
		// live on the tile and mints their uid at their first partition
		pkey, _, err := b.PartitionIdent(tile, string(util.UserPartition(p.UserID)))
		if err != nil {
			return term.Partition{}, err
		}
		out.Key = pkey
	}
	if dep != "" && !b.isPrimary(tile, dep) {
		out.Part = string(util.PartitionGlobal) // a non-primary deployment's one instance (PD-17)
		return out, nil
	}
	if p.Component != "" && p.Component != tile {
		p.Component = tile // a terminal on a sub-path acts as its tile, as the server's gate reads it
	}
	part, err := b.addressedPartition(p, tile)
	switch {
	case err != nil && p.UserID == "": // no person, and no global instance to reach (PD-10)
		out.NoAPI = err.Error()
	case err != nil:
		return term.Partition{}, err
	default:
		out.Part = string(part)
	}
	return out, nil
}

// TermTilePartitioned reports whether the tile holding path keeps each
// person's data apart — its recorded mode has user partitions, paused or
// not, or can't be read (fail closed) — and which tile that is.
func (b *Broker) TermTilePartitioned(path string) (string, bool) {
	c, _, ok := b.Reg.Resolve(path)
	if !ok {
		return "", false
	}
	_, r, _ := c.PartitionState()
	return c.Path, r.User || c.PartitionRecordUnknown()
}

// PersonPartitionKey is person userID's partition id from their stored uid:
// "" while they have none, or are gone. Never mints.
func (b *Broker) PersonPartitionKey(userID string) string {
	if uid := b.storedPartitionUID(userID); uid != "" {
		return util.PartitionKey(userID, uid)
	}
	return ""
}

// PartitionTerminals is the terminal manager's side of a mode switch
// (*term.Manager).
type PartitionTerminals interface {
	StopTileSessions(tile string) error
	WipePartitionTile(tile string, dryRun bool) (term.PartitionTileWipe, error)
}

// partitionTerms holds each broker's PartitionTerminals (a broker without
// one — no terminals — has nothing of theirs to stop or wipe).
var partitionTerms sync.Map // *Broker → PartitionTerminals

// SetPartitionTerminals installs the terminal manager a switch stops and
// wipes through.
func (b *Broker) SetPartitionTerminals(t PartitionTerminals) {
	if t == nil {
		partitionTerms.Delete(b)
		return
	}
	partitionTerms.Store(b, t)
}

func (b *Broker) partitionTerminals() PartitionTerminals {
	if v, ok := partitionTerms.Load(b); ok {
		return v.(PartitionTerminals)
	}
	return nil
}

// stopPersonTerminals is a switch's stop step for terminals (01 §2.5): when
// it deletes everything, every session on the tile ends — a person's holds
// the layer about to go, and every other one was opened for the mode that
// is ending — and the switch waits for their teardown (an agent session's
// history is saved then). New sessions are refused while the switch runs
// (TermPartition). A session that won't end fails the wipe step.
func stopPersonTerminals(b *Broker, t wipeTarget) {
	if tm := b.partitionTerminals(); tm != nil && t.Kind == wipeEverything {
		_ = tm.StopTileSessions(t.Tile) // wipePersonTerminals asks again, and fails the switch on it
	}
}

// wipePersonTerminals deletes the tile's person layers and partition
// agent-session history when a switch deletes everything (01 §2.6, PD-22);
// a dry run counts. Each person whose layer or history goes is told, as for
// their partition's data. The tile's own layer and people's own history
// stay (the keep list).
func wipePersonTerminals(b *Broker, t wipeTarget, sum *wipeSummary) error {
	tm := b.partitionTerminals()
	if tm == nil || t.Kind != wipeEverything {
		return nil
	}
	if !t.DryRun {
		if err := tm.StopTileSessions(t.Tile); err != nil {
			return err
		}
	}
	got, err := tm.WipePartitionTile(t.Tile, t.DryRun)
	for _, user := range b.peopleOfPartitionKeys(got.Keys) {
		sum.addPerson(user)
	}
	return err
}

// peopleOfPartitionKeys names the people whose current partition id is one
// of keys (a deleted person's, or an older incarnation's, names no one).
func (b *Broker) peopleOfPartitionKeys(keys []string) []string {
	if len(keys) == 0 || b.Users == nil {
		return nil
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out []string
	for _, pub := range b.Users.List() { // the listing leaves the uid out: ask each record
		if uid := b.storedPartitionUID(pub.ID); uid != "" && want[util.PartitionKey(pub.ID, uid)] {
			out = append(out, pub.ID)
		}
	}
	return out
}
