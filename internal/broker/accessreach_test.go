package broker

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// grantsAdd and bindAdd change the workspace's wiring for a test.
func (w *csWorld) grantsAdd(t *testing.T, gs ...registry.Grant) {
	t.Helper()
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { ws.Grants = append(ws.Grants, gs...) }); err != nil {
		t.Fatal(err)
	}
}

func (w *csWorld) grantsDrop(t *testing.T, from string) {
	t.Helper()
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = slices.DeleteFunc(ws.Grants, func(g registry.Grant) bool { return g.From == from })
	}); err != nil {
		t.Fatal(err)
	}
}

func (w *csWorld) bindSet(t *testing.T, comp, slot string, refs ...string) {
	t.Helper()
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Bindings == nil {
			ws.Bindings = map[string]map[string]registry.Binding{}
		}
		if ws.Bindings[comp] == nil {
			ws.Bindings[comp] = map[string]registry.Binding{}
		}
		if len(refs) == 0 {
			delete(ws.Bindings[comp], slot)
			return
		}
		var b registry.Binding
		for _, r := range refs {
			b = append(b, registry.BindRef{Ref: r})
		}
		ws.Bindings[comp][slot] = b
	}); err != nil {
		t.Fatal(err)
	}
}

// reachWorld is accessWorld with apps/x (alice's) holding nested tiles and
// slots, and apps/root holding the admin API.
func reachWorld(t *testing.T) *csWorld {
	t.Helper()
	w := accessWorld(t)
	files := map[string]string{
		"apps/x/xbin.json":          `{"runtime":"go","interfaces":{"out":{"kind":"net"},"data":{"kind":"http"},"llm":{"kind":"http","service":"openai"}}}`,
		"apps/x/backend/main.go":    "package main\n",
		"apps/root/xbin.json":       `{"runtime":"go"}`,
		"apps/root/backend/main.go": "package main\n",
	}
	for i := 1; i <= 7; i++ {
		files[fmt.Sprintf("apps/x/b%d/xbin.json", i)] = `{"runtime":"go"}`
		files[fmt.Sprintf("apps/x/b%d/backend/main.go", i)] = "package main\n"
	}
	w.write(files)
	w.rescan()
	w.grantsAdd(t, registry.Grant{From: "apps/root", Target: "xbin", Role: "admin"})
	return w
}

func accessErr(t *testing.T, w *csWorld, who, tile, user, level string) (delegation.ChangePreview, error) {
	t.Helper()
	return w.b.PreviewPlanned(personP(t, w.partWS, who), accessOp(tile, user, level), nil)
}

// covers D246 (review H1) — write is warned like terminal, a personal line
// too: a tile's writers change its code and every nested tile's.
func TestAccessLineWriteIsWarned(t *testing.T) {
	w := reachWorld(t)
	for _, who := range []string{"alice", "bob"} { // alice's own tile: personal; bob: an admin's
		pv, err := accessErr(t, w, who, "apps/x", "carol", "write")
		if err != nil {
			t.Fatal(err)
		}
		if !hasWarning(pv, "WRITE") || !strings.Contains(strings.Join(pv.Warnings, " "), "every tile inside it") {
			t.Errorf("%s: write on apps/x isn't warned: %v", who, pv.Warnings)
		}
		if who == "alice" && (pv.Admin || pv.Blocked != "") {
			t.Errorf("alice's line is no longer personal: admin %v blocked %q", pv.Admin, pv.Blocked)
		}
	}
	pv, _ := accessErr(t, w, "bob", "apps/x", "carol", "terminal")
	if !hasWarning(pv, "TERMINAL") || hasWarning(pv, "WRITE") || !strings.Contains(strings.Join(pv.Warnings, " "), "every tile inside it") {
		t.Errorf("terminal: %v", pv.Warnings)
	}
	if pv, _ := accessErr(t, w, "bob", "apps/x", "carol", "read"); len(pv.Warnings) != 0 {
		t.Errorf("read is warned: %v", pv.Warnings)
	}
}

// covers D246 (review H1) — power is judged on the tile's subtree and on
// what its tiles reach through code: and call grants and bindings: a raise
// on any of them is refused; a lowering isn't; a cycle ends; a walk that
// stops at its bound is refused.
func TestAccessLinePowerOverSubtreeAndReach(t *testing.T) {
	w := reachWorld(t)
	refused := func(why string) {
		t.Helper()
		for _, who := range []string{"bob", "alice"} {
			_, err := accessErr(t, w, who, "apps/x", "carol", "write")
			if err == nil || !strings.Contains(err.Error(), "holds power") {
				t.Errorf("%s, %s: a raise on apps/x isn't refused: %v", why, who, err)
				continue
			}
			if who == "alice" && !strings.HasSuffix(err.Error(), "never by a change set") {
				t.Errorf("%s: the holders are named to a person who isn't a workspace admin: %v", why, err)
			}
		}
		if _, err := accessErr(t, w, "bob", "apps/x", "carol", "none"); err != nil {
			t.Errorf("%s: a lowering is refused: %v", why, err)
		}
	}
	if _, err := accessErr(t, w, "bob", "apps/x", "carol", "write"); err != nil {
		t.Fatalf("apps/x reaches no power yet: %v", err)
	}
	w.grantsAdd(t, registry.Grant{From: "apps/x/b3", Target: "cap:containers", Role: "use"})
	refused("a nested tile holds a capability")
	if _, err := accessErr(t, w, "bob", "apps/x", "carol", "write"); err == nil || !strings.Contains(err.Error(), "cap:containers (apps/x/b3)") {
		t.Errorf("an admin isn't told which: %v", err)
	}
	w.grantsDrop(t, "apps/x/b3")
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "code:apps/root", Role: "reader"})
	refused("a code: grant on a tile with the admin API")
	w.grantsDrop(t, "apps/x")
	w.grantsAdd(t, registry.Grant{From: "apps/x/b1", Target: "apps/root", Role: "reader"})
	refused("a nested tile's call grant to a tile with the admin API")
	w.grantsDrop(t, "apps/x/b1")
	w.bindSet(t, "apps/x", "data", "apps/root")
	refused("a binding to a tile with the admin API")
	w.bindSet(t, "apps/x", "data")
	w.bindSet(t, "apps/x", "out", "host")
	refused("the host's network")
	w.bindSet(t, "apps/x", "out")
	// a cycle: apps/x → apps/pu → apps/x, nothing of power: it ends, and the raise is a line
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "apps/pu", Role: "reader"}, registry.Grant{From: "apps/pu", Target: "apps/x", Role: "reader"})
	if _, err := accessErr(t, w, "bob", "apps/x", "carol", "write"); err != nil {
		t.Fatalf("a cycle: %v", err)
	}
	w.grantsDrop(t, "apps/x")
	w.grantsDrop(t, "apps/pu")
	// a chain longer than the walk follows: refused, as what lies beyond isn't known
	chain := map[string]string{}
	for i := range accessReachMax + 2 {
		chain[fmt.Sprintf("apps/k%d/xbin.json", i)] = `{"runtime":"go"}`
		chain[fmt.Sprintf("apps/k%d/backend/main.go", i)] = "package main\n"
	}
	w.write(chain)
	w.rescan()
	gs := []registry.Grant{{From: "apps/x", Target: "apps/k0", Role: "reader"}}
	for i := range accessReachMax + 1 {
		gs = append(gs, registry.Grant{From: fmt.Sprintf("apps/k%d", i), Target: fmt.Sprintf("apps/k%d", i+1), Role: "reader"})
	}
	w.grantsAdd(t, gs...)
	refused("a chain past the walk's bound")
}

// covers D246 (review H1) — the line lists what write or terminal brings:
// the nested tiles by name (capped, with the count), the tiles reached —
// named where the person may read them —, the networks, the LLM
// connector, the vault secrets.
func TestAccessLineEffectsListWhatTheLevelBrings(t *testing.T) {
	w := reachWorld(t)
	w.write(map[string]string{"hidden/z/xbin.json": `{"runtime":"go"}`, "hidden/z/backend/main.go": "package main\n"})
	w.rescan()
	w.bindSet(t, "apps/x", "out", "lan:10.0.0.0/8")
	w.bindSet(t, "apps/x", "llm", LLMRef)
	w.grantsAdd(t, registry.Grant{From: "apps/x/b2", Target: "apps/pu", Role: "reader"}, registry.Grant{From: "apps/x/b2", Target: "hidden/z", Role: "reader"})
	vp := w.b.vaultPath("apps/x/b1")
	if err := os.MkdirAll(filepath.Dir(vp), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	pv, err := accessErr(t, w, "alice", "apps/x", "carol", "write")
	if err != nil {
		t.Fatal(err)
	}
	eff := strings.Join(pv.Effects, "\n")
	for _, want := range []string{
		"the 7 tile(s) inside apps/x, whose code its writers change too: apps/x/b1, apps/x/b2, apps/x/b3, apps/x/b4, apps/x/b5 and 2 more",
		"the tiles its code calls or reads through its grants and bindings: apps/pu, 2 other tile(s)", // apps/pg (an org's: her pattern stops there) and hidden/z
		"the networks its tiles are bound to: lan:10.0.0.0/8",
		"the workspace's LLM connector (bound for apps/x)",
		"the vault secrets of apps/x/b1",
	} {
		if !strings.Contains(eff, want) {
			t.Errorf("the effects don't say %q:\n%s", want, eff)
		}
	}
	if strings.Contains(eff, "hidden/z") {
		t.Errorf("a tile alice can't read is named to her:\n%s", eff)
	}
	if pv, _ := accessErr(t, w, "alice", "apps/x", "carol", "read"); strings.Contains(strings.Join(pv.Effects, "\n"), "inside apps/x") {
		t.Errorf("read lists what write brings: %v", pv.Effects)
	}
}

// covers D246 (review M1) — raise, warnings and the power refusal follow
// the EFFECTIVE level before and after, as the Access page explains it:
// removing an exclusion under an org's terminal is a raise to terminal,
// said with where it comes from; for an account nothing was resolved for,
// removing its entry counts as a raise.
func TestAccessLineJudgesEffectiveLevels(t *testing.T) {
	w := accessWorld(t)
	if _, err := w.b.Users.UpsertOrg(users.Org{ID: "fin", Name: "Finance", Members: []users.Member{{ID: "carol", Level: users.LevelTerminal}, {ID: "erin", Level: users.LevelRead, Admin: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.b.Users.SetUserTile("carol", "apps/pg", users.LevelNone); err != nil {
		t.Fatal(err)
	}
	pv, err := accessErr(t, w, "bob", "apps/pg", "carol", "")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Line != "carol — “Carol Fin”: none (excluded) → terminal through org fin on apps/pg" || !hasWarning(pv, "TERMINAL") {
		t.Errorf("removing an exclusion: %q %v", pv.Line, pv.Warnings)
	}
	// a lowering from what the org gives isn't warned
	if pv, err := accessErr(t, w, "bob", "apps/pg", "carol", "read"); err != nil || len(pv.Warnings) != 0 || !strings.Contains(pv.Line, "none (excluded) → read by their own entry") {
		t.Errorf("a read entry: %v %q %v", err, pv.Line, pv.Warnings)
	}
	w.grantsAdd(t, registry.Grant{From: "apps/pg", Target: "cap:containers", Role: "use"})
	if _, err := accessErr(t, w, "bob", "apps/pg", "carol", ""); err == nil || !strings.Contains(err.Error(), "holds power") {
		t.Errorf("removing an exclusion on a tile with power: %v", err)
	}
	if err := w.b.Users.SetUserTile("carol", "apps/pg", users.LevelTerminal); err != nil {
		t.Fatal(err)
	}
	if _, err := accessErr(t, w, "bob", "apps/pg", "carol", "read"); err != nil {
		t.Errorf("a lowering on a tile with power: %v", err)
	}
	w.grantsDrop(t, "apps/pg") // (apps/x calls apps/pg)
	// alice sees no accounts: removing an entry on her tile is a raise whose result isn't known
	pv, err = accessErr(t, w, "alice", "apps/x", "carol", "")
	if err != nil || !hasWarning(pv, "ENTRY REMOVED") || !hasWarning(pv, "TERMINAL") {
		t.Errorf("an unresolved removal: %v %v", err, pv.Warnings)
	}
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "cap:containers", Role: "use"})
	if _, err := accessErr(t, w, "alice", "apps/x", "carol", ""); err == nil || !strings.Contains(err.Error(), "holds power") {
		t.Errorf("an unresolved removal on a tile with power: %v", err)
	}
	if _, err := accessErr(t, w, "alice", "apps/x", "carol", "none"); err != nil {
		t.Errorf("an exclusion on a tile with power: %v", err)
	}
}

// covers D246 (review LOW b) — the account's id comes first and apart, its
// display name after it, quoted, with control and bidi characters shown
// escaped: a display name can't pass for another account's id.
func TestAccessLineNamesTheIDFirst(t *testing.T) {
	w := accessWorld(t)
	for _, u := range []users.User{{ID: "mallory", Name: "Dave Rossi (dave)", Role: users.RoleUser}, {ID: "eve", Name: "Eve\u202eevil", Role: users.RoleUser}} {
		if _, err := w.b.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	pv, err := accessErr(t, w, "bob", "apps/pu", "mallory", "read")
	if err != nil || !strings.HasPrefix(pv.Line, "mallory — “Dave Rossi (dave)”: ") {
		t.Errorf("a display name like another account: %v %q", err, pv.Line)
	}
	pv, err = accessErr(t, w, "bob", "apps/pu", "eve", "read")
	if err != nil || !strings.HasPrefix(pv.Line, `eve — “Eve\u202eevil”: `) || strings.ContainsRune(pv.Line, '\u202e') {
		t.Errorf("a bidi override in a display name: %v %q", err, pv.Line)
	}
}

// covers D246 (review LOW c) — ids are compared as the store keys them
// (users.NormalizeID), not by Unicode case folding: "ſam" is not sam's id,
// and sam's own line about it is not his own access.
func TestAccessLineComparesIDsAsTheStore(t *testing.T) {
	w := accessWorld(t)
	if _, err := w.b.Users.Upsert(users.User{ID: "sam", Role: users.RoleUser}, "password1"); err != nil {
		t.Fatal(err)
	}
	if err := w.b.Users.SetOwner("apps/pu", "user:sam"); err != nil {
		t.Fatal(err)
	}
	if pv, err := accessErr(t, w, "sam", "apps/pu", "SAM", "write"); err != nil || pv.Blocked != accessSelfBlocked {
		t.Errorf("sam's own id, upper case: %v %+v", err, pv)
	}
	if pv, err := accessErr(t, w, "sam", "apps/pu", "ſam", "write"); err != nil || pv.Blocked == accessSelfBlocked {
		t.Errorf("another spelling the store keys apart is taken for sam himself: %v %q", err, pv.Blocked)
	}
}

// covers D246 (review LOW d) — a tile's power is said only to a person who
// manages its access: anyone else reads the 403 line, the same whatever
// the tile holds.
func TestAccessLinePowerSaidOnlyToItsManagers(t *testing.T) {
	w := accessWorld(t)
	plain, _ := accessErr(t, w, "carol", "apps/pu", "erin", "read")
	w.grantsAdd(t, registry.Grant{From: "apps/pu", Target: "cap:containers", Role: "use"})
	for _, op := range []delegation.ChangeOp{accessOp("apps/pu", "erin", "read"), {Kind: ChangeOrgShare, Args: mustJSON(map[string]string{"org": "fin", "tile": "apps/pu", "level": "read"})}} {
		pv, err := w.b.PreviewPlanned(personP(t, w.partWS, "carol"), op, nil)
		if err != nil || !pv.Forbidden || strings.Contains(pv.Blocked+pv.Line, "cap:") || strings.Contains(pv.Blocked, "power") {
			t.Errorf("%s by a person who doesn't manage apps/pu: %v %+v", op.Kind, err, pv)
		}
		if op.Kind == ChangeAccessSet && (pv.Blocked != plain.Blocked || pv.Line != plain.Line) {
			t.Errorf("the refusal tells a tile with power apart: %q / %q", pv.Blocked, plain.Blocked)
		}
	}
}

// covers D246 (review M3) — a job's name is a short label: a new one with a
// control or format character or past 128 characters is refused; one a
// tile registered before the rule keeps working; a tile read for a
// person's agent shows names short and escaped.
func TestCronNamesAreShortLabels(t *testing.T) {
	f := newDormantFx(t, false)
	b := f.b
	job := func(name string) map[string]any {
		return map[string]any{"name": name, "resource": "res:apps/shop/ticks", "schedule": "@every 1h", "path": "/t"}
	}
	long := strings.Repeat("n", 200)
	for _, name := range []string{"a\x07b", "x\u202ey", long} {
		if rec := regCall(t, b.apiCronPut, shopMain, "PUT", "/cron/jobs", "", job(name)); rec.Code != 400 {
			t.Errorf("a new job named %q: %d %s", name, rec.Code, rec.Body)
		}
	}
	mustCode(t, regCall(t, b.apiCronPut, shopMain, "PUT", "/cron/jobs", "", job("nightly")), 200, "a plain name")
	// registered before the rule: it keeps working
	if err := b.cron.add(cronJob{Name: long, Schedule: "@every 1h", Component: fxShop, Path: "/t", Resource: "res:apps/shop/ticks"}); err != nil {
		t.Fatal(err)
	}
	mustCode(t, regCall(t, b.apiCronPut, shopMain, "PUT", "/cron/jobs", "", job(long)), 200, "a long name registered before")
	if got := cronNameWords(long); got != strings.Repeat("n", cronNameShown)+"…" {
		t.Errorf("a long name shown: %q", got)
	}
	if got := cronNameWords("x\u202ey"); got != `x\u202ey` {
		t.Errorf("a bidi override shown: %q", got)
	}
}
