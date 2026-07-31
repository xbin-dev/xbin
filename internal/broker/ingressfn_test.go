package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// ingressWorkspace: a blog (http expose), a game server (stream expose), a
// CMS (http expose, zone-delegated), and a traefik-shaped terminator tile.
func ingressBroker(t *testing.T) *Broker {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"schema":1,
		"bindings":{
			"apps/blog":  {"web": {"ref":"runtime","host":"blog.example.com"}},
			"apps/cms":   {"web": {"ref":"apps/traefik","zone":"*.sites.example.com"}},
			"apps/game":  {"game":{"ref":"runtime","listen":":2456"}},
			"apps/gw":    {"web": {"ref":"apps/traefik","host":"gw.example.com"}}
		},
		"ingressHosts":{"apps/cms":["a.sites.example.com","b.sites.example.com"]}}`)
	write("apps/blog/xbin.json", `{"runtime":"go",
		"exposes":{"web":{"kind":"http","paths":["/","/api/public/*"]}}}`)
	write("apps/cms/xbin.json", `{"runtime":"go",
		"exposes":{"web":{"kind":"http","paths":["/*"]}}}`)
	write("apps/gw/xbin.json", `{"runtime":"go",
		"exposes":{"web":{"kind":"http","paths":["/*"]}}}`)
	write("apps/game/xbin.json", `{"runtime":"go",
		"exposes":{"game":{"kind":"stream","proto":"udp","port":2456},
		           "rcon":{"kind":"stream","port":25575}}}`)
	write("apps/traefik/xbin.json", `{"runtime":"go",
		"provides":{"public":{"kind":"ingress"}},
		"interfaces":{"net":{"kind":"net"}},
		"exposes":{"web":{"kind":"stream","port":80},"websecure":{"kind":"stream","port":443}}}`)
	write("apps/client/xbin.json", `{"runtime":"go",
		"interfaces":{"db":{"kind":"stream"}}}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	testUsers(t, b)
	return b
}

func TestIngressLookup(t *testing.T) {
	b := ingressBroker(t)

	// Exact host, runtime source.
	rt, ok := b.IngressLookup("runtime", "blog.example.com")
	if !ok || rt.Component != "apps/blog" || rt.Slot != "web" {
		t.Fatalf("blog lookup: %+v %v", rt, ok)
	}
	if len(rt.Paths) != 2 {
		t.Fatalf("route must carry the declared paths: %+v", rt)
	}
	// Source scoping: the same host does NOT resolve for a terminator tile.
	if _, ok := b.IngressLookup("apps/traefik", "blog.example.com"); ok {
		t.Fatal("a terminator must not route hosts bound to runtime")
	}
	// Zone-registered host resolves for its terminator only.
	rt, ok = b.IngressLookup("apps/traefik", "a.sites.example.com")
	if !ok || rt.Component != "apps/cms" || rt.Zone != "*.sites.example.com" {
		t.Fatalf("zone lookup: %+v %v", rt, ok)
	}
	// Unregistered zone member: covered by Published, but routes nowhere.
	if _, ok := b.IngressLookup("apps/traefik", "new.sites.example.com"); ok {
		t.Fatal("unregistered zone host must not route")
	}
	if !b.PublishedHost("new.sites.example.com") || !b.PublishedHost("blog.example.com") {
		t.Fatal("published predicate must cover exact hosts and zone members")
	}
	if b.PublishedHost("bank.example.com") {
		t.Fatal("unpublished host leaked into split horizon")
	}
	// Unknown host.
	if _, ok := b.IngressLookup("runtime", "nope.example.com"); ok {
		t.Fatal("unknown host must not route")
	}
}

func TestIngressStreamSpecsAndSources(t *testing.T) {
	b := ingressBroker(t)
	specs := b.IngressStreamSpecs()
	if len(specs) != 1 {
		t.Fatalf("one bound stream expose expected, got %+v", specs)
	}
	sp := specs[0]
	if sp.Component != "apps/game" || sp.Proto != "udp" || sp.Listen != ":2456" || sp.Port != 2456 {
		t.Fatalf("spec: %+v", sp)
	}
	srcs := b.IngressSources()
	if len(srcs) != 1 || srcs[0] != "apps/traefik" {
		t.Fatalf("terminators: %v", srcs)
	}
	// The terminator gets its forward door + env.
	c, _ := b.Reg.Component("apps/traefik")
	b.IngressSocket = func(source string) string { return "/run/igw-" + source + ".sock" }
	fwd := b.IngressFwdFor(c)
	if fwd[ingressFwdPort] != "unix:/run/igw-apps/traefik.sock" {
		t.Fatalf("forward map: %v", fwd)
	}
	env := strings.Join(b.EnvFor(c), "\n")
	if !strings.Contains(env, "XBIN_INGRESS_FORWARD_URL=http://10.0.2.2:8642") {
		t.Fatalf("terminator env missing forward URL:\n%s", env)
	}
}

func TestIngressPolicyCeiling(t *testing.T) {
	b := ingressBroker(t)
	st := b.Users
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/blog", Deny: []string{users.PolicyDenyIngress}}}); err != nil {
		t.Fatal(err)
	}
	// Evaluation: the existing binding goes inert.
	if _, ok := b.IngressLookup("runtime", "blog.example.com"); ok {
		t.Fatal("ingress-denied tile must not route")
	}
	if b.PublishedHost("blog.example.com") {
		t.Fatal("ingress-denied tile must vanish from split horizon")
	}
	// Approval: a new expose binding is refused with the row named.
	err := b.validateBinding("apps/blog", "web",
		registry.Binding{{Ref: "runtime", Host: "blog2.example.com"}})
	if err == nil || !strings.Contains(err.Error(), "denies ingress") {
		t.Fatalf("expose binding must be refused under an ingress deny, got %v", err)
	}
	// Other tiles unaffected.
	if _, ok := b.IngressLookup("apps/traefik", "a.sites.example.com"); !ok {
		t.Fatal("ceiling must only hit matching tiles")
	}
}

func TestValidateExposeBinding(t *testing.T) {
	b := ingressBroker(t)
	valid := func(comp, slot string, br registry.BindRef) error {
		return b.validateBinding(comp, slot, registry.Binding{br})
	}
	// Happy paths.
	if err := valid("apps/blog", "web", registry.BindRef{Ref: "apps/traefik", Host: "blog2.example.com"}); err != nil {
		t.Fatalf("terminator bind: %v", err)
	}
	if err := valid("apps/game", "rcon", registry.BindRef{Ref: "runtime", Listen: ":25575"}); err != nil {
		t.Fatalf("stream bind: %v", err)
	}
	cases := []struct {
		comp, slot string
		br         registry.BindRef
		wantErr    string
	}{
		{"apps/blog", "web", registry.BindRef{Ref: "runtime"}, "hostname authority"},
		{"apps/blog", "web", registry.BindRef{Ref: "runtime", Host: "x.com", Zone: "*.y.com"}, "not both"},
		{"apps/blog", "web", registry.BindRef{Ref: "runtime", Host: "Bad_Host!"}, "bad hostname"},
		{"apps/blog", "web", registry.BindRef{Ref: "runtime", Zone: "sites.example.com"}, "bad zone"},
		{"apps/blog", "web", registry.BindRef{Ref: "apps/game", Host: "x.example.com"}, "not an ingress terminator"},
		{"apps/blog", "web", registry.BindRef{Ref: "runtime", Host: "gw.example.com"}, "already bound"},
		{"apps/blog", "web", registry.BindRef{Ref: "runtime", Host: "a.sites.example.com"}, "registered by"},
		{"apps/blog", "web", registry.BindRef{Ref: "runtime", Listen: ":8080", Host: "x.example.com"}, "listen is for stream"},
		{"apps/game", "rcon", registry.BindRef{Ref: "apps/traefik"}, "bind to \"runtime\""},
		{"apps/game", "rcon", registry.BindRef{Ref: "runtime", Listen: "nonsense"}, "bad listen"},
		{"apps/game", "rcon", registry.BindRef{Ref: "runtime", Host: "x.example.com"}, "host/zone are for http"},
	}
	for i, c := range cases {
		err := valid(c.comp, c.slot, c.br)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("case %d (%s.%s %+v): got %v, want %q", i, c.comp, c.slot, c.br, err, c.wantErr)
		}
	}
	// Host-port collisions are caught against PERSISTED bindings: bind
	// traefik's :80 leg, then a second tcp expose on the same host port.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings["apps/traefik"] = map[string]registry.Binding{
			"web": {{Ref: "runtime", Listen: ":8080"}},
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := valid("apps/game", "rcon", registry.BindRef{Ref: "runtime", Listen: ":8080"})
	if err == nil || !strings.Contains(err.Error(), "already taken") {
		t.Fatalf("host-port collision must be refused: %v", err)
	}
}

// A stream interface binds a sibling's exposed tcp port; the requester gets
// a gateway address and the provider is dial-reachable.
func TestStreamInterfaceBinding(t *testing.T) {
	b := ingressBroker(t)
	if err := b.validateBinding("apps/client", "db", registry.BindTo("apps/traefik#web")); err != nil {
		t.Fatalf("stream iface bind: %v", err)
	}
	// UDP targets are refused (tcp-only v1).
	if err := b.validateBinding("apps/client", "db", registry.BindTo("apps/game#game")); err == nil || !strings.Contains(err.Error(), "tcp-only") {
		t.Fatalf("udp stream iface must be refused: %v", err)
	}
	// Unbound slot: no env, no fwd, no ingress-net.
	c, _ := b.Reg.Component("apps/client")
	if b.IngressNetFor(c) {
		t.Fatal("unbound stream iface must not force plumbing")
	}
	// Bind it and re-check resolution end to end.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings["apps/client"] = map[string]registry.Binding{"db": registry.BindTo("apps/traefik#web")}
	}); err != nil {
		t.Fatal(err)
	}
	if !b.IngressNetFor(c) {
		t.Fatal("bound stream iface forces relay plumbing")
	}
	fwd := b.IngressFwdFor(c)
	if fwd[streamIfacePortBase] != "stream:apps/traefik:80" {
		t.Fatalf("stream fwd map: %v", fwd)
	}
	env := strings.Join(b.EnvFor(c), "\n")
	if !strings.Contains(env, "XBIN_IFACE_DB_ADDR=10.0.2.2:20000") {
		t.Fatalf("stream iface env:\n%s", env)
	}
	// The dialed provider needs ingress plumbing too.
	prov, _ := b.Reg.Component("apps/traefik")
	if !b.IngressNetFor(prov) {
		t.Fatal("stream-dialed provider needs ingress plumbing")
	}
}

func TestLanIngress(t *testing.T) {
	b := ingressBroker(t)
	root := b.Reg.Root
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("apps/vpn/xbin.json", `{"runtime":"go",
		"provides":{"net":{"kind":"net"},"lan":{"kind":"lan-ingress"}},
		"uses":[{"target":"cap:net-admin","role":"writer"}]}`)
	write("apps/db/xbin.json", `{"runtime":"go",
		"interfaces":{"vpnlan":{"kind":"lan-ingress"}}}`)
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := b.validateBinding("apps/db", "vpnlan", registry.BindTo("apps/vpn")); err != nil {
		t.Fatalf("lan-ingress bind: %v", err)
	}
	if err := b.validateBinding("apps/db", "vpnlan", registry.BindTo("apps/game")); err == nil {
		t.Fatal("non-provider lan-ingress target must be refused")
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings["apps/db"] = map[string]registry.Binding{"vpnlan": registry.BindTo("apps/vpn")}
	}); err != nil {
		t.Fatal(err)
	}
	db, _ := b.Reg.Component("apps/db")
	links := b.NetLinksFor(db)
	if len(links) != 1 || links[0].Provider != "apps/vpn" || links[0].Addr != "10.43.0.2/30" {
		t.Fatalf("client links: %+v", links)
	}
	vpn, _ := b.Reg.Component("apps/vpn")
	roster := b.NetProviderRoster(vpn)
	if len(roster) != 1 || roster[0].Name != "apps/db#vpnlan" || roster[0].Addr != "10.43.0.1/30" {
		t.Fatalf("provider roster: %+v", roster)
	}
	env := strings.Join(b.EnvFor(db), "\n")
	if !strings.Contains(env, "XBIN_IFACE_VPNLAN_IP=10.43.0.2") {
		t.Fatalf("client env:\n%s", env)
	}
	penv := strings.Join(b.EnvFor(vpn), "\n")
	if !strings.Contains(penv, `"component":"apps/db"`) || !strings.Contains(penv, "XBIN_LAN_INGRESS=") {
		t.Fatalf("provider env:\n%s", penv)
	}
	// A net policy deny severs the leg at evaluation.
	if err := b.Users.SetPolicy([]users.PolicyRow{{Tiles: "apps/db", Deny: []string{users.PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if got := b.NetLinksFor(db); len(got) != 0 {
		t.Fatalf("net-denied tile must lose its lan-ingress leg: %+v", got)
	}
}
