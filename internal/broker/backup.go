package broker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/backup"
	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/util"
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
	return filepath.Join(b.Reg.Root, "data", "resources", util.ScopeKey(scope))
}

func (b *Broker) termDir(comp string) string {
	return filepath.Join(b.Reg.Root, ".xbin", "term", util.CompKey(comp))
}

// --- build ------------------------------------------------------------------

// writeBackup streams a component's backup tar into bw. Scope (owner decision,
// LC-2): source + the scope's resource data (when the component roots its scope)
// + the terminal env layer. Excludes the env layer (rebuilt), logs, and vault.
func (b *Broker) writeBackup(bw *backup.Writer, c *registry.Component) error {
	scope, isRoot := b.Reg.Scopes()[c.Path]
	includes := []string{"source"}
	m := backup.Manifest{
		Component: c.Path, Scope: c.Path, ScopeRoot: isRoot,
		XBinVersion: b.Version, Created: time.Now().UTC().Format(time.RFC3339),
	}
	if isRoot {
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
	m.Includes = includes
	if err := bw.Manifest(m); err != nil {
		return err
	}

	// Source subtree — skip reproducible/history dirs (git owns history).
	if err := bw.Tree(backup.SourcePrefix, c.Dir, skipSource); err != nil {
		return err
	}
	// Resource data (only when this component roots its scope).
	if isRoot {
		if err := b.writeScopeData(bw, c.Path, scope); err != nil {
			return err
		}
	}
	// Terminal dev layer.
	if err := bw.Tree(backup.TermPrefix, b.termDir(c.Path), nil); err != nil {
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
	// Backups are plaintext (encryption is the archiver's job — plans/vault-data.md),
	// so we read through the decrypted view; that needs the vault unsealed.
	if b.vaultSealed() {
		return fmt.Errorf("vault sealed — unseal before backing up encrypted resources")
	}
	kv := map[string]map[string]string{} // resource -> {key: base64(value)}
	for name, res := range scope.Resources {
		switch res.Type {
		case "sqlite":
			// sqlite is a gocryptfs mount dir like filesystem (it holds the db,
			// but a tile may keep other files there too — e.g. use it as $HOME),
			// so back up the whole decrypted dir, not just <name>.sqlite*.
			if err := bw.Tree(backup.SQLitePrefix+name+"/", b.fsResPath(scopePath, name, false), nil); err != nil {
				return err
			}
		case "filesystem":
			if err := bw.Tree(backup.FSPrefix+name+"/", b.fsResPath(scopePath, name, false), nil); err != nil {
				return err
			}
		case "blob":
			if err := bw.Tree(backup.BlobPrefix+name+"/", b.fsResPath(scopePath, name, false), nil); err != nil {
				return err
			}
		case "kv":
			kv[name] = b.dumpKV("res:" + scopePath + "/" + name)
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

// dumpKV reads every key of one kv bucket into {key: base64(value)}.
func (b *Broker) dumpKV(bucket string) map[string]string {
	out := map[string]string{}
	if b.kv == nil {
		return out
	}
	_ = b.kv.db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte(bucket))
		if bk == nil {
			return nil
		}
		return bk.ForEach(func(k, v []byte) error {
			// Store decrypted values so the tar is plaintext (constraint: the
			// archiver, not the tar, is responsible for encryption).
			pv, err := b.decodeKV(bucket, append([]byte(nil), v...))
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

// --- restore ----------------------------------------------------------------

// restore unpacks a backup tar, reconstructing the component at the path recorded
// in the manifest. Placement is purely manifest-driven (self-describing).
func (b *Broker) restore(r io.Reader) (backup.Manifest, error) {
	br, err := backup.NewReader(r)
	if err != nil {
		return backup.Manifest{}, err
	}
	m := br.M
	root := b.Reg.Root
	srcRoot := filepath.Join(root, filepath.FromSlash(m.Component))
	termRoot := b.termDir(m.Component)
	// Restored resource data is re-encrypted under the current vault, so this
	// needs the vault unsealed (plans/vault-data.md).
	if b.vaultSealed() {
		return m, fmt.Errorf("vault sealed — unseal before restoring encrypted resources")
	}

	for {
		name, rd, err := br.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, err
		}
		switch {
		case name == backup.KVName:
			body, _ := io.ReadAll(rd)
			if err := b.loadKV(m.Scope, body); err != nil {
				return m, err
			}
		case strings.HasPrefix(name, backup.SQLitePrefix):
			dst, err := b.restoreFileDest(m.Scope, strings.TrimPrefix(name, backup.SQLitePrefix))
			if err != nil {
				return m, err
			}
			if err := writeFileFrom(dst, rd); err != nil {
				return m, err
			}
		case strings.HasPrefix(name, backup.FSPrefix):
			dst, err := b.restoreFileDest(m.Scope, strings.TrimPrefix(name, backup.FSPrefix))
			if err != nil {
				return m, err
			}
			if err := writeFileFrom(dst, rd); err != nil {
				return m, err
			}
		case strings.HasPrefix(name, backup.BlobPrefix):
			dst, err := b.restoreFileDest(m.Scope, strings.TrimPrefix(name, backup.BlobPrefix))
			if err != nil {
				return m, err
			}
			if err := writeFileFrom(dst, rd); err != nil {
				return m, err
			}
		case strings.HasPrefix(name, backup.SourcePrefix):
			if err := writeFileFrom(backup.SafeJoin(srcRoot, strings.TrimPrefix(name, backup.SourcePrefix)), rd); err != nil {
				return m, err
			}
		case strings.HasPrefix(name, backup.TermPrefix):
			if err := writeFileFrom(backup.SafeJoin(termRoot, strings.TrimPrefix(name, backup.TermPrefix)), rd); err != nil {
				return m, err
			}
		}
	}
	// Restore this component's cron jobs.
	for _, raw := range m.CronJobs {
		var j cronJob
		if json.Unmarshal(raw, &j) == nil && b.cron != nil {
			if b.cron.add(j) == nil {
				b.cron.persist()
			}
		}
	}
	return m, nil
}

// restoreFileDest maps a backup tar entry (its name minus the xbin prefix) to
// the on-disk destination, mounting the resource first so restored data is
// re-encrypted under the current vault. filesystem/sqlite/blob are all mount
// dirs, so rest is always "<name>/<rel>".
func (b *Broker) restoreFileDest(scope, rest string) (string, error) {
	scopeKey := util.ScopeKey(scope)
	name, rel, _ := strings.Cut(rest, "/")
	mdir, err := b.resenc.Ensure(resLabel(scopeKey, name), scopeKey, name)
	if err != nil {
		return "", err
	}
	return backup.SafeJoin(mdir, rel), nil
}

func (b *Broker) loadKV(scope string, body []byte) error {
	if b.kv == nil {
		return nil
	}
	var dump map[string]map[string]string
	if err := json.Unmarshal(body, &dump); err != nil {
		return err
	}
	return b.kv.db.Update(func(tx *bolt.Tx) error {
		for name, kvs := range dump {
			bucket := "res:" + scope + "/" + name
			bk, err := tx.CreateBucketIfNotExists([]byte(bucket))
			if err != nil {
				return err
			}
			for k, v64 := range kvs {
				v, err := base64.StdEncoding.DecodeString(v64)
				if err != nil {
					return err
				}
				// Re-encode under the current vault (the tar held plaintext).
				stored, err := b.encodeKV(bucket, v)
				if err != nil {
					return err
				}
				if err := bk.Put([]byte(k), stored); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func writeFileFrom(dst string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Replace even if the existing file is read-only — git objects (.git/objects)
	// are mode 0444, and os.Create can't reopen those for writing. A restore
	// overwrites wholesale, so removing first is correct.
	_ = os.Remove(dst)
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
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
	c, ok := b.Reg.Component(comp)
	if !ok {
		return "", fmt.Errorf("no such component %q", comp)
	}
	provider := b.archiveProvider(comp)
	if provider == "" {
		return "", fmt.Errorf("no archiver bound — set one: bx bind %q %s=<archiver> (or bind '*' for a default)", comp, archiveSlot)
	}
	pr, pw := io.Pipe()
	go func() {
		bw := backup.NewWriter(pw)
		err := b.writeBackup(bw, c)
		if err == nil {
			err = bw.Close()
		}
		pw.CloseWithError(err)
	}()
	code, resp, err := b.archiveDo("PUT", provider, "/archive/"+backupKey(comp), pr)
	if err != nil {
		return "", err
	}
	if code >= 400 {
		return "", fmt.Errorf("archiver %s: %s", provider, firstLine(string(resp)))
	}
	var out struct{ Version string }
	_ = json.Unmarshal(resp, &out)
	return out.Version, nil
}

// doRestore fetches a version's tar from the archiver and unpacks it. version ""
// means the latest.
func (b *Broker) doRestore(comp, version string) (backup.Manifest, error) {
	provider := b.archiveProvider(comp)
	if provider == "" {
		return backup.Manifest{}, fmt.Errorf("no archiver bound for %q", comp)
	}
	if version == "" {
		version = "latest"
	}
	code, body, err := b.archiveDo("GET", provider, "/archive/"+backupKey(comp)+"/versions/"+version, nil)
	if err != nil {
		return backup.Manifest{}, err
	}
	if code >= 400 {
		return backup.Manifest{}, fmt.Errorf("archiver %s: %s", provider, firstLine(string(body)))
	}
	b.StopBackendSafe(comp)
	return b.restore(bytes.NewReader(body))
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
func (b *Broker) offload(comp string, full bool) error {
	b.StopBackendSafe(comp)
	if _, err := b.doBackup(comp); err != nil {
		return fmt.Errorf("archive before offload failed (nothing removed): %w", err)
	}
	if err := b.removeScopeData(comp); err != nil {
		return err
	}
	if full {
		_ = os.RemoveAll(b.termDir(comp))
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
	if b.kv != nil {
		_ = b.kv.db.Update(func(tx *bolt.Tx) error {
			for name, res := range scope.Resources {
				if res.Type == "kv" {
					_ = tx.DeleteBucket([]byte("res:" + comp + "/" + name))
				}
			}
			return nil
		})
	}
	return os.RemoveAll(b.resourcesRoot(comp))
}

// removeSourceBulk clears a component's source subtree but keeps a stub
// (xbin.json + scope.json) so it stays listed and restorable (offloaded-full).
func (b *Broker) removeSourceBulk(comp string) error {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return nil
	}
	keep := map[string]bool{"xbin.json": true, "scope.json": true}
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(c.Dir, e.Name())); err != nil {
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
	if err := decodeJSON(r, &body); err != nil || body.Component == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {component}"})
		return
	}
	version, err := b.doBackup(body.Component)
	if err != nil {
		server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
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
		server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "archiver: " + firstLine(string(body))})
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
	if err := decodeJSON(r, &body); err != nil || body.Component == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {component, version?, file?}"})
		return
	}
	// Restore a single file: stream it back without touching live state.
	if body.File != "" {
		provider := b.archiveProvider(body.Component)
		ver := body.Version
		if ver == "" {
			ver = "latest"
		}
		code, data, err := b.archiveDo("GET", provider, "/archive/"+backupKey(body.Component)+"/versions/"+ver+"/file?path="+body.File, nil)
		if err != nil || code >= 400 {
			server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": "archiver: " + firstLine(string(data))})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
		return
	}
	m, err := b.doRestore(body.Component, body.Version)
	if err != nil {
		server.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	// A restored component is enabled + rescanned/provisioned.
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { delete(ws.Lifecycle, m.Component) })
	_ = b.Reg.Rescan()
	b.Provision()
	if b.OnStructureChange != nil {
		b.OnStructureChange()
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "component": m.Component, "restored": m.Includes})
}
