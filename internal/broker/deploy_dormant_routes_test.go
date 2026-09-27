package broker

// The interface instances and ingress hosts of deployments beyond main
// (dormantroutes.go), on the dormant registrations' fixture
// (deploy_dormant_test.go) with a provider of instances and its consumer,
// and a zone-delegated tile behind a terminator, added.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// The added tiles.
const (
	fxAccts   = "apps/accts"   // provides mail with instances
	fxMailer  = "apps/mailer"  // binds apps/accts#acme
	fxCMS     = "apps/cms"     // an http expose delegated the zone *.sites.example.com through apps/traefik
	fxGW      = "apps/gw"      // an http expose bound exactly to gw.sites.example.com
	fxTraefik = "apps/traefik" // an ingress terminator
)

// routesFx is the fixture, with what the side effects the tests watch for
// received: grants events, consumer restarts and ingress reconciles.
type routesFx struct {
	*dormantFx
	mu        sync.Mutex
	grants    []string
	restarts  []string
	reconcile int
}

// newRoutesFx opens the fixture; primaries names the added tiles that get a
// record (main and dev) and which of the two is their primary.
func newRoutesFx(t *testing.T, primaries map[string]string) *routesFx {
	t.Helper()
	b := deployBroker(t)
	f := &routesFx{dormantFx: &dormantFx{b: b, root: b.Reg.Root, cron: make(chan cronCall, 1024)}}
	for rel, content := range map[string]string{
		"apps/accts/xbin.json":  `{"runtime":"go","provides":{"mail":{"kind":"http","service":"mail","instances":true}}}`,
		"apps/mailer/xbin.json": `{"runtime":"go","interfaces":{"box":{"kind":"http","service":"mail"}}}`,
		"apps/cms/xbin.json":    `{"runtime":"go","exposes":{"web":{"kind":"http","paths":["/*"]}}}`,
		"apps/gw/xbin.json":     `{"runtime":"go","exposes":{"web":{"kind":"http","paths":["/*"]}}}`,
		"apps/traefik/xbin.json": `{"runtime":"go","provides":{"public":{"kind":"ingress"}},
			"exposes":{"web":{"kind":"stream","port":80}}}`,
	} {
		p := filepath.Join(f.root, filepath.FromSlash(rel))
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
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings[fxMailer] = map[string]registry.Binding{"box": {{Ref: fxAccts + "#acme"}}}
		ws.Bindings[fxCMS] = map[string]registry.Binding{"web": {{Ref: fxTraefik, Zone: "*.sites.example.com"}}}
		ws.Bindings[fxGW] = map[string]registry.Binding{"web": {{Ref: fxTraefik, Host: "gw.sites.example.com"}}}
	}); err != nil {
		t.Fatal(err)
	}
	for tile, primary := range primaries {
		dormantRecord(t, f.root, tile, primary, false)
	}
	f.installPlane(t, b)
	b.OnGrantChange = func(c string) { f.mu.Lock(); f.restarts = append(f.restarts, c); f.mu.Unlock() }
	b.OnIngressChange = func() { f.mu.Lock(); f.reconcile++; f.mu.Unlock() }
	ch, stop := b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "grants" })
	t.Cleanup(stop)
	go func() {
		for e := range ch {
			f.mu.Lock()
			f.grants = append(f.grants, e.Component)
			f.mu.Unlock()
		}
	}()
	return f
}

// effects drains what the side effects received so far. Grants events are
// published before the handler answers, and reach the subscriber a moment
// later: a marker event proves everything before it arrived.
func (f *routesFx) effects(t *testing.T) (grants, restarts []string, reconcile int) {
	t.Helper()
	f.b.Hub.Publish(events.Event{Type: "grants", Component: "marker"})
	for i := 0; ; i++ {
		f.mu.Lock()
		n := len(f.grants)
		last := n > 0 && f.grants[n-1] == "marker"
		f.mu.Unlock()
		if last {
			break
		}
		if i == 5000 {
			t.Fatal("the marker never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	grants, restarts, reconcile = f.grants[:len(f.grants)-1], f.restarts, f.reconcile
	f.grants, f.restarts, f.reconcile = nil, nil, 0
	return grants, restarts, reconcile
}

// the added tiles' principals
var (
	acctsMain   = auth.Principal{Component: fxAccts, Via: "instance"}
	acctsDev    = auth.Principal{Component: fxAccts, Via: "instance", Deployment: "dev"}
	cmsMain     = auth.Principal{Component: fxCMS, Via: "instance"}
	cmsDev      = auth.Principal{Component: fxCMS, Via: "instance", Deployment: "dev"}
	traefikMain = auth.Principal{Component: fxTraefik, Via: "instance"}
	traefikDev  = auth.Principal{Component: fxTraefik, Via: "instance", Deployment: "dev"}
)

// mailerURL is the URL apps/mailer's box slot resolves to ("" = none).
func mailerURL(b *Broker) string {
	eps := b.HTTPSlots(fxMailer)["box"].Endpoints
	if len(eps) == 0 {
		return ""
	}
	return eps[0].URL
}

// covers P13 T6 PO-9 SC-DORMANT — TestDormantRegistrations's
// interface-instance and ingress-host subtests
// (TestNonPrimaryIfaceInstancesNeverRouted,
// TestNonPrimaryIngressHostsNeverRouted): a non-primary deployment's
// PUT /iface-instances and PUT /ingress-hosts answer 200 with today's body
// plus dormant:true, into data/deployments/<TileKey>/dev/ in 11-contract
// §10.2's shapes, while the root xbin.json stays byte-identical; they send
// no grants event, restart no consumer and reconcile no ingress, and never
// route: prov#inst resolves to main's instance, a dev-only instance is
// neither offered nor bindable, a dev host routes nowhere and isn't listed;
// a dormant host is zone-validated but takes no part in conflict checks,
// either way; main beside a primary that isn't main is dormant in the root
// map; the panel lists each deployment's rows with dormant; a non-primary
// terminator reads no routes; a gone deployment's credential gets 404.
func TestDormantRegistrationsRouting(t *testing.T) {
	t.Run("iface-instances", func(t *testing.T) {
		f := newRoutesFx(t, map[string]string{fxAccts: util.MainDeployment})
		b := f.b
		rec := regCall(t, b.apiIfaceInstancesSet, acctsMain, "PUT", "/iface-instances", "",
			map[string]any{"instances": map[string]string{"acme": "/m/acme"}})
		if rec.Code != 200 || rec.Body.String() != `{"component":"apps/accts","instances":1}`+"\n" {
			t.Fatalf("main's instances: %d %s", rec.Code, rec.Body)
		}
		if g, r, _ := f.effects(t); !slices.Equal(g, []string{fxAccts}) || !slices.Equal(r, []string{fxMailer}) {
			t.Fatalf("main's side effects: grants %v, restarts %v", g, r)
		}
		before := f.mainStores(t)
		rec = regCall(t, b.apiIfaceInstancesSet, acctsDev, "PUT", "/iface-instances", "",
			map[string]any{"instances": map[string]string{"acme": "/d/acme", "beta": "/d/beta/"}})
		if rec.Code != 200 || rec.Body.String() != `{"component":"apps/accts","dormant":true,"instances":2}`+"\n" {
			t.Fatalf("dev's instances: %d %s", rec.Code, rec.Body)
		}
		f.sameStores(t, before)
		if g, r, _ := f.effects(t); len(g) != 0 || len(r) != 0 {
			t.Fatalf("dev's side effects: grants %v, restarts %v", g, r)
		}
		want := "{\n  \"schema\": 1,\n  \"instances\": {\n    \"acme\": \"/d/acme\",\n    \"beta\": \"/d/beta\"\n  }\n}\n"
		if got := string(readFile(t, f.depFile(fxAccts, "dev", "iface-instances.json"))); got != want {
			t.Errorf("dev's iface-instances.json:\n%s\nwant:\n%s", got, want)
		}

		// never routed: main's instance serves; dev's beta is neither offered nor bindable
		if got := mailerURL(b); got != "/api/apps/accts/m/acme" {
			t.Errorf("apps/mailer's box: %q, want main's instance", got)
		}
		c := mustComp(t, b, fxMailer)
		var ids []string
		for _, o := range b.bindOptions(fxMailer, c.Manifest.Interfaces["box"], true) {
			ids = append(ids, o.ID)
		}
		if !slices.Contains(ids, fxAccts+"#acme") || slices.Contains(ids, fxAccts+"#beta") {
			t.Errorf("bind options %v: want main's acme alone", ids)
		}
		if err := b.validateBinding(fxMailer, "box", registry.Binding{{Ref: fxAccts + "#beta"}}); err == nil || !strings.Contains(err.Error(), "unknown instance") {
			t.Errorf("binding dev's instance: %v", err)
		}
		if got := b.activeInstanceMap(b.Reg.Workspace().IfaceInstances)[fxAccts]; len(got) != 1 || got["acme"] != "/m/acme" {
			t.Errorf("the bindings listing's instances: %v", got)
		}

		// the panel's rows
		if regs := b.DeploymentRouteRegistrations(fxAccts, "dev"); len(regs) != 2 ||
			regs[0] != (deployments.Registration{Kind: deployments.RegIfaceInstance, Name: "acme", Prefix: "/d/acme", Dormant: true}) ||
			regs[1].Name != "beta" || !regs[1].Dormant {
			t.Errorf("dev's rows: %+v", regs)
		}
		if regs := b.DeploymentRouteRegistrations(fxAccts, util.MainDeployment); len(regs) != 1 || regs[0].Dormant || regs[0].Prefix != "/m/acme" {
			t.Errorf("main's rows: %+v", regs)
		}

		// clearing dev's removes its file; main's stays
		rec = regCall(t, b.apiIfaceInstancesSet, acctsDev, "PUT", "/iface-instances", "", map[string]any{"instances": map[string]string{}})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"dormant":true`) {
			t.Fatalf("dev clears: %d %s", rec.Code, rec.Body)
		}
		if _, err := os.Stat(f.depFile(fxAccts, "dev", "iface-instances.json")); !os.IsNotExist(err) {
			t.Errorf("dev's cleared file: %v", err)
		}
		f.sameStores(t, before)

		// a gone deployment
		gone := auth.Principal{Component: fxAccts, Via: "instance", Deployment: "gone"}
		if rec := regCall(t, b.apiIfaceInstancesSet, gone, "PUT", "/iface-instances", "",
			map[string]any{"instances": map[string]string{"x": "/x"}}); rec.Code != 404 {
			t.Errorf("a gone deployment: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("main-beside-another-primary", func(t *testing.T) {
		f := newRoutesFx(t, map[string]string{fxAccts: "dev"})
		b := f.b
		rec := regCall(t, b.apiIfaceInstancesSet, acctsMain, "PUT", "/iface-instances", "",
			map[string]any{"instances": map[string]string{"acme": "/m/acme"}})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"dormant":true`) {
			t.Fatalf("main beside a dev primary: %d %s", rec.Code, rec.Body)
		}
		if got := b.Reg.Workspace().IfaceInstances[fxAccts]["acme"]; got != "/m/acme" {
			t.Errorf("main's row belongs in the root map: %q", got)
		}
		if g, r, _ := f.effects(t); len(g) != 0 || len(r) != 0 {
			t.Errorf("main's dormant side effects: grants %v, restarts %v", g, r)
		}
		if got := mailerURL(b); got != "" {
			t.Errorf("apps/mailer's box resolved to main's dormant instance: %q", got)
		}
	})

	t.Run("ingress-hosts", func(t *testing.T) {
		f := newRoutesFx(t, map[string]string{fxCMS: util.MainDeployment, fxTraefik: util.MainDeployment})
		b := f.b
		rec := regCall(t, b.apiIngressHosts, cmsMain, "PUT", "/ingress-hosts", "", map[string]any{"hosts": []string{"a.sites.example.com"}})
		if rec.Code != 200 || rec.Body.String() != `{"component":"apps/cms","hosts":1}`+"\n" {
			t.Fatalf("main's hosts: %d %s", rec.Code, rec.Body)
		}
		if g, _, n := f.effects(t); !slices.Equal(g, []string{fxCMS}) || n != 1 {
			t.Fatalf("main's side effects: grants %v, reconciles %d", g, n)
		}
		before := f.mainStores(t)
		// gw.sites.example.com is bound exactly to apps/gw: a dormant host takes no part in conflicts
		rec = regCall(t, b.apiIngressHosts, cmsDev, "PUT", "/ingress-hosts", "",
			map[string]any{"hosts": []string{"x.sites.example.com", "gw.sites.example.com"}})
		if rec.Code != 200 || rec.Body.String() != `{"component":"apps/cms","dormant":true,"hosts":2}`+"\n" {
			t.Fatalf("dev's hosts: %d %s", rec.Code, rec.Body)
		}
		f.sameStores(t, before)
		if g, _, n := f.effects(t); len(g) != 0 || n != 0 {
			t.Fatalf("dev's side effects: grants %v, reconciles %d", g, n)
		}
		want := "{\n  \"schema\": 1,\n  \"hosts\": [\n    \"gw.sites.example.com\",\n    \"x.sites.example.com\"\n  ]\n}\n"
		if got := string(readFile(t, f.depFile(fxCMS, "dev", "ingress-hosts.json"))); got != want {
			t.Errorf("dev's ingress-hosts.json:\n%s\nwant:\n%s", got, want)
		}
		// zone-validated as today
		if rec := regCall(t, b.apiIngressHosts, cmsDev, "PUT", "/ingress-hosts", "", map[string]any{"hosts": []string{"bank.example.com"}}); rec.Code != 403 {
			t.Errorf("dev outside its zone: %d %s", rec.Code, rec.Body)
		}

		// never routed, never listed
		if _, ok := b.IngressLookup(fxTraefik, "x.sites.example.com"); ok {
			t.Error("dev's host routes")
		}
		if rt, ok := b.IngressLookup(fxTraefik, "a.sites.example.com"); !ok || rt.Component != fxCMS {
			t.Errorf("main's host: %+v %v", rt, ok)
		}
		for _, rt := range b.IngressRoutes() {
			if rt.Host == "x.sites.example.com" {
				t.Errorf("dev's host is listed: %+v", rt)
			}
		}
		if hosts := b.IngressOverview()["ingressHosts"].(map[string][]string)[fxCMS]; !slices.Equal(hosts, []string{"a.sites.example.com"}) {
			t.Errorf("the overview's hosts: %v", hosts)
		}
		// a dormant host blocks no one: main may take x, which dev holds dormant
		if rec := regCall(t, b.apiIngressHosts, cmsMain, "PUT", "/ingress-hosts", "",
			map[string]any{"hosts": []string{"a.sites.example.com", "x.sites.example.com"}}); rec.Code != 200 || strings.Contains(rec.Body.String(), "dormant") {
			t.Errorf("main takes dev's dormant host: %d %s", rec.Code, rec.Body)
		}
		// an active registration still conflicts
		if rec := regCall(t, b.apiIngressHosts, cmsMain, "PUT", "/ingress-hosts", "",
			map[string]any{"hosts": []string{"gw.sites.example.com"}}); rec.Code != 409 {
			t.Errorf("main onto an exact-bound host: %d %s", rec.Code, rec.Body)
		}

		// the panel's rows
		if regs := b.DeploymentRouteRegistrations(fxCMS, "dev"); len(regs) != 2 ||
			regs[0] != (deployments.Registration{Kind: deployments.RegIngressHost, Name: "gw.sites.example.com", Dormant: true}) {
			t.Errorf("dev's rows: %+v", regs)
		}

		// a non-primary terminator reads no routes; the primary's reads its own
		out := mustCode(t, regCall(t, b.apiIngressRoutes, traefikDev, "GET", "/ingress-routes", "", nil), 200, "dev's terminator")
		if routes, _ := out["routes"].([]any); routes == nil || len(routes) != 0 {
			t.Errorf("dev's terminator: %v", out)
		}
		out = mustCode(t, regCall(t, b.apiIngressRoutes, traefikMain, "GET", "/ingress-routes", "", nil), 200, "main's terminator")
		if routes, _ := out["routes"].([]any); len(routes) == 0 {
			t.Errorf("main's terminator: %v", out)
		}
		gone := auth.Principal{Component: fxTraefik, Via: "instance", Deployment: "gone"}
		mustCode(t, regCall(t, b.apiIngressRoutes, gone, "GET", "/ingress-routes", "", nil), 404, "a gone terminator")
	})
}

// covers P7 P13 — prov#inst resolves against the provider primary's active
// interface instance table (09-fabric §1, §8): main's root map while main is
// the primary, the primary's own file once another deployment is, and main's
// rows then lie dormant in the root map; the primary's registration, main
// or not, re-wires its consumers as today. The same holds for ingress hosts:
// each tile's primary's set routes.
func TestIfaceInstanceFollowsPrimary(t *testing.T) {
	f := newRoutesFx(t, map[string]string{fxAccts: util.MainDeployment, fxCMS: util.MainDeployment})
	b := f.b
	mustCode(t, regCall(t, b.apiIfaceInstancesSet, acctsMain, "PUT", "/iface-instances", "",
		map[string]any{"instances": map[string]string{"acme": "/m/acme"}}), 200, "main's instances")
	mustCode(t, regCall(t, b.apiIfaceInstancesSet, acctsDev, "PUT", "/iface-instances", "",
		map[string]any{"instances": map[string]string{"acme": "/d/acme"}}), 200, "dev's instances")
	mustCode(t, regCall(t, b.apiIngressHosts, cmsMain, "PUT", "/ingress-hosts", "",
		map[string]any{"hosts": []string{"m.sites.example.com"}}), 200, "main's hosts")
	mustCode(t, regCall(t, b.apiIngressHosts, cmsDev, "PUT", "/ingress-hosts", "",
		map[string]any{"hosts": []string{"d.sites.example.com"}}), 200, "dev's hosts")
	f.effects(t)
	if got := mailerURL(b); got != "/api/apps/accts/m/acme" {
		t.Fatalf("main primary: %q", got)
	}

	// dev becomes the primary (the record, as a reassignment writes it)
	dormantRecord(t, f.root, fxAccts, "dev", false)
	dormantRecord(t, f.root, fxCMS, "dev", false)
	f.installPlane(t, b)
	if got := mailerURL(b); got != "/api/apps/accts/d/acme" {
		t.Errorf("dev primary: %q, want dev's instance", got)
	}
	if got := b.activeInstanceMap(b.Reg.Workspace().IfaceInstances)[fxAccts]["acme"]; got != "/d/acme" {
		t.Errorf("the bindings listing under a dev primary: %q", got)
	}
	if _, ok := b.IngressLookup(fxTraefik, "d.sites.example.com"); !ok {
		t.Error("dev's host doesn't route under a dev primary")
	}
	if _, ok := b.IngressLookup(fxTraefik, "m.sites.example.com"); ok {
		t.Error("main's host routes beside a dev primary")
	}
	if regs := b.DeploymentRouteRegistrations(fxAccts, util.MainDeployment); len(regs) != 1 || !regs[0].Dormant {
		t.Errorf("main's rows beside a dev primary: %+v", regs)
	}

	// the primary dev's registration is active: stored in its file, consumers re-wired
	rec := regCall(t, b.apiIfaceInstancesSet, acctsDev, "PUT", "/iface-instances", "",
		map[string]any{"instances": map[string]string{"acme": "/d2/acme"}})
	if rec.Code != 200 || rec.Body.String() != `{"component":"apps/accts","instances":1}`+"\n" {
		t.Fatalf("the primary dev's instances: %d %s", rec.Code, rec.Body)
	}
	if g, r, _ := f.effects(t); !slices.Equal(g, []string{fxAccts}) || !slices.Equal(r, []string{fxMailer}) {
		t.Errorf("the primary dev's side effects: grants %v, restarts %v", g, r)
	}
	if got := mailerURL(b); got != "/api/apps/accts/d2/acme" {
		t.Errorf("after the primary dev's registration: %q", got)
	}
	var ws struct {
		IfaceInstances map[string]map[string]string `json:"ifaceInstances"`
	}
	if err := json.Unmarshal(readFile(t, filepath.Join(f.root, "xbin.json")), &ws); err != nil {
		t.Fatal(err)
	}
	if got := ws.IfaceInstances[fxAccts]; len(got) != 1 || got["acme"] != "/m/acme" {
		t.Errorf("the root map holds main's alone: %v", got)
	}

	// and back: main primary again resolves the root map
	dormantRecord(t, f.root, fxAccts, util.MainDeployment, false)
	f.installPlane(t, b)
	if got := mailerURL(b); got != "/api/apps/accts/m/acme" {
		t.Errorf("main primary again: %q", got)
	}
}
