package broker

// personalbind.go — bind types on a partitioned tile (plans/partitions/05
// §3; PD-16, PD-54). A partitioned requester's interface slots are wired by
// two kinds of bind:
//
//   - a global bind is today's binding record in the workspace xbin.json,
//     created under today's bind authority, partitioned or not (owner
//     ruling, 2026-09-29): apiBindingSet is unchanged but for the one
//     bind-time refusal below (bindConflict);
//   - a personal bind is xbind state, data/partitions/binds/<uid>.json: the
//     owner of a user-owned, unpartitioned provider wires it into their OWN
//     partition of a partitioned requester they can read, on a multi http
//     slot whose service the provider provides. Only that person's
//     partition instance lists it (XBIN_IFACE_<SLOT>, personal: true —
//     partitionIfaceEnvSeam) and only their frames' xbin-interfaces meta
//     (HTTPInterfacesIn); it is the call grant (personalBindRole, Route's
//     personalBindGrant) only for a call from the requester acting in
//     user:<id>, whose uid is the record's, while <id> still owns the
//     provider — read live on every call, env and document. Any other
//     partition, global included, gets exactly today's refusal.
//
// Records are keyed by uid (PD-43), so a person deleted and created again
// inherits none. A bind goes when its person or an admin deletes it, when
// the provider changes owner (personalBindsProviderMoved), when the person
// is deleted (PersonalBindsUserDeleted), and when the requester switches
// between user partitions and unpartitioned (the "personal-binds" wipe
// hook). A change restarts only that person's partition instance of the
// requester, whose env was captured at spawn.

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	personalBindsDir    = "binds" // data/partitions/binds/<uid>.json
	personalBindsSchema = 1
)

// personalBindsFile is one person's record.
type personalBindsFile struct {
	Schema int            `json:"schema"`
	User   string         `json:"user"`
	UID    string         `json:"uid"`
	Binds  []personalBind `json:"binds"`
}

// personalBind is one row: requester's slot → provider, for the file's
// person's partition only.
type personalBind struct {
	ID        string    `json:"id"`
	Requester string    `json:"requester"`
	Slot      string    `json:"slot"`
	Provider  string    `json:"provider"`
	At        time.Time `json:"at"`
}

// personalBindSlot is the broker's personal-bind state: mu serializes every
// read-modify-write of a record. A person's instance whose env changed is
// stopped through the one stop hook (SetPartitionInstanceStop,
// partitionwire.go) and starts again on its next request.
type personalBindSlot struct {
	mu sync.Mutex
}

func init() {
	personalBindGrant = func(b *Broker, caller string, part util.Partition, target string) (string, bool) {
		return b.personalBindRole(caller, part, target)
	}
	partitionIfaceEnvSeam = personalIfaceEnv
	registerPartitionStore(partitionStore{"personal-binds", holdsPersonalBinds})
	registerWipeHook(wipeHook{name: "personal-binds", wipe: wipePersonalBinds})
}

// restartPartition restarts person id's partition instance of tile, its
// primary's (user partitions run there only, PD-17): the one stop hook
// (SetPartitionInstanceStop; unwired, the change reaches a running
// instance only when it next starts — boot's TestPersonalBindRestartWired).
func (b *Broker) restartPartition(tile, id string) {
	b.stopPartitionInstance(tile, b.primaryOf(tile), string(util.UserPartition(id)))
}

// ---- the store ----

func (b *Broker) personalBindsRoot() string {
	return filepath.Join(b.Reg.Root, "data", partitionsDir, personalBindsDir)
}

// uidOK: a uid names a file (users.EnsureUID mints hex; an adopted one came
// from xbind's own records) — never a path.
func uidOK(uid string) bool {
	if uid == "" || len(uid) > 128 {
		return false
	}
	for _, r := range uid {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// readPersonalBinds is uid's record; nil when it has none.
func (b *Broker) readPersonalBinds(uid string) (*personalBindsFile, error) {
	if !uidOK(uid) {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(b.personalBindsRoot(), uid+".json")) // walk-ok: data/partitions is xbind's own
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f personalBindsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("personal binds %s: %w", uid, err)
	}
	if f.Schema != personalBindsSchema {
		return nil, fmt.Errorf("personal binds %s: schema %d, this xbind reads %d", uid, f.Schema, personalBindsSchema)
	}
	return &f, nil
}

// eachPersonalBinds calls fn for every record, sorted by file name; a
// record that can't be read is an error after the rest were visited.
func (b *Broker) eachPersonalBinds(fn func(f *personalBindsFile)) error {
	ents, err := os.ReadDir(b.personalBindsRoot()) // walk-ok: data/partitions is xbind's own
	if absent(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		uid, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !uidOK(uid) {
			continue
		}
		f, err := b.readPersonalBinds(uid)
		switch {
		case err != nil:
			errs = append(errs, err)
		case f != nil && f.UID == uid:
			fn(f)
		}
	}
	return errors.Join(errs...)
}

// writePersonalBinds stores f, or removes its file once it holds no bind.
func (b *Broker) writePersonalBinds(f *personalBindsFile) error {
	if !uidOK(f.UID) {
		return fmt.Errorf("no partition identity for %s", f.User)
	}
	path := filepath.Join(b.personalBindsRoot(), f.UID+".json")
	if len(f.Binds) == 0 {
		if err := os.Remove(path); err != nil && !absent(err) {
			return err
		}
		return nil
	}
	f.Schema = personalBindsSchema
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomicIn(path, append(raw, '\n'), 0o600)
}

// personalBindsLive: f is its person's record now — their stored uid (never
// minted here) is the record's. A deleted person's record, or an earlier
// incarnation's, isn't: it applies to no one, holds no tile's data, names
// no one in a switch and matches no delete (PD-43); it is only removed (a
// wipe, a transfer, a tile created at its requester's path,
// PersonalBindsUserDeleted).
func (b *Broker) personalBindsLive(f *personalBindsFile) bool {
	return f.UID != "" && b.storedPartitionUID(f.User) == f.UID
}

// livePersonalBinds is person id's record while it is theirs: their stored
// uid (never minted here) names it and it names them. nil otherwise — a
// deleted person's record, or one of an earlier incarnation, reaches
// nothing.
func (b *Broker) livePersonalBinds(id string) *personalBindsFile {
	uid := b.storedPartitionUID(id)
	if uid == "" {
		return nil
	}
	f, err := b.readPersonalBinds(uid)
	if err != nil {
		slog.Warn("partitions: a person's personal binds can't be read; none apply", "user", id, "err", err)
		return nil
	}
	if f == nil || f.User != id || f.UID != uid {
		return nil
	}
	return f
}

// ---- the live rules ----

// personalBindCheck judges bind pb of person id against the live state: the
// provide it reaches, or why it doesn't hold (05 §3). The requester keeps
// user partitions and the slot is a multi http slot; the provider is still
// id's, isn't partitioned and provides the slot's service (not as
// instances, v1); the ceiling allows the edge.
func (b *Broker) personalBindCheck(id string, pb personalBind) (registry.Iface, string) {
	c, ok := b.Reg.Component(pb.Requester)
	if !ok {
		return registry.Iface{}, pb.Requester + " is gone"
	}
	if _, part, err := b.tilePartitioning(pb.Requester); err != nil || !part {
		return registry.Iface{}, pb.Requester + " doesn't keep each person's data apart"
	}
	def, ok := c.Manifest.Interfaces[pb.Slot]
	if !ok || def.Kind != "http" || !def.Multi {
		return registry.Iface{}, fmt.Sprintf("%s has no multi http slot %q", pb.Requester, pb.Slot)
	}
	p, ok := b.Reg.Component(pb.Provider)
	switch {
	case !ok:
		return registry.Iface{}, pb.Provider + " is gone"
	case pb.Provider == pb.Requester:
		return registry.Iface{}, "a tile can't be its own provider"
	case b.Users == nil || b.Users.Owner(pb.Provider) != users.OwnerKindUser+":"+id:
		return registry.Iface{}, pb.Provider + " is no longer " + id + "'s"
	}
	if _, part, err := b.tilePartitioning(pb.Provider); err != nil || part {
		return registry.Iface{}, pb.Provider + " keeps each person's data apart: a personal bind wires an unpartitioned tile"
	}
	pd, ok := httpProvideFor(p, def.Service)
	switch {
	case !ok:
		return registry.Iface{}, fmt.Sprintf("%s doesn't provide the %q service the slot wants", pb.Provider, def.Service)
	case pd.Instances:
		return registry.Iface{}, pb.Provider + " exposes instances: a personal bind takes a plain provider"
	}
	if msg := b.ceilingBlockMsg(pb.Requester, pb.Provider); msg != "" {
		return registry.Iface{}, msg
	}
	return pd, ""
}

// personalBindRole is the role from acting in partition callerPart holds on
// target through a personal bind (05 §3): for a user partition only, its
// person's own live bind of from to target, the first by slot, granting
// that provide's role. It is the second half of 05 §3's grantedRoleIn —
// resolveTarget's grantedRole (grants and global binds, unchanged) is the
// first, and stays the single evaluation point: Route asks this, through
// personalBindGrant, only when that refused a user partition's call.
func (b *Broker) personalBindRole(from string, callerPart util.Partition, target string) (string, bool) {
	id, ok := callerPart.User()
	if !ok {
		return "", false
	}
	f := b.livePersonalBinds(id)
	if f == nil {
		return "", false
	}
	rows := slices.Clone(f.Binds)
	slices.SortFunc(rows, func(x, y personalBind) int { return cmp.Compare(x.Slot, y.Slot) })
	for _, pb := range rows {
		if pb.Requester != from || pb.Provider != target {
			continue
		}
		if pd, why := b.personalBindCheck(id, pb); why == "" {
			return provideRole(pd), true
		}
	}
	return "", false
}

// personalEndpoints are part's personal binds on comp that hold, by slot, in
// the order they were made; none for a partition that isn't a person's. A
// provider already bound globally on the slot isn't listed twice.
func (b *Broker) personalEndpoints(comp string, part util.Partition) map[string][]IfaceEndpoint {
	id, ok := part.User()
	if !ok {
		return nil
	}
	f := b.livePersonalBinds(id)
	if f == nil {
		return nil
	}
	global := b.Reg.Workspace().Bindings[comp]
	out := map[string][]IfaceEndpoint{}
	for _, pb := range f.Binds {
		if pb.Requester != comp || slices.Contains(global[pb.Slot].Refs(), pb.Provider) {
			continue
		}
		if _, why := b.personalBindCheck(id, pb); why != "" {
			continue
		}
		out[pb.Slot] = append(out[pb.Slot], IfaceEndpoint{Provider: pb.Provider, URL: "/api/" + pb.Provider, Personal: true})
	}
	return out
}

// personalIfaceEnv is partitionIfaceEnvSeam: a user partition's env on its
// tile's primary gains its person's personal binds, appended to each multi
// slot's XBIN_IFACE_<SLOT> JSON as {provider, url, service, personal: true}
// (additive; the global rows first, byte for byte).
func personalIfaceEnv(b *Broker, c *registry.Component, dep, part string, env []string) []string {
	if !b.isPrimary(c.Path, dep) {
		return env
	}
	eps := b.personalEndpoints(c.Path, util.Partition(part))
	if len(eps) == 0 {
		return env
	}
	out := slices.Clone(env)
	for _, slot := range slices.Sorted(maps.Keys(eps)) {
		key := "XBIN_IFACE_" + envName(slot)
		idx, list := -1, []map[string]any{}
		for i, e := range out {
			if k, v, ok := strings.Cut(e, "="); ok && k == key {
				idx = i
				if err := json.Unmarshal([]byte(v), &list); err != nil {
					list = []map[string]any{}
				}
			}
		}
		for _, e := range eps[slot] {
			list = append(list, map[string]any{"provider": e.Provider, "url": "http://xbin" + e.URL,
				"service": c.Manifest.Interfaces[slot].Service, "personal": true})
		}
		j, _ := json.Marshal(list)
		if kv := key + "=" + string(j); idx >= 0 {
			out[idx] = kv
		} else {
			out = append(out, kv)
		}
	}
	return out
}

// HTTPInterfacesIn is HTTPInterfaces for a document viewed in partition
// part: a person's frames also list their personal binds on the multi
// slots, as endpoints with personal: true (05 §3). Any other partition:
// HTTPInterfaces exactly.
func (b *Broker) HTTPInterfacesIn(comp string, part util.Partition) map[string]any {
	out := b.HTTPInterfaces(comp)
	for slot, list := range b.personalEndpoints(comp, part) {
		m, ok := out[slot].(map[string]any)
		if !ok {
			continue
		}
		cur, _ := m["endpoints"].([]IfaceEndpoint)
		m["endpoints"] = append(slices.Clone(cur), list...)
	}
	return out
}

// PartitionInterfaces is HTTPInterfacesIn for the server's document meta
// (server.PartitionInterfacesPolicy).
func (p brokerPolicy) PartitionInterfaces(comp string, part util.Partition) map[string]any {
	return p.b.HTTPInterfacesIn(comp, part)
}

// ---- lifecycle ----

// dropPersonalBinds removes every bind drop keeps, across every person's
// record (dryRun: counts only): how many of live records went, and whose
// (each once, sorted). live tells drop whether the row's record is its
// person's now (personalBindsLive); a dead record's rows that go are
// neither counted nor named — its id may be someone else's by now.
func (b *Broker) dropPersonalBinds(drop func(user string, live bool, pb personalBind) bool, dryRun bool) (int64, []string, error) {
	b.pbind.mu.Lock()
	defer b.pbind.mu.Unlock()
	var (
		n      int64
		people []string
		errs   []error
	)
	err := b.eachPersonalBinds(func(f *personalBindsFile) {
		live := b.personalBindsLive(f)
		keep := f.Binds[:0:0]
		for _, pb := range f.Binds {
			if drop(f.User, live, pb) {
				if live {
					n++
					if !slices.Contains(people, f.User) {
						people = append(people, f.User)
					}
				}
				continue
			}
			keep = append(keep, pb)
		}
		if dryRun || len(keep) == len(f.Binds) {
			return
		}
		f.Binds = keep
		if err := b.writePersonalBinds(f); err != nil {
			errs = append(errs, err)
		}
	})
	slices.Sort(people)
	return n, people, errors.Join(append(errs, err)...)
}

// personalBindsProviderMoved drops the personal binds to tile whose person
// no longer owns it (a transfer, 05 §3) and restarts each such person's
// partition of the requester. Owner is read live on every call anyway; this
// removes the rows and the env entries.
func (b *Broker) personalBindsProviderMoved(tile string) {
	if b.Users == nil {
		return
	}
	owner := b.Users.Owner(tile)
	type hit struct{ user, requester string }
	var hits []hit
	n, _, err := b.dropPersonalBinds(func(user string, live bool, pb personalBind) bool {
		switch {
		case pb.Provider != tile:
			return false
		case !live:
			return true // applies to no one: it goes, nothing restarts
		case owner == users.OwnerKindUser+":"+user:
			return false
		}
		hits = append(hits, hit{user, pb.Requester})
		return true
	}, false)
	if err != nil {
		slog.Error("partitions: personal binds of a transferred provider can't all be dropped", "provider", tile, "err", err)
	}
	if n > 0 {
		slog.Info("partitions: personal binds dropped: the provider changed owner", "provider", tile, "binds", n)
	}
	for _, h := range hits {
		b.restartPartition(h.requester, h.user)
		b.publishPersonalBinds(h.requester, h.user)
	}
}

// personalBindsTileCreated drops every personal bind naming path, or a tile
// under it, as requester or provider (assignOwner: a tile created there is a
// new tile, whoever creates it — an admin skips pathLeftovers). Personal
// binds are a person's consent to wire two particular tiles; a tile created
// at a removed one's path inherits none of it, and the person binds again.
// A live person's partition of a requester elsewhere restarts (its env
// listed the provider).
func (b *Broker) personalBindsTileCreated(path string) {
	under := func(p string) bool { return p == path || strings.HasPrefix(p, path+"/") }
	type hit struct{ user, requester string }
	var hits []hit
	n, _, err := b.dropPersonalBinds(func(user string, live bool, pb personalBind) bool {
		if !under(pb.Requester) && !under(pb.Provider) {
			return false
		}
		if live && !under(pb.Requester) {
			hits = append(hits, hit{user, pb.Requester})
		}
		return true
	}, false)
	if err != nil {
		slog.Error("partitions: personal binds naming a new tile's path can't all be dropped", "tile", path, "err", err)
	}
	if n > 0 {
		slog.Info("partitions: personal binds of a removed tile dropped: a new tile took its path", "tile", path, "binds", n)
	}
	for _, h := range hits {
		b.restartPartition(h.requester, h.user)
		b.publishPersonalBinds(h.requester, h.user)
	}
}

// PersonalBindsUserDeleted removes person userID's record, whose uid was
// uid (the users store's delete hook, beside PartitionUserDeleted): nothing
// of it could apply to a person created again under the id (a new uid), and
// this deletes the rows themselves.
func (b *Broker) PersonalBindsUserDeleted(userID, uid string) {
	b.pbind.mu.Lock()
	defer b.pbind.mu.Unlock()
	f, err := b.readPersonalBinds(uid)
	if err != nil || f == nil || f.User != userID {
		return
	}
	f.Binds = nil
	if err := b.writePersonalBinds(f); err != nil {
		slog.Error("partitions: a deleted person's personal binds can't be removed", "user", userID, "err", err)
	}
}

// holdsPersonalBinds: the "personal-binds" store keeps the tile's data when
// a live person's bind names it as the requester (01 §2.2); a deleted
// person's record, or an earlier incarnation's, holds nothing. A record
// that can't be read holds data.
func holdsPersonalBinds(b *Broker, ask registry.PartitionAsk) (bool, error) {
	held := false
	err := b.eachPersonalBinds(func(f *personalBindsFile) {
		if !b.personalBindsLive(f) {
			return
		}
		for _, pb := range f.Binds {
			held = held || pb.Requester == ask.Tile
		}
	})
	return held || err != nil, err
}

// wipePersonalBinds is the "personal-binds" wipe (01 §2.6): a switch
// between user partitions and unpartitioned removes every personal bind of
// the tile — a dead record's too, uncounted and unnamed — counted with the
// registrations; adding or removing "global" keeps people's partitions and
// so their binds (H1).
func wipePersonalBinds(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if t.Kind != wipeEverything {
		return nil
	}
	n, people, err := b.dropPersonalBinds(func(_ string, _ bool, pb personalBind) bool { return pb.Requester == t.Tile }, t.DryRun)
	sum.Registrations += n
	for _, u := range people {
		sum.addPerson(u)
	}
	return err
}

// publishPersonalBinds tells person id's own sockets and requester's frames
// in their partition that its wiring changed (a grants event carrying the
// partition: nobody else's documents reload).
func (b *Broker) publishPersonalBinds(requester, id string) {
	b.Hub.Publish(events.Event{Type: "grants", Component: requester, Partition: string(util.UserPartition(id))})
}

// newBindID is a fresh row id.
func newBindID() string {
	var r [6]byte
	_, _ = rand.Read(r[:])
	return "pb-" + hex.EncodeToString(r[:])
}
