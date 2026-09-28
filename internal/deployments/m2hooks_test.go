package deployments

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/util"
)

var m2TileLimits = cgroup.Limits{MemMax: 2 << 30, PidsMax: 512, CPUWeight: 100}

// covers D119c F1 PO-8 SC-ZERO — every answer the plane gives the hooks for
// deployments beyond main is today's for a tile without a record, from a
// literal that never booted and from a plane booted on a workspace without
// records: main is the one deployment and the primary; every principal of
// the tile, a terminal session following the primary included, acts in
// main, and a credential naming another deployment names none; main's
// registrations are the active ones; no edge policy is stored; a
// deployment's limits are the tile's; a session follows the primary; and no
// registration file can be written, so nothing lands under data/.
func TestM2HooksZeroState(t *testing.T) {
	root := t.TempDir()
	for name, p := range map[string]*Plane{
		"literal": {TileLimits: m2TileLimits},
		"booted":  bootPlane(t, root, newOwners(map[string]string{})),
	} {
		t.Run(name, func(t *testing.T) {
			p.TileLimits = m2TileLimits
			before := snapshot(t, root)
			const tile = "apps/crm"
			if prim, names := p.DeploymentsOf(tile); prim != util.MainDeployment || !reflect.DeepEqual(names, []string{"main"}) {
				t.Errorf("DeploymentsOf = %q %v", prim, names)
			}
			for _, pr := range []auth.Principal{
				{},
				{Owner: true},
				{UserID: "ana", Via: "session"},
				{Component: tile, Via: "instance"},
				{Component: tile, Via: "frame"},
				{Component: tile, Via: "terminal", UserID: "ana"},
				{Component: tile, Via: "terminal", Deployment: "main"},
				{Component: tile, Via: "frame", Deployment: "main"},
				{Component: "apps/other", Via: "instance", Deployment: "dev"},
			} {
				if dep, err := p.Addressed(pr, tile); dep != util.MainDeployment || err != nil {
					t.Errorf("Addressed(%+v) = %q, %v; want main", pr, dep, err)
				}
			}
			for _, via := range []string{"instance", "frame", "terminal"} {
				_, err := p.Addressed(auth.Principal{Component: tile, Via: via, Deployment: "dev"}, tile)
				if !errors.Is(err, util.ErrNoDeployment) {
					t.Errorf("Addressed(%s bound to dev) = %v; want no such deployment", via, err)
				}
			}
			if fires, routes := p.RegistrationsActive(tile, "main"); !fires || !routes {
				t.Errorf("main's registrations: fires %v routes %v", fires, routes)
			}
			if fires, routes := p.RegistrationsActive(tile, "dev"); fires || routes {
				t.Errorf("dev's registrations: fires %v routes %v", fires, routes)
			}
			if e := p.EdgePolicies(tile); e != nil {
				t.Errorf("EdgePolicies = %v", e)
			}
			for _, dep := range []string{"main", "dev"} {
				if l := p.LimitsFor(tile, dep); l != m2TileLimits {
					t.Errorf("LimitsFor(%s) = %+v; want the tile's", dep, l)
				}
			}
			want := term.TileDeployments{Primary: "main", LiveReload: "main", Names: []string{"main"}}
			if got := p.TileDeployments(tile); !reflect.DeepEqual(got, want) {
				t.Errorf("TileDeployments = %+v", got)
			}
			if tg, err := term.ChooseTarget(tile, p.TileDeployments(tile), true, ""); tg != (term.Target{}) || err != nil {
				t.Errorf("a new session's target = %+v, %v; want following the primary", tg, err)
			}
			if err := p.WriteDeploymentFile(tile, "dev", "cron.json", []byte("{}")); !errors.Is(err, util.ErrNoDeployment) {
				t.Errorf("writing dev's cron.json: %v; want no such deployment", err)
			}
			if err := p.WriteDeploymentFile(tile, "main", "cron.json", []byte("{}")); err == nil {
				t.Error("main's cron.json was written: main's registrations stay in today's stores")
			}
			if _, err := p.ReadDeploymentFile(tile, "dev", "cron.json"); !errors.Is(err, fs.ErrNotExist) && name == "booted" {
				t.Errorf("reading dev's cron.json: %v; want not found", err)
			}
			if err := p.RemoveDeploymentFile(tile, "dev", "cron.json"); err != nil && name == "booted" {
				t.Errorf("removing dev's missing cron.json: %v", err)
			}
			if d := diffSnapshots(before, snapshot(t, root)); len(d) > 0 {
				t.Errorf("the answers wrote into the workspace: %v", d)
			}
		})
	}
}

// m2Record is a record for tile beyond main: main the primary, pinned and
// protected; dev followed by live reload, deliveries stored on (as an M2
// build wrote it), limits lowered; qa pinned, deliveries switched off; one
// edge policy stored.
func m2Record(tile string) map[string]any {
	doc := recordDoc(tile, "")
	doc["liveReload"], doc["lastLiveReload"], doc["protectedPrimary"] = "dev", "dev", true
	doc["edges"] = map[string]any{"slot:llm": "block", "grant:apps/calendar": "read"}
	doc["deployments"] = map[string]any{
		"main": map[string]any{"checkpoint": treeA},
		"dev": map[string]any{"checkpoint": nil, "deliveries": true,
			"limits": map[string]any{"memMiB": 512, "pids": 100}},
		"qa": map[string]any{"checkpoint": treeB, "deliveries": false, "limits": map[string]any{"memMiB": 1 << 20}},
	}
	return doc
}

// covers D127c D127d D127g D127h D127m D127n D127p D127s — the answers read the record: the
// deployments main first; each principal of the tile reaches its bound
// deployment (a removed one is a 404, a session following a protected
// primary or naming it is refused), anyone else the primary; cron and bus
// fire for every deployment but one whose deliveries a manager switched off
// (a stored true reads as on), while only the primary routes; the stored edge policy comes back as a copy; limits
// are lowered, never raised past the tile's; the target choice learns the
// record; and registration files live beside the record, only for
// deployments that exist, never for main.
func TestM2HooksFromRecord(t *testing.T) {
	root := t.TempDir()
	const tile = "apps/crm"
	writeRecordDoc(t, root, tile, m2Record(tile))
	p := bootPlane(t, root, newOwners(map[string]string{}))
	p.TileLimits = m2TileLimits
	if f := p.Lookup(tile); f.State != RecordActive {
		t.Fatalf("the fixture record doesn't govern the tile: %+v", f)
	}

	if prim, names := p.DeploymentsOf(tile); prim != "main" || !reflect.DeepEqual(names, []string{"main", "dev", "qa"}) {
		t.Errorf("DeploymentsOf = %q %v", prim, names)
	}
	for _, c := range []struct {
		pr   auth.Principal
		want string
		err  string // "" none, "404", "403"
	}{
		{auth.Principal{Component: tile, Via: "frame"}, "main", ""},
		{auth.Principal{Component: tile, Via: "frame", Deployment: "dev"}, "dev", ""},
		{auth.Principal{Component: tile, Via: "instance", Deployment: "qa"}, "qa", ""},
		{auth.Principal{Component: tile, Via: "instance", Deployment: "gone"}, "", "404"},
		{auth.Principal{Component: tile, Via: "terminal", UserID: "ana"}, "", "403"},
		{auth.Principal{Component: tile, Via: "terminal", Deployment: "main"}, "", "403"},
		{auth.Principal{Component: tile, Via: "terminal", Deployment: "dev"}, "dev", ""},
		{auth.Principal{Component: "apps/other", Via: "instance", Deployment: "dev"}, "main", ""},
		{auth.Principal{UserID: "ana", Via: "session"}, "main", ""},
	} {
		dep, err := p.Addressed(c.pr, tile)
		got := ""
		switch {
		case errors.Is(err, util.ErrNoDeployment):
			got = "404"
		case err != nil:
			got = "403"
			if !strings.Contains(err.Error(), "protected") {
				t.Errorf("Addressed(%+v) refuses with %q; want the protection named", c.pr, err)
			}
		}
		if dep != c.want || got != c.err {
			t.Errorf("Addressed(%+v) = %q, %v; want %q %s", c.pr, dep, err, c.want, c.err)
		}
	}
	for dep, want := range map[string][2]bool{"main": {true, true}, "dev": {true, false}, "qa": {false, false}, "gone": {false, false}} {
		if fires, routes := p.RegistrationsActive(tile, dep); fires != want[0] || routes != want[1] {
			t.Errorf("RegistrationsActive(%s) = %v %v; want %v", dep, fires, routes, want)
		}
	}
	edges := p.EdgePolicies(tile)
	if want := map[string]string{"slot:llm": "block", "grant:apps/calendar": "read"}; !reflect.DeepEqual(edges, want) {
		t.Errorf("EdgePolicies = %v", edges)
	}
	edges["slot:llm"] = "read"
	if p.EdgePolicies(tile)["slot:llm"] != "block" {
		t.Error("changing EdgePolicies' answer changed the record")
	}
	for dep, want := range map[string]cgroup.Limits{
		"main": m2TileLimits,
		"dev":  {MemMax: 512 << 20, PidsMax: 100, CPUWeight: 100},
		"qa":   m2TileLimits, // 1 TiB asked: never above the tile's ceiling
		"gone": m2TileLimits,
	} {
		if got := p.LimitsFor(tile, dep); got != want {
			t.Errorf("LimitsFor(%s) = %+v; want %+v", dep, got, want)
		}
	}
	td := p.TileDeployments(tile)
	if want := (term.TileDeployments{Record: true, Primary: "main", Protected: true, LiveReload: "dev",
		Names: []string{"main", "dev", "qa"}}); !reflect.DeepEqual(td, want) {
		t.Errorf("TileDeployments = %+v", td)
	}

	// The registration files: beside the record, for existing non-main
	// deployments and the files 11-contract §10.2 names.
	body := []byte(`{"schema":1,"jobs":[]}`)
	if err := p.WriteDeploymentFile(tile, "dev", "cron.json", body); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "data", "deployments", util.TileKey(tile), "dev", "cron.json")
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dev's cron.json at %s: %v", path, err)
	}
	if got, err := p.ReadDeploymentFile(tile, "dev", "cron.json"); err != nil || !bytes.Equal(got, body) {
		t.Errorf("reading it back: %q %v", got, err)
	}
	for _, bad := range []struct{ dep, file string }{
		{"main", "cron.json"}, {"gone", "cron.json"}, {"dev", "../dev.json"}, {"dev", "notes.txt"}, {"Dev", "cron.json"},
	} {
		if err := p.WriteDeploymentFile(tile, bad.dep, bad.file, body); err == nil {
			t.Errorf("wrote %s's %q", bad.dep, bad.file)
		}
	}
	if err := p.RemoveDeploymentFile(tile, "dev", "cron.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dev's emptied registration directory stayed: %v", err)
	}
	if _, err := os.Stat(recordPath(root, tile)); err != nil {
		t.Errorf("removing a registration file took the record: %v", err)
	}
}

// covers T9 — the requests of the operations beyond main decode as the
// handlers decode every body, strictly: an unknown field is refused; a
// limits patch tells an absent key (left as it is) from null (the override
// removed) from a number, refuses a key it doesn't know, and encodes only
// the keys set; a switch or protection without "on" is told apart from
// "on": false.
func TestM2RequestsDecodeStrictly(t *testing.T) {
	decode := func(body string, v any) error {
		d := json.NewDecoder(strings.NewReader(body))
		d.DisallowUnknownFields()
		return d.Decode(v)
	}
	var lr LimitsRequest
	if err := decode(`{"tile":"apps/crm","deployment":"dev","limits":{"memMiB":1024,"pids":null}}`, &lr); err != nil {
		t.Fatal(err)
	}
	keys := lr.Limits.Keys()
	if len(keys) != 2 || keys[LimitMemMiB] == nil || *keys[LimitMemMiB] != 1024 || keys[LimitPids] != nil {
		t.Errorf("limits keys = %v", keys)
	}
	if _, ok := keys[LimitDiskGiB]; ok {
		t.Error("an absent diskGiB reads as sent")
	}
	if b, err := json.Marshal(lr.Limits); err != nil || string(b) != `{"memMiB":1024,"pids":null}` {
		t.Errorf("the patch encodes as %s, %v", b, err)
	}
	for _, body := range []string{
		`{"tile":"a","limits":{"cpu":1}}`,
		`{"tile":"a","limits":{"memMiB":"lots"}}`,
		`{"tile":"a","deployment":"dev","extra":1}`,
	} {
		if err := decode(body, &LimitsRequest{}); err == nil {
			t.Errorf("%s decoded", body)
		}
	}
	var sw SwitchRequest
	if err := decode(`{"tile":"a","deployment":"dev"}`, &sw); err != nil || sw.On != nil {
		t.Errorf("a switch without on: %+v %v", sw, err)
	}
	var pr ProtectRequest
	if err := decode(`{"tile":"a","on":false}`, &pr); err != nil || pr.On == nil || *pr.On {
		t.Errorf("protect on:false: %+v %v", pr, err)
	}
	for body, v := range map[string]any{
		`{"tile":"a","deployment":"dev","from":"primary","data":"seed","attach":true,"confirm":"copy-data","seq":3,"dryRun":true}`: &AddRequest{},
		`{"tile":"a","deployment":"dev","confirm":"erase","seq":3}`:                                                                &RemoveRequest{},
		`{"tile":"a","from":"dev","to":"main","expect":"c:3f2a1c9","seq":3}`:                                                       &PromoteRequest{},
		`{"tile":"a","deployment":"dev","confirm":"data-stays","expect":"c:3f2a1c9"}`:                                              &PrimaryRequest{},
		`{"tile":"a","edge":"slot:llm","policy":"block"}`:                                                                          &EdgeRequest{},
		`{"tile":"a","deployment":"dev","confirm":"copy-data","stop":true}`:                                                        &SeedRequest{},
		`{"tile":"a","deployment":"dev","confirm":"erase-data","vault":true}`:                                                      &ResetRequest{},
		`{"tile":"a","deployment":"dev","keys":["K"]}`:                                                                             &VaultCopyRequest{},
		`{"tile":"a","deployment":"dev"}`:                                                                                          &BackupRequest{},
		`{"tile":"a","deployment":"dev","version":"v1","into":"dev","replace":true,"confirm":"erase-data"}`:                        &RestoreRequest{},
		`{"tile":"a","deployment":"dev","schedule":"","retention":3}`:                                                              &BackupScheduleRequest{},
		`{"tile":"a","deployment":"dev","job":"nightly"}`:                                                                          &RunNowRequest{},
		`{"tile":"a","deployment":"dev","seq":3}`:                                                                                  &AttachRequest{},
	} {
		if err := decode(body, v); err != nil {
			t.Errorf("%T: %v", v, err)
		}
		if err := decode(strings.TrimSuffix(body, "}")+`,"nope":1}`, v); err == nil {
			t.Errorf("%T took an unknown field", v)
		}
	}
	var bs BackupScheduleRequest
	if err := decode(`{"tile":"a","deployment":"dev","schedule":""}`, &bs); err != nil || bs.Schedule == nil || *bs.Schedule != "" {
		t.Errorf(`schedule "" (remove) reads as %+v, %v`, bs, err)
	}
}
