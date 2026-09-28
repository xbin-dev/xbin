//go:build linux && integration

package isolated

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// TestBaseGC: a boot's base-image GC (internal/boot: layers.GC over
// layers.Pinned) keeps a preserved base that only a snapshot pins, and one
// that only a sandbox definition pins, and releases the rest — live, on a
// copy of the rootfs (a GC releases the unpinned `<rootfs>-<version>`
// siblings next to the rootfs it runs over, so never the shared one).
//
// An install upgrade preserves the old base as `<rootfs>-<version>` and
// installs the new one at <rootfs>; here the copy's version stamp moves on
// (A → B → C) and each preserved base is a stub carrying only its stamp —
// all a GC reads — since nothing here starts on an old base again.
func TestBaseGC(t *testing.T) {
	a := xbindtest.Require(t)
	root := xbindtest.TempRoot(t)
	rootfs := filepath.Join(root, "rootfs")
	a.CopyRootfs(t, rootfs, "itest-a")
	d := xbindtest.Start(t, a, xbindtest.Options{Rootfs: rootfs})
	importManager(t, d)
	owner, _ := managers(t, d)

	// on base A: snap's snapshot s-1 pins it; snap itself is reset (it pins
	// nothing until it starts again) and gone is deleted, so the snapshot
	// is A's only pin
	owner.create(t, "snap", "namespace", "")
	var sn snapshotInfo
	owner.must(t, "POST", "/sandboxes/snap/snapshots", map[string]string{"name": "on-a"}, 201, &sn)
	owner.must(t, "POST", "/sandboxes/snap/stop", nil, 200, nil)
	owner.must(t, "POST", "/sandboxes/snap/reset", nil, 200, nil)
	owner.create(t, "gone", "namespace", "")
	owner.must(t, "DELETE", "/sandboxes/gone", nil, 204, nil)

	// the upgrade to B, with a stale preserved base Z that nothing pins
	d.Stop(t)
	upgrade(t, rootfs, "itest-a", "itest-b")
	stub(t, rootfs, "itest-z")
	d.Restart(t)
	waitManager(t, d)
	exists(t, rootfs, "itest-a", true, "pinned by a snapshot only")
	exists(t, rootfs, "itest-z", false, "pinned by nothing")

	// on base B: def's definition will be its only pin — its state is lost
	// (a host's disk, a hand's rm): the definition still names the base
	in := owner.create(t, "def", "namespace", "")
	if in.Base.Version != "itest-b" {
		t.Fatalf("def's base: %+v", in.Base)
	}
	owner.must(t, "POST", "/sandboxes/def/stop", nil, 200, nil)
	d.Stop(t)
	xbindtest.RemoveTree(filepath.Join(findStateDir(t, d, "def", in.UID), "cur"))
	upgrade(t, rootfs, "itest-b", "itest-c")
	d.Restart(t)
	waitManager(t, d)
	exists(t, rootfs, "itest-a", true, "pinned by a snapshot only")
	exists(t, rootfs, "itest-b", true, "pinned by a definition only")
	// its start finds the state gone: error, never a blank root
	if r := owner.call(t, "POST", "/sandboxes/def/start", nil); r.Status != 409 || refusedAs(t, r).State != "error" {
		t.Errorf("def's start without its state: %d %s", r.Status, r)
	}
	if in := owner.get(t, "def"); in.State != "error" || !strings.Contains(in.StateDetail, "state is missing") {
		t.Errorf("def without its state: %+v", in)
	}

	// released: the snapshot deleted, def reset (its base cleared), the
	// next boot's GC removes both
	owner.must(t, "DELETE", "/sandboxes/snap/snapshots/"+sn.ID, nil, 204, nil)
	owner.must(t, "POST", "/sandboxes/def/reset", nil, 200, nil)
	d.Restart(t)
	waitManager(t, d)
	exists(t, rootfs, "itest-a", false, "no longer pinned")
	exists(t, rootfs, "itest-b", false, "no longer pinned")
	if base := baseVersion(t, rootfs); base != "itest-c" {
		t.Errorf("the current base: %s", base)
	}
	// what runs now runs on C
	if in := owner.create(t, "later", "namespace", ""); in.Base.Version != "itest-c" {
		t.Errorf("a new sandbox's base: %+v", in.Base)
	}
}

// upgrade preserves base from as a stub sibling and restamps rootfs as to
// (a new file renamed into place: the copy may share its files' extents).
func upgrade(t *testing.T, rootfs, from, to string) {
	t.Helper()
	if got := baseVersion(t, rootfs); got != from {
		t.Fatalf("the rootfs is %s, not %s", got, from)
	}
	stub(t, rootfs, from)
	p := filepath.Join(rootfs, "etc", "xbin-base-version")
	if err := os.WriteFile(p+".new", []byte(to+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p+".new", p); err != nil {
		t.Fatal(err)
	}
}

// stub makes a preserved base's sibling, `<rootfs>-<version>`, holding only
// its stamp.
func stub(t *testing.T, rootfs, version string) {
	t.Helper()
	dir := rootfs + "-" + version
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "xbin-base-version"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { xbindtest.RemoveTree(dir) })
}

func exists(t *testing.T, rootfs, version string, want bool, why string) {
	t.Helper()
	_, err := os.Stat(rootfs + "-" + version)
	if got := err == nil; got != want {
		t.Errorf("base %s (%s): kept %v, want %v (%v)", version, why, got, want, fmt.Sprint(err))
	}
}
