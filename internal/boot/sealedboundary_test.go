package boot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sealed"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// boundaryReq is one well-formed request of a Refused or Self row about the
// sealed component: one its handler would act on for a tile.
type boundaryReq struct {
	path string // after /api/xbin
	body any
	// person: the row is a person's own act (the root token is turned away
	// before the tile is looked at), so only the admin's answer must be the
	// sealed one.
	person bool
	// other: the handler's own refusal that is the boundary here, where it
	// isn't the sealed wording — with why in the table below.
	other string
}

const P, R = probePath, probeRes

// boundaryReqs is, per Refused and Self row of server/sealedclass.go, the
// requests that reach the code doing the work.
func boundaryReqs() map[string][]boundaryReq {
	tile := func(extra map[string]any) []boundaryReq {
		b := map[string]any{"tile": P}
		for k, v := range extra {
			b[k] = v
		}
		return []boundaryReq{{body: b}}
	}
	dep := map[string]any{"deployment": "dev"}
	q := []boundaryReq{{path: "?tile=" + P}}
	grant := map[string]any{"from": "apps/plain", "target": P, "role": "reader"}
	reqs := map[string][]boundaryReq{
		// ---- its own alone ----
		"GET /kv/{rest...}":       {{path: "/kv/" + R + "/k"}},
		"PUT /kv/{rest...}":       {{path: "/kv/" + R + "/k", body: boundarySecret}},
		"DELETE /kv/{rest...}":    {{path: "/kv/" + R + "/k"}},
		"GET /blob/{rest...}":     {{path: "/blob/res:" + P + "/files/a"}},
		"PUT /blob/{rest...}":     {{path: "/blob/res:" + P + "/files/a", body: "v"}},
		"DELETE /blob/{rest...}":  {{path: "/blob/res:" + P + "/files/a"}},
		"GET /vault/{rest...}":    {{path: "/vault/" + P + "/k"}, {path: "/vault/" + P}},
		"PUT /vault/{rest...}":    {{path: "/vault/" + P + "/k", body: map[string]any{"value": "v"}}},
		"DELETE /vault/{rest...}": {{path: "/vault/" + P + "/k"}},
		"POST /bus/publish":       {{body: map[string]any{"resource": "res:" + P + "/events", "topic": "t", "data": 1}}},
		"PUT /bus/subscriptions": {
			{body: map[string]any{"component": P, "name": "s", "resource": "res:" + P + "/events", "path": "/hello", "role": "reader"}},
			{body: map[string]any{"component": "apps/plain", "name": "s", "resource": "res:" + P + "/events", "path": "/hello", "role": "reader"}},
		},
		"DELETE /bus/subscriptions/{name}": {{path: "/bus/subscriptions/s?component=" + P}},
		"PUT /cron/jobs": {
			{body: map[string]any{"component": P, "name": "j", "schedule": "@every 1h", "path": "/hello", "role": "reader"}},
		},
		"DELETE /cron/jobs/{name}": {{path: "/cron/jobs/j?component=" + P}},
		"POST /tile-report": {
			{path: "/tile-report?component=" + P, body: map[string]any{"level": "info", "message": "m"}},
			{body: map[string]any{"level": "info", "message": "m", "component": P}},
		},
		// ---- refused ----
		"POST /grants": {{body: grant},
			{body: map[string]any{"from": P, "target": "apps/prov", "role": "reader"}},
			{body: map[string]any{"from": "apps/plain", "target": R, "role": "reader"}},
			{body: map[string]any{"from": "apps/plain", "target": "code:" + P, "role": "reader"}}},
		"DELETE /grants": {{body: grant}, {body: map[string]any{"from": P, "target": "cap:open-links", "role": "writer"}}},
		"POST /bindings": {{body: map[string]any{"component": "apps/plain", "slot": "llm", "provider": P}},
			{body: map[string]any{"component": "apps/plain", "slot": "llm", "providers": []string{"apps/prov", P}}},
			// and its own slot, to a provider its release doesn't name
			{body: map[string]any{"component": P, "slot": "llm", "provider": "apps/plain"}},
			{body: map[string]any{"component": P, "slot": "net", "provider": "internet"}}},
		"DELETE /bindings": {{body: map[string]any{"component": "apps/plain", "slot": "llm", "provider": P},
			other: "no such binding" /* none can exist: the file's writer takes no such row, and the view drops one */}},
		"POST /partitions/consents": {{body: map[string]any{"from": "apps/plain", "to": P}, person: true},
			{body: map[string]any{"from": P, "to": "apps/plain"}, person: true},
			{path: "/partitions/consents?from=apps/plain&to=" + P, person: true}},
		"DELETE /partitions/consents": {{path: "/partitions/consents?from=apps/plain&to=" + P, person: true},
			{body: map[string]any{"from": "apps/plain", "to": P}, person: true}},
		"POST /partitions/mode":           {{body: map[string]any{"tile": P, "act": "keep"}, person: true}},
		"POST /partitions/reviewed":       {{body: map[string]any{"tile": P, "on": true}, person: true}},
		"POST /owner":                     {{body: map[string]any{"tile": P, "to": "user:adm"}}},
		"GET /owner/preview":              {{path: "/owner/preview?tile=" + P + "&to=user:adm"}},
		"POST /clone":                     {{body: map[string]any{"from": P, "to": "apps/copy"}}, {body: map[string]any{"from": "apps/plain", "to": "xbin/copy"}}},
		"POST /create":                    {{body: map[string]any{"path": "xbin/new"}}},
		"POST /templates/new":             {{body: map[string]any{"source": P, "path": "apps/t"}}, {body: map[string]any{"source": "agent", "path": "xbin/t"}}},
		"POST /builtins/import":           {{body: map[string]any{"name": "admin", "path": "xbin/adm"}}},
		"POST /builtins/update":           {{body: map[string]any{"id": P, "mode": "replace"}}},
		"POST /git/import":                {{body: map[string]any{"url": "https://example.invalid/x.git", "path": "xbin/imp"}}},
		"PUT /iface-instances":            {{body: map[string]any{"component": P, "instances": map[string]string{"a": "/a"}}}},
		"PUT /ingress-hosts":              {{body: map[string]any{"component": P, "hosts": []string{"a.example.com"}}}},
		"PUT /chrome":                     {{body: map[string]any{"path": P, "approved": true}}},
		"POST /go-build-versions/dismiss": {{body: map[string]any{"tile": P}}},
		"GET /code/tree":                  {{path: "/code/tree?component=" + P}},
		"GET /code/file":                  {{path: "/code/file?component=" + P + "&path=index.html"}},
		"GET /git/log":                    {{path: "/git/log?component=" + P}},
		"GET /git/diff":                   {{path: "/git/diff?component=" + P}},
		"GET /git/activity":               {{path: "/git/activity?component=" + P}},
		"GET /code/pr":                    {{path: "/code/pr?target=" + P + "&id=1"}},
		"GET /code/pr/series":             {{path: "/code/pr/series?target=" + P + "&id=1"}},
		"GET /code/prs":                   {{path: "/code/prs?target=" + P}},
		"POST /code/prs":                  {{body: map[string]any{"target": P, "from": "apps/plain", "title": "t"}}},
		"POST /code/pr/comment":           {{body: map[string]any{"target": P, "n": 1, "body": "c"}}},
		"POST /code/pr/state":             {{body: map[string]any{"target": P, "n": 1, "state": "rejected"}}},
		"GET /term-net":                   {{path: "/term-net?tile=" + P}},
		"POST /term/sessions":             {{body: map[string]any{"cwd": P, "kind": "agent", "provider": "claude"}}},
		"GET /checkpoints/{rest...}":      {{path: "/checkpoints/" + P + ".git/info/refs?service=git-upload-pack"}},
		"DELETE /sandboxes/{name}":        {{path: "/sandboxes/x?tile=" + P}},
		"POST /sandboxes/{name}/stop":     {{path: "/sandboxes/x/stop?tile=" + P}},
		// a manager names only its own sandboxes: the body's tile is not
		// read, and nobody but a manager tile's backend creates one
		"POST /sandboxes": {{body: map[string]any{"tile": P, "name": "x"}, other: "manager tiles only"}},
		// ---- deployments: one resolver, one judge ----
		"GET /deployments":                     q,
		"GET /deployments/backups":             q,
		"GET /deployments/diff":                q,
		"GET /deployments/log":                 q,
		"POST /deployments/add":                tile(dep),
		"POST /deployments/always-on":          tile(map[string]any{"deployment": "dev", "on": true}),
		"POST /deployments/backup":             tile(nil),
		"POST /deployments/backup-schedule":    tile(nil),
		"POST /deployments/branch":             tile(map[string]any{"deployment": "dev", "branch": "b"}),
		"POST /deployments/deliveries":         tile(map[string]any{"deployment": "dev", "on": true}),
		"POST /deployments/deploy":             tile(nil),
		"POST /deployments/edge":               tile(map[string]any{"edge": "apps/plain", "policy": "allow"}),
		"POST /deployments/limits":             tile(dep),
		"POST /deployments/live-reload/attach": tile(dep),
		"POST /deployments/live-reload/now":    tile(nil),
		"POST /deployments/live-reload/pause":  tile(nil),
		"POST /deployments/live-reload/resume": tile(nil),
		"POST /deployments/primary":            tile(dep),
		"POST /deployments/promote":            tile(map[string]any{"from": "main", "to": "dev"}),
		"POST /deployments/protect":            tile(map[string]any{"on": true}),
		"POST /deployments/purge":              tile(map[string]any{"checkpoint": "c:abcdef0"}),
		"POST /deployments/remove":             tile(dep),
		"POST /deployments/reset":              tile(dep),
		"POST /deployments/restore":            tile(nil),
		"POST /deployments/rollback":           tile(nil),
		"POST /deployments/run-now":            tile(dep),
		"POST /deployments/seed":               tile(dep),
		"POST /deployments/vault-copy":         tile(dep),
	}
	return reqs
}

// The route gate (server/sealedclass.go) reads a request beside its
// handler, and two readers of one body can disagree — {"tile":"xbin/x",
// "tile":null} was read as nothing by the gate and as xbin/x by the
// handlers. So the gate is not the boundary, and this test runs without
// it: every Refused and Self row is sent as a request its handler would act
// on for a tile, as the root token, an admin, a reader, another tile's
// backend, the component's own page and (the Self rows) its own backend,
// and each is answered by the code that would have done the work.
func TestSealedBoundaryWithoutTheGate(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace and builds a backend")
	}
	quiet(t)
	ws := probeWorkspace(t)
	table := []sealed.Spec{probeSpec()}
	d := startSealed(t, ws, table, production(prebuilt(t, table)), func(c *Config) { c.sealedGateOff = true })
	admin := d.addUser("adm", nil)
	if code, body := d.do("PATCH", "/api/xbin/users/adm", map[string]any{"role": "admin"}); code != 200 {
		t.Fatalf("making an admin: %d %s", code, body)
	}
	reader := d.addUser("reader", map[string]string{probePath: "read", "apps/plain": "read"})
	const plainInstance, ownInstance = "test-instance-of-apps-plain", "test-instance-of-xbin-probe"
	d.st.Auth.RegisterInstance(plainInstance, "apps/plain")
	d.st.Auth.RegisterInstance(ownInstance, probePath)
	var page struct{ Token string }
	if code, body := d.do("GET", "/api/xbin/frame-token?component="+probePath, nil, reader); code != 200 || json.Unmarshal([]byte(body), &page) != nil || page.Token == "" {
		t.Fatalf("a reader's frame token of it: %d %s", code, body)
	}
	frame := func(method, path string, body any) (int, string) {
		var rd *strings.Reader = strings.NewReader("")
		if body != nil {
			rd = strings.NewReader(mustJSON(body))
		}
		req, _ := http.NewRequest(method, d.base+path, rd)
		req.Header.Set("X-XBin-Frame-Token", page.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, readAll(resp)
	}

	// the backend first writes what the others must not read
	const secret = boundarySecret
	if code, body := d.do("PUT", "/api/xbin/kv/"+R+"/k", secret, ownInstance); code != 200 {
		t.Fatalf("its backend's own kv write: %d %s", code, body)
	}

	rules, reqs := server.SealedRules(), boundaryReqs()
	patterns := make([]string, 0, len(rules))
	for p := range rules {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	const sealedSays = " is a system component, managed by xbin: "
	n := 0
	for _, pat := range patterns {
		rule := rules[pat]
		if rule.Class != server.SealedRefused && rule.Class != server.SealedSelf {
			if _, has := reqs[pat]; has {
				t.Errorf("%s has requests here and is no Refused or Self row", pat)
			}
			continue
		}
		if len(reqs[pat]) == 0 {
			t.Errorf("%s is a %s row of server/sealedclass.go with no request in boundaryReqs: add one its handler would act on", pat, rule.Class)
			continue
		}
		method, base := routeURL(pat, "")
		for i, rq := range reqs[pat] {
			n++
			path := "/api/xbin" + base
			switch {
			case strings.HasPrefix(rq.path, "?"):
				path += rq.path
			case rq.path != "":
				path = "/api/xbin" + rq.path
			}
			if !strings.Contains(path+mustJSON(rq.body), util_SealedPrefix) {
				t.Errorf("%s request %d names no sealed path", pat, i)
			}
			refused := func(who string, code int, out string, must bool) {
				t.Helper()
				marker := sealedSays
				if rq.other != "" {
					marker = rq.other
				}
				switch {
				case code < 400 || code >= 500:
					t.Errorf("%s (%s) as %s was not refused: %d %.300s", pat, path, who, code, out)
				case must && !strings.Contains(out, marker):
					t.Errorf("%s (%s) as %s was refused, but not by the code that acts on the component: %d %.300s\n\twant …%s…", pat, path, who, code, out, marker)
				case strings.Contains(out, secret):
					t.Errorf("%s (%s) as %s shows its data: %.300s", pat, path, who, out)
				}
			}
			code, out := d.do(method, path, rq.body, admin)
			refused("an admin", code, out, true)
			code, out = d.do(method, path, rq.body, d.token)
			refused("the root token", code, out, !rq.person)
			code, out = d.do(method, path, rq.body, reader)
			refused("a reader of it", code, out, false)
			code, out = d.do(method, path, rq.body, plainInstance)
			refused("another tile's backend", code, out, false)
			// its page holds a viewer's token: nothing of xbind's API
			if code, out := frame(method, path, rq.body); code != http.StatusForbidden || !strings.Contains(out, sealedSays+"its page reaches its own backend only") {
				t.Errorf("%s (%s) from its own page: %d %.300s\n\twant 403: a viewer's token", pat, path, code, out)
			}
			// its backend: what is its own passes this boundary; what
			// doesn't exist for it is refused for it too
			code, out = d.do(method, path, rq.body, ownInstance)
			if rule.Class == server.SealedSelf { // (a backend registers only itself: a named component is replaced by its own)
				if strings.Contains(out, sealedSays) {
					t.Errorf("%s (%s) as its own backend is refused as if it were someone else: %d %.300s", pat, path, code, out)
				}
			} else {
				refused("its own backend", code, out, false)
			}
		}
	}
	if n < 90 {
		t.Errorf("only %d requests driven: is the table intact?", n)
	}

	// ---- the rows the handler decides (handled): the route gate never
	// acts on them, so what answers here is the handler and what it calls.
	// Each is a change an admin may make to a sealed component — and only an
	// admin: not a reader, not another tile's backend, not the component's
	// page (a viewer) and not the component's own backend (it is no admin).
	// What an admin may NOT make (levels above read, offloading, a provider
	// the release doesn't name) is refused for the admin too.
	type handled struct {
		method, path string
		body         any
		admin        int // what an admin and the root token get
	}
	for _, h := range []handled{
		{"POST", "/lifecycle", map[string]any{"component": P, "state": "hidden"}, 200},
		{"POST", "/lifecycle", map[string]any{"component": P, "state": "enabled"}, 200},
		{"POST", "/lifecycle", map[string]any{"component": P, "state": "offloaded"}, 400},
		{"POST", "/limits", map[string]any{"tile": P, "kind": "diskGiB", "value": 60, "source": "admin-set"}, 200},
		{"DELETE", "/limits?tile=" + P + "&kind=diskGiB", nil, 200},
		{"PUT", "/access", map[string]any{"tile": P, "kind": "user", "id": "reader", "level": "read"}, 200},
		{"PUT", "/access", map[string]any{"tile": P, "kind": "user", "id": "reader", "level": "terminal"}, 400},
		{"PATCH", "/users/reader", map[string]any{"tiles": map[string]string{P: "write", "apps/plain": "read"}}, 400},
		{"PUT", "/defaults", map[string]any{"defaultTiles": map[string]string{P: "terminal"}}, 400},
		{"POST", "/bindings", map[string]any{"component": P, "slot": "llm", "provider": "apps/prov"}, 200},
		{"DELETE", "/bindings", map[string]any{"component": P, "slot": "llm"}, 200},
		{"POST", "/backup-schedule", map[string]any{"component": P, "schedule": "@daily"}, 0},
		{"DELETE", "/backup-schedule?component=" + P, nil, 0},
		{"POST", "/restore", map[string]any{"component": P}, 0},
		{"POST", "/partitions/limits", map[string]any{"tile": P, "maxRunning": 3}, 0},
		{"POST", "/partitions/stop", map[string]any{"tile": P, "user": "reader"}, 0},
		{"POST", "/partitions/purge", map[string]any{"tile": P, "user": "reader"}, 0},
		{"POST", "/partitions/reset", map[string]any{"tile": P, "user": "reader"}, 0},
		{"POST", "/partitions/restore", map[string]any{"tile": P, "user": "reader"}, 0},
		{"POST", "/backup/erase", map[string]any{"component": P, "what": "data"}, 0},
	} {
		path := "/api/xbin" + h.path
		for who, tok := range map[string]string{"a reader of it": reader, "another tile's backend": plainInstance, "its own backend": ownInstance} {
			if code, out := d.do(h.method, path, h.body, tok); code < 400 || code >= 500 {
				t.Errorf("%s %s as %s: %d %.200s — only an admin changes a sealed component", h.method, h.path, who, code, out)
			}
		}
		if code, out := frame(h.method, path, h.body); code != http.StatusForbidden || !strings.Contains(out, "its page reaches its own backend only") {
			t.Errorf("%s %s from its own page: %d %.200s", h.method, h.path, code, out)
		}
		for who, tok := range map[string]string{"an admin": admin, "the root token": d.token} {
			code, out := d.do(h.method, path, h.body, tok)
			switch {
			case code >= 500 && h.admin != 0, code >= 500 && !strings.Contains(out, "archiver") && !strings.Contains(out, "vault"):
				t.Errorf("%s %s as %s: %d %.200s", h.method, h.path, who, code, out)
			case h.admin != 0 && code != h.admin:
				t.Errorf("%s %s as %s: %d %.200s, want %d", h.method, h.path, who, code, out, h.admin)
			case strings.Contains(out, secret):
				t.Errorf("%s %s as %s shows its data: %.200s", h.method, h.path, who, out)
			}
		}
	}
	// the reads the handler decides: its data is in none of them, whoever asks
	for _, path := range []string{"/logs?component=" + P, "/components/" + P, "/tile-status?component=" + P, "/access?tile=" + P, "/limits?tile=" + P,
		"/backups?component=" + P, "/partitions?tile=" + P, "/resources?sealed=1", "/runtime?sealed=1", "/vaults?sealed=1", "/sealed", "/auth-overview?sealed=1"} {
		for who, tok := range map[string]string{"an admin": admin, "the root token": d.token, "a reader of it": reader, "another tile's backend": plainInstance} {
			code, out := d.do("GET", "/api/xbin"+path, nil, tok)
			if code >= 500 && !strings.Contains(out, "archiver") || strings.Contains(out, secret) {
				t.Errorf("GET %s as %s: %d %.200s", path, who, code, out)
			}
		}
	}

	// nothing of it reached the workspace: no row in its manifest (but the
	// binding and the switch an admin made above, undone), no directory, no
	// owner, no registration but the backend's own
	var man registry.WorkspaceManifest
	if b, err := os.ReadFile(filepath.Join(ws, "xbin.json")); err != nil || json.Unmarshal(b, &man) != nil {
		t.Fatalf("the workspace manifest: %v", err)
	}
	for _, g := range man.Grants {
		if util.SealedPath(g.From) || util.SealedTarget(g.Target) {
			t.Errorf("the workspace manifest holds a grant naming a sealed path: %+v", g)
		}
	}
	for comp, slots := range man.Bindings {
		for slot, bnd := range slots {
			if util.SealedPath(comp) && (slot != "llm" || len(bnd) != 0) {
				t.Errorf("the workspace manifest binds %s %s to %v", comp, slot, bnd.Refs())
			}
			for _, ref := range bnd.Refs() {
				if util.SealedPath(ref) {
					t.Errorf("the workspace manifest binds %s %s to a sealed component", comp, slot)
				}
			}
		}
	}
	if len(man.IfaceInstances[P]) != 0 || len(man.IngressHosts[P]) != 0 || registry.IsOffloaded(man.Lifecycle[P]) {
		t.Errorf("the workspace manifest holds instances, hosts or an offloaded state for it: %+v", man)
	}
	if _, err := os.Stat(filepath.Join(ws, "xbin")); err == nil {
		t.Error("a directory appeared at <workspace>/xbin")
	}
	if code, out := d.do("GET", "/api/xbin/kv/"+R+"/k", nil, ownInstance); code != 200 || !strings.Contains(out, secret) {
		t.Errorf("its backend's data after all that: %d %s", code, out)
	}
	// its page still reaches its own backend, as the reader it is
	if code, out := frame("GET", "/api/"+probePath+"/hello", nil); code != 200 || !strings.Contains(out, "self="+probePath) {
		t.Errorf("its page's call to its own backend: %d %s", code, out)
	}
}

const (
	util_SealedPrefix = "xbin/"
	boundarySecret    = "S3ALED-MARKER-VALUE"
)

// The rows classed SealedNoTile take no tile — by the table's word. This
// drives each of them anyway with the sealed component's path wherever a
// handler could read a name: every wildcard of the route, a query with the
// usual parameter names, a JSON body with them. None may fail inside xbind,
// show the component's data, or leave anything behind about it.
func TestSealedNoTileRowsTakeNoSealedPath(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace and builds a backend")
	}
	quiet(t)
	ws := probeWorkspace(t)
	table := []sealed.Spec{probeSpec()}
	d := startSealed(t, ws, table, production(prebuilt(t, table)))
	const ownInstance = "test-instance-of-xbin-probe"
	d.st.Auth.RegisterInstance(ownInstance, probePath)
	if code, body := d.do("PUT", "/api/xbin/kv/"+R+"/k", boundarySecret, ownInstance); code != 200 {
		t.Fatalf("its backend's own kv write: %d %s", code, body)
	}
	// routes that change who can sign in or whether the vault is open: a
	// request with any body would end the test's own access, and none of
	// them takes a name
	skip := map[string]string{
		"POST /auth-rotate-token": "rotates the token this test holds", "POST /vault-seal": "seals the vault", "POST /vault-rekey": "rekeys the vault",
		"POST /vault-unseal": "the vault's passphrase", "PATCH /auth-settings": "sign-in methods", "POST /impersonate": "a view-as ticket",
		"POST /impersonate/stop": "a view-as session", "POST /login": "counts failed sign-ins", "POST /account/password": "the caller's password",
		"POST /backup-keys/import": "the workspace's backup keys", "PUT /workspace-settings": "the workspace's own settings",
		"PUT /workspace-policies": "the workspace's own policies",
	}
	names := []string{"component", "tile", "path", "target", "from", "to", "id", "name", "scope", "resource", "cwd", "source", "provider", "user", "org", "key", "owner", "sandbox", "session"}
	q := url.Values{}
	body := map[string]any{"tiles": map[string]string{probePath: "terminal"}, "providers": []string{probePath}}
	for _, n := range names {
		q.Set(n, probePath)
		body[n] = probePath
	}
	esc := url.PathEscape(probePath)
	client := &http.Client{Timeout: 4 * time.Second}
	send := func(method, path string, payload any) (int, string) {
		var rd io.Reader
		if payload != nil {
			rd = strings.NewReader(mustJSON(payload))
		}
		req, _ := http.NewRequest(method, d.base+"/api/xbin"+path, rd)
		req.Header.Set("Authorization", "Bearer "+d.token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err.Error() // a stream that never ends: nothing was answered
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, string(b)
	}
	rules := server.SealedRules()
	patterns := make([]string, 0, len(rules))
	for p := range rules {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	n, silent := 0, 0
	for _, pat := range patterns {
		if rules[pat].Class != server.SealedNoTile {
			continue
		}
		if _, no := skip[pat]; no {
			continue
		}
		method, route, ok := strings.Cut(pat, " ")
		if !ok { // a route of every method (a sandbox's forwarded port)
			method, route = "GET", pat
		}
		route = regexp.MustCompile(`\{[a-zA-Z]+\.\.\.\}`).ReplaceAllString(route, probePath+"/k")
		route = wildcardRe.ReplaceAllString(route, esc)
		var payload any
		if method != "GET" && method != "DELETE" {
			payload = body
		}
		for _, variant := range []struct {
			path    string
			payload any
		}{{route + "?" + q.Encode(), nil}, {route, payload}, {route + "?sealed=1&component=" + url.QueryEscape("res:"+probePath+"/state"), payload}} {
			n++
			code, out := send(method, variant.path, variant.payload)
			switch {
			case code == 0:
				silent++
			case code == http.StatusInternalServerError:
				t.Errorf("%s (%s) failed inside xbind: %.300s", pat, variant.path, out)
			case strings.Contains(out, boundarySecret):
				t.Errorf("%s (%s) shows the component's data: %.300s", pat, variant.path, out)
			}
		}
	}
	if n < 400 || silent > 12 {
		t.Errorf("%d requests driven, %d unanswered: is the table intact, or did xbind stop answering?", n, silent)
	}
	for pat := range skip {
		if rules[pat].Class != server.SealedNoTile {
			t.Errorf("%s is skipped here and is no SealedNoTile row", pat)
		}
	}

	// nothing was left behind about it
	if b, err := os.ReadFile(filepath.Join(ws, "xbin.json")); err != nil || strings.Contains(string(b), "xbin/") {
		t.Errorf("the workspace manifest names a sealed path: %s (%v)", b, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "xbin")); err == nil {
		t.Error("a directory appeared at <workspace>/xbin")
	}
	if code, out := d.do("GET", "/api/xbin/kv/"+R+"/k", nil, ownInstance); code != 200 || !strings.Contains(out, boundarySecret) {
		t.Errorf("its backend's data after all that: %d %s", code, out)
	}
	for _, list := range []string{"/api/xbin/cron/jobs?sealed=1", "/api/xbin/bus/subscriptions?sealed=1", "/api/xbin/grants"} { // (GET /bindings names it: its declared slot is an admin's to bind)
		if code, out := d.do("GET", list, nil); code != 200 || strings.Contains(out, probePath) {
			t.Errorf("GET %s names it: %d %.300s", list, code, out)
		}
	}
	for _, u := range d.st.Users.List() {
		for path, level := range u.Tiles {
			if strings.HasPrefix(strings.Trim(path, "/"), "xbin/") && level != "read" && level != "none" && !strings.Contains(path, "*") {
				t.Errorf("user %s holds %s on %s", u.ID, level, path)
			}
		}
	}
	if code, out := d.do("GET", "/api/"+probePath+"/hello", nil); code != 200 || !strings.Contains(out, "self="+probePath) {
		t.Errorf("the component after all that: %d %s", code, out)
	}
}

// Every route, every spelling. A sealed path written another way — a
// leading, doubled or trailing slash, "./", "a/../" — is one path to the
// code that trims, joins or resolves it; a test that looked at the exact
// prefix let it through as "no sealed path" (POST /restore
// {component:"/xbin/x"}, GET /frame-token?component=/xbin/x). With the gate
// on such a spelling is refused at the edge; with it off the code that
// acts refuses it as it refuses xbin/<name>. Spellings that are other bytes
// (case, percent-escapes, look-alike letters) name no component at all:
// nothing answers them as the sealed one.
func TestSealedPathSpellings(t *testing.T) {
	if testing.Short() {
		t.Skip("boots two workspaces and builds a backend")
	}
	quiet(t)
	table := []sealed.Spec{probeSpec()}
	bin := prebuilt(t, table)
	same := []string{"/xbin/probe", "xbin/probe/", "//xbin/probe", "xbin//probe", "./xbin/probe", "apps/../xbin/probe", "xbin/./probe", "/./xbin/probe/"}
	foreign := []string{"XBIN/probe", "Xbin/Probe", "xbin%2Fprobe", "%78bin/probe", "xbіn/probe", "ｘbin/probe", "xbin∕probe", "x​bin/probe"}
	rules, reqs := server.SealedRules(), boundaryReqs()
	patterns := make([]string, 0, len(reqs))
	for p := range reqs {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	respell := func(v any, to string) any { // every occurrence, keys included
		if v == nil {
			return nil
		}
		var out any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(mustJSON(v), `"`+P, `"`+strings.ReplaceAll(to, `"`, ``))), &out); err != nil {
			t.Fatalf("respelling %v: %v", v, err)
		}
		var res any
		_ = json.Unmarshal([]byte(strings.ReplaceAll(mustJSON(out), `"res:`+P, `"res:`+to)), &res)
		return res
	}
	for _, gateOff := range []bool{false, true} {
		ws := probeWorkspace(t)
		d := startSealed(t, ws, table, production(bin), func(c *Config) { c.sealedGateOff = gateOff })
		admin := d.addUser("adm", nil)
		if code, body := d.do("PATCH", "/api/xbin/users/adm", map[string]any{"role": "admin"}); code != 200 {
			t.Fatalf("making an admin: %d %s", code, body)
		}
		const ownInstance = "test-instance-of-xbin-probe"
		d.st.Auth.RegisterInstance(ownInstance, probePath)
		if code, body := d.do("PUT", "/api/xbin/kv/"+R+"/k", boundarySecret, ownInstance); code != 200 {
			t.Fatalf("its backend's own kv write: %d %s", code, body)
		}
		send := func(method, path string, body any, tok string) (int, string) {
			var rd io.Reader
			if body != nil {
				rd = strings.NewReader(mustJSON(body))
			}
			req, err := http.NewRequest(method, d.base+path, rd)
			if err != nil {
				return 400, err.Error() // a spelling no URL carries
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if err != nil {
				return 0, err.Error()
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			return resp.StatusCode, string(b)
		}
		n := 0
		for _, pat := range patterns {
			method, base := routeURL(pat, "")
			for _, rq := range reqs[pat] {
				for i, sp := range append(append([]string{}, same...), foreign...) {
					path := "/api/xbin" + base
					switch {
					case strings.HasPrefix(rq.path, "?"):
						path += rq.path
					case rq.path != "":
						path = "/api/xbin" + rq.path
					}
					path = strings.ReplaceAll(path, P, (&url.URL{Path: sp}).EscapedPath())
					if strings.Contains(sp, "%") {
						path = strings.ReplaceAll(path, (&url.URL{Path: sp}).EscapedPath(), url.QueryEscape(sp))
					}
					body := respell(rq.body, sp)
					for who, tok := range map[string]string{"the root token": d.token, "an admin": admin} {
						n++
						code, out := send(method, path, body, tok)
						switch {
						case code == 0 || code >= 500:
							t.Errorf("gate off=%v: %s as %s, spelled %q: %d %.200s", gateOff, pat, who, sp, code, out)
						case strings.Contains(out, boundarySecret):
							t.Errorf("gate off=%v: %s as %s, spelled %q, shows its data: %.200s", gateOff, pat, who, sp, out)
						case i < len(same) && code < 400 && rules[pat].Class != server.SealedNoTile:
							// the same component, so the same answer as xbin/probe: a refusal
							t.Errorf("gate off=%v: %s as %s, spelled %q, was not refused: %d %.200s", gateOff, pat, who, sp, code, out)
						}
					}
				}
			}
		}
		if n < 2000 {
			t.Errorf("only %d requests driven", n)
		}
		// the two the review executed: a token and a restore under another spelling
		for _, sp := range append(append([]string{}, same...), foreign...) {
			code, out := send("GET", "/api/xbin/frame-token?component="+url.QueryEscape(sp), nil, d.token)
			var ft struct{ Token string }
			if code == 200 && json.Unmarshal([]byte(out), &ft) == nil && ft.Token != "" {
				if slices.Contains(same, sp) {
					t.Errorf("gate off=%v: a frame token was minted for %q", gateOff, sp)
				}
				// other bytes: a token of a tile that doesn't exist, as any
				// made-up tile path gets — and nothing of the sealed component's
				for _, path := range []string{"/api/xbin/kv/" + R + "/k", "/api/" + probePath + "/hello"} {
					req, _ := http.NewRequest("GET", d.base+path, nil)
					req.Header.Set("X-XBin-Frame-Token", ft.Token)
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					b := readAll(resp)
					resp.Body.Close()
					if resp.StatusCode < 400 || strings.Contains(b, boundarySecret) {
						t.Errorf("gate off=%v: the token minted for %q reaches %s: %d %.200s", gateOff, sp, path, resp.StatusCode, b)
					}
				}
			}
			if code, out := send("POST", "/api/xbin/restore", map[string]any{"component": sp}, d.token); code < 400 {
				t.Errorf("gate off=%v: POST /restore {component:%q}: %d %.200s", gateOff, sp, code, out)
			}
			if code, out := send("PUT", "/api/xbin/cron/jobs", map[string]any{"component": sp, "name": "j", "schedule": "@every 1h", "path": "/hello", "role": "admin"}, d.token); code < 400 {
				t.Errorf("gate off=%v: a cron job registered for %q: %d %.200s", gateOff, sp, code, out)
			}
			req, _ := http.NewRequest("GET", d.base+"/api/"+(&url.URL{Path: sp}).EscapedPath()+"/hello", nil)
			if req != nil {
				req.Header.Set("Authorization", "Bearer "+d.token)
				req.URL.Opaque = "//" + req.URL.Host + "/api/" + (&url.URL{Path: sp}).EscapedPath() + "/hello" // as written: no client-side cleaning
				if resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req); err == nil {
					b := readAll(resp)
					resp.Body.Close()
					if resp.StatusCode == 200 && strings.Contains(b, "self="+probePath) {
						t.Errorf("gate off=%v: its backend answered at /api/%s/hello", gateOff, sp)
					}
				}
			}
		}
		// nothing was left behind, and it still runs
		// (rows about the other-bytes names are rows about tiles that don't
		// exist, as any made-up name gets; none is a sealed path)
		if b, err := os.ReadFile(filepath.Join(ws, "xbin.json")); err != nil || strings.Contains(string(b), "xbin/") || strings.Contains(string(b), `xbin\/`) {
			t.Errorf("gate off=%v: the workspace manifest names a sealed path: %.600s (%v)", gateOff, b, err)
		}
		if code, out := d.do("GET", "/api/xbin/kv/"+R+"/k", nil, ownInstance); code != 200 || !strings.Contains(out, boundarySecret) {
			t.Errorf("gate off=%v: its backend's data after all that: %d %s", gateOff, code, out)
		}
		for _, list := range []string{"/api/xbin/cron/jobs?sealed=1", "/api/xbin/bus/subscriptions?sealed=1"} {
			if code, out := d.do("GET", list, nil); code != 200 || strings.Contains(out, "xbin/") {
				t.Errorf("gate off=%v: GET %s names it: %d %.300s", gateOff, list, code, out)
			}
		}
		if code, out := d.do("GET", "/api/"+probePath+"/hello", nil); code != 200 || !strings.Contains(out, "self="+probePath) {
			t.Errorf("gate off=%v: the component after all that: %d %s", gateOff, code, out)
		}
		d.stop()
	}
}
