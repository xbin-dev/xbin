package broker

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/internal/vault"
)

// Encryption-at-rest for resource data (plans/vault-data.md). There is no
// plaintext resource path: file-backed resources (filesystem/sqlite/blob) are
// ALWAYS stored as a per-resource gocryptfs mount keyed by a vault subkey, and
// kv values are ALWAYS envelope-encrypted per bucket. When encryption can't run
// (no gocryptfs binary, or the vault is sealed/absent) the resource is
// unavailable — the component that uses it is held — never silently plaintext.

// initResEnc builds the resource-encryption manager and clears any stale mounts
// left by a previous xbind. Called from New (after the barrier is opened).
func (b *Broker) initResEnc() {
	bin := resenc.Resolve()
	b.resenc = resenc.New(b.Reg.Root, bin, b.barrier.DeriveKey)
	b.resenc.RecoverStale()
	if bin == "" {
		slog.Warn("resource encryption: gocryptfs not found — a component that uses a filesystem/sqlite/blob resource will be HELD until it's available (build it via `make build`, or set XBIN_GOCRYPTFS)")
	}
}

// fileBackedType: resource types stored as a gocryptfs mount (a dir on disk).
// kv is the exception — it lives in the shared bbolt db and is envelope-encrypted
// per bucket instead.
func fileBackedType(t string) bool {
	switch t {
	case "filesystem", "sqlite", "blob":
		return true
	}
	return false
}

// resLabel is the stable key-derivation / mount identity for a resource.
func resLabel(scopeKey, name string) string { return scopeKey + "/" + name }

// resSingleTenant decides whether a file-backed resource is mounted in
// gocryptfs single-tenant mode (hack/gocryptfs-patches/): filesystem
// resources of a scope hosting a cap:containers component. A container layer
// store needs full uid/mode/whiteout round-trip, which a normal unprivileged
// gocryptfs mount structurally cannot give (the daemon is the I/O actor);
// single-tenant mode virtualizes that identity into encrypted xattrs and
// skips in-mount permission checks — sound here because a scoped resenc
// mount serves exactly that scope's sandboxes, which already share the
// resource rw. Conservative on purpose: never workspace-level resources,
// never sqlite/blob.
func (b *Broker) resSingleTenant(scope, rtype string) bool {
	if rtype != "filesystem" || scope == "" {
		return false
	}
	for _, c := range b.Reg.Components() {
		if c.Scope == scope && b.ContainersFor(c) {
			return true
		}
	}
	return false
}

// resType looks up a declared resource's type ("" when unknown).
func (b *Broker) resType(scope, name string) string {
	if scope == "" {
		if r, ok := b.Reg.Workspace().Resources[name]; ok {
			return r.Type
		}
		return ""
	}
	if sm, ok := b.Reg.Scopes()[scope]; ok {
		if r, ok := sm.Resources[name]; ok {
			return r.Type
		}
	}
	return ""
}

// fsReady reports whether a file-backed resource can be served right now:
// gocryptfs present, a vault configured + unsealed, and its mount up.
func (b *Broker) fsReady(k resKeys) bool {
	return b.encryptionReady() && volumeMounted(b.resenc, k)
}

// volumeMounted reports whether k's decrypted view is mounted: resenc's
// mountinfo check. The unit tests, which have no FUSE, stand a fake in.
var volumeMounted = func(m *resenc.Manager, k resKeys) bool { return m.Mounted(k.DirKey, k.Name) }

// encryptionReady reports whether a volume can mount now: gocryptfs present
// and a vault configured and unsealed.
func (b *Broker) encryptionReady() bool {
	return b.resenc != nil && b.resenc.Available() &&
		b.barrier != nil && b.barrier.Initialized() && !b.barrier.Sealed()
}

// ensureVolume mounts k's volume, one beyond main, on its first use: a start
// of a deployment whose namespace declares it, or a blob request (08-data
// §3.6); the first mount initializes it. It stays mounted until seal (or a
// reset, removal or shutdown). true only once the view is mounted: a bare
// mountpoint is never handed out, so nothing lands there in plaintext.
func (b *Broker) ensureVolume(k resKeys, scope, rtype string) bool {
	if !b.encryptionReady() {
		return false
	}
	if volumeMounted(b.resenc, k) {
		return true
	}
	if _, err := b.resenc.Ensure(k.FSLabel, k.DirKey, k.Name, b.resSingleTenant(scope, rtype)); err != nil {
		slog.Error("resource encryption: mount failed", "res", k.FSLabel, "err", err)
		return false
	}
	return volumeMounted(b.resenc, k)
}

// resMount returns the decrypted mount of a file-backed resource: the path
// handed to a same-scope backend for a filesystem resource, and the one
// blobAccess serves. With sqlite, the .sqlite file inside the mount.
func (b *Broker) resMount(k resKeys, sqlite bool) string {
	dir := b.resenc.MountDir(k.DirKey, k.Name)
	if sqlite {
		return filepath.Join(dir, k.Name+".sqlite")
	}
	return dir
}

// fsResPath is resMount of main's keys for scope's resource name: "" for a
// name the key function refuses, which is never provisioned.
func (b *Broker) fsResPath(scope, name string, sqlite bool) string {
	k, err := b.resKeys(resTarget{Scope: scope, Name: name}, util.MainDeployment)
	if err != nil {
		return ""
	}
	return b.resMount(k, sqlite)
}

// EncryptionHold reports whether a component must be kept from spawning because
// the tile state it depends on isn't currently accessible: a file resource whose
// encrypted mount isn't up (gocryptfs missing, vault sealed, or not yet mounted),
// or a kv bucket behind a sealed vault. Composed into runner.ShouldRun. It is
// the hold of the tile's primary: DeploymentEncryptionHold's.
func (b *Broker) EncryptionHold(comp string) bool {
	return b.DeploymentEncryptionHold(comp, b.primaryOf(comp))
}

// DeploymentEncryptionHold is EncryptionHold for deployment dep of tile, per
// data namespace (08-data §3.6): each resource its uses name resolves in the
// namespace dep reaches (its own scope's in dep's, any other in the scope
// primary's), against that namespace's declared set. A volume beyond main is
// mounted here, on the spawn path, when it isn't yet, so a start settles what
// dep's own code declares before its backend runs (P22). main's volumes are
// MountEncrypted's, as today.
func (b *Broker) DeploymentEncryptionHold(tile, dep string) bool {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return false
	}
	dep = cmp.Or(dep, util.MainDeployment)
	sealedVault := b.barrier != nil && b.barrier.Initialized() && b.barrier.Sealed()
	for _, u := range c.Manifest.Uses {
		rt, res, ok := b.envTarget(c, dep, u.Target)
		if !ok {
			continue
		}
		switch {
		case fileBackedType(res.Type):
			ns := dep
			if rt.Scope == "" || rt.Scope != c.Scope {
				ns = b.scopePrimary(rt.Scope)
			}
			switch k, err := b.resKeys(rt, ns); {
			case err != nil:
				return true // a refused name is never mounted
			case k.NS != "" && !b.ensureVolume(k, rt.Scope, res.Type), k.NS == "" && !b.fsReady(k):
				return true
			}
		case res.Type == "kv":
			if sealedVault {
				return true // can't decode kv values while sealed
			}
		}
	}
	return false
}

// MountEncrypted ensures every file-backed resource main's namespaces declare
// has its decrypted view mounted. Called after the vault is unsealed, at the
// end of each (re)provision and on a cap:containers change. No-op while
// encryption can't run. The volumes beyond main mount on first use; the ones
// that are mounted are ensured again here, so a cap:containers change
// reaches them too: their single-tenant mode follows the grant (08-data
// §3.6).
func (b *Broker) MountEncrypted() {
	if !b.encryptionReady() {
		return
	}
	b.forEachFileRes(func(scope, name, rtype string) {
		k, err := b.resKeys(resTarget{Scope: scope, Name: name}, util.MainDeployment)
		if err != nil {
			slog.Error("resource encryption: not mounted", "err", err) // a refused name (NP-08-11)
			return
		}
		if _, err := b.resenc.Ensure(k.FSLabel, k.DirKey, k.Name, b.resSingleTenant(scope, rtype)); err != nil {
			slog.Error("resource encryption: mount failed", "res", k.FSLabel, "err", err)
		}
	})
	for _, m := range b.resenc.Mounts() {
		scope, dep, ok := nsVolume(m.ScopeKey)
		if !ok {
			continue
		}
		set, _ := b.declaredIn(scope, dep)
		res, declared := set[m.Name]
		k, err := b.resKeys(resTarget{Scope: scope, Name: m.Name}, dep)
		if !declared || err != nil || k.DirKey != m.ScopeKey {
			continue // no longer declared: it keeps its mode until it is unmounted
		}
		if _, err := b.resenc.Ensure(k.FSLabel, k.DirKey, k.Name, b.resSingleTenant(scope, res.Type)); err != nil {
			slog.Error("resource encryption: remount failed", "res", k.FSLabel, "err", err)
		}
	}
}

// nsVolume recovers the scope and deployment of a volume's directory key
// beyond main, ".deployments/<escS>/<d>/fs"; false for any other key.
func nsVolume(dirKey string) (scope, dep string, ok bool) {
	rest, ok := strings.CutPrefix(dirKey, deploymentsLevel+"/")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) != 3 || parts[2] != "fs" || !util.DeploymentNameOK(parts[1]) {
		return "", "", false
	}
	scope, ok = unescS(parts[0])
	return scope, parts[1], ok
}

// SealResources stops the components that depend on file resources and unmounts
// every decrypted view, so a sealed vault leaves only ciphertext on disk. Called
// from the seal API after the barrier is sealed. A tile stops when any of its
// deployments addresses a file-backed volume; StopBackend stops every
// deployment of it.
func (b *Broker) SealResources() {
	if b.resenc == nil {
		return
	}
	if b.StopBackend != nil {
		for _, c := range b.Reg.Components() {
			_, deps := b.deploymentsOf(c.Path)
			if slices.ContainsFunc(deps, func(dep string) bool { return b.componentUsesFileRes(c, dep) }) {
				b.StopBackend(c.Path)
			}
		}
	}
	b.resenc.UnmountAll()
}

// componentUsesFileRes reports whether deployment dep of c addresses a
// file-backed resource.
func (b *Broker) componentUsesFileRes(c *registry.Component, dep string) bool {
	for _, u := range c.Manifest.Uses {
		if _, res, ok := b.envTarget(c, dep, u.Target); ok && fileBackedType(res.Type) {
			return true
		}
	}
	return false
}

// forEachFileRes calls fn(scope, name, type) for every file-backed resource
// (filesystem/sqlite/blob) main's namespaces declare, across the workspace
// and its scopes: each from main's own code (declaredFrom).
func (b *Broker) forEachFileRes(fn func(scope, name, rtype string)) {
	do := func(scope string, resources map[string]registry.Resource) {
		for name, res := range resources {
			if fileBackedType(res.Type) {
				fn(scope, name, res.Type)
			}
		}
	}
	do("", b.Reg.Workspace().Resources)
	for scope, sm := range b.Reg.Scopes() {
		res, _ := b.declaredFrom(scope, sm, util.MainDeployment)
		do(scope, res)
	}
}

// ---- tile deployments: each namespace's declarations and volumes ----

// DeploymentEnv is the runner's EnvFor hook (runner.DeploymentHooks): the env
// of deployment dep's generation of c, its view, is EnvFor's, the same values
// in every deployment for every resource both declare (P17). main's remap is
// nil: every resource path bound at itself, today's binds. Every other
// deployment's maps each canonical resource directory it is handed to what
// backs it in dep's namespace, mounted now if it isn't (its first use,
// 08-data §3.6): a volume that can't mount has no entry, so the start fails
// closed. A workspace-level filesystem or sqlite resource of a
// workspace-scope tile is an edge: main's data at its own path for the
// primary (P3), no bind at all for the others, whose env var stays (P23).
func (b *Broker) DeploymentEnv(c *registry.Component, dep string) ([]string, map[string]runner.ResBind) {
	dep = cmp.Or(dep, util.MainDeployment)
	if b.viewDeployment(c) != dep {
		v := *c
		v.Deployment = dep
		c = &v
	}
	env := b.EnvFor(c)
	if dep == util.MainDeployment {
		return env, nil
	}
	if c.Scope != "" {
		if _, err := b.declaredIn(c.Scope, dep); err != nil {
			slog.Warn("resources: not provisioned", "tile", c.Path, "deployment", dep, "err", err)
		}
	}
	handed := map[string]bool{}
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && strings.HasPrefix(k, "XBIN_RES_") {
			handed[v] = true
		}
	}
	remap := map[string]runner.ResBind{}
	for _, u := range c.Manifest.Uses {
		rt, res, ok := b.envTarget(c, dep, u.Target)
		if !ok || rt.Scope != c.Scope || res.Type != "filesystem" && res.Type != "sqlite" {
			continue
		}
		canon := b.fsResPath(rt.Scope, rt.Name, false)
		if _, done := remap[canon]; done || canon == "" || !handed[b.fsResPath(rt.Scope, rt.Name, res.Type == "sqlite")] {
			continue
		}
		if rt.Scope == "" {
			remap[canon] = runner.ResBind{Src: canon, Omit: !b.isPrimary(c.Path, dep)}
			continue
		}
		k, err := b.resKeys(rt, dep)
		if err != nil || !b.ensureVolume(k, rt.Scope, res.Type) {
			continue
		}
		remap[canon] = runner.ResBind{Src: b.resMount(k, false)}
	}
	return env, remap
}

// declaredIn is D(scope, dep), the resources scope's data namespace in
// deployment dep has (08-data §6.7) (P22); declaredFrom reads it.
func (b *Broker) declaredIn(scope, dep string) (map[string]registry.Resource, error) {
	if scope == "" {
		return b.Reg.Workspace().Resources, nil // for every deployment: never split
	}
	return b.declaredFrom(scope, b.Reg.Scopes()[scope], dep)
}

// declaredFrom is D(scope, dep) for the scope whose registry manifest is sm,
// read from the code that owns scope's declarations for dep:
//   - a scope no tile roots (a plain directory): its scope.json, for every
//     deployment;
//   - a scope rooted by tile R: the registry's for R's primary, for a
//     deployment R doesn't have (a sibling's, which shares the primary's
//     declarations), and always for a tile without a record; that is R's
//     primary's code, its checkpoint's scope.json while pinned. For another
//     deployment of R, that deployment's own code (deploymentDeclares).
//
// Beyond main a name must also pass the namespace name rule, and at most
// maxNSResources are declared, in name order; the error names what was
// skipped, or why nothing is declared. The map is never to be written.
func (b *Broker) declaredFrom(scope string, sm *registry.ScopeManifest, dep string) (map[string]registry.Resource, error) {
	if sm == nil {
		return nil, nil
	}
	dep = cmp.Or(dep, util.MainDeployment)
	res := sm.Resources
	if root, isTile := b.Reg.Component(scope); isTile && dep != b.primaryOf(scope) && b.hasDeployment(scope, dep) {
		var err error
		if res, err = b.deploymentDeclares(root, dep); err != nil {
			return nil, err
		}
	}
	if dep == util.MainDeployment {
		return res, nil
	}
	names := make([]string, 0, len(res))
	for n := range res {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make(map[string]registry.Resource, min(len(names), maxNSResources))
	var refused, over []string
	for _, n := range names {
		switch {
		case !mainResNameOK(n) || !nsResNameOK(n):
			refused = append(refused, strconv.Quote(n))
		case len(out) == maxNSResources:
			over = append(over, n)
		default:
			out[n] = res[n]
		}
	}
	var errs []error
	if len(refused) > 0 {
		errs = append(errs, fmt.Errorf("resource names %s in %s/scope.json are not plain relative paths: not provisioned for %q",
			strings.Join(refused, ", "), scope, dep))
	}
	if len(over) > 0 {
		errs = append(errs, fmt.Errorf("%s/scope.json declares %d resources for %q, over the %d a deployment beyond main takes: %s not provisioned",
			scope, len(res), dep, maxNSResources, strings.Join(over, ", ")))
	}
	return out, errors.Join(errs...)
}

// deploymentDeclares is what deployment dep of the scope root tile c declares
// in its own code's scope.json: its checkpoint's, read beneath the
// materialized tree and checked (registry.ReadCheckpoint), or the work tree's
// while it follows it. Nothing, with the reason, when its code can't be
// read: never another deployment's declarations in its place.
func (b *Broker) deploymentDeclares(c *registry.Component, dep string) (map[string]registry.Resource, error) {
	if b.DeploymentCodeRoot == nil {
		return nil, fmt.Errorf("%s: deployment %q's code can't be read without the deployments plane", c.Path, dep)
	}
	root, pinned, err := b.DeploymentCodeRoot(c, dep)
	switch {
	case err != nil:
		return nil, err
	case pinned:
		return checkpointDeclares(root)
	}
	return workTreeDeclares(root)
}

// declaredFiles caches the scope.json declarations read from deployments'
// code, by the tree they were read from: a checkpoint's materialized tree
// never changes; a work tree's file is read again when its size or
// modification time moves.
var declaredFiles struct {
	sync.Mutex
	m map[string]declaredFile
}

type declaredFile struct {
	size int64
	mod  time.Time
	res  map[string]registry.Resource
	err  error // the names it refused
}

// declaredFilesMax bounds the cache; a full one starts afresh.
const declaredFilesMax = 256

func cachedDeclares(key string, size int64, mod time.Time) (declaredFile, bool) {
	declaredFiles.Lock()
	defer declaredFiles.Unlock()
	f, ok := declaredFiles.m[key]
	return f, ok && f.size == size && f.mod.Equal(mod)
}

func cacheDeclares(key string, f declaredFile) {
	declaredFiles.Lock()
	defer declaredFiles.Unlock()
	if declaredFiles.m == nil || len(declaredFiles.m) >= declaredFilesMax {
		declaredFiles.m = map[string]declaredFile{}
	}
	declaredFiles.m[key] = f
}

// checkpointDeclares is the resources the checkpoint materialized at root
// declares: none when it has no scope.json; an error, and nothing, when its
// files can't be read or checked. A failed read isn't cached.
func checkpointDeclares(root string) (map[string]registry.Resource, error) {
	key := "checkpoint\x00" + root
	if f, ok := cachedDeclares(key, 0, time.Time{}); ok {
		return f.res, nil
	}
	pc, err := registry.ReadCheckpoint(root)
	if err != nil {
		return nil, err
	}
	var f declaredFile
	if pc.Scope != nil {
		f.res = pc.Scope.Resources
	}
	cacheDeclares(key, f)
	return f.res, nil
}

// workTreeDeclares is the resources the scope.json in the work tree at dir
// declares, opened beneath dir so a symlink never leads out of the tile,
// parsed as the registry parses it, each name outside the registry's rule
// dropped (D118) and named in the error. Nothing, with the reason, when it
// can't be read or doesn't parse.
func workTreeDeclares(dir string) (map[string]registry.Resource, error) {
	const cantRead = "the work tree's scope.json %s: nothing is provisioned from it"
	f, err := fsutil.OpenBeneath(dir, "scope.json")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case errors.Is(err, fsutil.ErrEscapes):
		return nil, fmt.Errorf(cantRead, "leaves the tile through a symlink")
	case err != nil:
		return nil, fmt.Errorf(cantRead, "can't be read")
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		return nil, nil
	}
	key := "worktree\x00" + dir
	if c, ok := cachedDeclares(key, fi.Size(), fi.ModTime()); ok {
		return c.res, c.err
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf(cantRead, "can't be read")
	case len(data) > 1<<20:
		return nil, fmt.Errorf(cantRead, "is larger than 1 MiB")
	}
	var sm registry.ScopeManifest
	if err := jsonc.Unmarshal(data, &sm); err != nil {
		return nil, fmt.Errorf(cantRead, "does not parse")
	}
	var bad []string
	for n := range sm.Resources {
		if !registry.ValidResourceName(n) {
			bad = append(bad, strconv.Quote(n))
			delete(sm.Resources, n)
		}
	}
	c := declaredFile{size: fi.Size(), mod: fi.ModTime(), res: sm.Resources}
	if len(bad) > 0 {
		sort.Strings(bad)
		c.err = fmt.Errorf("resource names %s in the work tree's scope.json are not allowed (%s): not provisioned",
			strings.Join(bad, ", "), registry.ResourceNameRule)
	}
	cacheDeclares(key, c)
	return c.res, c.err
}

// kv values carry a 1-byte storage tag so the store is self-describing and can
// hold a mix of encrypted and (dev/insecure) plaintext values:
//
//	0x01 | nonce||ciphertext   — encrypted with DeriveKey(<label>): the
//	                             resource's resKeys.KVLabel, "kv:<bucket>" in
//	                             main, the namespace's own beyond it
//	0x00 | plaintext           — stored while no barrier was configured
//	(anything else)            — legacy untagged plaintext (returned as-is)
const (
	kvTagPlain = 0x00
	kvTagEnc   = 0x01
)

// encodeKV wraps a kv value for storage under label (resKeys.KVLabel),
// encrypting it whenever a barrier is configured. Returns vault.ErrSealed
// when the barrier is sealed.
func (b *Broker) encodeKV(label string, val []byte) ([]byte, error) {
	if b.barrier != nil && b.barrier.Initialized() {
		ct, err := b.barrier.EncryptFor(label, val)
		if err != nil {
			return nil, err
		}
		return append([]byte{kvTagEnc}, ct...), nil
	}
	return append([]byte{kvTagPlain}, val...), nil
}

// decodeKV unwraps a kv value stored under label, decrypting when tagged
// encrypted: a value moved to another resource or namespace fails closed.
func (b *Broker) decodeKV(label string, raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	switch raw[0] {
	case kvTagEnc:
		if b.barrier == nil {
			return nil, vault.ErrSealed
		}
		return b.barrier.DecryptFor(label, raw[1:])
	case kvTagPlain:
		return raw[1:], nil
	default:
		return raw, nil // legacy untagged
	}
}
