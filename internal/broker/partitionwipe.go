package broker

// partitionwipe.go — the wipe executor of a partition mode switch
// (plans/partitions/01 §2.5-§2.6; PD-44, owner ruling H1). A tile manager's
// confirmed switch deletes the tile's data before the recorded mode changes:
// every store is a wipeHook, run in registration order while every instance
// of the tile is stopped, its namespaces held and its backups locked
// (partitionswitch.go keeps that order). This file registers today's stores
// — the data namespaces of the scope the tile roots (main's and every
// deployment's), its vault files and its registrations — through the same
// confined, nofollow removers the offload and the deployment reset use
// (wipeMain/wipeNS, removeScopeData's rules; D78). The planes that land later
// register theirs with registerWipeHook from an init func (partition
// namespaces, vaults, registrations and records, mail, person layers and
// history, consents, ledgers, personal binds), so the list stays complete as
// they land: a switch leaves no trace of the tile's data in any of them.
//
// What a switch deletes depends on the change (owner ruling H1):
//   - user partitions ↔ unpartitioned: everything (wipeEverything);
//   - removing "global" from a partitioned tile: global's data only — its
//     namespace at today's keys (its own resources and the shared ones), its
//     vault and registrations; people's partitions and the deployments
//     beyond main stay (wipeGlobal);
//   - adding "global": nothing — the new global instance starts empty
//     (wipeNone).
//
// A dry run (DryRun) only counts: the confirmation shows what would go.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// wipeKind is what one switch deletes.
type wipeKind uint8

const (
	wipeEverything wipeKind = iota // user partitions ↔ unpartitioned: every item of 01 §2.6
	wipeGlobal                     // removing "global": global's data only (H1)
	wipeNone                       // adding "global": nothing (H1)
)

// wipeKindOf is what switching from R to Q deletes.
func wipeKindOf(from, to registry.PartitionSpec) wipeKind {
	switch {
	case from.User != to.User:
		return wipeEverything
	case from.Global && !to.Global:
		return wipeGlobal
	}
	return wipeNone
}

func (k wipeKind) String() string {
	switch k {
	case wipeGlobal:
		return "global"
	case wipeNone:
		return "none"
	}
	return "everything"
}

// wipeTarget is one switch's wipe as every store sees it.
type wipeTarget struct {
	Tile, Scope string
	// RootsScope: the tile roots its scope, whose data namespaces are then
	// the tile's (a tile that doesn't holds none of them, 01 §2.2).
	RootsScope bool
	From, To   registry.PartitionSpec
	Kind       wipeKind
	By         string
	At         time.Time
	DryRun     bool // count only, delete nothing
}

// wipeSummary is what a switch deleted, or — dry — would delete.
type wipeSummary struct {
	Namespaces    int64 // data namespaces that held content: main's, deployments', people's partitions'
	Partitions    int64 // people's partitions deleted
	VaultKeys     int64 // keys in the vault files removed (a sealed vault's count unknown: 0)
	Registrations int64 // cron jobs, bus subscriptions, interface instances, ingress hosts
	Bytes         int64 // stored bytes removed (ciphertext and plaintext files)
	Subkeys       int64 // backup keys erased (crypto-erasing those archives)
	// People are the users whose partition was deleted: each is told
	// (partitionswitch.go). Hooks of the partition planes add them.
	People []string
}

// counts is the summary as the history's "wiped" and the answers carry it.
func (s *wipeSummary) counts() map[string]int64 {
	return map[string]int64{"namespaces": s.Namespaces, "partitions": s.Partitions, "vaultKeys": s.VaultKeys,
		"registrations": s.Registrations, "bytes": s.Bytes, "subkeys": s.Subkeys}
}

// addPerson records a user whose partition went, once.
func (s *wipeSummary) addPerson(user string) {
	if user != "" && !slices.Contains(s.People, user) {
		s.People = append(s.People, user)
	}
}

// wipeHook is one plane's part of a switch (01 §2.6).
type wipeHook struct {
	name string
	// stop runs first, before anything is held or removed: a plane that runs
	// something for the tile — the partition instances (whose instance
	// tokens it revokes synchronously), a person's terminal — stops it. nil:
	// the plane runs nothing. Not called on a dry run.
	stop func(b *Broker, t wipeTarget)
	// wipe removes the plane's data of t.Tile as t.Kind says — or, on a dry
	// run, only counts it — adding to sum. An error stops the switch: the
	// request stays open and a manager may try again (what was removed stays
	// removed; the manager confirmed deleting it).
	wipe func(b *Broker, t wipeTarget, sum *wipeSummary) error
}

// wipeHooks are every store a switch wipes, in order.
var wipeHooks []wipeHook

// registerWipeHook adds a store; call it from an init func only.
func registerWipeHook(h wipeHook) { wipeHooks = append(wipeHooks, h) }

func init() {
	registerWipeHook(wipeHook{name: "namespaces", wipe: wipeNamespaces})
	registerWipeHook(wipeHook{name: "vault", wipe: wipeVaults})
	registerWipeHook(wipeHook{name: "registrations", wipe: wipeRegistrations})
}

// wipeHeldNamespaces are the namespaces of the tile's scope a switch holds
// (holdNS) while it wipes: main's and, when it deletes everything, every
// deployment's on disk. None for a tile that doesn't root its scope.
func (b *Broker) wipeHeldNamespaces(t wipeTarget) []nsID {
	if !t.RootsScope || t.Kind == wipeNone {
		return nil
	}
	ids := []nsID{nsOf(t.Scope, util.MainDeployment)}
	if t.Kind == wipeEverything {
		for _, dep := range b.namespaceDeps(t.Scope) {
			ids = append(ids, nsOf(t.Scope, dep))
		}
	}
	return ids
}

// namespaceDeps lists the deployment namespaces beyond main that scope has
// on disk (data/resources-enc/.deployments/<escS>/<dep>).
func (b *Broker) namespaceDeps(scope string) []string {
	if scope == "" || len(escS(scope)) > maxEscS {
		return nil
	}
	dir := filepath.Join(b.Reg.Root, "data", "resources-enc", deploymentsLevel, escS(scope))
	entries, _ := os.ReadDir(dir) // walk-ok: data/resources-enc is xbind's own
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != util.MainDeployment && util.DeploymentNameOK(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// ---- namespaces ----

// wipeNamespaces empties the data namespaces of the scope the tile roots:
// main's at today's keys (every kv bucket res:<scope>/<name>, every volume,
// the plaintext directory), and, for wipeEverything, every deployment's
// beyond main, whose ns.json records the reset. The caller holds them.
func wipeNamespaces(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if !t.RootsScope || t.Kind == wipeNone {
		return nil
	}
	var errs []error
	if err := b.wipeMainNamespace(t, sum); err != nil {
		errs = append(errs, fmt.Errorf("main's data: %w", err))
	}
	if t.Kind != wipeEverything {
		return errors.Join(errs...)
	}
	for _, dep := range b.namespaceDeps(t.Scope) {
		if err := b.wipeDeploymentNamespace(t, dep, sum); err != nil {
			errs = append(errs, fmt.Errorf("%s's data: %w", dep, err))
		}
	}
	return errors.Join(errs...)
}

// wipeMainNamespace empties main's namespace of t.Scope, whatever its
// scope.json declares today (a resource it dropped still holds data): the
// kv buckets of one segment below res:<scope>/, and — when the scope holds
// its data key (D118) — its volumes, unmounted and verified first, and its
// plaintext directory. It counts first.
func (b *Broker) wipeMainNamespace(t wipeTarget, sum *wipeSummary) error {
	main, _ := scopeKeys(t.Scope, util.MainDeployment) // main's keys: never an error
	prefix := []byte(resTarget{Scope: t.Scope}.String())
	enc := filepath.Join(b.Reg.Root, filepath.FromSlash(main.Enc))
	plain := filepath.Join(b.Reg.Root, filepath.FromSlash(main.Plain))
	mounts := filepath.Join(b.Reg.Root, ".xbin", "resenc", filepath.FromSlash(main.DirKey))
	ownsDirs := b.Reg.HoldsScopeKey(t.Scope)                  // data/resources*/<key> is the scope's that holds the key
	db, err := b.scopeKV(t.Scope, util.MainDeployment, false) // data/kv.db
	if err != nil {
		return err
	}
	keys, buckets, err := kvCount(db, prefix)
	if err != nil {
		return err
	}
	var size int64
	held := keys > 0
	if ownsDirs {
		vols, err := volumesHold(enc)
		if err != nil {
			return err
		}
		n, _ := treeBytes(enc)
		p, pfiles := treeBytes(plain)
		size = n + p
		held = held || vols || pfiles > 0
	}
	sum.Bytes += size
	if held {
		sum.Namespaces++
	}
	if t.DryRun {
		return nil
	}
	g := b.nsTab().gate(nsOf(t.Scope, util.MainDeployment))
	g.Lock()
	defer g.Unlock()
	var errs []error
	if ownsDirs {
		if err := b.unmountUnder(mounts, main.DirKey); err != nil {
			return err // still mounted: nothing of the files is removed
		}
		errs = append(errs, os.RemoveAll(enc), os.RemoveAll(mounts), os.RemoveAll(plain))
	}
	if db != nil && len(buckets) > 0 {
		errs = append(errs, db.Update(func(tx *bolt.Tx) error {
			for _, name := range buckets {
				if err := tx.DeleteBucket([]byte(name)); err != nil && !errors.Is(err, bolt.ErrBucketNotFound) {
					return err
				}
			}
			return nil
		}))
	}
	return errors.Join(errs...)
}

// wipeDeploymentNamespace empties deployment dep's namespace of t.Scope as a
// reset does (wipe: volumes unmounted first, all but ns.json removed), and
// records the reset in ns.json. It counts first.
func (b *Broker) wipeDeploymentNamespace(t wipeTarget, dep string, sum *wipeSummary) error {
	id := nsOf(t.Scope, dep)
	k, err := id.keys()
	if err != nil {
		return err
	}
	db, err := b.scopeKV(t.Scope, dep, false)
	if err != nil {
		return err
	}
	keys, _, err := kvCount(db, nil)
	if err != nil {
		return err
	}
	dir := filepath.Join(b.Reg.Root, "data", "resources-enc", filepath.FromSlash(k.NS))
	size, _ := treeBytes(dir)
	vols, err := volumesHold(filepath.Join(dir, "fs"))
	if err != nil {
		return err
	}
	sum.Bytes += size
	if keys > 0 || vols {
		sum.Namespaces++
	}
	if t.DryRun {
		return nil
	}
	if err := b.wipe(id); err != nil {
		return err
	}
	at := nowStamp(t.At)
	err = b.updateNS(id, false, func(m *nsMeta) {
		*m = nsMeta{State: nsEmpty, Reset: true, At: at, By: t.By,
			History: append(m.History, nsEvent{Op: "reset", At: at, By: t.By + " (partition mode switch)"})}
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// kvCount counts the keys of db's buckets named with prefix and one name
// segment beyond it (every bucket for nil), and names those buckets.
func kvCount(db *bolt.DB, prefix []byte) (keys int64, buckets []string, err error) {
	err = kvView(db, func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, bk *bolt.Bucket) error {
			rest, ok := bytes.CutPrefix(name, prefix)
			if !ok || prefix != nil && (len(rest) == 0 || bytes.IndexByte(rest, '/') >= 0) {
				return nil
			}
			buckets = append(buckets, string(name))
			keys += int64(bk.Stats().KeyN)
			return nil
		})
	})
	return keys, buckets, err
}

// treeBytes sums the sizes of the regular files under dir and counts them;
// it never follows a link (WalkDir reads entries' own types). A missing or
// unreadable dir counts what it could.
func treeBytes(dir string) (size, files int64) {
	// walk-ok: data/resources*/ and .xbin/resenc are xbind's; only sizes are read, no file is opened
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			size += fi.Size()
			files++
		}
		return nil
	})
	return size, files
}

// ---- vaults ----

// wipeVaults removes the tile's vault files: main's (global's, today's
// file) and, for wipeEverything, every deployment's. A vault that can't be
// read (sealed) is removed all the same; its keys go uncounted.
func wipeVaults(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if t.Kind == wipeNone {
		return nil
	}
	files := []string{b.vaultPath(t.Tile)}
	counts := []func() (map[string]string, error){func() (map[string]string, error) { return b.vaultRead(t.Tile) }}
	if t.Kind == wipeEverything {
		dir := filepath.Join(b.Reg.Root, "data", "vault", deploymentsLevel, util.TileKey(t.Tile))
		entries, err := os.ReadDir(dir) // walk-ok: data/vault is xbind's own
		if err != nil && !errNotExist(err) {
			return err
		}
		for _, e := range entries {
			dep, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || !util.DeploymentNameOK(dep) {
				continue
			}
			files = append(files, filepath.Join(dir, e.Name()))
			counts = append(counts, func() (map[string]string, error) { return b.vaultReadIn(t.Tile, dep) })
		}
	}
	var errs []error
	for i, f := range files {
		if _, err := os.Lstat(f); errNotExist(err) {
			continue
		}
		if m, err := counts[i](); err == nil {
			sum.VaultKeys += int64(len(m))
		}
		if !t.DryRun {
			if err := os.Remove(f); err != nil && !errNotExist(err) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// ---- registrations ----

// wipeRegistrations removes the tile's registrations: its cron jobs and bus
// subscriptions in main's stores, the interface instances and ingress hosts
// main registered in the workspace xbin.json and, for wipeEverything, every
// deployment's registration files and their live rows. Consumers bound to
// the tile are re-bound, ingress reconciled.
func wipeRegistrations(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if t.Kind == wipeNone {
		return nil
	}
	n := b.dropMainRegistrations(t.Tile, t.DryRun)
	ws := b.Reg.Workspace()
	inst, hosts := len(ws.IfaceInstances[t.Tile]), len(ws.IngressHosts[t.Tile])
	n += int64(inst + hosts)
	var errs []error
	if !t.DryRun && inst+hosts > 0 {
		errs = append(errs, b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
			delete(ws.IfaceInstances, t.Tile)
			delete(ws.IngressHosts, t.Tile)
		}))
	}
	if t.Kind == wipeEverything {
		d, err := b.dropDeploymentRegistrations(t.Tile, t.DryRun)
		n += d
		errs = append(errs, err)
	}
	sum.Registrations += n
	if !t.DryRun && n > 0 {
		b.registrationsGone(t.Tile)
	}
	return errors.Join(errs...)
}

// dropMainRegistrations removes the tile's own cron jobs and bus
// subscriptions from main's stores (a nested tile's stay), counting them.
func (b *Broker) dropMainRegistrations(tile string, dry bool) int64 {
	var n int64
	if cr := b.cron; cr != nil {
		var names []string
		cr.mu.Lock()
		for _, j := range cr.jobs {
			if j.Component == tile {
				names = append(names, j.Name)
			}
		}
		cr.mu.Unlock()
		n += int64(len(names))
		if !dry && len(names) > 0 {
			for _, name := range names {
				cr.remove(tile, name)
			}
			cr.persist()
		}
	}
	if bs := b.bus; bs != nil {
		subs := bs.forComponent(tile)
		n += int64(len(subs))
		if !dry && len(subs) > 0 {
			for _, s := range subs {
				bs.remove(tile, s.Name)
			}
			bs.persist()
		}
	}
	return n
}

// dropDeploymentRegistrations removes the registrations of the tile's
// deployments beyond main — their live cron and bus rows and their four
// registration files — counting the rows and the instances and hosts the
// files name. It mirrors dropDormantAt, for the tile alone.
func (b *Broker) dropDeploymentRegistrations(tile string, dry bool) (int64, error) {
	deps, err := b.deploymentDirsErr(tile)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, dep := range deps {
		n += int64(len(b.depInstances(tile, dep)) + len(b.depIngressHosts(tile, dep)))
	}
	if b.cron == nil || b.bus == nil {
		return n, nil
	}
	b.cron.fileMu.Lock()
	defer b.cron.fileMu.Unlock()
	b.bus.fileMu.Lock()
	defer b.bus.fileMu.Unlock()
	b.cron.mu.Lock()
	for key, dj := range b.cron.dep {
		if dj.job.Component == tile {
			n++
			if !dry {
				b.cron.sched.Remove(dj.entry)
				delete(b.cron.dep, key)
			}
		}
	}
	b.cron.mu.Unlock()
	b.bus.mu.Lock()
	for key, st := range b.bus.dep {
		if st.sub.Component == tile {
			n++
			if !dry {
				st.gone, st.queue = true, nil
				delete(b.bus.dep, key)
			}
		}
	}
	b.bus.mu.Unlock()
	if dry {
		return n, nil
	}
	var errs []error
	for _, dep := range deps {
		for _, f := range []string{depCronFile, depBusFile, depIfaceFile, depIngressFile} {
			errs = append(errs, b.removeDeploymentFile(tile, dep, f))
		}
	}
	return n, errors.Join(errs...)
}

// registrationsGone tells what reads the tile's registrations they went:
// consumers bound to it re-read their interface URLs, ingress reconciles.
func (b *Broker) registrationsGone(tile string) {
	ws := b.Reg.Workspace()
	b.rebindConsumers(&ws, tile)
	if b.OnIngressChange != nil {
		b.OnIngressChange()
	}
}
