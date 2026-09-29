package broker

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
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// partWS is a workspace for the mode tests: files under root, a broker on
// it (the mode store installed by New), plaintext vaults allowed.
type partWS struct {
	t    *testing.T
	root string
	b    *Broker
}

func newPartWS(t *testing.T, files map[string]string, pinned func(string) (*registry.PinnedCode, bool)) *partWS {
	t.Helper()
	w := &partWS{t: t, root: t.TempDir()}
	w.write(files)
	reg := &registry.Registry{Root: w.root, PinnedPrimary: pinned}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	b.AllowInsecureVault = true
	w.b = b
	return w
}

func (w *partWS) write(files map[string]string) {
	w.t.Helper()
	for rel, body := range files {
		p := filepath.Join(w.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *partWS) rescan() {
	w.t.Helper()
	if err := w.b.Reg.Rescan(); err != nil {
		w.t.Fatal(err)
	}
}

// state is the registry's settled state of tile.
func (w *partWS) state(tile string) (registry.PartitionState, registry.PartitionSpec, *registry.PartitionRequest) {
	w.t.Helper()
	c, ok := w.b.Reg.Component(tile)
	if !ok {
		w.t.Fatalf("%s isn't registered", tile)
	}
	return c.PartitionState()
}

// record reads tile's mode.json from disk (nil: none).
func (w *partWS) record(tile string) *modeRecord {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.root, "data", "partitions", util.TileKey(tile), "mode.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		w.t.Fatal(err)
	}
	var rec modeRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		w.t.Fatal(err)
	}
	return &rec
}

func (w *partWS) ops(tile string) string {
	rec := w.record(tile)
	if rec == nil {
		return ""
	}
	var ops []string
	for _, h := range rec.History {
		ops = append(ops, h.Op)
	}
	return strings.Join(ops, ",")
}

// kvPut writes one key into main's bucket res:<scope>/<name>.
func (w *partWS) kvPut(scope, name string) {
	w.t.Helper()
	err := w.b.kv.db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte("res:" + scope + "/" + name))
		if err != nil {
			return err
		}
		return bk.Put([]byte("k"), []byte("v"))
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// emptyVolume makes the ciphertext directory an initialized, empty volume
// has (what provisioning leaves).
func (w *partWS) emptyVolume(scope, name string) string {
	w.t.Helper()
	dir := filepath.Join(w.root, "data", "resources-enc", util.ScopeKey(scope), name)
	w.write(map[string]string{
		filepath.ToSlash(mustRel(w.t, w.root, filepath.Join(dir, "gocryptfs.conf"))):  "{}",
		filepath.ToSlash(mustRel(w.t, w.root, filepath.Join(dir, "gocryptfs.diriv"))): "iv",
	})
	return dir
}

func mustRel(t *testing.T, root, p string) string {
	t.Helper()
	rel, err := filepath.Rel(root, p)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

var (
	userSpec = registry.PartitionSpec{User: true}
	bothSpec = registry.PartitionSpec{User: true, Global: true}
)

// covers PD-44 PD-50 PD-51 — every row of 01 §2.1's state table: auto on an
// empty tile (a fresh instance with provisioned, empty volumes); pending on
// a tile with one kv key, one vault key, one cron row; declined runs R and
// a new Q′ reopens a request; code back at R is withdrawn; invalid records
// nothing. Pending and invalid hold the primary (the runner's HoldReason),
// and a tile that becomes held is stopped.
func TestPartitionStateTable(t *testing.T) {
	const app = `{"runtime":"go","partition":["user","global"],"uses":[{"target":"res:%s/db","role":"writer"}]}`
	scoped := func(tile string) map[string]string {
		return map[string]string{
			tile + "/scope.json": `{"resources":{"db":{"type":"kv"},"files":{"type":"filesystem"}}}`,
			tile + "/xbin.json":  strings.Replace(app, "%s", tile, 1),
		}
	}
	files := map[string]string{"apps/plain/xbin.json": `{"runtime":"go"}`}
	for _, tile := range []string{"apps/fresh"} {
		for k, v := range scoped(tile) {
			files[k] = v
		}
	}
	pinned := map[string]*registry.PinnedCode{} // a primary pinned to a checkpoint (D119e)
	w := newPartWS(t, files, func(rel string) (*registry.PinnedCode, bool) { pc, ok := pinned[rel]; return pc, ok })
	var stopMu sync.Mutex
	var stopped []string
	w.b.SetPartitionStop(func(tile string) { stopMu.Lock(); stopped = append(stopped, tile); stopMu.Unlock() })
	stops := func() string { stopMu.Lock(); defer stopMu.Unlock(); return strings.Join(stopped, ",") }

	t.Run("auto on an empty tile", func(t *testing.T) {
		if st, r, req := w.state("apps/fresh"); st != registry.PartitionPartitioned || r != bothSpec || req != nil {
			t.Errorf("apps/fresh: %v %v %+v, want partitioned user + global at once", st, r, req)
		}
		if got := w.ops("apps/fresh"); got != "auto" {
			t.Errorf("history %q, want auto", got)
		}
		if w.record("apps/plain") != nil {
			t.Error("a tile that asks for nothing got a mode record")
		}
		// Provisioned, empty volumes are no data: a second empty tile with
		// them switches just as freely.
		w.write(scoped("apps/vols"))
		w.emptyVolume("apps/vols", "files")
		w.rescan()
		if st, _, _ := w.state("apps/vols"); st != registry.PartitionPartitioned {
			t.Errorf("empty volumes counted as data: %v", st)
		}
	})

	t.Run("pending on a kv key", func(t *testing.T) {
		w.kvPut("apps/fresh", "db")
		w.write(map[string]string{"apps/fresh/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/fresh/db","role":"writer"}]}`})
		w.rescan()
		st, r, req := w.state("apps/fresh")
		if st != registry.PartitionPending || r != bothSpec || req == nil || req.Spec != nil || req.Declined {
			t.Fatalf("dropping the key on a tile with data: %v %v %+v, want pending user + global → unpartitioned", st, r, req)
		}
		if got := w.ops("apps/fresh"); got != "auto,request" {
			t.Errorf("history %q", got)
		}
		if why := w.b.PartitionHoldReason("apps/fresh"); !strings.Contains(why, "paused: a partition mode switch is requested (user + global → unpartitioned)") {
			t.Errorf("hold reason %q", why)
		}
		// The stop runs on its own goroutine; only the tile that became held.
		waitFor(t, func() bool { return stops() == "apps/fresh" }, "the pending tile's primary is stopped")
		// The data layout follows R, not the pause: the scope still reads
		// as partitioned for the namespace choice, while nothing runs.
		if spec, ok := w.b.Reg.PartitionedScope("apps/fresh"); !ok || spec != bothSpec {
			t.Errorf("a pending scope with R = user + global: %v %v, want partitioned", spec, ok)
		}
		if c, _ := w.b.Reg.Component("apps/fresh"); func() bool { _, ok := c.Partitioned(); return ok }() {
			t.Error("a pending tile runs as partitioned")
		}
		since := req.Since
		w.rescan() // the same request stays, with its time
		if _, _, req := w.state("apps/fresh"); req == nil || !req.Since.Equal(since) || w.ops("apps/fresh") != "auto,request" {
			t.Errorf("a rescan reopened the request: %+v %q", req, w.ops("apps/fresh"))
		}
	})

	t.Run("pending on a vault key", func(t *testing.T) {
		w.write(map[string]string{"apps/vault/xbin.json": `{"runtime":"go"}`})
		w.rescan()
		if err := w.b.vaultWrite("apps/vault", map[string]string{"token": "x"}); err != nil {
			t.Fatal(err)
		}
		w.write(map[string]string{"apps/vault/xbin.json": `{"runtime":"go","partition":["user"]}`})
		w.rescan()
		if st, r, req := w.state("apps/vault"); st != registry.PartitionPending || !r.IsZero() || req == nil || *req.Spec != userSpec {
			t.Errorf("apps/vault: %v %v %+v, want pending unpartitioned → user", st, r, req)
		}
	})

	t.Run("pending on a cron row", func(t *testing.T) {
		w.write(map[string]string{"apps/cron/xbin.json": `{"runtime":"go"}`})
		w.rescan()
		if err := w.b.cron.add(cronJob{Name: "tick", Schedule: "@every 1h", Component: "apps/cron", Path: "/tick"}); err != nil {
			t.Fatal(err)
		}
		w.write(map[string]string{"apps/cron/xbin.json": `{"runtime":"go","partition":["user"]}`})
		w.rescan()
		if st, _, _ := w.state("apps/cron"); st != registry.PartitionPending {
			t.Errorf("apps/cron: %v, want pending", st)
		}
		// Its ticks are missed and its bus deliveries dropped, quietly: the
		// paused primary is never dispatched to (01 §2.3).
		dispatched := 0
		w.b.cron.mu.Lock()
		w.b.cron.dispatch = func(auth.Principal, string, string) (int, string) { dispatched++; return 200, "" }
		w.b.cron.mu.Unlock()
		w.b.cron.fire(cronJob{Name: "tick", Schedule: "@every 1h", Component: "apps/cron", Path: "/tick"})
		busSent := func(context.Context, auth.Principal, string, string, []byte) (int, string) {
			dispatched++
			return 200, ""
		}
		if out, _ := w.b.bus.deliver(busSent, "", busSub{Name: "s", Component: "apps/cron", Path: "/bus"}, busDelivery{}); out != "dropped" {
			t.Errorf("a bus delivery to a paused primary: %q, want dropped", out)
		}
		if dispatched != 0 {
			t.Errorf("a paused primary got %d deliveries", dispatched)
		}
	})

	t.Run("declined runs R, a new Q' reopens", func(t *testing.T) {
		if err := w.b.recordDecision("apps/vault", modeOpKeep, registry.PartitionSpec{}, userSpec, "alice", nil); err != nil {
			t.Fatal(err)
		}
		w.rescan()
		st, r, req := w.state("apps/vault")
		if st != registry.PartitionUnpartitioned || !r.IsZero() || req == nil || !req.Declined || *req.Spec != userSpec {
			t.Errorf("declined: %v %v %+v, want unpartitioned running, request user declined", st, r, req)
		}
		if why := w.b.PartitionHoldReason("apps/vault"); why != "" {
			t.Errorf("a declined tile is held: %q", why)
		}
		if rec := w.record("apps/vault"); rec.Declined == nil || rec.Declined.By != "alice" || rec.Request != nil {
			t.Errorf("record %+v", rec)
		}
		// A stale decision is refused.
		if err := w.b.recordDecision("apps/vault", modeOpKeep, registry.PartitionSpec{}, userSpec, "bob", nil); !errors.Is(err, errModeStale) {
			t.Errorf("a decision with no open request: %v", err)
		}
		w.write(map[string]string{"apps/vault/xbin.json": `{"runtime":"go","partition":["user","global"]}`})
		w.rescan()
		if st, _, req := w.state("apps/vault"); st != registry.PartitionPending || req == nil || req.Declined || *req.Spec != bothSpec {
			t.Errorf("a new request after a decline: %v %+v, want pending → user + global", st, req)
		}
		if got := w.ops("apps/vault"); got != "request,keep,request" {
			t.Errorf("history %q", got)
		}
		if rec := w.record("apps/vault"); rec.Declined != nil {
			t.Error("the decline of another request survived a new one")
		}
	})

	t.Run("code back at R is withdrawn", func(t *testing.T) {
		w.write(map[string]string{"apps/vault/xbin.json": `{"runtime":"go"}`})
		w.rescan()
		if st, _, req := w.state("apps/vault"); st != registry.PartitionUnpartitioned || req != nil {
			t.Errorf("withdrawn: %v %+v", st, req)
		}
		if got := w.ops("apps/vault"); got != "request,keep,request,withdrawn" {
			t.Errorf("history %q", got)
		}
		if why := w.b.PartitionHoldReason("apps/vault"); why != "" {
			t.Errorf("a withdrawn tile is still held: %q", why)
		}
	})

	t.Run("switch records R := Q", func(t *testing.T) {
		if err := w.b.recordDecision("apps/cron", modeOpSwitch, registry.PartitionSpec{}, userSpec, "alice", map[string]int64{"registrations": 1}); err != nil {
			t.Fatal(err)
		}
		w.rescan()
		if st, r, req := w.state("apps/cron"); st != registry.PartitionPartitioned || r != userSpec || req != nil {
			t.Errorf("after a switch: %v %v %+v", st, r, req)
		}
	})

	t.Run("an unreadable code holds a recorded tile", func(t *testing.T) {
		// A syntax error in a live edit, then a pinned checkpoint that isn't
		// prepared: Q is unknown, never "absent" — the tile waits in R,
		// invalid, and nothing is requested, withdrawn or recorded.
		before := w.ops("apps/cron")
		for _, broken := range []func(){
			func() { w.write(map[string]string{"apps/cron/xbin.json": `{"runtime":"go","partition":["user"]`}) },
			func() {
				w.write(map[string]string{"apps/cron/xbin.json": `{"runtime":"go","partition":["user"]}`})
				pinned["apps/cron"] = &registry.PinnedCode{ManifestErr: "apps/cron: the primary (main) is pinned to checkpoint 0123, which isn't prepared"}
			},
		} {
			broken()
			w.rescan()
			st, r, req := w.state("apps/cron")
			if st != registry.PartitionInvalid || r != userSpec || req != nil {
				t.Errorf("unreadable code: %v %v %+v, want invalid in R = user", st, r, req)
			}
			if why := w.b.PartitionHoldReason("apps/cron"); !strings.Contains(why, "can't be read") {
				t.Errorf("hold reason %q", why)
			}
			if c, _ := w.b.Reg.Component("apps/cron"); !strings.Contains(c.PartitionErr, "request can't be read") {
				t.Errorf("partition error %q", c.PartitionErr)
			}
			delete(pinned, "apps/cron")
			w.write(map[string]string{"apps/cron/xbin.json": `{"runtime":"go","partition":["user"]}`})
			w.rescan()
			if st, _, _ := w.state("apps/cron"); st != registry.PartitionPartitioned || w.ops("apps/cron") != before {
				t.Errorf("readable again: %v, history %q, want partitioned, %q", st, w.ops("apps/cron"), before)
			}
		}
		// A tile with no record whose code can't be read is the zero state.
		w.write(map[string]string{"apps/plain/xbin.json": `{"runtime":"go",`})
		w.rescan()
		if c, _ := w.b.Reg.Component("apps/plain"); c.PartitionShown() || w.record("apps/plain") != nil || w.b.PartitionHoldReason("apps/plain") != "" {
			t.Errorf("an unrecorded tile with a broken manifest carries partition state: %q", c.PartitionErr)
		}
		w.write(map[string]string{"apps/plain/xbin.json": `{"runtime":"go"}`})
		w.rescan()
	})

	t.Run("invalid records nothing", func(t *testing.T) {
		w.write(map[string]string{"apps/bad/xbin.json": `{"runtime":"go","partition":["org"]}`})
		w.rescan()
		if st, _, _ := w.state("apps/bad"); st != registry.PartitionInvalid {
			t.Errorf("apps/bad: %v", st)
		}
		if w.record("apps/bad") != nil {
			t.Error("an invalid request was recorded")
		}
		if why := w.b.PartitionHoldReason("apps/bad"); !strings.Contains(why, `unknown word "org"`) {
			t.Errorf("hold reason %q", why)
		}
		// Invalid on a recorded tile keeps R and changes nothing on disk.
		before := w.record("apps/cron")
		w.write(map[string]string{"apps/cron/xbin.json": `{"runtime":"go","partition":"user"}`})
		w.rescan()
		if st, r, _ := w.state("apps/cron"); st != registry.PartitionInvalid || r != userSpec {
			t.Errorf("apps/cron invalid: %v %v", st, r)
		}
		if after := w.record("apps/cron"); len(after.History) != len(before.History) {
			t.Error("an invalid request wrote history")
		}
	})

	t.Run("a reload keeps the store", func(t *testing.T) {
		pm := newPartitionModes(w.root)
		pm.load()
		if rec := pm.recs["apps/vault"]; rec == nil || rec.recorded() != (registry.PartitionSpec{}) || len(rec.History) != 4 {
			t.Errorf("reloaded apps/vault: %+v", rec)
		}
		if rec := pm.recs["apps/fresh"]; rec == nil || rec.recorded() != bothSpec || rec.Request == nil {
			t.Errorf("reloaded apps/fresh: %+v", rec)
		}
	})
}

// covers D119e PD-44 — a D127 rollback of a partitioned primary to a
// checkpoint from before partitions is a request, never a silent switch:
// the tile holds data, so it pauses (pending) until a manager decides. On an
// empty tile the same rollback records the checkpoint's mode (auto).
func TestPinnedPrimaryRollback(t *testing.T) {
	cp := t.TempDir()
	if err := os.WriteFile(filepath.Join(cp, "xbin.json"), []byte(`{"runtime":"go","uses":[{"target":"res:apps/p/db","role":"writer"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old, err := registry.ReadCheckpoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]*registry.PinnedCode{}
	hook := func(rel string) (*registry.PinnedCode, bool) { pc, ok := pinned[rel]; return pc, ok }
	tile := func(p string) map[string]string {
		return map[string]string{
			p + "/scope.json": `{"resources":{"db":{"type":"kv"}}}`,
			p + "/xbin.json":  `{"runtime":"go","partition":["user"],"uses":[{"target":"res:` + p + `/db","role":"writer"}]}`,
		}
	}
	files := tile("apps/p")
	for k, v := range tile("apps/e") {
		files[k] = v
	}
	w := newPartWS(t, files, hook)
	for _, p := range []string{"apps/p", "apps/e"} {
		if st, r, _ := w.state(p); st != registry.PartitionPartitioned || r != userSpec {
			t.Fatalf("%s before the rollback: %v %v", p, st, r)
		}
	}
	w.kvPut("apps/p", "db")
	pinned["apps/p"], pinned["apps/e"] = old, old // the rollback pins both primaries to the old code
	w.rescan()
	c, _ := w.b.Reg.Component("apps/p")
	if c.WorkTreeManifest().Partition == nil || c.Manifest.Partition != nil {
		t.Fatalf("the pinned primary's request isn't its checkpoint's: %v / work tree %v", c.Manifest.Partition, c.WorkTreeManifest().Partition)
	}
	if st, r, req := w.state("apps/p"); st != registry.PartitionPending || r != userSpec || req == nil || req.Spec != nil {
		t.Errorf("rolled back with data: %v %v %+v, want pending user → unpartitioned", st, r, req)
	}
	if rec := w.record("apps/p"); rec.recorded() != userSpec {
		t.Errorf("the rollback changed the recorded mode: %+v", rec)
	}
	if st, r, _ := w.state("apps/e"); st != registry.PartitionUnpartitioned || !r.IsZero() {
		t.Errorf("rolled back empty: %v %v, want auto to unpartitioned", st, r)
	}
	// Rolling forward again withdraws the request.
	delete(pinned, "apps/p")
	w.rescan()
	if st, _, req := w.state("apps/p"); st != registry.PartitionPartitioned || req != nil || w.ops("apps/p") != "auto,request,withdrawn" {
		t.Errorf("rolled forward: %v %+v %q", st, req, w.ops("apps/p"))
	}
}

// PD-28 — approving governance, cap:sandboxes, cap:net-admin or
// cap:containers for a partitioned tile (or one that asks to be) is 409;
// other grants, and every grant to an unpartitioned tile, pass as today.
func TestPartitionGrantRefusals(t *testing.T) {
	w := newPartWS(t, map[string]string{
		"apps/p/xbin.json": `{"runtime":"go","partition":["user"]}`,
		"apps/u/xbin.json": `{"runtime":"go"}`,
	}, nil)
	post := func(from, target string) int {
		body, _ := json.Marshal(registry.Grant{From: from, Target: target, Role: "writer"})
		req := httptest.NewRequest(http.MethodPost, "/api/xbin/grants", strings.NewReader(string(body)))
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Owner: true}))
		rec := httptest.NewRecorder()
		w.b.grantMutation(rec, req, func(ws *registry.WorkspaceManifest, g registry.Grant) { ws.Grants = append(ws.Grants, g) })
		return rec.Code
	}
	for _, target := range []string{"xbin", "xbin:users", "cap:sandboxes", "cap:net-admin", "cap:containers"} {
		if code := post("apps/p", target); code != http.StatusConflict {
			t.Errorf("%s for a partitioned tile: %d, want 409", target, code)
		}
		if code := post("apps/u", target); code != http.StatusOK {
			t.Errorf("%s for an unpartitioned tile: %d, want 200", target, code)
		}
	}
	if code := post("apps/p", "apps/u"); code != http.StatusOK {
		t.Errorf("a call grant for a partitioned tile: %d", code)
	}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// PartitionHoldReason, which every proxied call and every spawn asks, never
// waits for a settle, whose "holds data" opens kv files and decrypts vaults.
func TestPartitionHoldReasonNeverWaits(t *testing.T) {
	w := newPartWS(t, map[string]string{"apps/p/xbin.json": `{"runtime":"go","partition":["user"]}`}, nil)
	w.b.parts.settleMu.Lock()
	defer w.b.parts.settleMu.Unlock()
	done := make(chan string, 1)
	go func() { done <- w.b.PartitionHoldReason("apps/p") }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PartitionHoldReason waited for a settle")
	}
}
