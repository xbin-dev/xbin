package boot

// sealedroutes_test.go — what every /api/xbin route answers about a sealed
// component (D227), through a real daemon run as a production xbind: auth
// on, not --dev, the fixture's backend prebuilt as a release ships it.
//
// The Refused and Self rows of server/sealedclass.go are driven from the
// table itself, one request per parameter the row names, as the owner (an
// admin: nobody gets further) — so a row can't claim a refusal the gate
// doesn't make. The Handled rows have a case each below, and a row without
// one fails the test. internal/apicheck's TestSealedRouteClasses keeps the
// table complete over the route inventory.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sealed"
	"github.com/xbin-dev/xbin/internal/server"
)

const (
	probePath = "xbin/probe"
	probeRes  = "res:xbin/probe/state"
	systemMsg = " is a system component, managed by xbin: "
)

func probeSpec() sealed.Spec {
	return sealed.Spec{Name: "probe", Title: "Probe", Source: sealedCounter("r1"), Authored: "apps/counter",
		Slots: []string{"llm"}, Providers: map[string][]string{"llm": {"apps/prov"}}, Grants: []registry.Use{{Target: "cap:open-links", Role: "writer"}}}
}

// probeWorkspace is a workspace with two ordinary tiles beside the sealed
// component: apps/plain (asks for an llm slot) and apps/prov (provides one).
func probeWorkspace(t *testing.T) string {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"apps/plain/index.html": "<html><body>plain</body></html>",
		"apps/plain/xbin.json":  `{"interfaces":{"llm":{"kind":"http","service":"openai"}}}`,
		"apps/prov/index.html":  "<html><body>prov</body></html>",
		"apps/prov/xbin.json":   `{"provides":{"api":{"kind":"http","service":"openai","role":"reader"}},"expose":{"roles":{"reader":"read"}}}`,
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

var wildcardRe = regexp.MustCompile(`\{[a-zA-Z]+\}`)

// routeURL fills a route pattern's wildcards: rest for a {x...} tail, "x"
// for the others.
func routeURL(pattern, rest string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	if i := strings.Index(path, "{rest...}"); i >= 0 {
		path = path[:i] + rest
	}
	if i := strings.Index(path, "{path...}"); i >= 0 {
		path = path[:i] + rest
	}
	return method, wildcardRe.ReplaceAllString(path, "x")
}

func TestSealedRouteAnswers(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace and builds a backend")
	}
	quiet(t)
	ws := probeWorkspace(t)
	table := []sealed.Spec{probeSpec()}
	d := startSealed(t, ws, table, production(prebuilt(t, table)))
	api := func(method, path string, body any, as ...string) (int, string) {
		return d.do(method, "/api/xbin"+path, body, as...)
	}
	reader := d.addUser("reader", map[string]string{probePath: "read", "apps/plain": "read"})
	nobody := d.addUser("nobody", nil)
	d.addUser("levels", nil)

	rules := server.SealedRules()
	patterns := make([]string, 0, len(rules))
	for p := range rules {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)

	// ---- every Refused and Self row, one request per named parameter ----
	t.Run("refused and self rows", func(t *testing.T) {
		n := 0
		for _, pat := range patterns {
			rule := rules[pat]
			if rule.Class != server.SealedRefused && rule.Class != server.SealedSelf {
				continue
			}
			want, where := http.StatusBadRequest, "refused"
			if rule.Class == server.SealedSelf {
				want, where = http.StatusForbidden, "its own alone"
			}
			type probe struct {
				what, path string
				body       any
			}
			var probes []probe
			method, base := routeURL(pat, probePath+"/k")
			if rule.Rest {
				_, kv := routeURL(pat, probeRes+"/k")
				probes = append(probes, probe{"the path " + probePath, base, nil}, probe{"the resource " + probeRes, kv, nil})
			}
			for _, q := range rule.Query {
				probes = append(probes, probe{"?" + q + "=", base + "?" + q + "=" + url.QueryEscape(probePath), nil},
					probe{"?" + q + "= (a path below it)", base + "?" + q + "=" + url.QueryEscape("/"+probePath+"/sub/"), nil})
			}
			for _, f := range rule.Body {
				probes = append(probes, probe{"body " + f, base, map[string]any{f: probePath}},
					probe{"body " + f + " (its resource)", base, map[string]any{f: probeRes}},
					probe{"body " + f + " (a list)", base, map[string]any{f: []string{"apps/plain", probePath}}},
					probe{"body " + strings.ToUpper(f) + " (the key in another case, which the handler's decoder reads too)", base, map[string]any{strings.ToUpper(f): probePath}})
			}
			for _, p := range probes {
				n++
				code, body := api(method, p.path, p.body)
				if code != want || !strings.Contains(body, systemMsg+rule.What) || !strings.Contains(body, server.SealedDocs) {
					t.Errorf("%s with %s: %d %s\n\twant %d (%s): …%s%s", pat, p.what, code, body, want, where, systemMsg, rule.What)
				}
			}
			// and the same route about a tile is not refused this way
			tile := map[string]any{}
			for _, f := range rule.Body {
				tile[f] = "apps/plain"
			}
			q := url.Values{}
			for _, k := range rule.Query {
				q.Set(k, "apps/plain")
			}
			_, tpath := routeURL(pat, "apps/plain/k")
			if len(q) > 0 {
				tpath += "?" + q.Encode()
			}
			var tbody any
			if len(tile) > 0 {
				tbody = tile
			}
			if _, body := api(method, tpath, tbody); strings.Contains(body, "is a system component") {
				t.Errorf("%s about a tile is answered as for a system component: %s", pat, body)
			}
		}
		if n < 150 {
			t.Errorf("only %d refusals driven: is the table intact?", n)
		}
	})

	// ---- Self rows: its backend's credential passes; its page's doesn't ----
	t.Run("its own credentials", func(t *testing.T) {
		if code, body := d.ownCall(probePath, "PUT", "/api/xbin/kv/"+probeRes+"/k", "v"); code != 200 {
			t.Errorf("its backend's kv write: %d %s", code, body)
		}
		if code, body := d.ownCall(probePath, "GET", "/api/xbin/kv/"+probeRes+"/k", nil); code != 200 {
			t.Errorf("its backend's kv read: %d %s", code, body)
		}
		if code, body := api("GET", "/kv/"+probeRes+"/k", nil); code != 403 || !strings.Contains(body, systemMsg) {
			t.Errorf("an admin's read of its kv: %d %s", code, body)
		}
		framed := func(component, method, path string, body any) (int, string) {
			code, out := api("GET", "/frame-token?component="+component, nil)
			var ft struct{ Token string }
			if code != 200 || json.Unmarshal([]byte(out), &ft) != nil || ft.Token == "" {
				t.Fatalf("the frame token of %s: %d %s", component, code, out)
			}
			req, _ := http.NewRequest(method, d.base+path, strings.NewReader(mustJSON(body)))
			req.Header.Set("X-XBin-Frame-Token", ft.Token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			return resp.StatusCode, readAll(resp)
		}
		// its page — the root token's own frame of it — holds a viewer's
		// token: the component's backend, its own renewal, and nothing else
		const viewer = "its page reaches its own backend only"
		for _, c := range []struct{ method, path string }{
			{"GET", "/api/xbin/kv/" + probeRes + "/k"}, {"PUT", "/api/xbin/kv/" + probeRes + "/k"},
			{"POST", "/api/xbin/bus/publish"}, {"PUT", "/api/xbin/cron/jobs"}, {"POST", "/api/xbin/tile-report"},
			{"GET", "/api/xbin/components"}, {"GET", "/api/xbin/users"}, {"GET", "/api/xbin/llm/v1/models"},
			{"GET", "/api/apps/plain/x"}, {"GET", "/api/xbin/probe/../kv/" + probeRes + "/k"}, {"GET", "/c/apps/plain/index.html"},
		} {
			if code, body := framed(probePath, c.method, c.path, "v"); code != 403 || !strings.Contains(body, viewer) {
				t.Errorf("its page's %s %s: %d %s\n\twant 403: %s", c.method, c.path, code, body, viewer)
			}
		}
		if code, body := framed(probePath, "GET", "/api/"+probePath+"/hello", nil); code != 200 || !strings.Contains(body, "self="+probePath) {
			t.Errorf("its page's call to its own backend: %d %s", code, body)
		}
		if code, body := framed(probePath, "GET", "/api/xbin/frame-token?component="+probePath, nil); code != 200 {
			t.Errorf("its page's renewal of its token: %d %s", code, body)
		}
		if code, body := framed(probePath, "GET", "/api/xbin/frame-token?component=apps/plain", nil); code != 403 {
			t.Errorf("its page asking for another tile's token: %d %s", code, body)
		}
		if code, body := framed(probePath, "GET", "/c/"+probePath+"/app.js", nil); code != 200 {
			t.Errorf("its page's own script: %d %s", code, body)
		}
		// another tile's credentials are not its own
		if code, body := framed("apps/plain", "GET", "/api/xbin/kv/"+probeRes+"/k", nil); code != 403 || !strings.Contains(body, systemMsg) {
			t.Errorf("another tile's read of its kv: %d %s", code, body)
		}
		// one key twice in a body is refused by the gate before anything
		// reads it two ways ({"component":"xbin/x","component":null})
		req, _ := http.NewRequest("PUT", d.base+"/api/xbin/cron/jobs", strings.NewReader(`{"component":"`+probePath+`","component":null,"name":"j","schedule":"@every 1h","path":"/hello","role":"reader"}`))
		req.Header.Set("Authorization", "Bearer "+d.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if b := readAll(resp); resp.StatusCode != 400 || !strings.Contains(b, "names component twice") {
			t.Errorf("a body naming component twice: %d %s", resp.StatusCode, b)
		}
		resp.Body.Close()
		if code, body := api("GET", "/cron/jobs?sealed=1", nil); code != 200 || strings.Contains(body, probePath) {
			t.Errorf("a job was registered for it: %d %s", code, body)
		}
	})

	// ---- the Handled rows ----
	covered := map[string]bool{}
	on := func(patterns ...string) {
		for _, p := range patterns {
			if rules[p].Class != server.SealedHandled {
				t.Errorf("the test has a case for %s, which isn't a handled row", p)
			}
			covered[p] = true
		}
	}
	expect := func(t *testing.T, what string, code int, body string, want int, has ...string) {
		t.Helper()
		ok := code == want
		for _, h := range has {
			ok = ok && strings.Contains(body, h)
		}
		if !ok {
			t.Errorf("%s: %d %s\n\twant %d with %q", what, code, body, want, has)
		}
	}
	lists := func(body string) bool { return strings.Contains(body, probePath) }

	t.Run("access is read or none", func(t *testing.T) {
		on("PUT /access", "GET /access", "POST /access-requests", "POST /access-requests/approve", "DELETE /access-requests")
		for level, want := range map[string]int{"read": 200, "": 200, "none": 200, "write": 400, "terminal": 400} {
			code, body := api("PUT", "/access", map[string]any{"tile": probePath, "kind": "user", "id": "levels", "level": level})
			expect(t, "PUT /access level "+level, code, body, want)
			if want == 400 && !strings.Contains(body, "read or none") {
				t.Errorf("PUT /access level %s: %s", level, body)
			}
		}
		code, body := api("PUT", "/access", map[string]any{"tile": probePath, "kind": "org", "id": "nosuch", "level": "terminal"})
		expect(t, "an org's terminal level", code, body, 400, "read or none")
		code, body = api("GET", "/access?tile="+probePath, nil)
		expect(t, "GET /access", code, body, 200, `"reader"`)
		code, body = api("POST", "/access-requests", map[string]any{"tile": probePath, "level": "write"}, nobody)
		expect(t, "asking for write", code, body, 400, systemMsg)
		code, body = api("POST", "/access-requests", map[string]any{"tile": probePath, "level": "read"}, nobody)
		expect(t, "asking for read", code, body, 200)
		code, body = api("POST", "/access-requests/approve", map[string]any{"tile": probePath, "user": "nobody", "level": "terminal"})
		expect(t, "approving at terminal", code, body, 400, "read or none")
		code, body = api("DELETE", "/access-requests", map[string]any{"tile": probePath, "user": "nobody"})
		expect(t, "dismissing the request", code, body, 200)
	})

	t.Run("levels above read don't exist", func(t *testing.T) {
		on("POST /users", "PATCH /users/{id}", "PUT /defaults", "PUT /permission-sets/{name}", "PATCH /orgs/{org}", "POST /orgs", "GET /access-matrix")
		// an entry that names it above read is refused on every route that
		// writes entries; a pattern that reaches it is kept, and gives read
		code, body := api("POST", "/users", map[string]any{"id": "named", "name": "named", "role": "user", "password": "pw-named-pw-pw",
			"tiles": map[string]string{probePath: "terminal"}})
		expect(t, "a new user with an entry at terminal", code, body, 400, "is a system component, managed by xbin", "read or none")
		code, body = api("POST", "/users", map[string]any{"id": "wide", "name": "wide", "role": "user", "password": "pw-wide-pw-pw",
			"tiles": map[string]string{"*": "terminal", "xbin/*": "write", probePath: "read"}})
		expect(t, "a user with terminal everywhere by pattern", code, body, 200)
		code, body = api("PATCH", "/users/wide", map[string]any{"tiles": map[string]string{"*": "terminal", probePath: "write"}})
		expect(t, "PATCH /users with an entry at write", code, body, 400, "read or none")
		code, body = api("PUT", "/defaults", map[string]any{"defaultTiles": map[string]string{probePath: "terminal"}})
		expect(t, "PUT /defaults with an entry at terminal", code, body, 400, "read or none")
		code, body = api("PUT", "/defaults", map[string]any{"newUsers": map[string]any{"tiles": map[string]string{probePath: "write"}}})
		expect(t, "PUT /defaults: new users' entry at write", code, body, 400, "read or none")
		_, was := api("GET", "/defaults", nil)
		var defaults struct {
			DefaultTiles map[string]string `json:"defaultTiles"`
		}
		_ = json.Unmarshal([]byte(was), &defaults)
		code, body = api("PUT", "/defaults", map[string]any{"defaultTiles": map[string]string{"xbin/*": "terminal"}})
		expect(t, "PUT /defaults with a pattern", code, body, 200)
		if code, body = api("GET", "/frame-token?component="+probePath, nil, nobody); code != 200 {
			t.Errorf("a default pattern over xbin/* gives nobody read: %d %s", code, body)
		}
		if code, body = d.do("GET", "/ws/term?cwd="+probePath, nil, nobody); code != 403 {
			t.Errorf("…and a terminal, at its terminal level: %d %s", code, body)
		}
		code, body = api("PUT", "/defaults", map[string]any{"defaultTiles": defaults.DefaultTiles})
		expect(t, "putting the defaults back", code, body, 200)
		code, body = api("POST", "/orgs", map[string]any{"id": "team", "name": "Team"})
		expect(t, "POST /orgs", code, body, 200)
		code, body = api("PATCH", "/orgs/team", map[string]any{"name": "The team"})
		if code >= 500 {
			t.Errorf("PATCH /orgs: %d %s", code, body)
		}
		code, body = api("PUT", "/access", map[string]any{"tile": probePath, "kind": "org", "id": "team", "level": "write"})
		expect(t, "an org's entry at write", code, body, 400, "read or none")
		code, body = api("PUT", "/permission-sets/everything", map[string]any{"tiles": map[string]string{"*": "terminal"}})
		if code >= 500 {
			t.Errorf("PUT /permission-sets: %d %s", code, body)
		}
		code, body = api("GET", "/access-matrix", nil)
		expect(t, "GET /access-matrix", code, body, 200)
		if lists(body) {
			t.Errorf("the access matrix lists the system component unasked: %s", body)
		}
		code, body = api("GET", "/access-matrix?sealed=1", nil)
		var m struct {
			Matrix map[string]map[string]struct{ Level string }
		}
		if code != 200 || json.Unmarshal([]byte(body), &m) != nil {
			t.Fatalf("GET /access-matrix?sealed=1: %d %s", code, body)
		}
		for _, u := range []string{"wide", "reader", "nobody"} {
			if l := m.Matrix[u][probePath].Level; l != "read" && l != "" {
				t.Errorf("%s has level %q on the system component", u, l)
			}
		}
		if m.Matrix["wide"][probePath].Level != "read" || m.Matrix["wide"]["apps/plain"].Level != "terminal" {
			t.Errorf("wide: %+v", m.Matrix["wide"])
		}
	})

	t.Run("lifecycle", func(t *testing.T) {
		on("POST /lifecycle")
		for state, want := range map[string]int{"disabled": 200, "hidden": 200, "enabled": 200, "offloaded": 400, "offloaded-full": 400} {
			code, body := api("POST", "/lifecycle", map[string]any{"component": probePath, "state": state})
			expect(t, "state "+state, code, body, want)
		}
		code, body := api("POST", "/lifecycle", map[string]any{"component": probePath, "state": "enabled"})
		expect(t, "enabled again", code, body, 200)
	})

	t.Run("bindings: declared slots, outward", func(t *testing.T) {
		code, body := api("POST", "/bindings", map[string]any{"component": probePath, "slot": "llm", "provider": "apps/prov"})
		expect(t, "binding its declared slot", code, body, 200)
		code, body = api("POST", "/bindings", map[string]any{"component": probePath, "slot": "net", "provider": "internet"})
		expect(t, "a slot the table doesn't give it", code, body, 400, systemMsg+"its release names the providers its net slot may be bound to")
		code, body = api("POST", "/bindings", map[string]any{"component": probePath, "slot": "llm", "provider": "apps/plain"})
		expect(t, "its declared slot, to a provider its release doesn't name", code, body, 400, systemMsg+"its release names the providers its llm slot may be bound to, and apps/plain is not one")
		code, body = api("POST", "/bindings", map[string]any{"component": probePath, "slot": "@archive", "provider": "apps/prov"})
		expect(t, "a pseudo-slot", code, body, 400, systemMsg)
		code, body = api("POST", "/bindings", map[string]any{"component": "apps/plain", "slot": "llm", "provider": probePath})
		expect(t, "it as a provider", code, body, 400, systemMsg+"it is never the provider of a binding")
		code, body = api("POST", "/bindings", map[string]any{"component": "apps/plain", "slot": "llm", "providers": []string{"apps/prov", probePath + "#x"}})
		expect(t, "it among providers", code, body, 400, systemMsg)
	})

	t.Run("limits, as a tile's", func(t *testing.T) {
		on("GET /limits", "POST /limits", "DELETE /limits", "POST /partitions/limits")
		code, body := api("GET", "/limits?tile="+probePath, nil)
		expect(t, "GET /limits", code, body, 200, `"tile":"xbin/probe"`)
		code, body = api("POST", "/limits", map[string]any{"tile": probePath, "kind": "diskGiB", "value": 60, "source": "admin-set"})
		expect(t, "POST /limits", code, body, 200)
		code, body = api("DELETE", "/limits?tile="+probePath+"&kind=diskGiB", nil)
		expect(t, "DELETE /limits", code, body, 200)
		code, body = api("POST", "/partitions/limits", map[string]any{"tile": probePath, "maxRunning": 3})
		expect(t, "POST /partitions/limits", code, body, 200)
	})

	t.Run("it runs, for the people who have access", func(t *testing.T) {
		on("GET /logs", "GET /tile-status", "GET /frame-token", "GET /components/{path...}", "GET /tile-assets", "GET /owner")
		code, body := d.do("GET", "/api/"+probePath+"/hello", nil)
		expect(t, "the owner's call", code, body, 200, "r1 self=xbin/probe")
		if _, err := os.Stat(filepath.Join(ws, ".xbin", "build")); err == nil {
			ents, _ := os.ReadDir(filepath.Join(ws, ".xbin", "build"))
			if len(ents) > 0 {
				t.Errorf("a production xbind compiled something: %v", ents)
			}
		}
		// people reach it as they reach a tile: through its own page, whose
		// frame token only someone with read access gets
		frame := func(component, as string) string {
			code, body := api("GET", "/frame-token?component="+component, nil, as)
			var ft struct{ Token string }
			if code != 200 || json.Unmarshal([]byte(body), &ft) != nil {
				return ""
			}
			return ft.Token
		}
		call := func(token string) (int, string) {
			req, _ := http.NewRequest("GET", d.base+"/api/"+probePath+"/hello", nil)
			req.Header.Set("X-XBin-Frame-Token", token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			return resp.StatusCode, readAll(resp)
		}
		code, body = call(frame(probePath, reader))
		expect(t, "a reader's call from its page", code, body, 200, "r1 self=xbin/probe from=xbin/probe")
		if tok := frame(probePath, nobody); tok != "" {
			t.Error("someone without access got its frame token")
		}
		// never a target: no tile's code calls into it, whoever drives the tile
		for who, as := range map[string]string{"the owner": d.token, "a reader of it": reader} {
			code, body = call(frame("apps/plain", as))
			if code != 403 {
				t.Errorf("a tile's frame driven by %s called into it: %d %s", who, code, body)
			}
		}
		code, body = d.do("GET", "/c/"+probePath+"/app.js", nil, reader)
		expect(t, "a reader's page load", code, body, 200, "r1")
		if code, body = d.do("GET", "/c/"+probePath+"/app.js", nil, nobody); code == 200 {
			t.Errorf("its files are served to someone without access: %s", body)
		}
		code, body = api("GET", "/logs?component="+probePath, nil)
		expect(t, "an admin's read of its log", code, body, 200, "gen 1 start")
		code, body = api("GET", "/logs?component="+probePath, nil, reader)
		expect(t, "a reader's read of its log", code, body, 403)
		code, body = api("GET", "/tile-status?component="+probePath, nil)
		expect(t, "GET /tile-status", code, body, 200, `"component":"xbin/probe"`)
		code, body = api("GET", "/components/"+probePath, nil)
		expect(t, "GET /components/<path>", code, body, 200, `"sealed":true`, `"title":"Probe"`)
		if strings.Contains(body, `"roles"`) || strings.Contains(body, `"chrome":true`) {
			t.Errorf("it exposes roles or runs as chrome: %s", body)
		}
		code, body = api("GET", "/tile-assets?component="+probePath, nil)
		expect(t, "GET /tile-assets by name", code, body, 200, probePath)
		if code, body = api("GET", "/tile-assets", nil); lists(body) {
			t.Errorf("the asset report lists it unasked: %s", body)
		}
		code, body = api("GET", "/owner?tile="+probePath, nil)
		expect(t, "GET /owner", code, body, 200, `"owner":""`)
	})

	t.Run("listed only when asked", func(t *testing.T) {
		on("GET /components", "GET /auth-overview", "GET /backends", "GET /runtime", "GET /grants", "GET /vaults",
			"GET /cron/jobs", "GET /bus/subscriptions", "GET /resources", "GET /sealed")
		for _, route := range []string{"/components", "/auth-overview", "/backends", "/runtime", "/grants", "/resources", "/cron/jobs", "/bus/subscriptions", "/vaults"} {
			code, body := api("GET", route, nil)
			if code != 200 || lists(body) {
				t.Errorf("GET %s lists the system component unasked (an older console would show it): %d %.300s", route, code, body)
			}
		}
		for _, route := range []string{"/components", "/auth-overview", "/backends", "/runtime", "/grants", "/resources"} {
			code, body := api("GET", route+"?sealed=1", nil)
			if code != 200 || !lists(body) {
				t.Errorf("GET %s?sealed=1 doesn't list it: %d %.300s", route, code, body)
			}
		}
		if code, body := api("GET", "/vaults?sealed=1", nil); lists(body) {
			t.Errorf("its secrets are listed: %d %s", code, body)
		}
		// a reader sees its row when asking; someone without access never
		if _, body := api("GET", "/components?sealed=1", nil, reader); !lists(body) {
			t.Errorf("a reader's /components?sealed=1: %s", body)
		}
		if _, body := api("GET", "/components?sealed=1", nil, nobody); lists(body) {
			t.Errorf("/components?sealed=1 without access: %s", body)
		}
		code, body := api("GET", "/sealed", nil)
		var out struct {
			Release    string
			Components []sealedRow
		}
		if code != 200 || json.Unmarshal([]byte(body), &out) != nil || len(out.Components) != 1 {
			t.Fatalf("GET /sealed: %d %s", code, body)
		}
		row := out.Components[0]
		if row.Path != probePath || row.Title != "Probe" || row.Build != "release" || row.State != "enabled" || row.Hold != nil ||
			row.Backend != "healthy" || len(row.Digest) != 24 || row.Stamp == nil || row.Stamp.Data != 1 || row.DataFormat != 1 ||
			len(row.Slots) != 1 || row.Slots[0].Slot != "llm" || len(row.Slots[0].Bound) != 1 || row.Slots[0].Bound[0] != "apps/prov" ||
			len(row.Grants) != 1 || row.Grants[0].Target != "cap:open-links" {
			t.Errorf("GET /sealed row: %+v", row)
		}
		code, body = api("GET", "/sealed", nil, reader)
		expect(t, "a reader's GET /sealed", code, body, 403)
	})

	t.Run("backups: its data, through the workspace's archiver", func(t *testing.T) {
		on("POST /backup", "POST /restore", "POST /backup-schedule", "DELETE /backup-schedule", "POST /backup/erase", "GET /backups")
		// no archiver here: the answers are a tile's (the archive's content
		// is the broker's test, TestSealedBackupIsDataOnly)
		code, body := api("POST", "/backup", map[string]any{"component": probePath})
		expect(t, "POST /backup", code, body, 502, "no archiver bound")
		code, body = api("POST", "/restore", map[string]any{"component": probePath})
		expect(t, "POST /restore", code, body, 502, "no archiver bound")
		code, body = api("POST", "/restore", map[string]any{"component": probePath, "file": "data/kv.json"})
		expect(t, "reading a file out of its backup", code, body, 400, systemMsg+"its backup is restored whole")
		code, body = api("GET", "/backups?component="+probePath, nil)
		expect(t, "GET /backups", code, body, 200, `"versions":[]`)
		code, body = api("POST", "/backup-schedule", map[string]any{"component": probePath, "schedule": "@daily"})
		expect(t, "POST /backup-schedule", code, body, 200)
		code, body = api("DELETE", "/backup-schedule?component="+probePath, nil)
		expect(t, "DELETE /backup-schedule", code, body, 200)
		code, body = api("POST", "/backup/erase", map[string]any{"component": probePath, "what": "data"})
		expect(t, "POST /backup/erase", code, body, 200)
	})

	t.Run("as for a tile", func(t *testing.T) {
		on("GET /llm/usage")
		if code, body := api("GET", "/llm/usage?component="+probePath, nil); code != 200 {
			t.Errorf("GET /llm/usage?component=%s: %d %s", probePath, code, body)
		}
		on("GET /sandboxes", "GET /term/sessions", "GET /agent/history", "PUT /screens/default", "PUT /screens/folders", "PUT /screens/org",
			"GET /partitions", "GET /partitions/backups", "GET /partitions/ledger", "POST /partitions/purge", "POST /partitions/reset",
			"POST /partitions/restore", "POST /partitions/stop", "POST /partitions/share-log", "DELETE /partitions/share-log",
			"POST /partitions/binds", "DELETE /partitions/binds", "GET /partitions/binds")
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/sandboxes?tile=" + probePath, nil},
			{"GET", "/term/sessions?cwd=" + probePath, nil},
			{"GET", "/agent/history?cwd=" + probePath, nil},
			{"PUT", "/screens/default", map[string]any{"tiles": []string{probePath}}},
			{"GET", "/partitions?tile=" + probePath, nil},
			{"GET", "/partitions/backups?tile=" + probePath + "&user=reader", nil},
			{"GET", "/partitions/ledger?tile=" + probePath, nil},
			{"POST", "/partitions/purge", map[string]any{"tile": probePath, "partition": "user:reader"}},
			{"POST", "/partitions/reset", map[string]any{"tile": probePath, "partition": "user:reader", "confirm": probePath + " user:reader"}},
			{"POST", "/partitions/restore", map[string]any{"tile": probePath, "user": "reader"}},
			{"POST", "/partitions/stop", map[string]any{"tile": probePath, "partition": "user:reader"}},
			{"POST", "/partitions/share-log", map[string]any{"tile": probePath, "days": 1}},
			{"DELETE", "/partitions/share-log", map[string]any{"tile": probePath}},
			{"POST", "/partitions/binds", map[string]any{"tile": probePath, "slot": "llm", "provider": "apps/prov"}},
			{"GET", "/partitions/binds?tile=" + probePath, nil},
			{"DELETE", "/partitions/binds", map[string]any{"tile": probePath, "slot": "llm"}},
		} {
			code, body := api(c.method, c.path, c.body)
			if code >= 500 || strings.Contains(body, "is a system component") {
				t.Errorf("%s %s: %d %s", c.method, c.path, code, body)
			}
		}
		for _, p := range []string{"/term/sessions?cwd=" + probePath, "/agent/history?cwd=" + probePath} {
			if code, body := api("GET", p, nil); code != 200 || strings.TrimSpace(body) != "[]" {
				t.Errorf("GET %s: %d %s, want an empty list", p, code, body)
			}
		}
	})

	t.Run("outside /api/xbin", func(t *testing.T) {
		code, body := d.do("GET", "/ws/term?cwd="+probePath, nil)
		expect(t, "a terminal", code, body, 403, "is a system component, managed by xbin: it has no terminal")
		code, body = d.do("GET", server.SystemPagePath, nil)
		expect(t, "the system page", code, body, 200, "<bx-system-page>")
		code, body = d.do("GET", "/vendor/system.html", nil)
		if code == 200 {
			t.Error("the system page is served under /vendor/ too, without its framing headers")
		}
	})

	// every handled row has a case above
	for _, pat := range patterns {
		if rules[pat].Class == server.SealedHandled && !covered[pat] {
			t.Errorf("%s is a handled row of server/sealedclass.go (%s) with no case in this test", pat, rules[pat].What)
		}
	}
	// and nothing of any of it reached the workspace tree or its manifest's
	// sealed rows
	if _, err := os.Stat(filepath.Join(ws, "xbin")); err == nil {
		t.Error("a directory appeared at <workspace>/xbin")
	}
}

func mustJSON(v any) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func readAll(resp *http.Response) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}
