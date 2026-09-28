package broker

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers F1 P5 P11 SC-ZERO — for tiles without a deployment record, the
// fabric's new functions take today's code path (09-fabric §10): Route
// answers what Policy answered, its refusal in the proxy's words; the single
// evaluation point resolveTarget gives grantedRole's role; allowRes and
// busFilter answer as they did. Over a generated matrix of grants, bindings,
// same-scope uses and scopes (rooted, plain-directory, workspace), with and
// without a policy ceiling, every principal kind, and both with the hooks
// unset and with a booted deployments plane installed as boot installs it.
// The references below are those functions as they stood before tile
// deployments, frozen here: a later change that moves a zero-state answer
// fails this test.
func TestZeroStateRoute(t *testing.T) {
	seen := map[string]int{}
	for seed := uint64(1); seed <= 24; seed++ {
		for _, ceiling := range []bool{false, true} {
			for _, plane := range []bool{false, true} {
				name := fmt.Sprintf("seed%d/ceiling=%v/plane=%v", seed, ceiling, plane)
				t.Run(name, func(t *testing.T) {
					b := zeroRouteBroker(t, seed, ceiling)
					if plane {
						installZeroPlane(t, b)
					}
					checkZeroStateRoute(t, b, seen)
				})
			}
		}
	}
	// The matrix must reach every outcome, or it proves nothing about one.
	for _, k := range []string{"route granted", "route refused", "route self", "route admin", "route delivery",
		"role custom", "role via binding", "allowRes ok", "allowRes refused", "bus delivered", "bus withheld"} {
		if seen[k] == 0 {
			t.Errorf("the generated matrix never reached %q", k)
		}
	}
}

func checkZeroStateRoute(t *testing.T, b *Broker, seen map[string]int) {
	comps := b.Reg.Components()
	var paths []string
	for _, c := range comps {
		paths = append(paths, c.Path)
	}
	slices.Sort(paths)
	principals := zeroRoutePrincipals(paths)

	// Route against Policy and the proxy's refusal.
	for _, p := range principals {
		for _, c := range comps {
			role, ok := todayPolicy(b, p, c)
			d := b.Route(p, c, "")
			switch {
			case p.Component == CronPrincipal || p.Component == BusPrincipal:
				seen["route delivery"]++
			case p.Component == c.Path:
				seen["route self"]++
			case p.IsAdmin():
				seen["route admin"]++
			case ok:
				seen["route granted"]++
			default:
				seen["route refused"]++
			}
			switch {
			case ok && (d.Deny != nil || d.Role != role || d.Deployment != util.MainDeployment):
				t.Errorf("Route(%s → %s) = %+v; Policy allowed at %q in main", describe(p), c.Path, d, role)
			case !ok && d.Deny == nil:
				t.Errorf("Route(%s → %s) = %+v; Policy refused", describe(p), c.Path, d)
			case !ok && d.Deny.Error() != todayProxyRefusal(p, c):
				t.Errorf("Route(%s → %s) refuses with %q; the proxy said %q", describe(p), c.Path, d.Deny, todayProxyRefusal(p, c))
			}
		}
	}

	// resolveTarget against grantedRole, for every caller (a person's "" too)
	// and every kind of target, main named or not.
	targets := append(slices.Clone(paths), zeroRouteResources...)
	targets = append(targets, "xbin", "xbin:users", "code", "code:w/a", "gpu:0", "cap:net-admin", "apps/none", "res:s1/nope")
	for _, caller := range append([]string{""}, paths...) {
		for _, target := range targets {
			role, ok := b.grantedRole(caller, target)
			if ok && role == "editor" {
				seen["role custom"]++
			}
			if _, bound := b.httpBindingRole(caller, target); ok && bound {
				seen["role via binding"]++
			}
			for _, dep := range []string{"", util.MainDeployment} {
				d := b.resolveTarget(caller, dep, target)
				switch {
				case ok && (d.Deny != nil || d.Role != role || d.Clamped):
					t.Errorf("resolveTarget(%q, %q, %s) = %+v; grantedRole gave %q", caller, dep, target, d, role)
				case !ok && (d.Deny == nil || d.Role != ""):
					t.Errorf("resolveTarget(%q, %q, %s) = %+v; grantedRole refused", caller, dep, target, d)
				}
			}
		}
	}

	// allowRes and busFilter against their references.
	for _, p := range principals {
		for _, target := range append(slices.Clone(zeroRouteResources), "res:s1/nope", "res:workspace/nope", "apps/x") {
			for _, want := range []string{"reader", "writer", "admin", "subscriber", "publisher"} {
				got, ref := b.allowRes(p, target, want), todayAllowRes(b, p, target, want)
				if ref == nil && p.Component != "" {
					seen["allowRes ok"]++
				} else if ref != nil && p.Component != "" {
					seen["allowRes refused"]++
				}
				if fmt.Sprint(got) != fmt.Sprint(ref) {
					t.Errorf("allowRes(%s, %s, %s) = %v; was %v", describe(p), target, want, got, ref)
				}
			}
		}
		for _, topic := range zeroRouteTopics {
			e := events.Event{Type: "bus", Topic: topic}
			got, ref := b.busFilter(p, e), todayBusFilter(b, p, e)
			if ref {
				seen["bus delivered"]++
			} else if p.Component != "" {
				seen["bus withheld"]++
			}
			if got != ref {
				t.Errorf("busFilter(%s, %s) = %v; was %v", describe(p), topic, got, ref)
			}
		}
	}
}

// ---- the references: today's functions, frozen ----

// todayPolicy is Broker.Policy as it stood before tile deployments.
func todayPolicy(b *Broker, p auth.Principal, target *registry.Component) (string, bool) {
	if p.IsAdmin() {
		return "admin", true
	}
	if p.Component == target.Path {
		return "admin", true // element is admin of itself
	}
	if p.Component == CronPrincipal || p.Component == BusPrincipal {
		return p.Role, true // role bound at registration, always self-targeted (cron.go, bussubs.go)
	}
	role, ok := b.grantedRole(p.Component, target.Path)
	return role, ok
}

// todayProxyRefusal is the proxy's 403 text for a call Policy refused
// (internal/proxy/proxy.go).
func todayProxyRefusal(p auth.Principal, comp *registry.Component) string {
	return fmt.Sprintf(
		"%s is not granted access to %s — declare it in \"uses\" and approve the grant (bx grant, or the grants panel)",
		p.From(), comp.Path)
}

// todayAllowRes is Broker.allowRes as it stood before tile deployments.
func todayAllowRes(b *Broker, p auth.Principal, target string, want string) error {
	rt, res, ok := b.parseRes(target)
	if !ok || res == nil {
		return fmt.Errorf("unknown resource %s", target)
	}
	if p.IsAdmin() {
		return nil
	}
	if p.Component == "" {
		return fmt.Errorf("unauthenticated")
	}
	role, ok := b.grantedRole(p.Component, rt.String())
	if !ok || !roleSatisfies(role, want, nil) {
		return fmt.Errorf("%s needs role %q on %s — declare it in \"uses\" and approve with bx grant", p.Component, want, rt)
	}
	return nil
}

// todayBusFilter is Broker.busFilter as it stood before tile deployments.
func todayBusFilter(b *Broker, p auth.Principal, e events.Event) bool {
	if p.Component == "" {
		return false
	}
	probe := e.Topic
	for strings.HasPrefix(probe, "res:") {
		if rt, res, found := b.parseRes(probe); found && res != nil && res.Type == "bus" {
			return todayAllowRes(b, p, rt.String(), "reader") == nil
		}
		i := strings.LastIndex(probe, "/")
		if i < 0 {
			break
		}
		probe = probe[:i]
	}
	return false
}

// ---- the generated workspaces ----

// zeroRouteTiles are the tiles of every generated workspace: two in the
// workspace scope, two in a plain-directory scope (s1), a scope-rooting tile
// and its member (s2/root), and two providers of the http service svc, one
// with a custom role that implies reader.
var zeroRouteTiles = []string{"w/a", "w/b", "s1/a", "s1/b", "s2/root", "s2/root/sub", "p/prov", "p/custom"}

// zeroRouteResources are every declared resource id.
var zeroRouteResources = []string{
	"res:s1/k", "res:s1/bus", "res:s2/root/k", "res:s2/root/bus", "res:workspace/wk", "res:workspace/wbus",
}

// zeroRouteTopics are bus topics on every bus, and topics that name no bus.
var zeroRouteTopics = []string{
	"res:s1/bus/orders/1", "res:s2/root/bus/x", "res:workspace/wbus/t", "res:s1/k/t", "res:s1/nope/t", "orders/1",
	"res:s1/bus",
}

var zeroRouteRoles = []string{"reader", "writer", "admin", "subscriber", "publisher", "editor"}

// zeroRouteBroker writes the workspace seed generates — grants, slot
// bindings and uses picked at random over every tile and resource — and
// opens a broker on it with a users store; ceiling adds a policy row whose
// call allow-list covers only part of the workspace.
func zeroRouteBroker(t *testing.T, seed uint64, ceiling bool) *Broker {
	t.Helper()
	rnd := rand.New(rand.NewPCG(seed, 0x5eed))
	pick := func(xs []string) string { return xs[rnd.IntN(len(xs))] }
	targets := append(slices.Clone(zeroRouteTiles), zeroRouteResources...)

	type grant struct {
		From   string `json:"from"`
		Target string `json:"target"`
		Role   string `json:"role"`
	}
	var grants []grant
	for range 3 + rnd.IntN(10) {
		grants = append(grants, grant{pick(zeroRouteTiles), pick(targets), pick(zeroRouteRoles)})
	}
	if rnd.IntN(2) == 0 {
		grants = append(grants, grant{pick(zeroRouteTiles), "xbin", "admin"})
	}
	bindings := map[string]map[string]any{}
	manifests := map[string]map[string]any{}
	for _, tile := range zeroRouteTiles {
		m := map[string]any{"runtime": "go",
			"expose": map[string]any{"roles": map[string]string{"reader": "r", "writer": "w", "admin": "a"}}}
		if rnd.IntN(2) == 0 {
			m["interfaces"] = map[string]any{"svc": map[string]any{"kind": "http", "service": "svc"}}
			if rnd.IntN(3) > 0 {
				bindings[tile] = map[string]any{"svc": map[string]string{"ref": pick([]string{"p/prov", "p/custom"})}}
			}
		}
		var uses []map[string]string
		for range rnd.IntN(4) {
			uses = append(uses, map[string]string{"target": pick(targets), "role": pick(zeroRouteRoles)})
		}
		if uses != nil {
			m["uses"] = uses
		}
		manifests[tile] = m
	}
	manifests["p/prov"]["provides"] = map[string]any{"svc": map[string]any{"kind": "http", "service": "svc", "role": pick(zeroRouteRoles[:3])}}
	manifests["p/custom"]["expose"] = map[string]any{"roles": map[string]string{"editor": "e", "reader": "r"},
		"implies": map[string][]string{"editor": {"reader"}}}
	manifests["p/custom"]["provides"] = map[string]any{"svc": map[string]any{"kind": "http", "service": "svc", "role": "editor"}}

	root := t.TempDir()
	write := func(rel string, v any) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := map[string]any{"resources": map[string]any{"k": map[string]string{"type": "kv"}, "bus": map[string]string{"type": "bus"}}}
	write("xbin.json", map[string]any{"schema": 1, "grants": grants, "bindings": bindings,
		"resources": map[string]any{"wk": map[string]string{"type": "kv"}, "wbus": map[string]string{"type": "bus"}}})
	write("s1/scope.json", res)
	write("s2/root/scope.json", res)
	for tile, m := range manifests {
		write(tile+"/xbin.json", m)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Users = st
	if ceiling {
		if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*/*", MayCall: []string{"p/*", "res:s1/*", "w/a"}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range reg.Components() {
		if c.ManifestErr != "" {
			t.Fatalf("seed %d: %s: %s", seed, c.Path, c.ManifestErr)
		}
	}
	return b
}

// installZeroPlane boots a deployments plane on b's workspace, which has no
// record, and installs its answers as boot does.
func installZeroPlane(t *testing.T, b *Broker) {
	t.Helper()
	dp := &deployments.Plane{Root: b.Reg.Root, Reg: b.Reg}
	if err := dp.Boot(); err != nil {
		t.Fatal(err)
	}
	b.DeploymentHooks = DeploymentHooks{DeploymentCodeRoot: dp.CodeRoot, DeploymentExists: dp.HasDeployment,
		AddressableDeployments: dp.Addressable, DeploymentSummary: dp.PrimarySummary}
	b.DeploymentAnswers = DeploymentAnswers{PrimaryOf: dp.Primary, DeploymentsOf: dp.DeploymentsOf,
		AddressedDeployment: dp.Addressed, RegistrationsActive: dp.RegistrationsActive,
		DeploymentEdges: dp.EdgePolicies, ReadDeploymentFile: dp.ReadDeploymentFile,
		WriteDeploymentFile: dp.WriteDeploymentFile, RemoveDeploymentFile: dp.RemoveDeploymentFile}
	if _, err := os.Stat(filepath.Join(b.Reg.Root, "data", "deployments")); err == nil {
		t.Fatal("booting the plane on a workspace without records wrote data/deployments")
	}
}

// zeroRoutePrincipals are every kind of caller: nobody, the owner, an admin,
// a person who isn't one, each tile's instance, frame and terminal
// principals (the terminal carrying its user), and the cron and bus
// deliveries.
func zeroRoutePrincipals(paths []string) []auth.Principal {
	ps := []auth.Principal{
		{},
		{Owner: true},
		{UserID: "ana", User: &users.User{ID: "ana", Role: users.RoleAdmin}, Via: "session"},
		{UserID: "bo", User: &users.User{ID: "bo", Role: users.RoleUser}, Via: "session"},
		{Component: CronPrincipal, Role: "writer", Via: "cron"},
		{Component: BusPrincipal, Role: "reader", Via: "bus"},
		{Component: CronPrincipal, Via: "cron"},
	}
	for _, path := range paths {
		ps = append(ps,
			auth.Principal{Component: path, Via: "instance"},
			auth.Principal{Component: path, Via: "frame"},
			auth.Principal{Component: path, Via: "terminal", UserID: "bo"},
		)
	}
	return ps
}

func describe(p auth.Principal) string {
	return fmt.Sprintf("{%s via %q}", p.From(), p.Via)
}
