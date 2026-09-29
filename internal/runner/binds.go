package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/resenc"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, "/")+"/")
}

func pathExists(p string) bool { _, err := os.Stat(p); return err == nil }

// resourceBinds returns the read-write binds for a component's file-backed
// resources (sqlite). EnvFor hands each granted same-scope sqlite resource to the
// backend as an absolute XBIN_RES_* path. We bind that path's **directory** (the
// scope's resource dir), not the file, so that:
//   - a *fresh* db works — the file doesn't exist yet (sqlite creates it on first
//     open), so binding the file alone would drop it (pathExists was false) and
//     the db would land on the throwaway overlay instead of persisting; and
//   - sqlite's -wal/-shm sidecars, written next to the db, persist too.
//
// Non-path resources (kv/blob/bus, addressed by res: string over HTTP) aren't
// paths and are skipped. Dirs are deduped; only paths under root are bound.
//
// These are main's binds, every path at itself: another deployment's come
// from resourceBindsFor with its remap, never from here.
func resourceBinds(env []string, root string) []sandbox.Bind {
	seen := map[string]bool{}
	var binds []sandbox.Bind
	for _, e := range env {
		i := strings.IndexByte(e, '=')
		if i < 0 {
			continue
		}
		v := e[i+1:]
		if !strings.HasPrefix(v, "/") || !within(v, root) {
			continue
		}
		// A `filesystem` resource hands the backend a DIRECTORY (bind it); a
		// `sqlite` resource hands a FILE path (bind its dir so a fresh db + the
		// -wal/-shm sidecars persist, not just the file).
		d := v
		if fi, err := os.Stat(v); err != nil || !fi.IsDir() {
			d = filepath.Dir(v)
		}
		if seen[d] || !pathExists(d) {
			continue
		}
		seen[d] = true
		binds = append(binds, sandbox.Bind{Src: d, Dst: d})
	}
	return binds
}

// resourceBindsFor is resourceBinds for a generation of deployment dep, with
// its resource remap (07-runtime §10.4; 08-data §5). A nil remap is main's:
// every path at itself, resourceBinds' binds exactly. Any other deployment
// passes its remap, never nil, and each XBIN_RES_* path under root is bound
// at its canonical directory from the host directory that backs it for dep,
// {Src: remap[d].Src, Dst: d}, so the env values stay identical across
// deployments (D127j). The remap is keyed by that canonical directory: a
// filesystem resource's value, or the directory of a sqlite resource's value
// (its file). Nothing at a canonical path is stat'ed: it is main's data.
//
// It fails closed (06-security C7, T18): a path-valued resource without an
// entry is refused, since binding it at itself would hand dep main's data,
// and so is an entry whose Src is relative, lies in main's data namespace or
// overlaps the canonical directory, or isn't a directory of its own (a
// namespace that isn't mounted). An Omit entry binds nothing and its env var
// stays: a blocked edge, whose path is absent in the sandbox. Only XBIN_RES_*
// values count for another deployment; the run dir and the gateway socket
// are the launch spec's own binds, never taken from the env. A Shared entry
// is a user partition's alone (partitionBindsFor): refused here.
func resourceBindsFor(env []string, root, dep string, remap map[string]ResBind) ([]sandbox.Bind, error) {
	if remap == nil {
		return resourceBinds(env, root), nil
	}
	return remapBinds(env, root, dep, remap, nil)
}

// sharedLookup answers the share mode the registry gives the resource whose
// canonical directory is canon; ok false when it isn't a shared resource of
// a partitioned scope.
type sharedLookup func(canon string) (mode registry.SharedMode, ok bool)

// partitionBindsFor is resourceBindsFor for a generation of a user partition
// (plans/partitions/03 §B.4), who names it in errors. Its remap, never nil,
// maps each canonical directory to the partition's own volume, as another
// deployment's does, except for the shared resources of its partitioned
// scope: an entry with Shared set binds main's data at its own canonical
// path (Src == the path, and nowhere else), and only when shared — the
// runner's own reading of the registry (sharedResources), never the env or
// the broker's word alone — says the resource is shared; a "read" resource
// is bound read-only whatever the entry says (06-security C7).
func partitionBindsFor(env []string, root, who string, remap map[string]ResBind, shared sharedLookup) ([]sandbox.Bind, error) {
	if remap == nil {
		return nil, fmt.Errorf("%s has no data namespace: refused", who)
	}
	if shared == nil {
		shared = func(string) (registry.SharedMode, bool) { return "", false }
	}
	return remapBinds(env, root, who, remap, shared)
}

// remapBinds binds each XBIN_RES_* path under root from its remap entry:
// resourceBindsFor's rules, and with shared (nil: none allowed) the Shared
// entries of partitionBindsFor.
func remapBinds(env []string, root, who string, remap map[string]ResBind, shared sharedLookup) ([]sandbox.Bind, error) {
	seen := map[string]bool{}
	var binds []sandbox.Bind
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || !strings.HasPrefix(k, "XBIN_RES_") || !strings.HasPrefix(v, "/") || !within(v, root) {
			continue
		}
		d := filepath.Clean(v)
		rb, mapped := remap[d]
		if !mapped { // a sqlite file: its directory is the resource's
			d = filepath.Dir(d)
			rb, mapped = remap[d]
		}
		switch {
		case !mapped:
			return nil, fmt.Errorf("no data namespace for %s's resource %s", who, k)
		case seen[d]:
			continue
		}
		seen[d] = true
		if rb.Omit {
			continue
		}
		src := filepath.Clean(rb.Src)
		if rb.Shared {
			b, err := sharedBind(k, who, d, src, rb, shared)
			if err != nil {
				return nil, err
			}
			binds = append(binds, b)
			continue
		}
		switch {
		case !filepath.IsAbs(rb.Src):
			return nil, fmt.Errorf("%s's resource %s: its data namespace %q isn't an absolute path", who, k, rb.Src)
		case mainData(root, src) || within(src, d) || within(d, src):
			return nil, fmt.Errorf("%s's resource %s would bind main's data (%s): refused", who, k, src)
		}
		if fi, err := os.Lstat(src); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s's resource %s: its data namespace %s isn't mounted", who, k, src)
		}
		ro := rb.RO
		if shared != nil {
			mode, _ := shared(d)
			ro = ro || mode == registry.SharedRead // a "read" resource, whatever backs it
		}
		binds = append(binds, sandbox.Bind{Src: src, Dst: d, RO: ro})
	}
	return binds, nil
}

// sharedBind is a Shared entry's bind of canonical directory d, or why it is
// refused: no shared lookup (not a user partition), a source other than d
// itself, a resource the registry doesn't mark shared, or one that isn't
// mounted. A "read" resource is read-only.
func sharedBind(k, who, d, src string, rb ResBind, shared sharedLookup) (sandbox.Bind, error) {
	if shared == nil {
		return sandbox.Bind{}, fmt.Errorf("%s's resource %s: a shared bind is a user partition's only: refused", who, k)
	}
	if !filepath.IsAbs(rb.Src) || src != d {
		return sandbox.Bind{}, fmt.Errorf("%s's resource %s: a shared resource binds at its own path %s, not %s: refused", who, k, d, rb.Src)
	}
	mode, ok := shared(d)
	if !ok {
		return sandbox.Bind{}, fmt.Errorf("%s's resource %s isn't shared in its scope.json: refused", who, k)
	}
	if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
		return sandbox.Bind{}, fmt.Errorf("%s's shared resource %s isn't mounted", who, k)
	}
	return sandbox.Bind{Src: d, Dst: d, RO: rb.RO || mode == registry.SharedRead}, nil
}

// sharedResources is the runner's own reading of which file resources of
// c's scope are shared (plans/partitions/03 §B.4): none unless the registry
// says the scope is partitioned (Registry.PartitionedScope), then each
// filesystem or sqlite resource the scope declares "shared": true or "read",
// by its canonical directory (main's mount, resenc.MountPath). It never
// reads the env or asks the broker.
func (r *Runner) sharedResources(c *registry.Component) sharedLookup {
	out := map[string]registry.SharedMode{}
	if r.Reg != nil && c.Scope != "" {
		if _, ok := r.Reg.PartitionedScope(c.Scope); ok {
			if sm := r.Reg.Scopes()[c.Scope]; sm != nil {
				for name, res := range sm.Resources {
					if (res.Shared == registry.SharedAll || res.Shared == registry.SharedRead) &&
						(res.Type == "filesystem" || res.Type == "sqlite") {
						out[resenc.MountPath(r.Root, util.ScopeKey(c.Scope), name)] = res.Shared
					}
				}
			}
		}
	}
	return func(canon string) (registry.SharedMode, bool) {
		m, ok := out[filepath.Clean(canon)]
		return m, ok
	}
}

// mainData reports whether host path p lies in main's data namespace: the
// resource mounts under .xbin/resenc, the ciphertext under
// data/resources-enc and the plaintext under data/resources, each without
// its .deployments and .partitions trees, names no ScopeKey produces
// (08-data §3.1; plans/partitions/03 §B.3).
func mainData(root, p string) bool {
	for _, base := range []string{".xbin/resenc", "data/resources-enc", "data/resources"} {
		b := filepath.Join(root, filepath.FromSlash(base))
		if within(p, b) && !within(p, filepath.Join(b, ".deployments")) && !within(p, filepath.Join(b, ".partitions")) {
			return true
		}
	}
	return false
}
