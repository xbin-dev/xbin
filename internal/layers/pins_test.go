package layers

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// pinWS is a workspace whose layers pin, between them, every source:
//
//	.xbin/term/apps~t-1     b-term (stamped)
//	.xbin/term/apps~old-2   (unstamped: predates stamps → v0)
//	.xbin/term/view-abc     a staged view — not a layer
//	.xbin/sbx/apps~m-3/web  b-sbx
//	.xbin/sbx/apps~m-3/new  (unstamped: never started — pins nothing)
//	.xbin/sbx/apps~m-3/web/snapshots/s1  b-snap
//
// and the host has the current base (b-cur) plus preserved siblings for
// every version, one of them pinned by nothing, and a sibling that isn't a
// base image at all.
func pinWS(t *testing.T) (ws, rootfs string) {
	t.Helper()
	root := t.TempDir()
	ws = mkdir(t, filepath.Join(root, "ws"))
	rootfs = filepath.Join(root, "rootfs")
	stampBase(t, rootfs, "b-cur")
	for _, v := range []string{"b-term", "v0", "b-sbx", "b-snap", "b-def", "b-free"} {
		stampBase(t, rootfs+"-"+v, v)
	}
	mkdir(t, rootfs+"-notabase")

	term := filepath.Join(ws, ".xbin", "term")
	must(t, Stamp(mkdir(t, filepath.Join(term, "apps~t-1")), Stamps{Base: "b-term"}))
	mkdir(t, filepath.Join(term, "apps~old-2", "upper"))
	mkdir(t, filepath.Join(term, "view-abc"))
	sbx := filepath.Join(ws, ".xbin", "sbx", "apps~m-3")
	must(t, Stamp(mkdir(t, filepath.Join(sbx, "web")), Stamps{Base: "b-sbx", Overlay: OverlayFuse}))
	mkdir(t, filepath.Join(sbx, "new"))
	must(t, Stamp(mkdir(t, filepath.Join(sbx, "web", "snapshots", "s1")), Stamps{Base: "b-snap", Overlay: OverlayFuse}))
	return ws, rootfs
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]bool) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func remaining(t *testing.T, rootfs string) string {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Dir(rootfs))
	var out []string
	for _, e := range ents {
		if v, ok := strings.CutPrefix(e.Name(), filepath.Base(rootfs)+"-"); ok {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// Pinned is the union of the terminal stamps, the sandbox and snapshot
// stamps, and the definitions'; GC keeps exactly that (and the current base,
// and what isn't a base image).
func TestPinnedUnionAndGC(t *testing.T) {
	ws, rootfs := pinWS(t)
	pins, err := Pinned(ws, func() []string { return []string{"b-def", ""} })
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(pins); got != "b-def,b-sbx,b-snap,b-term,v0" {
		t.Fatalf("pinned: %s", got)
	}
	gone := GC(rootfs, pins)
	if len(gone) != 1 || gone[0] != rootfs+"-b-free" {
		t.Fatalf("released: %v", gone)
	}
	if got := remaining(t, rootfs); got != "b-def,b-sbx,b-snap,b-term,notabase,v0" {
		t.Fatalf("left: %s", got)
	}
}

// A base only a .xbin/sbx stamp pins, only a snapshot pins, or only a
// definition pins survives GC; each goes once its last pin does.
func TestGCKeepsSandboxOnlyPins(t *testing.T) {
	ws, rootfs := pinWS(t)
	os.RemoveAll(filepath.Join(ws, ".xbin", "term"))
	pins, err := Pinned(ws, func() []string { return []string{"b-def"} })
	if err != nil {
		t.Fatal(err)
	}
	GC(rootfs, pins)
	if got := remaining(t, rootfs); got != "b-def,b-sbx,b-snap,notabase" {
		t.Fatalf("with sandbox pins: %s", got)
	}
	// The snapshot is deleted, the sandbox reset onto the current base, the
	// definition too: nothing old is pinned any more.
	os.RemoveAll(filepath.Join(ws, ".xbin", "sbx", "apps~m-3", "web", "snapshots"))
	must(t, Stamp(filepath.Join(ws, ".xbin", "sbx", "apps~m-3", "web"), Stamps{Base: "b-cur"}))
	pins, err = Pinned(ws, nil)
	if err != nil || keys(pins) != "b-cur" {
		t.Fatalf("pinned: %s %v", keys(pins), err)
	}
	GC(rootfs, pins)
	if got := remaining(t, rootfs); got != "notabase" {
		t.Fatalf("after the last pins went: %s", got)
	}
}

// An unreadable stamp makes the pins unknown: Pinned says so, and GC with
// unknown (nil) pins releases nothing.
func TestPinnedUnreadableStampReleasesNothing(t *testing.T) {
	ws, rootfs := pinWS(t)
	stamp := filepath.Join(ws, ".xbin", "sbx", "apps~m-3", "web", BaseFile)
	os.Remove(stamp)
	os.Symlink(filepath.Join(rootfs, VersionFile), stamp)
	if _, err := Pinned(ws, nil); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("an unreadable stamp: %v", err)
	}
	if gone := GC(rootfs, nil); gone != nil {
		t.Fatalf("GC on unknown pins released %v", gone)
	}
	if got := remaining(t, rootfs); !strings.Contains(got, "b-free") {
		t.Fatalf("left: %s", got)
	}
	// A tree swapped for a symlink is refused, not walked.
	sbx := filepath.Join(ws, ".xbin", "sbx")
	os.Rename(sbx, sbx+".real")
	os.Symlink(sbx+".real", sbx)
	if _, err := Pinned(ws, nil); err == nil {
		t.Fatal("a symlinked tree was walked")
	}
}

// Check reports each layer: its tree, key, sandbox or snapshot, effective
// pin, and whether that base is installed and current.
func TestCheckPerLayer(t *testing.T) {
	ws, rootfs := pinWS(t)
	os.RemoveAll(rootfs + "-b-snap")
	ls, err := Check(ws, rootfs)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Layer{}
	for _, l := range ls {
		got[l.Tree+":"+l.Key+"/"+l.Sandbox+"/"+l.Snapshot] = l
	}
	if len(got) != 5 {
		t.Fatalf("layers: %+v", ls)
	}
	for id, want := range map[string]Layer{
		"term:apps~t-1//":     {Stamps: Stamps{Base: "b-term"}, Resolved: rootfs + "-b-term", Outdated: true},
		"term:apps~old-2//":   {Stamps: Stamps{Base: Legacy}, Resolved: rootfs + "-v0", Outdated: true},
		"sbx:apps~m-3/web/":   {Stamps: Stamps{Base: "b-sbx", Overlay: OverlayFuse}, Resolved: rootfs + "-b-sbx", Outdated: true},
		"sbx:apps~m-3/new/":   {},
		"sbx:apps~m-3/web/s1": {Stamps: Stamps{Base: "b-snap", Overlay: OverlayFuse}, Missing: true, Outdated: true},
	} {
		l, ok := got[id]
		if !ok {
			t.Fatalf("%s not reported: %+v", id, ls)
		}
		if l.Stamps != want.Stamps || l.Resolved != want.Resolved || l.Missing != want.Missing || l.Outdated != want.Outdated || l.Err != "" {
			t.Fatalf("%s: %+v, want %+v", id, l, want)
		}
	}
	if l := got["sbx:apps~m-3/web/s1"]; l.Dir != filepath.Join(ws, ".xbin", "sbx", "apps~m-3", "web", "snapshots", "s1") {
		t.Fatalf("snapshot dir: %q", l.Dir)
	}
}
