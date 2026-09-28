package broker

// deployvault.go — the vault of tile deployments (08-data §10) (D127i): one
// file per deployment, reached by the deployment a request addresses; the
// placeholders a non-primary deployment lists, computed from the primary's
// key names and never stored; the vault copy, a tile manager's act; the
// placeholders a reassignment's dry run lists; the legacy migration of the
// vaults beyond main.
//
// main's vault is today's file, data/vault/<CompKey>.json, in today's
// format, whether or not main is the primary (PO-2, PO-3). A tile without a
// deployment record has main alone, its primary, and every vault route
// answers it exactly as before tile deployments (D119c).

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/vault"
)

// depVaultDoc is a vault beyond main on disk,
// data/vault/.deployments/<TileKey>/<name>.json: main's envelope ({"enc":1,
// "data":…}, the whole key→value map sealed with the barrier) plus the tile
// the file belongs to, which every load checks (06-security C10). Without a
// barrier, which only --insecure-vault allows, the map is kept in plain, and
// the barrier's first init seals it, as it seals main's legacy files.
type depVaultDoc struct {
	Tile  string            `json:"tile"`
	Enc   int               `json:"enc,omitempty"`
	Data  []byte            `json:"data,omitempty"`
	Plain map[string]string `json:"plain,omitempty"`
}

// vaultCall is what a vault route acts on: the tile, the deployment whose
// vault it reaches, the key ("" lists), and whether the caller named the
// deployment, whose answer then echoes it (11-contract §8, the echo rule).
type vaultCall struct {
	comp, dep, key string
	named          bool
}

// isMain reports whether dep names main ("" is main, the name rule).
func isMain(dep string) bool { return dep == "" || dep == util.MainDeployment }

// vaultPathIn is where deployment dep of comp keeps its vault: main's is
// today's (vaultPath); any other's is keyed by the tile's TileKey, below a
// level no CompKey can produce (08-data §3.3), which an older xbind never
// reads.
func (b *Broker) vaultPathIn(comp, dep string) (string, error) {
	if isMain(dep) {
		return b.vaultPath(comp), nil
	}
	f, err := deploymentFiles(comp, dep)
	if err != nil {
		return "", err
	}
	return filepath.Join(b.Reg.Root, filepath.FromSlash(f.Vault)), nil
}

// vaultReadIn reads deployment dep of comp's vault: empty when it has none.
func (b *Broker) vaultReadIn(comp, dep string) (map[string]string, error) {
	if isMain(dep) {
		return b.vaultRead(comp)
	}
	p, err := b.vaultPathIn(comp, dep)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	bts, err := os.ReadFile(p) // data/vault is xbind's own: no sandbox sees it
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var doc depVaultDoc
	if err := json.Unmarshal(bts, &doc); err != nil {
		return nil, fmt.Errorf("vault of %s's deployment %s: %w", comp, dep, err)
	}
	if doc.Tile != comp {
		return nil, fmt.Errorf("the vault file of %s's deployment %s names another tile (%q): refused", comp, dep, doc.Tile)
	}
	if doc.Enc > 0 {
		return b.vaultOpen(comp+"+"+dep, doc.Data)
	}
	for k, v := range doc.Plain {
		out[k] = v
	}
	return out, nil
}

// vaultWriteIn replaces deployment dep of comp's vault with m, atomically:
// sealed whenever the barrier is initialized, in plain only under
// --insecure-vault, refused otherwise (vaultSeal).
func (b *Broker) vaultWriteIn(comp, dep string, m map[string]string) error {
	if isMain(dep) {
		return b.vaultWrite(comp, m)
	}
	p, err := b.vaultPathIn(comp, dep)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	ct, err := b.vaultSeal(m)
	if err != nil {
		return err
	}
	doc := depVaultDoc{Tile: comp, Plain: m}
	if ct != nil {
		doc = depVaultDoc{Tile: comp, Enc: 1, Data: ct}
	}
	bts, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(p, bts, 0o600)
}

// ---- who reaches which vault ----

// vaultDeployment picks the deployment of tile a vault request by p
// reaches, and refuses what it may not reach (08-data §4.1, §10; 11-contract
// §0.4, §8):
//   - the tile's own principals reach their bound deployment's vault (DR1),
//     and naming another is refused; a session that follows a protected
//     primary reaches none (D127p);
//   - anyone else reaches ?deployment=<name>, or the primary: its vault as
//     today, admins only; a non-primary deployment's only for a person in
//     their own session who is an admin or manages the tile, since other
//     tiles' credentials never reach a non-primary deployment (D127d).
//
// Frame principals were refused before this (D30). A tile without a record
// answers every request that names no deployment exactly as today.
func (b *Broker) vaultDeployment(w http.ResponseWriter, r *http.Request, p auth.Principal, tile string) (dep string, named, ok bool) {
	name := r.URL.Query().Get("deployment")
	named = name != ""
	if p.Component == tile {
		bound, err := b.addressed(p, tile)
		switch {
		case errors.Is(err, util.ErrNoDeployment):
			server.WriteError(w, http.StatusNotFound, err.Error(), "/docs/protocol.md")
			return "", false, false
		case err != nil:
			server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
			return "", false, false
		case named && name != bound:
			server.WriteError(w, http.StatusForbidden,
				fmt.Sprintf("a tile's own credentials act only on their own deployment (%s)", bound), "/docs/auth.md")
			return "", false, false
		}
		return bound, named, true
	}
	manager, admin := b.MayManageDeployments(p, tile), b.vaultAdmin(p)
	primary := b.primaryOf(tile)
	dep = cmp.Or(name, primary)
	switch {
	case !manager && !admin:
		server.WriteError(w, http.StatusForbidden, errVaultPrivate, "/docs/auth.md")
	case named && !util.DeploymentNameOK(name):
		server.WriteError(w, http.StatusBadRequest,
			`deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`, "/docs/protocol.md")
	case !b.hasDeployment(tile, dep):
		server.WriteError(w, http.StatusNotFound, util.NoDeployment(tile, dep).Error(), "/docs/protocol.md")
	case dep == primary && !admin:
		server.WriteError(w, http.StatusForbidden, errVaultPrivate, "/docs/auth.md")
	case dep != primary && !manager:
		server.WriteError(w, http.StatusForbidden, fmt.Sprintf("%s's deployment %s is reached by the tile's own credentials "+
			"and by people who manage it, in their own session: other tiles' credentials never reach it", tile, dep), "/docs/auth.md")
	default:
		return dep, named, true
	}
	return "", false, false
}

// errVaultPrivate is today's refusal of a caller who is neither the element
// nor an admin, byte for byte.
const errVaultPrivate = "vaults are private to their element; cross-vault access needs xbin:admin"

// vaultAdmin is Broker.IsAdmin on the vault routes: a person, or a tile
// principal acting in its tile's primary. A non-primary deployment's
// credentials never hold governance (05-model §10), whatever grants the tile
// holds.
func (b *Broker) vaultAdmin(p auth.Principal) bool {
	if !b.IsAdmin(p) {
		return false
	}
	if p.Component == "" {
		return true
	}
	dep, err := b.addressed(p, p.Component)
	return err == nil && b.isPrimary(p.Component, dep)
}

// primaryProtected reports whether tile's primary is protected; never for
// a tile without a record.
func (b *Broker) primaryProtected(tile string) bool {
	if f := b.DeploymentSummary; f != nil {
		_, _, protected, ok := f(tile)
		return ok && protected
	}
	return false
}

// vaultWritable refuses a write to a protected primary's vault by anyone
// but its own backend and tile managers in their own session (06-security
// T5.4). The tile's terminal and agent sessions never reach it (D127p); this
// also keeps out other tiles' admin credentials, since no element principal
// passes the manager gate. Every other write passes as it did.
func (b *Broker) vaultWritable(w http.ResponseWriter, p auth.Principal, c vaultCall) bool {
	if !b.isPrimary(c.comp, c.dep) || !b.primaryProtected(c.comp) {
		return true
	}
	if p.Component == c.comp && p.Via == "instance" || b.MayManageDeployments(p, c.comp) {
		return true
	}
	server.WriteError(w, http.StatusForbidden, fmt.Sprintf("the primary of %s (%s) is protected: only tile managers change its code, "+
		"and not from a terminal or agent session — its vault is written only by its own backend and by tile managers in their own session",
		c.comp, cmp.Or(c.dep, util.MainDeployment)), "/docs/auth.md")
	return false
}

// vaultAnswer writes a vault route's answer: v, echoing the deployment when
// the caller named one.
func vaultAnswer(w http.ResponseWriter, c vaultCall, v map[string]any) {
	if c.named {
		v["deployment"] = c.dep
	}
	server.WriteJSON(w, http.StatusOK, v)
}

// vaultOK is a vault write's answer: today's {"ok":"true"}, plus the echo.
func vaultOK(w http.ResponseWriter, c vaultCall) {
	if !c.named {
		server.WriteOK(w)
		return
	}
	vaultAnswer(w, c, map[string]any{"ok": "true"})
}

// valueRefused is D30 per deployment: a secret's value is read only by the
// backend of the deployment whose vault it is — an instance token of the
// tile, bound to that deployment (08-data §10). The binding holds by
// construction, since a tile principal reaches only its own deployment; it
// is checked again all the same.
func valueRefused(p auth.Principal, c vaultCall) bool {
	return p.Component != c.comp || p.Via != "instance" ||
		cmp.Or(p.Deployment, util.MainDeployment) != cmp.Or(c.dep, util.MainDeployment)
}

// ---- placeholders (D127i) ----

// vaultPlaceholders lists the key names the primary's vault holds that m,
// deployment dep's, has no value for, sorted: none for the primary itself.
// Computed on each request from the primary's file, so a key the primary
// gains shows at once and one it drops disappears.
func (b *Broker) vaultPlaceholders(tile, dep string, m map[string]string) ([]string, error) {
	primary := b.primaryOf(tile)
	out := []string{}
	if cmp.Or(dep, util.MainDeployment) == primary {
		return out, nil
	}
	src, err := b.vaultReadIn(tile, primary)
	if err != nil {
		return nil, err
	}
	for k := range src {
		if _, ok := m[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out, nil
}

// placeholderRead answers a value read of a key dep has no value for: the
// placeholder's own 404 when the primary holds it, today's otherwise.
func (b *Broker) placeholderRead(w http.ResponseWriter, c vaultCall) {
	if !b.isPrimary(c.comp, c.dep) {
		if src, err := b.vaultReadIn(c.comp, b.primaryOf(c.comp)); err == nil {
			if _, ok := src[c.key]; ok {
				server.WriteError(w, http.StatusNotFound, fmt.Sprintf("vault key %q has no value in deployment %s; "+
					"a tile manager can copy it from the primary", c.key, c.dep))
				return
			}
		}
	}
	server.WriteError(w, http.StatusNotFound, "no such key")
}

// VaultPlaceholders lists deployment dep of tile's placeholders: the key
// names the primary's vault holds a value for and dep's doesn't, sorted,
// none for the primary. A reassignment's dry run lists its target's, the
// secrets the new primary would start without (08-data §10).
func (b *Broker) VaultPlaceholders(tile, dep string) ([]string, error) {
	m, err := b.vaultReadIn(tile, dep)
	if err != nil {
		return nil, err
	}
	return b.vaultPlaceholders(tile, dep, m)
}

// DeploymentVault is Deployment.vault in the deployments state: how many key
// names deployment dep of tile shows, its values and its placeholders
// together, and how many of them are placeholders (D127i).
func (b *Broker) DeploymentVault(tile, dep string) (deployments.VaultSummary, error) {
	m, err := b.vaultReadIn(tile, dep)
	if err != nil {
		return deployments.VaultSummary{}, err
	}
	ph, err := b.vaultPlaceholders(tile, dep, m)
	if err != nil {
		return deployments.VaultSummary{}, err
	}
	return deployments.VaultSummary{Keys: len(m) + len(ph), Placeholders: len(ph)}, nil
}

// EmptyDeploymentVault empties deployment dep of tile's vault, so every key
// the primary holds is a placeholder again: a reset with vault:true
// (08-data §9.1), main's included while it isn't the primary. The primary's
// vault is never emptied this way. No file is no error.
func (b *Broker) EmptyDeploymentVault(tile, dep string) error {
	dep = cmp.Or(dep, util.MainDeployment)
	if b.isPrimary(tile, dep) {
		return fmt.Errorf("%s is the primary of %s: its vault isn't reset", dep, tile)
	}
	p, err := b.vaultPathIn(tile, dep)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ---- vault copy ----

// vaultCopyWhat names the vault copy in its refusals, as the deployments
// plane names the operation.
const vaultCopyWhat = "copying vault values"

// VaultCopy copies values from tile's primary's vault into deployment
// req.Deployment's (08-data §10) (D127i): the named keys, or all the primary
// holds, overwriting the deployment's own, never toward the primary. It is a
// tile manager's act in a person's own session (05-model §10), judged here
// again whatever the caller judged. It reads and writes through vaultReadIn
// and vaultWriteIn, both atomic. The audit line names keys, never values. A
// dry run answers the same and writes nothing. Its errors are
// *deployments.Error, the vault routes' 503 while the vault is sealed
// among them.
func (b *Broker) VaultCopy(p auth.Principal, req deployments.VaultCopyRequest) (deployments.VaultCopyAnswer, error) {
	fail := func(status int, kind, msg string) (deployments.VaultCopyAnswer, error) {
		return deployments.VaultCopyAnswer{}, &deployments.Error{Status: status, Kind: kind, Msg: msg}
	}
	tile, dep := req.Tile, req.Deployment
	switch {
	case (len(req.Keys) > 0) == req.All:
		return fail(http.StatusBadRequest, "", "vault copy takes exactly one of keys and all")
	case p.ReadOnly():
		return fail(http.StatusForbidden, deployments.KindAuthority, vaultCopyWhat+" is refused in a view-as session: it is read-only")
	case p.Component != "":
		return fail(http.StatusForbidden, deployments.KindAuthority, vaultCopyWhat+
			" is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it")
	case !b.MayManageDeployments(p, tile):
		return fail(http.StatusForbidden, deployments.KindAuthority, vaultCopyWhat+
			" is a tile manager's act: the tile's owner, its org's admins, or a workspace admin")
	case !util.DeploymentNameOK(dep):
		return fail(http.StatusBadRequest, "", `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters`)
	case !b.hasDeployment(tile, dep):
		return fail(http.StatusNotFound, "", util.NoDeployment(tile, dep).Error())
	case b.isPrimary(tile, dep):
		return fail(http.StatusConflict, deployments.KindState, fmt.Sprintf("%s is the primary of %s", dep, tile))
	}
	primary := b.primaryOf(tile)
	src, err := b.vaultReadIn(tile, primary)
	if err != nil {
		return deployments.VaultCopyAnswer{}, vaultCopyError(err)
	}
	dst, err := b.vaultReadIn(tile, dep)
	if err != nil {
		return deployments.VaultCopyAnswer{}, vaultCopyError(err)
	}
	keys := req.Keys
	if req.All {
		keys = make([]string, 0, len(src))
		for k := range src {
			keys = append(keys, k)
		}
		slices.Sort(keys)
	}
	ans := deployments.VaultCopyAnswer{Copied: []string{}, Missing: []string{}}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		if v, ok := src[k]; ok && k != "" {
			dst[k] = v
			ans.Copied = append(ans.Copied, k)
		} else {
			ans.Missing = append(ans.Missing, k)
		}
	}
	if req.DryRun || len(ans.Copied) == 0 {
		return ans, nil
	}
	if err := b.vaultWriteIn(tile, dep, dst); err != nil {
		return deployments.VaultCopyAnswer{}, vaultCopyError(err)
	}
	slog.Info("vault copy", "tile", tile, "from", primary, "to", dep,
		"keys", strings.Join(ans.Copied, ","), "missing", strings.Join(ans.Missing, ","), "by", p.From())
	return ans, nil
}

// vaultCopyError is a vault failure as the vault routes answer it: 503
// while the vault is sealed or unconfigured, 500 otherwise.
func vaultCopyError(err error) error {
	switch {
	case errors.Is(err, vault.ErrSealed):
		return &deployments.Error{Status: http.StatusServiceUnavailable, Kind: deployments.KindState,
			Msg: "vault is sealed — unseal it first (bx vault unseal, or the admin console)"}
	case errors.Is(err, errVaultUnconfigured):
		return &deployments.Error{Status: http.StatusServiceUnavailable, Kind: deployments.KindState, Msg: err.Error()}
	}
	return &deployments.Error{Status: http.StatusInternalServerError, Msg: err.Error()}
}

// ---- legacy migration ----

// migrateDeploymentVaults seals the vaults beyond main written in plain
// under --insecure-vault, once a barrier is first initialized, as
// migrateVaults seals main's (08-data §10). An older binary skips the
// directory, which is harmless. Idempotent.
func (b *Broker) migrateDeploymentVaults() {
	top := filepath.Join(b.Reg.Root, "data", "vault", deploymentsLevel)
	tiles, err := os.ReadDir(top) // walk-ok: data/vault is xbind's own; no sandbox sees it
	if err != nil {
		return
	}
	for _, t := range tiles {
		if !t.IsDir() {
			continue
		}
		dir := filepath.Join(top, t.Name())
		files, err := os.ReadDir(dir) // walk-ok: as above
		if err != nil {
			continue
		}
		for _, f := range files {
			name, ok := strings.CutSuffix(f.Name(), ".json")
			if !ok || !f.Type().IsRegular() || !util.DeploymentNameOK(name) {
				continue
			}
			p := filepath.Join(dir, f.Name())
			bts, err := os.ReadFile(p) // walk-ok: as above
			if err != nil {
				continue
			}
			var doc depVaultDoc
			if json.Unmarshal(bts, &doc) != nil || doc.Enc > 0 || doc.Tile == "" {
				continue
			}
			m := doc.Plain
			if m == nil {
				m = map[string]string{}
			}
			plain, err := json.Marshal(m)
			if err != nil {
				continue
			}
			ct, err := b.barrier.Encrypt(plain)
			if err != nil {
				continue
			}
			out, err := json.MarshalIndent(depVaultDoc{Tile: doc.Tile, Enc: 1, Data: ct}, "", "  ")
			if err == nil && fsutil.WriteFileAtomic(p, out, 0o600) == nil {
				slog.Info("vault: migrated legacy plaintext to encrypted", "tile", doc.Tile, "deployment", name)
			}
		}
	}
}
