// Package prefsfile is where the per-user prefs buckets live on disk and
// how one is read and written: the obs plane's /prefs routes keep them, and
// the document injection reads the shell's bucket for the person's
// appearance (internal/server appearance.go, D184), so the path rule has
// one home. A bucket is data/prefs/<CompKey(user)>/<CompKey(component)>.json
// under the workspace root: user is a person's id, or Root for the owner
// token and --no-auth; component is the tile that keeps it, or Root for
// the shell and the main page. A tile deployment beyond main keeps its
// buckets in a level of their own (internal/obs deployprefs.go).
package prefsfile

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// Root names the owner (the root token, single-user) as a user, and the
// shell (the main page) as a bucket's component.
const Root = "root"

// Dir is user's prefs directory under the workspace root.
func Dir(root, user string) string {
	return filepath.Join(root, "data", "prefs", util.CompKey(user))
}

// Path is user's bucket of component comp: main's, for a tile.
func Path(root, user, comp string) string {
	return filepath.Join(Dir(root, user), util.CompKey(comp)+".json")
}

// Read is a bucket's keys; a missing file is an empty bucket.
func Read(path string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	bts, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(bts, &out)
}

// Write replaces a bucket, atomically (a reader sees the old file or the
// new one, never half of either).
func Write(path string, m map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	bts, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, bts, 0o644)
}
