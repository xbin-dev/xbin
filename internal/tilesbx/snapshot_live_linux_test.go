//go:build linux && integration

package tilesbx

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/layers"
)

// seedLower gives a minimal lower what the snapshot tests take away: a
// file (a removed package's) and a directory with entries (/usr/share/doc).
// A rootfs has both already.
func seedLower(t *testing.T, lower string) {
	t.Helper()
	for _, f := range []string{"etc/debian_version", "usr/share/doc/a/copyright", "usr/share/doc/b/copyright"} {
		p := filepath.Join(lower, f)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte("from the base\n"), 0o644))
	}
}

// snapKept is what snapState could make.
type snapKept struct{ opaque, owned bool }

// snapState makes the state the copies must carry exactly: a whiteout (a
// file of the base removed), an opaque directory (one of the base's
// removed and made again, holding only a new file), a file owned by 1000
// (where the sandbox maps more than root) and a plain one. The kernel
// overlay of a tile sandbox can't remove a directory of the base here
// (EIO; fuse-overlayfs and a VM can), and a host mapping one uid can't
// chown: it answers what it made.
func snapState(le *liveEnv, name, v string) (k snapKept) {
	le.probeOK(name, "rm", "/etc/debian_version")
	if out, ex := le.probe(name, "rm", "/usr/share/doc"); ex.Code == 0 {
		le.probeOK(name, "mkdir", "/usr/share/doc")
		le.probeOK(name, "write", "/usr/share/doc/only", "new")
		k.opaque = true
	} else if overlayFlavour() != layers.OverlayKernel || le.runOf(name).def.Mode != ModeNamespace {
		le.t.Fatalf("rm -rf /usr/share/doc in %s: %q", name, out)
	} else {
		le.t.Logf("the kernel overlay can't remove a directory of the base here: %q (the opaque-directory case is fuse-overlayfs's and a VM's)", out)
	}
	le.probeOK(name, "mkdir", "/work")
	le.probeOK(name, "write", "/work/v", v)
	le.probeOK(name, "write", "/work/owned", "theirs")
	_, ex := le.probe(name, "chown", "/work/owned", "1000", "1000")
	k.owned = ex.Code == 0
	return k
}

// checkSnapState checks what snapState made, as the copy brought it back.
func checkSnapState(le *liveEnv, name, v string, k snapKept) {
	le.t.Helper()
	if got := le.probeOK(name, "cat", "/work/v"); got != v {
		le.t.Fatalf("%s: /work/v %q, want %q", name, got, v)
	}
	if got := le.probeOK(name, "exists", "/etc/debian_version"); got != "no\n" {
		le.t.Fatalf("%s: the whiteout didn't survive (/etc/debian_version is back)", name)
	}
	if got := le.probeOK(name, "ls", "/usr/share/doc"); k.opaque && got != "only\n" {
		le.t.Fatalf("%s: the opaque directory holds %q: the base's entries came back", name, got)
	}
	if k.owned {
		if got := le.probeOK(name, "owner", "/work/owned"); got != "1000:1000\n" {
			le.t.Fatalf("%s: /work/owned is %q, want 1000:1000", name, got)
		}
	}
}

func (le *liveEnv) snapshot(name, snap string) SnapshotInfo {
	le.t.Helper()
	w := le.do(mgr, "POST", "/sandboxes/"+name+"/snapshots", map[string]any{"name": snap})
	le.want(w, http.StatusCreated, "")
	var s SnapshotInfo
	must(le.t, json.Unmarshal(w.Body.Bytes(), &s))
	return s
}

// testLiveSnapshots is WP-20's part of TestLive: snapshots, restores and
// clones of live namespace sandboxes, their upper copied confined and
// exactly (whiteouts, opaque directories, owners), and a copy cut short.
func testLiveSnapshots(t *testing.T, le *liveEnv, minimal bool) {
	t.Run("snapshot, restore, clone: whiteouts, opaque dirs and owners survive", func(t *testing.T) {
		le.t = t
		if !confine.Isolated() && le.m.uidRange {
			t.Skip("the copy of a sub-uid's upper runs confined: it needs the rootfs")
		}
		if minimal {
			seedLower(t, le.m.rootfs)
		}
		le.create(map[string]any{"name": "snap", "mode": "namespace", "mounts": []any{probeMount}})
		le.start("snap")
		owned := snapState(le, "snap", "one")
		t.Logf("made: %+v (uid range: %v)", owned, le.m.uidRange)
		start := time.Now()
		s := le.snapshot("snap", "first")
		t.Logf("snapshot %s: %d bytes in %s", s.ID, s.Bytes, time.Since(start).Round(time.Millisecond))
		if in, _ := le.m.infoOf(le.k, "snap"); in.State != StateRunning || in.Snapshots != 1 {
			t.Fatalf("after the snapshot: %+v", in)
		}
		checkSnapState(le, "snap", "one", owned) // the snapshot's restart runs on the same state
		// on: the state changes, and a restore brings the snapshot back
		le.probeOK("snap", "write", "/work/v", "two")
		le.probeOK("snap", "write", "/usr/share/doc/later", "x")
		w := le.do(mgr, "POST", "/sandboxes/snap/snapshots/"+s.ID+"/restore", nil)
		le.want(w, http.StatusOK, "")
		if in := le.info(w); in.State != StateRunning {
			t.Fatalf("restored: %+v\n%s", in, le.logOf("snap"))
		}
		checkSnapState(le, "snap", "one", owned)
		// a clone of the snapshot, the source running on
		w = le.do(mgr, "POST", "/sandboxes", map[string]any{"name": "snap-c", "mode": "namespace", "start": true,
			"mounts": []any{probeMount}, "from": map[string]any{"sandbox": "snap", "snapshot": s.ID}})
		le.want(w, http.StatusCreated, "")
		if in := le.info(w); in.State != StateRunning {
			t.Fatalf("the clone: %+v\n%s", in, le.logOf("snap-c"))
		}
		checkSnapState(le, "snap-c", "one", owned)
		// not of the running source's cur/; of the stopped one's
		le.want(le.do(mgr, "POST", "/sandboxes", map[string]any{"name": "snap-d", "mode": "namespace",
			"from": map[string]any{"sandbox": "snap"}}), http.StatusConflict, RefState)
		le.probeOK("snap", "write", "/work/v", "three")
		le.stop("snap")
		w = le.do(mgr, "POST", "/sandboxes", map[string]any{"name": "snap-d", "mode": "namespace", "start": true,
			"mounts": []any{probeMount}, "from": map[string]any{"sandbox": "snap"}})
		le.want(w, http.StatusCreated, "")
		checkSnapState(le, "snap-d", "three", owned)
		// the snapshot counts toward the tile's disk: measured at the stop
		le.m.waitUsage()
		if in, _ := le.m.infoOf(le.k, "snap"); s.Bytes <= 0 || in.DiskBytes <= s.Bytes {
			t.Fatalf("diskBytes %d with a snapshot of %d", in.DiskBytes, s.Bytes)
		}
		// another flavour's snapshot can't be restored here
		other := findFuseOverlayfs()
		if overlayFlavour() == layers.OverlayFuse {
			other = "none"
		}
		if other != "" {
			t.Setenv("XBIN_FUSE_OVERLAYFS", other)
			if overlayFlavour() != s0flavour(le, "snap", s.ID) {
				le.want(le.do(mgr, "POST", "/sandboxes/snap/snapshots/"+s.ID+"/restore", nil), http.StatusBadRequest, RefInvalid)
				le.want(le.do(mgr, "POST", "/sandboxes", map[string]any{"name": "snap-e", "mode": "namespace",
					"from": map[string]any{"sandbox": "snap", "snapshot": s.ID}}), http.StatusBadRequest, RefInvalid)
			}
		}
	})

	t.Run("a copy cut short leaves nothing half-done", func(t *testing.T) {
		le.t = t
		if !confine.Isolated() && le.m.uidRange {
			t.Skip("the copy of a sub-uid's upper runs confined: it needs the rootfs")
		}
		for _, n := range []string{"snap-c", "snap-d"} {
			le.want(le.do(mgr, "DELETE", "/sandboxes/"+n, nil), http.StatusNoContent, "")
		}
		le.start("snap")
		le.probeOK("snap", "fill", "/work/big/data", "256")
		le.stop("snap")
		real := le.m.copyTree
		entered := make(chan struct{}, 4)
		le.m.copyTree = func(ctx context.Context, src, dst string) error {
			entered <- struct{}{}
			return real(ctx, src, dst)
		}
		defer func() { le.m.copyTree = real }()
		// xbind shutting down mid-copy (StopAll's cancel): a snapshot, then a clone
		le.want(le.do(mgr, "POST", "/sandboxes/snap/snapshots?wait=0", map[string]any{"name": "cut"}), http.StatusAccepted, "")
		<-entered
		le.m.copies.cancelAll()
		w := le.do(mgr, "POST", "/sandboxes?wait=0", map[string]any{"name": "cut-c", "mode": "namespace", "from": map[string]any{"sandbox": "snap"}})
		for w.Code == http.StatusConflict { // the snapshot's job still ending: busy
			time.Sleep(20 * time.Millisecond)
			w = le.do(mgr, "POST", "/sandboxes?wait=0", map[string]any{"name": "cut-c", "mode": "namespace", "from": map[string]any{"sandbox": "snap"}})
		}
		le.want(w, http.StatusCreated, "")
		<-entered
		le.m.copies.cancelAll()
		le.waitState("cut-c", StateError, 30*time.Second)
		for _, s := range le.m.snapshotsOf(le.k, "snap") {
			if s.Name == "cut" {
				t.Fatalf("a snapshot cut short is listed: %+v", s)
			}
		}
		// the next boot: staging emptied, the clone error until it is deleted
		// (no cgroup: its sweep would kill what this runtime still runs)
		le.m.trash.wait()
		deps := le.m.deps
		deps.Cgroup = nil
		m2 := New(Options{Root: le.m.root, Isolated: true, UIDRange: le.m.uidRange, Deps: deps, Rootfs: le.m.rootfs, BxPath: le.m.bxPath})
		m2.trash.wait()
		m2.StopAll("the test's second boot is done")
		for _, n := range []string{"snap", "cut-c"} {
			d, _ := le.m.defs.get(le.k, n)
			dir, _ := le.m.StateDir(le.k, d)
			if ents, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(ents) != 0 {
				t.Fatalf("%s's staging after the boot: %v", n, ents)
			}
		}
		in, _ := m2.infoOf(le.k, "cut-c")
		if in.State != StateError || !strings.Contains(in.StateDetail, "cut short") {
			t.Fatalf("a clone cut short, after the boot: %+v", in)
		}
		le.want(le.do(mgr, "DELETE", "/sandboxes/cut-c", nil), http.StatusNoContent, "")
		le.want(le.do(mgr, "DELETE", "/sandboxes/snap", nil), http.StatusNoContent, "")
		le.m.trash.wait()
	})
}

// s0flavour is the overlay flavour snapshot sid of name was written by.
func s0flavour(le *liveEnv, name, sid string) string {
	d, _ := le.m.defs.get(le.k, name)
	dir, _ := le.m.StateDir(le.k, d)
	st, _ := layers.Read(filepath.Join(dir, "snapshots", sid))
	return st.Overlay
}

// snapshotsOf is k's sandbox's snapshots (none on an error).
func (m *Manager) snapshotsOf(k Key, name string) []SnapshotInfo {
	l, _ := m.Snapshots(k, name)
	return l
}

// testLiveVMSnapshots is WP-20's part of TestLiveVM: a VM sandbox's
// snapshot, restore and clone copy its disk (sparse), so what the guest
// did — a whiteout, an opaque directory, an owner — comes back exactly.
func testLiveVMSnapshots(t *testing.T, lv *liveVMEnv) {
	le := lv.liveEnv
	t.Run("snapshot, restore, clone: the disk copied sparse", func(t *testing.T) {
		le.t = t
		le.create(map[string]any{"name": "vm-snap", "mode": "vm", "memMiB": 512, "vcpus": 1, "diskGiB": 1, "mounts": []any{probeMount}})
		le.start("vm-snap")
		owned := snapState(le, "vm-snap", "one")
		start := time.Now()
		s := le.snapshot("vm-snap", "first")
		t.Logf("snapshot %s: %d bytes in %s (restarted)", s.ID, s.Bytes, time.Since(start).Round(time.Millisecond))
		d, _ := le.m.defs.get(le.k, "vm-snap")
		dir, _ := le.m.StateDir(le.k, d)
		img := filepath.Join(dir, "snapshots", s.ID, "vm", "disk.img")
		if fi, err := os.Stat(img); err != nil || fi.Size() != 1<<30 || s.Bytes <= 0 || s.Bytes >= 1<<30 {
			t.Fatalf("the snapshot's disk: %v %v (%d bytes allocated)", fi, err, s.Bytes)
		}
		checkSnapState(le, "vm-snap", "one", owned)
		le.probeOK("vm-snap", "write", "/work/v", "two")
		le.probeOK("vm-snap", "write", "/usr/share/doc/later", "x")
		w := le.do(mgr, "POST", "/sandboxes/vm-snap/snapshots/"+s.ID+"/restore", nil)
		le.want(w, http.StatusOK, "")
		if in := le.info(w); in.State != StateRunning {
			t.Fatalf("restored: %+v\n%s", in, le.logOf("vm-snap"))
		}
		checkSnapState(le, "vm-snap", "one", owned)
		le.stop("vm-snap")
		w = le.do(mgr, "POST", "/sandboxes", map[string]any{"name": "vm-clone", "mode": "vm", "memMiB": 512, "vcpus": 1, "diskGiB": 1, "start": true,
			"mounts": []any{probeMount}, "from": map[string]any{"sandbox": "vm-snap", "snapshot": s.ID}})
		le.want(w, http.StatusCreated, "")
		if in := le.info(w); in.State != StateRunning || in.DiskGiB != 1 {
			t.Fatalf("the clone: %+v\n%s", in, le.logOf("vm-clone"))
		}
		checkSnapState(le, "vm-clone", "one", owned)
		le.want(le.do(mgr, "POST", "/sandboxes", map[string]any{"name": "ns-of-vm", "mode": "namespace",
			"from": map[string]any{"sandbox": "vm-snap", "snapshot": s.ID}}), http.StatusBadRequest, RefInvalid)
		le.stop("vm-clone")
		for _, n := range []string{"vm-clone", "vm-snap"} {
			le.want(le.do(mgr, "DELETE", "/sandboxes/"+n, nil), http.StatusNoContent, "")
		}
		le.m.trash.wait()
	})
}
