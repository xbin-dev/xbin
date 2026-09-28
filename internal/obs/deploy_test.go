package obs

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// depRig is testPlane with the deployment input the broker installs,
// answering as the deployments plane does (its Primary and Addressed):
// apps/calendar has main (the primary) and dev, apps/email has main and dev
// (the primary); a primary can be moved or protected.
type depRig struct {
	*Plane
	mu        sync.Mutex
	primary   map[string]string
	protected map[string]bool
	deps      map[string][]string
}

func newDepRig(t *testing.T) *depRig {
	t.Helper()
	r := &depRig{Plane: testPlane(t),
		primary:   map[string]string{"apps/calendar": "main", "apps/email": "dev"},
		protected: map[string]bool{},
		deps:      map[string][]string{"apps/calendar": {"main", "dev", "evil"}, "apps/email": {"main", "dev"}}}
	r.Primary = func(tile string) string {
		r.mu.Lock()
		defer r.mu.Unlock()
		return cmp.Or(r.primary[tile], util.MainDeployment)
	}
	r.Addressed = func(p auth.Principal, tile string) (string, error) {
		primary := r.Primary(tile)
		if p.Component != tile {
			return primary, nil
		}
		session, dep := p.Via == "terminal", p.Deployment
		switch {
		case dep == "" && !session:
			return util.MainDeployment, nil
		case dep == "":
			dep = primary
		case !slices.Contains(r.deps[tile], dep):
			return "", util.NoDeployment(tile, dep)
		}
		r.mu.Lock()
		protected := r.protected[tile]
		r.mu.Unlock()
		if session && protected && dep == primary {
			return "", fmt.Errorf("the primary of %s is protected: terminal and agent sessions can't target it", tile)
		}
		return dep, nil
	}
	return r
}

func (r *depRig) setPrimary(tile, dep string) {
	r.mu.Lock()
	r.primary[tile] = dep
	r.mu.Unlock()
}

// The rig's principals.
var (
	calMain  = auth.Principal{Component: "apps/calendar", Via: "instance"}
	calDev   = auth.Principal{Component: "apps/calendar", Via: "instance", Deployment: "dev"}
	calGone  = auth.Principal{Component: "apps/calendar", Via: "instance", Deployment: "gone"}
	calTerm  = auth.Principal{Component: "apps/calendar", Via: "terminal"} // follows the primary
	emailMn  = auth.Principal{Component: "apps/email", Via: "instance"}
	emailDev = auth.Principal{Component: "apps/email", Via: "instance", Deployment: "dev"}
	ownerP   = auth.Principal{Owner: true}
)

// statusWatch follows the status plane's events: today's status type and
// the deployments event's op status, each rendered without its ts.
type statusWatch struct {
	t      *testing.T
	o      *depRig
	seen   <-chan events.Event
	probes int
}

func watchStatus(t *testing.T, o *depRig) *statusWatch {
	t.Helper()
	go o.watchStatusRestarts()
	seen, stop := o.Hub.Subscribe(func(e events.Event) bool {
		return e.Type == "status" || e.Type == "deployments" && statusFacts(e).Op == "status"
	})
	t.Cleanup(stop)
	w := &statusWatch{t: t, o: o, seen: seen}
	for i := 0; ; i++ { // the watcher is up once it clears a probe
		if _, ok := w.probe(20 * time.Millisecond); ok {
			break
		}
		if i == 500 {
			t.Fatal("the watcher never subscribed")
		}
	}
	o.statusMu.Lock() // the probes published before it subscribed
	for k := range o.statuses {
		if strings.HasPrefix(k, "probe-") {
			delete(o.statuses, k)
		}
	}
	o.statusMu.Unlock()
	return w
}

// probe publishes a build-start on a fresh probe tile that holds a status
// and returns the other events published before the watcher cleared it: it
// handles events in order, so everything published before the probe has
// been handled by then.
func (w *statusWatch) probe(wait time.Duration) ([]string, bool) {
	w.probes++
	name := fmt.Sprintf("probe-%d", w.probes)
	w.o.statusMu.Lock()
	w.o.statuses[name] = statusRec{Level: "warn", Message: "probe"}
	w.o.statusMu.Unlock()
	w.o.Hub.Publish(events.Event{Type: "build-start", Component: name})
	var out []string
	timeout := time.After(wait)
	for {
		select {
		case e := <-w.seen:
			switch {
			case e.Component == name:
				return out, true
			case strings.HasPrefix(e.Component, "probe-"): // an earlier probe's
			default:
				out = append(out, render(e))
			}
		case <-timeout:
			return out, false
		}
	}
}

// settle returns the events published so far, once the watcher handled them.
func (w *statusWatch) settle(what string) []string {
	w.t.Helper()
	out, ok := w.probe(10 * time.Second)
	if !ok {
		w.t.Fatalf("%s: the watcher never handled the probe", what)
	}
	return out
}

// render is an event as "<type> <component> <data without ts>".
func render(e events.Event) string {
	var d map[string]any
	b, _ := json.Marshal(e.Data)
	_ = json.Unmarshal(b, &d)
	delete(d, "ts")
	b, _ = json.Marshal(d)
	return e.Type + " " + e.Component + " " + string(b)
}

// report posts a status as p; query is ?component= and the like.
func report(t *testing.T, o *depRig, p auth.Principal, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/tile-report"+query, strings.NewReader(body))
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	o.apiStatusSet(w, r)
	return w
}

// listed is GET /tile-report's answer for the owner, as raw JSON.
func listed(t *testing.T, o *depRig) string {
	t.Helper()
	r := httptest.NewRequest("GET", "/tile-report", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), ownerP))
	w := httptest.NewRecorder()
	o.apiStatusList(w, r)
	return strings.TrimSpace(regexpTS(w.Body.String()))
}

// regexpTS drops the ts fields, which vary.
func regexpTS(s string) string {
	for {
		i := strings.Index(s, `,"ts":`)
		if i < 0 {
			return s
		}
		j := i + len(`,"ts":`)
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		s = s[:i] + s[j:]
	}
}

func (o *depRig) stored(key string) string {
	o.statusMu.Lock()
	defer o.statusMu.Unlock()
	rec, ok := o.statuses[key]
	if !ok {
		return "-"
	}
	return rec.Level + ":" + rec.Message
}

// covers D127h PO-5 SC-EVENTS — status is per deployment
// (TestNonPrimaryStatusNamespaced): a non-primary deployment's report,
// transient notice and clear answer as today and ride only the deployments
// event (op status, with its deployment), never a status event, never GET
// /tile-report, and never touch the primary's status; a non-primary build
// (op build, phase start) or deploy swap clears only that deployment's
// status, and the primary's build-start only the primary's; a primary that
// isn't main reports as today with the bare component, and main beside it
// is the non-primary one; an admin reporting for a tile reports for its
// primary; a reassignment of the primary exchanges the two statuses, and a
// removed deployment's status goes; a credential whose deployment is gone
// gets 404, a session following a protected primary 403. Built on testPlane (status_test.go).
func TestStatusPerDeployment(t *testing.T) {
	o := newDepRig(t)
	w := watchStatus(t, o)
	check := func(what string, got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("%s: published\n  %s\nwant\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
	ok := func(rec *httptest.ResponseRecorder, what string) {
		t.Helper()
		if rec.Code != 200 || rec.Body.String() != "{\"ok\":\"true\"}\n" {
			t.Fatalf("%s: %d %q, want today's answer", what, rec.Code, rec.Body)
		}
	}

	ok(report(t, o, calMain, "", `{"level":"error","message":"main broken"}`), "main's report")
	check("main's report", w.settle("main"),
		`status apps/calendar {"level":"error","message":"main broken"}`)

	ok(report(t, o, calDev, "", `{"level":"error","message":"dev broken"}`), "dev's report")
	ok(report(t, o, calDev, "", `{"level":"info","message":"hello","transient":true}`), "dev's notice")
	check("dev's report and notice", w.settle("dev"),
		`deployments apps/calendar {"deployment":"dev","level":"error","message":"dev broken","op":"status","transient":false}`,
		`deployments apps/calendar {"deployment":"dev","level":"info","message":"hello","op":"status","transient":true}`)
	if got, want := listed(t, o), `{"statuses":{"apps/calendar":{"level":"error","message":"main broken"}}}`; got != want {
		t.Fatalf("GET /tile-report: %s, want %s", got, want)
	}
	if got := o.stored(depKey("apps/calendar", "dev")); got != "error:dev broken" {
		t.Fatalf("dev's stored status: %s", got)
	}

	ok(report(t, o, calDev, "", `{"level":"ok"}`), "dev's clear")
	check("dev's clear", w.settle("dev's clear"),
		`deployments apps/calendar {"deployment":"dev","level":"ok","message":"","op":"status","transient":false}`)
	if o.stored("apps/calendar") != "error:main broken" || o.stored(depKey("apps/calendar", "dev")) != "-" {
		t.Fatalf("after dev's clear: main %s, dev %s", o.stored("apps/calendar"), o.stored(depKey("apps/calendar", "dev")))
	}

	// a non-primary build clears dev's alone; the primary's build-start main's alone
	report(t, o, calDev, "", `{"level":"warn","message":"dev again"}`)
	w.settle("dev again")
	o.Hub.Publish(events.Event{Type: "deployments", Component: "apps/calendar",
		Data: map[string]any{"op": "build", "deployment": "dev", "phase": "start"}})
	check("dev's build", w.settle("dev's build"),
		`deployments apps/calendar {"deployment":"dev","level":"ok","message":"","op":"status","transient":false}`)
	if o.stored("apps/calendar") != "error:main broken" {
		t.Fatalf("dev's build touched main's status: %s", o.stored("apps/calendar"))
	}
	report(t, o, calDev, "", `{"level":"warn","message":"dev third"}`)
	w.settle("dev third")
	o.Hub.Publish(events.Event{Type: "build-start", Component: "apps/calendar"})
	check("main's build-start", w.settle("main's build"), `status apps/calendar {"level":"ok","message":""}`)
	if o.stored(depKey("apps/calendar", "dev")) != "warn:dev third" {
		t.Fatalf("main's build-start touched dev's status: %s", o.stored(depKey("apps/calendar", "dev")))
	}
	// a deploy onto dev clears dev's at its swap, not before
	deploy := func(phase, result string) events.Event {
		return events.Event{Type: "deployments", Component: "apps/calendar", Data: map[string]any{"op": "deploy",
			"deployment": "dev", "checkpoint": "c:1111111", "result": result, "phase": phase, "id": 7, "how": "deploy"}}
	}
	o.Hub.Publish(deploy("build", "running"))
	check("dev's deploy before its swap", w.settle("dev's deploy"))
	o.Hub.Publish(deploy("swap", "running"))
	o.Hub.Publish(deploy("swap", "ok"))
	check("dev's swap", w.settle("dev's swap"),
		`deployments apps/calendar {"deployment":"dev","level":"ok","message":"","op":"status","transient":false}`)

	// apps/email's primary is dev: its reports are today's; main beside it is non-primary
	ok(report(t, o, emailDev, "", `{"level":"warn","message":"slow"}`), "email dev's report")
	ok(report(t, o, emailMn, "", `{"level":"error","message":"old"}`), "email main's report")
	ok(report(t, o, ownerP, "?component=apps/email", `{"level":"warn","message":"by the owner"}`), "the owner's report")
	check("apps/email", w.settle("apps/email"),
		`status apps/email {"level":"warn","message":"slow"}`,
		`deployments apps/email {"deployment":"main","level":"error","message":"old","op":"status","transient":false}`,
		`status apps/email {"level":"warn","message":"by the owner"}`)

	// a reassignment exchanges the statuses
	report(t, o, calMain, "", `{"level":"error","message":"M"}`)
	report(t, o, calDev, "", `{"level":"warn","message":"D"}`)
	w.settle("before the reassignment")
	o.setPrimary("apps/calendar", "dev")
	o.Hub.Publish(events.Event{Type: "deployments", Component: "apps/calendar",
		Data: map[string]any{"op": "record", "what": []string{"primary"}}})
	check("the reassignment", w.settle("reassignment"),
		`deployments apps/calendar {"deployment":"main","level":"error","message":"M","op":"status","transient":false}`,
		`status apps/calendar {"level":"warn","message":"D"}`)
	if o.stored("apps/calendar") != "warn:D" || o.stored(depKey("apps/calendar", "main")) != "error:M" {
		t.Fatalf("after the reassignment: bare %s, main %s", o.stored("apps/calendar"), o.stored(depKey("apps/calendar", "main")))
	}
	o.Hub.Publish(events.Event{Type: "deployments", Component: "apps/calendar",
		Data: map[string]any{"op": "record", "what": []string{"liveReload"}}})
	check("a record change that moved nothing", w.settle("record"))

	// a removed deployment's status goes with it
	report(t, o, auth.Principal{Component: "apps/calendar", Via: "instance", Deployment: "evil"}, "", `{"level":"warn","message":"E"}`)
	w.settle("evil's report")
	o.mu.Lock()
	o.deps["apps/calendar"] = []string{"main", "dev"}
	o.mu.Unlock()
	o.Hub.Publish(events.Event{Type: "deployments", Component: "apps/calendar",
		Data: map[string]any{"op": "record", "what": []string{"deployments"}}})
	check("a removal", w.settle("removal"))
	if o.stored(depKey("apps/calendar", "evil")) != "-" || o.stored(depKey("apps/calendar", "main")) != "error:M" {
		t.Fatalf("after evil's removal: evil %s, main %s", o.stored(depKey("apps/calendar", "evil")), o.stored(depKey("apps/calendar", "main")))
	}

	// gone and refused credentials
	if rec := report(t, o, calGone, "", `{"level":"warn","message":"x"}`); rec.Code != 404 {
		t.Fatalf("a gone deployment's report: %d %s", rec.Code, rec.Body)
	}
	o.mu.Lock()
	o.protected["apps/calendar"] = true
	o.mu.Unlock()
	if rec := report(t, o, calTerm, "", `{"level":"warn","message":"x"}`); rec.Code != 403 || !strings.Contains(rec.Body.String(), "protected") {
		t.Fatalf("a session following a protected primary: %d %s", rec.Code, rec.Body)
	}
	check("the refusals", w.settle("refusals"))
}

// covers D119c PO-5 — the zero state: a plane without the deployment input
// answers as today (TestTileStatus); with the input, a tile it holds no
// record for reports today's status event and nothing else.
func TestStatusZeroStateUnchanged(t *testing.T) {
	o := newDepRig(t)
	w := watchStatus(t, o)
	p := auth.Principal{Component: "apps/plain", Via: "instance"}
	if rec := report(t, o, p, "", `{"level":"warn","message":"x"}`); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if got := w.settle("zero state"); len(got) != 1 || got[0] != `status apps/plain {"level":"warn","message":"x"}` {
		t.Fatalf("zero state published %v", got)
	}
}
