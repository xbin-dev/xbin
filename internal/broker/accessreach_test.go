package broker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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

func powerBlocked(pv delegation.ChangePreview) bool {
	return pv.Power && strings.HasPrefix(pv.Blocked, "xbin refuses this one: ")
}

// covers D246 (second review, 1) — the power rule: a raise to write or
// terminal is refused when power is held by code its writers change — the
// tile, a tile nested in it, a tile it lies in, a tile whose build, deps
// (transitively) or pages' import map take its code; it is only warned,
// with the power listed, when its code merely calls a tile that holds
// power (a call or code: grant, a binding); a cap below power
// (cap:open-links) is listed, never refused; a read raise, a lowering and
// an exclusion are never refused; the holders are named to an admin and,
// to the tile's owner, where she may see their grants.
func TestAccessLinePowerRule(t *testing.T) {
	w := reachWorld(t)
	whos := []string{"bob", "alice"}
	refused := func(why, tile string, holders ...string) {
		t.Helper()
		for _, who := range whos {
			pv, err := accessErr(t, w, who, tile, "carol", "write")
			if err != nil || !powerBlocked(pv) {
				t.Errorf("%s, %s: a raise to write on %s isn't refused: %v %q", why, who, tile, err, pv.Blocked)
				continue
			}
			for _, h := range holders {
				if who == "bob" && !strings.Contains(pv.Blocked, "held by "+h) {
					t.Errorf("%s: the admin isn't told %s holds it: %q", why, h, pv.Blocked)
				}
			}
		}
		for _, level := range []string{"read", "none"} {
			if pv, err := accessErr(t, w, "bob", tile, "carol", level); err != nil || pv.Power || pv.Blocked != "" {
				t.Errorf("%s: %s on %s is refused: %v %q", why, level, tile, err, pv.Blocked)
			}
		}
	}
	warned := func(why string, effect string) {
		t.Helper()
		pv, err := accessErr(t, w, "bob", "apps/x", "carol", "write")
		if err != nil || pv.Power || pv.Blocked != "" {
			t.Errorf("%s: refused: %v %q", why, err, pv.Blocked)
		}
		if effect != "" && !strings.Contains(strings.Join(pv.Effects, "\n"), effect) {
			t.Errorf("%s: the effects don't say %q: %v", why, effect, pv.Effects)
		}
	}
	warned("nothing of power", "")
	// held by the tile, a tile inside it, a tile it lies in
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "cap:containers", Role: "use"})
	refused("the tile itself", "apps/x", "apps/x")
	whos = []string{"bob"} // (alice owns apps/x, not the tiles inside it)
	refused("a tile it lies in", "apps/x/b1", "apps/x")
	whos = []string{"bob", "alice"}
	w.grantsDrop(t, "apps/x")
	w.grantsAdd(t, registry.Grant{From: "apps/x/b3", Target: "gpu:0", Role: "use"})
	refused("a nested tile holds a device", "apps/x", "apps/x/b3")
	w.grantsDrop(t, "apps/x/b3")
	w.bindSet(t, "apps/x", "out", "host")
	refused("the host's network", "apps/x", "apps/x")
	w.bindSet(t, "apps/x", "out")
	// a tile that builds or loads its code: its build (BuildsWith), its deps (transitively), its pages' import map
	w.b.BuildsWith = func(c *registry.Component) []string {
		if c.Path == "apps/root" {
			return []string{"apps/x/b2"}
		}
		return nil
	}
	refused("a power tile's build takes a nested tile's module", "apps/x", "apps/root")
	w.b.BuildsWith = nil
	w.write(map[string]string{"apps/lib/xbin.json": `{"runtime":"go","deps":["apps/x"]}`, "apps/lib/backend/main.go": "package main\n",
		"apps/root/xbin.json": `{"runtime":"go","deps":["apps/lib"]}`})
	w.rescan()
	refused("a power tile's deps take it, through a library", "apps/x", "apps/root")
	w.write(map[string]string{"apps/lib/xbin.json": `{"runtime":"go"}`, "apps/root/xbin.json": `{"runtime":"go"}`})
	w.rescan()
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { ws.ImportMap = map[string]string{"ui": "/c/apps/x/b4/ui.js"} }); err != nil {
		t.Fatal(err)
	}
	refused("a power tile's pages load it through the import map", "apps/x", "apps/root")
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { ws.ImportMap = nil }); err != nil {
		t.Fatal(err)
	}
	// what its code only calls: warned and listed, never refused
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "apps/root", Role: "reader"})
	warned("a call grant to a tile with the admin API", "apps/root, which holds xbin — the admin API")
	if pv, _ := accessErr(t, w, "bob", "apps/x", "carol", "write"); !hasWarning(pv, "REACHES POWER") {
		t.Errorf("a call to power isn't warned: %v", pv.Warnings)
	}
	w.grantsDrop(t, "apps/x")
	w.grantsAdd(t, registry.Grant{From: "apps/x/b1", Target: "code:apps/root", Role: "reader"})
	warned("a code: grant (read-only) on a tile with the admin API", "apps/root, which holds xbin")
	w.grantsDrop(t, "apps/x/b1")
	w.bindSet(t, "apps/x", "data", "apps/root")
	warned("a binding to a tile with the admin API", "apps/root, which holds xbin")
	w.bindSet(t, "apps/x", "data")
	// a cap below power: listed
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "cap:open-links", Role: "writer"})
	warned("cap:open-links", "cap:open-links — its pages may open windows that leave their sandbox (apps/x)")
	w.grantsDrop(t, "apps/x")
	// what the owner sees of a holder she can't see: counted
	w.write(map[string]string{"hidden/z/xbin.json": `{"runtime":"go","deps":["apps/x"]}`, "hidden/z/backend/main.go": "package main\n"})
	w.rescan()
	w.grantsAdd(t, registry.Grant{From: "hidden/z", Target: "xbin", Role: "admin"})
	if pv, _ := accessErr(t, w, "alice", "apps/x", "carol", "write"); !powerBlocked(pv) || strings.Contains(pv.Blocked, "hidden/z") || !strings.Contains(pv.Blocked, "power held by 1 tile(s) you can't see") {
		t.Errorf("a holder alice can't see: %q", pv.Blocked)
	}
}

// covers D246 (second review, 1) — the walks are linear: 300 nested tiles
// are no refusal and take little time; only more tiles holding power than
// a raise checks (accessHoldersMax) refuse it, failing closed.
func TestAccessLinePowerRuleScales(t *testing.T) {
	w := reachWorld(t)
	files := map[string]string{}
	for i := range 300 {
		files[fmt.Sprintf("apps/x/n%03d/xbin.json", i)] = `{"runtime":"go"}`
	}
	w.write(files)
	w.rescan()
	start := time.Now()
	pv, err := accessErr(t, w, "bob", "apps/x", "carol", "write")
	if err != nil || pv.Power || pv.Blocked != "" {
		t.Fatalf("300 nested tiles: %v %q", err, pv.Blocked)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("300 nested tiles took %v", d)
	}
	var gs []registry.Grant
	files = map[string]string{}
	for i := range accessHoldersMax + 1 {
		files[fmt.Sprintf("apps/p%02d/xbin.json", i)] = `{"runtime":"go"}`
		gs = append(gs, registry.Grant{From: fmt.Sprintf("apps/p%02d", i), Target: "cap:containers", Role: "use"})
	}
	w.write(files)
	w.rescan()
	w.grantsAdd(t, gs...)
	if pv, _ := accessErr(t, w, "bob", "apps/x", "carol", "write"); !powerBlocked(pv) || !strings.Contains(pv.Blocked, "more than xbin checks") {
		t.Errorf("more tiles holding power than a raise checks: %q", pv.Blocked)
	}
}

// covers D246 (second review, 1) — org.share: a raise of a share on code
// that holds power is refused (as before, for real power only); lowering
// or removing one is a line.
func TestOrgShareLowersOnPowerTiles(t *testing.T) {
	w := reachWorld(t)
	if err := w.b.Users.SetOrgTile("fin", "apps/x", users.LevelWrite); err != nil {
		t.Fatal(err)
	}
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "cap:containers", Role: "use"})
	share := func(level string) delegation.ChangePreview {
		t.Helper()
		pv, err := w.b.PreviewPlanned(personP(t, w.partWS, "bob"), delegation.ChangeOp{Kind: ChangeOrgShare, Args: mustJSON(map[string]string{"org": "fin", "tile": "apps/x", "level": level})}, nil)
		if err != nil {
			t.Fatalf("share at %q: %v", level, err)
		}
		return pv
	}
	if pv := share(users.LevelTerminal); !powerBlocked(pv) {
		t.Errorf("a raise of the share: %q", pv.Blocked)
	}
	for _, level := range []string{users.LevelRead, ""} {
		if pv := share(level); pv.Blocked != "" || pv.Power {
			t.Errorf("lowering the share to %q is refused: %q", level, pv.Blocked)
		}
	}
	w.grantsDrop(t, "apps/x")
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "cap:open-links", Role: "writer"})
	if pv := share(users.LevelTerminal); pv.Blocked != "" {
		t.Errorf("a share of a tile with cap:open-links only: %q", pv.Blocked)
	}
}

// covers D246 (second review, 1) — every cap: xbind's code or its auth
// docs name is weighed in capClasses: a new one fails here until it is
// classified as power or not.
func TestEveryCapIsClassified(t *testing.T) {
	re := regexp.MustCompile(`cap:[a-z][a-z0-9-]*[a-z0-9]`)
	found := map[string]string{}
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "docs"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".md")) {
				return nil
			}
			b, _ := os.ReadFile(path)
			for _, m := range re.FindAllString(string(b), -1) {
				found[m] = path
			}
			return nil
		})
	}
	if len(found) < len(capClasses) {
		t.Fatalf("found %d caps, fewer than the %d classified: the scan is broken", len(found), len(capClasses))
	}
	for c, where := range found {
		if _, ok := capClasses[c]; !ok {
			t.Errorf("%s (named in %s) isn't weighed in capClasses: say whether code holding it holds power", c, where)
		}
	}
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
		"its code can call or read, through its grants and bindings: apps/pu, 2 other tile(s)", // apps/pg (an org's: her pattern stops there) and hidden/z
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
	if pv, err := accessErr(t, w, "bob", "apps/pg", "carol", ""); err != nil || !powerBlocked(pv) {
		t.Errorf("removing an exclusion on a tile with power: %v %q", err, pv.Blocked)
	}
	if err := w.b.Users.SetUserTile("carol", "apps/pg", users.LevelTerminal); err != nil {
		t.Fatal(err)
	}
	if pv, err := accessErr(t, w, "bob", "apps/pg", "carol", "write"); err != nil || pv.Power {
		t.Errorf("a lowering on a tile with power: %v %q", err, pv.Blocked)
	}
	w.grantsDrop(t, "apps/pg") // (apps/x calls apps/pg)
	// alice sees no accounts: removing an entry on her tile is a raise whose result isn't known
	pv, err = accessErr(t, w, "alice", "apps/x", "carol", "")
	if err != nil || !hasWarning(pv, "ENTRY REMOVED") || !hasWarning(pv, "TERMINAL") {
		t.Errorf("an unresolved removal: %v %v", err, pv.Warnings)
	}
	w.grantsAdd(t, registry.Grant{From: "apps/x", Target: "cap:containers", Role: "use"})
	if pv, err := accessErr(t, w, "alice", "apps/x", "carol", ""); err != nil || !powerBlocked(pv) {
		t.Errorf("an unresolved removal on a tile with power: %v %q", err, pv.Blocked)
	}
	if pv, err := accessErr(t, w, "alice", "apps/x", "carol", "none"); err != nil || pv.Power {
		t.Errorf("an exclusion on a tile with power: %v %q", err, pv.Blocked)
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
	plain, _ := accessErr(t, w, "carol", "apps/pu", "erin", "write")
	w.grantsAdd(t, registry.Grant{From: "apps/pu", Target: "cap:containers", Role: "use"})
	for _, op := range []delegation.ChangeOp{accessOp("apps/pu", "erin", "write"), {Kind: ChangeOrgShare, Args: mustJSON(map[string]string{"org": "fin", "tile": "apps/pu", "level": "write"})}} {
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
