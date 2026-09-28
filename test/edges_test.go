//go:build integration

package test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fabric (09-fabric) end to end, on the isolated daemon: every inbound
// edge of a tile reaches its primary (D127d), a non-primary deployment's
// outbound calls meet the edge policy (D127a) (D127o), and a reassignment moves
// every inbound edge at once (flow F). Each tile here runs the fabric probe
// (fabSource): a backend that records every request it is sent, with who
// sent it, and acts on the gateway on request, so a test can say both what
// a deployment saw and what it could do. examples/ is never edited for it.

// fabTile is one fabric probe tile's manifest beyond its runtime, its own
// scope (kv, bus and cron, each used at writer) and its marker.
type fabTile struct {
	uses  []string // more "uses" rows, raw JSON objects
	extra string   // more manifest fields, raw JSON, each with its leading comma
	echo  int      // a TCP echo listener on this port (a stream expose); 0 = none
}

// writeFab writes the fabric probe at ws/tile, or turns an existing one into
// marker: backend/main.go with marker compiled in (GET /v), probeFile and
// native-<marker>.js holding marker (a native entry the manifest may name),
// a go.mod requiring the sdk under a module path of the tile's own (go.work
// never sees one twice), scope.json, echo.port when f.echo is set, and
// xbin.json last, so the tile becomes a Go component once its code is
// there. Only files whose bytes change are written.
func writeFab(t *testing.T, ws, tile, marker string, f fabTile) {
	t.Helper()
	dir := filepath.Join(ws, filepath.FromSlash(tile))
	uses := []string{}
	for _, name := range []string{"kv", "bus", "cron"} {
		uses = append(uses, `{"target":"res:`+tile+`/`+name+`","role":"writer"}`)
	}
	uses = append(uses, f.uses...)
	files := [][2]string{
		{"go.mod", "module fab/" + strings.NewReplacer("/", "_", "+", "_").Replace(tile) + "\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n"},
		{"scope.json", `{"resources":{"kv":{"type":"kv"},"bus":{"type":"bus"},"cron":{"type":"cron"}}}` + "\n"},
		{"backend/main.go", strings.Replace(fabSource, "__MARKER__", strconv.Quote(marker), 1)},
		{probeFile, marker},
		{"native-" + marker + ".js", "// the native entry of " + marker + "\n"},
	}
	if f.echo > 0 {
		files = append(files, [2]string{"echo.port", strconv.Itoa(f.echo)})
	}
	files = append(files, [2]string{"xbin.json", `{"runtime":"go","uses":[` + strings.Join(uses, ",") + `]` + f.extra + "}\n"})
	for _, fl := range files {
		writeIfChanged(t, filepath.Join(dir, filepath.FromSlash(fl[0])), fl[1])
	}
}

// fabWait waits until the fabric probe at ref (a tile, or <tile>+<name>)
// answers marker on GET /v through a, and fails the test with the last
// answer otherwise. A failed build doesn't mend itself: saying so ends it.
func fabWait(t *testing.T, a dlAPI, ref, marker string) {
	t.Helper()
	var code int
	var body string
	waitFor(func() bool {
		code, body = a.do("GET", "/api/"+ref+"/v", "")
		return code == 200 && body == marker || strings.Contains(body, "build failed")
	}, 3*time.Minute)
	if code != 200 || body != marker {
		t.Fatalf("the probe at %s never served %q: %d %s", ref, marker, code, body)
	}
}

// fabHit is one request the probe was sent, with who sent it (xbind's
// headers) and, for a bus delivery, the event.
type fabHit struct {
	Method       string `json:"method"`
	Path         string `json:"path"`
	From         string `json:"from"`
	Role         string `json:"role"`
	Deployment   string `json:"deployment"`
	Subscription string `json:"subscription,omitempty"`
	Topic        string `json:"topic,omitempty"`
}

// fabHits is every request the probe at ref was sent since it started,
// oldest first (GET /hits itself isn't recorded).
func fabHits(t *testing.T, a dlAPI, ref string) []fabHit {
	t.Helper()
	code, body := a.do("GET", "/api/"+ref+"/hits", "")
	var hits []fabHit
	if code != 200 || json.Unmarshal([]byte(body), &hits) != nil {
		t.Fatalf("GET /api/%s/hits: %d %s", ref, code, body)
	}
	return hits
}

// fabCount is how many of hits match.
func fabCount(hits []fabHit, match func(fabHit) bool) int {
	n := 0
	for _, h := range hits {
		if match(h) {
			n++
		}
	}
	return n
}

// fabPath matches a hit on path.
func fabPath(path string) func(fabHit) bool { return func(h fabHit) bool { return h.Path == path } }

// fabForeign matches a hit anyone but the test itself sent: the test calls
// as the owner (--no-auth) or with the root token, both "owner".
func fabForeign(h fabHit) bool { return h.From != "owner" }

// fabResult is what the probe answers for an act on the gateway: the
// status and body of the call it made (the response's X-XBin-Deployment
// too), or the error that kept it from answering.
type fabResult struct {
	Status     int    `json:"status"`
	Body       string `json:"body"`
	Deployment string `json:"deployment"`
	Err        string `json:"err"`
}

// fabAct asks the probe at ref to act (GET /act/<verb>?<q>), which it must
// answer 200, and returns what the act met.
func fabAct(t *testing.T, a dlAPI, ref, verb string, q url.Values) fabResult {
	t.Helper()
	code, body := a.do("GET", "/api/"+ref+"/act/"+verb+"?"+q.Encode(), "")
	var r fabResult
	if code != 200 || json.Unmarshal([]byte(body), &r) != nil {
		t.Fatalf("GET /api/%s/act/%s?%s: %d %s", ref, verb, q.Encode(), code, body)
	}
	return r
}

// fabHTTP asks the probe at ref to call the gateway: method on target,
// which is a gateway path ("/api/…"), or "$VAR/…" for a URL from the
// probe's env (an interface slot's).
func fabHTTP(t *testing.T, a dlAPI, ref, method, target, body string) fabResult {
	t.Helper()
	return fabAct(t, a, ref, "http", url.Values{"method": {method}, "url": {target}, "body": {body}})
}

// fabJSON is v as a JSON string, for request bodies.
func fabJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// fabMust200 is a request through a that must answer 200; it returns the
// body.
func fabMust200(t *testing.T, a dlAPI, method, path, body string) string {
	t.Helper()
	code, b := a.do(method, path, body)
	if code != 200 {
		t.Fatalf("%s %s %s: %d %s", method, path, body, code, b)
	}
	return b
}

// fabBackend is one backend row of the sandbox registry: the deployment it
// runs for ("" for main, the registry's name rule) and its generation.
type fabBackend struct {
	Kind       string `json:"kind"`
	Deployment string `json:"deployment"`
	Gen        int    `json:"gen"`
}

// fabBackends is tile's backend rows in the sandbox registry.
func fabBackends(t *testing.T, a dlAPI, tile string) []fabBackend {
	t.Helper()
	body := fabMust200(t, a, "GET", "/api/xbin/sandboxes?tile="+tile, "")
	var out struct{ Sandboxes []fabBackend }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("/sandboxes: %v %.300s", err, body)
	}
	var rows []fabBackend
	for _, s := range out.Sandboxes {
		if s.Kind == "backend" {
			rows = append(rows, s)
		}
	}
	return rows
}

// fabIngress sends GET path to d's ingress listener as host.
func fabIngress(t *testing.T, d *isoDaemon, host, path string) (int, string) {
	t.Helper()
	rq, _ := http.NewRequest("GET", "http://"+d.Ingress+path, nil)
	rq.Host = host
	r, err := isoClient.Do(rq)
	if err != nil {
		return 0, err.Error()
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

// ---- the inbound fixture ----

// fabInbound is 09-fabric §1's fixture: tile T has two deployments, main
// primary and pinned to checkpoint A (the probe at m1), dev the live reload
// target whose work tree answers m2 (B); consumer C binds T's provide (slot
// fab) and holds a grant on T; T's http expose is published on the ingress
// listener as host. Both of T's codes declare alwaysOn, name their marker's
// native entry, and provide fab-inst, whose instances each deployment's
// backend may register.
type fabInbound struct {
	T, C, host string
	tile       fabTile // T's manifest at either marker
}

// fabFixtureHost is the fixture's ingress hostname.
const fabFixtureHost = "t.fab.test"

// setupFabInbound builds the fixture on d, T and C at those paths, and
// returns it once main answers m1 and dev m2.
func setupFabInbound(t *testing.T, d *isoDaemon, T, C string) fabInbound {
	t.Helper()
	a := d.dl()
	f := fabInbound{T: T, C: C, host: fabFixtureHost}
	f.tile = fabTile{extra: `,"alwaysOn":true,"native":"./native-m1.js"` +
		`,"expose":{"roles":{"reader":"read the probe","writer":"change the probe"}}` +
		`,"provides":{"fab":{"kind":"http","service":"fab","role":"reader"},` +
		`"inst":{"kind":"http","service":"fab-inst","role":"reader","instances":true}}` +
		`,"exposes":{"web":{"kind":"http","paths":["/v"]}}`}
	writeFab(t, d.WS, T, "m1", f.tile)
	writeFab(t, d.WS, C, "c1", fabTile{
		uses:  []string{`{"target":"` + T + `","role":"reader"}`},
		extra: `,"interfaces":{"fab":{"kind":"http","service":"fab"}}`,
	})
	fabWait(t, a, T, "m1")
	fabWait(t, a, C, "c1")
	fabMust200(t, a, "POST", "/api/xbin/bindings", fabJSON(map[string]string{"component": C, "slot": "fab", "provider": T}))
	fabMust200(t, a, "POST", "/api/xbin/grants", fabJSON(map[string]string{"from": C, "target": T, "role": "reader"}))
	fabMust200(t, a, "POST", "/api/xbin/bindings", fabJSON(map[string]string{"component": T, "slot": "web", "provider": "runtime", "host": f.host}))
	// The binding restarts C with the slot's URL in its env.
	if !waitFor(func() bool {
		c, b := a.do("GET", "/api/"+C+"/env", "")
		return c == 200 && strings.Contains(b, `"XBIN_IFACE_FAB_URL":"http://xbin/api/`+T+`"`)
	}, 2*time.Minute) {
		_, b := a.do("GET", "/api/"+C+"/env", "")
		t.Fatalf("%s's env never carried its fab slot: %s", C, b)
	}

	// dev follows the work tree; main is pinned where it stood (A).
	ans, e := a.op(t, "add", T, "deployment", "dev", "attach", true)
	if e.Result != "ok" {
		t.Fatalf("adding dev with live reload attached: %+v (answer %+v)", e, ans)
	}
	st := a.state(t, T)
	if st.Primary != "main" || st.LiveReload != "dev" || st.pinned("main") == "" {
		t.Fatalf("after the add: primary %q, live reload %q, main pinned %q", st.Primary, st.LiveReload, st.pinned("main"))
	}
	f.tile.extra = strings.Replace(f.tile.extra, "native-m1.js", "native-m2.js", 1)
	writeFab(t, d.WS, T, "m2", f.tile)
	fabWait(t, a, T+"+dev", "m2")
	fabWait(t, a, T, "m1")
	return f
}

// ---- TestInboundEdgesReachOnlyPrimary ----

// covers D127d D127h T6 SC-INBOUND — every inbound edge of 15-test-plan §5.5
// that can run end to end, on 09-fabric §1's fixture (main primary pinned
// to A, answering m1; dev the live reload target, answering m2), each fired
// and answered by A while dev sees nothing: the bare /api/ and /c/ URLs
// and the native document; the ingress listener's Host route; a consumer's
// call through its slot URL and through its grant (and its refused calls to
// <tile>+dev and <tile>+main); cron (main's job ticks main alone); a bus
// publish into the tile's bus (main's subscription only); alwaysOn (an
// xbind restart starts main, never dev, whose own checkpoint declares it
// too). dev's own registrations aren't inbound edges from others (D127h,
// revised): its job, active by default, ticks dev alone, never main; with
// its deliveries off it stays quiet and run now fires it once to dev; back
// on, its job ticks dev again and its own publish (dev's namespace) reaches
// its own subscription, never main's. L4 streams,
// lan-ingress, net-provider splices, the archiver and notify links have no
// end-to-end step (their unit tests are §5.5's).
func TestInboundEdgesReachOnlyPrimary(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{Ingress: true})
	a := d.dl()
	f := setupFabInbound(t, d, "apps/fab-in", "apps/fab-in-user")
	T, C := f.T, f.C

	// Bare URLs: the primary's code and code root.
	if c, b := a.do("GET", "/api/"+T+"/v", ""); c != 200 || b != "m1" {
		t.Errorf("bare /api/%s/v: %d %q, want main's m1", T, c, b)
	}
	if c, b := a.do("GET", "/c/"+T+"/"+probeFile, ""); c != 200 || b != "m1" {
		t.Errorf("bare /c/%s/%s: %d %q, want main's m1", T, probeFile, c, b)
	}
	if c, b := a.do("GET", "/c/"+T+"+dev/"+probeFile, ""); c != 200 || b != "m2" {
		t.Errorf("/c/%s+dev/%s for the owner: %d %q, want dev's m2", T, probeFile, c, b)
	}
	if c, b := a.do("GET", "/c/"+T+"/?native=1", ""); c != 200 || !strings.Contains(b, "native-m1.js") || strings.Contains(b, "native-m2.js") {
		t.Errorf("the native document of the bare URL: %d, want one built from main's code (native-m1.js):\n%.600s", c, b)
	}

	// Ingress: a Host-routed request on the ingress listener.
	var ic int
	var ib string
	if !waitFor(func() bool { ic, ib = fabIngress(t, d, f.host, "/v"); return ic == 200 }, time.Minute) || ib != "m1" {
		t.Errorf("ingress %s/v: %d %q, want main's m1", f.host, ic, ib)
	}

	// A consumer: through its slot URL and through its grant; never to a
	// deployment by name, the primary's included (a name breaks at the next
	// reassignment).
	if r := fabHTTP(t, a, C, "GET", "$XBIN_IFACE_FAB_URL/v", ""); r.Status != 200 || r.Body != "m1" || r.Deployment != "" {
		t.Errorf("%s through its slot URL: %+v, want main's m1 with no X-XBin-Deployment", C, r)
	}
	if r := fabHTTP(t, a, C, "GET", "/api/"+T+"/v", ""); r.Status != 200 || r.Body != "m1" {
		t.Errorf("%s through its grant: %+v, want main's m1", C, r)
	}
	for _, name := range []string{"dev", "main"} {
		if r := fabHTTP(t, a, C, "GET", "/api/"+T+"+"+name+"/v", ""); r.Status != 403 {
			t.Errorf("%s calling %s+%s: %+v, want 403 (other tiles reach only the bare URL)", C, T, name, r)
		}
	}

	// Cron: each deployment's backend registers a job, active for its own
	// deployment (D127h): main's ticks main alone, dev's ticks dev alone.
	cronJob := func(ref, name, path string) {
		t.Helper()
		r := fabHTTP(t, a, ref, "PUT", "/api/xbin/cron/jobs", fabJSON(map[string]string{
			"name": name, "resource": "res:" + T + "/cron", "schedule": "@every 1s", "path": path, "role": "writer"}))
		if r.Status != 200 {
			t.Fatalf("%s registering cron job %s: %+v", ref, name, r)
		}
		if strings.Contains(r.Body, `"dormant"`) {
			t.Errorf("%s registering cron job %s: %s, want it active", ref, name, r.Body)
		}
	}
	cronJob(T, "main-tick", "/tick")
	cronJob(T+"+dev", "dev-tick", "/dev-tick")
	if !waitFor(func() bool { return fabCount(fabHits(t, a, T), fabPath("/tick")) >= 3 }, time.Minute) {
		t.Fatalf("main's job never ticked main three times: %+v", fabHits(t, a, T))
	}
	if !waitFor(func() bool { return fabCount(fabHits(t, a, T+"+dev"), fabPath("/dev-tick")) >= 1 }, time.Minute) {
		t.Errorf("dev's job never ticked dev: %+v", fabHits(t, a, T+"+dev"))
	}
	if n := fabCount(fabHits(t, a, T), fabPath("/dev-tick")); n != 0 {
		t.Errorf("dev's job reached main %d times", n)
	}
	if n := fabCount(fabHits(t, a, T+"+dev"), fabPath("/tick")); n != 0 {
		t.Errorf("main's job reached dev %d times", n)
	}

	// Bus: a subscription of each deployment; a publish into the tile's bus
	// (main's namespace) delivers to main alone: dev's own-scope
	// subscription reads dev's namespace.
	subscribe := func(ref, name, path string) {
		t.Helper()
		r := fabHTTP(t, a, ref, "PUT", "/api/xbin/bus/subscriptions", fabJSON(map[string]string{
			"name": name, "resource": "res:" + T + "/bus", "prefix": "ev/", "path": path}))
		if r.Status != 200 {
			t.Fatalf("%s subscribing %s: %+v", ref, name, r)
		}
	}
	subscribe(T, "main-on", "/on")
	subscribe(T+"+dev", "dev-on", "/dev-on")
	fabMust200(t, a, "POST", "/api/xbin/bus/publish", `{"resource":"res:`+T+`/bus","topic":"ev/1","data":{"n":1}}`)
	if !waitFor(func() bool {
		return fabCount(fabHits(t, a, T), func(h fabHit) bool { return h.Path == "/on" && h.Topic == "ev/1" }) == 1
	}, 30*time.Second) {
		t.Fatalf("the publish never reached main's subscription: %+v", fabHits(t, a, T))
	}
	time.Sleep(2 * time.Second) // negative: main's namespace isn't dev's
	if n := fabCount(fabHits(t, a, T+"+dev"), fabPath("/dev-on")); n != 0 {
		t.Errorf("a publish in main's namespace reached dev's subscription %d times", n)
	}

	// dev's deliveries off (a manager's act): its job goes quiet; run now
	// fires it once, to dev.
	a.mustPost(t, "deliveries", dlBody(T, "deployment", "dev", "on", false))
	time.Sleep(1500 * time.Millisecond) // a tick in flight at the switch lands
	quietFrom := fabCount(fabHits(t, a, T+"+dev"), fabPath("/dev-tick"))
	time.Sleep(3 * time.Second) // negative: three of dev's every-second ticks would have fired
	if n := fabCount(fabHits(t, a, T+"+dev"), fabPath("/dev-tick")); n != quietFrom {
		t.Errorf("with its deliveries off dev's job fired %d times", n-quietFrom)
	}
	var rn struct {
		Delivery struct {
			Status int `json:"status"`
		} `json:"delivery"`
	}
	if c, _, raw := a.post(t, "run-now", dlBody(T, "deployment", "dev", "job", "dev-tick")); c != 200 ||
		json.Unmarshal([]byte(raw), &rn) != nil || rn.Delivery.Status != 200 {
		t.Fatalf("run now of dev's dev-tick: %d %s", c, raw)
	}
	devHits := fabHits(t, a, T+"+dev")
	if n := fabCount(devHits, func(h fabHit) bool {
		return h.Path == "/dev-tick" && h.From == "xbin/cron" && h.Deployment == ""
	}); n != quietFrom+1 {
		t.Errorf("run now reached dev %d times as xbin/cron, want once: %+v", n-quietFrom, devHits)
	}
	if n := fabCount(fabHits(t, a, T), fabPath("/dev-tick")); n != 0 {
		t.Errorf("run now of dev's job reached main %d times", n)
	}

	// Nothing but the test itself and dev's own registrations reached dev.
	for _, h := range fabHits(t, a, T+"+dev") {
		if fabForeign(h) && !(h.Path == "/dev-tick" && h.From == "xbin/cron") && !(h.Path == "/dev-on" && h.From == "xbin/bus") {
			t.Errorf("dev received an inbound call: %+v", h)
		}
	}
	for _, from := range []string{"ingress", C, "xbin/cron", "xbin/bus"} {
		if fabCount(fabHits(t, a, T), func(h fabHit) bool { return h.From == from }) == 0 {
			t.Errorf("main never saw a call from %s", from)
		}
	}

	// alwaysOn: both codes declare it; dev is pinned to its checkpoint of
	// B; an xbind restart starts main, never dev (its alwaysOn switch is
	// off), and dev's job, its deliveries off, doesn't either.
	if _, e := a.op(t, "deploy", T, "deployment", "dev"); e.Result != "ok" {
		t.Fatalf("pinning dev to its work tree: %+v", e)
	}
	if st := a.state(t, T); st.pinned("dev") == "" || st.LiveReload != "" {
		t.Fatalf("after deploying dev: dev pinned %q, live reload %q", st.pinned("dev"), st.LiveReload)
	}
	d.restart(t)
	if !waitFor(func() bool {
		for _, b := range fabBackends(t, a, T) {
			if b.Deployment == "" {
				return true
			}
		}
		return false
	}, 2*time.Minute) {
		t.Fatalf("main never started after the restart: %+v", fabBackends(t, a, T))
	}
	time.Sleep(3 * time.Second) // negative: nothing wakes dev
	for _, b := range fabBackends(t, a, T) {
		if b.Deployment != "" {
			t.Errorf("after the restart %s's %s runs (gen %d) with no request of its own", T, b.Deployment, b.Gen)
		}
	}
	if c, b := a.do("GET", "/api/"+T+"/v", ""); c != 200 || b != "m1" {
		t.Errorf("bare /api/%s/v after the restart: %d %q, want m1", T, c, b)
	}

	// Deliveries back on: dev's own job ticks dev, and its own publish
	// (dev's namespace) reaches its own subscription, never main's.
	a.mustPost(t, "deliveries", dlBody(T, "deployment", "dev", "on", true))
	if !waitFor(func() bool { return fabCount(fabHits(t, a, T+"+dev"), fabPath("/dev-tick")) >= 1 }, time.Minute) {
		t.Errorf("with deliveries on, dev's job never ticked dev: %+v", fabHits(t, a, T+"+dev"))
	}
	if r := fabHTTP(t, a, T+"+dev", "POST", "/api/xbin/bus/publish", `{"resource":"res:`+T+`/bus","topic":"ev/2","data":{"n":2}}`); r.Status != 200 {
		t.Fatalf("dev publishing into its own bus: %+v", r)
	}
	if !waitFor(func() bool {
		return fabCount(fabHits(t, a, T+"+dev"), func(h fabHit) bool { return h.Path == "/dev-on" && h.Topic == "ev/2" }) == 1
	}, 30*time.Second) {
		t.Errorf("with deliveries on, dev's publish never reached dev's subscription: %+v", fabHits(t, a, T+"+dev"))
	}
	time.Sleep(2 * time.Second) // negative: dev's namespace isn't main's
	if n := fabCount(fabHits(t, a, T), func(h fabHit) bool { return h.Topic == "ev/2" || h.Path == "/dev-tick" }); n != 0 {
		t.Errorf("dev's publish or job reached main %d times", n)
	}
}

// ---- TestReassignPrimaryFlowF ----

// fabRegs is each deployment's registrations of tile, from the state:
// "<kind>:<name>" → dormant.
func fabRegs(t *testing.T, a dlAPI, tile string) map[string]map[string]bool {
	t.Helper()
	body := fabMust200(t, a, "GET", "/api/xbin/deployments?tile="+tile, "")
	var st struct {
		Deployments []struct {
			Name          string `json:"name"`
			Registrations []struct {
				Kind    string `json:"kind"`
				Name    string `json:"name"`
				Dormant bool   `json:"dormant"`
			} `json:"registrations"`
		} `json:"deployments"`
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("the state of %s: %v", tile, err)
	}
	out := map[string]map[string]bool{}
	for _, d := range st.Deployments {
		out[d.Name] = map[string]bool{}
		for _, r := range d.Registrations {
			out[d.Name][r.Kind+":"+r.Name] = r.Dormant
		}
	}
	return out
}

// fabEnvHas waits until the probe at ref runs with XBIN_DEPLOYMENT set to
// want ("" = unset): the generation spawned with the wiring its role
// gives it (a reassignment restarts both deployments, D127j).
func fabEnvHas(t *testing.T, a dlAPI, ref, want string) {
	t.Helper()
	var env map[string]string
	if !waitFor(func() bool {
		env = nil
		c, b := a.do("GET", "/api/"+ref+"/env", "")
		return c == 200 && json.Unmarshal([]byte(b), &env) == nil && env["XBIN_DEPLOYMENT"] == want
	}, 2*time.Minute) {
		t.Fatalf("%s never ran with XBIN_DEPLOYMENT=%q: %v", ref, want, env)
	}
}

// covers D127d — flow F on 09-fabric §1's fixture, the registrations and the
// data of each deployment in place: reassigning the primary to dev (a
// manager's act, confirmed "data-stays", refused without it) moves every
// inbound edge to dev and dev's data: the bare URLs, the native document,
// ingress, the consumer's slot and grant calls, a prov#inst consumer (dev's
// instance table, the consumer re-wired) and bus deliveries (a publish into
// the tile's bus lands in dev's namespace, for dev's subscription); both
// deployments restart with the wiring their new role gives
// (XBIN_DEPLOYMENT); main stays pinned to A, with its data, its instance
// dormant, and its cron job and subscription its own: each deployment's job
// ticks it alone whichever is the primary (D127h, revised). Reassigning back
// restores all of it.
func TestReassignPrimaryFlowF(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{Ingress: true})
	a := d.dl()
	f := setupFabInbound(t, d, "apps/fab-f", "apps/fab-f-user")
	T, C := f.T, f.C
	pinnedA := a.state(t, T).pinned("main")

	// Each deployment's own data and registrations.
	kvURL := "/api/xbin/kv/res:" + T + "/kv/who"
	for ref, who := range map[string]string{T: "main", T + "+dev": "dev"} {
		if r := fabHTTP(t, a, ref, "PUT", kvURL, who); r.Status != 200 {
			t.Fatalf("%s writing its own kv: %+v", ref, r)
		}
	}
	register := func(ref, suffix string) {
		t.Helper()
		if r := fabHTTP(t, a, ref, "PUT", "/api/xbin/cron/jobs", fabJSON(map[string]string{"name": suffix + "-tick",
			"resource": "res:" + T + "/cron", "schedule": "@every 1s", "path": "/" + suffix + "-tick", "role": "writer"})); r.Status != 200 {
			t.Fatalf("%s registering its cron job: %+v", ref, r)
		}
		if r := fabHTTP(t, a, ref, "PUT", "/api/xbin/bus/subscriptions", fabJSON(map[string]string{"name": suffix + "-on",
			"resource": "res:" + T + "/bus", "prefix": "ev/", "path": "/" + suffix + "-on"})); r.Status != 200 {
			t.Fatalf("%s subscribing: %+v", ref, r)
		}
	}
	register(T, "main")
	register(T+"+dev", "dev")
	// Each deployment registers instance a of its fab-inst provide at its own
	// prefix; C2 binds T#a, which the primary's table resolves (09-fabric §8).
	for ref, dep := range map[string]string{T: "main", T + "+dev": "dev"} {
		r := fabHTTP(t, a, ref, "PUT", "/api/xbin/iface-instances", `{"instances":{"a":"/m/`+dep+`"}}`)
		if r.Status != 200 || strings.Contains(r.Body, `"dormant":true`) != (dep == "dev") {
			t.Fatalf("%s registering its instances: %+v, want dormant exactly for dev", ref, r)
		}
	}
	C2 := C + "-inst"
	writeFab(t, d.WS, C2, "i1", fabTile{extra: `,"interfaces":{"inst":{"kind":"http","service":"fab-inst"}}`})
	fabWait(t, a, C2, "i1")
	fabMust200(t, a, "POST", "/api/xbin/bindings", fabJSON(map[string]string{"component": C2, "slot": "inst", "provider": T + "#a"}))

	// The matrix: which deployment every inbound edge reaches, whose data
	// it serves, whose registrations fire.
	matrix := func(primary, other, marker, native string) {
		t.Helper()
		pref, oref := T, T+"+"+other
		if c, b := a.do("GET", "/api/"+T+"/v", ""); c != 200 || b != marker {
			t.Errorf("[%s primary] bare /api/%s/v: %d %q, want %q", primary, T, c, b, marker)
		}
		if c, b := a.do("GET", "/c/"+T+"/"+probeFile, ""); c != 200 || b != marker {
			t.Errorf("[%s primary] bare /c/%s/%s: %d %q, want %q", primary, T, probeFile, c, b, marker)
		}
		if c, b := a.do("GET", "/c/"+T+"/?native=1", ""); c != 200 || !strings.Contains(b, native) {
			t.Errorf("[%s primary] the native document: %d, want it built from %s's code (%s):\n%.400s", primary, c, primary, native, b)
		}
		if c, b := fabIngress(t, d, f.host, "/v"); c != 200 || b != marker {
			t.Errorf("[%s primary] ingress %s/v: %d %q, want %q", primary, f.host, c, b, marker)
		}
		if r := fabHTTP(t, a, C, "GET", "$XBIN_IFACE_FAB_URL/v", ""); r.Status != 200 || r.Body != marker {
			t.Errorf("[%s primary] %s through its slot URL: %+v, want %q", primary, C, r, marker)
		}
		if r := fabHTTP(t, a, C, "GET", "/api/"+T+"/v", ""); r.Status != 200 || r.Body != marker {
			t.Errorf("[%s primary] %s through its grant: %+v, want %q", primary, C, r, marker)
		}
		if r := fabHTTP(t, a, C, "GET", "/api/"+T+"+"+primary+"/v", ""); r.Status != 403 {
			t.Errorf("[%s primary] %s naming the primary: %+v, want 403", primary, C, r)
		}
		// T#a: the primary's instance table, the consumer re-wired to it.
		instURL := `"XBIN_IFACE_INST_URL":"http://xbin/api/` + T + `/m/` + primary + `"`
		if !waitFor(func() bool {
			c, b := a.do("GET", "/api/"+C2+"/env", "")
			return c == 200 && strings.Contains(b, instURL)
		}, time.Minute) {
			_, b := a.do("GET", "/api/"+C2+"/env", "")
			t.Errorf("[%s primary] %s's env never named %s's instance a: %s", primary, C2, primary, b)
		}
		if r := fabHTTP(t, a, C2, "GET", "$XBIN_IFACE_INST_URL/x", ""); r.Status != 200 ||
			fabCount(fabHits(t, a, T), func(h fabHit) bool { return h.From == C2 && h.Path == "/m/"+primary+"/x" }) == 0 {
			t.Errorf("[%s primary] %s through T#a: %+v, want %s's instance to answer", primary, C2, r, primary)
		}
		// The data inbound calls serve: the primary's; the other keeps its own.
		if c, b := a.do("GET", kvURL, ""); c != 200 || b != primary {
			t.Errorf("[%s primary] the tile's kv as the owner reads it: %d %q, want %s's", primary, c, b, primary)
		}
		if r := fabHTTP(t, a, pref, "GET", kvURL, ""); r.Status != 200 || r.Body != primary {
			t.Errorf("[%s primary] the bare URL's backend reads its kv: %+v, want %s's", primary, r, primary)
		}
		if r := fabHTTP(t, a, oref, "GET", kvURL, ""); r.Status != 200 || r.Body != other {
			t.Errorf("[%s primary] %s reads its own kv: %+v, want %s's", primary, other, r, other)
		}
		// Registrations: cron jobs and subscriptions active for each
		// deployment; the instance the primary's alone, the other's dormant.
		regs := fabRegs(t, a, T)
		for dep, want := range map[string]bool{primary: false, other: true} {
			for k, dormant := range map[string]bool{"cron:" + dep + "-tick": false, "bus:" + dep + "-on": false, "iface-instance:a": want} {
				if got, ok := regs[dep][k]; !ok || got != dormant {
					t.Errorf("[%s primary] %s's %s: listed %v, dormant %v, want dormant %v", primary, dep, k, ok, got, dormant)
				}
			}
		}
		// Cron: each deployment's job ticks it alone.
		for ref, dep := range map[string]string{T: primary, oref: other} {
			start := fabCount(fabHits(t, a, ref), fabPath("/"+dep+"-tick"))
			if !waitFor(func() bool { return fabCount(fabHits(t, a, ref), fabPath("/"+dep+"-tick")) >= start+3 }, time.Minute) {
				t.Errorf("[%s primary] %s's job never ticked it three times: %+v", primary, dep, fabHits(t, a, ref))
			}
		}
		if n := fabCount(fabHits(t, a, T), fabPath("/"+other+"-tick")); n != 0 {
			t.Errorf("[%s primary] %s's job reached the primary %d times", primary, other, n)
		}
		if n := fabCount(fabHits(t, a, oref), fabPath("/"+primary+"-tick")); n != 0 {
			t.Errorf("[%s primary] the primary's job reached %s %d times", primary, other, n)
		}
		// Bus: a publish into the tile's bus lands in the primary's namespace.
		topic := "ev/" + primary
		fabMust200(t, a, "POST", "/api/xbin/bus/publish", `{"resource":"res:`+T+`/bus","topic":"`+topic+`","data":{}}`)
		if !waitFor(func() bool {
			return fabCount(fabHits(t, a, T), func(h fabHit) bool { return h.Path == "/"+primary+"-on" && h.Topic == topic }) == 1
		}, 30*time.Second) {
			t.Errorf("[%s primary] the publish never reached %s's subscription: %+v", primary, primary, fabHits(t, a, T))
		}
		time.Sleep(2 * time.Second) // negative: the other's subscription reads its own namespace
		if n := fabCount(fabHits(t, a, oref), func(h fabHit) bool { return h.Topic == topic }); n != 0 {
			t.Errorf("[%s primary] the publish reached %s %d times", primary, other, n)
		}
	}
	matrix("main", "dev", "m1", "native-m1.js")

	// Reassign: refused without the loud confirmation, then routing moves.
	if c, _, raw := a.post(t, "primary", dlBody(T, "deployment", "dev")); c != 400 || !strings.Contains(raw, `confirm:\"data-stays\"`) {
		t.Errorf("reassigning without confirm: %d %s, want 400 naming data-stays", c, raw)
	}
	if st := a.state(t, T); st.Primary != "main" {
		t.Fatalf("a refused reassignment moved the primary to %q", st.Primary)
	}
	a.mustPost(t, "primary", dlBody(T, "deployment", "dev", "confirm", "data-stays"))
	st := a.state(t, T)
	if st.Primary != "dev" || st.pinned("main") != pinnedA || st.LiveReload != "dev" {
		t.Fatalf("after the reassignment: primary %q, main pinned %q (was %q), live reload %q", st.Primary, st.pinned("main"), pinnedA, st.LiveReload)
	}
	fabEnvHas(t, a, T, "")
	fabEnvHas(t, a, T+"+main", "main")
	matrix("dev", "main", "m2", "native-m2.js")

	// Back: main is the primary again, pinned where it was.
	a.mustPost(t, "primary", dlBody(T, "deployment", "main", "confirm", "data-stays"))
	st = a.state(t, T)
	if st.Primary != "main" || st.pinned("main") != pinnedA || st.LiveReload != "dev" {
		t.Fatalf("after reassigning back: primary %q, main pinned %q (was %q), live reload %q", st.Primary, st.pinned("main"), pinnedA, st.LiveReload)
	}
	fabEnvHas(t, a, T, "")
	fabEnvHas(t, a, T+"+dev", "dev")
	matrix("main", "dev", "m1", "native-m1.js")
}

// ---- TestOutboundEdgePolicy ----

// fabEdge is one Edge of the deployments state (11-contract §1.1).
type fabEdge struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	To        string   `json:"to"`
	Role      string   `json:"role"`
	Policy    string   `json:"policy"`
	Default   string   `json:"default"`
	Values    []string `json:"values"`
	Effective string   `json:"effective"`
	Why       string   `json:"why"`
	Refused   int64    `json:"refused"`
	Clamped   int64    `json:"clamped"`
}

// fabEdges is tile's edges, by id, from the full view of its state.
func fabEdges(t *testing.T, a dlAPI, tile string) map[string]fabEdge {
	t.Helper()
	body := fabMust200(t, a, "GET", "/api/xbin/deployments?tile="+tile, "")
	var st struct {
		Edges []fabEdge `json:"edges"`
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("the state of %s: %v", tile, err)
	}
	out := map[string]fabEdge{}
	for _, e := range st.Edges {
		out[e.ID] = e
	}
	return out
}

// fabBlockOnly checks that tile's edge id takes block alone, by default,
// with the reason named (D127o), and that a tile manager can't set any other
// value on it: 400, naming the edge.
func fabBlockOnly(t *testing.T, a dlAPI, tile, id, why string, try ...string) {
	t.Helper()
	e, ok := fabEdges(t, a, tile)[id]
	switch {
	case !ok:
		t.Errorf("%s lists no edge %s: %v", tile, id, fabEdges(t, a, tile))
		return
	case len(e.Values) != 1 || e.Values[0] != "block" || e.Default != "block" || e.Policy != "block":
		t.Errorf("%s's edge %s: %+v, want block alone, by default", tile, id, e)
	case !strings.Contains(e.Why, why):
		t.Errorf("%s's edge %s says why %q, want it to name %q", tile, id, e.Why, why)
	}
	for _, v := range try {
		if c, _, raw := a.post(t, "edge", dlBody(tile, "edge", id, "policy", v)); c != 400 || !strings.Contains(raw, id+" takes block") {
			t.Errorf("setting %s's %s to %s: %d %s, want 400 (no override, D127o)", tile, id, v, c, raw)
		}
	}
}

// covers D127a D127f D127o T4 SC-CLAMP — a dev consumer's outbound calls under the
// edge policy, each tried from dev and from main. Over each clampable edge
// kind (an http interface binding at writer, a grant call at writer, a
// cross-scope res: grant at writer, a bus subscription on another scope's
// bus) no write from dev succeeds: the provider's writer-guarded route
// (llm-gw's shape) refuses it and its reader route answers, the provider's
// primary sees reader and X-XBin-Deployment: dev, xbind's kv refuses the
// write, the publish is refused, and the subscription, like a read bind,
// reads the provider's primary: a publish there reaches dev (D127h, revised).
// block on an edge fails the call closed naming the policy (a bus
// subscription at its next delivery, a new one at once), and the edge
// counts its clamps and refusals. Edges nothing narrows (a custom role with
// no path to reader, a stream slot, a lan-ingress slot, a net-provider
// splice) take block alone, refuse any override, and a stream dial from dev
// is refused while main's reaches the stream. With net bound to host, dev
// has no egress while main reaches the host. main's calls carry writer and
// no X-XBin-Deployment, as before tile deployments.
func TestOutboundEdgePolicy(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	const (
		C  = "apps/fab-out"           // the consumer, with a dev deployment
		P  = "apps/fab-out-prov"      // provides fab-svc at writer; its kv and bus are another scope's
		G  = "apps/fab-out-grant"     // C holds a grant row on it at writer
		Q  = "apps/fab-out-chan"      // provides fab-chan at the custom role channel, which implies nothing
		S  = "apps/fab-out-stream"    // exposes a stream (a TCP echo)
		H  = "apps/fab-out-host"      // its net is bound to the host
		N  = "apps/fab-out-netprov"   // provides a net and a lan-ingress
		NC = "apps/fab-out-netclient" // its net is bound to N: a splice
		L  = "apps/fab-out-lan"       // a lan-ingress slot bound to N
	)
	roles := `,"expose":{"roles":{"reader":"read the probe","writer":"change the probe"}}`
	writeFab(t, d.WS, P, "p1", fabTile{extra: roles + `,"provides":{"svc":{"kind":"http","service":"fab-svc","role":"writer"}}`})
	writeFab(t, d.WS, G, "g1", fabTile{extra: roles})
	writeFab(t, d.WS, Q, "q1", fabTile{extra: `,"expose":{"roles":{"channel":"post messages","reader":"read the probe"}}` +
		`,"provides":{"chan":{"kind":"http","service":"fab-chan","role":"channel"}}`})
	writeFab(t, d.WS, S, "s1", fabTile{echo: 7777, extra: `,"exposes":{"echo":{"kind":"stream","port":7777}}`})
	writeFab(t, d.WS, C, "c1", fabTile{
		uses: []string{`{"target":"` + G + `","role":"writer"}`, `{"target":"res:` + P + `/kv","role":"writer"}`,
			`{"target":"res:` + P + `/bus","role":"writer"}`},
		extra: `,"interfaces":{"svc":{"kind":"http","service":"fab-svc"},"chan":{"kind":"http","service":"fab-chan"},"db":{"kind":"stream"}}`,
	})
	writeFab(t, d.WS, H, "h1", fabTile{extra: `,"interfaces":{"net":{"kind":"net"}}`})
	writeFab(t, d.WS, N, "n1", fabTile{extra: `,"provides":{"egress":{"kind":"net"},"lan":{"kind":"lan-ingress"}}`})
	writeFab(t, d.WS, NC, "nc1", fabTile{extra: `,"interfaces":{"net":{"kind":"net"}}`})
	writeFab(t, d.WS, L, "l1", fabTile{extra: `,"interfaces":{"lan":{"kind":"lan-ingress"}}`})
	for tile, m := range map[string]string{P: "p1", G: "g1", Q: "q1", S: "s1", C: "c1", H: "h1"} {
		fabWait(t, a, tile, m)
	}
	a.waitTile(t, NC)
	a.waitTile(t, L)
	for _, b := range []map[string]string{
		{"component": C, "slot": "svc", "provider": P}, {"component": C, "slot": "chan", "provider": Q},
		{"component": C, "slot": "db", "provider": S + "#echo"}, {"component": H, "slot": "net", "provider": "host"},
		{"component": NC, "slot": "net", "provider": N}, {"component": L, "slot": "lan", "provider": N},
	} {
		fabMust200(t, a, "POST", "/api/xbin/bindings", fabJSON(b))
	}
	for _, g := range [][2]string{{G, "writer"}, {"res:" + P + "/kv", "writer"}, {"res:" + P + "/bus", "writer"}} {
		fabMust200(t, a, "POST", "/api/xbin/grants", fabJSON(map[string]string{"from": C, "target": g[0], "role": g[1]}))
	}
	// The bindings and grants restart C with its wiring.
	if !waitFor(func() bool {
		c, b := a.do("GET", "/api/"+C+"/env", "")
		return c == 200 && strings.Contains(b, `"XBIN_IFACE_SVC_URL"`) && strings.Contains(b, `"XBIN_IFACE_CHAN_URL"`) &&
			strings.Contains(b, `"XBIN_IFACE_DB_ADDR"`)
	}, 2*time.Minute) {
		_, b := a.do("GET", "/api/"+C+"/env", "")
		t.Fatalf("%s's env never carried its slots: %s", C, b)
	}
	fabWait(t, a, H, "h1")

	// Each gets a dev deployment, pinned to its work tree as it is; main
	// keeps following the work tree.
	for _, tile := range []string{C, H, NC, L} {
		if _, e := a.op(t, "add", tile, "deployment", "dev"); e.Result != "ok" {
			t.Fatalf("adding %s's dev: %+v", tile, e)
		}
	}
	fabWait(t, a, C+"+dev", "c1")
	fabWait(t, a, H+"+dev", "h1")
	dev := C + "+dev"

	// An http interface binding at writer (llm-gw's openai shape): main
	// writes; dev's call arrives at reader, marked, and only the reader
	// route answers it.
	if r := fabHTTP(t, a, C, "GET", "$XBIN_IFACE_SVC_URL/w", ""); r.Status != 200 || r.Body != "write ok" {
		t.Errorf("main's write through slot svc: %+v", r)
	}
	if r := fabHTTP(t, a, dev, "GET", "$XBIN_IFACE_SVC_URL/w", ""); r.Status != 403 {
		t.Errorf("dev's write through slot svc: %+v, want the provider's 403 (writer clamped to reader)", r)
	}
	if r := fabHTTP(t, a, dev, "GET", "$XBIN_IFACE_SVC_URL/r", ""); r.Status != 200 || r.Body != "read ok" || r.Deployment != "" {
		t.Errorf("dev's read through slot svc: %+v, want the reader route's answer from P's primary", r)
	}
	// A grant call at writer: the same.
	if r := fabHTTP(t, a, C, "GET", "/api/"+G+"/w", ""); r.Status != 200 {
		t.Errorf("main's write through its grant on %s: %+v", G, r)
	}
	if r := fabHTTP(t, a, dev, "GET", "/api/"+G+"/w", ""); r.Status != 403 {
		t.Errorf("dev's write through its grant on %s: %+v, want 403", G, r)
	}
	if r := fabHTTP(t, a, dev, "GET", "/api/"+G+"/r", ""); r.Status != 200 {
		t.Errorf("dev's read through its grant on %s: %+v", G, r)
	}
	// What the providers' primaries saw: main at writer, unmarked; dev at
	// reader, marked dev; From the tile path either way.
	for _, prov := range []string{P, G} {
		for _, h := range fabHits(t, a, prov) {
			if h.From != C {
				continue
			}
			switch h.Deployment {
			case "":
				if h.Role != "writer" {
					t.Errorf("%s saw main's call at %q: %+v, want writer", prov, h.Role, h)
				}
			case "dev":
				if h.Role != "reader" {
					t.Errorf("%s saw dev's call at %q: %+v, want reader", prov, h.Role, h)
				}
			default:
				t.Errorf("%s saw a call marked %q: %+v", prov, h.Deployment, h)
			}
		}
		hits := fabHits(t, a, prov)
		if fabCount(hits, func(h fabHit) bool { return h.From == C && h.Deployment == "dev" }) == 0 ||
			fabCount(hits, func(h fabHit) bool { return h.From == C && h.Deployment == "" }) == 0 {
			t.Errorf("%s didn't see both of %s's deployments call: %+v", prov, C, hits)
		}
	}

	// A resource grant in another scope, at writer: dev reads, never writes.
	kvURL := "/api/xbin/kv/res:" + P + "/kv/k"
	if r := fabHTTP(t, a, C, "PUT", kvURL, "from-main"); r.Status != 200 {
		t.Errorf("main's write to %s's kv: %+v", P, r)
	}
	if r := fabHTTP(t, a, dev, "PUT", kvURL, "from-dev"); r.Status != 403 || !strings.Contains(r.Body, "read-only") {
		t.Errorf("dev's write to %s's kv: %+v, want 403 (read-only across scopes)", P, r)
	}
	if r := fabHTTP(t, a, dev, "GET", kvURL, ""); r.Status != 200 || r.Body != "from-main" {
		t.Errorf("dev's read of %s's kv: %+v, want main's value", P, r)
	}
	// Another scope's bus: no publish from dev; its subscription reads the
	// provider's primary, like a read bind: main's publish reaches dev.
	pub := `{"resource":"res:` + P + `/bus","topic":"ev/x","data":{}}`
	if r := fabHTTP(t, a, C, "POST", "/api/xbin/bus/publish", pub); r.Status != 200 {
		t.Errorf("main's publish on %s's bus: %+v", P, r)
	}
	if r := fabHTTP(t, a, dev, "POST", "/api/xbin/bus/publish", pub); r.Status != 403 {
		t.Errorf("dev's publish on %s's bus: %+v, want 403", P, r)
	}
	sub := fabJSON(map[string]string{"name": "prov-ev", "resource": "res:" + P + "/bus", "prefix": "ev/", "path": "/on"})
	if r := fabHTTP(t, a, dev, "PUT", "/api/xbin/bus/subscriptions", sub); r.Status != 200 || strings.Contains(r.Body, `"dormant"`) {
		t.Errorf("dev subscribing to %s's bus: %+v, want it active", P, r)
	}
	busTo := func(topic string) int {
		return fabCount(fabHits(t, a, dev), func(h fabHit) bool { return h.Path == "/on" && h.From == "xbin/bus" && h.Topic == topic })
	}
	fabMust200(t, a, "POST", "/api/xbin/bus/publish", `{"resource":"res:`+P+`/bus","topic":"ev/y","data":{}}`)
	if !waitFor(func() bool { return busTo("ev/y") == 1 }, 30*time.Second) {
		t.Errorf("a publish on %s's bus never reached dev's subscription: %+v", P, fabHits(t, a, dev))
	}
	if e := fabEdges(t, a, C)["slot:svc"]; e.Clamped == 0 || e.Policy != "read" {
		t.Errorf("slot:svc after dev's clamped calls: %+v, want read with clamps counted", e)
	}

	// block fails closed, naming the policy; main never notices.
	a.mustPost(t, "edge", dlBody(C, "edge", "grant:"+G, "policy", "block"))
	if r := fabHTTP(t, a, dev, "GET", "/api/"+G+"/r", ""); r.Status != 403 || !strings.Contains(r.Body, `the tile's edge policy for it is \"block\"`) {
		t.Errorf("dev's read of %s with grant:%s at block: %+v, want 403 naming the policy", G, G, r)
	}
	if r := fabHTTP(t, a, C, "GET", "/api/"+G+"/w", ""); r.Status != 200 {
		t.Errorf("main's write to %s with grant:%s at block: %+v (the primary uses every edge as before)", G, G, r)
	}
	if e := fabEdges(t, a, C)["grant:"+G]; e.Policy != "block" || e.Refused == 0 {
		t.Errorf("grant:%s at block: %+v, want its refusal counted", G, e)
	}
	a.mustPost(t, "edge", dlBody(C, "edge", "grant:res:"+P+"/bus", "policy", "block"))
	sub2 := fabJSON(map[string]string{"name": "prov-ev2", "resource": "res:" + P + "/bus", "prefix": "ev/", "path": "/on"})
	if r := fabHTTP(t, a, dev, "PUT", "/api/xbin/bus/subscriptions", sub2); r.Status != 403 {
		t.Errorf("dev subscribing to %s's bus at block: %+v, want 403", P, r)
	}
	fabMust200(t, a, "POST", "/api/xbin/bus/publish", `{"resource":"res:`+P+`/bus","topic":"ev/z","data":{}}`)
	time.Sleep(2 * time.Second) // negative: the delivery asks the edge again
	if n := busTo("ev/z"); n != 0 {
		t.Errorf("at block a publish on %s's bus reached dev's subscription %d times", P, n)
	}
	for _, id := range []string{"grant:" + G, "grant:res:" + P + "/bus"} {
		a.mustPost(t, "edge", dlBody(C, "edge", id, "policy", "default"))
	}
	if r := fabHTTP(t, a, dev, "GET", "/api/"+G+"/r", ""); r.Status != 200 {
		t.Errorf("dev's read of %s back at default: %+v", G, r)
	}

	// A custom role with no path to reader: blocked for dev, no override.
	if r := fabHTTP(t, a, C, "GET", "$XBIN_IFACE_CHAN_URL/v", ""); r.Status != 200 || r.Body != "q1" {
		t.Errorf("main through slot chan: %+v", r)
	}
	if r := fabHTTP(t, a, dev, "GET", "$XBIN_IFACE_CHAN_URL/v", ""); r.Status != 403 || !strings.Contains(r.Body, `custom role \"channel\"`) {
		t.Errorf("dev through slot chan: %+v, want 403 naming the custom role", r)
	}
	if n := fabCount(fabHits(t, a, Q), func(h fabHit) bool { return h.Deployment == "dev" }); n != 0 {
		t.Errorf("%s received %d of dev's calls", Q, n)
	}
	fabBlockOnly(t, a, C, "slot:chan", `custom role "channel"`, "read")

	// A stream slot: main's dial reaches the echo; dev's is closed at once,
	// with one line in dev's log.
	if r := fabAct(t, a, C, "echo", url.Values{"env": {"XBIN_IFACE_DB_ADDR"}}); r.Status != 200 || r.Body != "ping" {
		t.Errorf("main through stream slot db: %+v", r)
	}
	if r := fabAct(t, a, dev, "echo", url.Values{"env": {"XBIN_IFACE_DB_ADDR"}}); r.Err == "" {
		t.Errorf("dev through stream slot db: %+v, want the dial refused", r)
	}
	// 09-fabric §5.7's log line isn't asserted: dev's sandbox gets no
	// gateway forward to dial, so the refusal happens before the runner's
	// per-dial check, which writes the line (and counts it for the panel).
	fabBlockOnly(t, a, C, "slot:db", "stream edges can't be read-clamped", "read")

	// A lan-ingress slot and a net-provider splice: block alone.
	fabBlockOnly(t, a, L, "slot:lan", "lan-ingress links can't be read-clamped", "read")
	fabBlockOnly(t, a, NC, "slot:net", "net provider splices serve the tile's primary only", "inherit")

	// The net bound to the host: main shares the host's network, dev gets no
	// egress at all.
	if e := fabEdges(t, a, H)["slot:net"]; e.Effective != "block" || !strings.Contains(e.Why, "host networking serves the tile's primary only") {
		t.Errorf("%s's slot:net: %+v, want effective block naming host sharing", H, e)
	}
	if r := fabAct(t, a, H, "dial", url.Values{"addr": {d.Addr}}); r.Status != 200 {
		t.Errorf("%s's main dialing the host's %s: %+v, want it reached (host networking)", H, d.Addr, r)
	}
	if r := fabAct(t, a, H+"+dev", "dial", url.Values{"addr": {d.Addr}}); r.Err == "" {
		t.Errorf("%s's dev dialing the host's %s: %+v, want no egress", H, d.Addr, r)
	}
}

// fabSource is the fabric probe's backend/main.go; writeFab puts the quoted
// marker in place of __MARKER__. It serves:
//   - GET /v: the marker compiled in;
//   - GET /hits: every request it was sent (method, path, xbind's caller
//     headers, a bus event's subscription and topic), oldest first;
//   - GET /caller: this request's caller;
//   - GET /r and GET /w: guarded at reader and at writer, llm-gw's shape;
//   - GET /env: XBIN_RES_*, XBIN_IFACE_*, XBIN_DEPLOYMENT, XBIN_COMPONENT;
//   - GET /act/http?method=&url=&body=: one call through the gateway with
//     its own identity, url a gateway path or "$VAR/…" (a slot's URL);
//   - GET /act/dial?addr=: a TCP connect (egress);
//   - GET /act/echo?env=: a round trip through the stream address in env;
//   - GET /act/alloc?mib=: allocates and touches that much memory, kept;
//   - any other request: recorded, answered 200.
//
// With echo.port in its directory it also runs a TCP echo on that port.
const fabSource = `package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const marker = __MARKER__

type hit struct {
	Method       string ` + "`json:\"method\"`" + `
	Path         string ` + "`json:\"path\"`" + `
	From         string ` + "`json:\"from\"`" + `
	Role         string ` + "`json:\"role\"`" + `
	Deployment   string ` + "`json:\"deployment\"`" + `
	Subscription string ` + "`json:\"subscription,omitempty\"`" + `
	Topic        string ` + "`json:\"topic,omitempty\"`" + `
}

var (
	mu   sync.Mutex
	hits = []hit{}
	held [][]byte
)

func record(r *http.Request) {
	c := xbin.Caller(r)
	h := hit{Method: r.Method, Path: r.URL.Path, From: c.From, Role: c.Role, Deployment: c.Deployment}
	if r.Method == "POST" {
		var ev xbin.BusEvent
		if b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20)); json.Unmarshal(b, &ev) == nil {
			h.Subscription, h.Topic = ev.Subscription, ev.Topic
		}
	}
	mu.Lock()
	hits = append(hits, h)
	mu.Unlock()
}

func echo(port string) {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) { defer c.Close(); io.Copy(c, c) }(c)
	}
}

func act(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out := map[string]any{}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	switch r.PathValue("verb") {
	case "http":
		u := q.Get("url")
		if strings.HasPrefix(u, "$") {
			name, rest, _ := strings.Cut(u[1:], "/")
			u = os.Getenv(name) + "/" + rest
		} else {
			u = "http://xbin" + u
		}
		var body io.Reader
		if b := q.Get("body"); b != "" {
			body = strings.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, q.Get("method"), u, body)
		if err != nil {
			out["err"] = err.Error()
			break
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := xbin.Client().Do(req)
		if err != nil {
			out["err"] = err.Error()
			break
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		out["status"], out["body"], out["deployment"] = resp.StatusCode, string(b), resp.Header.Get("X-XBin-Deployment")
	case "dial":
		c, err := net.DialTimeout("tcp", q.Get("addr"), 3*time.Second)
		if err != nil {
			out["err"] = err.Error()
			break
		}
		c.Close()
		out["status"] = 200
	case "echo":
		c, err := net.DialTimeout("tcp", os.Getenv(q.Get("env")), 5*time.Second)
		if err != nil {
			out["err"] = err.Error()
			break
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			out["err"] = err.Error()
			break
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			out["err"] = err.Error()
			break
		}
		out["status"], out["body"] = 200, string(buf)
	case "alloc":
		n, _ := strconv.Atoi(q.Get("mib"))
		for i := 0; i < n; i++ {
			b := make([]byte, 1<<20)
			for j := range b {
				b[j] = byte(i + j)
			}
			mu.Lock()
			held = append(held, b)
			mu.Unlock()
		}
		out["status"] = 200
	default:
		xbin.WriteError(w, 404, "no such act")
		return
	}
	xbin.WriteJSON(w, 200, out)
}

func main() {
	if b, err := os.ReadFile("echo.port"); err == nil {
		go echo(strings.TrimSpace(string(b)))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, marker) })
	mux.HandleFunc("GET /hits", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		xbin.WriteJSON(w, 200, hits)
	})
	mux.HandleFunc("GET /caller", func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		xbin.WriteJSON(w, 200, map[string]string{"from": c.From, "role": c.Role, "deployment": c.Deployment})
	})
	mux.Handle("GET /r", xbin.RoleFunc("reader", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "read ok") }))
	mux.Handle("GET /w", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "write ok") }))
	mux.HandleFunc("GET /env", func(w http.ResponseWriter, r *http.Request) {
		env := map[string]string{}
		for _, e := range os.Environ() {
			k, v, _ := strings.Cut(e, "=")
			if strings.HasPrefix(k, "XBIN_RES_") || strings.HasPrefix(k, "XBIN_IFACE_") || k == "XBIN_DEPLOYMENT" || k == "XBIN_COMPONENT" {
				env[k] = v
			}
		}
		xbin.WriteJSON(w, 200, env)
	})
	mux.HandleFunc("GET /act/{verb}", act)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { xbin.WriteJSON(w, 200, map[string]bool{"ok": true}) })
	rec := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hits" {
			record(r)
		}
		mux.ServeHTTP(w, r)
	})
	xbin.Serve(rec)
}
`
