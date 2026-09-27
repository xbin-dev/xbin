package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/sandbox"
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
// deployments (P17). The remap is keyed by that canonical directory: a
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
// are the launch spec's own binds, never taken from the env.
func resourceBindsFor(env []string, root, dep string, remap map[string]ResBind) ([]sandbox.Bind, error) {
	if remap == nil {
		return resourceBinds(env, root), nil
	}
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
			return nil, fmt.Errorf("no data namespace for %s's resource %s", dep, k)
		case seen[d]:
			continue
		}
		seen[d] = true
		if rb.Omit {
			continue
		}
		src := filepath.Clean(rb.Src)
		switch {
		case !filepath.IsAbs(rb.Src):
			return nil, fmt.Errorf("%s's resource %s: its data namespace %q isn't an absolute path", dep, k, rb.Src)
		case mainData(root, src) || within(src, d) || within(d, src):
			return nil, fmt.Errorf("%s's resource %s would bind main's data (%s): refused", dep, k, src)
		}
		if fi, err := os.Lstat(src); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s's resource %s: its data namespace %s isn't mounted", dep, k, src)
		}
		binds = append(binds, sandbox.Bind{Src: src, Dst: d, RO: rb.RO})
	}
	return binds, nil
}

// mainData reports whether host path p lies in main's data namespace: the
// resource mounts under .xbin/resenc, the ciphertext under
// data/resources-enc and the plaintext under data/resources, each without
// its .deployments tree, a name no ScopeKey produces (08-data §3.1).
func mainData(root, p string) bool {
	for _, base := range []string{".xbin/resenc", "data/resources-enc", "data/resources"} {
		b := filepath.Join(root, filepath.FromSlash(base))
		if within(p, b) && !within(p, filepath.Join(b, ".deployments")) {
			return true
		}
	}
	return false
}
