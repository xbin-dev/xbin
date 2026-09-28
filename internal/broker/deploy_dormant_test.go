package broker

// The dormant registrations of deployments beyond main (dormant.go): the
// broker fixture (deploy_fixture_test.go) with cron resources added, a real
// deployments plane booted over records for apps/calendar (main primary,
// dev beside it) and apps/shop (dev primary, main beside it), its answers
// installed as boot installs them, and fake cron and bus dispatches.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// cronCall is one tick or run the fake cron dispatch received.
type cronCall struct {
	p          auth.Principal
	comp, path string
}

// dormantFx is the fixture: the broker, the plane over its workspace, and
// what the fake dispatches received.
type dormantFx struct {
	b     *Broker
	dp    *deployments.Plane
	root  string
	cron  chan cronCall
	bus   chan busCall
	reply func(path string) (int, string) // the fake cron dispatch's answer; nil: 200 ok
}

var (
	calMain  = auth.Principal{Component: fxCalendar, Via: "instance"}
	calDev   = auth.Principal{Component: fxCalendar, Via: "instance", Deployment: "dev"}
	shopMain = auth.Principal{Component: fxShop, Via: "instance"}
	shopDev  = auth.Principal{Component: fxShop, Via: "instance", Deployment: "dev"}
	emailDev = auth.Principal{Component: fxEmail, Via: "instance", Deployment: "dev"}
)

// dormantRecord writes tile's deployment record: main and dev, primary
// following the work tree and the other pinned; deliveries is the other's
// switch: true leaves it at its default, on, and false stores it off, as a
// tile manager switches it (D127h, revised).
func dormantRecord(t *testing.T, root, tile, primary string, deliveries bool) {
	t.Helper()
	other := map[string]string{util.MainDeployment: "dev", "dev": util.MainDeployment}[primary]
	deps := map[string]any{
		primary: map[string]any{"checkpoint": nil, "created": "2026-09-27T10:12:03Z", "by": "user:ana"},
		other:   map[string]any{"checkpoint": lifeTree, "created": "2026-09-27T10:12:03Z", "by": "user:ana"},
	}
	if !deliveries {
		deps[other].(map[string]any)["deliveries"] = false
	}
	data, err := json.Marshal(map[string]any{
		"schema": 1, "tile": tile, "owner": "", "created": "2026-09-27T10:12:03Z", "seq": 2,
		"liveReload": primary, "lastLiveReload": primary, "primary": primary, "protectedPrimary": false,
		"nextDeploy": 2, "deployments": deps,
	})
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

// installPlane boots a plane over b's workspace and installs its answers as
// boot's stepBroker does, then the fake dispatches as stepProxy does.
func (f *dormantFx) installPlane(t *testing.T, b *Broker) {
	t.Helper()
	f.dp = &deployments.Plane{Root: f.root, OwnerRef: f.b.Users.Owner}
	if err := f.dp.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	dp := f.dp
	b.DeploymentHooks = DeploymentHooks{ResetDeploymentState: dp.ResetDeploymentState,
		DeploymentLeftovers: dp.DeploymentLeftovers, DeploymentExists: dp.HasDeployment,
		AddressableDeployments: dp.Addressable}
	b.DeploymentAnswers = DeploymentAnswers{PrimaryOf: dp.Primary, DeploymentsOf: dp.DeploymentsOf,
		AddressedDeployment: dp.Addressed, RegistrationsActive: dp.RegistrationsActive,
		DeploymentEdges: dp.EdgePolicies, ReadDeploymentFile: dp.ReadDeploymentFile,
		WriteDeploymentFile: dp.WriteDeploymentFile, RemoveDeploymentFile: dp.RemoveDeploymentFile}
	f.dispatch(b)
}

// dispatch installs the fake cron and bus dispatches into b.
func (f *dormantFx) dispatch(b *Broker) {
	b.SetDispatch(func(p auth.Principal, comp, path string) (int, string) {
		f.cron <- cronCall{p, comp, path}
		if f.reply != nil {
			return f.reply(path)
		}
		return 200, "ok"
	})
	f.bus = fakeBusDispatch(b, nil)
}

func newDormantFx(t *testing.T, calDeliveries bool) *dormantFx {
	t.Helper()
	b := deployBroker(t)
	f := &dormantFx{b: b, root: b.Reg.Root, cron: make(chan cronCall, 1024)}
	for rel, content := range map[string]string{
		"apps/calendar/scope.json": `{"resources":{
			"events":{"type":"kv"},"bus":{"type":"bus"},"files":{"type":"filesystem"},"ticks":{"type":"cron"}}}`,
		"apps/calendar/xbin.json": `{"runtime":"go",
			"expose":{"roles":{"reader":"read the calendar","writer":"edit events","admin":"configure"}},
			"uses":[{"target":"res:apps/calendar/events","role":"writer"},
			        {"target":"res:apps/calendar/bus","role":"writer"},
			        {"target":"res:apps/calendar/files","role":"writer"},
			        {"target":"res:apps/calendar/ticks","role":"writer"}]}`,
		"apps/shop/scope.json": `{"resources":{"orders":{"type":"kv"},"events":{"type":"bus"},"ticks":{"type":"cron"}}}`,
		"apps/shop/xbin.json": `{"runtime":"go","expose":{"roles":{"reader":"browse","writer":"order"}},
			"uses":[{"target":"res:apps/shop/orders","role":"writer"},{"target":"res:apps/shop/events","role":"writer"},
			        {"target":"res:apps/shop/ticks","role":"writer"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(f.root, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// apps/calendar may schedule on apps/shop's cron resource: a foreign one
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: fxCalendar, Target: "res:apps/shop/ticks", Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	dormantRecord(t, f.root, fxCalendar, util.MainDeployment, calDeliveries)
	dormantRecord(t, f.root, fxEmail, util.MainDeployment, false)
	dormantRecord(t, f.root, fxShop, "dev", false)
	f.installPlane(t, b)
	return f
}

// regCall calls a registration handler as p; name is the {name} path value.
func regCall(t *testing.T, h http.HandlerFunc, p auth.Principal, method, target, name string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		bts, _ := json.Marshal(body)
		rd = bytes.NewReader(bts)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, rd)
	if name != "" {
		req.SetPathValue("name", name)
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func mustCode(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) map[string]any {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: %d %s, want %d", what, rec.Code, rec.Body, want)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// tickAll runs every scheduled cron entry once, as the scheduler would.
func tickAll(b *Broker) {
	for _, e := range b.cron.sched.Entries() {
		e.Job.Run()
	}
}

// ticked drains the cron calls received so far.
func (f *dormantFx) ticked() []cronCall {
	var out []cronCall
	for {
		select {
		case c := <-f.cron:
			out = append(out, c)
		default:
			return out
		}
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return data
}

// depFile is where deployment dep of tile keeps file.
func (f *dormantFx) depFile(tile, dep, file string) string {
	return filepath.Join(f.root, "data", "deployments", util.TileKey(tile), dep, file)
}

// mainStores snapshots the stores an older xbind loads (12-compat §5.3).
func (f *dormantFx) mainStores(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, rel := range []string{"data/cron-jobs.json", "data/bus-subscriptions.json", "xbin.json"} {
		out[rel] = string(readFile(t, filepath.Join(f.root, filepath.FromSlash(rel))))
	}
	return out
}

func (f *dormantFx) sameStores(t *testing.T, before map[string]string) {
	t.Helper()
	for rel, want := range f.mainStores(t) {
		if before[rel] != want {
			t.Errorf("%s changed:\n got: %s\nwant: %s", rel, want, before[rel])
		}
	}
}

// noEdgePolicy removes the installed edge verdict (edgepolicy.go's, once it
// is built) for the test's duration: the state before any edge policy,
// which refuses every non-primary call to a foreign resource.
func noEdgePolicy(t *testing.T) {
	old := edgeVerdict
	edgeVerdict = nil
	t.Cleanup(func() { edgeVerdict = old })
}

// edgeAs installs an edge verdict answering every non-primary call with v
// (read or block) for the test's duration.
func edgeAs(t *testing.T, v string) {
	old := edgeVerdict
	edgeVerdict = func(_ *Broker, d Decision, caller, callerDep, target, role string) Decision {
		if v == "block" {
			d.Deny = fmt.Errorf("%s's deployment %s: edge grant:%s is block", caller, callerDep, target)
			return d
		}
		d.Role, d.Clamped = "reader", true
		return d
	}
	t.Cleanup(func() { edgeVerdict = old })
}

// covers D127h T6 PO-9 SC-DORMANT — dormant registrations
// (TestNonPrimaryCronDormant and TestNonPrimaryBusSubDormant are the cron
// and bus subtests): a non-primary deployment whose deliveries a tile
// manager switched off (TestRegistrationsActiveByDefault has the default):
// its cron job and bus
// subscription, named like main's, register with 200 and dormant into
// data/deployments/<TileKey>/dev/ in today's row shapes without component,
// while data/cron-jobs.json, data/bus-subscriptions.json and the root
// xbin.json stay byte-identical; each deployment lists only its own, with
// deployment and dormant, and main's rows keep today's bytes; a tick fires
// main's job alone and a publish delivers to main's subscription alone,
// counting dev's as dormant; a primary that isn't main fires, and main
// beside it is dormant. A registration on a foreign resource is stored
// dormant under read and refused under block (and refused while this xbind
// has no edge policy), never for an own-scope one; the cap is 64 per
// (tile, deployment). A component backup carries main's rows only, and a
// tile created at the path drops every deployment's files.
func TestDormantRegistrations(t *testing.T) {
	t.Run("cron", func(t *testing.T) {
		f := newDormantFx(t, false)
		b := f.b
		job := map[string]any{"name": "tick", "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": "/tick"}
		if rec := regCall(t, b.apiCronPut, calMain, "PUT", "/cron/jobs", "", job); rec.Code != 200 || rec.Body.String() != "{\"ok\":\"true\"}\n" {
			t.Fatalf("main's job: %d %q", rec.Code, rec.Body)
		}
		before := f.mainStores(t)
		job["path"] = "/dev-tick"
		out := mustCode(t, regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", job), 200, "dev's job")
		if out["dormant"] != true || out["ok"] != "true" {
			t.Errorf("dev's PUT answer %v, want ok and dormant", out)
		}
		f.sameStores(t, before)
		got := string(readFile(t, f.depFile(fxCalendar, "dev", "cron.json")))
		want := `{
  "schema": 1,
  "jobs": [
    {
      "name": "tick",
      "resource": "res:apps/calendar/ticks",
      "schedule": "@every 1h",
      "path": "/dev-tick",
      "role": "writer"
    }
  ]
}
`
		if got != want {
			t.Errorf("dev's cron.json:\n%s\nwant:\n%s", got, want)
		}

		// each deployment lists its own; main's rows are today's bytes
		rec := regCall(t, b.apiCronList, calMain, "GET", "/cron/jobs", "", nil)
		if want := `{"jobs":[{"name":"tick","resource":"res:apps/calendar/ticks","schedule":"@every 1h","component":"apps/calendar","path":"/tick","role":"writer"}]}` + "\n"; rec.Body.String() != want {
			t.Errorf("main's list:\n%s\nwant:\n%s", rec.Body, want)
		}
		rec = regCall(t, b.apiCronList, calDev, "GET", "/cron/jobs", "", nil)
		if want := `{"jobs":[{"name":"tick","resource":"res:apps/calendar/ticks","schedule":"@every 1h","component":"apps/calendar","path":"/dev-tick","role":"writer","deployment":"dev","dormant":true}]}` + "\n"; rec.Body.String() != want {
			t.Errorf("dev's list:\n%s\nwant:\n%s", rec.Body, want)
		}
		admin := deployPerson(t, b, fxAdmin)
		rec = regCall(t, b.apiCronList, admin, "GET", "/cron/jobs?deployment=dev", "", nil)
		if !strings.Contains(rec.Body.String(), `"/dev-tick"`) || strings.Contains(rec.Body.String(), `"/tick"`) ||
			!strings.HasPrefix(rec.Body.String(), `{"deployment":"dev","jobs":`) {
			t.Errorf("an admin's ?deployment=dev list: %s", rec.Body)
		}
		mustCode(t, regCall(t, b.apiCronList, calDev, "GET", "/cron/jobs?deployment=main", "", nil), 403, "dev naming main")

		// a tick fires main's job alone
		tickAll(b)
		calls := f.ticked()
		if len(calls) != 1 || calls[0].comp != fxCalendar || calls[0].path != "/tick" ||
			calls[0].p != (auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer"}) {
			t.Errorf("a tick dispatched %+v, want main's /tick alone", calls)
		}

		// apps/shop's primary is dev: dev's job fires, to dev; main's is dormant
		shopJob := map[string]any{"name": "t", "resource": "res:apps/shop/ticks", "schedule": "@every 1h", "path": "/m"}
		if out := mustCode(t, regCall(t, b.apiCronPut, shopMain, "PUT", "/cron/jobs", "", shopJob), 200, "shop main's job"); out["dormant"] != true {
			t.Errorf("main beside a dev primary: answer %v, want dormant", out)
		}
		shopJob["path"] = "/d"
		if rec := regCall(t, b.apiCronPut, shopDev, "PUT", "/cron/jobs", "", shopJob); rec.Code != 200 || rec.Body.String() != "{\"ok\":\"true\"}\n" {
			t.Fatalf("the primary dev's job: %d %s", rec.Code, rec.Body)
		}
		tickAll(b)
		var shop []cronCall
		for _, c := range f.ticked() {
			if c.comp == fxShop {
				shop = append(shop, c)
			}
		}
		if len(shop) != 1 || shop[0].path != "/d" || shop[0].p.Deployment != "dev" {
			t.Errorf("apps/shop's ticks: %+v, want dev's /d alone, to dev", shop)
		}
		if d := b.Route(shop[0].p, mustComp(t, b, fxShop), ""); d.Deny != nil || d.Deployment != "dev" || d.Role != "writer" {
			t.Errorf("dev's tick routes to %+v, want dev at writer", d)
		}

		// a foreign cron resource: no edge policy refuses; read stores dormant; block refuses
		foreign := map[string]any{"name": "f", "resource": "res:apps/shop/ticks", "schedule": "@every 1h", "path": "/f"}
		noEdgePolicy(t)
		rec = regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", foreign)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "edge policy") {
			t.Errorf("a foreign job without an edge policy: %d %s", rec.Code, rec.Body)
		}
		edgeAs(t, "block")
		rec = regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", foreign)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "is block") {
			t.Errorf("a foreign job under block: %d %s", rec.Code, rec.Body)
		}
		edgeAs(t, "read")
		if out := mustCode(t, regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", foreign), 200, "a foreign job under read"); out["dormant"] != true {
			t.Errorf("under read: %v, want dormant", out)
		}
		mustCode(t, regCall(t, b.apiCronPut, calMain, "PUT", "/cron/jobs", "", foreign), 200, "main's foreign job, as today")

		// delete: dev's own, never main's
		mustCode(t, regCall(t, b.apiCronDelete, calDev, "DELETE", "/cron/jobs/f", "f", nil), 200, "dev deletes its job")
		mustCode(t, regCall(t, b.apiCronDelete, calDev, "DELETE", "/cron/jobs/f", "f", nil), 404, "dev deletes it again")
		if _, ok := b.cron.jobs[fxCalendar+"\x00f"]; !ok {
			t.Error("dev's delete removed main's job of the same name")
		}
	})

	t.Run("bus", func(t *testing.T) {
		f := newDormantFx(t, false)
		b := f.b
		sub := map[string]any{"name": "s1", "resource": "res:apps/calendar/bus", "path": "/on"}
		if rec := busAPI(t, b.apiBusSubsPut, calMain, "PUT", "/bus/subscriptions", sub); rec.Code != 200 || rec.Body.String() != "{\"ok\":\"true\"}\n" {
			t.Fatalf("main's subscription: %d %s", rec.Code, rec.Body)
		}
		before := f.mainStores(t)
		sub["path"] = "/dev-on"
		if out := mustCode(t, busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", sub), 200, "dev's subscription"); out["dormant"] != true {
			t.Errorf("dev's PUT answer %v, want dormant", out)
		}
		f.sameStores(t, before)
		var doc map[string]any
		if err := json.Unmarshal(readFile(t, f.depFile(fxCalendar, "dev", "bus-subscriptions.json")), &doc); err != nil {
			t.Fatal(err)
		}
		if want := `map[schema:1 subscriptions:[map[name:s1 path:/dev-on resource:res:apps/calendar/bus role:writer]]]`; fmt.Sprint(doc) != want {
			t.Errorf("dev's bus-subscriptions.json: %v, want %s", doc, want)
		}

		// a publish in the primary namespace reaches main's alone: dev's own-scope
		// subscription reads dev's namespace
		b.bus.publish("res:apps/calendar/bus", "x", 1)
		c := recv(t, f.bus)
		if c.path != "/on" || c.p != (auth.Principal{Component: BusPrincipal, Via: "bus", Role: "writer"}) {
			t.Errorf("delivered %+v, want main's /on", c)
		}
		quiet(t, f.bus)
		// one in dev's namespace reaches no one: main's reads another, and dev's
		// is dormant, which counts it
		b.bus.publishIn("res:apps/calendar/bus", "dev", "x", 2)
		quiet(t, f.bus)
		rec := busAPI(t, b.apiBusSubsList, calDev, "GET", "/bus/subscriptions", nil)
		var list struct {
			Subscriptions []map[string]any `json:"subscriptions"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &list)
		if len(list.Subscriptions) != 1 || list.Subscriptions[0]["path"] != "/dev-on" || list.Subscriptions[0]["deployment"] != "dev" ||
			list.Subscriptions[0]["dormant"] != true || list.Subscriptions[0]["dormantEvents"] != 1.0 {
			t.Errorf("dev's list: %s", rec.Body)
		}
		rec = busAPI(t, b.apiBusSubsList, calMain, "GET", "/bus/subscriptions", nil)
		if strings.Contains(rec.Body.String(), "dev-on") || strings.Contains(rec.Body.String(), `"deployment"`) ||
			strings.Contains(rec.Body.String(), `"dormant`) {
			t.Errorf("main's list: %s", rec.Body)
		}

		// apps/shop's primary is dev: its own subscription reads its namespace, the primary's
		shopSub := map[string]any{"name": "o", "resource": "res:apps/shop/events", "path": "/o"}
		mustCode(t, busAPI(t, b.apiBusSubsPut, shopDev, "PUT", "/bus/subscriptions", shopSub), 200, "shop dev's subscription")
		shopSub["path"] = "/m"
		mustCode(t, busAPI(t, b.apiBusSubsPut, shopMain, "PUT", "/bus/subscriptions", shopSub), 200, "shop main's subscription")
		b.bus.publish("res:apps/shop/events", "y", 3)
		if c := recv(t, f.bus); c.comp != fxShop || c.path != "/o" || c.p.Deployment != "dev" {
			t.Errorf("apps/shop's publish delivered %+v, want dev's /o, to dev", c)
		}
		quiet(t, f.bus)

		// a foreign bus: apps/email reads apps/calendar's
		foreign := map[string]any{"name": "cal", "resource": "res:apps/calendar/bus", "path": "/cal"}
		noEdgePolicy(t)
		rec = busAPI(t, b.apiBusSubsPut, emailDev, "PUT", "/bus/subscriptions", foreign)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "edge policy") {
			t.Errorf("a foreign subscription without an edge policy: %d %s", rec.Code, rec.Body)
		}
		edgeAs(t, "block")
		if rec := busAPI(t, b.apiBusSubsPut, emailDev, "PUT", "/bus/subscriptions", foreign); rec.Code != 403 {
			t.Errorf("a foreign subscription under block: %d %s", rec.Code, rec.Body)
		}
		edgeAs(t, "read")
		if out := mustCode(t, busAPI(t, b.apiBusSubsPut, emailDev, "PUT", "/bus/subscriptions", foreign), 200, "under read"); out["dormant"] != true {
			t.Errorf("a foreign subscription under read: %v, want dormant", out)
		}

		// the cap: 64 per (tile, deployment); main's is its own
		for i := 2; i <= busSubsPerComp; i++ {
			s := map[string]any{"name": fmt.Sprintf("s%d", i), "resource": "res:apps/calendar/bus", "path": "/n"}
			mustCode(t, busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", s), 200, "dev's subscription "+fmt.Sprint(i))
		}
		over := map[string]any{"name": "over", "resource": "res:apps/calendar/bus", "path": "/n"}
		if rec := busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", over); rec.Code != 409 || !strings.Contains(rec.Body.String(), "limit") {
			t.Errorf("dev's 65th: %d %s", rec.Code, rec.Body)
		}
		mustCode(t, busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", sub), 200, "replacing one at the cap")
		mustCode(t, busAPI(t, b.apiBusSubsPut, calMain, "PUT", "/bus/subscriptions", over), 200, "main's second")
		mustCode(t, busAPI(t, b.apiBusSubsDelete, calDev, "DELETE", "/bus/subscriptions/s1", nil), 200, "dev deletes s1")
		if len(b.bus.forComponent(fxCalendar)) != 2 {
			t.Errorf("dev's delete touched main's: %+v", b.bus.forComponent(fxCalendar))
		}
	})

	t.Run("backup-manifest", func(t *testing.T) {
		f := newDormantFx(t, false)
		b := f.b
		job := map[string]any{"name": "tick", "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": "/tick"}
		sub := map[string]any{"name": "s1", "resource": "res:apps/calendar/bus", "path": "/on"}
		mustCode(t, regCall(t, b.apiCronPut, calMain, "PUT", "/cron/jobs", "", job), 200, "main's job")
		mustCode(t, busAPI(t, b.apiBusSubsPut, calMain, "PUT", "/bus/subscriptions", sub), 200, "main's subscription")
		job["name"], sub["name"] = "devtick", "devsub"
		mustCode(t, regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", job), 200, "dev's job")
		mustCode(t, busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", sub), 200, "dev's subscription")
		// Manifest.CronJobs and Manifest.BusSubs come from these (backup.go)
		if jobs := b.cronJobsFor(fxCalendar); len(jobs) != 1 || !strings.Contains(string(jobs[0]), `"name":"tick"`) {
			t.Errorf("the manifest's cron jobs: %s", jobs)
		}
		if subs := b.bus.forComponent(fxCalendar); len(subs) != 1 || subs[0].Name != "s1" {
			t.Errorf("the manifest's bus subscriptions: %+v", subs)
		}
		regs := b.DeploymentRegistrations(fxCalendar, "dev")
		if len(regs) != 2 || regs[0].Kind != deployments.RegCron || regs[0].Name != "devtick" || !regs[0].Dormant ||
			regs[1].Kind != deployments.RegBus || regs[1].Name != "devsub" || !regs[1].Dormant {
			t.Errorf("the panel's list for dev: %+v", regs)
		}
		if regs := b.DeploymentRegistrations(fxCalendar, util.MainDeployment); len(regs) != 2 || regs[0].Dormant || regs[1].Dormant {
			t.Errorf("the panel's list for main: %+v", regs)
		}
	})

	t.Run("creation-drops", func(t *testing.T) {
		f := newDormantFx(t, false)
		b := f.b
		job := map[string]any{"name": "tick", "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": "/tick"}
		sub := map[string]any{"name": "s1", "resource": "res:apps/calendar/bus", "path": "/on"}
		mustCode(t, regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", job), 200, "dev's job")
		mustCode(t, busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", sub), 200, "dev's subscription")
		// a deployment the record doesn't name (an older owner's), with every kind of file
		stray := filepath.Join(f.root, "data", "deployments", util.TileKey(fxCalendar), "old")
		if err := os.MkdirAll(stray, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range depRegistrationFiles {
			if err := os.WriteFile(filepath.Join(stray, name), []byte(`{"schema":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		b.assignOwner(fxCalendar, "")
		for _, p := range []string{filepath.Dir(f.depFile(fxCalendar, "dev", "cron.json")), stray, lifeRecordPath(f.root, fxCalendar)} {
			lifeAbsent(t, p, p)
		}
		if len(b.cron.dep) != 0 || len(b.bus.dep) != 0 {
			t.Errorf("the broker still holds %d jobs and %d subscriptions of the removed tile", len(b.cron.dep), len(b.bus.dep))
		}
		if _, err := os.Lstat(lifeRecordPath(f.root, fxShop)); err != nil {
			t.Errorf("another tile's record went too: %v", err)
		}
	})
}

// covers D127h (revised 2026-09-28) SC-DORMANT — a non-primary deployment's
// cron jobs and bus push subscriptions are active by default and reach it
// alone: dev's job and subscription register with 200 and no dormant, list
// without it (the state's registrations too), a tick reaches dev's handler
// as dev (Route sends it there) beside main's to main, and a publish in
// dev's namespace reaches dev's subscription alone, one in main's main's
// alone. Another scope's bus is read through the edge policy, asked again
// at every delivery: under read apps/email's dev gets apps/calendar's
// primary's events; under block the delivery fails and never reaches it.
// Interface instances and ingress hosts stay dormant
// (TestDormantRegistrationsRouting).
func TestRegistrationsActiveByDefault(t *testing.T) {
	f := newDormantFx(t, true)
	dormantRecord(t, f.root, fxEmail, util.MainDeployment, true)
	b := reopenBroker(t, f)
	f.b = b

	job := map[string]any{"name": "tick", "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": "/tick"}
	mustCode(t, regCall(t, b.apiCronPut, calMain, "PUT", "/cron/jobs", "", job), 200, "main's job")
	job["path"] = "/dev-tick"
	if out := mustCode(t, regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", job), 200, "dev's job"); out["dormant"] != nil || out["ok"] != "true" {
		t.Errorf("dev's job answered %v, want ok and active", out)
	}
	if rec := regCall(t, b.apiCronList, calDev, "GET", "/cron/jobs", "", nil); strings.Contains(rec.Body.String(), "dormant") ||
		!strings.Contains(rec.Body.String(), `"deployment":"dev"`) {
		t.Errorf("dev's list: %s", rec.Body)
	}
	tickAll(b)
	calls := f.ticked()
	got := map[string]string{}
	for _, c := range calls {
		got[c.path] = c.p.Deployment
		if d := b.Route(c.p, mustComp(t, b, fxCalendar), ""); d.Deny != nil || d.Deployment != cmp.Or(c.p.Deployment, util.MainDeployment) {
			t.Errorf("%s's tick routes to %+v", c.path, d)
		}
	}
	if len(calls) != 2 || got["/tick"] != "" || got["/dev-tick"] != "dev" {
		t.Errorf("ticks dispatched %+v, want main's /tick to main and dev's /dev-tick to dev", calls)
	}

	sub := map[string]any{"name": "s1", "resource": "res:apps/calendar/bus", "path": "/on"}
	mustCode(t, busAPI(t, b.apiBusSubsPut, calMain, "PUT", "/bus/subscriptions", sub), 200, "main's subscription")
	sub["path"] = "/dev-on"
	if out := mustCode(t, busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", sub), 200, "dev's subscription"); out["dormant"] != nil {
		t.Errorf("dev's subscription answered %v, want active", out)
	}
	b.bus.publishIn("res:apps/calendar/bus", "dev", "x", 1)
	if c := recv(t, f.bus); c.path != "/dev-on" || c.p != (auth.Principal{Component: BusPrincipal, Via: "bus", Role: "writer", Deployment: "dev"}) {
		t.Errorf("a publish in dev's namespace delivered %+v, want dev's /dev-on to dev", c)
	}
	quiet(t, f.bus)
	b.bus.publish("res:apps/calendar/bus", "x", 2)
	if c := recv(t, f.bus); c.path != "/on" || c.p.Deployment != "" {
		t.Errorf("a publish in main's namespace delivered %+v, want main's /on", c)
	}
	quiet(t, f.bus)
	for _, r := range b.DeploymentRegistrations(fxCalendar, "dev") {
		if r.Dormant {
			t.Errorf("dev's state lists %s %s dormant", r.Kind, r.Name)
		}
	}

	// another scope's bus, through the edge policy at every delivery
	edgeAs(t, "read")
	foreign := map[string]any{"name": "cal", "resource": "res:apps/calendar/bus", "path": "/cal"}
	if out := mustCode(t, busAPI(t, b.apiBusSubsPut, emailDev, "PUT", "/bus/subscriptions", foreign), 200, "under read"); out["dormant"] != nil {
		t.Errorf("a foreign subscription under read: %v, want active", out)
	}
	b.bus.publish("res:apps/calendar/bus", "x", 3)
	seen := map[string]string{}
	for range 2 {
		c := recv(t, f.bus)
		seen[c.comp+c.path] = c.p.Deployment
	}
	quiet(t, f.bus)
	if len(seen) != 2 || seen[fxCalendar+"/on"] != "" || seen[fxEmail+"/cal"] != "dev" {
		t.Errorf("under read the primary's publish delivered %v, want main's /on and email dev's /cal", seen)
	}
	b.bus.publishIn("res:apps/calendar/bus", "dev", "x", 4) // calendar dev's namespace: never a foreign reader's
	if c := recv(t, f.bus); c.comp != fxCalendar || c.path != "/dev-on" {
		t.Errorf("a publish in calendar dev's namespace delivered %+v", c)
	}
	quiet(t, f.bus)
	edgeAs(t, "block")
	b.bus.publish("res:apps/calendar/bus", "x", 5)
	if c := recv(t, f.bus); c.comp != fxCalendar || c.path != "/on" {
		t.Errorf("under block delivered %+v, want main's /on alone", c)
	}
	quiet(t, f.bus)
	var stats busSubStats
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b.bus.mu.Lock()
		stats = b.bus.dep[depKey(fxEmail, "dev", "cal")].stats
		b.bus.mu.Unlock()
		if stats.Failed > 0 {
			break
		}
	}
	if stats.Delivered != 1 || stats.Failed != 1 || !strings.Contains(stats.LastError, "block") {
		t.Errorf("email dev's counters %+v, want 1 delivered and the block's failure", stats)
	}
}

// covers D127h T6 SC-DORMANT — the deliveries switch, an off switch
// (TestDeliveriesSwitchManagerOnly): with deliveries on, a non-primary
// deployment's cron ticks and bus deliveries reach it (their principals name
// it, and Route sends them there), while its interface instances and ingress
// hosts still don't route; switching applies at once, an event queued
// before the switch went off counts as dormant, and a record without the
// switch (its default, on) fires after a restart. The switch is a tile manager's act in
// a person's own session: MayManageDeployments passes an admin and refuses a
// terminal-level user, the tile's terminal token driven by an admin, its
// instance, cron and bus principals, and an element granted xbin admin.
func TestDeliveriesSwitch(t *testing.T) {
	f := newDormantFx(t, false)
	b := f.b
	job := map[string]any{"name": "tick", "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": "/dev-tick"}
	sub := map[string]any{"name": "s1", "resource": "res:apps/calendar/bus", "path": "/dev-on"}
	mustCode(t, regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", job), 200, "dev's job")
	mustCode(t, busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", sub), 200, "dev's subscription")

	var on atomic.Bool
	real := b.RegistrationsActive
	b.RegistrationsActive = func(tile, dep string) (bool, bool) {
		if tile == fxCalendar && dep == "dev" {
			return on.Load(), false
		}
		return real(tile, dep)
	}
	tickAll(b)
	b.bus.publishIn("res:apps/calendar/bus", "dev", "x", 1)
	if calls := f.ticked(); len(calls) != 0 {
		t.Errorf("off: ticks dispatched %+v", calls)
	}
	quiet(t, f.bus)

	on.Store(true)
	tickAll(b)
	calls := f.ticked()
	wantP := auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer", Deployment: "dev"}
	if len(calls) != 1 || calls[0].p != wantP || calls[0].comp != fxCalendar || calls[0].path != "/dev-tick" {
		t.Fatalf("on: ticks dispatched %+v, want dev's /dev-tick as %+v", calls, wantP)
	}
	if d := b.Route(calls[0].p, mustComp(t, b, fxCalendar), ""); d.Deny != nil || d.Deployment != "dev" {
		t.Errorf("dev's tick routes to %+v, want dev", d)
	}
	if listed := regCall(t, b.apiCronList, calDev, "GET", "/cron/jobs", "", nil); strings.Contains(listed.Body.String(), "dormant") {
		t.Errorf("with deliveries on dev's job still lists dormant: %s", listed.Body)
	}
	b.bus.publishIn("res:apps/calendar/bus", "dev", "x", 2)
	c := recv(t, f.bus)
	if c.path != "/dev-on" || c.p != (auth.Principal{Component: BusPrincipal, Via: "bus", Role: "writer", Deployment: "dev"}) {
		t.Errorf("on: delivered %+v, want dev's /dev-on to dev", c)
	}
	if d := b.Route(c.p, mustComp(t, b, fxCalendar), ""); d.Deny != nil || d.Deployment != "dev" {
		t.Errorf("dev's delivery routes to %+v, want dev", d)
	}

	// switching off applies at once, to what is already queued too
	block := make(chan struct{})
	calls2 := fakeBusDispatch(b, block)
	b.bus.publishIn("res:apps/calendar/bus", "dev", "x", 3) // held in the dispatch
	recv(t, calls2)
	b.bus.publishIn("res:apps/calendar/bus", "dev", "x", 4) // queued behind it
	on.Store(false)
	close(block)
	quiet(t, calls2)
	var stats busSubStats
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		b.bus.mu.Lock()
		stats = b.bus.dep[depKey(fxCalendar, "dev", "s1")].stats
		b.bus.mu.Unlock()
		if stats.Delivered+stats.DormantEvents >= 4 {
			break
		}
	}
	if stats.Delivered != 2 || stats.DormantEvents != 2 { // one at publish (off), one queued
		t.Errorf("dev's counters %+v, want 2 delivered and 2 dormant", stats)
	}

	// the record's switch, through the real plane, after a restart
	b.RegistrationsActive = real
	dormantRecord(t, f.root, fxCalendar, util.MainDeployment, true)
	b2 := reopenBroker(t, f)
	if fires, routes := b2.registrationsActive(fxCalendar, "dev"); !fires || routes {
		t.Errorf("deliveries on: fires %v routes %v, want fires and never routes", fires, routes)
	}
	tickAll(b2)
	if calls := f.ticked(); len(calls) != 1 || calls[0].p.Deployment != "dev" {
		t.Errorf("after a restart with deliveries on: %+v", calls)
	}

	// who may switch
	tp := deployPerson(t, b, fxAdmin)
	for _, c := range []struct {
		who string
		p   auth.Principal
		ok  bool
	}{
		{"an admin", deployPerson(t, b, fxAdmin), true},
		{"a terminal-level user", deployPerson(t, b, fxTerm), false},
		{"the tile's terminal token, an admin driving", auth.Principal{Component: fxCalendar, Via: "terminal",
			UserID: tp.UserID, User: tp.User, Access: tp.Access}, false},
		{"the tile's instance", calDev, false},
		{"a cron principal", auth.Principal{Component: CronPrincipal, Via: "cron", Deployment: "dev"}, false},
		{"a bus principal", auth.Principal{Component: BusPrincipal, Via: "bus", Deployment: "dev"}, false},
		{"an element granted xbin admin", auth.Principal{Component: fxConsole, Via: "instance"}, false},
	} {
		if got := b.MayManageDeployments(c.p, fxCalendar); got != c.ok {
			t.Errorf("%s may switch deliveries: %v, want %v", c.who, got, c.ok)
		}
		plane := &deployments.Plane{IsAdmin: b.IsAdmin, MayManage: b.MayManageDeployments}
		_, err := plane.Authorize(c.p, deployments.OpDeliveries,
			deployments.Subject{Tile: fxCalendar, Deployment: "dev", Record: true, Seq: 2})
		if (err == nil) != c.ok {
			t.Errorf("%s: the plane's deliveries op answers %v, want allowed %v", c.who, err, c.ok)
		}
	}
}

// reopenBroker closes f's broker and opens another on its workspace, with
// the plane booted again and installed, as an xbind restart does.
func reopenBroker(t *testing.T, f *dormantFx) *Broker {
	t.Helper()
	f.b.Close()
	reg, err := registry.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	b.Users = f.b.Users
	f.installPlane(t, b)
	return b
}

// covers D127h T6 — run now (NP-09-9): one delivery of a non-primary
// deployment's job to it, as xbin/cron with the job's role, whatever its
// switch says, answering the handler's status (an error status included);
// 404 for an unknown deployment or job, 409 for the primary's jobs, a
// disabled tile or a run in flight, 502 when the backend can't start, 504
// when the caller stops waiting (the run stays in flight). It needs terminal
// level: a terminal-level user and the tile's terminal token pass, a writer,
// the tile's instance and frame and a cron principal don't.
func TestRunNow(t *testing.T) {
	f := newDormantFx(t, false)
	b := f.b
	ctx := context.Background()
	for name, path := range map[string]string{"nightly": "/nightly", "slow": "/slow", "broken": "/broken", "bad": "/bad"} {
		job := map[string]any{"name": name, "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": path, "role": "reader"}
		mustCode(t, regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", job), 200, "dev's job "+name)
	}
	mustCode(t, regCall(t, b.apiCronPut, calMain, "PUT", "/cron/jobs", "",
		map[string]any{"name": "tick", "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": "/tick"}), 200, "main's job")
	release := make(chan struct{})
	var slowStarted sync.WaitGroup
	slowStarted.Add(1)
	f.reply = func(path string) (int, string) {
		switch path {
		case "/slow":
			slowStarted.Done()
			<-release
		case "/broken":
			return 502, `{"docs":"/docs/protocol.md","error":"backend build failed","detail":"main.go:3: syntax error\nmore"}`
		case "/bad":
			return 500, "handler failed\nstack"
		}
		return 200, "done"
	}

	d, err := b.RunNow(ctx, fxCalendar, "dev", "nightly")
	if err != nil || d.Status != 200 || d.MS < 0 {
		t.Fatalf("run now: %+v %v", d, err)
	}
	calls := f.ticked()
	if len(calls) != 1 || calls[0].path != "/nightly" || calls[0].comp != fxCalendar ||
		calls[0].p != (auth.Principal{Component: CronPrincipal, Via: "cron", Role: "reader", Deployment: "dev"}) {
		t.Errorf("run now dispatched %+v, want one /nightly to dev as reader", calls)
	}
	tickAll(b) // dev's jobs are still dormant on schedule
	for _, c := range f.ticked() {
		if c.p.Deployment == "dev" {
			t.Errorf("a dormant job ticked: %+v", c)
		}
	}

	status := func(err error) int {
		var e *deployments.Error
		if !errors.As(err, &e) {
			return 0
		}
		return e.Status
	}
	for _, c := range []struct {
		what, dep, job string
		want           int
	}{
		{"an unknown deployment", "nope", "nightly", 404},
		{"an unknown job", "dev", "nope", 404},
		{"the primary's job", util.MainDeployment, "tick", 409},
		{"a backend that can't start", "dev", "broken", 502},
	} {
		if _, err := b.RunNow(ctx, fxCalendar, c.dep, c.job); status(err) != c.want {
			t.Errorf("%s: %v, want %d", c.what, err, c.want)
		}
	}
	if _, err := b.RunNow(ctx, fxCalendar, "dev", "broken"); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("a failed build names its first line: %v", err)
	}
	if d, err := b.RunNow(ctx, fxCalendar, "dev", "bad"); err != nil || d.Status != 500 {
		t.Errorf("a handler's 500: %+v %v, want the status", d, err)
	}
	f.ticked()

	// one run in flight per job; a caller that stops waiting gets 504
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := b.RunNow(cctx, fxCalendar, "dev", "slow"); done <- err }()
	slowStarted.Wait()
	if _, err := b.RunNow(ctx, fxCalendar, "dev", "slow"); status(err) != 409 {
		t.Errorf("a second run in flight: %v, want 409", err)
	}
	if d, err := b.RunNow(ctx, fxCalendar, "dev", "nightly"); err != nil || d.Status != 200 {
		t.Errorf("another job meanwhile: %+v %v", d, err)
	}
	cancel()
	if err := <-done; status(err) != 504 {
		t.Errorf("a caller that stops waiting: %v, want 504", err)
	}
	if _, err := b.RunNow(ctx, fxCalendar, "dev", "slow"); status(err) != 409 {
		t.Errorf("the abandoned run is still in flight: %v, want 409", err)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.cron.mu.Lock()
		busy := b.cron.running[depKey(fxCalendar, "dev", "slow")]
		b.cron.mu.Unlock()
		if !busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Lifecycle == nil {
			ws.Lifecycle = map[string]string{}
		}
		ws.Lifecycle[fxCalendar] = registry.StateDisabled
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RunNow(ctx, fxCalendar, "dev", "nightly"); status(err) != 409 {
		t.Errorf("a disabled tile: %v, want 409", err)
	}

	// who may run now: terminal level
	term := deployPerson(t, b, fxTerm)
	plane := &deployments.Plane{IsAdmin: b.IsAdmin, MayManage: b.MayManageDeployments}
	for _, c := range []struct {
		who string
		p   auth.Principal
		ok  bool
	}{
		{"a terminal-level user", term, true},
		{"the tile's terminal token, that user driving", auth.Principal{Component: fxCalendar, Via: "terminal",
			UserID: term.UserID, User: term.User, Access: term.Access}, true},
		{"a writer", deployPerson(t, b, fxWriter), false},
		{"the tile's instance", calDev, false},
		{"the tile's frame", auth.Principal{Component: fxCalendar, Via: "frame", Deployment: "dev"}, false},
		{"a cron principal", auth.Principal{Component: CronPrincipal, Via: "cron", Deployment: "dev"}, false},
	} {
		_, err := plane.Authorize(c.p, deployments.OpRunNow,
			deployments.Subject{Tile: fxCalendar, Deployment: "dev", Record: true, Seq: 2})
		if (err == nil) != c.ok {
			t.Errorf("%s: run now answers %v, want allowed %v", c.who, err, c.ok)
		}
	}
}

// covers PO-9 T11 — 12-compat PO-9's unit test: the registrations active
// without the deployments plane, as under a binary that never reads
// data/deployments, are exactly main's. A broker opened on the workspace
// without the plane's answers loads main's rows only, ticks and delivers
// main's alone, and rewrites today's stores from main's rows while every
// per-deployment file stays byte for byte what it was; back with the plane,
// dev's registrations load again from those files, dormant.
func TestOlderBinaryIgnoresDeploymentFiles(t *testing.T) {
	f := newDormantFx(t, false)
	b := f.b
	job := map[string]any{"name": "tick", "resource": "res:apps/calendar/ticks", "schedule": "@every 1h", "path": "/tick"}
	sub := map[string]any{"name": "s1", "resource": "res:apps/calendar/bus", "path": "/on"}
	mustCode(t, regCall(t, b.apiCronPut, calMain, "PUT", "/cron/jobs", "", job), 200, "main's job")
	mustCode(t, busAPI(t, b.apiBusSubsPut, calMain, "PUT", "/bus/subscriptions", sub), 200, "main's subscription")
	job["path"], sub["path"] = "/dev-tick", "/dev-on"
	mustCode(t, regCall(t, b.apiCronPut, calDev, "PUT", "/cron/jobs", "", job), 200, "dev's job")
	mustCode(t, busAPI(t, b.apiBusSubsPut, calDev, "PUT", "/bus/subscriptions", sub), 200, "dev's subscription")
	depFiles := map[string]string{}
	for _, file := range []string{"cron.json", "bus-subscriptions.json"} {
		p := f.depFile(fxCalendar, "dev", file)
		depFiles[p] = string(readFile(t, p))
		if depFiles[p] == "" {
			t.Fatalf("%s wasn't written", p)
		}
	}

	// without the plane
	b.Close()
	reg, err := registry.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	old, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	f.dispatch(old)
	if len(old.cron.jobs) != 1 || len(old.cron.dep) != 0 || len(old.bus.subs) != 1 || len(old.bus.dep) != 0 {
		t.Fatalf("loaded %d+%d jobs and %d+%d subscriptions, want main's one of each",
			len(old.cron.jobs), len(old.cron.dep), len(old.bus.subs), len(old.bus.dep))
	}
	tickAll(old)
	if calls := f.ticked(); len(calls) != 1 || calls[0].path != "/tick" || calls[0].p.Deployment != "" {
		t.Errorf("ticks: %+v, want main's /tick", calls)
	}
	old.bus.publish("res:apps/calendar/bus", "x", 1)
	if c := recv(t, f.bus); c.path != "/on" {
		t.Errorf("delivered %+v, want main's /on", c)
	}
	quiet(t, f.bus)
	mustCode(t, regCall(t, old.apiCronPut, calMain, "PUT", "/cron/jobs", "",
		map[string]any{"name": "tock", "resource": "res:apps/calendar/ticks", "schedule": "@every 2h", "path": "/tock"}), 200, "main's second job")
	mustCode(t, busAPI(t, old.apiBusSubsDelete, calMain, "DELETE", "/bus/subscriptions/s1", nil), 200, "main's unsubscribe")
	if got := string(readFile(t, filepath.Join(f.root, "data", "cron-jobs.json"))); strings.Contains(got, "dev-tick") || !strings.Contains(got, "/tock") {
		t.Errorf("data/cron-jobs.json: %s", got)
	}
	for p, want := range depFiles {
		if got := string(readFile(t, p)); got != want {
			t.Errorf("%s changed without the plane:\n%s\nwant:\n%s", p, got, want)
		}
	}
	old.Close()

	// back with the plane: dev's load again, dormant
	b2 := reopenBroker(t, f)
	if len(b2.cron.dep) != 1 || len(b2.bus.dep) != 1 {
		t.Fatalf("with the plane again: %d jobs and %d subscriptions of dev, want one of each", len(b2.cron.dep), len(b2.bus.dep))
	}
	rec := regCall(t, b2.apiCronList, calDev, "GET", "/cron/jobs", "", nil)
	if !strings.Contains(rec.Body.String(), `"path":"/dev-tick","role":"writer","deployment":"dev","dormant":true`) {
		t.Errorf("dev's list after the round trip: %s", rec.Body)
	}
	tickAll(b2)
	for _, c := range f.ticked() {
		if c.p.Deployment == "dev" {
			t.Errorf("dev's dormant job ticked after the round trip: %+v", c)
		}
	}
}
