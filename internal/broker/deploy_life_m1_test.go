package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// lifeTree is the checkpoint a pinned primary runs in these tests.
const lifeTree = "3f2a1c9e0b7d4a6f8e2c1b0a9d8e7f6a5b4c3d2e"

// pinRecord writes tile's deployment record as pausing live reload commits
// it, made for owner ref owner: main pinned to lifeTree, live reload paused.
func pinRecord(t *testing.T, root, tile, owner string) {
	t.Helper()
	doc := map[string]any{
		"schema": 1, "tile": tile, "owner": owner, "created": "2026-09-27T10:12:03Z", "seq": 3,
		"liveReload": "", "lastLiveReload": "main", "primary": "main", "protectedPrimary": false,
		"nextDeploy": 4,
		"deployments": map[string]any{
			"main": map[string]any{"checkpoint": lifeTree, "created": "2026-09-27T10:12:03Z", "by": "user:ana"},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	p := lifeRecordPath(root, tile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func lifeRecordPath(root, tile string) string {
	return filepath.Join(root, "data", "deployments", util.TileKey(tile)+".json")
}

// lifeRepo makes one of tile's repositories under data/checkpoints: its
// checkpoint store (".git") or its view repository (".view.git").
func lifeRepo(t *testing.T, root, tile, suffix string) string {
	t.Helper()
	dir := filepath.Join(root, "data", "checkpoints", util.TileKey(tile)+suffix)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// lifePlane boots the deployments plane over b's workspace, with the owner
// store answering owner refs, and installs its tile-life hooks into the
// broker as boot does.
func lifePlane(t *testing.T, b *Broker, st *users.Store) *deployments.Plane {
	t.Helper()
	dp := &deployments.Plane{Root: b.Reg.Root, OwnerRef: st.Owner}
	if err := dp.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	b.DeploymentHooks = DeploymentHooks{
		RewriteDeploymentOwner: dp.RewriteDeploymentOwner,
		ResetDeploymentState:   dp.ResetDeploymentState,
		DeploymentLeftovers:    dp.DeploymentLeftovers,
	}
	return dp
}

// pinnedWhy is "" while tile's primary is pinned to lifeTree, and otherwise
// says what runs instead.
func pinnedWhy(dp *deployments.Plane, tile string) string {
	if f := dp.Lookup(tile); f.State != deployments.RecordActive {
		return fmt.Sprintf("record state %d (%v)", f.State, f.Err)
	}
	if code, err := dp.CodeFor(tile, util.MainDeployment); err != nil || code != (runner.Code{Tree: lifeTree}) {
		return fmt.Sprintf("main runs %+v (%v)", code, err)
	}
	if _, ok := dp.PinnedPrimary(tile); !ok {
		return "the registry isn't told of a pinned primary"
	}
	return ""
}

// recordOwner reads the owner ref and seq in tile's record file.
func recordOwner(t *testing.T, root, tile string) (string, int64) {
	t.Helper()
	data, err := os.ReadFile(lifeRecordPath(root, tile))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Owner string `json:"owner"`
		Seq   int64  `json:"seq"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r.Owner, r.Seq
}

func lifeAbsent(t *testing.T, what, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is still there (%v)", what, err)
	}
}

// covers P29 PO-7 — the M1 half of the transfer row: a transfer (D39) moves
// the tile's deployment record with it, rewriting the record's owner ref in
// the same step, before the restart the transfer makes: at that restart and
// after it the primary is still pinned to its checkpoint, the owner store
// already reports the new owner, and the record on disk names that owner
// with the next seq, so a pinned primary stays pinned across an xbind
// restart too. The owner ref written is the store's own (a user id
// normalized). A transfer SetOwner refuses answers as it does today and
// leaves the record with the owner that stays; a record that can't be
// rewritten stops the transfer before anything moves. Without the seam —
// the owner store moving alone, as an older binary's transfer did — the
// record would be inert and the tile would run its work tree. A tile
// without a record transfers exactly as before, and a workspace where no
// tile opted in gains no deployment state from a transfer.
func TestDeploymentsAcrossTileLife(t *testing.T) {
	b, st := orgFixture(t) // apps/email owned by org:sales, carol its admin
	root := b.Reg.Root
	pinRecord(t, root, "apps/email", "org:sales")
	dp := lifePlane(t, b, st)
	if why := pinnedWhy(dp, "apps/email"); why != "" {
		t.Fatalf("fixture: apps/email isn't pinned: %s", why)
	}
	type restart struct{ owner, why string }
	var restarts []restart
	b.OnGrantChange = func(comp string) {
		if comp == "apps/email" {
			restarts = append(restarts, restart{st.Owner(comp), pinnedWhy(dp, comp)})
		}
	}
	transfer := func(p auth.Principal, to string) *httptest.ResponseRecorder {
		t.Helper()
		restarts = nil
		return call(t, b.apiOwnerTransfer, p, "POST", "/owner", `{"tile":"apps/email","to":"`+to+`"}`, nil)
	}
	rootP := auth.Principal{Owner: true}

	seq := int64(3)
	for _, tc := range []struct {
		name string
		p    auth.Principal
		to   string
		want string // the owner ref the store and the record end with
	}{
		{"the owning org's admin takes it", principalFor(t, st, "carol"), "user:carol", "user:carol"},
		{"an admin gives it to another user (id normalized)", rootP, "user:Dave", "user:dave"},
		{"to workspace-owned", rootP, "", ""},
		{"back to the org", rootP, "org:sales", "org:sales"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := transfer(tc.p, tc.to)
			if w.Code != 200 {
				t.Fatalf("transfer: %d %s", w.Code, w.Body.String())
			}
			seq++
			if got := st.Owner("apps/email"); got != tc.want {
				t.Fatalf("owner = %q, want %q", got, tc.want)
			}
			if len(restarts) != 1 || restarts[0] != (restart{tc.want, ""}) {
				t.Errorf("restarts = %+v; want one, with owner %q and main still pinned", restarts, tc.want)
			}
			if why := pinnedWhy(dp, "apps/email"); why != "" {
				t.Errorf("after the transfer: %s", why)
			}
			if owner, got := recordOwner(t, root, "apps/email"); owner != tc.want || got != seq {
				t.Errorf("record on disk: owner %q seq %d, want %q seq %d", owner, got, tc.want, seq)
			}
			// xbind restarted: the record read back still binds.
			dp = lifePlane(t, b, st)
			if why := pinnedWhy(dp, "apps/email"); why != "" {
				t.Errorf("after a restart: %s", why)
			}
		})
	}

	t.Run("a transfer SetOwner refuses", func(t *testing.T) {
		w := transfer(rootP, "user:nosuch")
		if w.Code != 400 || !strings.Contains(w.Body.String(), `no such user \"nosuch\"`) {
			t.Fatalf("transfer: %d %s; want today's 400", w.Code, w.Body.String())
		}
		if got := st.Owner("apps/email"); got != "org:sales" {
			t.Errorf("owner = %q, want org:sales", got)
		}
		if owner, _ := recordOwner(t, root, "apps/email"); owner != "org:sales" {
			t.Errorf("record owner %q, want org:sales back", owner)
		}
		if why := pinnedWhy(dp, "apps/email"); why != "" || len(restarts) != 0 {
			t.Errorf("after a refused transfer: %s; restarts %+v", why, restarts)
		}
		dp = lifePlane(t, b, st) // xbind restarted
		if why := pinnedWhy(dp, "apps/email"); why != "" {
			t.Errorf("after a restart: %s", why)
		}
	})

	t.Run("a record that can't be rewritten stops the transfer", func(t *testing.T) {
		dir := filepath.Dir(lifeRecordPath(root, "apps/email"))
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if f, err := os.CreateTemp(dir, "probe"); err == nil { // root ignores the mode
			f.Close()
			_ = os.Remove(f.Name())
			t.Skip("the records directory stays writable to this user; can't make the rewrite fail")
		}
		w := transfer(rootP, "user:carol")
		if w.Code != 500 || !strings.Contains(w.Body.String(), "deployment record can't follow the transfer, so nothing moved") {
			t.Fatalf("transfer: %d %s; want 500", w.Code, w.Body.String())
		}
		if got := st.Owner("apps/email"); got != "org:sales" {
			t.Errorf("owner = %q: the transfer moved", got)
		}
		if why := pinnedWhy(dp, "apps/email"); why != "" || len(restarts) != 0 {
			t.Errorf("after a stopped transfer: %s; restarts %+v", why, restarts)
		}
	})

	t.Run("the owner store moving alone leaves the record inert", func(t *testing.T) {
		if err := st.SetOwner("apps/email", "user:bob"); err != nil {
			t.Fatal(err)
		}
		if f := dp.Lookup("apps/email"); f.State != deployments.RecordInert {
			t.Errorf("record state %d, want inert: the rewrite must precede the owner store", f.State)
		}
		if code, err := dp.CodeFor("apps/email", util.MainDeployment); err != nil || !code.WorkTree {
			t.Errorf("main runs %+v (%v), want the work tree", code, err)
		}
	})

	t.Run("a tile without a record", func(t *testing.T) {
		w := call(t, b.apiOwnerTransfer, rootP, "POST", "/owner", `{"tile":"apps/calendar","to":"user:bob"}`, nil)
		if w.Code != 200 || st.Owner("apps/calendar") != "user:bob" {
			t.Fatalf("transfer: %d %s, owner %q", w.Code, w.Body.String(), st.Owner("apps/calendar"))
		}
		if f := dp.Lookup("apps/calendar"); f.State != deployments.RecordNone {
			t.Errorf("record state %d, want none", f.State)
		}
		lifeAbsent(t, "a record for apps/calendar", lifeRecordPath(root, "apps/calendar"))
	})

	t.Run("a workspace where no tile opted in", func(t *testing.T) {
		b, st := orgFixture(t)
		lifePlane(t, b, st)
		w := call(t, b.apiOwnerTransfer, rootP, "POST", "/owner", `{"tile":"apps/email","to":"user:bob"}`, nil)
		if w.Code != 200 || st.Owner("apps/email") != "user:bob" {
			t.Fatalf("transfer: %d %s", w.Code, w.Body.String())
		}
		lifeAbsent(t, "data/deployments", filepath.Join(b.Reg.Root, "data", "deployments"))
		lifeAbsent(t, "data/checkpoints", filepath.Join(b.Reg.Root, "data", "checkpoints"))
	})
}

// covers P29 T11 PO-7 — the M1 half of the leftovers row: after a tile's
// removal its deployment record and checkpoint store are leftovers on
// D82's refusal list (pathLeftovers, newTilePathOK), each on its own and
// named, so a non-owner can't create a tile at the path; the path's owner
// is exempt, as today. A tile re-created at the path starts in the zero
// state, whoever creates it (its owner, or an admin while the old owner
// entry stays): the creation drops the record and the view repository
// before it assigns the owner (through the create API, before the tile
// registers, so the registry composes it from its work tree), and keeps the
// checkpoint store, still a leftover. A creation where no tile opted in
// writes no deployment state.
func TestPathLeftoversIncludeDeploymentState(t *testing.T) {
	b, st := orgFixture(t)
	root := b.Reg.Root
	// Removed tiles: apps/gone was bob's (his owner entry stays); apps/wsgone
	// was workspace-owned; apps/storeonly left its checkpoint store alone;
	// apps/admingone was bob's too.
	for _, tile := range []string{"apps/gone", "apps/admingone"} {
		if err := st.SetOwner(tile, "user:bob"); err != nil {
			t.Fatal(err)
		}
		pinRecord(t, root, tile, "user:bob")
		lifeRepo(t, root, tile, ".git")
		lifeRepo(t, root, tile, ".view.git")
	}
	pinRecord(t, root, "apps/wsgone", "")
	lifeRepo(t, root, "apps/wsgone", ".git")
	lifeRepo(t, root, "apps/storeonly", ".git")
	dp := lifePlane(t, b, st)
	b.Reg.PinnedPrimary = dp.PinnedPrimary
	bob, dave := principalFor(t, st, "bob"), principalFor(t, st, "dave")

	t.Run("leftovers refuse a non-owner", func(t *testing.T) {
		for tile, want := range map[string][]string{
			"apps/wsgone":    {"checkpoint store", "deployment record"},
			"apps/storeonly": {"checkpoint store"},
			"apps/gone":      {"checkpoint store", "deployment record", "owner entry user:bob"},
		} {
			if got := b.pathLeftovers(tile, "user:dave"); !reflect.DeepEqual(got, want) {
				t.Errorf("pathLeftovers(%s) = %q, want %q", tile, got, want)
			}
			ok, msg := b.newTilePathOK(tile, "user:dave")
			if ok || !strings.Contains(msg, "still carries state from a removed tile ("+strings.Join(want, "; ")+")") {
				t.Errorf("newTilePathOK(%s) = %v %q", tile, ok, msg)
			}
		}
		w := call(t, b.apiCreate, dave, "POST", "/create", `{"path":"apps/wsgone"}`, nil)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "deployment record") {
			t.Fatalf("dave's create: %d %s", w.Code, w.Body.String())
		}
		if _, err := os.Lstat(lifeRecordPath(root, "apps/wsgone")); err != nil {
			t.Errorf("a refused creation touched the record: %v", err)
		}
		if got := b.pathLeftovers("apps/untouched", "user:dave"); got != nil {
			t.Errorf("pathLeftovers(apps/untouched) = %q, want none", got)
		}
	})

	t.Run("the path's owner is exempt", func(t *testing.T) {
		if got := b.pathLeftovers("apps/gone", "user:bob"); got != nil {
			t.Errorf("pathLeftovers for the owner = %q, want none", got)
		}
		if ok, msg := b.newTilePathOK("apps/gone", "user:bob"); !ok {
			t.Errorf("newTilePathOK for the owner: %q", msg)
		}
	})

	zeroState := func(t *testing.T, tile string) {
		t.Helper()
		if f := dp.Lookup(tile); f.State != deployments.RecordNone {
			t.Errorf("record state %d (%v), want none", f.State, f.Err)
		}
		if code, err := dp.CodeFor(tile, util.MainDeployment); err != nil || code != (runner.Code{WorkTree: true}) {
			t.Errorf("main runs %+v (%v), want the work tree", code, err)
		}
		if _, ok := dp.PinnedPrimary(tile); ok {
			t.Error("a pinned primary survived the re-creation")
		}
		lifeAbsent(t, "the record", lifeRecordPath(root, tile))
		lifeAbsent(t, "the view repository", filepath.Join(root, "data", "checkpoints", util.TileKey(tile)+".view.git"))
		if got := dp.DeploymentLeftovers(tile); !reflect.DeepEqual(got, []string{"checkpoint store"}) {
			t.Errorf("DeploymentLeftovers = %q, want the checkpoint store kept", got)
		}
		c, ok := b.Reg.Component(tile)
		if !ok || c.WorkTree != nil || c.Kept || c.ManifestErr != "" {
			t.Errorf("registered component %+v (%v), want the work tree's", c, ok)
		}
	}

	t.Run("the owner re-creates it", func(t *testing.T) {
		w := call(t, b.apiCreate, bob, "POST", "/create", `{"path":"apps/gone"}`, nil)
		if w.Code != 200 {
			t.Fatalf("bob's create: %d %s", w.Code, w.Body.String())
		}
		if got := st.Owner("apps/gone"); got != "user:bob" {
			t.Errorf("owner = %q", got)
		}
		zeroState(t, "apps/gone")
	})

	t.Run("an admin re-creates it, the old owner entry kept", func(t *testing.T) {
		w := call(t, b.apiCreate, auth.Principal{Owner: true}, "POST", "/create", `{"path":"apps/admingone"}`, nil)
		if w.Code != 200 {
			t.Fatalf("admin create: %d %s", w.Code, w.Body.String())
		}
		if got := st.Owner("apps/admingone"); got != "user:bob" {
			t.Fatalf("owner = %q: the fixture wants the old entry kept, which the old record matches", got)
		}
		zeroState(t, "apps/admingone")
	})

	t.Run("a creation where no tile opted in", func(t *testing.T) {
		b, st := orgFixture(t)
		lifePlane(t, b, st)
		w := call(t, b.apiCreate, principalFor(t, st, "bob"), "POST", "/create", `{"path":"apps/fresh"}`, nil)
		if w.Code != 200 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		lifeAbsent(t, "data/deployments", filepath.Join(b.Reg.Root, "data", "deployments"))
		lifeAbsent(t, "data/checkpoints", filepath.Join(b.Reg.Root, "data", "checkpoints"))
	})
}
