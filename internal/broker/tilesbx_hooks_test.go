package broker

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/backup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// fakeTileSbx is the tile-sandbox runtime as the broker drives it, in
// memory: what it holds, and what it was asked.
type fakeTileSbx struct {
	mu        sync.Mutex
	defs      map[string][]json.RawMessage
	skip      []string
	running   []TileSandbox
	state     map[string][2]int64 // tile → n, bytes
	left      map[string][2]int64 // path → n, bytes
	usage     map[string]int64
	stopDelay time.Duration

	log []string // what happened, in order
}

func (f *fakeTileSbx) note(s string) { f.mu.Lock(); f.log = append(f.log, s); f.mu.Unlock() }

func (f *fakeTileSbx) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeTileSbx) Defs(tile string) []json.RawMessage { return f.defs[tile] }
func (f *fakeTileSbx) RestoreDefs(tile string, defs []json.RawMessage) []string {
	parts := make([]string, len(defs))
	for i, d := range defs {
		var c bytes.Buffer
		_ = json.Compact(&c, d) // the manifest is indented
		parts[i] = c.String()
	}
	f.note("restore " + tile + " " + strings.Join(parts, ","))
	return f.skip
}
func (f *fakeTileSbx) StopTile(tile, why string) { f.note("stop " + tile + ": " + why) }
func (f *fakeTileSbx) StopWhere(pred func(TileSandbox) bool, why string) {
	var picked []string
	for _, s := range f.running {
		if pred(s) {
			picked = append(picked, s.Name)
		}
	}
	time.Sleep(f.stopDelay)
	f.note("stopWhere [" + strings.Join(picked, " ") + "]: " + why)
}
func (f *fakeTileSbx) HasState(tile string) (int, int64) { s := f.state[tile]; return int(s[0]), s[1] }
func (f *fakeTileSbx) Leftovers(path string) (int, int64) {
	l := f.left[path]
	return int(l[0]), l[1]
}
func (f *fakeTileSbx) Usage() map[string]int64      { return f.usage }
func (f *fakeTileSbx) OnResourceChange(tile string) { f.note("resources " + tile) }
func (f *fakeTileSbx) OnLowDisk()                   { f.note("lowdisk") }

// wsOwner is the workspace owner (an admin).
var wsOwner = auth.Principal{Owner: true}

// archived binds the fake archiver as the workspace default.
func archived(t *testing.T, b *Broker) *fakeArchiver {
	t.Helper()
	a := &fakeArchiver{}
	b.ProxyHandler = a
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"*": {archiveSlot: {{Ref: "apps/archiver"}}}}
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

// backupOf is comp's backup tar, its creation time blanked (same length:
// the tar stays valid and comparable byte for byte).
func backupOf(t *testing.T, b *Broker, comp string) []byte {
	t.Helper()
	c, _ := b.Reg.Component(comp)
	var buf bytes.Buffer
	bw := backup.NewWriter(&buf)
	if err := b.writeBackup(bw, c, nil); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	return regexp.MustCompile(`"created": "[^"]*"`).ReplaceAllFunc(buf.Bytes(), func(m []byte) []byte {
		return append(append([]byte(`"created": "`), bytes.Repeat([]byte("x"), len(m)-len(`"created": ""`))...), '"')
	})
}

// §9: a tile without sandboxes backs up byte for byte as before; a
// manager's definitions ride along (never their state), and a restore
// stops its sandboxes first, then merges them back, answering what it
// skipped.
func TestBackupCarriesSandboxDefinitions(t *testing.T) {
	const comp = "apps/calendar"
	b := testBroker(t)
	before := backupOf(t, b, comp)
	f := &fakeTileSbx{defs: map[string][]json.RawMessage{"apps/other": {json.RawMessage(`{"name":"x"}`)}}}
	b.SetTileSandboxes(f)
	if after := backupOf(t, b, comp); !bytes.Equal(before, after) {
		t.Fatal("a tile without sandboxes backs up differently with the runtime installed")
	}

	f.defs[comp] = []json.RawMessage{json.RawMessage(`{"name":"a","uid":"0123456789ab"}`), json.RawMessage(`{"name":"b","uid":"ba9876543210"}`)}
	br, err := backup.NewReader(bytes.NewReader(backupOf(t, b, comp)))
	if err != nil {
		t.Fatal(err)
	}
	if !br.M.Has("sandboxes") || len(br.M.Sandboxes) != 2 {
		t.Fatalf("the manifest: includes %v, %d sandboxes", br.M.Includes, len(br.M.Sandboxes))
	}

	archived(t, b)
	f.skip = []string{"a (uid 0123456789ab): the name is taken by another sandbox (uid 111111111111): the live one is kept"}
	if _, err := b.doBackup(comp); err != nil {
		t.Fatal(err)
	}
	w := call(t, b.apiRestore, wsOwner, "POST", "/restore", `{"component":"apps/calendar"}`, nil)
	if w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	var out struct {
		Restored         []string
		SandboxesSkipped []string
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.SandboxesSkipped) != 1 || !strings.Contains(strings.Join(out.Restored, ","), "sandboxes") {
		t.Fatalf("the restore's answer: %s", w.Body)
	}
	ev := f.events()
	if len(ev) != 2 || !strings.HasPrefix(ev[0], "stop apps/calendar: its tile was restored") ||
		ev[1] != `restore apps/calendar {"name":"a","uid":"0123456789ab"},{"name":"b","uid":"ba9876543210"}` {
		t.Fatalf("the restore asked the runtime %q", ev)
	}
}

// §9: offload refuses (409, nothing archived) while the tile's sandboxes
// hold state, and goes ahead once they don't; disabling or hiding a tile
// stops its sandboxes.
func TestLifecycleAndTileSandboxes(t *testing.T) {
	const comp = "apps/calendar"
	b := testBroker(t)
	a := archived(t, b)
	f := &fakeTileSbx{state: map[string][2]int64{comp: {2, 3 << 30}}}
	b.SetTileSandboxes(f)
	set := func(state string) *httptest.ResponseRecorder {
		return call(t, b.apiLifecycleSet, wsOwner, "POST", "/lifecycle", `{"component":"apps/calendar","state":"`+state+`"}`, nil)
	}
	w := set("offloaded")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "its 2 tile sandboxes hold state (3.0GB)") {
		t.Fatalf("offload with sandbox state: %d %s", w.Code, w.Body)
	}
	if a.latest != nil || b.Reg.LifecycleState(comp) != registry.StateEnabled || len(f.events()) != 0 {
		t.Fatalf("a refused offload archived, flipped or stopped something: %q", f.events())
	}
	f.state = nil
	if w := set("offloaded"); w.Code != 200 {
		t.Fatalf("offload without sandbox state: %d %s", w.Code, w.Body)
	}
	if a.latest == nil {
		t.Fatal("nothing archived")
	}
	for _, st := range []string{"enabled", "disabled", "hidden"} {
		if w := set(st); w.Code != 200 {
			t.Fatalf("%s: %d %s", st, w.Code, w.Body)
		}
	}
	ev := f.events()
	want := []string{"stop apps/calendar: its tile was offloaded: stopped, state kept",
		"stop apps/calendar: its tile was restored from a backup: stopped, state kept", // enabling an offloaded tile restores it
		"stop apps/calendar: its tile was disabled: stopped, state kept",
		"stop apps/calendar: its tile was hidden: stopped, state kept"}
	if strings.Join(ev, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the runtime was asked\n%s\nwant\n%s", strings.Join(ev, "\n"), strings.Join(want, "\n"))
	}
}

// §5: sealing the vault stops every sandbox with a resource mounted — and
// waits for the stops — before the views unmount; the others run on.
func TestSealStopsMountedSandboxes(t *testing.T) {
	b := testBroker(t)
	f := &fakeTileSbx{stopDelay: 50 * time.Millisecond, running: []TileSandbox{
		{Tile: "apps/calendar", Name: "m", Res: []string{"res:apps/calendar/events"}},
		{Tile: "apps/calendar", Name: "plain"},
		{Tile: "apps/email", Name: "ws", Res: []string{"res:workspace/files"}},
	}}
	b.SetTileSandboxes(f)
	b.SealResources()
	ev := f.events() // SealResources returned: the stop had ended
	if len(ev) != 1 || ev[0] != "stopWhere [m ws]: the vault was sealed: stopped, state kept — start it again once the vault is unsealed" {
		t.Fatalf("the seal asked %q", ev)
	}
}

// §5: a res: grant approved or revoked re-checks the tile's running
// mounts; a cap:containers flip stops the scope's mounted sandboxes before
// the remount — and only those.
func TestGrantChangesReachTileSandboxes(t *testing.T) {
	b := testBroker(t)
	f := &fakeTileSbx{running: []TileSandbox{
		{Tile: "apps/calendar", Name: "m", Res: []string{"res:apps/calendar/events"}},
		{Tile: "apps/email", Name: "ws", Res: []string{"res:workspace/files"}},
		{Tile: "apps/calendar", Name: "plain"},
	}}
	b.SetTileSandboxes(f)
	b.OnGrantChange = func(string) {}
	grant := `{"from":"apps/calendar","target":"res:apps/calendar/events","role":"writer"}`
	if w := call(t, b.apiGrantsRevoke, wsOwner, "DELETE", "/grants", grant, nil); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if w := call(t, b.apiGrantsAdd, wsOwner, "POST", "/grants", grant, nil); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	b.grantRestart(registry.Grant{From: "apps/calendar", Target: ContainersCap, Role: "writer"})
	ev := f.events()
	want := []string{"resources apps/calendar", "resources apps/calendar",
		"stopWhere [m]: its tile's scope changed how its resources are mounted (cap:containers): stopped, state kept — start it again"}
	if strings.Join(ev, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the runtime was asked\n%s\nwant\n%s", strings.Join(ev, "\n"), strings.Join(want, "\n"))
	}
}

// §9: a removed tile's sandboxes are a leftover of its path — a tile
// created there would inherit them.
func TestPathLeftoversTileSandboxes(t *testing.T) {
	b := testBroker(t)
	b.SetTileSandboxes(&fakeTileSbx{left: map[string][2]int64{"apps/gone": {2, 5 << 30}}})
	left := b.pathLeftovers("apps/gone", "user:bob")
	if len(left) != 1 || left[0] != "2 tile sandboxes (5.0GB) of a removed tile" {
		t.Fatalf("leftovers %q", left)
	}
	if left := b.pathLeftovers("apps/new", "user:bob"); len(left) != 0 {
		t.Fatalf("leftovers of a clean path %q", left)
	}
	if ok, why := b.newTilePathOK("apps/gone", "user:bob"); ok || !strings.Contains(why, "tile sandboxes") {
		t.Fatalf("a non-admin creates over a removed tile's sandboxes: %v %q", ok, why)
	}
}

// §6.3/§9: diskmon counts sandbox bytes for disk pressure only — a scope
// whose manager's sandboxes hold 60 GiB still writes its kv (the quota and
// write blocking count its resources alone), the fair share includes them,
// and a low-disk verdict is passed on to the runtime.
func TestDiskMonSandboxBytes(t *testing.T) {
	b := testBroker(t)
	f := &fakeTileSbx{usage: map[string]int64{"apps/calendar": 60 << 30}}
	b.SetTileSandboxes(f)
	if b.disk.sbxUsage == nil || b.disk.onLow == nil {
		t.Fatal("New doesn't wire the tile sandboxes into diskmon")
	}
	// a monitor of our own, fed and scanned by the test (New's scans in the
	// background)
	b.disk.close()
	d := newDiskMon(b.Reg.Root, 0, func() map[string]int64 { return map[string]int64{util.ScopeKey("apps/calendar"): 2 << 30} })
	d.sbxUsage, d.onLow = b.sandboxUsage, b.sandboxesLowDisk
	d.free = func(string) (int64, int64) { return 50 << 30, 100 << 30 }
	b.disk = d
	b.disk.scan()
	if _, blocked := b.disk.Blocked(util.ScopeKey("apps/calendar")); blocked {
		t.Fatal("sandbox bytes blocked the scope's writes")
	}
	w := httptest.NewRecorder()
	if !b.quotaOK(w, "apps/calendar", "writer") {
		t.Fatalf("a kv write is refused: %s", w.Body)
	}
	if got := b.DiskFairShare(); got != 31<<30 {
		t.Fatalf("fair share %d, want the sandboxes' bytes counted (31 GiB)", got)
	}
	if len(f.events()) != 0 {
		t.Fatal("told the runtime the disk is low when it isn't")
	}
	b.disk.free = func(string) (int64, int64) { return 5 << 30, 100 << 30 }
	b.disk.scan()
	if ev := f.events(); len(ev) != 1 || ev[0] != "lowdisk" || !b.DiskLow() {
		t.Fatalf("a low disk: %q", ev)
	}
	// low disk write-blocks resources above the fair share — still not
	// counting the sandboxes' bytes against the scope
	if _, blocked := b.disk.Blocked(util.ScopeKey("apps/calendar")); blocked {
		t.Fatal("under low disk, the scope's 2 GiB of resources were blocked for its sandboxes' 60")
	}
	if !b.DiskLowAt(9, 100) || b.DiskLowAt(11, 100) || b.DiskLowAt(0, 0) {
		t.Fatal("DiskLowAt isn't diskmon's reserve rule")
	}
}
