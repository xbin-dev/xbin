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
// rule leans toward asking. Only a missing file or directory is "nothing
// there"; an EACCES or an EIO is "can't tell", never "no data".

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/vault"
)

// partitionStore is one plane's store of a tile's data.
type partitionStore struct {
	name string
	// holds reports whether the store keeps any of ask.Tile's data; an
	// error counts as holding data (vault.ErrSealed: until the vault
	// unseals, see tileHoldsData).
	holds func(b *Broker, ask registry.PartitionAsk) (bool, error)
}

// partitionStores are every store "holds data" asks, in order.
var partitionStores []partitionStore

// registerPartitionStore adds a store; call it from an init func only.
func registerPartitionStore(s partitionStore) { partitionStores = append(partitionStores, s) }

func init() {
	registerPartitionStore(partitionStore{"lifecycle", holdsOffloaded})
	registerPartitionStore(partitionStore{"namespaces", holdsNamespaces})
	registerPartitionStore(partitionStore{"vault", holdsVault})
	registerPartitionStore(partitionStore{"registrations", holdsRegistrations})
}

// tileHoldsData reports whether any store keeps ask.Tile's data and names
// the first that does. sealed says the answer is a sealed vault's alone —
// it holds data until the vault unseals and says otherwise — so a settle
// pauses the tile without recording a request on it.
func (b *Broker) tileHoldsData(ask registry.PartitionAsk) (held bool, store string, sealed bool) {
	for _, s := range partitionStores {
		held, err := s.holds(b, ask)
		if errors.Is(err, vault.ErrSealed) {
			sealed, store = true, s.name
			continue // another store may hold data for sure
		}
		if err != nil {
			slog.Warn("partitions: a store can't tell whether the tile holds data; it does", "tile", ask.Tile, "store", s.name, "err", err)
			return true, s.name, false
		}
		if held {
			return true, s.name, false
		}
	}
	return sealed, store, sealed
}

// absent reports whether err says a file or directory isn't there: nothing
// stored. Every other error is "can't tell".
func absent(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// holdsOffloaded: an offloaded tile's data sits in its offload archive,
// which a restore brings back, while its kv buckets and namespaces are gone
// from the host — data all the same.
func holdsOffloaded(b *Broker, ask registry.PartitionAsk) (bool, error) {
	return registry.IsOffloaded(b.Reg.LifecycleState(ask.Tile)), nil
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
	switch entries, err := os.ReadDir(filepath.Join(b.Reg.Root, filepath.FromSlash(main.Plain))); {
	case err != nil && !absent(err):
		return true, err
	case len(entries) > 0:
		return true, nil
	}
	if len(escS(scope)) > maxEscS {
		return false, nil // no deployment beyond main can have a namespace (08-data §3.2)
	}
	deps := filepath.Join(b.Reg.Root, "data", "resources-enc", deploymentsLevel, escS(scope))
	entries, err := os.ReadDir(deps) // walk-ok: data/resources-enc is xbind's own
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return true, err
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
	if absent(err) {
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
// A vault that can't be read holds data; one the sealed barrier keeps
// closed answers vault.ErrSealed (known once it unseals).
func holdsVault(b *Broker, ask registry.PartitionAsk) (bool, error) {
	if _, err := os.Lstat(b.vaultPath(ask.Tile)); err == nil {
		if held, err := vaultHolds(b.vaultRead(ask.Tile)); held || err != nil {
			return held, err
		}
	} else if !absent(err) {
		return true, err
	}
	dir := filepath.Join(b.Reg.Root, "data", "vault", deploymentsLevel, util.TileKey(ask.Tile))
	entries, err := os.ReadDir(dir) // walk-ok: data/vault is xbind's own
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	for _, e := range entries {
		dep, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !util.DeploymentNameOK(dep) {
			continue
		}
		if held, err := vaultHolds(b.vaultReadIn(ask.Tile, dep)); held || err != nil {
			return held, err
		}
	}
	return false, nil
}

// vaultHolds judges one vault read: a key holds data, and so does a vault
// that can't be read (a sealed barrier's vault.ErrSealed is passed on).
func vaultHolds(m map[string]string, err error) (bool, error) {
	if err != nil {
		return true, err
	}
	return len(m) > 0, nil
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
	deps, err := b.deploymentDirsErr(ask.Tile)
	if err != nil {
		return true, err
	}
	for _, dep := range deps {
		f, err := deploymentFiles(ask.Tile, dep)
		if err != nil {
			return true, err // deploymentDirsErr lists valid names only
		}
		for _, name := range []string{depCronFile, depBusFile, "iface-instances.json", "ingress-hosts.json"} {
			if _, err := os.Lstat(filepath.Join(b.Reg.Root, filepath.FromSlash(f.Records), name)); err == nil {
				return true, nil
			} else if !absent(err) {
				return true, err
			}
		}
	}
	return false, nil
}

// deploymentDirsErr lists the deployment directories data/deployments keeps
// beside tile's record, whatever the record says: none when the tile has no
// directory there, an error when it can't be read.
func (b *Broker) deploymentDirsErr(tile string) ([]string, error) {
	dir := filepath.Join(b.Reg.Root, "data", "deployments", util.TileKey(tile))
	entries, err := os.ReadDir(dir) // walk-ok: data/deployments is xbind's own; no tile writes there
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != util.MainDeployment && util.DeploymentNameOK(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
