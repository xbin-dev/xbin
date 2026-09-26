package runner

import (
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
