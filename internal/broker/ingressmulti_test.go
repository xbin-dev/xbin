package broker

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// An exposed endpoint takes many routes (D79): several hostnames — through
// different sources, even — or several host ports, each validated and
// resolved on its own; exclusivity stays per hostname, zone and host port.
func TestExposeManyRoutes(t *testing.T) {
	b := ingressBroker(t)
	set := func(comp, slot string, bind registry.Binding) error {
		if err := b.validateBinding(comp, slot, bind); err != nil {
			return err
		}
		return b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
			if ws.Bindings[comp] == nil {
				ws.Bindings[comp] = map[string]registry.Binding{}
			}
			ws.Bindings[comp][slot] = bind
		})
	}
	// a webhost behind traefik under two hostnames, plus one direct via runtime
	blog := registry.Binding{
		{Ref: "runtime", Host: "blog.example.com"},
		{Ref: "apps/traefik", Host: "shop.example.com"},
		{Ref: "apps/traefik", Host: "www.example.net"},
	}
	if err := set("apps/blog", "web", blog); err != nil {
		t.Fatalf("three routes on one slot: %v", err)
	}
	for host, source := range map[string]string{"blog.example.com": "runtime", "shop.example.com": "apps/traefik", "www.example.net": "apps/traefik"} {
		rt, ok := b.IngressLookup(source, host)
		if !ok || rt.Component != "apps/blog" || rt.Slot != "web" {
			t.Errorf("%s via %s: %+v %v", host, source, rt, ok)
		}
		other := "runtime"
		if source == "runtime" {
			other = "apps/traefik"
		}
		if _, ok := b.IngressLookup(other, host); ok {
			t.Errorf("%s resolved through %s, which it isn't bound to", host, other)
		}
		if !b.PublishedHost(host) {
			t.Errorf("%s not in the split horizon", host)
		}
	}
	viaTraefik := 0
	for _, rt := range b.IngressRoutes() {
		if rt.Component == "apps/blog" && rt.Source == "apps/traefik" {
			viaTraefik++
		}
	}
	if viaTraefik != 2 {
		t.Fatalf("traefik must be told both hostnames: %d", viaTraefik)
	}

	// refused: a hostname twice in the slot, one another slot holds, a
	// host port twice, a port another slot holds
	for _, c := range []struct {
		comp, slot string
		bind       registry.Binding
		want       string
	}{
		{"apps/blog", "web", registry.Binding{{Ref: "runtime", Host: "a.example.com"}, {Ref: "apps/traefik", Host: "a.example.com"}}, "given twice"},
		{"apps/blog", "web", registry.Binding{{Ref: "runtime", Host: "a.example.com"}, {Ref: "runtime", Host: "gw.example.com"}}, "already bound to apps/gw"},
		{"apps/game", "rcon", registry.Binding{{Ref: "runtime", Listen: ":2525"}, {Ref: "runtime", Listen: "127.0.0.1:2525"}}, "given twice"},
		{"apps/blog", "web", registry.Binding{{Ref: "apps/traefik", Zone: "*.x.example.com"}, {Ref: "runtime", Zone: "*.x.example.com"}}, "given twice"},
	} {
		if err := b.validateBinding(c.comp, c.slot, c.bind); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s.%s %+v: %v, want %q", c.comp, c.slot, c.bind, err, c.want)
		}
	}

	// many host ports into one tile port: a listener per route
	if err := set("apps/game", "rcon", registry.Binding{{Ref: "runtime", Listen: ":25575"}, {Ref: "runtime", Listen: ":2525"}}); err != nil {
		t.Fatalf("two listens: %v", err)
	}
	var listens []string
	for _, sp := range b.IngressStreamSpecs() {
		if sp.Slot == "rcon" {
			listens = append(listens, sp.Listen)
			if sp.Port != 25575 {
				t.Errorf("rcon listener targets port %d", sp.Port)
			}
		}
	}
	if strings.Join(listens, " ") != ":25575 :2525" {
		t.Fatalf("rcon listeners: %v", listens)
	}
	if err := b.validateBinding("apps/traefik", "web", registry.Binding{{Ref: "runtime", Listen: ":2525"}}); err == nil || !strings.Contains(err.Error(), "already taken by apps/game.rcon") {
		t.Fatalf("a port of the second route must stay exclusive: %v", err)
	}

	// overlapping zones on one slot: a registered host belongs to the most
	// specific, and resolves only through that zone's source
	if err := set("apps/cms", "web", registry.Binding{{Ref: "apps/traefik", Zone: "*.sites.example.com"}, {Ref: "runtime", Zone: "*.eu.sites.example.com"}}); err != nil {
		t.Fatalf("two zones: %v", err)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.IngressHosts["apps/cms"] = []string{"a.sites.example.com", "x.eu.sites.example.com"}
	}); err != nil {
		t.Fatal(err)
	}
	if rt, ok := b.IngressLookup("runtime", "x.eu.sites.example.com"); !ok || rt.Zone != "*.eu.sites.example.com" {
		t.Fatalf("the specific zone's host: %+v %v", rt, ok)
	}
	if _, ok := b.IngressLookup("apps/traefik", "x.eu.sites.example.com"); ok {
		t.Fatal("the broader zone's source must not also serve it")
	}
	if rt, ok := b.IngressLookup("apps/traefik", "a.sites.example.com"); !ok || rt.Zone != "*.sites.example.com" {
		t.Fatalf("the broad zone's host: %+v %v", rt, ok)
	}
	for _, rt := range b.IngressRoutes() {
		if rt.Host == "x.eu.sites.example.com" && rt.Source != "runtime" {
			t.Fatalf("route list and lookup disagree: %+v", rt)
		}
	}
	// the admin overview lists every route; the scalars keep the first
	ov := b.IngressOverview()
	raw, _ := json.Marshal(ov["exposes"])
	var slots []struct {
		Component, Slot, Source, Host string
		Routes                        []routeInfo
	}
	_ = json.Unmarshal(raw, &slots)
	for _, s := range slots {
		if s.Component == "apps/blog" && s.Slot == "web" && (len(s.Routes) != 3 || s.Source != "runtime" || s.Host != "blog.example.com") {
			t.Fatalf("overview row: %+v", s)
		}
	}
}

// POST /bindings {add:true} appends a route; DELETE naming a route removes
// just it; a plain POST still replaces and a plain DELETE still clears.
func TestBindingsAddAndRemoveRoute(t *testing.T) {
	b := ingressBroker(t)
	admin := auth.Principal{Owner: true}
	do := func(method, body string) (int, string) {
		r := httptest.NewRequest(method, "/bindings", strings.NewReader(body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), admin))
		w := httptest.NewRecorder()
		b.apiBindingSet(w, r)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	routes := func() []string {
		var out []string
		for _, br := range b.Reg.Workspace().Bindings["apps/blog"]["web"] {
			out = append(out, br.Ref+" "+br.Host)
		}
		return out
	}
	if c, body := do("POST", `{"component":"apps/blog","slot":"web","provider":"apps/traefik","host":"shop.example.com","add":true}`); c != 200 {
		t.Fatalf("add: %d %s", c, body)
	}
	if c, body := do("POST", `{"component":"apps/blog","slot":"web","provider":"apps/traefik","host":"Www.Example.net ","add":true}`); c != 200 {
		t.Fatalf("add 2: %d %s", c, body)
	}
	if got := strings.Join(routes(), ", "); got != "runtime blog.example.com, apps/traefik shop.example.com, apps/traefik www.example.net" {
		t.Fatalf("after two adds: %s", got)
	}
	if c, _ := do("POST", `{"component":"apps/blog","slot":"web","provider":"apps/traefik","host":"shop.example.com","add":true}`); c != 400 {
		t.Fatalf("adding a hostname twice: %d", c)
	}
	// GET /bindings lists the endpoint with every route and its sources
	lr := httptest.NewRequest("GET", "/bindings", nil)
	lr = lr.WithContext(auth.WithPrincipal(lr.Context(), admin))
	lw := httptest.NewRecorder()
	b.apiBindingsList(lw, lr)
	var list struct {
		Exposes []exposeSlot `json:"exposes"`
		Pending []struct{ Component, Slot string }
	}
	_ = json.Unmarshal(lw.Body.Bytes(), &list)
	found := false
	for _, e := range list.Exposes {
		if e.Component == "apps/blog" && e.Slot == "web" {
			found = len(e.Routes) == 3 && e.Approvable && len(e.Options) == 2 // runtime + apps/traefik
		}
	}
	if !found {
		t.Fatalf("/bindings exposes: %s", lw.Body.String())
	}
	for _, p := range list.Pending {
		if p.Component == "apps/blog" && p.Slot == "web" {
			t.Fatal("a bound endpoint must not come back as pending")
		}
	}
	if c, body := do("DELETE", `{"component":"apps/blog","slot":"web","provider":"apps/traefik","host":"shop.example.com"}`); c != 200 {
		t.Fatalf("remove one: %d %s", c, body)
	}
	if got := strings.Join(routes(), ", "); got != "runtime blog.example.com, apps/traefik www.example.net" {
		t.Fatalf("after removing one: %s", got)
	}
	if c, _ := do("DELETE", `{"component":"apps/blog","slot":"web","host":"nope.example.com"}`); c != 404 {
		t.Fatalf("removing a route that isn't there: %d", c)
	}
	if c, _ := do("POST", `{"component":"apps/blog","slot":"web","provider":"runtime","host":"only.example.com"}`); c != 200 {
		t.Fatal("replace")
	}
	if got := strings.Join(routes(), ", "); got != "runtime only.example.com" {
		t.Fatalf("a plain POST replaces: %s", got)
	}
	if c, _ := do("DELETE", `{"component":"apps/blog","slot":"web"}`); c != 200 || len(routes()) != 0 {
		t.Fatalf("a plain DELETE clears the slot: %d %v", c, routes())
	}
}

// The org-admin gate judges the change, not the slot: an org admin adds and
// removes its own terminator route beside a route a workspace admin wired
// through the runtime listener (which the org could never approve itself).
func TestOrgAdminRouteDelta(t *testing.T) {
	b, st := orgFixture(t) // sales: carol admin
	for rel, content := range map[string]string{
		"apps/shop/xbin.json": `{"runtime":"go","exposes":{"web":{"kind":"http","paths":["/"]}}}`,
		"apps/edge/xbin.json": `{"runtime":"go","provides":{"ingress":{"kind":"ingress"}}}`,
	} {
		p := filepath.Join(b.Reg.Root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	for tile, owner := range map[string]string{"apps/shop": "org:sales", "apps/edge": "org:sales"} {
		if err := st.SetOwner(tile, owner); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Bindings == nil {
			ws.Bindings = map[string]map[string]registry.Binding{}
		}
		ws.Bindings["apps/shop"] = map[string]registry.Binding{"web": {{Ref: "runtime", Host: "direct.example.com"}}}
	}); err != nil {
		t.Fatal(err)
	}
	carol := principalFor(t, st, "carol")
	do := func(method, body string) int {
		r := httptest.NewRequest(method, "/bindings", strings.NewReader(body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), carol))
		w := httptest.NewRecorder()
		b.apiBindingSet(w, r)
		return w.Code
	}
	if c := do("POST", `{"component":"apps/shop","slot":"web","provider":"apps/edge","host":"shop.example.com","add":true}`); c != 200 {
		t.Fatalf("carol adds her terminator's host beside the ws-admin route: %d", c)
	}
	if c := do("POST", `{"component":"apps/shop","slot":"web","provider":"runtime","host":"other.example.com","add":true}`); c != 403 {
		t.Fatalf("a runtime route is still not hers to add: %d", c)
	}
	if c := do("DELETE", `{"component":"apps/shop","slot":"web","provider":"apps/edge","host":"shop.example.com"}`); c != 200 {
		t.Fatalf("carol removes her route: %d", c)
	}
	if got := b.Reg.Workspace().Bindings["apps/shop"]["web"]; len(got) != 1 || got[0].Host != "direct.example.com" {
		t.Fatalf("the ws-admin route must stay: %+v", got)
	}
}
