package apicheck

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// inventory is every /api/xbin route pattern xbind can mount, → where it is
// registered: what the fixture mounts (the broker's planes, the server's
// core API), internal/boot's literals, and every RegisterAPI literal under
// cmd/ and internal/ (TestRouteInventory's three sources).
func inventory(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range mount(t).APIRoutes() {
		out[p] = "RegisterAPI (mounted)"
	}
	for _, p := range mainRoutes(t) {
		out[p] = "internal/boot"
	}
	for p, f := range sourceRoutes(t) {
		out[p] = filepath.ToSlash(f)
	}
	return out
}

// covers D127r T9 NP-09-4 NP-06-1 NP-04-8 NP-13-11 — TestDeploymentRouteClasses,
// the guard (09-fabric §6's enforcement): every /api/xbin route xbind mounts
// has a class in internal/server/deployclass.go (deployment-scoped,
// primary-only or neutral), and every row of the table names a route that
// exists. Its "refusals" subtest drives the table through handleAPI with
// real credentials.
func TestDeploymentRouteClasses(t *testing.T) {
	inv := inventory(t)
	if len(inv) < 100 {
		t.Fatalf("an inventory of %d routes — is the fixture wiring intact?", len(inv))
	}
	table := server.RouteClasses()
	var problems []string
	for p, where := range inv {
		if table[p] == server.Unclassified {
			problems = append(problems, where+" registers "+p+", which has no class in internal/server/deployclass.go: "+
				"add a row (deployment-scoped, primary-only or neutral), or non-primary deployments' credentials get 403 on it")
		}
	}
	for p, c := range table {
		if _, ok := inv[p]; !ok {
			problems = append(problems, "internal/server/deployclass.go classes "+p+" ("+c.String()+"), which no code registers: drop the row")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	t.Run("refusals", func(t *testing.T) { classRefusals(t, inv) })
}

// twoDeployments is a Policy whose apps/crm has the deployments main and
// dev, primary as the field names; its Addressed is the plane's rule
// (deployments.Plane.Addressed) without protection.
type twoDeployments struct {
	server.NoopPolicy
	primary string
}

func (d twoDeployments) HasDeployment(tile, name string) bool {
	return name == util.MainDeployment || (tile == "apps/crm" && name == "dev")
}

func (d twoDeployments) Primary(tile string) string {
	if tile == "apps/crm" {
		return d.primary
	}
	return util.MainDeployment
}

func (d twoDeployments) Addressed(p auth.Principal, tile string) (string, error) {
	switch {
	case p.Component != tile:
		return d.Primary(tile), nil
	case p.Deployment == "" && p.Via != "terminal":
		return util.MainDeployment, nil
	case p.Deployment == "":
		return d.Primary(tile), nil
	case !d.HasDeployment(tile, p.Deployment):
		return "", util.NoDeployment(tile, p.Deployment)
	}
	return p.Deployment, nil
}

// probePattern is a route no code registers, so it has no class: the
// fixture mounts it to show what an unconverted route does.
const probePattern = "/zz-unclassified-probe"

// classRefusals mounts every inventory route on a server of its own, each
// answering which pattern ran unless the server's own core API already
// holds it, plus two routes without a class, and calls each with the
// credentials of apps/crm's deployments (D127r):
//   - dev's instance, frame (its xbin.window documents' too) and terminal
//     tokens, and an instance token of a removed deployment, are refused on
//     every primary-only route and on the unclassified ones, reads included,
//     before any handler runs; they reach every deployment-scoped and
//     neutral route, except that deciding a PR is refused to dev's backend;
//   - the primary's instance and frame tokens, a session following the
//     primary and the owner reach everything, whichever deployment is the
//     primary: once dev is, main's credentials are the refused ones;
//   - the audit line of a non-main credential names its deployment.
func classRefusals(t *testing.T, inv map[string]string) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	root := t.TempDir()
	for rel, body := range map[string]string{
		"xbin.json":            `{"schema":1}`,
		"apps/crm/xbin.json":   `{}`,
		"apps/crm/index.html":  "<!doctype html><p>crm</p>\n",
		"apps/other/xbin.json": `{}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	pol := &twoDeployments{primary: util.MainDeployment}
	srv := &server.Server{Reg: reg, Auth: a, Hub: events.NewHub()}
	srv.InstallPolicy(pol)
	h := srv.Handler()
	core := map[string]bool{}
	for _, p := range srv.APIRoutes() {
		core[p] = true
	}
	ran := func(pattern string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			server.WriteJSON(w, http.StatusOK, map[string]string{"ran": pattern})
		}
	}
	var stubs []string
	for p := range inv {
		if !core[p] {
			srv.RegisterAPI(p, ran(p))
			stubs = append(stubs, p)
		}
	}
	probes := []string{"GET " + probePattern, "POST " + probePattern}
	for _, p := range probes {
		srv.RegisterAPI(p, ran(p))
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	a.RegisterInstanceDeployment("inst-dev", "apps/crm", "dev")
	a.RegisterInstanceDeployment("inst-gone", "apps/crm", "gone")
	a.RegisterInstance("inst-main", "apps/crm")
	a.RegisterInstance("inst-other", "apps/other")
	type cred struct {
		name   string
		header http.Header
		dep    string // the deployment it is bound to
	}
	bearer := func(tok string) http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }
	frame := func(tok string) http.Header { return http.Header{auth.FrameTokenHeader: {tok}} }
	creds := []cred{
		{"dev's instance", bearer("inst-dev"), "dev"},
		{"dev's frame", frame(a.MintFrameTokenDeployment("apps/crm", "", "dev", time.Hour)), "dev"},
		{"dev's xbin.window document", frame(a.MintFrameTokenDeployment("apps/crm/editor", "", "dev", time.Hour)), "dev"},
		{"a session targeting dev", bearer(a.MintTerminalTarget("apps/crm", "", "dev")), "dev"},
		{"a removed deployment's instance", bearer("inst-gone"), "gone"},
		{"main's instance", bearer("inst-main"), util.MainDeployment},
		{"main's frame", frame(a.MintFrameToken("apps/crm", "", time.Hour)), util.MainDeployment},
		{"a session targeting main", bearer(a.MintTerminalTarget("apps/crm", "", util.MainDeployment)), util.MainDeployment},
		{"a session following the primary", bearer(a.MintTerminalTarget("apps/crm", "", "")), ""},
		{"another tile's instance", bearer("inst-other"), util.MainDeployment},
		{"the owner", bearer(a.OwnerTokenValue()), ""},
	}
	call := func(c cred, pattern string) (int, string) {
		t.Helper()
		method, path, _ := strings.Cut(pattern, " ")
		var segs []string
		for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
			switch {
			case strings.HasSuffix(s, "...}"):
				segs = append(segs, "x", "y")
			case strings.HasPrefix(s, "{"):
				segs = append(segs, "x")
			default:
				segs = append(segs, s)
			}
		}
		req, err := http.NewRequest(method, ts.URL+"/api/xbin/"+strings.Join(segs, "/"), strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range c.header {
			req.Header[k] = v
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.name, pattern, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	const (
		unclassifiedText = "this route isn't available to a non-primary deployment's credentials yet"
		primaryOnlyText  = "this route is the primary's alone: a non-primary deployment's credentials can't use it"
		prText           = "deciding a PR is the primary's act"
	)
	// check drives every stub and probe, and every core route a credential
	// bound elsewhere than the primary is refused on (a refused call runs no
	// handler; the core API's own handlers need a daemon behind them).
	check := func(primary string) {
		t.Helper()
		pol.primary = primary
		for _, c := range creds {
			np := c.dep != "" && c.dep != primary && !strings.HasPrefix(c.name, "another tile") && c.name != "the owner"
			all := append(append([]string{}, stubs...), probes...)
			for p := range core {
				all = append(all, p)
			}
			for _, pattern := range all {
				class := server.RouteClassOf(pattern)
				refused := np && (class == server.Unclassified || class == server.PrimaryOnly ||
					(pattern == "POST /code/pr/state" && strings.HasSuffix(c.name, "instance")))
				if core[pattern] && !refused {
					continue
				}
				code, body := call(c, pattern)
				var want string
				switch {
				case !refused:
					want = `"ran":"` + pattern + `"`
				case c.dep == "gone":
					want = `apps/crm has no deployment \"gone\"`
				case class == server.Unclassified:
					want = unclassifiedText + " (" + c.dep + ")"
				case class == server.PrimaryOnly:
					want = primaryOnlyText + " (" + c.dep + ")"
				default:
					want = prText
				}
				wantCode := http.StatusOK
				switch {
				case refused && c.dep == "gone":
					wantCode = http.StatusNotFound
				case refused:
					wantCode = http.StatusForbidden
				}
				if code != wantCode || !strings.Contains(body, want) {
					t.Errorf("primary %s, %s → %s (%s): %d %.200s\n  want %d with %s", primary, c.name, pattern, class, code, body, wantCode, want)
				}
			}
		}
	}
	check(util.MainDeployment)
	check("dev") // reassigned: main's credentials are now the non-primary ones

	// The audit line of a credential bound elsewhere than main names its
	// deployment; a main-bound one's is today's.
	pol.primary = util.MainDeployment
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	for _, c := range []cred{creds[0], creds[4]} {
		buf.Reset()
		if code, body := call(c, "PUT /cron/jobs"); code != http.StatusOK {
			t.Fatalf("%s PUT /cron/jobs: %d %s", c.name, code, body)
		}
		var line map[string]any
		for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "audit" {
				line = m
			}
		}
		if line == nil {
			t.Fatalf("%s: no audit line: %q", c.name, buf.String())
		}
		want := map[string]any{"dev": "dev", util.MainDeployment: nil}[c.dep]
		if line["deployment"] != want {
			t.Errorf("%s: the audit line's deployment = %v, want %v (%s)", c.name, line["deployment"], want, buf.String())
		}
	}
}
