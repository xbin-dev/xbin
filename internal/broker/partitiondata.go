package broker

// partitiondata.go — "holds data" (plans/partitions/01 §2.2, PD-51): whether
// a tile has anything a mode switch would delete, judged from xbind's own
// stores only, never from content a sandbox wrote (a sandbox can't make a
// tile look empty). A tile that holds no data takes the mode its code asks
// for at once; one that does waits for a tile manager. Every store is one
// partitionStore; the planes that land later (partition namespaces, vaults
// and registrations, mail, personal binds, consents, ledgers, …) register
// theirs with registerPartitionStore from an init func, so the answer stays
// complete as they land. When a store can't tell, the tile holds data: the
// rule leans toward asking.

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionStore is one plane's store of a tile's data.
type partitionStore struct {
	name string
	// holds reports whether the store keeps any of ask.Tile's data; an
	// error counts as holding data.
	holds func(b *Broker, ask registry.PartitionAsk) (bool, error)
}

// partitionStores are every store "holds data" asks, in order.
var partitionStores []partitionStore

// registerPartitionStore adds a store; call it from an init func only.
func registerPartitionStore(s partitionStore) { partitionStores = append(partitionStores, s) }

func init() {
	registerPartitionStore(partitionStore{"namespaces", holdsNamespaces})
	registerPartitionStore(partitionStore{"vault", holdsVault})
	registerPartitionStore(partitionStore{"registrations", holdsRegistrations})
}

// tileHoldsData reports whether any store keeps ask.Tile's data, and names
// the first that does.
func (b *Broker) tileHoldsData(ask registry.PartitionAsk) (bool, string) {
	for _, s := range partitionStores {
		held, err := s.holds(b, ask)
		if err != nil {
			slog.Warn("partitions: a store can't tell whether the tile holds data; it does", "tile", ask.Tile, "store", s.name, "err", err)
			return true, s.name
		}
		if held {
			return true, s.name
		}
	}
	return false, ""
}

// holdsNamespaces: the data namespaces of the scope the tile roots — main's
// (data/kv.db buckets res:<scope>/<name>, file volumes under
// data/resources-enc/<ScopeKey>, the plaintext data/resources/<ScopeKey>) and
// every deployment's (data/resources-enc/.deployments/<escS>/<d>). A tile
// that doesn't root its scope holds none of its scope's data. Content is a
// kv key, a volume file beyond gocryptfs's own config, or any entry in the
// plaintext directory: empty volumes a provisioning made don't count.
func holdsNamespaces(b *Broker, ask registry.PartitionAsk) (bool, error) {
	if !ask.RootsScope {
		return false, nil
	}
	scope := ask.Scope
	main, _ := scopeKeys(scope, util.MainDeployment) // main's keys: never an error
	db, err := b.scopeKV(scope, util.MainDeployment, false)
	if err != nil {
		return true, err
	}
	if held, err := kvHasKey(db, []byte(resTarget{Scope: scope}.String())); held || err != nil { // res:<scope>/
		return held, err
	}
	if held, err := volumesHold(filepath.Join(b.Reg.Root, filepath.FromSlash(main.Enc))); held || err != nil {
		return held, err
	}
	// walk-ok: data/resources is xbind's; only the entries' presence is read
	if entries, err := os.ReadDir(filepath.Join(b.Reg.Root, filepath.FromSlash(main.Plain))); err == nil && len(entries) > 0 {
		return true, nil
	}
	if len(escS(scope)) > maxEscS {
		return false, nil // no deployment beyond main can have a namespace (08-data §3.2)
	}
	deps := filepath.Join(b.Reg.Root, "data", "resources-enc", deploymentsLevel, escS(scope))
	entries, err := os.ReadDir(deps) // walk-ok: data/resources-enc is xbind's own
	if err != nil {
		return false, nil
	}
	for _, e := range entries {
		if !e.IsDir() || !util.DeploymentNameOK(e.Name()) {
			return true, nil // something this xbind didn't make: lean toward asking
		}
		db, err := b.scopeKV(scope, e.Name(), false)
		if err != nil {
			return true, err
		}
		if held, err := kvHasKey(db, nil); held || err != nil {
			return held, err
		}
		if held, err := volumesHold(filepath.Join(deps, e.Name(), "fs")); held || err != nil {
			return held, err
		}
	}
	return false, nil
}

// kvHasKey reports whether db has a key in any bucket named with prefix
// (every bucket for nil) — a bucket of one name segment beyond it only, so a
// nested scope's buckets aren't counted.
func kvHasKey(db *bolt.DB, prefix []byte) (bool, error) {
	held := false
	err := kvView(db, func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, bk *bolt.Bucket) error {
			rest, ok := bytes.CutPrefix(name, prefix)
			if !ok || prefix != nil && bytes.IndexByte(rest, '/') >= 0 {
				return nil
			}
			if k, _ := bk.Cursor().First(); k != nil {
				held = true
			}
			return nil
		})
	})
	return held, err
}

// volumesHold reports whether any volume under dir (one ciphertext
// directory per resource) holds a file beyond the gocryptfs.conf and
// gocryptfs.diriv an initialized, empty volume has. A missing dir holds
// nothing.
func volumesHold(dir string) (bool, error) {
	entries, err := os.ReadDir(dir) // walk-ok: data/resources-enc is xbind's; gocryptfs writes it
	if errNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true, nil // a file where only volumes live (a namespace's kv.db is judged by its keys)
		}
		files, err := os.ReadDir(filepath.Join(dir, e.Name())) // walk-ok: a ciphertext directory, entries' names only
		if err != nil {
			return true, err
		}
		for _, f := range files {
			if n := f.Name(); n != "gocryptfs.conf" && n != "gocryptfs.diriv" {
				return true, nil
			}
		}
	}
	return false, nil
}

// holdsVault: a key in the tile's main vault or any of its deployments'.
// A vault that can't be read (sealed) holds data.
func holdsVault(b *Broker, ask registry.PartitionAsk) (bool, error) {
	if _, err := os.Lstat(b.vaultPath(ask.Tile)); err == nil {
		m, err := b.vaultRead(ask.Tile)
		if err != nil || len(m) > 0 {
			return true, nil
		}
	}
	dir := filepath.Join(b.Reg.Root, "data", "vault", deploymentsLevel, util.TileKey(ask.Tile))
	entries, err := os.ReadDir(dir) // walk-ok: data/vault is xbind's own
	if err != nil {
		return false, nil
	}
	for _, e := range entries {
		dep, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !util.DeploymentNameOK(dep) {
			continue
		}
		if m, err := b.vaultReadIn(ask.Tile, dep); err != nil || len(m) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// holdsRegistrations: a cron job or bus subscription of the tile in main's
// stores, an interface instance or ingress host it registered, or any
// registration file of its deployments beyond main (each file is removed
// when it empties).
func holdsRegistrations(b *Broker, ask registry.PartitionAsk) (bool, error) {
	if cr := b.cron; cr != nil {
		cr.mu.Lock()
		for _, j := range cr.jobs {
			if j.Component == ask.Tile {
				cr.mu.Unlock()
				return true, nil
			}
		}
		cr.mu.Unlock()
	}
	if b.bus != nil && len(b.bus.forComponent(ask.Tile)) > 0 {
		return true, nil
	}
	ws := b.Reg.Workspace()
	if len(ws.IfaceInstances[ask.Tile]) > 0 || len(ws.IngressHosts[ask.Tile]) > 0 {
		return true, nil
	}
	for _, dep := range b.deploymentDirs(ask.Tile) {
		f, err := deploymentFiles(ask.Tile, dep)
		if err != nil {
			continue
		}
		for _, name := range []string{depCronFile, depBusFile, "iface-instances.json", "ingress-hosts.json"} {
			if _, err := os.Lstat(filepath.Join(b.Reg.Root, filepath.FromSlash(f.Records), name)); err == nil {
				return true, nil
			}
		}
	}
	return false, nil
}
