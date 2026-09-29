package broker

// partitionroute.go — which partition a principal acts in, and which one a
// call reaches (plans/partitions/02 §3-§5, 05 §1; PD-08, PD-10, PD-11,
// PD-12, PD-20). A partitioned tile runs one backend instance per person
// ("user:<id>") and, when it declares one, a global instance. xbind decides
// the partition from the verified credential, never from the URL or a
// header the caller controls:
//
//   - addressedPartition(p, tile) is the one function: the partition of tile
//     a request by p reaches. The server's planes (the class gate, events,
//     the document meta) ask it through brokerPolicy, Route through
//     routePartition. For a tile that isn't partitioned it answers "" for
//     everyone — every existing answer is unchanged;
//   - callerPartition(p) is the partition p acts in on its own tile;
//   - personLive is the liveness gate (PD-20): a user partition is reached,
//     fires and authenticates only while its person exists, is enabled and
//     can read the tile.
//
// "Partitioned" here means the RECORDED mode has user partitions (01 §2.1):
// a tile whose mode switch is pending or invalid keeps its recorded data
// layout, so its principals keep their partitions while the proxy's 409
// gate holds its backend (fail closed toward the partitions).
//
// Seams other packs fill (the defaults fail closed or do nothing):
// partitionConsentHolds (F10), personalBindGrant (F15), partitionEdgeCounted
// (F10's ledger), partitionAdoptUID (F5's records).

import (
	"cmp"
	"errors"
	"fmt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionMailPrincipal is the Component of a partition-mail doorbell's
// principal (04 §3, 02 §5): a delivery like cron's and the bus's.
const partitionMailPrincipal = "xbin/mail"

// isDelivery reports a cron, bus or mail delivery's synthetic principal.
func isDelivery(p auth.Principal) bool {
	return p.Component == CronPrincipal || p.Component == BusPrincipal || p.Component == partitionMailPrincipal
}

// Seams, filled by the packs that own them.
var (
	// partitionConsentHolds reports whether userID consented to caller's
	// partition using their data in target (05 §2), asked only while the
	// workspace policy partitionConsent is on. nil: no consent is recorded
	// anywhere, so every such call is refused (F10 fills it).
	partitionConsentHolds func(b *Broker, userID, caller, target string) bool
	// personalBindGrant is the role a personal bind gives caller acting in
	// callerPart on target (05 §3): only its owner's live partition. nil: no
	// personal binds exist (F15 fills it, beside grantedRoleIn).
	personalBindGrant func(b *Broker, caller string, callerPart util.Partition, target string) (role string, ok bool)
	// partitionEdgeCounted counts one allowed cross-tile call made by
	// caller's partition callerPart in its egress ledger (06 §6). nil:
	// nothing is counted (F10 fills it).
	partitionEdgeCounted func(b *Broker, caller string, callerPart util.Partition, target string)
	// partitionAdoptUID is the uid userID's partition records carry, when
	// they were created after the person's record (03 §E): what a person
	// whose uid an older xbind dropped adopts instead of a new one. nil or
	// "": none (F5 fills it).
	partitionAdoptUID func(b *Broker, userID string, created int64) string
)

// tilePartitioning answers tile's recorded mode when it has user
// partitions. err: the tile's record can't be read (R unknown), so no
// answer about its partitions can be given.
func (b *Broker) tilePartitioning(tile string) (registry.PartitionSpec, bool, error) {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return registry.PartitionSpec{}, false, nil
	}
	if c.PartitionRecordUnknown() {
		return registry.PartitionSpec{}, false, fmt.Errorf("%s's partition mode can't be read: it reaches no partition until an admin repairs it", tile)
	}
	if _, r, _ := c.PartitionState(); r.User {
		return r, true, nil
	}
	return registry.PartitionSpec{}, false, nil
}

// personLive is the liveness gate (PD-20): userID exists, is enabled and can
// read tile — from the users store, never a principal's snapshot.
func (b *Broker) personLive(userID, tile string) error {
	if b.Users == nil {
		return fmt.Errorf("%s: no person %q in this workspace", tile, userID)
	}
	u, ok := b.Users.Get(userID)
	switch {
	case !ok:
		return fmt.Errorf("%s: %s no longer exists", tile, userID)
	case u.Disabled:
		return fmt.Errorf("%s: %s's account is disabled", tile, userID)
	}
	if acc, ok := b.Users.Access(userID); !ok || !acc.CanReadTile(tile) {
		return fmt.Errorf("%s can't read %s: a person's partition reaches only tiles they can read", userID, tile)
	}
	return nil
}

// userPartition is userID's partition of tile, when they are live on it.
func (b *Broker) userPartition(userID, tile string) (util.Partition, error) {
	part, err := util.ParsePartition(string(util.UserPartition(userID)))
	if err != nil {
		return "", fmt.Errorf("%s keeps each person's data apart, and %q can't have a partition: %w", tile, userID, err)
	}
	if err := b.personLive(userID, tile); err != nil {
		return "", err
	}
	return part, nil
}

// globalOf is tile's global partition, when its mode declares one; err
// says why a principal without a person reaches nothing.
func globalOf(spec registry.PartitionSpec, err error) (util.Partition, error) {
	if spec.Global {
		return util.PartitionGlobal, nil
	}
	return "", err
}

// registeredPartition checks a partition taken from a registration (an
// instance token's, a delivery's): "" is global's (PD-04); a user
// partition's person must be live on tile.
func (b *Broker) registeredPartition(part util.Partition, tile string, spec registry.PartitionSpec) (util.Partition, error) {
	if id, ok := part.User(); ok {
		return b.userPartition(id, tile)
	}
	if part != "" && part != util.PartitionGlobal {
		return "", fmt.Errorf("%s: %q is no partition", tile, part)
	}
	return globalOf(spec, fmt.Errorf("%s has no global instance", tile))
}

// addressedPartition is the partition of tile a request by p reaches
// (02 §3): "" when tile isn't partitioned (for everyone: today's answer);
// otherwise "global", "user:<id>", or an error (a 403 whose text says why).
func (b *Broker) addressedPartition(p auth.Principal, tile string) (util.Partition, error) {
	spec, partitioned, err := b.tilePartitioning(tile)
	delivery := isDelivery(p)
	own := !delivery && p.Component != "" && p.Component == tile
	switch {
	case err != nil:
		return "", err
	case !partitioned && (own || delivery) && p.Partition.IsUser():
		// a person's partition credential on a tile no longer partitioned:
		// never the tile's main instance and data (S13)
		return "", fmt.Errorf("%s no longer keeps each person's data apart: %s's partition is gone", tile, p.Partition)
	case !partitioned:
		return "", nil
	}
	if own || delivery {
		dep := cmp.Or(p.Deployment, util.MainDeployment)
		if own {
			if dep, err = b.addressed(p, tile); err != nil {
				return "", err
			}
		}
		if !b.isPrimary(tile, dep) { // a non-primary deployment's one instance (PD-17)
			if p.Partition.IsUser() {
				return "", fmt.Errorf("%s: a person's partition runs only in the primary", tile)
			}
			return util.PartitionGlobal, nil
		}
	}
	switch {
	case delivery:
		return b.registeredPartition(p.Partition, tile, spec)
	case p.Impersonator != "":
		return "", fmt.Errorf("%s keeps %s's data private: view-as can't open it", tile, p.UserID)
	case own && p.Via == "instance":
		return b.registeredPartition(p.Partition, tile, spec)
	case own || p.Component == "":
		// the tile's frames, terminals, agent sessions and path tickets, and
		// people and the root token: the person's own partition (an admin's
		// too, PD-11); without a person, global (PD-10)
		if p.UserID == "" {
			return globalOf(spec, fmt.Errorf("sign in as a person: %s keeps each person's data apart", tile))
		}
		return b.userPartition(p.UserID, tile)
	}
	// another tile's principal: its own partition, mapped onto tile (05 §1)
	cp, err := b.callerPartition(p)
	if err != nil {
		return "", err
	}
	if id, ok := cp.User(); ok {
		if err := b.personLive(id, tile); err != nil {
			return "", err
		}
		if b.Policies().PartitionConsent && (partitionConsentHolds == nil || !partitionConsentHolds(b, id, p.Component, tile)) {
			return "", fmt.Errorf("%s hasn't let %s use their %s data", id, p.Component, tile)
		}
		return cp, nil
	}
	return globalOf(spec, fmt.Errorf("%s is partitioned: only partitioned tiles reach its people's data, and it has no global instance", tile))
}

// callerPartition is the partition p acts in on its own tile: "" for people,
// the owner, deliveries and every principal of a tile that isn't
// partitioned.
func (b *Broker) callerPartition(p auth.Principal) (util.Partition, error) {
	if p.Component == "" || isDelivery(p) {
		return "", nil
	}
	return b.addressedPartition(p, p.Component)
}

// partitionID is part's partition id (pkey) for X-XBin-Partition-Id: "" for
// global; for a user partition, from the person's uid, minted (or adopted
// from the partition records) at their first partition.
func (b *Broker) partitionID(part util.Partition) (string, error) {
	id, ok := part.User()
	if !ok {
		return "", nil
	}
	if b.Users == nil {
		return "", fmt.Errorf("no person %q in this workspace", id)
	}
	u, ok := b.Users.Get(id)
	if !ok {
		return "", fmt.Errorf("%s no longer exists", id)
	}
	uid := u.UID
	if uid == "" {
		adopt := ""
		if partitionAdoptUID != nil {
			adopt = partitionAdoptUID(b, u.ID, u.Created)
		}
		var err error
		if uid, err = b.Users.EnsureUID(u.ID, adopt); err != nil {
			return "", fmt.Errorf("%s's partition identity can't be recorded: %w", id, err)
		}
	}
	return util.PartitionKey(u.ID, uid), nil
}

// routePartition adds the partition dimension to Route's decision d (02 §4):
// the target's partition (Partition), the caller's (CallerPartition, with
// its id), and whether a start it causes is a background one. A call
// between two unpartitioned ends returns d as it came, byte for byte.
func (b *Broker) routePartition(p auth.Principal, target *registry.Component, d Decision) Decision {
	t := target.Path
	_, partitioned, terr := b.tilePartitioning(t)
	delivery := isDelivery(p)
	crossTile := p.Component != "" && !delivery && p.Component != t
	var cp util.Partition
	if crossTile {
		var err error
		if cp, err = b.callerPartition(p); err != nil {
			return Decision{Deny: err} // a caller whose partition can't be told reaches nothing
		}
	}
	if !partitioned && cp == "" && terr == nil {
		if p.Partition.IsUser() && !crossTile { // a person's partition credential on a tile no longer partitioned
			_, err := b.addressedPartition(p, t)
			return Decision{Deny: err}
		}
		return d
	}
	if d.Deny != nil {
		var ng *NotGrantedError
		role, ok := "", false
		if cp.IsUser() && errors.As(d.Deny, &ng) && personalBindGrant != nil {
			role, ok = personalBindGrant(b, p.Component, cp, t)
		}
		if !ok {
			return d
		}
		d = Decision{Deployment: b.primaryOf(t), Role: role}
	}
	if terr != nil {
		return d // a tile whose mode can't be read: the proxy's partition gate holds it (409)
	}
	if partitioned {
		part := util.PartitionGlobal // a non-primary deployment's one instance (PD-17)
		if b.isPrimary(t, d.Deployment) {
			var err error
			if part, err = b.addressedPartition(p, t); err != nil {
				return Decision{Deny: err}
			}
		}
		d.Partition, d.Background = part, delivery
	}
	d.CallerPartition = d.Partition
	if crossTile {
		d.CallerPartition = cp
		if cp.IsUser() && partitionEdgeCounted != nil {
			partitionEdgeCounted(b, p.Component, cp, t)
		}
	}
	id, err := b.partitionID(d.CallerPartition)
	if err != nil {
		return Decision{Deny: err}
	}
	d.CallerPartitionID = id
	return d
}

// busPartitionAllows is busFilter's partition rule (02 §9): an event in a
// user partition's namespace reaches a subscriber only when that is the
// partition it reaches on the scope's (partitioned) root tile. The grant
// and namespace checks follow as for every bus event.
func (b *Broker) busPartitionAllows(p auth.Principal, e events.Event) bool {
	rt, ok := b.resScope(e.Topic)
	if !ok || rt.Scope == "" || p.ReadOnly() {
		return false
	}
	part, err := b.addressedPartition(p, rt.Scope)
	return err == nil && string(part) == e.Partition
}

// PartitionCovered reports whether a user partition's instance token still
// authenticates (02 §2, S13; auth.SetPartitionCoverage): tile is
// partitioned with user partitions, and part's person exists with that
// uid, is enabled and can read tile. Anything else: not covered, a 401.
func (b *Broker) PartitionCovered(tile string, part util.Partition, uid string) bool {
	id, ok := part.User()
	if !ok || uid == "" || b.Users == nil {
		return false
	}
	c, ok := b.Reg.Component(tile)
	if !ok {
		return false
	}
	if spec, ok := c.Partitioned(); !ok || !spec.User {
		return false
	}
	u, ok := b.Users.Get(id)
	return ok && u.UID == uid && b.personLive(id, tile) == nil
}

// AddressedPartition is addressedPartition for the server's planes
// (server.PartitionPolicy): the class gate, events and the document meta.
func (p brokerPolicy) AddressedPartition(pr auth.Principal, tile string) (util.Partition, error) {
	return p.b.addressedPartition(pr, tile)
}
