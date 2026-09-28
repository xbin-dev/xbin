package broker

// The outbound edge policy of non-primary deployments (edgepolicy.go;
// 09-fabric §5): the broker fixture (deploy_fixture_test.go) with the edge
// shapes it lacks added (a custom role that implies reader, a mixed multi
// slot, net slots bound to the host, a host set, the internet and a
// provider tile, a lan-ingress slot, capability grants, a workspace-level
// filesystem), and a real deployments plane booted over records whose
// edges map is the stored policy, its answers installed as boot installs
// them.

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// The tiles the edge fixture adds.
const (
	efEditorProv = "apps/editor-prov" // provides docs at the custom role editor, which implies reader
	efEditorUser = "apps/editor-user" // binds apps/editor-prov's docs
	efMCPA       = "apps/mcp-a"       // provides mcp at writer
	efMCPB       = "apps/mcp-b"       // provides mcp at the custom role tool, which implies nothing
	efMixer      = "apps/mixer"       // a multi mcp slot bound to both
	efHostNet    = "apps/hostnet"     // net bound to the host builtin
	efSetNet     = "apps/setnet"      // net bound to a named set whose rules say host
	efWeb        = "apps/web"         // net bound to the internet
	efNetProv    = "apps/netprov"     // provides a net interface
	efNetClient  = "apps/netclient"   // net bound to apps/netprov: a splice
	efLAN        = "apps/lan-client"  // a lan-ingress slot bound to apps/netprov
	efGPU        = "apps/gpu-user"    // gpu:0, cap:net-admin, cap:containers, cap:open-links
	efBusPub     = "apps/bus-pub"     // publisher on apps/calendar's bus
)

// newEdgeBroker is deployBroker with the edge fixture's tiles and workspace.
func newEdgeBroker(t *testing.T) *Broker {
	t.Helper()
	b := deployBroker(t)
	root := b.Reg.Root
	files := map[string]string{
		"xbin.json": `{"schema":1,
			"resources":{"wsfiles":{"type":"filesystem"}},
			"grants":[
				{"from":"apps/email","target":"apps/calendar","role":"writer"},
				{"from":"apps/email","target":"res:apps/calendar/bus","role":"reader"},
				{"from":"apps/email","target":"res:apps/calendar/events","role":"writer"},
				{"from":"apps/email","target":"apps/shop","role":"admin"},
				{"from":"apps/email","target":"res:workspace/wsfiles","role":"writer"},
				{"from":"apps/email","target":"code:apps/calendar","role":"reader"},
				{"from":"apps/console","target":"xbin","role":"admin"},
				{"from":"apps/console","target":"xbin:users","role":"admin"},
				{"from":"apps/gpu-user","target":"gpu:0","role":"reader"},
				{"from":"apps/gpu-user","target":"cap:net-admin","role":"admin"},
				{"from":"apps/gpu-user","target":"cap:containers","role":"admin"},
				{"from":"apps/gpu-user","target":"cap:open-links","role":"admin"},
				{"from":"apps/bus-pub","target":"res:apps/calendar/bus","role":"publisher"}
			],
			"bindings":{
				"apps/webhooks":    {"agents":{"ref":"apps/agent"}},
				"apps/coder":       {"sandboxes":[{"ref":"apps/sbx-a"},{"ref":"apps/sbx-b"}]},
				"apps/chat":        {"llm":{"ref":"apps/llm-gw"}},
				"apps/pg-client":   {"db":{"ref":"apps/pg#pg"}},
				"apps/editor-user": {"docs":{"ref":"apps/editor-prov"}},
				"apps/mixer":       {"mcp":[{"ref":"apps/mcp-a"},{"ref":"apps/mcp-b"}]},
				"apps/hostnet":     {"net":{"ref":"host"}},
				"apps/setnet":      {"net":{"ref":"set:hostnet"}},
				"apps/web":         {"net":{"ref":"internet"}},
				"apps/netclient":   {"net":{"ref":"apps/netprov"}},
				"apps/lan-client":  {"lan":{"ref":"apps/netprov"}}
			}}`,
		"apps/editor-prov/xbin.json": `{"runtime":"go",
			"expose":{"roles":{"editor":"edit docs","reader":"read docs"},"implies":{"editor":["reader"]}},
			"provides":{"docs":{"kind":"http","service":"docs","role":"editor"}}}`,
		"apps/editor-user/xbin.json": `{"runtime":"go","interfaces":{"docs":{"kind":"http","service":"docs"}}}`,
		"apps/mcp-a/xbin.json": `{"runtime":"go","expose":{"roles":{"reader":"list","writer":"call tools"}},
			"provides":{"mcp":{"kind":"http","service":"mcp","role":"writer"}}}`,
		"apps/mcp-b/xbin.json": `{"runtime":"go","expose":{"roles":{"tool":"call tools","reader":"list"}},
			"provides":{"mcp":{"kind":"http","service":"mcp","role":"tool"}}}`,
		"apps/mixer/xbin.json":      `{"runtime":"go","interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true}}}`,
		"apps/hostnet/xbin.json":    `{"runtime":"go","interfaces":{"net":{"kind":"net"}}}`,
		"apps/setnet/xbin.json":     `{"runtime":"go","interfaces":{"net":{"kind":"net"}}}`,
		"apps/web/xbin.json":        `{"runtime":"go","interfaces":{"net":{"kind":"net"}}}`,
		"apps/netprov/xbin.json":    `{"runtime":"go","provides":{"egress":{"kind":"net"}}}`,
		"apps/netclient/xbin.json":  `{"runtime":"go","interfaces":{"net":{"kind":"net"}}}`,
		"apps/lan-client/xbin.json": `{"runtime":"go","interfaces":{"lan":{"kind":"lan-ingress"}}}`,
		"apps/gpu-user/xbin.json": `{"runtime":"go","uses":[{"target":"gpu:0","role":"reader"},
			{"target":"cap:net-admin","role":"admin"},{"target":"cap:containers","role":"admin"},{"target":"cap:open-links","role":"admin"}]}`,
		"apps/bus-pub/xbin.json": `{"runtime":"go","uses":[{"target":"res:apps/calendar/bus","role":"publisher"}]}`,
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Users.UpsertNetSet("hostnet", users.NetSet{Rules: []string{"host"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	return b
}

// edgeRec is one tile's deployment record for the fixture: main and dev
// (and any extra names), primary following the work tree and the others
// pinned; edges is the stored policy.
type edgeRec struct {
	tile, primary string
	edges         map[string]string
	extra         []string
	protected     bool
}

// edgePlane writes recs (every other record gone) and boots a deployments
// plane over b's workspace, installing its answers as stepBroker does.
func edgePlane(t *testing.T, b *Broker, recs ...edgeRec) *deployments.Plane {
	t.Helper()
	root := b.Reg.Root
	_ = os.RemoveAll(filepath.Join(root, "data", "deployments"))
	for _, r := range recs {
		deps := map[string]any{}
		for _, name := range append([]string{util.MainDeployment, "dev"}, r.extra...) {
			d := map[string]any{"checkpoint": lifeTree, "created": "2026-09-27T10:12:03Z", "by": "user:ana"}
			if name == r.primary && !r.protected { // a protected primary is pinned
				d["checkpoint"] = nil
			}
			deps[name] = d
		}
		doc := map[string]any{
			"schema": 1, "tile": r.tile, "owner": "", "created": "2026-09-27T10:12:03Z", "seq": 2,
			"liveReload": r.primary, "lastLiveReload": r.primary, "primary": r.primary, "protectedPrimary": r.protected,
			"nextDeploy": 2, "deployments": deps,
		}
		if r.protected {
			doc["liveReload"] = ""
		}
		if len(r.edges) > 0 {
			doc["edges"] = r.edges
		}
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		p := lifeRecordPath(root, r.tile)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dp := &deployments.Plane{Root: root, OwnerRef: b.Users.Owner}
	if err := dp.Boot(); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	for _, r := range recs {
		if _, names := dp.DeploymentsOf(r.tile); len(names) < 2 {
			t.Fatalf("the plane didn't load %s's record: %v", r.tile, names)
		}
	}
	b.DeploymentHooks = DeploymentHooks{ResetDeploymentState: dp.ResetDeploymentState,
		DeploymentLeftovers: dp.DeploymentLeftovers, DeploymentExists: dp.HasDeployment,
		AddressableDeployments: dp.Addressable}
	b.DeploymentAnswers = DeploymentAnswers{PrimaryOf: dp.Primary, DeploymentsOf: dp.DeploymentsOf,
		AddressedDeployment: dp.Addressed, RegistrationsActive: dp.RegistrationsActive,
		DeploymentEdges: dp.EdgePolicies, ReadDeploymentFile: dp.ReadDeploymentFile,
		WriteDeploymentFile: dp.WriteDeploymentFile, RemoveDeploymentFile: dp.RemoveDeploymentFile}
	dp.IsAdmin, dp.MayManage = b.IsAdmin, b.MayManageDeployments
	return dp
}

// edgeDev and edgeMain are tile's instance principals bound to dev and to main.
func edgeDev(tile string) auth.Principal {
	return auth.Principal{Component: tile, Via: "instance", Deployment: "dev"}
}
func edgeMain(tile string) auth.Principal { return auth.Principal{Component: tile, Via: "instance"} }

// edgeRoute is Route's answer for p calling target by its bare URL.
func edgeRoute(t *testing.T, b *Broker, p auth.Principal, target string) Decision {
	t.Helper()
	return b.Route(p, mustComp(t, b, target), "")
}

// edgeListed is tile's listed edge id.
func edgeListed(t *testing.T, b *Broker, tile, id string) deployments.Edge {
	t.Helper()
	for _, e := range b.EdgesOf(tile) {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("%s lists no edge %s: %+v", tile, id, b.EdgesOf(tile))
	return deployments.Edge{}
}

// edgeWantDeny fails unless d is refused with a text holding every part.
func edgeWantDeny(t *testing.T, what string, d Decision, parts ...string) {
	t.Helper()
	if d.Deny == nil {
		t.Errorf("%s: allowed at %q, want refused", what, d.Role)
		return
	}
	if d.Role != "" || d.Clamped {
		t.Errorf("%s: refused, yet role %q clamped %v", what, d.Role, d.Clamped)
	}
	for _, p := range parts {
		if !strings.Contains(d.Deny.Error(), p) {
			t.Errorf("%s: %q doesn't say %q", what, d.Deny, p)
		}
	}
}

// covers D127a D127f D127o T4 SC-CLAMP — the read clamp on every edge kind
// (TestReadClampOnEveryEdge): a non-primary caller holding writer or admin,
// by grant row or http binding, reaches the provider's primary as reader,
// with the edges it used and the clamp named for the response; a resource
// writer grant in another scope reads but can't write (a registration's
// check, allowResUnclamped, keeps the tile's writer), and a cross-scope
// publish is refused naming the policy; a custom role clamps to reader only
// where the provider's implies reach reader, and the channel fixture, which
// merely declares reader, is blocked; the bus aliases map (publisher to
// subscriber); the primary's roles are grantedRole's, unclamped.
func TestGrantedRoleReadClamp(t *testing.T) {
	b := newEdgeBroker(t)
	edgePlane(t, b, edgeRec{tile: fxEmail, primary: util.MainDeployment}, edgeRec{tile: fxChat, primary: util.MainDeployment},
		edgeRec{tile: efEditorUser, primary: util.MainDeployment}, edgeRec{tile: fxWebhooks, primary: util.MainDeployment},
		edgeRec{tile: efBusPub, primary: util.MainDeployment})

	for _, c := range []struct {
		what, caller, target, granted string
		edges                         []string
	}{
		{"a writer grant row", fxEmail, fxCalendar, "writer", []string{"grant:apps/calendar"}},
		{"an admin grant row", fxEmail, fxShop, "admin", []string{"grant:apps/shop"}},
		{"a writer binding", fxChat, fxLLM, "writer", []string{"slot:llm"}},
		{"a custom role implying reader", efEditorUser, efEditorProv, "editor", []string{"slot:docs"}},
	} {
		if role, ok := b.grantedRole(c.caller, c.target); !ok || role != c.granted {
			t.Fatalf("%s: the tile holds %q (%v), want %q", c.what, role, ok, c.granted)
		}
		d := edgeRoute(t, b, edgeDev(c.caller), c.target)
		if d.Deny != nil || d.Role != "reader" || d.Deployment != util.MainDeployment || !slices.Equal(d.Edges, c.edges) {
			t.Errorf("%s, from dev: %+v, want reader at the primary over %v", c.what, d, c.edges)
		}
		if d.Clamped != (c.granted != "reader") {
			t.Errorf("%s: clamped %v, granted %q", c.what, d.Clamped, c.granted)
		}
		if role, ok := b.Policy(edgeDev(c.caller), mustComp(t, b, c.target)); !ok || role != "reader" {
			t.Errorf("%s: Policy gives dev %q (%v), want reader", c.what, role, ok)
		}
		p := edgeRoute(t, b, edgeMain(c.caller), c.target)
		if p.Deny != nil || p.Role != c.granted || p.Clamped || p.Edges != nil {
			t.Errorf("%s, from the primary: %+v, want %q untouched", c.what, p, c.granted)
		}
	}

	// the llm-gw shape: completions guard writer, the models list reader
	d := edgeRoute(t, b, edgeDev(fxChat), fxLLM)
	if roleSatisfies(d.Role, "writer", nil) || !roleSatisfies(d.Role, "reader", nil) {
		t.Errorf("dev's clamped role %q: a writer guard must refuse it, a reader guard pass it", d.Role)
	}
	if n := edgeListed(t, b, fxChat, "slot:llm").Clamped; n < 1 {
		t.Errorf("slot:llm counts %d clamped calls, want at least 1", n)
	}

	// the channel fixture: apps/agent declares reader, but channel implies nothing
	edgeWantDeny(t, "webhooks → agent from dev", edgeRoute(t, b, edgeDev(fxWebhooks), fxAgent),
		`custom role "channel" on apps/agent`, "implies no reader", "slot:agents", "declare what channel implies")
	if d := edgeRoute(t, b, edgeMain(fxWebhooks), fxAgent); d.Deny != nil || d.Role != "channel" {
		t.Errorf("webhooks → agent from the primary: %+v, want channel", d)
	}

	// resources in another scope: read, never write
	for want, ok := range map[string]bool{"reader": true, "subscriber": true, "writer": false} {
		err := b.allowRes(edgeDev(fxEmail), "res:apps/calendar/events", want)
		switch {
		case ok && err != nil:
			t.Errorf("dev reads res:apps/calendar/events at %s: %v", want, err)
		case !ok && (err == nil || !strings.Contains(err.Error(), `read-only (edge policy "read")`)):
			t.Errorf("dev at %s on res:apps/calendar/events: %v, want the read-only refusal", want, err)
		}
		if err := b.allowRes(edgeMain(fxEmail), "res:apps/calendar/events", want); err != nil {
			t.Errorf("the primary at %s: %v", want, err)
		}
	}
	// a registration on a foreign cron resource meets the tile's authority and
	// the edge's block, not the clamp (NP-09-18): the check cron.go's seam calls
	if err := b.allowResUnclamped(edgeDev(fxEmail), "res:apps/calendar/events", "writer"); err != nil {
		t.Errorf("the unclamped check refuses dev at the tile's writer: %v", err)
	}
	if err := b.allowResUnclamped(edgeDev(fxEmail), "res:apps/calendar/events", "admin"); err == nil {
		t.Error("the unclamped check widened the tile's authority")
	}
	// a cross-scope publish needs writer, which the clamp takes away
	if err := b.allowRes(edgeDev(efBusPub), "res:apps/calendar/bus", "publisher"); err == nil {
		t.Error("dev published to another scope's bus")
	}
	if err := b.allowRes(edgeDev(efBusPub), "res:apps/calendar/bus", "subscriber"); err != nil {
		t.Errorf("dev subscribes to another scope's bus: %v", err)
	}
	if d := b.resolveTarget(efBusPub, "dev", "res:apps/calendar/bus"); d.Role != "subscriber" || !d.Clamped {
		t.Errorf("publisher clamps to %+v, want subscriber", d)
	}
	if err := b.allowRes(edgeMain(efBusPub), "res:apps/calendar/bus", "publisher"); err != nil {
		t.Errorf("the primary publishes: %v", err)
	}
	for _, c := range []struct{ have, want string }{{"reader", "reader"}, {"subscriber", "subscriber"},
		{"writer", "reader"}, {"admin", "reader"}, {"publisher", "subscriber"}, {"editor", "reader"}} {
		if got := clampedRole(c.have); got != c.want {
			t.Errorf("clampedRole(%q) = %q, want %q", c.have, got, c.want)
		}
	}
}

// covers D127a D127o T4 NP-09-6 NP-09-10 — block fails closed on every edge kind
// (TestEdgeBlockFailsClosed): a slot, a grant row, a resource grant (its
// WebSocket bus reads and a subscription's edge check included), the net
// slot and a capability grant at block refuse a non-primary deployment,
// naming the deployment, the edge, the target and the policy, and saying
// who can change it; each refusal is counted on its edge. The primary is
// untouched.
func TestEdgePolicyBlock(t *testing.T) {
	b := newEdgeBroker(t)
	edgePlane(t, b,
		edgeRec{tile: fxChat, primary: util.MainDeployment, edges: map[string]string{"slot:llm": "block"}},
		edgeRec{tile: fxEmail, primary: util.MainDeployment, edges: map[string]string{
			"grant:apps/calendar": "block", "grant:res:apps/calendar/bus": "block"}},
		edgeRec{tile: efWeb, primary: util.MainDeployment, edges: map[string]string{"slot:net": "block"}},
		edgeRec{tile: efGPU, primary: util.MainDeployment, edges: map[string]string{"grant:cap:containers": "block"}})

	edgeWantDeny(t, "slot:llm", edgeRoute(t, b, edgeDev(fxChat), fxLLM),
		`apps/chat's non-primary deployment "dev" may not use edge slot:llm to apps/llm-gw`,
		`the tile's edge policy for it is "block"`, "A tile manager can change it in the Deployments panel")
	edgeWantDeny(t, "grant:apps/calendar", edgeRoute(t, b, edgeDev(fxEmail), fxCalendar),
		`apps/email's non-primary deployment "dev" may not use edge grant:apps/calendar:`, `"block"`)
	if err := b.allowRes(edgeDev(fxEmail), "res:apps/calendar/bus", "reader"); err == nil || !strings.Contains(err.Error(), "grant:res:apps/calendar/bus") {
		t.Errorf("dev reads a blocked bus: %v", err)
	}
	if b.busFilter(edgeDev(fxEmail), events.Event{Type: "bus", Topic: "res:apps/calendar/bus/x"}) {
		t.Error("dev's WebSocket received a blocked bus's event")
	}
	if err := b.depEdge(fxEmail, "dev", "res:apps/calendar/bus"); err == nil {
		t.Error("a bus subscription on a blocked bus passed its edge check")
	}
	if !b.busFilter(edgeMain(fxEmail), events.Event{Type: "bus", Topic: "res:apps/calendar/bus/x"}) {
		t.Error("the primary lost its bus events")
	}
	if v, _ := b.NetEdge(efWeb); v != deployments.EdgeBlock || b.viewNetInherits(edgeDevView(t, b, efWeb)) {
		t.Errorf("a blocked net slot reads %q", v)
	}
	if !b.viewNetInherits(mustComp(t, b, efWeb)) {
		t.Error("the primary lost its relay policy")
	}
	if _, ok := b.viewGrant(edgeDevView(t, b, efGPU), ContainersCap); ok {
		t.Error("a blocked cap:containers reached dev's sandbox")
	}
	if _, ok := b.viewGrant(mustComp(t, b, efGPU), ContainersCap); !ok {
		t.Error("the primary lost cap:containers")
	}
	for tile, want := range map[string]map[string]int64{
		fxChat:  {"slot:llm": 1},
		fxEmail: {"grant:apps/calendar": 1, "grant:res:apps/calendar/bus": 3},
	} {
		for id, n := range want {
			if e := edgeListed(t, b, tile, id); e.Refused != n || e.Policy != "block" || !e.Set {
				t.Errorf("%s's %s: %+v, want policy block, set, %d refused", tile, id, e, n)
			}
		}
	}
	for _, c := range []struct{ caller, target string }{{fxChat, fxLLM}, {fxEmail, fxCalendar}} {
		want, _ := b.grantedRole(c.caller, c.target)
		if d := edgeRoute(t, b, edgeMain(c.caller), c.target); d.Deny != nil || d.Role != want {
			t.Errorf("%s → %s from the primary: %+v, want %q", c.caller, c.target, d, want)
		}
	}
}

// edgeDevView is the runner's view of tile's dev deployment: the component with
// Deployment set, as registry.View makes it for a non-primary generation.
func edgeDevView(t *testing.T, b *Broker, tile string) *registry.Component {
	t.Helper()
	v := *mustComp(t, b, tile)
	v.Deployment = "dev"
	return &v
}

// covers D127o T4 E10 — edges that can't be read-clamped are blocked by
// default, and nothing stored unblocks them: a custom role with no path to
// reader (channel; consumer on the sandbox managers), a stream slot, a
// lan-ingress slot and a net provider splice list block alone with the
// reason, and refuse calls even with read or inherit hand-written into the
// record; gpu:* defaults to block and the other capability grants to
// inherit; a workspace-level filesystem on a workspace-scope tile takes
// block alone.
func TestUnclampableEdgeDefaults(t *testing.T) {
	b := newEdgeBroker(t)
	hand := map[string]string{"slot:agents": "read", "slot:sandboxes": "read", "slot:db": "read", "slot:lan": "inherit", "slot:net": "inherit"}
	edgePlane(t, b, edgeRec{tile: fxWebhooks, primary: util.MainDeployment}, edgeRec{tile: fxCoder, primary: util.MainDeployment},
		edgeRec{tile: fxPGClient, primary: util.MainDeployment}, edgeRec{tile: efLAN, primary: util.MainDeployment},
		edgeRec{tile: efNetClient, primary: util.MainDeployment}, edgeRec{tile: efGPU, primary: util.MainDeployment},
		edgeRec{tile: fxEmail, primary: util.MainDeployment})
	for _, c := range []struct{ tile, id, kind, why string }{
		{fxWebhooks, "slot:agents", edgeHTTP, `custom role "channel" on apps/agent`},
		{fxCoder, "slot:sandboxes", edgeHTTP, `custom role "consumer" on apps/sbx-a`},
		{fxPGClient, "slot:db", edgeStream, "stream edges can't be read-clamped"},
		{efLAN, "slot:lan", edgeLAN, "lan-ingress links can't be read-clamped"},
		{efNetClient, "slot:net", edgeNet, "net provider splices serve the tile's primary only"},
		{fxEmail, "grant:res:workspace/wsfiles", edgeResource, "a read-only bind is not a safe read"},
	} {
		e := edgeListed(t, b, c.tile, c.id)
		if e.Kind != c.kind || e.Policy != "block" || e.Default != "block" || !slices.Equal(e.Values, []string{"block"}) ||
			!strings.Contains(e.Why, c.why) {
			t.Errorf("%s's %s: %+v, want %s, block alone, why %q", c.tile, c.id, e, c.kind, c.why)
		}
	}
	edgeWantDeny(t, "webhooks from dev", edgeRoute(t, b, edgeDev(fxWebhooks), fxAgent), "slot:agents", "channel")
	for _, sbx := range []string{fxSbxA, fxSbxB} {
		edgeWantDeny(t, "coder → "+sbx, edgeRoute(t, b, edgeDev(fxCoder), sbx), "slot:sandboxes", "consumer")
	}
	for id, want := range map[string]string{"grant:gpu:0": "block", "grant:cap:net-admin": "inherit",
		"grant:cap:containers": "inherit", "grant:cap:open-links": "inherit"} {
		if e := edgeListed(t, b, efGPU, id); e.Kind != edgeCapability || e.Default != want || e.Policy != want ||
			!slices.Equal(e.Values, []string{"inherit", "block"}) {
			t.Errorf("%s: %+v, want default %s over inherit|block", id, e, want)
		}
	}

	// the same edges with read or inherit written into the record by hand
	edgePlane(t, b, edgeRec{tile: fxWebhooks, primary: util.MainDeployment, edges: hand},
		edgeRec{tile: fxCoder, primary: util.MainDeployment, edges: hand},
		edgeRec{tile: efNetClient, primary: util.MainDeployment, edges: hand})
	edgeWantDeny(t, "webhooks with a hand-written read", edgeRoute(t, b, edgeDev(fxWebhooks), fxAgent), "slot:agents", "channel")
	edgeWantDeny(t, "coder with a hand-written read", edgeRoute(t, b, edgeDev(fxCoder), fxSbxB), "slot:sandboxes", "consumer")
	if v, why := b.NetEdge(efNetClient); v != "block" || why == "" {
		t.Errorf("a splice with a hand-written inherit reads %q (%q)", v, why)
	}
	if e := edgeListed(t, b, fxWebhooks, "slot:agents"); e.Policy != "read" || e.Effective != "block" || e.Why == "" {
		t.Errorf("a hand-written read on slot:agents lists %+v, want effective block with why", e)
	}
}

// covers D127o D127s T4 — D127o has no override: writing read or inherit to a
// stream, lan-ingress, splice or custom-role edge answers 400 in 11-contract
// §1.14's form (<edge> takes <values>: <reason>); a hand-written read for one
// in the record reads as block (TestUnclampableEdgeDefaults drives the
// calls). The other writes: a value the edge takes passes; inherit on a
// read-clamp edge, read on the net slot, match and a word no edge takes are
// 400; an edge the tile doesn't have is 404; default removes an override,
// even one whose edge is gone; a host-sharing net slot takes inherit, which
// stays block in effect (11-contract §1.7). Setting a policy is a tile
// manager's act: the plane's gate, MayManageDeployments, passes an admin
// and refuses a writer, a terminal-level user and every tile credential.
func TestUnclampableEdgesRefuseOverride(t *testing.T) {
	b := newEdgeBroker(t)
	dp := edgePlane(t, b, edgeRec{tile: fxEmail, primary: util.MainDeployment, edges: map[string]string{"grant:apps/gone": "read"}},
		edgeRec{tile: fxCalendar, primary: util.MainDeployment})
	if !dp.Manager(deployPerson(t, b, fxAdmin), fxCalendar) {
		t.Error("an admin can't set apps/calendar's edge policy")
	}
	for _, p := range []auth.Principal{deployPerson(t, b, fxWriter), deployPerson(t, b, fxTerm), edgeMain(fxCalendar),
		{Component: fxCalendar, Via: "terminal", UserID: fxAdmin}} {
		if dp.Manager(p, fxCalendar) {
			t.Errorf("%s via %q passes the manager gate for an edge policy", p.From(), p.Via)
		}
	}
	status := func(err error) int {
		var de *deployments.Error
		switch {
		case err == nil:
			return 200
		case errors.As(err, &de):
			return de.Status
		}
		return -1
	}
	for _, c := range []struct {
		tile, edge, policy string
		code               int
		says               string
	}{
		{fxWebhooks, "slot:agents", "read", 400, "slot:agents takes block: a custom role with no path to reader can't be read-clamped"},
		{fxCoder, "slot:sandboxes", "read", 400, `custom role "consumer"`},
		{fxPGClient, "slot:db", "read", 400, "slot:db takes block: stream edges can't be read-clamped"},
		{fxPGClient, "slot:db", "inherit", 400, "with no override"},
		{efLAN, "slot:lan", "read", 400, "lan-ingress links can't be read-clamped"},
		{efNetClient, "slot:net", "inherit", 400, "slot:net takes block: net provider splices serve the tile's primary only"},
		{fxEmail, "grant:res:workspace/wsfiles", "read", 400, "takes block"},
		{fxWebhooks, "slot:agents", "block", 200, ""},
		{fxWebhooks, "slot:agents", "default", 200, ""},
		{fxChat, "slot:llm", "read", 200, ""},
		{fxChat, "slot:llm", "inherit", 400, "slot:llm takes read or block: inherit is for the net slot and capability grants"},
		{fxChat, "slot:llm", "match", 400, "match isn't accepted"},
		{fxChat, "slot:llm", "wide", 400, `"wide" is not an edge-policy value`},
		{efWeb, "slot:net", "read", 400, "slot:net takes inherit or block: read clamps a role"},
		{efWeb, "slot:net", "inherit", 200, ""},
		{efHostNet, "slot:net", "inherit", 200, ""},
		{efGPU, "grant:gpu:0", "inherit", 200, ""},
		{efGPU, "grant:gpu:0", "read", 400, "takes inherit or block"},
		{fxEmail, "code:apps/calendar", "read", 404, "apps/email has no edge code:apps/calendar"},
		{fxEmail, "grant:code:apps/calendar", "read", 200, ""},
		{fxEmail, "grant:xbin", "read", 404, "has no edge"},
		{fxChat, "slot:nope", "block", 404, "apps/chat has no edge slot:nope"},
		{fxEmail, "grant:apps/gone", "read", 404, "has no edge"},
		{fxEmail, "grant:apps/gone", "default", 200, ""},
		{"apps/nope", "slot:x", "block", 404, "no such tile: apps/nope"},
	} {
		err := b.ValidateEdgePolicy(c.tile, c.edge, c.policy)
		if got := status(err); got != c.code || c.says != "" && !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s %s=%s: %d %v, want %d saying %q", c.tile, c.edge, c.policy, got, err, c.code, c.says)
		}
	}
	if e := edgeListed(t, b, fxEmail, "grant:apps/gone"); e.Kind != "" || len(e.Values) != 0 || !e.Set || !strings.Contains(e.Why, "no longer present") {
		t.Errorf("a vanished edge's override lists %+v", e)
	}
	edgePlane(t, b, edgeRec{tile: efHostNet, primary: util.MainDeployment, edges: map[string]string{"slot:net": "inherit"}})
	if e := edgeListed(t, b, efHostNet, "slot:net"); e.Policy != "inherit" || e.Effective != "block" || !strings.Contains(e.Why, "host networking") {
		t.Errorf("a host-sharing net slot at inherit lists %+v, want effective block", e)
	}
}

// covers D127a D127o T4 K14 — the sandbox-managers interplay: apps/coder's multi
// sandbox-manager slot, whose providers both grant the custom role consumer,
// is blocked for every provider on every non-primary deployment (dev and a
// third, qa), each refusal counted on the slot, while the primary's calls
// are unchanged; llm-gw's shape, a writer binding, reaches the provider as
// reader, so its writer-guarded completions refuse dev and its reader route
// answers. A mixed multi slot clamps its clampable provider and blocks the
// other.
func TestSandboxManagerEdgeBlocked(t *testing.T) {
	b := newEdgeBroker(t)
	edgePlane(t, b, edgeRec{tile: fxCoder, primary: util.MainDeployment, extra: []string{"qa"}},
		edgeRec{tile: fxChat, primary: util.MainDeployment}, edgeRec{tile: efMixer, primary: util.MainDeployment})
	calls := 0
	for _, depName := range []string{"dev", "qa"} {
		p := auth.Principal{Component: fxCoder, Via: "instance", Deployment: depName}
		for _, sbx := range []string{fxSbxA, fxSbxB} {
			edgeWantDeny(t, depName+" → "+sbx, edgeRoute(t, b, p, sbx), `non-primary deployment "`+depName+`"`, "slot:sandboxes",
				`custom role "consumer" on `+sbx, "Test this from the primary")
			calls++
		}
		if b.codeReadAllowed(p, fxSbxA) {
			t.Errorf("%s reads a sandbox manager's code without a grant", depName)
		}
	}
	e := edgeListed(t, b, fxCoder, "slot:sandboxes")
	if e.Refused != int64(calls) || e.To != "apps/sbx-a, apps/sbx-b" || e.Role != "consumer" || !slices.Equal(e.Values, []string{"block"}) {
		t.Errorf("slot:sandboxes lists %+v, want %d refused, both managers at consumer, block alone", e, calls)
	}
	for _, sbx := range []string{fxSbxA, fxSbxB} {
		if d := edgeRoute(t, b, edgeMain(fxCoder), sbx); d.Deny != nil || d.Role != "consumer" || d.Clamped {
			t.Errorf("the primary → %s: %+v, want consumer", sbx, d)
		}
	}
	d := edgeRoute(t, b, edgeDev(fxChat), fxLLM)
	if d.Deny != nil || roleSatisfies(d.Role, "writer", nil) || !roleSatisfies(d.Role, "reader", nil) {
		t.Errorf("chat's dev → llm-gw: %+v: completions (writer) must refuse, models (reader) answer", d)
	}
	if d := edgeRoute(t, b, edgeMain(fxChat), fxLLM); !roleSatisfies(d.Role, "writer", nil) {
		t.Errorf("chat's primary lost completions: %+v", d)
	}
	if d := edgeRoute(t, b, edgeDev(efMixer), efMCPA); d.Deny != nil || d.Role != "reader" || !d.Clamped {
		t.Errorf("mixer's dev → mcp-a (writer): %+v, want reader", d)
	}
	edgeWantDeny(t, "mixer's dev → mcp-b (tool)", edgeRoute(t, b, edgeDev(efMixer), efMCPB), "slot:mcp", `custom role "tool"`)
	if e := edgeListed(t, b, efMixer, "slot:mcp"); !slices.Equal(e.Values, []string{"read", "block"}) || e.Policy != "read" {
		t.Errorf("a mixed multi slot lists %+v, want read|block", e)
	}
}

// covers D127a D127o T4 T17 — the net edge: inherit by default, the tile's relay
// policy; block on a manager's word; a net that shares the host's (the host
// builtin, a named set whose rules say host) is block whatever is stored,
// with the reason, and a non-primary view never answers NetHostShare; a net
// bound to a provider tile is a splice, block alone. The primary's view
// always takes the tile's relay policy, and its NetHostShare is today's.
func TestNetEdgeDefault(t *testing.T) {
	b := newEdgeBroker(t)
	edgePlane(t, b, edgeRec{tile: efWeb, primary: util.MainDeployment}, edgeRec{tile: efHostNet, primary: util.MainDeployment},
		edgeRec{tile: efSetNet, primary: util.MainDeployment}, edgeRec{tile: efNetClient, primary: util.MainDeployment})
	for _, c := range []struct {
		tile, want, why string
		values          []string
	}{
		{efWeb, "inherit", "", []string{"inherit", "block"}},
		{efHostNet, "block", "host networking serves the tile's primary only; non-primary deployments get no egress", []string{"inherit", "block"}},
		{efSetNet, "block", "host networking serves the tile's primary only", []string{"inherit", "block"}},
		{efNetClient, "block", "net provider splices serve the tile's primary only", []string{"block"}},
		{fxPlain, "inherit", "", nil},
	} {
		v, why := b.NetEdge(c.tile)
		if v != c.want || !strings.Contains(why, c.why) || c.why == "" && why != "" {
			t.Errorf("%s's net edge: %q (%q), want %q (%q)", c.tile, v, why, c.want, c.why)
		}
		if got := b.viewNetInherits(edgeDevView(t, b, c.tile)); got != (c.want == "inherit") {
			t.Errorf("%s: dev takes the relay policy: %v", c.tile, got)
		}
		if !b.viewNetInherits(mustComp(t, b, c.tile)) {
			t.Errorf("%s: the primary lost its relay policy", c.tile)
		}
		if c.values != nil {
			if e := edgeListed(t, b, c.tile, "slot:net"); e.Kind != edgeNet || !slices.Equal(e.Values, c.values) || e.Default != c.values[0] {
				t.Errorf("%s's net slot lists %+v", c.tile, e)
			}
		}
	}
	for _, tile := range []string{efHostNet, efSetNet} {
		if !b.NetHostShare(mustComp(t, b, tile)) {
			t.Errorf("%s's primary no longer shares the host network", tile)
		}
		if b.NetHostShare(edgeDevView(t, b, tile)) {
			t.Errorf("%s's dev view shares the host network", tile)
		}
	}
	if !b.EdgeChangeRestarts(efWeb, "slot:net") || !b.EdgeChangeRestarts(efGPU, "grant:gpu:0") || b.EdgeChangeRestarts(fxChat, "slot:llm") {
		t.Error("only spawn-time edges (net, capabilities) restart the non-primary deployments on a change")
	}
	edgePlane(t, b, edgeRec{tile: efWeb, primary: util.MainDeployment, edges: map[string]string{"slot:net": "block"}})
	if v, _ := b.NetEdge(efWeb); v != "block" {
		t.Errorf("a manager's block on the net slot reads %q", v)
	}
}

// covers D127a D119c Z3 — the edge policy never changes a primary call: with
// every edge of every fixture tile stored as block (or a value no xbind
// knows), each tile's primary principals (instance, frame, a terminal that
// follows the primary) get Route, Policy, allowRes and resolveTarget exactly
// as grantedRole and today's allowRes answer, unclamped and naming no edge;
// so does a primary that isn't main (apps/shop's dev, with main beside it
// clamped).
func TestEdgePolicyNeverTouchesPrimary(t *testing.T) {
	b := newEdgeBroker(t)
	var recs []edgeRec
	var tiles []string
	for _, c := range b.Reg.Components() {
		if c.Path == fxChrome || c.Path == fxConsole || c.Path == fxShop {
			continue
		}
		tiles = append(tiles, c.Path)
		edges := map[string]string{}
		for _, e := range b.tileEdges(c) {
			edges[e.id] = "block"
		}
		edges["slot:llm"] = "match"
		recs = append(recs, edgeRec{tile: c.Path, primary: util.MainDeployment, edges: edges})
	}
	sort.Strings(tiles)
	recs = append(recs, edgeRec{tile: fxShop, primary: "dev", edges: map[string]string{"grant:res:apps/calendar/bus": "block"}})
	edgePlane(t, b, recs...)
	targets := append(slices.Clone(tiles), fxShop, fxConsole, fxChrome)
	resources := []string{"res:apps/calendar/events", "res:apps/calendar/bus", "res:apps/shop/orders", "res:workspace/wsfiles"}
	for _, tile := range tiles {
		for _, p := range []auth.Principal{edgeMain(tile), {Component: tile, Via: "frame"}, {Component: tile, Via: "terminal", UserID: fxTerm}} {
			for _, target := range targets {
				role, ok := b.grantedRole(tile, target)
				if target == tile {
					role, ok = "admin", true
				}
				d := edgeRoute(t, b, p, target)
				if ok != (d.Deny == nil) || d.Role != role || d.Clamped || d.Edges != nil {
					t.Errorf("%s via %s → %s: %+v, want %q (%v)", tile, p.Via, target, d, role, ok)
				}
				if got, gotOK := b.Policy(p, mustComp(t, b, target)); got != role || gotOK != ok {
					t.Errorf("Policy(%s via %s → %s) = %q %v, want %q %v", tile, p.Via, target, got, gotOK, role, ok)
				}
			}
			for _, res := range resources {
				for _, want := range []string{"reader", "writer"} {
					if got, ref := b.allowRes(p, res, want), todayAllowRes(b, p, res, want); (got == nil) != (ref == nil) {
						t.Errorf("allowRes(%s via %s, %s, %s) = %v; today %v", tile, p.Via, res, want, got, ref)
					}
				}
			}
		}
	}
	// apps/shop: dev is the primary and main isn't
	shopDev, shopMain := edgeDev(fxShop), edgeMain(fxShop)
	for _, target := range targets {
		role, ok := b.grantedRole(fxShop, target)
		if d := edgeRoute(t, b, shopDev, target); target != fxShop && (ok != (d.Deny == nil) || d.Role != role || d.Clamped) {
			t.Errorf("shop's primary dev → %s: %+v, want %q", target, d, role)
		}
	}
	if err := b.allowRes(shopDev, "res:apps/shop/orders", "writer"); err != nil {
		t.Errorf("shop's primary dev writes its own data: %v", err)
	}
	if err := b.allowRes(shopMain, "res:apps/shop/orders", "writer"); err != nil {
		t.Errorf("shop's main writes its own namespace (own scope, no edge): %v", err)
	}
	if role, ok := b.Policy(shopMain, mustComp(t, b, fxShop)); ok {
		t.Errorf("Policy lets main, not shop's primary, reach its tile as %q", role)
	}
}

// covers D127g T3 — self-calls stay in their deployment
// (TestSelfCallStaysInDeployment): the tile's principal bound to dev (an
// instance, a frame whose user can write, a terminal targeting dev) reaches
// dev as admin by the bare URL, and a URL naming main is refused; main's
// reaches main and is refused at dev; a frame whose user lost write is
// refused on dev; a deployment that no longer exists is a 404; a session
// following a protected primary reaches nothing. Policy, which reaches the
// primary alone, keeps self-admin for the primary's principals only.
func TestSelfCallRouting(t *testing.T) {
	b := newEdgeBroker(t)
	edgePlane(t, b, edgeRec{tile: fxCalendar, primary: util.MainDeployment},
		edgeRec{tile: fxShop, primary: "dev", protected: true})
	cal := mustComp(t, b, fxCalendar)
	writer := deployPerson(t, b, fxWriter)
	reader := deployPerson(t, b, fxReader)
	devFrame := auth.Principal{Component: fxCalendar, Via: "frame", Deployment: "dev", UserID: fxWriter, Access: writer.Access}
	readerFrame := auth.Principal{Component: fxCalendar, Via: "frame", Deployment: "dev", UserID: fxReader, Access: reader.Access}
	devTerm := auth.Principal{Component: fxCalendar, Via: "terminal", Deployment: "dev", UserID: fxTerm}
	for _, p := range []auth.Principal{edgeDev(fxCalendar), devFrame, devTerm} {
		if d := b.Route(p, cal, ""); d.Deny != nil || d.Deployment != "dev" || d.Role != "admin" {
			t.Errorf("%s's %s self-call: %+v, want dev as admin", fxCalendar, p.Via, d)
		}
		if d := b.Route(p, cal, "dev"); d.Deny != nil || d.Deployment != "dev" {
			t.Errorf("%s's %s naming dev: %+v", fxCalendar, p.Via, d)
		}
		edgeWantDeny(t, p.Via+" of dev naming main", b.Route(p, cal, util.MainDeployment), `can only call itself`, `belongs to "dev", not "main"`)
		if _, ok := b.Policy(p, cal); ok {
			t.Errorf("Policy lets dev's %s be admin of the primary", p.Via)
		}
	}
	if d := b.Route(edgeMain(fxCalendar), cal, ""); d.Deny != nil || d.Deployment != util.MainDeployment || d.Role != "admin" {
		t.Errorf("main's self-call: %+v", d)
	}
	edgeWantDeny(t, "main naming dev", b.Route(edgeMain(fxCalendar), cal, "dev"), `belongs to "main", not "dev"`)
	if role, ok := b.Policy(edgeMain(fxCalendar), cal); !ok || role != "admin" {
		t.Errorf("Policy: main's own principal is %q (%v), want admin", role, ok)
	}
	edgeWantDeny(t, "a reader's dev frame", b.Route(readerFrame, cal, ""), "need write access")
	gone := auth.Principal{Component: fxCalendar, Via: "instance", Deployment: "old"}
	if d := b.Route(gone, cal, ""); !errors.Is(d.Deny, util.ErrNoDeployment) {
		t.Errorf("a gone deployment's credential: %+v, want a 404", d)
	}
	follow := auth.Principal{Component: fxShop, Via: "terminal", UserID: fxTerm}
	edgeWantDeny(t, "a session following a protected primary", b.Route(follow, mustComp(t, b, fxShop), ""), "protected")
	if b.IsAdmin(follow) {
		t.Error("a session following a protected primary is an admin")
	}
}

// covers D127k T14 — approving an xbin or xbin:* grant for a tile that has
// non-primary deployments is refused with 409 (11-contract §1.14's text);
// a tile whose record holds main alone, and a tile without a record, may
// be granted one; revoking is always allowed.
func TestXbinGrantRefusedWithNonPrimary(t *testing.T) {
	b := newEdgeBroker(t)
	edgePlane(t, b, edgeRec{tile: fxCalendar, primary: util.MainDeployment})
	admin := deployPerson(t, b, fxAdmin)
	for _, target := range []string{"xbin", "xbin:users"} {
		body := `{"from":"apps/calendar","target":"` + target + `","role":"admin"}`
		rec := call(t, b.apiGrantsAdd, admin, "POST", "/grants", body, nil)
		want := "apps/calendar has non-primary deployments: remove them before granting it " + target
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("granting %s to a tile with dev: %d %s", target, rec.Code, rec.Body)
		}
		if rec := call(t, b.apiGrantsRevoke, admin, "DELETE", "/grants", body, nil); rec.Code != 200 {
			t.Errorf("revoking %s: %d %s", target, rec.Code, rec.Body)
		}
	}
	if rec := call(t, b.apiGrantsAdd, admin, "POST", "/grants", `{"from":"apps/calendar","target":"apps/shop","role":"reader"}`, nil); rec.Code != 200 {
		t.Errorf("an ordinary grant for a tile with dev: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, b.apiGrantsAdd, admin, "POST", "/grants", `{"from":"apps/plain","target":"xbin","role":"admin"}`, nil); rec.Code != 200 {
		t.Errorf("an xbin grant for a tile without deployments: %d %s", rec.Code, rec.Body)
	}
	pinRecord(t, b.Reg.Root, fxShop, "")
	dp := &deployments.Plane{Root: b.Reg.Root, OwnerRef: b.Users.Owner}
	if err := dp.Boot(); err != nil {
		t.Fatal(err)
	}
	b.DeploymentAnswers.DeploymentsOf = dp.DeploymentsOf
	if rec := call(t, b.apiGrantsAdd, admin, "POST", "/grants", `{"from":"apps/shop","target":"xbin:users","role":"admin"}`, nil); rec.Code != 200 {
		t.Errorf("an xbin grant for a tile with main alone (live reload paused): %d %s", rec.Code, rec.Body)
	}
}

// covers D127k T14 — a non-primary principal is never an admin element and
// holds no governance role, whatever the grant table says: apps/console,
// granted xbin and xbin:users at admin by a hand-edited workspace, has dev
// beside main; its instance, frame and terminal principals bound to dev
// fail IsAdmin and governanceRole, while main's pass; a credential of a gone
// deployment passes nothing. resolveTarget refuses dev every governance
// target. (canCreateAt, requireWriter, canManageUsers and
// elementXbinCapable read grantedRole directly: governanceRole is their
// seam, named in this card's amendments.)
func TestNonPrimaryPrincipalNeverAdmin(t *testing.T) {
	b := newEdgeBroker(t)
	edgePlane(t, b, edgeRec{tile: fxConsole, primary: util.MainDeployment})
	for _, p := range []auth.Principal{edgeDev(fxConsole), {Component: fxConsole, Via: "frame", Deployment: "dev"},
		{Component: fxConsole, Via: "terminal", Deployment: "dev", UserID: fxTerm}, {Component: fxConsole, Via: "instance", Deployment: "gone"}} {
		if b.IsAdmin(p) {
			t.Errorf("%s via %s bound to %q is an admin", p.Component, p.Via, p.Deployment)
		}
		for _, target := range []string{"xbin", "xbin:users"} {
			if role, ok := b.governanceRole(p, target); ok {
				t.Errorf("%s via %s bound to %q holds %s at %q", p.Component, p.Via, p.Deployment, target, role)
			}
		}
	}
	for _, target := range []string{"xbin", "xbin:users"} {
		edgeWantDeny(t, "dev → "+target, b.resolveTarget(fxConsole, "dev", target), "governance grants serve the tile's primary only")
	}
	for _, p := range []auth.Principal{edgeMain(fxConsole), {Component: fxConsole, Via: "frame"}, {Component: fxConsole, Via: "terminal", UserID: fxTerm}} {
		if !b.IsAdmin(p) {
			t.Errorf("the primary's %s isn't an admin element", p.Via)
		}
	}
}

// covers D127s NP-09-2 — block wins among several edges authorizing one
// call: apps/chat reaches apps/llm-gw by its llm slot and a writer grant row; a
// block on either refuses the call, naming that edge, though the other is
// read; both at read clamp to reader. The same for code edges: grant:code
// at block refuses a read grant:code:<tile> would allow.
func TestEdgeBlockWins(t *testing.T) {
	b := newEdgeBroker(t)
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: fxChat, Target: fxLLM, Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		edges map[string]string
		deny  string
	}{
		{map[string]string{"slot:llm": "block"}, "slot:llm"},
		{map[string]string{"grant:apps/llm-gw": "block", "slot:llm": "read"}, "grant:apps/llm-gw"},
		{map[string]string{"grant:apps/llm-gw": "read"}, ""},
	} {
		edgePlane(t, b, edgeRec{tile: fxChat, primary: util.MainDeployment, edges: c.edges})
		d := edgeRoute(t, b, edgeDev(fxChat), fxLLM)
		if c.deny != "" {
			edgeWantDeny(t, "chat's dev under "+c.deny+" block", d, "may not use edge "+c.deny)
			for _, other := range []string{"slot:llm", "grant:apps/llm-gw"} {
				if other != c.deny && strings.Contains(d.Deny.Error(), other) {
					t.Errorf("the refusal names %s too: %q", other, d.Deny)
				}
			}
		} else if d.Deny != nil || d.Role != "reader" || !slices.Equal(d.Edges, []string{"grant:apps/llm-gw", "slot:llm"}) {
			t.Errorf("both edges at read: %+v", d)
		}
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: fxEmail, Target: "code", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	edgePlane(t, b, edgeRec{tile: fxEmail, primary: util.MainDeployment})
	if !b.codeReadAllowed(edgeDev(fxEmail), fxCalendar) {
		t.Error("code edges at read: dev can't read apps/calendar's code")
	}
	edgePlane(t, b, edgeRec{tile: fxEmail, primary: util.MainDeployment, edges: map[string]string{"grant:code": "block"}})
	if b.codeReadAllowed(edgeDev(fxEmail), fxCalendar) {
		t.Error("grant:code at block: dev read apps/calendar's code through grant:code:apps/calendar")
	}
	if !b.codeReadAllowed(edgeMain(fxEmail), fxCalendar) {
		t.Error("the primary lost its code read")
	}
}

// covers D127s — a stored value this xbind doesn't know, or one the edge
// doesn't take, reads as block: match (a later xbind's), an unknown word,
// inherit on a read-clamp edge and read on a capability grant each refuse
// the call, list effective block with the reason, and leave the primary as
// it was.
func TestEdgeUnknownValueBlocks(t *testing.T) {
	b := newEdgeBroker(t)
	for _, v := range []string{"match", "wide", "inherit", `{"policy":"match","else":"read"}`} {
		edgePlane(t, b, edgeRec{tile: fxChat, primary: util.MainDeployment, edges: map[string]string{"slot:llm": v}},
			edgeRec{tile: efGPU, primary: util.MainDeployment, edges: map[string]string{"grant:cap:net-admin": "read"}})
		edgeWantDeny(t, "slot:llm="+v, edgeRoute(t, b, edgeDev(fxChat), fxLLM), "slot:llm", "reads as block")
		e := edgeListed(t, b, fxChat, "slot:llm")
		if e.Policy != v || e.Effective != "block" || !strings.Contains(e.Why, "reads as block") {
			t.Errorf("slot:llm=%s lists %+v", v, e)
		}
		if d := edgeRoute(t, b, edgeMain(fxChat), fxLLM); d.Deny != nil || d.Role != "writer" {
			t.Errorf("the primary under slot:llm=%s: %+v", v, d)
		}
		if _, ok := b.viewGrant(edgeDevView(t, b, efGPU), NetAdminCap); ok {
			t.Error("cap:net-admin=read (a value capability grants don't take) reached dev")
		}
	}
}

// covers D127a T4 NP-09-5 — capability grants shape the deployment's own
// sandbox: for a non-primary generation's view, gpu:0 is withheld by
// default and granted at inherit; cap:net-admin, cap:containers and
// cap:open-links pass at their default inherit and are withheld at block;
// the primary's view is grantedRole's whatever is stored. (The runner and
// the document plane apply this through viewGrant once gpu.go's hooks call
// it: the amendment this card names.)
func TestEdgeCapabilities(t *testing.T) {
	b := newEdgeBroker(t)
	caps := []string{"gpu:0", NetAdminCap, ContainersCap, OpenLinksCap}
	for _, c := range []struct {
		edges map[string]string
		want  map[string]bool
	}{
		{nil, map[string]bool{"gpu:0": false, NetAdminCap: true, ContainersCap: true, OpenLinksCap: true}},
		{map[string]string{"grant:gpu:0": "inherit", "grant:cap:net-admin": "block", "grant:cap:containers": "block", "grant:cap:open-links": "block"},
			map[string]bool{"gpu:0": true, NetAdminCap: false, ContainersCap: false, OpenLinksCap: false}},
	} {
		edgePlane(t, b, edgeRec{tile: efGPU, primary: util.MainDeployment, edges: c.edges})
		for _, target := range caps {
			if _, ok := b.viewGrant(edgeDevView(t, b, efGPU), target); ok != c.want[target] {
				t.Errorf("edges %v: dev holds %s: %v, want %v", c.edges, target, ok, c.want[target])
			}
			if _, ok := b.viewGrant(mustComp(t, b, efGPU), target); !ok {
				t.Errorf("edges %v: the primary lost %s", c.edges, target)
			}
		}
	}
	if err := b.StreamDialAllowed(mustComp(t, b, fxPGClient), "db"); err != nil {
		t.Errorf("the primary's stream dial: %v", err)
	}
	edgePlane(t, b, edgeRec{tile: fxPGClient, primary: util.MainDeployment})
	if err := b.StreamDialAllowed(edgeDevView(t, b, fxPGClient), "db"); err == nil || err.Error() != "stream slot db blocked by edge policy (edge slot:db)" {
		t.Errorf("dev's stream dial: %v", err)
	}
	if e := edgeListed(t, b, fxPGClient, "slot:db"); e.Refused != 1 {
		t.Errorf("slot:db counts %d refused dials, want 1", e.Refused)
	}
}

// covers F5 T4 — the single evaluation point stays single (09-fabric §5.3):
// non-test code in internal/broker calls grantedRole, httpBindingRole and
// codeGrantAllows only from the functions listed here, each with why it
// may. A new caller must go through resolveTarget, or join this list with
// its reason. The entries marked "seam" read the tile's authority for a
// principal and are the amendments this card names: they move to the
// principal-aware helpers of edgepolicy.go.
func TestEdgePolicyCallers(t *testing.T) {
	allowed := map[string]map[string]string{
		"grantedRole": {
			"resolveTarget":   "the single evaluation point itself",
			"governanceRole":  "governance targets, for a principal that isn't a non-primary one",
			"viewGrant":       "the primary's view; a non-primary view goes through resolveTarget",
			"allowRes":        "the tile's authority; a non-primary principal then meets resEdge",
			"Pending":         "unsatisfied uses declarations: no call",
			"EnvFor":          "the tile's resource env, which every deployment keeps (09-fabric §5.6); seam for the workspace fs block (WP-40)",
			"OpenLinksFor":    "seam: the document's deployment (the server's SandboxExtras)",
			"codeGrantAllows": "the code grants, for a principal that isn't a non-primary one (codeReadAllowed)",
			"SandboxesFor":    "cap:sandboxes is the tile's (05-model §12); tilesbx refuses a non-main deployment's principal (keyOf) before it counts",
			"capChanged":      "a cap: grant row changed: the tile's held state, for OnCapChange; no call",
			"capSweep":        "the policy ceiling moved: the tile's held cap: rows, for OnCapChange; no call",
			"ResourceMount":   "main's tile sandboxes mount the tile's own scope in main's namespace, as EnvFor's env; a deployment's own set would go through resolveTarget (tile-sandbox-runtime.md §14)",
		},
		"httpBindingRole": {
			"grantedRole": "the tile's authority, merging bindings with grant rows",
		},
		"codeGrantAllows": {
			"codeReadAllowed": "the primary's code reads; a non-primary principal's go through resolveTarget",
			"CodeReadGrant":   "seam: the server's /c/ plane passes no principal",
		},
	}
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := ce.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				callers, watched := allowed[sel.Sel.Name]
				if !watched {
					return true
				}
				seen[sel.Sel.Name+"←"+fn.Name.Name] = true
				if _, ok := callers[fn.Name.Name]; !ok {
					t.Errorf("%s: %s calls %s outside the edge policy's allow-list: go through resolveTarget (09-fabric §5.3)",
						fset.Position(ce.Pos()), fn.Name.Name, sel.Sel.Name)
				}
				return true
			})
		}
	}
	for callee, callers := range allowed {
		for caller := range callers {
			if !seen[callee+"←"+caller] {
				t.Errorf("the allow-list names %s calling %s, which no longer does: drop the entry", caller, callee)
			}
		}
	}
}
