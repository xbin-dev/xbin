package boot

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sealed"
	"github.com/xbin-dev/xbin/internal/server"
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

	// nothing of it reached the workspace: no row in its manifest, no
	// directory, no owner, no registration but the backend's own
	if b, err := os.ReadFile(filepath.Join(ws, "xbin.json")); err != nil || strings.Contains(string(b), "xbin/") {
		t.Errorf("the workspace manifest names a sealed path: %s (%v)", b, err)
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
