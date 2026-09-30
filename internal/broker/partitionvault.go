package broker

// partitionvault.go — a person's partition's own vault (plans/partitions/
// 03 §C; PD-07, S19, C22). A user partition's backend, and its person's
// terminals and agent sessions on the tile, reach that partition's vault,
// data/vault/.partitions/<TileKey>/<dep>/<pkey>.json: the global
// instance's envelope ({"enc":1,"data":…}, sealed with the barrier) plus
// the tile, deployment and partition the file belongs to, which every load
// checks. It is xbind's own and never bound into a sandbox: the vault stays
// broker-only. A secret's value is read only by that partition's backend
// (D30, per partition).
//
// Everyone else reaches today's (global's) vault of a partitioned tile —
// an admin never reaches a person's (PD-07) — and `?partition=` on any vault,
// cron or bus-subscription route of a partitioned tile is refused (400) for
// everyone: a partition's own registrations and vault are reached only from
// inside it. On a tile that isn't partitioned the parameter is ignored, as
// every unknown parameter is (C22). The admins' listing (GET /vaults) shows
// a partitioned tile's global keys and how many people's partitions keep a
// vault, never their key names (S19).

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// partVaultDoc is a person's partition's vault on disk.
type partVaultDoc struct {
	Tile       string            `json:"tile"`
	Deployment string            `json:"deployment"`
	Partition  string            `json:"partition"` // user:<id>
	Enc        int               `json:"enc,omitempty"`
	Data       []byte            `json:"data,omitempty"`
	Plain      map[string]string `json:"plain,omitempty"`
}

// partVaultDir is data/vault/.partitions/<TileKey>.
func (b *Broker) partVaultDir(tile string) string {
	return filepath.Join(b.Reg.Root, "data", "vault", partitionsLevel, util.TileKey(tile))
}

// partVaultPath is user partition pkey of deployment dep of tile's vault
// file.
func (b *Broker) partVaultPath(tile, dep, pkey string) (string, error) {
	dep = cmp.Or(dep, util.MainDeployment)
	if tile == "" || !util.DeploymentNameOK(dep) || !util.PartitionKeyOK(pkey) {
		return "", fmt.Errorf("%s: no partition vault for %q, %q", tile, dep, pkey)
	}
	return filepath.Join(b.partVaultDir(tile), dep, pkey+".json"), nil
}

// readPartVaultDoc reads a partition vault file; ok false when there is
// none.
func readPartVaultDoc(p string) (partVaultDoc, bool, error) {
	var doc partVaultDoc
	data, err := os.ReadFile(p) // data/vault is xbind's own: no sandbox sees it
	if errNotExist(err) {
		return doc, false, nil
	}
	if err != nil {
		return doc, false, err
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return doc, false, fmt.Errorf("a partition vault: %w", err)
	}
	return doc, true, nil
}

// partVaultRead reads t's vault: empty when it has none. A file naming
// another tile, deployment or partition is refused (C10).
func (b *Broker) partVaultRead(t partTarget) (map[string]string, error) {
	p, err := b.partVaultPath(t.tile, t.dep, t.pkey)
	if err != nil {
		return nil, err
	}
	doc, ok, err := readPartVaultDoc(p)
	if err != nil || !ok {
		return map[string]string{}, err
	}
	if doc.Tile != t.tile || doc.Deployment != cmp.Or(t.dep, util.MainDeployment) || doc.Partition != string(t.part) {
		return nil, fmt.Errorf("the vault file of %s's partition %s names another partition: refused", t.tile, t.part)
	}
	return b.openPartVault(doc)
}

// openPartVault is a partition vault file's map.
func (b *Broker) openPartVault(doc partVaultDoc) (map[string]string, error) {
	if doc.Enc > 0 {
		return b.vaultOpen(doc.Tile+"+"+doc.Partition, doc.Data)
	}
	out := map[string]string{}
	for k, v := range doc.Plain {
		out[k] = v
	}
	return out, nil
}

// partVaultWrite replaces t's vault with m, atomically, sealed as every
// vault is (vaultSeal); an empty map removes the file. The partition's
// record is made first (partitionrecords.go).
func (b *Broker) partVaultWrite(t partTarget, m map[string]string) error {
	p, err := b.partVaultPath(t.tile, t.dep, t.pkey)
	if err != nil {
		return err
	}
	if len(m) == 0 {
		if err := os.Remove(p); err != nil && !errNotExist(err) {
			return err
		}
		return nil
	}
	if err := b.notePartition(t, false); err != nil {
		return err
	}
	ct, err := b.vaultSeal(m)
	if err != nil {
		return err
	}
	doc := partVaultDoc{Tile: t.tile, Deployment: cmp.Or(t.dep, util.MainDeployment), Partition: string(t.part), Plain: m}
	if ct != nil {
		doc.Plain, doc.Enc, doc.Data = nil, 1, ct
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(p, data, 0o600)
}

// ---- the vault routes ----

// partitionParamRefused answers 400 when r names a partition (?partition=)
// on a route of tile while tile is partitioned (its recorded mode has user
// partitions, or can't be read): a partition's own vault and registrations
// are reached only from inside it (03 §C, §D). Elsewhere the parameter is
// ignored, as today (C22).
func (b *Broker) partitionParamRefused(w http.ResponseWriter, r *http.Request, tile string) bool {
	if tile == "" || !r.URL.Query().Has("partition") {
		return false
	}
	if _, partitioned, err := b.tilePartitioning(tile); !partitioned && err == nil {
		return false
	}
	server.WriteError(w, http.StatusBadRequest, "a partition's vault and registrations are reached only from inside it: "+
		tile+" keeps each person's apart, and ?partition= names none of them", "/docs/partitions.md")
	return true
}

// vaultPartition settles, in place, which partition's vault call c by p
// reaches: a principal of the tile acting in a person's partition (the
// partition gate stamped it) reaches that partition's; everyone else the
// deployment's, as today. ok false: the refusal is answered.
func (b *Broker) vaultPartition(w http.ResponseWriter, p auth.Principal, c *vaultCall) bool {
	if p.Component != c.comp || !p.Partition.IsUser() {
		return true
	}
	t, err := b.partTargetOf(p, c.comp)
	if err != nil {
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/partitions.md")
		return false
	}
	c.part = &t
	return true
}

// vaultReadCall reads the vault call c reaches.
func (b *Broker) vaultReadCall(c vaultCall) (map[string]string, error) {
	if c.part != nil {
		return b.partVaultRead(*c.part)
	}
	return b.vaultReadIn(c.comp, c.dep)
}

// vaultWriteCall replaces the vault call c reaches with m.
func (b *Broker) vaultWriteCall(c vaultCall, m map[string]string) error {
	if c.part != nil {
		return b.partVaultWrite(*c.part, m)
	}
	return b.vaultWriteIn(c.comp, c.dep, m)
}

// partitionVaults counts the people's partitions of tile that keep a vault
// file, for the admins' listing: a count, never key names (S19).
func (b *Broker) partitionVaults(tile string) int {
	n := 0
	deps, _ := os.ReadDir(b.partVaultDir(tile)) // walk-ok: data/vault is xbind's own
	for _, d := range deps {
		if !d.IsDir() || !util.DeploymentNameOK(d.Name()) {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(b.partVaultDir(tile), d.Name())) // walk-ok: as above
		for _, f := range files {
			if pkey, ok := strings.CutSuffix(f.Name(), ".json"); ok && util.PartitionKeyOK(pkey) {
				n++
			}
		}
	}
	return n
}

// ---- holds data and the switch's wipe ----

// holdsPartitionVault: a person's partition of the tile keeps a vault file
// (each is removed when it empties). A directory that can't be read holds
// data.
func holdsPartitionVault(b *Broker, ask registry.PartitionAsk) (bool, error) {
	ents, err := os.ReadDir(b.partVaultDir(ask.Tile)) // walk-ok: data/vault is xbind's own
	switch {
	case errNotExist(err):
		return false, nil
	case err != nil:
		return true, err
	}
	return len(ents) > 0, nil
}

// wipePartitionVaults removes every person's partition vault of the tile on
// a switch that deletes everything (H1: "global" coming or going keeps
// them), counting their keys (a sealed vault's go uncounted) and people.
func wipePartitionVaults(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if t.Kind != wipeEverything {
		return nil
	}
	dir := b.partVaultDir(t.Tile)
	var errs []error
	deps, err := os.ReadDir(dir) // walk-ok: data/vault is xbind's own
	if errNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	for _, d := range deps {
		files, err := os.ReadDir(filepath.Join(dir, d.Name())) // walk-ok: as above
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, f := range files {
			doc, ok, err := readPartVaultDoc(filepath.Join(dir, d.Name(), f.Name()))
			if err != nil || !ok {
				continue
			}
			if id, isUser := util.Partition(doc.Partition).User(); isUser {
				sum.addPerson(id)
			}
			if m, err := b.openPartVault(doc); err == nil {
				sum.VaultKeys += int64(len(m))
			}
		}
	}
	if t.DryRun {
		return errors.Join(errs...)
	}
	return errors.Join(append(errs, os.RemoveAll(dir))...)
}
