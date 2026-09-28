//go:build linux

package tilesbx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/layers"
)

// cur is k's sandbox's cur/ (fake env, main key).
func (fe *fakeEnv) cur(name string) string {
	fe.t.Helper()
	return filepath.Join(fe.stateDir(fe.k, name), layers.CurDir)
}

func (fe *fakeEnv) def(name string) *Def {
	fe.t.Helper()
	fe.m.mu.Lock()
	defer fe.m.mu.Unlock()
	d, ok := fe.m.defs.get(fe.k, name)
	if !ok {
		fe.t.Fatalf("no sandbox %s", name)
	}
	return d
}

// xattr is p's xattr name ("" when it has none).
func xattr(p, name string) string {
	v := make([]byte, 256)
	n, err := syscall.Getxattr(p, name, v)
	if err != nil {
		return ""
	}
	return string(v[:n])
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func snapOf(t *testing.T, w interface{ Bytes() []byte }) SnapshotInfo {
	t.Helper()
	var s SnapshotInfo
	if err := json.Unmarshal(w.Bytes(), &s); err != nil {
		t.Fatalf("%v: %s", err, w.Bytes())
	}
	return s
}

func (fe *fakeEnv) snapshots(name string) []SnapshotInfo {
	fe.t.Helper()
	w := fe.do(mgr, "GET", "/sandboxes/"+name+"/snapshots", nil)
	fe.want(w, http.StatusOK, "")
	var out struct{ Snapshots []SnapshotInfo }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		fe.t.Fatal(err)
	}
	return out.Snapshots
}

// A snapshot stops the sandbox, copies its upper (xattrs exactly) with its
// stamps and a meta.json, and starts it again; a restore swaps the copy
// back in — the old state put aside, execs killed, running again — and a
// deleted snapshot's id is never handed out again.
func TestSnapshotRestore(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	up := filepath.Join(fe.cur("sb-1"), "upper")
	must(t, os.MkdirAll(filepath.Join(up, "work", "opaque"), 0o755))
	must(t, os.WriteFile(filepath.Join(up, "work", "a"), []byte("one"), 0o640))
	must(t, syscall.Setxattr(filepath.Join(up, "work", "opaque"), "user.fuseoverlayfs.opaque", []byte("y"), 0))
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/execs", map[string]any{"argv": []string{"sleep", "30"}})
	fe.want(w, http.StatusCreated, "")
	var x struct{ ID string }
	must(t, json.Unmarshal(w.Body.Bytes(), &x))
	starts := fe.l.count()

	w = fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "first", "clientId": "c-1"})
	fe.want(w, http.StatusCreated, "")
	if s := snapOf(t, w.Body); s.ID != "s-1" || s.Name != "first" || s.Pending || s.Created == 0 {
		t.Fatalf("the snapshot: %+v", s)
	}
	if in := fe.get("sb-1"); in.State != StateRunning || in.Snapshots != 1 || in.StateDetail != "" || fe.l.count() != starts+1 {
		t.Fatalf("after the snapshot (restarted): %+v, %d starts", in, fe.l.count()-starts)
	}
	w = fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+x.ID, nil)
	if !strings.Contains(w.Body.String(), `"state":"killed"`) {
		t.Fatalf("an exec across the snapshot's stop: %s", w.Body)
	}
	snap := filepath.Join(fe.stateDir(fe.k, "sb-1"), "snapshots", "s-1")
	if got := readFile(t, filepath.Join(snap, "upper", "work", "a")); got != "one" {
		t.Fatalf("the copy: %q", got)
	}
	if st, _ := layers.Read(snap); st.Base != "b-test" || st.Overlay == "" {
		t.Fatalf("the snapshot's stamps: %+v", st)
	}
	var sm snapMeta
	must(t, json.Unmarshal([]byte(readFile(t, filepath.Join(snap, snapMetaFile))), &sm))
	if sm.ID != "s-1" || sm.Mode != ModeNamespace || sm.Base != "b-test" || sm.ClientID != "c-1" {
		t.Fatalf("meta.json: %+v", sm)
	}
	// a clientId repeat answers the same snapshot; another request with it is exists
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "first", "clientId": "c-1"})
	fe.want(w, http.StatusOK, "")
	if s := snapOf(t, w.Body); s.ID != "s-1" {
		t.Fatalf("a repeat: %+v", s)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "other", "clientId": "c-1"}), http.StatusConflict, RefExists)

	// the state moves on; the restore brings s-1 back
	must(t, os.WriteFile(filepath.Join(up, "work", "a"), []byte("two"), 0o640))
	must(t, os.WriteFile(filepath.Join(up, "work", "opaque", "new"), []byte("x"), 0o640))
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/execs", map[string]any{"argv": []string{"sleep", "30"}})
	fe.want(w, http.StatusCreated, "")
	must(t, json.Unmarshal(w.Body.Bytes(), &x))
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots/s-1/restore", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateRunning || in.Base.Version != "b-test" || in.StateDetail != "" {
		t.Fatalf("restored: %+v", in)
	}
	up = filepath.Join(fe.cur("sb-1"), "upper")
	if got := readFile(t, filepath.Join(up, "work", "a")); got != "one" {
		t.Fatalf("after the restore: %q", got)
	}
	if exists(filepath.Join(up, "work", "opaque", "new")) {
		t.Fatal("a file made after the snapshot survived its restore")
	}
	if v := xattr(filepath.Join(up, "work", "opaque"), "user.fuseoverlayfs.opaque"); v != "y" {
		t.Fatalf("the opaque marker through the snapshot and its restore: %q", v)
	}
	if fi, _ := os.Stat(filepath.Join(up, "work", "a")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("a mode through the copies: %v", fi.Mode())
	}
	w = fe.do(mgr, "GET", "/sandboxes/sb-1/execs/"+x.ID, nil)
	if !strings.Contains(w.Body.String(), `"state":"killed"`) {
		t.Fatalf("an exec across the restore: %s", w.Body)
	}
	fe.m.trash.wait()
	if ents, _ := os.ReadDir(filepath.Join(fe.stateDir(fe.k, "sb-1"), "tmp")); len(ents) != 0 {
		t.Fatalf("staging left: %v", ents)
	}
	trash, _ := fe.m.TrashDir(fe.k)
	if ents, _ := os.ReadDir(trash); len(ents) != 0 {
		t.Fatalf("the old state wasn't removed: %v", ents)
	}

	// delete: gone from the list and the disk; the next id is s-2
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/sb-1/snapshots/s-1", nil), http.StatusNoContent, "")
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/sb-1/snapshots/s-1", nil), http.StatusNotFound, RefNotFound)
	if l := fe.snapshots("sb-1"); len(l) != 0 {
		t.Fatalf("after the delete: %+v", l)
	}
	fe.m.trash.wait()
	if exists(snap) {
		t.Fatal("the deleted snapshot is still there")
	}
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "again"})
	fe.want(w, http.StatusCreated, "")
	if s := snapOf(t, w.Body); s.ID != "s-2" {
		t.Fatalf("the next snapshot: %+v", s)
	}
	// a stopped sandbox is snapshotted and restored stopped
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "stopped"}), http.StatusCreated, "")
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots/s-3/restore", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || in.Snapshots != 2 {
		t.Fatalf("a stopped restore: %+v", in)
	}
}

// A snapshot pins its base: a rebase moves the sandbox on and a restore
// brings the old base back (Def.base follows); a reset keeps the
// snapshot's base pinned for the GC; a clone of the old snapshot runs on it.
func TestSnapshotBases(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "old"}), http.StatusCreated, "")
	// a new base is installed; the old one kept as a sibling
	must(t, os.MkdirAll(filepath.Join(fe.m.rootfs+"-b-test", "etc"), 0o755))
	must(t, os.WriteFile(filepath.Join(fe.m.rootfs, layers.VersionFile), []byte("b-new\n"), 0o644))
	fe.m.baseVersion = "b-new" // (read at New: as after the upgrade's restart)
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/rebase", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.Base.Version != "b-new" {
		t.Fatalf("rebased: %+v", in.Base)
	}
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots/s-1/restore", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.Base.Version != "b-test" || fe.def("sb-1").Base != "b-test" || in.State != StateRunning {
		t.Fatalf("restored: %+v", in)
	}
	if st, _ := layers.Read(fe.cur("sb-1")); st.Base != "b-test" {
		t.Fatalf("cur's stamp: %+v", st)
	}
	if lower := fe.l.last().spec.Lower[0]; lower != fe.m.rootfs+"-b-test" {
		t.Fatalf("it runs on %s, not the snapshot's base", lower)
	}
	// reset: the sandbox is on the new base; the snapshot still pins the old
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/reset", nil), http.StatusOK, "")
	pins, err := layers.Pinned(fe.m.root, func() ([]string, error) { return DefBases(fe.m.root) })
	if err != nil || !pins["b-test"] || !pins["b-new"] {
		t.Fatalf("pins after a reset: %v %v", pins, err)
	}
	// a clone of the old snapshot takes its base
	w = fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "sb-2", "mode": "namespace", "start": true,
		"from": map[string]any{"sandbox": "sb-1", "snapshot": "s-1"}})
	fe.want(w, http.StatusCreated, "")
	if in := fe.info(w); in.State != StateRunning || in.Base.Version != "b-test" || !in.Base.Outdated {
		t.Fatalf("the clone: %+v", in)
	}
	if lower := fe.l.last().spec.Lower[0]; lower != fe.m.rootfs+"-b-test" {
		t.Fatalf("the clone runs on %s", lower)
	}
	// a base nobody has any more can't be restored or cloned
	must(t, os.RemoveAll(fe.m.rootfs+"-b-test"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots/s-1/restore", nil), http.StatusBadRequest, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "sb-3", "mode": "namespace",
		"from": map[string]any{"sandbox": "sb-1", "snapshot": "s-1"}}), http.StatusBadRequest, RefInvalid)
}

// A clone copies a snapshot whatever its source does, or a stopped
// source's cur/ — never a running one's — in the same mode and flavour,
// counted against the tile's sandboxes and its disk.
func TestClone(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("src"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/start", nil), http.StatusOK, "")
	must(t, os.WriteFile(filepath.Join(fe.cur("src"), "upper", "f"), []byte("snap"), 0o644))
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/snapshots", map[string]any{"name": "s"}), http.StatusCreated, "")
	must(t, os.WriteFile(filepath.Join(fe.cur("src"), "upper", "f"), []byte("live"), 0o644))

	clone := func(name string, from map[string]any, extra ...string) *httptest.ResponseRecorder {
		body := map[string]any{"name": name, "mode": "namespace", "from": from}
		for i := 0; i+1 < len(extra); i += 2 {
			body[extra[i]] = extra[i+1]
		}
		return fe.do(mgr, "POST", "/sandboxes", body)
	}
	// of a snapshot, while the source runs
	w := clone("c-1", map[string]any{"sandbox": "src", "snapshot": "s-1"})
	fe.want(w, http.StatusCreated, "")
	in := fe.info(w)
	if in.State != StateStopped || in.UID == fe.get("src").UID || in.Base.Version != "b-test" {
		t.Fatalf("the clone: %+v", in)
	}
	if got := readFile(t, filepath.Join(fe.cur("c-1"), "upper", "f")); got != "snap" {
		t.Fatalf("the clone's state: %q", got)
	}
	if st, _ := layers.Read(fe.cur("c-1")); st.Base != "b-test" || st.Overlay == "" {
		t.Fatalf("the clone's stamps: %+v", st)
	}
	if fe.def("c-1").Pending != "" {
		t.Fatal("a made clone is still pending")
	}
	// of the running source's cur/: refused
	w = clone("c-2", map[string]any{"sandbox": "src"})
	fe.want(w, http.StatusConflict, RefState)
	if !strings.Contains(w.Body.String(), "clone a snapshot") {
		t.Fatalf("the refusal: %s", w.Body)
	}
	// stopped: its cur/
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/stop", nil), http.StatusOK, "")
	fe.want(clone("c-2", map[string]any{"sandbox": "src"}), http.StatusCreated, "")
	if got := readFile(t, filepath.Join(fe.cur("c-2"), "upper", "f")); got != "live" {
		t.Fatalf("a clone of the cur: %q", got)
	}
	if in := fe.get("src"); in.StateDetail != "" || in.State != StateStopped {
		t.Fatalf("the source after: %+v", in)
	}
	// refusals: another mode, a snapshot that isn't there, another source
	fe.want(fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "c-3", "mode": "vm", "from": map[string]any{"sandbox": "src"}}), http.StatusBadRequest, RefInvalid)
	fe.want(clone("c-3", map[string]any{"sandbox": "src", "snapshot": "s-9"}), http.StatusNotFound, RefNotFound)
	fe.want(clone("c-3", map[string]any{"sandbox": "src", "snapshot": "9"}), http.StatusBadRequest, RefInvalid)
	fe.want(clone("c-3", map[string]any{"sandbox": "nope"}), http.StatusNotFound, RefNotFound)
	// a clientId repeat answers the clone
	fe.want(clone("c-4", map[string]any{"sandbox": "src", "snapshot": "s-1"}, "clientId", "k-1"), http.StatusCreated, "")
	fe.want(clone("c-4", map[string]any{"sandbox": "src", "snapshot": "s-1"}, "clientId", "k-1"), http.StatusOK, "")
	// another overlay flavour can't be read here
	mp := filepath.Join(fe.stateDir(fe.k, "src"), "snapshots", "s-1", snapMetaFile)
	var sm snapMeta
	must(t, json.Unmarshal([]byte(readFile(t, mp)), &sm))
	other := layers.OverlayFuse
	if overlayFlavour() == layers.OverlayFuse {
		other = layers.OverlayKernel
	}
	sm.Overlay = other
	b, _ := json.Marshal(sm)
	must(t, os.WriteFile(mp, b, 0o600))
	fe.m.mu.Lock()
	fe.m.live[fe.k]["src"].snapsLoaded = false // read again
	fe.m.mu.Unlock()
	w = clone("c-5", map[string]any{"sandbox": "src", "snapshot": "s-1"})
	fe.want(w, http.StatusBadRequest, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/snapshots/s-1/restore", nil), http.StatusBadRequest, RefInvalid)
	if !strings.Contains(w.Body.String(), "overlay") {
		t.Fatalf("the flavour refusal: %s", w.Body)
	}
	// a delete of a clone's source's snapshot waits for no one once it is made
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/src/snapshots/s-1", nil), http.StatusNoContent, "")
}

// The tile's disk and count caps hold for copies: a snapshot or clone
// whose bytes would pass perTile.diskGiB is 429, and a clone counts
// against perTile.max from the moment it is stored.
func TestCopiesAgainstTheCaps(t *testing.T) {
	var mu sync.Mutex
	size := int64(0)
	fe := newFakeEnv(t, func(o *Options) {
		o.DiskUsage = func(context.Context, string) (int64, error) { mu.Lock(); defer mu.Unlock(); return size, nil }
	})
	fe.want(fe.do(admin, "PUT", "/sandboxes/policy", `{"overrides":{"apps/mgr":{"perTile":{"max":3,"diskGiB":10}}}}`), http.StatusOK, "")
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	mu.Lock()
	size = 6 << 30 // its upper, when measured
	mu.Unlock()
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusOK, "")
	fe.m.waitUsage()
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "big"})
	fe.want(w, http.StatusTooManyRequests, RefLimit)
	fe.want(fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "c", "mode": "namespace", "from": map[string]any{"sandbox": "sb-1"}}),
		http.StatusTooManyRequests, RefLimit)
	mu.Lock()
	size = 1 << 30
	mu.Unlock()
	fe.m.measureSoon(fe.k, fe.def("sb-1"))
	fe.m.waitUsage()
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "small"}), http.StatusCreated, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "c-1", "mode": "namespace", "from": map[string]any{"sandbox": "sb-1"}}), http.StatusCreated, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "c-2", "mode": "namespace", "from": map[string]any{"sandbox": "sb-1", "snapshot": "s-1"}}), http.StatusCreated, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "c-3", "mode": "namespace", "from": map[string]any{"sandbox": "sb-1"}}), http.StatusTooManyRequests, RefLimit)
}

// Copies running at once each count the others': what a snapshot or a
// clone is copying is booked against perTile.diskGiB until it ends, so
// concurrent copies can't together pass the cap each alone stays within.
func TestCopiesBookTheirBytes(t *testing.T) {
	fe := newFakeEnv(t, func(o *Options) {
		o.DiskUsage = func(context.Context, string) (int64, error) { return 3 << 30, nil } // every state, and every copy
	})
	fe.want(fe.do(admin, "PUT", "/sandboxes/policy", `{"overrides":{"apps/mgr":{"perTile":{"max":8,"diskGiB":10}}}}`), http.StatusOK, "")
	for _, n := range []string{"sb-1", "sb-2"} {
		fe.create(ns(n))
		fe.want(fe.do(mgr, "POST", "/sandboxes/"+n+"/start", nil), http.StatusOK, "")
		fe.want(fe.do(mgr, "POST", "/sandboxes/"+n+"/stop", nil), http.StatusOK, "")
	}
	fe.m.waitUsage() // 6 GiB in all
	g := newGate(fe.m.copyTree)
	fe.m.copyTree = g.copyTree
	// two snapshots at once: 6 + 3 fits, 6 + 3 + 3 doesn't
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots?wait=0", map[string]any{"name": "a"}), http.StatusAccepted, "")
	<-g.entered
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-2/snapshots?wait=0", map[string]any{"name": "b"}), http.StatusTooManyRequests, RefLimit)
	close(g.release)
	fe.waitSettled("sb-1")
	fe.m.waitUsage()
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/sb-2", nil), http.StatusNoContent, "") // 3 GiB: sb-1 and its s-1, as measured
	// clones of s-1 (3 GiB) at once: 3 + 3 + 3 fits, a third one's 3 more doesn't
	g2 := newGate(g.next)
	fe.m.copyTree = g2.copyTree
	for _, n := range []string{"c-1", "c-2"} {
		w := fe.do(mgr, "POST", "/sandboxes?wait=0", map[string]any{"name": n, "mode": "namespace", "from": map[string]any{"sandbox": "sb-1", "snapshot": "s-1"}})
		fe.want(w, http.StatusCreated, "")
		<-g2.entered
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes?wait=0", map[string]any{"name": "c-3", "mode": "namespace", "from": map[string]any{"sandbox": "sb-1", "snapshot": "s-1"}}),
		http.StatusTooManyRequests, RefLimit)
	close(g2.release)
	fe.waitState("c-1", StateStopped)
	fe.waitState("c-2", StateStopped)
	fe.m.waitUsage()
	fe.want(fe.do(mgr, "POST", "/sandboxes?wait=0", map[string]any{"name": "c-3", "mode": "namespace", "from": map[string]any{"sandbox": "sb-1", "snapshot": "s-1"}}),
		http.StatusTooManyRequests, RefLimit) // made, they count as measured
}

// waitSettled waits until no copy keeps the sandbox busy.
func (fe *fakeEnv) waitSettled(name string) Info {
	fe.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		in := fe.get(name)
		if !strings.HasPrefix(in.StateDetail, "busy:") {
			return in
		}
		if time.Now().After(deadline) {
			fe.t.Fatalf("sandbox %q is still %s", name, in.StateDetail)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// xbind shutting down mid-copy (StopAll) cancels the copy, and the
// sandbox it stopped for it isn't started again: StopAll's own stop found
// it stopped and waits for nothing after the copy.
func TestStopAllMidCopyStartsNothing(t *testing.T) {
	fe := newFakeEnv(t)
	g := newGate(fe.m.copyTree)
	fe.m.copyTree = g.copyTree
	for _, n := range []string{"sb-1", "sb-2"} {
		fe.create(ns(n))
		fe.want(fe.do(mgr, "POST", "/sandboxes/"+n+"/start", nil), http.StatusOK, "")
		fe.want(fe.do(mgr, "POST", "/sandboxes/"+n+"/snapshots?wait=0", map[string]any{"name": "s"}), http.StatusAccepted, "")
		<-g.entered
	}
	fe.m.StopAll("xbind is shutting down")
	for _, n := range []string{"sb-1", "sb-2"} {
		if in := fe.waitSettled(n); in.State != StateStopped || fe.runOf(n) != nil {
			t.Fatalf("%s after StopAll cut its copy short: %+v", n, in)
		}
		if l := fe.snapshots(n); len(l) != 0 {
			t.Fatalf("%s: a snapshot cut short is listed: %+v", n, l)
		}
	}
}

// gate is a copyTree that blocks until released, then copies (next): a
// copy that takes a while.
type gate struct {
	entered chan struct{}
	release chan struct{}
	next    func(ctx context.Context, src, dst string) error
}

func newGate(next func(ctx context.Context, src, dst string) error) *gate {
	return &gate{entered: make(chan struct{}, 8), release: make(chan struct{}), next: next}
}

func (g *gate) copyTree(ctx context.Context, src, dst string) error {
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.next(ctx, src, dst)
}

// While a copy runs the sandbox is busy: ?wait=0 answers 202 pending (a
// snapshot) or the sandbox with its busy stateDetail (a restore); the
// lifecycle, another copy, DELETE and a call without autoStart answer 409
// with retryAfterMs; one with autoStart waits it out.
func TestCopyBusy(t *testing.T) {
	fe := newFakeEnv(t)
	g := newGate(fe.m.copyTree)
	fe.m.copyTree = g.copyTree
	fe.create(map[string]any{"name": "sb-1", "mode": "namespace", "autoStart": false})
	fe.create(map[string]any{"name": "sb-2", "mode": "namespace"})
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-2/start", nil), http.StatusOK, "")

	w := fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots?wait=0", map[string]any{"name": "slow", "clientId": "c"})
	fe.want(w, http.StatusAccepted, "")
	if s := snapOf(t, w.Body); s.ID != "s-1" || !s.Pending {
		t.Fatalf("202: %+v", s)
	}
	<-g.entered
	if l := fe.snapshots("sb-1"); len(l) != 1 || !l[0].Pending {
		t.Fatalf("listed while taken: %+v", l)
	}
	if in := fe.get("sb-1"); in.StateDetail != "busy: taking snapshot s-1" {
		t.Fatalf("busy: %+v", in)
	}
	for _, c := range [][2]string{{"POST", "/sandboxes/sb-1/start"}, {"POST", "/sandboxes/sb-1/stop"}, {"POST", "/sandboxes/sb-1/reset"},
		{"POST", "/sandboxes/sb-1/rebase"}, {"DELETE", "/sandboxes/sb-1"}, {"POST", "/sandboxes/sb-1/snapshots/s-1/restore"},
		{"DELETE", "/sandboxes/sb-1/snapshots/s-1"}} {
		w := fe.do(mgr, c[0], c[1], nil)
		fe.want(w, http.StatusConflict, RefState)
		if !strings.Contains(w.Body.String(), `"retryAfterMs"`) || !strings.Contains(w.Body.String(), "busy") {
			t.Fatalf("%s %s: %s", c[0], c[1], w.Body)
		}
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "another"}), http.StatusConflict, RefState)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/run", map[string]any{"argv": []string{"true"}}), http.StatusConflict, RefState) // no autoStart
	// a repeat of the pending one waits with it
	repeat := make(chan int, 1)
	go func() {
		repeat <- fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "slow", "clientId": "c"}).Code
	}()
	// sb-2 (autoStart): a run waits the copy out, then runs
	w = fe.do(mgr, "POST", "/sandboxes/sb-2/snapshots?wait=0", map[string]any{"name": "slow2"})
	fe.want(w, http.StatusAccepted, "")
	<-g.entered
	ran := make(chan int, 1)
	go func() {
		ran <- fe.do(mgr, "POST", "/sandboxes/sb-2/run", map[string]any{"argv": []string{"true"}}).Code
	}()
	select {
	case c := <-ran:
		t.Fatalf("a run on a busy sandbox answered %d at once", c)
	case <-time.After(200 * time.Millisecond):
	}
	close(g.release)
	if c := <-ran; c != http.StatusOK {
		t.Fatalf("the run after the copy: %d", c)
	}
	if c := <-repeat; c != http.StatusOK {
		t.Fatalf("the repeat after the copy: %d", c)
	}
	if l := fe.snapshots("sb-1"); len(l) != 1 || l[0].Pending {
		t.Fatalf("listed after: %+v", l)
	}
	fe.waitState("sb-1", StateRunning)

	// a restore: ?wait=0 answers the sandbox, busy
	g2 := newGate(g.next)
	fe.m.copyTree = g2.copyTree
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots/s-1/restore?wait=0", nil)
	fe.want(w, http.StatusOK, "")
	<-g2.entered
	if in := fe.get("sb-1"); in.StateDetail != "busy: restoring snapshot s-1" {
		t.Fatalf("restoring: %+v", in)
	}
	close(g2.release)
	fe.waitState("sb-1", StateRunning)
	if in := fe.get("sb-1"); in.StateDetail != "" {
		t.Fatalf("after the restore: %+v", in)
	}
}

// A clone copying is `creating`: ?wait=0 answers it so, every call but GET,
// the list and DELETE is 409, its source is held, and a DELETE ends the
// copy and removes what it made.
func TestCloneCreating(t *testing.T) {
	fe := newFakeEnv(t)
	g := newGate(fe.m.copyTree)
	fe.m.copyTree = g.copyTree
	fe.create(ns("src"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/stop", nil), http.StatusOK, "")

	w := fe.do(mgr, "POST", "/sandboxes?wait=0", map[string]any{"name": "c-1", "mode": "namespace", "start": true, "from": map[string]any{"sandbox": "src"}})
	fe.want(w, http.StatusCreated, "")
	if in := fe.info(w); in.State != StateCreating {
		t.Fatalf("?wait=0: %+v", in)
	}
	<-g.entered
	for _, c := range [][2]string{{"POST", "/sandboxes/c-1/start"}, {"POST", "/sandboxes/c-1/stop"}, {"POST", "/sandboxes/c-1/reset"},
		{"PATCH", "/sandboxes/c-1"}, {"GET", "/sandboxes/c-1/snapshots"}, {"POST", "/sandboxes/c-1/run"}} {
		fe.want(fe.do(mgr, c[0], c[1], map[string]any{"argv": []string{"true"}}), http.StatusConflict, RefState)
	}
	fe.want(fe.do(mgr, "GET", "/sandboxes/c-1", nil), http.StatusOK, "")
	// the source (its cur/ being copied) is busy
	if in := fe.get("src"); !strings.HasPrefix(in.StateDetail, "busy: being cloned") {
		t.Fatalf("the source: %+v", in)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/start", nil), http.StatusConflict, RefState)
	close(g.release)
	fe.waitState("c-1", StateRunning) // start: true
	if in := fe.get("src"); in.StateDetail != "" {
		t.Fatalf("the source after: %+v", in)
	}

	// a delete while creating ends the copy
	g2 := newGate(g.next)
	fe.m.copyTree = g2.copyTree
	fe.want(fe.do(mgr, "POST", "/sandboxes?wait=0", map[string]any{"name": "c-2", "mode": "namespace", "from": map[string]any{"sandbox": "src"}}), http.StatusCreated, "")
	<-g2.entered
	dir := fe.stateDir(fe.k, "c-2")
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/c-2", nil), http.StatusNoContent, "")
	fe.m.trash.wait()
	if exists(dir) {
		t.Fatal("the deleted clone's state is still there")
	}
	if in := fe.get("src"); in.StateDetail != "" {
		t.Fatalf("the source after a cancelled clone: %+v", in)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/start", nil), http.StatusOK, "")

	// a clone of a snapshot: the source runs on, but neither it nor the
	// snapshot is deleted from under the copy
	fe.m.copyTree = g.next
	fe.want(fe.do(mgr, "POST", "/sandboxes/src/snapshots", map[string]any{"name": "s"}), http.StatusCreated, "")
	g3 := newGate(g.next)
	fe.m.copyTree = g3.copyTree
	fe.want(fe.do(mgr, "POST", "/sandboxes?wait=0", map[string]any{"name": "c-3", "mode": "namespace",
		"from": map[string]any{"sandbox": "src", "snapshot": "s-1"}}), http.StatusCreated, "")
	<-g3.entered
	fe.running(fe.k, "src")
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/src/snapshots/s-1", nil), http.StatusConflict, RefState)
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/src", nil), http.StatusConflict, RefState)
	close(g3.release)
	fe.waitState("c-3", StateStopped)
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/src/snapshots/s-1", nil), http.StatusNoContent, "")
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/src", nil), http.StatusNoContent, "")
}

// A copy cut short leaves nothing half-done: a failed snapshot is never
// listed and its id never handed out again; a restore whose swap fails
// leaves the state as it was; what was staged is removed.
func TestCopyFailures(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	must(t, os.WriteFile(filepath.Join(fe.cur("sb-1"), "upper", "f"), []byte("one"), 0o644))
	real := fe.m.copyTree
	fe.m.copyTree = func(context.Context, string, string) error { return errors.New("the disk broke") }
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "x"})
	fe.want(w, http.StatusInternalServerError, "")
	if !strings.Contains(w.Body.String(), "the disk broke") {
		t.Fatalf("the answer: %s", w.Body)
	}
	if l := fe.snapshots("sb-1"); len(l) != 0 {
		t.Fatalf("a failed snapshot listed: %+v", l)
	}
	fe.waitState("sb-1", StateRunning) // started again all the same
	fe.m.copyTree = real
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "y"})
	fe.want(w, http.StatusCreated, "")
	if s := snapOf(t, w.Body); s.ID != "s-2" {
		t.Fatalf("after a failed one: %+v", s)
	}
	must(t, os.WriteFile(filepath.Join(fe.cur("sb-1"), "upper", "f"), []byte("two"), 0o644))
	fe.m.exchange = func(string, string) error { return syscall.EIO }
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots/s-2/restore", nil), http.StatusInternalServerError, "")
	if got := readFile(t, filepath.Join(fe.cur("sb-1"), "upper", "f")); got != "two" {
		t.Fatalf("a failed restore changed the state: %q", got)
	}
	fe.waitState("sb-1", StateRunning)
	fe.m.trash.wait()
	if ents, _ := os.ReadDir(filepath.Join(fe.stateDir(fe.k, "sb-1"), "tmp")); len(ents) != 0 {
		t.Fatalf("staging left: %v", ents)
	}
	// a clone that fails is error, and only a DELETE helps
	fe.m.copyTree = func(context.Context, string, string) error { return errors.New("no room") }
	w = fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "c-1", "mode": "namespace", "from": map[string]any{"sandbox": "sb-1", "snapshot": "s-2"}})
	fe.want(w, http.StatusCreated, "")
	if in := fe.info(w); in.State != StateError || !strings.Contains(in.StateDetail, "no room") {
		t.Fatalf("a failed clone: %+v", in)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/c-1/start", nil), http.StatusConflict, RefState)
	fe.want(fe.do(mgr, "POST", "/sandboxes/c-1/reset", nil), http.StatusConflict, RefState)
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/c-1", nil), http.StatusNoContent, "")
}

// At boot every staging dir and every snapshot dir without its meta.json
// is removed, and a clone a restart cut short is error until deleted.
func TestBootReconcilesCopies(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "kept"}), http.StatusCreated, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusOK, "")
	dir := fe.stateDir(fe.k, "sb-1")
	must(t, os.MkdirAll(filepath.Join(dir, "tmp", "abcd1234", "upper"), 0o700))  // a copy cut short
	must(t, os.MkdirAll(filepath.Join(dir, "snapshots", "s-7", "upper"), 0o700)) // renamed without meta (not ours)
	fe.m.mu.Lock()
	d, _ := fe.m.defs.get(fe.k, "sb-1")
	c := d.clone()
	c.Name, c.UID, c.Pending, c.ClientID = "c-1", newUID(), "clone", ""
	must(t, fe.m.defs.put(fe.k, c))
	fe.m.mu.Unlock()
	fe.m.StopAll("restart")

	m2 := New(Options{Root: fe.m.root, Isolated: true, UIDRange: true, Deps: testDeps(), Rootfs: fe.m.rootfs,
		DiskUsage: func(context.Context, string) (int64, error) { return 0, nil }})
	m2.trash.wait()
	if ents, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(ents) != 0 {
		t.Fatalf("staging after the boot: %v", ents)
	}
	if exists(filepath.Join(dir, "snapshots", "s-7")) || !exists(filepath.Join(dir, "snapshots", "s-1")) {
		t.Fatal("the boot's snapshot sweep")
	}
	e2 := &testEnv{t: t, m: m2, mux: routes(m2)}
	w := e2.do(mgr, "GET", "/sandboxes/c-1", nil)
	e2.want(w, http.StatusOK, "")
	if in := e2.info(w); in.State != StateError || !strings.Contains(in.StateDetail, "cut short") {
		t.Fatalf("a cut-short clone: %+v", in)
	}
	if in := e2.info(e2.do(mgr, "GET", "/sandboxes/sb-1", nil)); in.Snapshots != 1 {
		t.Fatalf("snapshots after the boot: %+v", in)
	}
	e2.want(e2.do(mgr, "POST", "/sandboxes/c-1/start", nil), http.StatusConflict, RefState)
	e2.want(e2.do(mgr, "DELETE", "/sandboxes/c-1", nil), http.StatusNoContent, "")
}

// A VM sandbox's snapshot, restore and clone copy its disk sparse
// (fsutil.CloneSparse), never read as a disk: the same bytes, its holes
// kept, its size kept by a clone that asked for less.
func TestVMSnapshotClone(t *testing.T) {
	fe, _ := newVMEnv(t)
	fe.create(vmDef("vm-1", 512))
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil), http.StatusOK, "")
	img := filepath.Join(fe.cur("vm-1"), "vm", "disk.img")
	write := func(p, s string, at int64) {
		f, err := os.OpenFile(p, os.O_WRONLY, 0)
		must(t, err)
		_, err = f.WriteAt([]byte(s), at)
		must(t, err)
		must(t, f.Close())
	}
	read := func(p string, at int64, n int) string {
		f, err := os.Open(p)
		must(t, err)
		defer f.Close()
		b := make([]byte, n)
		_, err = f.ReadAt(b, at)
		must(t, err)
		return string(b)
	}
	write(img, "first", 1<<30)
	w := fe.do(mgr, "POST", "/sandboxes/vm-1/snapshots", map[string]any{"name": "disk"})
	fe.want(w, http.StatusCreated, "")
	s := snapOf(t, w.Body)
	snapImg := filepath.Join(fe.stateDir(fe.k, "vm-1"), "snapshots", "s-1", "vm", "disk.img")
	fi, err := os.Stat(snapImg)
	if err != nil || fi.Size() != 2<<30 || read(snapImg, 1<<30, 5) != "first" {
		t.Fatalf("the snapshot's disk: %v %v", fi, err)
	}
	if a := allocBytes(snapImg); a > 1<<20 || s.Bytes != a {
		t.Fatalf("the snapshot's disk allocates %d (answered %d): its holes were filled", a, s.Bytes)
	}
	if exists(filepath.Join(filepath.Dir(filepath.Dir(snapImg)), "upper")) {
		t.Fatal("a VM snapshot has an upper")
	}
	fe.waitState("vm-1", StateRunning)
	write(img, "later", 1<<30)
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/snapshots/s-1/restore", nil), http.StatusOK, "")
	if got := read(filepath.Join(fe.cur("vm-1"), "vm", "disk.img"), 1<<30, 5); got != "first" {
		t.Fatalf("restored: %q", got)
	}
	// a clone asking for a smaller disk keeps the snapshot's
	w = fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "vm-2", "mode": "vm", "memMiB": 512, "diskGiB": 1,
		"from": map[string]any{"sandbox": "vm-1", "snapshot": "s-1"}})
	fe.want(w, http.StatusCreated, "")
	if in := fe.info(w); in.DiskGiB != 2 || in.State != StateStopped {
		t.Fatalf("the clone: %+v", in)
	}
	if got := read(filepath.Join(fe.cur("vm-2"), "vm", "disk.img"), 1<<30, 5); got != "first" {
		t.Fatalf("the clone's disk: %q", got)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes", map[string]any{"name": "ns-1", "mode": "namespace",
		"from": map[string]any{"sandbox": "vm-1", "snapshot": "s-1"}}), http.StatusBadRequest, RefInvalid)
	// snapshots count toward the tile's disk
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/stop", nil), http.StatusOK, "")
	fe.m.waitUsage()
	if in := fe.get("vm-1"); in.DiskBytes != allocBytes(filepath.Join(fe.cur("vm-1"), "vm", "disk.img"))+allocBytes(snapImg) {
		t.Fatalf("diskBytes %d", in.DiskBytes)
	}
}
