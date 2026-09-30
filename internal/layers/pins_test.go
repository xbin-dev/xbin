package layers

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The sandbox state dirs of pinWS (`<name>.<uid>`).
const (
	webDir = "web.0123456789ab"
	newDir = "new.aaaaaaaaaaaa"
)

// pinWS is a workspace whose layers pin, between them, every source:
//
//	.xbin/term/apps~t-1                        b-term (stamped)
//	.xbin/term/apps~old-2                      (unstamped: predates stamps → v0)
//	.xbin/term/view-abc                        a staged view — not a layer
//	.xbin/sbx/apps~m-3/web.<uid>/cur           b-sbx
//	.xbin/sbx/apps~m-3/web.<uid>/snapshots/s1  b-snap
//	.xbin/sbx/apps~m-3/new.<uid>               (no cur/: never started — pins nothing)
//
// and, pinning nothing: what the trash holds (a deleted sandbox's state dir,
// a reset's old cur), a staging dir, and dirs not named `<name>.<uid>`
// (b-gone). The host has the current base (b-cur) plus preserved siblings
// for every version, some pinned by nothing, and a sibling that isn't a base
// image at all.
func pinWS(t *testing.T) (ws, rootfs string) {
	t.Helper()
	root := t.TempDir()
	ws = mkdir(t, filepath.Join(root, "ws"))
	rootfs = filepath.Join(root, "rootfs")
	stampBase(t, rootfs, "b-cur")
	for _, v := range []string{"b-term", "v0", "b-sbx", "b-snap", "b-def", "b-free", "b-gone"} {
		stampBase(t, rootfs+"-"+v, v)
	}
	mkdir(t, rootfs+"-notabase")

	term := filepath.Join(ws, ".xbin", "term")
	must(t, Stamp(mkdir(t, filepath.Join(term, "apps~t-1")), Stamps{Base: "b-term"}))
	mkdir(t, filepath.Join(term, "apps~old-2", "upper"))
	mkdir(t, filepath.Join(term, "view-abc"))
	sbx := filepath.Join(ws, ".xbin", "sbx", "apps~m-3")
	must(t, Stamp(mkdir(t, filepath.Join(sbx, webDir, CurDir)), Stamps{Base: "b-sbx", Overlay: OverlayFuse}))
	mkdir(t, filepath.Join(sbx, newDir))
	must(t, Stamp(mkdir(t, filepath.Join(sbx, webDir, "snapshots", "s1")), Stamps{Base: "b-snap", Overlay: OverlayFuse}))
	for _, d := range []string{
		filepath.Join(sbx, ".trash", "fedcba987654", CurDir), // a deleted sandbox
		filepath.Join(sbx, ".trash", "0123456789ab.r4nd"),    // a reset's old cur
		filepath.Join(sbx, webDir, "tmp", "r4nd"),            // staging
		filepath.Join(sbx, "old", CurDir),                    // no uid
		filepath.Join(sbx, "old"),
		filepath.Join(sbx, "odd.0123456789AB", CurDir), // not a uid
		filepath.Join(sbx, ".hidden.0123456789ab", CurDir),
	} {
		must(t, Stamp(mkdir(t, d), Stamps{Base: "b-gone"}))
	}
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
	pins, err := Pinned(ws, func() ([]string, error) { return []string{"b-def", ""}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(pins); got != "b-def,b-sbx,b-snap,b-term,v0" {
		t.Fatalf("pinned: %s", got)
	}
	gone := GC(rootfs, pins)
	sort.Strings(gone)
	if len(gone) != 2 || gone[0] != rootfs+"-b-free" || gone[1] != rootfs+"-b-gone" {
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
	pins, err := Pinned(ws, func() ([]string, error) { return []string{"b-def"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	GC(rootfs, pins)
	if got := remaining(t, rootfs); got != "b-def,b-sbx,b-snap,notabase" {
		t.Fatalf("with sandbox pins: %s", got)
	}
	// The snapshot is deleted, the sandbox reset onto the current base, the
	// definition too: nothing old is pinned any more.
	os.RemoveAll(filepath.Join(ws, ".xbin", "sbx", "apps~m-3", webDir, "snapshots"))
	must(t, Stamp(filepath.Join(ws, ".xbin", "sbx", "apps~m-3", webDir, CurDir), Stamps{Base: "b-cur"}))
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
	stamp := filepath.Join(ws, ".xbin", "sbx", "apps~m-3", webDir, CurDir, BaseFile)
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
	// So is a cur/ swapped for a symlink (to a dir with a readable stamp):
	// refused, not followed.
	os.Remove(stamp)
	cur := filepath.Dir(stamp)
	os.Rename(cur, cur+".real")
	os.Symlink(cur+".real", cur)
	if _, err := Pinned(ws, nil); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a symlinked cur/: %v", err)
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
		got[l.Tree+":"+l.Key+"/"+l.Sandbox+"."+l.UID+"/"+l.Snapshot] = l
	}
	if len(got) != 5 {
		t.Fatalf("layers: %+v", ls)
	}
	for id, want := range map[string]Layer{
		"term:apps~t-1/./":               {Stamps: Stamps{Base: "b-term"}, Resolved: rootfs + "-b-term", Outdated: true},
		"term:apps~old-2/./":             {Stamps: Stamps{Base: Legacy}, Resolved: rootfs + "-v0", Outdated: true},
		"sbx:apps~m-3/" + webDir + "/":   {Stamps: Stamps{Base: "b-sbx", Overlay: OverlayFuse}, Resolved: rootfs + "-b-sbx", Outdated: true},
		"sbx:apps~m-3/" + newDir + "/":   {},
		"sbx:apps~m-3/" + webDir + "/s1": {Stamps: Stamps{Base: "b-snap", Overlay: OverlayFuse}, Missing: true, Outdated: true},
	} {
		l, ok := got[id]
		if !ok {
			t.Fatalf("%s not reported: %+v", id, ls)
		}
		if l.Stamps != want.Stamps || l.Resolved != want.Resolved || l.Missing != want.Missing || l.Outdated != want.Outdated || l.Err != "" {
			t.Fatalf("%s: %+v, want %+v", id, l, want)
		}
	}
	sbx := filepath.Join(ws, ".xbin", "sbx", "apps~m-3")
	if l := got["sbx:apps~m-3/"+webDir+"/s1"]; l.Dir != filepath.Join(sbx, webDir, "snapshots", "s1") || l.Sandbox != "web" || l.UID != "0123456789ab" {
		t.Fatalf("snapshot: %+v", l)
	}
	if l := got["sbx:apps~m-3/"+webDir+"/"]; l.Dir != filepath.Join(sbx, webDir, CurDir) {
		t.Fatalf("a sandbox's layer is its cur/: %q", l.Dir)
	}
}

// An error from the definitions' source (an unreadable definitions file)
// makes the set unknown, as an unreadable stamp does; what it did return
// still counts.
func TestPinnedExtraError(t *testing.T) {
	ws, _ := pinWS(t)
	pins, err := Pinned(ws, func() ([]string, error) {
		return []string{"b-def"}, errors.New("data/sandboxes.json: unexpected EOF")
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("an erroring definitions source: %v", err)
	}
	if !pins["b-def"] || !pins["b-sbx"] {
		t.Fatalf("pins: %s", keys(pins))
	}
}

// A state dir name is `<name>.<uid>`, split at its last dot; the uid is 12
// lowercase hex.
func TestSplitStateDir(t *testing.T) {
	for in, want := range map[string]string{
		"web.0123456789ab":        "web 0123456789ab",
		"a.b.0123456789ab":        "a.b 0123456789ab",
		"web":                     "",
		".trash":                  "",
		".0123456789ab":           "",
		".x.0123456789ab":         "",
		"web.0123456789AB":        "",
		"web.0123456789a":         "",
		"web.0123456789abc":       "",
		"web.0123456789ab.":       "",
		"0123456789ab.r4nd":       "",
		"web.0123456789ab.backup": "",
	} {
		n, u, ok := SplitStateDir(in)
		got := ""
		if ok {
			got = n + " " + u
		}
		if got != want {
			t.Errorf("SplitStateDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// covers PD-22 — a person's terminal layer on a partitioned tile
// (.xbin/term-part/<TK>/<pkey>) pins its base like a tile's: a base only it
// pins survives GC (the person's persistent apt and /etc changes stand on
// it), and goes once the layer does. List names it by tree term-part and
// key "<TK>/<pkey>"; an unstamped one pins the legacy base, as a tile's.
func TestPersonLayerPinsItsBase(t *testing.T) {
	ws, rootfs := pinWS(t)
	os.RemoveAll(filepath.Join(ws, ".xbin", "term"))
	os.RemoveAll(filepath.Join(ws, ".xbin", "sbx"))
	stampBase(t, rootfs+"-b-person", "b-person")
	part := filepath.Join(ws, ".xbin", TreeTermPart, "0123abcd")
	must(t, Stamp(mkdir(t, filepath.Join(part, "u-ana")), Stamps{Base: "b-person"}))
	mkdir(t, filepath.Join(part, "u-bob", "upper")) // unstamped: the legacy base
	ls, err := List(ws)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range ls {
		got[l.Tree+":"+l.Key] = l.Base
	}
	if len(got) != 2 || got["term-part:0123abcd/u-ana"] != "b-person" || got["term-part:0123abcd/u-bob"] != Legacy {
		t.Fatalf("person layers listed: %v", got)
	}
	pins, err := Pinned(ws, nil)
	if err != nil || keys(pins) != "b-person,v0" {
		t.Fatalf("pinned: %s %v", keys(pins), err)
	}
	GC(rootfs, pins)
	if got := remaining(t, rootfs); got != "b-person,notabase,v0" {
		t.Fatalf("after GC: %s", got)
	}
	os.RemoveAll(filepath.Join(ws, ".xbin", TreeTermPart))
	pins, _ = Pinned(ws, nil)
	GC(rootfs, pins)
	if got := remaining(t, rootfs); strings.Contains(got, "b-person") {
		t.Fatalf("the base outlived its last person layer: %s", got)
	}
}
