package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/util"
)

// lifeM2Plane boots the deployments plane over b's workspace and installs
// the hooks and answers the M2 halves of the tile's life read, as boot does.
func lifeM2Plane(t *testing.T, b *Broker) *deployments.Plane {
	t.Helper()
	dp := &deployments.Plane{Root: b.Reg.Root, OwnerRef: b.Users.Owner}
	if err := dp.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	b.DeploymentHooks = DeploymentHooks{
		RewriteDeploymentOwner: dp.RewriteDeploymentOwner, ResetDeploymentState: dp.ResetDeploymentState,
		DeploymentLeftovers: dp.DeploymentLeftovers, DeploymentExists: dp.HasDeployment,
		AddressableDeployments: dp.Addressable, DeploymentSummary: dp.PrimarySummary,
	}
	b.DeploymentAnswers = DeploymentAnswers{PrimaryOf: dp.Primary, DeploymentsOf: dp.DeploymentsOf,
		AddressedDeployment: dp.Addressed, RegistrationsActive: dp.RegistrationsActive,
		DeploymentEdges: dp.EdgePolicies, ReadDeploymentFile: dp.ReadDeploymentFile,
		WriteDeploymentFile: dp.WriteDeploymentFile, RemoveDeploymentFile: dp.RemoveDeploymentFile}
	return dp
}

// lifeWrite writes a file of the workspace at root.
func lifeWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lifeDevState gives tile's deployment dev what its life leaves on disk
// beyond the record: a vault, a dormant cron job and a data namespace in
// the tile's scope, each through the writer its plane uses. main's own
// namespace metadata is written too, which is never a leftover.
func lifeDevState(t *testing.T, b *Broker, tile string) {
	t.Helper()
	if err := b.vaultWriteIn(tile, "dev", map[string]string{"API_KEY": "dev-key"}); err != nil {
		t.Fatalf("%s: dev's vault: %v", tile, err)
	}
	doc := depCronDoc{Schema: depFileSchema, Jobs: []depCronRow{{Name: "nightly", Schedule: "@daily", Path: "/tick", Role: "writer"}}}
	if err := b.writeDepFile(tile, "dev", depCronFile, doc, false); err != nil {
		t.Fatalf("%s: dev's cron file: %v", tile, err)
	}
	for _, dep := range []string{"dev", util.MainDeployment} {
		if err := b.updateNS(nsOf(tile, dep), true, func(m *nsMeta) { m.State = nsEmpty }); err != nil {
			t.Fatalf("%s: the %s namespace: %v", tile, dep, err)
		}
	}
}

// covers P29 T11 — the M2 half of the leftovers row (08-data §9.3): after a
// tile's removal, what its deployments beyond main left is on D82's refusal
// list beside the record and the checkpoint store (the M1 half, in
// deploy_life_m1_test.go): their vault files, their registration
// directories, and every data namespace beyond main whose scope is at or
// under the path, orphaned or not, each named. A non-owner can't create the
// path, through newTilePathOK or the create API; the path's owner is
// exempt, as today. Under a path, a tile is known by a file that names it
// (its record or a vault), and a vault whose tile doesn't hash to its key is
// never attributed. main's namespace metadata is no leftover. A tile the
// owner re-creates at the path doesn't take the old deployments up again:
// it has main alone and its old registrations are gone. A workspace where
// no tile opted in lists nothing and gains no deployment state.
func TestPathLeftoversIncludeDeploymentStateM2(t *testing.T) {
	b := deployBroker(t)
	b.AllowInsecureVault = true
	root := b.Reg.Root
	for _, tile := range []string{"apps/crm", "apps/suite/ledger"} {
		lifeWrite(t, root, tile+"/scope.json", `{"resources":{"leads":{"type":"kv"}}}`)
		lifeWrite(t, root, tile+"/xbin.json", `{}`)
		lifeWrite(t, root, tile+"/index.html", `<html></html>`)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	for _, tile := range []string{"apps/crm", "apps/suite/ledger"} {
		if err := b.Users.SetOwner(tile, "user:"+fxWriter); err != nil {
			t.Fatal(err)
		}
		vaultRecord(t, root, tile, "user:"+fxWriter, util.MainDeployment, false)
	}
	lifeRepo(t, root, "apps/crm", ".git")
	lifeM2Plane(t, b)
	for _, tile := range []string{"apps/crm", "apps/suite/ledger"} {
		lifeDevState(t, b, tile)
	}
	// Under apps/suite, a removed tile whose record is gone left a vault
	// that names it; a vault file whose tile doesn't hash to its key names
	// nothing. apps/regonly left a registration directory and nothing else,
	// apps/emptyreg an empty one.
	if err := b.vaultWriteIn("apps/suite/notes", "qa", map[string]string{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	lifeWrite(t, root, "data/vault/.deployments/"+util.TileKey("apps/elsewhere")+"/dev.json", `{"tile":"apps/suite/forged","plain":{}}`)
	lifeWrite(t, root, "data/deployments/"+util.TileKey("apps/regonly")+"/dev/cron.json", `{"schema":1,"jobs":[]}`)
	if err := os.MkdirAll(filepath.Join(root, "data", "deployments", util.TileKey("apps/emptyreg"), "dev"), 0o700); err != nil {
		t.Fatal(err) // an empty registration directory holds nothing to inherit
	}
	// The tiles are removed.
	for _, dir := range []string{"apps/crm", "apps/suite"} {
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(dir))); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	tom := deployPerson(t, b, fxTerm)

	crm := []string{`apps/crm's "dev" data`, "checkpoint store", "deployment record", "owner entry user:" + fxWriter,
		"registrations of apps/crm+dev", "vault secrets of apps/crm+dev"}
	t.Run("leftovers refuse a non-owner", func(t *testing.T) {
		for path, want := range map[string][]string{
			"apps/crm": crm,
			"apps/suite": {`apps/suite/ledger's "dev" data`, "registrations of apps/suite/ledger+dev",
				"vault secrets of apps/suite/ledger+dev", "vault secrets of apps/suite/notes+qa"},
			"apps/suite/ledger": {`apps/suite/ledger's "dev" data`, "deployment record", "owner entry user:" + fxWriter,
				"registrations of apps/suite/ledger+dev", "vault secrets of apps/suite/ledger+dev"},
			"apps/regonly": {"registrations of apps/regonly+dev"},
		} {
			if got := b.pathLeftovers(path, "user:"+fxTerm); !reflect.DeepEqual(got, want) {
				t.Errorf("pathLeftovers(%s) =\n  %q\nwant\n  %q", path, got, want)
			}
			ok, msg := b.newTilePathOK(path, "user:"+fxTerm)
			if ok || !strings.Contains(msg, "still carries state from a removed tile ("+strings.Join(want, "; ")+")") {
				t.Errorf("newTilePathOK(%s) = %v %q", path, ok, msg)
			}
		}
		w := call(t, b.apiCreate, tom, "POST", "/create", `{"path":"apps/crm"}`, nil)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "vault secrets of apps/crm+dev") ||
			!strings.Contains(w.Body.String(), `apps/crm's \"dev\" data`) {
			t.Fatalf("tom's create: %d %s", w.Code, w.Body.String())
		}
		lifeAbsent(t, "a refused creation's directory", filepath.Join(root, "apps", "crm"))
		for _, path := range []string{"apps/untouched", "apps/suite-x", "apps/cr", "apps/emptyreg"} {
			if got := b.pathLeftovers(path, "user:"+fxTerm); got != nil {
				t.Errorf("pathLeftovers(%s) = %q, want none", path, got)
			}
		}
	})

	t.Run("the path's owner is exempt", func(t *testing.T) {
		if got := b.pathLeftovers("apps/crm", "user:"+fxWriter); got != nil {
			t.Errorf("pathLeftovers for the owner = %q, want none", got)
		}
		if ok, msg := b.newTilePathOK("apps/crm", "user:"+fxWriter); !ok {
			t.Errorf("newTilePathOK for the owner: %q", msg)
		}
	})

	t.Run("the owner re-creates it", func(t *testing.T) {
		w := call(t, b.apiCreate, deployPerson(t, b, fxWriter), "POST", "/create", `{"path":"apps/crm"}`, nil)
		if w.Code != 200 {
			t.Fatalf("wes's create: %d %s", w.Code, w.Body.String())
		}
		if _, names := b.deploymentsOf("apps/crm"); !reflect.DeepEqual(names, []string{util.MainDeployment}) {
			t.Errorf("the new tile's deployments = %q, want main alone", names)
		}
		if b.hasDeployment("apps/crm", "dev") {
			t.Error("the new tile took the old tile's dev up again")
		}
		lifeAbsent(t, "the old dev's registrations", filepath.Join(root, "data", "deployments", util.TileKey("apps/crm"), "dev"))
		if got := b.DeploymentRegistrations("apps/crm", "dev"); len(got) != 0 {
			t.Errorf("the new tile lists dev's registrations %+v", got)
		}
	})

	t.Run("a workspace where no tile opted in", func(t *testing.T) {
		b := deployBroker(t)
		lifeM2Plane(t, b)
		for _, path := range []string{"apps/fresh", "apps", "apps/calendar"} {
			if got := b.deploymentDataLeftovers(path, func(p string) bool { return p == path || strings.HasPrefix(p, path+"/") }); got != nil {
				t.Errorf("deploymentDataLeftovers(%s) = %q, want none", path, got)
			}
		}
		w := call(t, b.apiCreate, deployPerson(t, b, fxTerm), "POST", "/create", `{"path":"apps/fresh"}`, nil)
		if w.Code != 200 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		for _, rel := range []string{"data/deployments", "data/checkpoints", "data/vault/.deployments", "data/resources-enc/.deployments"} {
			lifeAbsent(t, rel, filepath.Join(b.Reg.Root, filepath.FromSlash(rel)))
		}
	})
}

// covers P17 T3 — no new tile name holds '+' (11-contract §2.1; 12-compat
// §7.1): every creator is refused, admins, the root token, the tile's own
// owner and an element holding xbin included, ahead of the admin early
// return, with util.PlusNameRefusal's text, on every creation path (create,
// clone, template instantiate, builtin import, git import), whether or not
// any tile has deployments, and a refused creation writes nothing. A name
// without '+' answers exactly as before. A directory whose name already
// holds '+' keeps resolving (an exact match wins) and gets no deployments;
// the qualified URL still names the deployment.
func TestPlusReservedInNewTilePaths(t *testing.T) {
	b := deployBroker(t)
	root := b.Reg.Root
	if err := b.Users.SetOwner(fxCalendar, "user:"+fxWriter); err != nil {
		t.Fatal(err)
	}
	vaultRecord(t, root, fxCalendar, "user:"+fxWriter, util.MainDeployment, false) // main (primary) and dev
	vaultRecord(t, root, fxShop, "", "dev", false)                                 // dev (primary) and main
	dp := lifeM2Plane(t, b)

	ana, tom, wes := deployPerson(t, b, fxAdmin), deployPerson(t, b, fxTerm), deployPerson(t, b, fxWriter)
	creators := []struct {
		name  string
		p     auth.Principal
		owner string
	}{
		{"a workspace admin", ana, ""},
		{"the root token", auth.Principal{Owner: true}, ""},
		{"the tile's owner", wes, "user:" + fxWriter},
		{"a terminal-level user", tom, "user:" + fxTerm},
		{"an element holding xbin", auth.Principal{Component: fxConsole, Via: "instance"}, ""},
		{"an admin driving an element holding xbin", auth.Principal{Component: fxConsole, Via: "frame", UserID: fxAdmin}, ""},
	}
	plus := []string{
		"apps/calendar+dev",  // a deployment's URL
		"apps/calendar+main", // the primary's alias
		"/apps/shop+dev/",
		"apps/calendar+nope", // a name the tile doesn't have
		"apps/email+dev",     // a tile without a record
		"apps/calendar+Dev",  // a name outside the grammar
		"apps/shop+", "apps/+dev", "tools+x/lint", "apps/a+b+c",
	}

	t.Run("every creator is refused", func(t *testing.T) {
		for _, c := range creators {
			for _, path := range plus {
				want := util.PlusNameRefusal(path)
				if ok, msg := b.canCreateAt(c.p, path, c.owner); ok || msg != want || want == "" {
					t.Errorf("%s at %s: %v %q, want %q", c.name, path, ok, msg, want)
				}
			}
		}
	})

	// A workspace template and a builtin to instantiate, on disk and embedded.
	for rel, content := range map[string]string{
		"templates/tpl/xbin.json":  `{"template":{"title":"Tpl"}}`,
		"templates/tpl/index.html": "<html>tpl</html>",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	set, err := builtins.Load(fstest.MapFS{
		"hello/tile.json": {Data: []byte(`{"name":"hello"}`)},
		"hello/xbin.json": {Data: []byte(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	b.SetBuiltins(set)

	routes := []struct {
		name string
		h    http.HandlerFunc
		url  string
		body func(path string) string
	}{
		{"create", b.apiCreate, "/create", func(p string) string { return `{"path":"` + p + `"}` }},
		{"clone", b.apiClone, "/clone", func(p string) string { return `{"from":"` + fxCalendar + `","to":"` + p + `"}` }},
		{"template instantiate", b.apiTemplatesNew, "/templates/new", func(p string) string { return `{"source":"templates/tpl","path":"` + p + `"}` }},
		{"builtin import", b.apiBuiltinsImport, "/builtins/import", func(p string) string { return `{"name":"hello","path":"` + p + `"}` }},
		{"git import", b.apiGitImport, "/git/import", func(p string) string { return `{"url":"https://example.invalid/x.git","path":"` + p + `"}` }},
	}
	t.Run("every creation path refuses, writing nothing", func(t *testing.T) {
		for _, rt := range routes {
			for _, p := range []auth.Principal{ana, {Owner: true}, tom} {
				for _, path := range []string{"apps/new+x", "apps/calendar+dev", "tools+y/lint"} {
					w := call(t, rt.h, p, "POST", rt.url, rt.body(path), nil)
					var got struct {
						Error string `json:"error"`
					}
					_ = json.Unmarshal(w.Body.Bytes(), &got)
					if w.Code != 403 || got.Error != util.PlusNameRefusal(path) {
						t.Errorf("%s by %s at %s: %d %s", rt.name, p.From(), path, w.Code, w.Body.String())
					}
				}
			}
		}
		for _, rel := range []string{"apps/new+x", "apps/calendar+dev", "tools+y"} {
			lifeAbsent(t, "a refused creation's directory", filepath.Join(root, filepath.FromSlash(rel)))
		}
	})

	t.Run("a name without '+' answers as before", func(t *testing.T) {
		w := call(t, b.apiCreate, tom, "POST", "/create", `{"path":"apps/plain-two"}`, nil)
		var m map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		if want := []string{"files", "owner", "path"}; !reflect.DeepEqual(lifeKeys(m), want) {
			t.Errorf("answer keys %q, want today's %q", lifeKeys(m), want)
		}
	})

	t.Run("clone resets before its first Rescan", func(t *testing.T) {
		reset := b.ResetDeploymentState
		first := map[string]bool{} // path → registered at its first reset
		b.ResetDeploymentState = func(path string) error {
			if _, seen := first[path]; !seen {
				_, first[path] = b.Reg.Component(path)
			}
			return reset(path)
		}
		defer func() { b.ResetDeploymentState = reset }()
		w := call(t, b.apiClone, ana, "POST", "/clone", `{"from":"apps/plain-two","to":"apps/plain-three"}`, nil)
		var m map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil {
			t.Fatalf("clone: %d %s", w.Code, w.Body.String())
		}
		if want := []string{"from", "path", "pendingGrants", "rewritten"}; !reflect.DeepEqual(lifeKeys(m), want) {
			t.Errorf("clone's answer keys %q, want today's %q", lifeKeys(m), want)
		}
		if registered, reset := first["apps/plain-three"]; !reset || registered {
			t.Errorf("reset %v, already registered at its first reset %v; want a reset before the tree exists", reset, registered)
		}
	})

	t.Run("an existing '+' directory keeps resolving; the qualified URL names the deployment", func(t *testing.T) {
		legacy := filepath.Join(root, "apps", "calendar+old") // made before the rule, or by hand
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "xbin.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := b.Reg.Rescan(); err != nil {
			t.Fatal(err)
		}
		c, dep, qualified, rest, err := b.Reg.ResolveRef("apps/calendar+old/index.html", dp)
		if err != nil || c == nil || c.Path != "apps/calendar+old" || qualified || rest != "index.html" {
			t.Errorf("apps/calendar+old/index.html resolves to %v %q %v %q (%v), want the tile there", c, dep, qualified, rest, err)
		}
		c, dep, qualified, _, err = b.Reg.ResolveRef("apps/calendar+dev/", dp)
		if err != nil || c == nil || c.Path != fxCalendar || dep != "dev" || !qualified {
			t.Errorf("apps/calendar+dev resolves to %v %q %v (%v), want apps/calendar's dev", c, dep, qualified, err)
		}
	})

	t.Run("without deployment records '+' is refused too", func(t *testing.T) {
		b := deployBroker(t)
		lifeM2Plane(t, b)
		b.DeploymentHooks, b.DeploymentAnswers = DeploymentHooks{}, DeploymentAnswers{} // no plane at all
		for _, path := range []string{"apps/calendar+dev", "apps/calendar+main", "apps/x+y"} {
			if ok, msg := b.canCreateAt(deployPerson(t, b, fxAdmin), path, ""); ok || msg != util.PlusNameRefusal(path) {
				t.Errorf("canCreateAt(%s) without the plane: %v %q", path, ok, msg)
			}
		}
	})
}

// lifeCreateOwner is the owner ref apiCreate resolves for p with no owner
// asked.
func lifeCreateOwner(t *testing.T, b *Broker, p auth.Principal) string {
	t.Helper()
	owner, msg := b.resolveCreateOwner(p, "")
	if msg != "" {
		t.Fatalf("resolveCreateOwner(%s): %s", p.From(), msg)
	}
	return owner
}

// lifeKeys lists m's keys in order.
func lifeKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
