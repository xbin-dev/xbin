package broker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// Per-component backup / archive (plans/lifecycle.md). xbind builds a
// self-describing tar of a component (source + its scope's resource data +
// terminal env layer) and streams it to the archiver tile bound to its
// `@archive` interface; the same path powers offload and scheduled backups.
// Restore reconstructs the component from the archive alone — no local metadata.

const archiveSlot = "@archive"

// archiveProvider resolves the archiver bound for a component: its own
// `@archive` override, else the workspace default (bindings["*"]["@archive"]).
func (b *Broker) archiveProvider(comp string) string {
	ws := b.Reg.Workspace()
	if p := ws.Bindings[comp][archiveSlot].First(); p != "" {
		return p
	}
	return ws.Bindings["*"][archiveSlot].First()
}

// backupKey is a component's stable archive key (also its restore identity is in
// the manifest, so DR can map keys→components by reading each archive).
func backupKey(comp string) string { return util.CompKey(comp) }

func (b *Broker) resourcesRoot(scope string) string {
	k, _ := scopeKeys(scope, util.MainDeployment) // main's keys: never an error
	return filepath.Join(b.Reg.Root, filepath.FromSlash(k.Plain))
}

// scopeDataKey is scope's data key in main, as the D118 refusals name it.
func scopeDataKey(scope string) string {
	k, _ := scopeKeys(scope, util.MainDeployment)
	return k.DirKey
}

func (b *Broker) termDir(comp string) string {
	return filepath.Join(b.Reg.Root, ".xbin", "term", util.CompKey(comp))
}

// --- build ------------------------------------------------------------------

// writeBackup streams a component's backup tar into bw. Scope (owner decision,
// LC-2): source + the scope's resource data (when the component roots its scope)
// + the terminal env layer. Excludes the env layer (rebuilt), logs, and vault.
// Its data is main's namespace, as main's code declares it, whichever
// deployment is the primary; archives lists the deployment archives written
// before it, which a tile with a record names in its manifest. A split
// archive (a sealed workspace's, schema 3; split non-nil) holds no data: it
// names the data archive written before it, if any (backup_seal.go).
func (b *Broker) writeBackup(bw *backup.Writer, c *registry.Component, archives map[string]string, split *splitBackup) error {
	scope, isRoot := b.Reg.Scopes()[c.Path]
	includes := []string{"source"}
	m := backup.Manifest{
		Component: c.Path, Scope: c.Path, ScopeRoot: isRoot,
		XBinVersion: b.Version, Created: time.Now().UTC().Format(time.RFC3339),
	}
	if split != nil {
		m.Schema, m.Data, m.BackupID = backup.SchemaSplit, split.data, split.id
	}
	if isRoot {
		scope = b.mainDeclared(c.Path, scope)
		m.Resources = map[string]string{}
		for name, res := range scope.Resources {
			m.Resources[name] = res.Type
		}
		includes = append(includes, "data")
	} else {
		m.Scope = c.Scope // data belongs to an ancestor scope; not this component's to hold
	}
	if _, err := os.Stat(b.termDir(c.Path)); err == nil {
		includes = append(includes, "term-env")
	}
	if jobs := b.cronJobsFor(c.Path); len(jobs) > 0 {
		m.CronJobs = jobs
	}
	for _, s := range b.bus.forComponent(c.Path) {
		if raw, err := json.Marshal(s); err == nil {
			m.BusSubs = append(m.BusSubs, raw)
		}
	}
	// A manager tile's sandbox definitions — never their state (§9).
	if h := b.tileSandboxes(); h != nil {
		if m.Sandboxes = h.Defs(c.Path); len(m.Sandboxes) > 0 {
			includes = append(includes, "sandboxes")
		}
	}
	m.Includes = includes
	dep := b.deploymentBackupFor(c.Path) // nil in the zero state (backup_deploy.go)
	dep.section(&m, archives)
	if err := bw.Manifest(m); err != nil {
		return err
	}
	if err := dep.write(bw); err != nil {
		return err
	}

	// Source subtree — skip reproducible/history dirs (git owns history). The
	// tile's dir is reached from the workspace without symlinks: a nested
	// tile lives in its parent's writable tree, which could swap it for a link.
	if err := bw.TreeIn(backup.SourcePrefix, b.Reg.Root, c.Path, skipSource); err != nil {
		return err
	}
	// Resource data (only when this component roots its scope, and inline
	// only in a plaintext workspace's archive).
	if isRoot && split == nil {
		if err := b.writeScopeData(bw, c.Path, scope); err != nil {
			return err
		}
	}
	// Terminal dev layer — without a VM terminal's disk image (vm/): a sparse
	// multi-GiB file, live while its VM runs (plans/vm-sandbox.md).
	if err := bw.Tree(backup.TermPrefix, b.termDir(c.Path), func(rel string) bool { return rel == "vm" }); err != nil {
		return err
	}
	return nil
}

func skipSource(rel string) bool {
	top := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		top = rel[:i]
	}
	// Keep .git — a component is its own repo now (history + remote travel with
	// the backup, so a restore is a full, re-pullable clone). Still drop
	// node_modules (reproducible) and .xbin (runtime layers).
	return top == "node_modules" || top == ".xbin"
}

func (b *Broker) writeScopeData(bw *backup.Writer, scopePath string, scope *registry.ScopeManifest) error {
	// Data is read through the decrypted view (the archive is sealed under a
	// backup key instead, backup_seal.go); that needs the vault unsealed.
	if b.vaultSealed() {
		return fmt.Errorf("vault sealed — unseal before backing up encrypted resources")
	}
	kv := map[string]map[string]string{} // resource -> {key: base64(value)}
	for name, res := range scope.Resources {
		k, err := b.resKeys(resTarget{Scope: scopePath, Name: name}, util.MainDeployment)
		if err != nil {
			return err
		}
		switch res.Type {
		case "sqlite":
			// sqlite is a gocryptfs mount dir like filesystem (it holds the db,
			// but a tile may keep other files there too — e.g. use it as $HOME),
			// so back up the whole decrypted dir, not just <name>.sqlite*.
			if err := bw.Tree(backup.SQLitePrefix+name+"/", b.resMount(k, false), nil); err != nil {
				return err
			}
		case "filesystem":
			if err := bw.Tree(backup.FSPrefix+name+"/", b.resMount(k, false), nil); err != nil {
				return err
			}
		case "blob":
			if err := bw.Tree(backup.BlobPrefix+name+"/", b.resMount(k, false), nil); err != nil {
				return err
			}
		case "kv":
			kv[name] = b.dumpKV(k)
		}
	}
	if len(kv) > 0 {
		j, _ := json.Marshal(kv)
		return bw.File(backup.KVName, 0o644, j)
	}
	return nil
}

// addFile tars an on-disk file if it exists (sidecars like -wal may be absent).
func addFile(bw *backup.Writer, tarName, osPath string) error {
	f, err := os.Open(osPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return bw.Stream(tarName, 0o644, fi.Size(), f)
}

// dumpKV reads every key of one kv resource's bucket into {key:
// base64(value)}.
func (b *Broker) dumpKV(rk resKeys) map[string]string {
	out := map[string]string{}
	db, err := b.kvDB(rk, false)
	if err != nil || db == nil {
		return out
	}
	_ = db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(rk.Bucket))
		if bk == nil {
			return nil
		}
		return bk.ForEach(func(k, v []byte) error {
			// Store decrypted values: the tar holds them plain, and the
			// archive is sealed under a backup key (backup_seal.go).
			pv, err := b.decodeKV(rk.KVLabel, append([]byte(nil), v...))
			if err != nil {
				return err // sealed / undecodable — abort the backup
			}
			out[string(k)] = base64.StdEncoding.EncodeToString(pv)
			return nil
		})
	})
	return out
}

func (b *Broker) cronJobsFor(comp string) []json.RawMessage {
	if b.cron == nil {
		return nil
	}
	b.cron.mu.Lock()
	defer b.cron.mu.Unlock()
	var out []json.RawMessage
	for _, j := range b.cron.jobs {
		if j.Component == comp {
			if raw, err := json.Marshal(j); err == nil {
				out = append(out, raw)
			}
		}
	}
	return out
}

// --- archiver dispatch ------------------------------------------------------

// archiveDo calls the archiver's API internally, as the owner (admin), routed
// through the proxy (so it spawns + streams like any element call). The request
// body streams (backups aren't buffered); the response is small for PUT/list and
// buffered for a version fetch (fine — restores are occasional).
func (b *Broker) archiveDo(method, provider, apiPath string, body io.Reader) (int, []byte, error) {
	if b.ProxyHandler == nil {
		return 0, nil, fmt.Errorf("no proxy wired")
	}
	req := httptest.NewRequest(method, "/api/"+provider+apiPath, body)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Owner: true}))
	rec := httptest.NewRecorder()
	b.ProxyHandler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

// doBackup builds a component's tar and PUTs it to its archiver, returning the
// version the archiver assigned.
func (b *Broker) doBackup(comp string) (string, error) {
	v, _, err := b.backupTile(comp, false)
	return v, err
}

// backupTile archives comp (08-data §11.1): first the deployment archives —
// the primary's when it isn't main, and with every (an offload) each other
// deployment's whose namespace holds data — then, in a sealed workspace, the
// data archive of a scope root's main data (when its scope declares any
// resource), then the main archive, which lists them. It answers the main
// archive's version and the deployment archives'; a failed PUT fails it
// before anything later is written. It holds comp's backup lock
// (backup_prune.go).
func (b *Broker) backupTile(comp string, every bool) (string, map[string]string, error) {
	defer b.holdBackups(comp)()
	return b.backupTileHeld(comp, every)
}

// backupTileHeld is backupTile for a caller holding comp's backup lock. A
// data archive whose main archive fails stays: the archiver may have
// stored the main archive all the same, and a retention run deletes it
// when no kept main archive names it (pruneData).
func (b *Broker) backupTileHeld(comp string, every bool) (string, map[string]string, error) {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return "", nil, fmt.Errorf("no such component %q", comp)
	}
	provider := b.archiveProvider(comp)
	if provider == "" {
		return "", nil, fmt.Errorf("no archiver bound — set one: bx bind %q %s=<archiver> (or bind '*' for a default)", comp, archiveSlot)
	}
	split, err := b.sealing() // a sealed vault fails the backup before anything is written
	if err != nil {
		return "", nil, err
	}
	archives, err := b.putDeploymentArchives(c, provider, every)
	if err != nil {
		return "", nil, err
	}
	var sp *splitBackup
	if split {
		if sp, err = b.putDataArchive(c, provider); err != nil {
			return "", nil, err
		}
	}
	v, err := b.putArchive(provider, backupKey(comp), mainSeal(comp), func(bw *backup.Writer) error { return b.writeBackup(bw, c, archives, sp) })
	if err == nil && sp != nil && archiveVersion.MatchString(v) {
		b.noteDataRef(provider, comp, v, dataRefOf(sp.data))
	}
	return v, archives, err
}

// restored is what a restore brought back: the archive's manifest, and the
// tile sandbox definitions it left out (by name and why).
type restored struct {
	backup.Manifest
	SandboxesSkipped []string
	DataErased       string // why a split archive's data isn't restored: its key was erased
	DataMissing      string // why it isn't: its data archive is gone, its key not erased
}

// doRestore fetches a version's tar from the archiver and unpacks it. version ""
// means the latest. The tile's backend and its tile sandboxes are stopped
// first (their state is kept: a restore never touches it).
func (b *Broker) doRestore(comp, version string) (restored, error) {
	r, _, err := b.restoreTile(comp, version)
	return r, err
}

// restoreTile is doRestore, then the deployment archives the main archive
// lists, each into its deployment when that exists (listedRestore).
func (b *Broker) restoreTile(comp, version string) (restored, *listedRestore, error) {
	provider := b.archiveProvider(comp)
	if provider == "" {
		return restored{}, nil, fmt.Errorf("no archiver bound for %q", comp)
	}
	if version == "" {
		version = "latest"
	}
	body, err := b.fetchArchive(provider, backupKey(comp), version)
	if err != nil {
		return restored{}, nil, err
	}
	// Both archives are opened — a sealed one authenticated whole — before
	// anything stops or is written.
	br, err := b.openArchive(body)
	if err != nil {
		return restored{}, nil, err
	}
	var data *backup.Reader
	var gone error
	if br.M.Data != nil && !br.M.DataArchive() {
		if data, gone, err = b.openDataArchive(provider, comp, br.M); err != nil {
			return restored{Manifest: br.M}, nil, err
		}
	}
	b.StopBackendSafe(comp)
	b.stopTileSandboxes(comp, "its tile was restored from a backup: stopped, state kept")
	m, err := b.restoreFrom(comp, br, data, b.deploymentStateRestorer())
	if err != nil {
		return restored{Manifest: m}, nil, err
	}
	r := restored{Manifest: m, SandboxesSkipped: b.restoreSandboxes(m)}
	if gone != nil {
		why := gone.Error() + ": its source and terminal layer were restored, its data wasn't"
		if errors.As(gone, new(erasedError)) {
			r.DataErased = why
		} else {
			r.DataMissing = why
		}
		r.Includes = slices.DeleteFunc(slices.Clone(r.Includes), func(p string) bool { return p == "data" })
		slog.Warn("restore: the archive's data is erased or missing; source and terminal layer restored", "component", comp, "why", gone)
	}
	if m.Deployments == nil || len(m.Deployments.Archives) == 0 {
		return r, nil, nil
	}
	return r, b.restoreListed(comp, m.Deployments.Archives), nil
}

// restoreSandboxes merges a restored tile's sandbox definitions by uid
// (§9); what it skipped is logged and answered.
func (b *Broker) restoreSandboxes(m backup.Manifest) []string {
	h := b.tileSandboxes()
	if h == nil || len(m.Sandboxes) == 0 {
		return nil
	}
	skipped := h.RestoreDefs(m.Component, m.Sandboxes)
	for _, s := range skipped {
		slog.Warn("restore: a tile sandbox definition was left out", "component", m.Component, "sandbox", s)
	}
	return skipped
}

func (b *Broker) StopBackendSafe(comp string) {
	if b.StopBackend != nil {
		b.StopBackend(comp)
	}
}

// --- offload (backup, then free local bytes) --------------------------------

// offload archives a component, then removes its local data (and, when full,
// its source + terminal env layer). It NEVER removes anything before the archive
// PUT is confirmed. full=false keeps source + term-env (LC-1: two depths).
// Every deployment stops, and every data namespace of comp's deployments is
// archived, under its write gate, before anything is removed (08-data
// §11.5); beyond main only the kv files go, which a deployment archive holds
// whole.
//
// full takes the terminal layer out of use first (HoldTermEnv: its sessions
// killed, the layer held so none mounts it until the offload is done) — a
// session that won't let go fails the offload there, after the archive, with
// nothing removed — then removes it in a confined run (removeTree, WP-9b). A
// layer that removal leaves behind is replaced whole by the restore.
func (b *Broker) offload(comp string, full bool) error {
	if err := cmpErr(b.partitionOffloadCheck(comp), b.sandboxOffloadCheck(comp)); err != nil { // partitioned tiles (partitionops.go)
		return err // nothing archived, nothing stopped (tilesbx_hooks.go)
	}
	b.StopBackendSafe(comp)
	defer b.gateNamespaces(comp)()
	_, archived, err := b.backupTile(comp, true)
	if err != nil {
		return fmt.Errorf("archive before offload failed (nothing removed): %w", err)
	}
	if full && b.HoldTermEnv != nil {
		release, err := b.HoldTermEnv(comp)
		if err != nil {
			return fmt.Errorf("archived, but its terminal layer is still in use (nothing removed): %w", err)
		}
		defer release()
	}
	if err := b.removeScopeData(comp); err != nil {
		return err
	}
	if err := b.dropNamespaceKV(comp, archived); err != nil {
		return err
	}
	if full {
		if err := b.removeTree(b.termDir(comp)); err != nil {
			slog.Warn("offload: removing the terminal layer", "component", comp, "err", err)
		}
		if err := b.removeSourceBulk(comp); err != nil {
			return err
		}
	}
	return nil
}

// removeScopeData deletes a component's resource data (only if it roots its
// scope) — the sqlite/blob files and the kv buckets.
func (b *Broker) removeScopeData(comp string) error {
	scope, isRoot := b.Reg.Scopes()[comp]
	if !isRoot {
		return nil
	}
	scope = b.mainDeclared(comp, scope)
	if db, _ := b.scopeKV(comp, util.MainDeployment, false); db != nil { // main's: data/kv.db
		_ = db.Update(func(tx *bolt.Tx) error {
			for name, res := range scope.Resources {
				if k, err := b.resKeys(resTarget{Scope: comp, Name: name}, util.MainDeployment); err == nil && res.Type == "kv" {
					_ = tx.DeleteBucket([]byte(k.Bucket))
				}
			}
			return nil
		})
	}
	if !b.Reg.HoldsScopeKey(comp) {
		return nil // data/resources/<key> belongs to the scope that holds the key (D118)
	}
	return os.RemoveAll(b.resourcesRoot(comp))
}

// removeSourceBulk clears a component's source subtree but keeps a stub
// (xbin.json + scope.json) so it stays listed and restorable (offloaded-full).
// The tile's dir is reached without symlinks and cleared through an os.Root,
// so a link planted in place of it (a nested tile's parent can) never turns
// the clearing onto whatever the link points at.
func (b *Broker) removeSourceBulk(comp string) error {
	if _, ok := b.Reg.Component(comp); !ok {
		return nil
	}
	r, err := fsutil.OpenRootIn(b.Reg.Root, comp)
	if err != nil {
		return err
	}
	defer r.Close()
	d, err := r.Open(".")
	if err != nil {
		return err
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return err
	}
	keep := map[string]bool{"xbin.json": true, "scope.json": true}
	for _, name := range names {
		if keep[name] {
			continue
		}
		if err := r.RemoveAll(name); err != nil {
			return err
		}
	}
	return nil
}

// --- API --------------------------------------------------------------------

func (b *Broker) apiBackupNow(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var body struct{ Component string }
	if err := server.DecodeJSON(r, &body); err != nil || body.Component == "" {
		server.WriteError(w, http.StatusBadRequest, "need {component}")
		return
	}
	version, err := b.doBackup(body.Component)
	if err != nil {
		server.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true", "version": version})
}

func (b *Broker) apiBackupList(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	comp := r.URL.Query().Get("component")
	provider := b.archiveProvider(comp)
	if comp == "" || provider == "" {
		server.WriteJSON(w, http.StatusOK, map[string]any{"versions": []any{}, "archiver": provider})
		return
	}
	code, body, err := b.archiveDo("GET", provider, "/archive/"+backupKey(comp)+"/versions", nil)
	if err != nil || code >= 400 {
		server.WriteError(w, http.StatusBadGateway, "archiver: "+firstLine(string(body)))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body) // pass the archiver's version list through
}

func (b *Broker) apiRestore(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var body struct{ Component, Version, File string }
	if err := server.DecodeJSON(r, &body); err != nil || body.Component == "" {
		server.WriteError(w, http.StatusBadRequest, "need {component, version?, file?}")
		return
	}
	// Restore a single file: stream it back without touching live state.
	// xbind extracts it (an archiver can't read a sealed archive).
	if body.File != "" {
		ver := body.Version
		if ver == "" {
			ver = "latest"
		}
		data, code, err := b.extractMember(body.Component, ver, body.File)
		if err != nil {
			server.WriteError(w, code, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
		return
	}
	m, listed, err := b.restoreTile(body.Component, body.Version)
	if err != nil {
		server.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	// A restored component is enabled + rescanned/provisioned.
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { delete(ws.Lifecycle, m.Component) })
	_ = b.Reg.Rescan()
	b.Provision()
	if b.OnStructureChange != nil {
		b.OnStructureChange()
	}
	out := map[string]any{"ok": true, "component": m.Component, "restored": m.Includes}
	if listed != nil { // the deployment archives the main archive lists: restored, or why not
		out["deployments"] = listed
	}
	m.answer(out)
	server.WriteJSON(w, http.StatusOK, out)
}

// answer adds what a restore left out to its answer: sandboxesSkipped,
// dataErased, dataMissing (POST /restore, POST /lifecycle's enable).
func (r restored) answer(out map[string]any) {
	if len(r.SandboxesSkipped) > 0 {
		out["sandboxesSkipped"] = r.SandboxesSkipped
	}
	if r.DataErased != "" {
		out["dataErased"] = r.DataErased
	}
	if r.DataMissing != "" {
		out["dataMissing"] = r.DataMissing
	}
}
