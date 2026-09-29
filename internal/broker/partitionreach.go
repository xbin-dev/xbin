package broker

// partitionreach.go — which data namespace a request or a user partition's
// instance reaches in a partitioned scope (plans/partitions/03 §B.2, §B.4,
// §B.6; 04 §1-§2; 05 §2): the one decision, in reachRes and PartitionEnv.
// A resource of a scope no tile partitions is reached exactly as before;
// in a partitioned scope a shared resource ("shared": true | "read") is at
// today's keys, read-only for people's partitions when "read", and any
// other is per partition: a person's own, the global instance's at today's
// keys (PD-04, PD-05). A user partition of another partitioned tile reaches
// the same person's namespace here only if the person can read this tile,
// and, with the workspace's partitionConsent policy on, consented (PD-13).

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// partGlobalKey is the global partition's wire key (02 §1).
const partGlobalKey = "global"

// partReach is what the partition decision adds to a reach: the user
// partition whose namespace holds the resource (pkey, "" for today's keys),
// the partition the caller acts in on the scope ("" when it isn't
// partitioned), whether a user partition reaches a "read" resource, and the
// identity a partition namespace's ns.json records at its first write.
type partReach struct {
	pkey     string
	part     string
	readOnly bool
	who      *nsPartition
}

// ---- seams other packs fill ----

// partitionConsentSeam reports whether person userID consented to tile
// from using their data in tile to (05 §2), asked only while the
// workspace's partitionConsent policy is on. The consent plane (F10) fills
// it; until then no consent exists, so with the policy on every
// cross-tile partition edge is refused (fail closed).
var partitionConsentSeam = func(b *Broker, userID, from, to string) bool { return false }

// partitionEdgeSeam counts one cross-scope reach into person userID's
// namespace of tile to by tile from, in the caller partition's egress
// ledger (06 §6). The ledger plane (F10) fills it; a no-op until then.
var partitionEdgeSeam = func(b *Broker, userID, from, to string) {}

// partitionIfaceEnvSeam adds a user partition's personal binds to its env
// (XBIN_IFACE_<SLOT> rows with personal: true, 05 §3). The bind-types
// plane (F15) fills it; until then a partition's env is EnvFor's alone.
var partitionIfaceEnvSeam = func(b *Broker, c *registry.Component, dep, part string, env []string) []string { return env }

// publishPartitionBusSeam publishes a bus event in a user partition's
// namespace (04 §2): stamped with the partition, delivered to that
// partition's subscriptions and its person's frames only. The identity
// plane (F2: events.Event.Partition, busFilter) fills it; until then such
// a publish is refused rather than delivered as today's namespace's.
var publishPartitionBusSeam = func(b *Broker, ra reach, topic string, data any) error {
	return errors.New("bus events in people's partitions aren't available in this xbind yet")
}

// ---- the decision ----

// partReachRefusal is a request the partition decision refuses, with its
// status (403 unless said otherwise).
type partReachRefusal struct {
	status int
	msg    string
}

func (e *partReachRefusal) Error() string { return e.msg }

func refusePartition(status int, format string, a ...any) error {
	return &partReachRefusal{status: status, msg: fmt.Sprintf(format, a...)}
}

// partitionReach settles, in place, which namespace ra — found by reachRes
// for p — is in when its scope is partitioned (03 §B.2). Nothing changes
// for a scope whose root's recorded mode has no user partitions, nor for a
// cron resource (its jobs belong to their registrant). A root whose mode
// record can't be read refuses (F1: never fall back to today's keys), and
// so does one paused (pending or invalid): nothing of it runs.
func (b *Broker) partitionReach(p auth.Principal, ra *reach) error {
	scope := ra.rt.Scope
	root, isTile := b.Reg.Component(scope)
	if scope == "" || !isTile {
		return nil
	}
	if root.PartitionRecordUnknown() {
		return refusePartition(http.StatusConflict, "%s's partition mode can't be read: its data isn't reachable until an admin repairs it", scope)
	}
	if _, ok := b.Reg.PartitionedScope(scope); !ok || ra.res.Type == "cron" {
		return nil
	}
	if st, _, _ := root.PartitionState(); st.Held() {
		return refusePartition(http.StatusConflict, "%s is paused (its partition mode is %s): its data isn't reachable until a manager decides", scope, st)
	}
	part, err := b.reachPartition(p, scope, ra.own)
	if err != nil {
		return err
	}
	ra.part = part
	user, isUser := strings.CutPrefix(part, "user:")
	switch {
	case part == partGlobalKey:
		return nil // today's keys: global's own data and every shared resource
	case !isUser || user == "":
		return refusePartition(http.StatusForbidden, "%s keeps each person's data apart: this credential reaches no partition of it", scope)
	case ra.res.Shared == registry.SharedAll:
		return nil
	case ra.res.Shared == registry.SharedRead:
		ra.readOnly = true
		return nil
	case ra.dep != b.scopePrimary(scope):
		return refusePartition(http.StatusForbidden, "%s's people's partitions run on its primary deployment only", scope)
	}
	pkey, uid, err := b.partitionKeyOf(user)
	if err != nil {
		return refusePartition(http.StatusForbidden, "%s: %s's partition: %v", scope, user, err)
	}
	ra.pkey = pkey
	ra.who = &nsPartition{User: user, UID: uid, Tile: scope}
	return nil
}

// reachPartition is the partition p acts in on scope's root tile: the
// tile's own principal's (addressedPartition on its own tile); a user
// partition of another partitioned tile maps to the same person's, when
// they can read this tile and — with partitionConsent on — consented; any
// other tile principal reaches global; a person, the root token and xbind's
// deliveries as the identity plane says (addressedPartition).
func (b *Broker) reachPartition(p auth.Principal, scope string, own bool) (string, error) {
	if own {
		return addressedPartitionSeam(b, p, p.Component)
	}
	if _, isTile := b.Reg.Component(p.Component); p.Component == "" || !isTile {
		return addressedPartitionSeam(b, p, scope)
	}
	cp, err := addressedPartitionSeam(b, p, p.Component)
	if err != nil {
		return "", err
	}
	user, ok := strings.CutPrefix(cp, "user:")
	if !ok {
		return partGlobalKey, nil // global, or an unpartitioned caller: today's (global's) data
	}
	switch {
	case !b.reachPersonReads(user, scope):
		return "", refusePartition(http.StatusForbidden, "%s can't read %s: %s's partition of it isn't reachable from %s", user, scope, user, p.Component)
	case b.Policies().PartitionConsent && !partitionConsentSeam(b, user, p.Component, scope):
		return "", refusePartition(http.StatusForbidden, "%s hasn't let %s use their %s data", user, p.Component, scope)
	}
	partitionEdgeSeam(b, user, p.Component, scope)
	return cp, nil
}

// reachPersonReads reports whether person userID, enabled, can read tile.
func (b *Broker) reachPersonReads(userID, tile string) bool {
	if b.Users == nil {
		return false
	}
	if u, ok := b.Users.Get(userID); !ok || u.Disabled {
		return false
	}
	acc, ok := b.Users.Access(userID)
	return ok && acc.CanReadTile(tile)
}

// errReadOnly is the refusal of a write by a user partition to a "read"
// resource (04 §1, S8).
func errReadOnly(rt resTarget) error {
	return refusePartition(http.StatusForbidden, "%s is read-only for people's partitions", rt)
}

// nsID is the data namespace ra reaches.
func (ra reach) nsID() nsID { return partNS(ra.rt.Scope, ra.dep, ra.pkey) }

// keys are ra's physical keys in the namespace it reaches.
func (b *Broker) reachKeys(ra reach) (resKeys, error) { return b.resKeysIn(ra.rt, ra.dep, ra.pkey) }

// nsEnterReach is nsEnter for the namespace ra reaches: a writer's first
// write into a user partition's namespace records whose it is first, and
// its kv file is marked in use until release (the idle close skips it).
func (b *Broker) nsEnterReach(w http.ResponseWriter, ra reach, want string) (release func(), ok bool) {
	id := ra.nsID()
	if ra.pkey != "" && ra.who != nil && want == "writer" {
		if err := b.notePartitionNS(id, *ra.who); err != nil {
			server.WriteError(w, http.StatusInternalServerError, "the partition's data namespace: "+err.Error())
			return nil, false
		}
	}
	gate, ok := b.nsEnterID(w, id, want)
	if !ok || ra.pkey == "" {
		return gate, ok
	}
	k, err := id.keys()
	if err != nil {
		return gate, ok
	}
	done := b.usePartitionKV(k.NS)
	return func() { gate(); done() }, true
}

// writeNamespaceRefusal answers a request whose credential's deployment
// can't be reached (404 when it is gone), or which the partition decision
// refuses (its status); 403 otherwise.
func writeNamespaceRefusal(w http.ResponseWriter, err error) {
	var pr *partReachRefusal
	switch {
	case errors.Is(err, util.ErrNoDeployment):
		server.WriteError(w, http.StatusNotFound, err.Error(), "/docs/protocol.md")
	case errors.As(err, &pr):
		server.WriteError(w, pr.status, pr.msg, "/docs/partitions.md")
	default:
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
	}
}

// ---- a user partition's instance ----

// PartitionEnv is the runner's env hook for an instance of partition part
// ("user:<id>" or "global"; "" is today's) of deployment dep of c
// (03 §B.4). The global instance's is DeploymentEnv's exactly: it keeps
// today's keys (PD-04). A user partition's env is EnvFor's, the same
// canonical paths and ids in every partition (so its code is identical),
// plus its personal binds (partitionIfaceEnvSeam); its remap binds each
// file resource of its own scope at the canonical path from its own volume,
// mounted now on first use — a shared one from today's volume, marked
// Shared (the runner re-derives that from the registry, and binds a "read"
// one read-only), and a workspace-level one not at all (an edge absent in
// a partition). A volume that can't mount, or a person whose partition key
// can't be told, has no entry: the start fails closed.
func (b *Broker) PartitionEnv(c *registry.Component, dep, part string) ([]string, map[string]runner.ResBind) {
	user, isUser := strings.CutPrefix(part, "user:")
	if part == "" || part == partGlobalKey {
		return b.DeploymentEnv(c, dep)
	}
	dep = cmp.Or(dep, util.MainDeployment)
	if b.viewDeployment(c) != dep {
		v := *c
		v.Deployment = dep
		c = &v
	}
	env := partitionIfaceEnvSeam(b, c, dep, part, b.EnvFor(c))
	remap := map[string]runner.ResBind{}
	if !isUser || user == "" {
		return env, remap
	}
	pkey, uid, err := b.partitionKeyOf(user)
	if err != nil {
		slog.Warn("partitions: no data namespace for the partition", "tile", c.Path, "partition", part, "err", err)
		return env, remap
	}
	handed := map[string]bool{}
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && strings.HasPrefix(k, "XBIN_RES_") {
			handed[v] = true
		}
	}
	noted := false
	for _, u := range c.Manifest.Uses {
		rt, res, ok := b.envTarget(c, dep, u.Target)
		if !ok || rt.Scope != c.Scope || res.Type != "filesystem" && res.Type != "sqlite" {
			continue
		}
		canon := b.fsResPath(rt.Scope, rt.Name, false)
		if _, done := remap[canon]; done || canon == "" || !handed[b.fsResPath(rt.Scope, rt.Name, res.Type == "sqlite")] {
			continue
		}
		switch {
		case rt.Scope == "":
			remap[canon] = runner.ResBind{Src: canon, Omit: true}
			continue
		case (res.Shared == registry.SharedAll || res.Shared == registry.SharedRead) && dep == util.MainDeployment:
			remap[canon] = runner.ResBind{Src: canon, Shared: true, RO: res.Shared == registry.SharedRead}
			continue
		}
		shared := res.Shared == registry.SharedAll || res.Shared == registry.SharedRead
		pk := pkey
		if shared {
			pk = "" // today's keys of a primary beyond main: its deployment's volume
		}
		k, err := b.resKeysIn(rt, dep, pk)
		if err != nil {
			continue
		}
		if shared {
			if b.ensureVolume(k, rt.Scope, res.Type) {
				remap[canon] = runner.ResBind{Src: b.resMount(k, false), RO: res.Shared == registry.SharedRead}
			}
			continue
		}
		if !noted {
			if err := b.notePartitionNS(partNS(rt.Scope, dep, pkey), nsPartition{User: user, UID: uid, Tile: c.Path}); err != nil {
				slog.Warn("partitions: the namespace's record", "tile", c.Path, "partition", part, "err", err)
				continue
			}
			noted = true
		}
		if !b.ensureVolume(k, rt.Scope, res.Type) {
			continue
		}
		remap[canon] = runner.ResBind{Src: b.resMount(k, false)}
	}
	return env, remap
}

// PartitionEncryptionHoldReason is DeploymentEncryptionHold's reason for
// user partition part of deployment dep of tile (03 §B.6): its own
// namespace held or left partial, or a resource it reaches — its own
// volumes, today's for a shared one — that can't be served. "" when it may
// start. For "" and "global" it is the deployment's.
func (b *Broker) PartitionEncryptionHoldReason(tile, dep, part string) string {
	user, isUser := strings.CutPrefix(part, "user:")
	if part == "" || part == partGlobalKey {
		return b.deploymentHoldReason(tile, dep)
	}
	if !isUser || user == "" {
		return "is held: " + part + " is no partition this xbind knows"
	}
	pkey, uid, err := b.partitionKeyOf(user)
	if err != nil {
		return "is held: " + err.Error()
	}
	return b.holdReasonIn(tile, cmp.Or(dep, util.MainDeployment), pkey, &nsPartition{User: user, UID: uid, Tile: tile})
}
