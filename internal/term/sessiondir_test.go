package term

import (
	"path/filepath"
	"testing"
)

// A session's directory is a tile: never one of xbind's own trees, whose
// masks its read-write bind would otherwise cover (sessiondir.go).
func TestSessionDirIsATile(t *testing.T) {
	m := &Manager{TileAt: func(rel string) (string, bool) {
		for _, tile := range []string{"apps/a", "notes+ideas"} {
			if rel == tile || len(rel) > len(tile) && rel[:len(tile)+1] == tile+"/" {
				return tile, true
			}
		}
		return "", false
	}}
	for rel, ok := range map[string]bool{
		"apps/a": true, "apps/a/sub/dir": true, "notes+ideas": true,
		".xbin": false, ".xbin/run": false, "data": false, "data/vault": false, "homes": false, "homes/ann": false,
		"vendor": false, "xbin/agent": false, "apps/a/.git": false, "apps/../.xbin": false, "apps": false, "apps/nosuch": false, "": false,
	} {
		if why := m.sessionDir(rel); (why == "") != ok {
			t.Errorf("sessionDir(%q) = %q, want allowed=%v", rel, why, ok)
		}
	}
	// without a registry the shape alone decides: xbind's trees stay out
	bare := &Manager{}
	if bare.sessionDir("apps/whatever") != "" || bare.sessionDir(".xbin") == "" || bare.sessionDir("data") == "" {
		t.Error("a manager without a registry: a component-shaped path passes, xbind's own trees never")
	}
	// and whatever asked for it, no read-write bind is laid over the masks
	root := "/ws"
	for _, rel := range []string{".xbin", "data", "homes", "apps/../.xbin"} {
		for _, b := range scopedBinds(root, rel, "/ws/homes/ann", nil, nil) {
			if !b.RO && !b.Mask && b.Src != "" && b.Src != "/ws/homes/ann" {
				t.Errorf("scopedBinds(%q) binds %s read-write", rel, b.Src)
			}
		}
	}
	rw := false
	for _, b := range scopedBinds(root, "apps/a", "/ws/homes/ann", nil, nil) {
		rw = rw || (!b.RO && b.Src == filepath.Join(root, "apps/a"))
	}
	if !rw {
		t.Error("a tile's own directory is no longer bound read-write")
	}
}
