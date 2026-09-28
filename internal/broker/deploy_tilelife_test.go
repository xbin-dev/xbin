package broker

// The M2 half of TestDeploymentsAcrossTileLife (05-model §11): a tile's
// deployments beyond main through its transfer, its lifecycle, and the
// creation paths that copy it. WP-23b's deploy_life_m1_test.go holds the M1
// half (a pinned primary stays pinned across a transfer); the runtime half,
// what the runner does with the StopBackend, WakeBackends and OnGrantChange
// calls asserted here, is internal/runner's
// TestDeploymentsAcrossTileLifeRuntime.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// lifeTree2 is a second checkpoint the fixture's records name.
const lifeTree2 = "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d"

// emailLifeRecord is apps/email's record, made for owner: main the primary,
// pinned to lifeTree; live reload on dev, with its deliveries and alwaysOn
// switches on and a lowered limit; qa pinned to lifeTree2 after a failed
// move; two edge overrides; and a field and a deployment field only a newer
// xbind knows.
func emailLifeRecord(owner string) map[string]any {
	return map[string]any{
		"schema": 1, "tile": "apps/email", "owner": owner, "created": "2026-09-27T10:12:03Z", "seq": 7,
		"liveReload": "dev", "lastLiveReload": "dev", "primary": "main", "protectedPrimary": false,
		"edges":      map[string]any{"slot:llm": "block", "grant:apps/calendar": "read"},
		"nextDeploy": 9,
		"deployments": map[string]any{
			"main": map[string]any{"checkpoint": lifeTree, "created": "2026-09-27T10:12:03Z", "by": "user:carol"},
			"dev": map[string]any{"checkpoint": nil, "deliveries": true, "alwaysOn": true, "limits": map[string]any{"memMiB": 512},
				"created": "2026-09-27T10:13:00Z", "by": "user:carol", "futureDeploymentField": "kept"},
			"qa": map[string]any{"checkpoint": lifeTree2, "state": "failed", "created": "2026-09-27T10:14:00Z", "by": "user:carol"},
		},
		"futureField": map[string]any{"kept": true},
	}
}

// notesLifeRecord is apps/notes's record, workspace-owned: live reload
// paused, dev the primary (reassigned), main beside it, both pinned.
func notesLifeRecord() map[string]any {
	return map[string]any{
		"schema": 1, "tile": "apps/notes", "owner": "", "created": "2026-09-27T10:12:03Z", "seq": 4,
		"liveReload": "", "lastLiveReload": "main", "primary": "dev", "protectedPrimary": false, "nextDeploy": 3,
		"deployments": map[string]any{
			"main": map[string]any{"checkpoint": lifeTree2, "created": "2026-09-27T10:12:03Z", "by": "user:root2"},
			"dev":  map[string]any{"checkpoint": lifeTree, "created": "2026-09-27T10:12:03Z", "by": "user:root2"},
		},
	}
}

// boardLifeRecord is templates/board's record: a static template whose live
// reload is paused, main pinned.
func boardLifeRecord() map[string]any {
	return map[string]any{
		"schema": 1, "tile": "templates/board", "owner": "", "created": "2026-09-27T10:12:03Z", "seq": 2,
		"liveReload": "", "lastLiveReload": "main", "primary": "main", "protectedPrimary": false, "nextDeploy": 2,
		"deployments": map[string]any{
			"main": map[string]any{"checkpoint": lifeTree, "created": "2026-09-27T10:12:03Z", "by": "user:root2"},
		},
	}
}

func writeLifeRecord(t *testing.T, root, tile string, doc map[string]any) {
	t.Helper()
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

// readLifeRecord decodes tile's record file as a generic object.
func readLifeRecord(t *testing.T, root, tile string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(lifeRecordPath(root, tile))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// lifeArchiver stands in for the archiver tile behind the proxy: it keeps
// each archive a PUT sends and serves it back as the latest version.
type lifeArchiver struct {
	mu       sync.Mutex
	archives map[string][]byte
}

func (a *lifeArchiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key, latest := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/apps/archiver/archive/"), "/versions/latest")
	switch {
	case r.Method == "PUT" && !latest:
		body, _ := io.ReadAll(r.Body)
		a.archives[key] = body
		_, _ = w.Write([]byte(`{"version":"v1"}`))
	case r.Method == "GET" && latest && a.archives[key] != nil:
		_, _ = w.Write(a.archives[key])
	default:
		http.Error(w, "no such archive", http.StatusNotFound)
	}
}

// lifeFx is the fixture: orgFixture's broker (apps/email owned by org:sales,
// carol its admin; apps/calendar workspace-owned, in the zero state), with
// apps/notes and templates/board added, the records above, a real
// deployments plane over them installed as boot installs it, an archiver,
// and recorders for the runner hooks.
type lifeFx struct {
	b    *Broker
	st   *users.Store
	dp   *deployments.Plane
	root string
	evs  <-chan events.Event

	mu       sync.Mutex
	stops    []string      // StopBackend's arguments
	restarts []string      // OnGrantChange's: "tile owner=<store's owner> <the plane's view then>"
	wakes    chan struct{} // WakeBackends' calls
}

func newLifeFx(t *testing.T) *lifeFx {
	t.Helper()
	b, st := orgFixture(t)
	f := &lifeFx{b: b, st: st, root: b.Reg.Root, wakes: make(chan struct{}, 16)}
	for rel, content := range map[string]string{
		"apps/email/notes.txt":        "the work tree's own file\n",
		"apps/notes/xbin.json":        `{"runtime":"go"}`,
		"templates/board/xbin.json":   `{"template":{"title":"Board"}}`,
		"templates/board/index.html":  "<html>board</html>",
		"templates/board/columns.txt": "todo doing done\n",
	} {
		p := filepath.Join(f.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Bindings == nil {
			ws.Bindings = map[string]map[string]registry.Binding{}
		}
		ws.Bindings["*"] = map[string]registry.Binding{archiveSlot: {{Ref: "apps/archiver"}}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	writeLifeRecord(t, f.root, "apps/email", emailLifeRecord("org:sales"))
	writeLifeRecord(t, f.root, "apps/notes", notesLifeRecord())
	writeLifeRecord(t, f.root, "templates/board", boardLifeRecord())
	f.boot(t)
	if err := f.dp.WriteDeploymentFile("apps/email", "dev", "cron.json", []byte(`{"jobs":[{"name":"tick"}]}`)); err != nil {
		t.Fatal(err)
	}
	b.ProxyHandler = &lifeArchiver{archives: map[string][]byte{}}
	b.StopBackend = func(tile string) {
		f.mu.Lock()
		f.stops = append(f.stops, tile)
		f.mu.Unlock()
	}
	b.WakeBackends = func() { f.wakes <- struct{}{} }
	b.OnGrantChange = func(tile string) {
		f.mu.Lock()
		f.restarts = append(f.restarts, tile+" owner="+st.Owner(tile)+" "+f.view(tile))
		f.mu.Unlock()
	}
	ch, cancel := b.Hub.Subscribe(nil)
	t.Cleanup(cancel)
	f.evs = ch
	return f
}

// boot boots a deployments plane over the workspace, as xbind's boot does,
// and installs its hooks and answers into the broker as stepBroker does.
func (f *lifeFx) boot(t *testing.T) {
	t.Helper()
	dp := &deployments.Plane{Root: f.root, OwnerRef: f.st.Owner}
	if err := dp.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	f.dp = dp
	f.b.DeploymentHooks = DeploymentHooks{RewriteDeploymentOwner: dp.RewriteDeploymentOwner,
		ResetDeploymentState: dp.ResetDeploymentState, DeploymentLeftovers: dp.DeploymentLeftovers,
		DeploymentExists: dp.HasDeployment, AddressableDeployments: dp.Addressable}
	f.b.DeploymentAnswers = DeploymentAnswers{PrimaryOf: dp.Primary, DeploymentsOf: dp.DeploymentsOf,
		AddressedDeployment: dp.Addressed, RegistrationsActive: dp.RegistrationsActive,
		DeploymentEdges: dp.EdgePolicies, ReadDeploymentFile: dp.ReadDeploymentFile,
		WriteDeploymentFile: dp.WriteDeploymentFile, RemoveDeploymentFile: dp.RemoveDeploymentFile}
}

// view is what the broker and the runner learn of tile's deployments from
// the plane: the record's state, the primary a request by someone else
// reaches, each deployment's code and whether its registrations fire and
// route, and the edge policy.
func (f *lifeFx) view(tile string) string {
	found := f.dp.Lookup(tile)
	state := map[deployments.RecordState]string{deployments.RecordNone: "none", deployments.RecordActive: "active",
		deployments.RecordHeld: "held", deployments.RecordInert: "inert"}[found.State]
	outside, err := f.b.addressed(auth.Principal{UserID: "alice"}, tile)
	if err != nil {
		outside = "error: " + err.Error()
	}
	primary, names := f.b.deploymentsOf(tile)
	var deps []string
	for _, dep := range names {
		code, err := f.dp.CodeFor(tile, dep)
		what := "worktree"
		switch {
		case err != nil:
			what = "error: " + err.Error()
		case !code.WorkTree:
			what = code.Tree[:7]
		}
		fires, routes := f.b.registrationsActive(tile, dep)
		deps = append(deps, fmt.Sprintf("%s=%s fires=%v routes=%v", dep, what, fires, routes))
	}
	return fmt.Sprintf("%s primary=%s reached=%s [%s] edges=%v", state, primary, outside,
		strings.Join(deps, "; "), f.b.deploymentEdges(tile))
}

// takeRestarts, takeStops: the recorded calls since the last take.
func (f *lifeFx) takeRestarts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.restarts
	f.restarts = nil
	return out
}

func (f *lifeFx) takeStops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.stops
	f.stops = nil
	return out
}

// woke reports whether WakeBackends was called (the broker calls it in the
// background): waits up to five seconds when want, a moment otherwise.
func (f *lifeFx) woke(want bool) bool {
	wait := 50 * time.Millisecond
	if want {
		wait = 5 * time.Second
	}
	select {
	case <-f.wakes:
		return true
	case <-time.After(wait):
		return false
	}
}

// takeEvents returns the hub's events since the last take as their wire
// JSON, only those of types that speak of a tile's code or its deployments,
// and fails the test for any event of any type whose component is a
// qualified name (C2).
func (f *lifeFx) takeEvents(t *testing.T) []string {
	t.Helper()
	var out []string
	for {
		select {
		case e := <-f.evs:
			if strings.Contains(e.Component, "+") {
				t.Errorf("a %s event names the qualified component %q (C2)", e.Type, e.Component)
			}
			if e.Type != "reload" && e.Type != "deployments" && !strings.HasPrefix(e.Type, "build-") && e.Type != "status" {
				continue
			}
			data, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(data))
		default:
			return out
		}
	}
}

func bareReload(tile string) string {
	return `{"type":"reload","component":"` + tile + `"}`
}

func opReload(tile, dep string) string {
	return `{"type":"deployments","component":"` + tile + `","data":{"op":"reload","deployment":"` + dep + `"}}`
}

// covers P29 P5 C2 PO-15 — the M2 half of the tile-life row (05-model §11):
//   - a transfer (D39) moves every deployment with the tile: the record
//     keeps every deployment, its code, its switches and limits, the edge
//     policy and the fields only a newer xbind knows, and changes its owner
//     ref and seq alone; a deployment's registration files stay; the
//     primary stays whatever it was (main, or a reassigned dev) and is what
//     everyone else reaches; the restart (OnGrantChange, which boot wires to
//     the runner's ChangedTile, restarting every running deployment) comes
//     once, after the owner ref moved, with every deployment still in place;
//     and xbind restarting finds the same;
//   - lifecycle is the tile's: disabling, hiding and offloading stop the
//     tile (StopBackend, the runner's Stop of every deployment), enabling
//     (restoring an offloaded tile included) wakes the primary (WakeBackends),
//     and each change publishes today's bare reload plus op reload naming
//     each deployment that isn't the primary — main too, when dev is — and no
//     event names a qualified component; lifecycle never writes the record;
//     a tile without a record publishes exactly today's one reload; a
//     qualified name has no lifecycle of its own (404, nothing stopped, no
//     manifest row);
//   - clone and template instantiation copy the work tree only: the new
//     tile starts in the zero state, and its source keeps its record.
func TestDeploymentsAcrossTileLifeM2(t *testing.T) {
	f := newLifeFx(t)
	b, st, root := f.b, f.st, f.root
	rootP := auth.Principal{Owner: true}
	emailView := "active primary=main reached=main [main=3f2a1c9 fires=true routes=true; dev=worktree fires=true routes=false; " +
		"qa=9e8d7c6 fires=false routes=false] edges=map[grant:apps/calendar:read slot:llm:block]"
	notesView := "active primary=dev reached=dev [main=9e8d7c6 fires=false routes=false; dev=3f2a1c9 fires=true routes=true] edges=map[]"
	if got := f.view("apps/email"); got != emailView {
		t.Fatalf("fixture: apps/email is\n  %s\nwant\n  %s", got, emailView)
	}
	if got := f.view("apps/notes"); got != notesView {
		t.Fatalf("fixture: apps/notes is\n  %s\nwant\n  %s", got, notesView)
	}
	cronFile := filepath.Join(root, "data", "deployments", util.TileKey("apps/email"), "dev", "cron.json")
	cronBytes, err := os.ReadFile(cronFile)
	if err != nil {
		t.Fatal(err)
	}

	transfer := func(t *testing.T, p auth.Principal, tile, to, wantView string) {
		t.Helper()
		before := readLifeRecord(t, root, tile)
		f.takeRestarts()
		w := call(t, b.apiOwnerTransfer, p, "POST", "/owner", `{"tile":"`+tile+`","to":"`+to+`"}`, nil)
		if w.Code != 200 {
			t.Fatalf("transfer: %d %s", w.Code, w.Body.String())
		}
		if got, want := f.takeRestarts(), []string{tile + " owner=" + to + " " + wantView}; !reflect.DeepEqual(got, want) {
			t.Errorf("restarts:\n got %q\nwant %q", got, want)
		}
		after := readLifeRecord(t, root, tile)
		if after["owner"] != to || after["seq"] != before["seq"].(float64)+1 {
			t.Errorf("record owner %v seq %v; want %q and seq %v", after["owner"], after["seq"], to, before["seq"].(float64)+1)
		}
		delete(before, "owner")
		delete(before, "seq")
		delete(after, "owner")
		delete(after, "seq")
		if !reflect.DeepEqual(after, before) {
			t.Errorf("the transfer changed more than the owner ref and seq:\n got %v\nwant %v", after, before)
		}
		if got := f.view(tile); got != wantView {
			t.Errorf("after the transfer:\n  %s\nwant\n  %s", got, wantView)
		}
		f.boot(t) // xbind restarted
		if got := f.view(tile); got != wantView {
			t.Errorf("after a restart:\n  %s\nwant\n  %s", got, wantView)
		}
	}

	t.Run("a transfer moves every deployment with the tile", func(t *testing.T) {
		transfer(t, principalFor(t, st, "carol"), "apps/email", "user:carol", emailView)
		if got, err := os.ReadFile(cronFile); err != nil || string(got) != string(cronBytes) {
			t.Errorf("dev's registration file: %q (%v), want %q kept", got, err, cronBytes)
		}
		transfer(t, rootP, "apps/email", "org:sales", emailView)
	})

	t.Run("a reassigned primary stays the primary", func(t *testing.T) {
		transfer(t, rootP, "apps/notes", "org:sales", notesView)
		transfer(t, rootP, "apps/notes", "", notesView)
	})

	f.takeEvents(t)
	f.takeStops()
	emailRecord, err := os.ReadFile(lifeRecordPath(root, "apps/email"))
	if err != nil {
		t.Fatal(err)
	}
	setState := func(t *testing.T, tile, state string) {
		t.Helper()
		w := call(t, b.apiLifecycleSet, principalFor(t, st, "carol"), "POST", "/lifecycle",
			`{"component":"`+tile+`","state":"`+state+`"}`, nil)
		if w.Code != 200 {
			t.Fatalf("lifecycle %s %s: %d %s", tile, state, w.Code, w.Body.String())
		}
	}

	t.Run("lifecycle reaches every deployment", func(t *testing.T) {
		for _, step := range []struct {
			state      string
			stop, wake bool
		}{
			{"disabled", true, false},
			{"enabled", false, true},
			{"hidden", true, false},
			{"enabled", false, true},
			{"offloaded", true, false},
			{"enabled", true, true}, // the restore stops the tile before it writes
		} {
			setState(t, "apps/email", step.state)
			stops := f.takeStops()
			if step.stop != (len(stops) > 0) || slices.ContainsFunc(stops, func(s string) bool { return s != "apps/email" }) {
				t.Errorf("%s: StopBackend calls %q; want the tile's: %v", step.state, stops, step.stop)
			}
			if got := f.woke(step.wake); got != step.wake {
				t.Errorf("%s: WakeBackends called %v, want %v", step.state, got, step.wake)
			}
			want := []string{bareReload("apps/email"), opReload("apps/email", "dev"), opReload("apps/email", "qa")}
			if got := f.takeEvents(t); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: events\n got %q\nwant %q", step.state, got, want)
			}
			if got := f.view("apps/email"); got != emailView {
				t.Errorf("%s: apps/email is\n  %s\nwant\n  %s", step.state, got, emailView)
			}
			if got, err := os.ReadFile(lifeRecordPath(root, "apps/email")); err != nil || string(got) != string(emailRecord) {
				t.Errorf("%s: lifecycle wrote the record (%v)", step.state, err)
			}
			if got := b.Reg.LifecycleState("apps/email"); got != step.state {
				t.Errorf("%s: the tile's lifecycle is %q", step.state, got)
			}
		}
	})

	t.Run("main is named when it isn't the primary", func(t *testing.T) {
		for _, state := range []string{"disabled", "enabled"} {
			w := call(t, b.apiLifecycleSet, rootP, "POST", "/lifecycle", `{"component":"apps/notes","state":"`+state+`"}`, nil)
			if w.Code != 200 {
				t.Fatalf("lifecycle %s: %d %s", state, w.Code, w.Body.String())
			}
			f.woke(state == "enabled")
			if got, want := f.takeEvents(t), []string{bareReload("apps/notes"), opReload("apps/notes", "main")}; !reflect.DeepEqual(got, want) {
				t.Errorf("%s: events\n got %q\nwant %q", state, got, want)
			}
		}
		f.takeStops()
	})

	t.Run("a tile without a record publishes today's reload alone", func(t *testing.T) {
		for _, state := range []string{"disabled", "hidden", "enabled"} {
			w := call(t, b.apiLifecycleSet, rootP, "POST", "/lifecycle", `{"component":"apps/calendar","state":"`+state+`"}`, nil)
			if w.Code != 200 {
				t.Fatalf("lifecycle %s: %d %s", state, w.Code, w.Body.String())
			}
			f.woke(state == "enabled")
			if got, want := f.takeEvents(t), []string{bareReload("apps/calendar")}; !reflect.DeepEqual(got, want) {
				t.Errorf("%s: events\n got %q\nwant %q", state, got, want)
			}
		}
		f.takeStops()
		if got := f.view("apps/calendar"); got != "none primary=main reached=main [main=worktree fires=true routes=true] edges=map[]" {
			t.Errorf("apps/calendar gained deployment state: %s", got)
		}
	})

	t.Run("a qualified name has no lifecycle", func(t *testing.T) {
		w := call(t, b.apiLifecycleSet, rootP, "POST", "/lifecycle", `{"component":"apps/email+dev","state":"disabled"}`, nil)
		if w.Code != 404 {
			t.Errorf("disabling apps/email+dev: %d %s, want 404", w.Code, w.Body.String())
		}
		if got := f.takeStops(); len(got) != 0 {
			t.Errorf("StopBackend calls %q, want none", got)
		}
		if got := f.takeEvents(t); len(got) != 0 {
			t.Errorf("events %q, want none", got)
		}
		if _, ok := b.Reg.Workspace().Lifecycle["apps/email+dev"]; ok {
			t.Error("the manifest gained a lifecycle row for a qualified name")
		}
	})

	zeroState := func(t *testing.T, tile, file string) {
		t.Helper()
		if got, want := f.view(tile), "none primary=main reached=main [main=worktree fires=true routes=true] edges=map[]"; got != want {
			t.Errorf("%s is\n  %s\nwant the zero state\n  %s", tile, got, want)
		}
		lifeAbsent(t, "a record for "+tile, lifeRecordPath(root, tile))
		lifeAbsent(t, "a checkpoint store for "+tile, filepath.Join(root, "data", "checkpoints", util.TileKey(tile)+".git"))
		lifeAbsent(t, "registration files for "+tile, filepath.Join(root, "data", "deployments", util.TileKey(tile)))
		if got := f.dp.DeploymentLeftovers(tile); got != nil {
			t.Errorf("%s's leftovers: %q", tile, got)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(tile), file)); err != nil {
			t.Errorf("the work tree's %s wasn't copied: %v", file, err)
		}
	}

	t.Run("a clone starts in the zero state", func(t *testing.T) {
		w := call(t, b.apiClone, rootP, "POST", "/clone", `{"from":"apps/email","to":"apps/letters"}`, nil)
		if w.Code != 200 {
			t.Fatalf("clone: %d %s", w.Code, w.Body.String())
		}
		zeroState(t, "apps/letters", "notes.txt")
		if got, err := os.ReadFile(lifeRecordPath(root, "apps/email")); err != nil || string(got) != string(emailRecord) {
			t.Errorf("the clone touched its source's record (%v)", err)
		}
		if got := f.view("apps/email"); got != emailView {
			t.Errorf("the source is\n  %s\nwant\n  %s", got, emailView)
		}
	})

	t.Run("a template instance starts in the zero state", func(t *testing.T) {
		boardRecord, err := os.ReadFile(lifeRecordPath(root, "templates/board"))
		if err != nil {
			t.Fatal(err)
		}
		w := call(t, b.apiTemplatesNew, rootP, "POST", "/templates/new", `{"source":"templates/board","path":"apps/board"}`, nil)
		if w.Code != 200 {
			t.Fatalf("instantiate: %d %s", w.Code, w.Body.String())
		}
		zeroState(t, "apps/board", "columns.txt")
		if got, err := os.ReadFile(lifeRecordPath(root, "templates/board")); err != nil || string(got) != string(boardRecord) {
			t.Errorf("the instance touched its template's record (%v)", err)
		}
	})
}
