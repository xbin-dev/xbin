package auth

// partition.go — the partition a tile principal acts in (plans/partitions/02
// §1-§2; PD-01, S13). A partitioned tile runs one backend instance per person
// ("user:<id>") plus, when it declares one, a global instance. Which
// partition a credential acts in always comes from xbind state:
//
//   - an instance token: the partition its generation was started for,
//     registered by the runner (RegisterInstancePartition) — part of the
//     token's identity. The global instance registers as today's instance
//     (partition ""), which reads as "global" on a partitioned tile;
//   - frame tokens, terminal and agent tokens and path tickets already name
//     their person: the broker derives the partition from it
//     (addressedPartition). Their wire formats don't change (PD-01);
//   - cron, bus and mail deliveries: their registration's (the broker).
//
// A user partition's instance token authenticates only while that partition
// is covered — its tile is partitioned with user partitions, and its person
// exists with the same uid, is enabled and can read the tile — asked on every
// lookup (SetPartitionCoverage), so no window exists in which the token
// resolves to the tile's main instance: once not covered it is a 401. The
// runner and the mode/people hooks also drop such tokens eagerly
// (RevokePartitionInstances, RevokeUserPartitionInstances).

import (
	"log/slog"

	"github.com/xbin-dev/xbin/internal/util"
)

// Attribution is how an F5 call from a user partition reaches its tile's
// global instance (plans/partitions/02 §6, 05 §6; S3): as the partition's
// person, never as the tile itself. The broker's Route decides it, the
// proxy sets it as the callee's X-XBin-User, -User-Level and -Role.
type Attribution struct {
	UserID string // X-XBin-User
	Level  string // X-XBin-User-Level: the person's level on the tile
	Role   string // X-XBin-Role: clamped to that level, never self-admin
}

// SetPartitionCoverage installs the broker's answer to "is this user
// partition of tile, for the person with uid, still covered?" (boot). Without
// it no user partition's token authenticates (fail closed).
func (a *Auth) SetPartitionCoverage(f func(tile string, part util.Partition, uid string) bool) {
	a.mu.Lock()
	a.covered = f
	a.mu.Unlock()
}

// partitionCovered asks the coverage hook about id, a user partition's
// token, outside a.mu (the hook reads the registry and the users store).
func (a *Auth) partitionCovered(id instanceID) bool {
	a.mu.RLock()
	f := a.covered
	a.mu.RUnlock()
	return f != nil && f(id.component, id.partition, id.uid)
}

// RegisterInstancePartition registers the instance token of one backend
// generation of partition part of deployment of component: the runner calls
// it at spawn from its own state (never from env). part "" or "global" is
// the global instance, today's instance: it registers as
// RegisterInstanceDeployment does. A user partition needs its person's uid,
// and a key and uid that parse; anything else registers nothing (fail
// closed: the generation's calls authenticate as nobody).
func (a *Auth) RegisterInstancePartition(token, component, deployment string, part util.Partition, uid string) {
	if part == "" || part == util.PartitionGlobal {
		a.RegisterInstanceDeployment(token, component, deployment)
		return
	}
	dep, ok := claimName(deployment)
	if _, err := util.ParsePartition(string(part)); err != nil || !part.IsUser() || !ok || uid == "" {
		slog.Warn("instance token not registered: not a user partition of a deployment", "component", component,
			"deployment", deployment, "partition", string(part), "uid", uid != "")
		return
	}
	a.mu.Lock()
	a.instances[token] = instanceID{component: component, deployment: dep, partition: part, uid: uid}
	a.mu.Unlock()
}

// RevokePartitionInstances drops every user partition's instance token of
// tile, synchronously: its mode is about to change (01 §6), and no such
// token may act while the new state is published.
func (a *Auth) RevokePartitionInstances(tile string) int {
	return a.revokeInstances(func(id instanceID) bool { return id.component == tile && id.partition != "" })
}

// RevokeUserPartitionInstances drops every instance token of userID's
// partitions, on every tile, synchronously: the person was deleted,
// disabled, or lost access (06 §9).
func (a *Auth) RevokeUserPartitionInstances(userID string) int {
	return a.revokeInstances(func(id instanceID) bool {
		u, ok := id.partition.User()
		return ok && u == userID
	})
}

func (a *Auth) revokeInstances(match func(instanceID) bool) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for tok, id := range a.instances {
		if match(id) {
			delete(a.instances, tok)
			n++
		}
	}
	return n
}
