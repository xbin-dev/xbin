package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

type busCall struct {
	p          auth.Principal
	comp, path string
	d          busDelivery
}

// fakeBusDispatch records deliveries; block, when set, holds each one until
// it is closed.
func fakeBusDispatch(b *Broker, block chan struct{}) chan busCall {
	calls := make(chan busCall, 1024)
	b.SetBusDispatch(func(_ context.Context, p auth.Principal, comp, path string, body []byte) (int, string) {
		var d busDelivery
		_ = json.Unmarshal(body, &d)
		calls <- busCall{p, comp, path, d}
		if block != nil {
			<-block
		}
		return 200, "ok"
	})
	return calls
}

func busAPI(t *testing.T, h http.HandlerFunc, p auth.Principal, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		bts, _ := json.Marshal(body)
		rd = bytes.NewReader(bts)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, rd)
	if i := strings.LastIndex(target, "/subscriptions/"); i >= 0 {
		name, _, _ := strings.Cut(target[i+len("/subscriptions/"):], "?")
		req.SetPathValue("name", name)
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func grantBusReader(t *testing.T, b *Broker, from string) {
	t.Helper()
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: from, Target: "res:apps/calendar/bus", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
}

func recv(t *testing.T, calls chan busCall) busCall {
	t.Helper()
	select {
	case c := <-calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	return busCall{}
}

func quiet(t *testing.T, calls chan busCall) {
	t.Helper()
	select {
	case c := <-calls:
		t.Fatalf("unexpected delivery: %+v", c)
	case <-time.After(100 * time.Millisecond):
	}
}

var (
	asEmail    = auth.Principal{Component: "apps/email"}
	asCalendar = auth.Principal{Component: "apps/calendar"}
)

// Subscribing needs `reader` on the bus (the 403 names the uses entry); a
// component subscribes only itself; the list is your own unless you're admin.
func TestBusSubsRegister(t *testing.T) {
	b := testBroker(t)
	sub := map[string]any{"name": "cal", "resource": "res:apps/calendar/bus", "prefix": "events/", "path": "/bus", "component": "apps/calendar"}
	rec := busAPI(t, b.apiBusSubsPut, asEmail, "PUT", "/bus/subscriptions", sub)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "uses") {
		t.Fatalf("ungranted: %d %s", rec.Code, rec.Body)
	}
	grantBusReader(t, b, "apps/email")
	if rec := busAPI(t, b.apiBusSubsPut, asEmail, "PUT", "/bus/subscriptions", sub); rec.Code != 200 {
		t.Fatalf("granted: %d %s", rec.Code, rec.Body)
	}
	got := b.bus.forComponent("apps/email")
	if len(got) != 1 || got[0].Component != "apps/email" || got[0].Role != "writer" {
		t.Fatalf("a component subscribes itself, role defaults to writer: %+v", got)
	}
	if len(b.bus.forComponent("apps/calendar")) != 0 {
		t.Fatal("an element named another component")
	}
	for _, bad := range []map[string]any{
		{"name": "x", "resource": "res:apps/calendar/events", "path": "/bus"}, // kv, not bus
		{"name": "a/b", "resource": "res:apps/calendar/bus", "path": "/bus"},
		{"name": "x", "resource": "res:apps/calendar/bus", "path": "bus"},
	} {
		if rec := busAPI(t, b.apiBusSubsPut, asEmail, "PUT", "/bus/subscriptions", bad); rec.Code < 400 {
			t.Fatalf("accepted %v", bad)
		}
	}
	// an admin registering for a component doesn't lend it their access
	admin := auth.Principal{Owner: true}
	if rec := busAPI(t, b.apiBusSubsPut, admin, "PUT", "/bus/subscriptions",
		map[string]any{"name": "x", "resource": "res:apps/calendar/bus", "path": "/bus", "component": "apps/other"}); rec.Code != 404 {
		t.Fatalf("unknown component: %d", rec.Code)
	}
	var list struct {
		Subscriptions []busSubView `json:"subscriptions"`
	}
	_ = json.Unmarshal(busAPI(t, b.apiBusSubsList, asCalendar, "GET", "/bus/subscriptions", nil).Body.Bytes(), &list)
	if len(list.Subscriptions) != 0 {
		t.Fatalf("calendar sees email's subscription: %+v", list)
	}
	_ = json.Unmarshal(busAPI(t, b.apiBusSubsList, admin, "GET", "/bus/subscriptions", nil).Body.Bytes(), &list)
	if len(list.Subscriptions) != 1 {
		t.Fatalf("admin sees all: %+v", list)
	}
	if rec := busAPI(t, b.apiBusSubsDelete, asCalendar, "DELETE", "/bus/subscriptions/cal", nil); rec.Code != 404 {
		t.Fatalf("calendar deleted email's subscription: %d", rec.Code)
	}
	if rec := busAPI(t, b.apiBusSubsDelete, asEmail, "DELETE", "/bus/subscriptions/cal", nil); rec.Code != 200 {
		t.Fatalf("delete own: %d", rec.Code)
	}
}

// A publish reaches the subscriber through the dispatch as xbin/bus with its
// role; the prefix filters; a revoked grant or a disabled component stops
// delivery; a vanished component's subscription is pruned.
func TestBusSubsDelivery(t *testing.T) {
	b := testBroker(t)
	calls := fakeBusDispatch(b, nil)
	grantBusReader(t, b, "apps/email")
	if err := b.bus.put(busSub{Name: "cal", Resource: "res:apps/calendar/bus", Prefix: "events/", Component: "apps/email", Path: "/bus", Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	publish := func(topic string) {
		t.Helper()
		rec := busAPI(t, b.apiBusPublish, asCalendar, "POST", "/bus/publish", map[string]any{"resource": "res:apps/calendar/bus", "topic": topic, "data": map[string]int{"n": 1}})
		if rec.Code != 200 {
			t.Fatalf("publish: %d %s", rec.Code, rec.Body)
		}
	}
	publish("events/created")
	c := recv(t, calls)
	if c.p.Component != BusPrincipal || c.p.Role != "reader" || c.comp != "apps/email" || c.path != "/bus" {
		t.Fatalf("delivery: %+v", c)
	}
	if c.d.ID == "" || c.d.Subscription != "cal" || c.d.Resource != "res:apps/calendar/bus" || c.d.Topic != "events/created" || c.d.TS == 0 {
		t.Fatalf("body: %+v", c.d)
	}
	if role, ok := b.Policy(c.p, mustComp(t, b, "apps/email")); !ok || role != "reader" {
		t.Fatalf("policy for xbin/bus: %q %v", role, ok)
	}
	publish("other/thing")
	quiet(t, calls)

	// disabled: dropped, not delivered
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Lifecycle == nil {
			ws.Lifecycle = map[string]string{}
		}
		ws.Lifecycle["apps/email"] = registry.StateDisabled
	})
	publish("events/x")
	quiet(t, calls)
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { delete(ws.Lifecycle, "apps/email") })

	// revoked: failed, with the reason
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { ws.Grants = nil })
	publish("events/y")
	quiet(t, calls)
	b.bus.mu.Lock()
	st := *b.bus.subs[subKey("apps/email", "cal")]
	b.bus.mu.Unlock()
	if st.stats.Delivered != 1 || st.stats.Dropped != 1 || st.stats.Failed != 1 || !strings.Contains(st.stats.LastError, "reader") {
		t.Fatalf("stats: %+v", st.stats)
	}

	// a component that no longer exists: pruned, and the file forgets it
	_ = b.bus.put(busSub{Name: "ghost", Resource: "res:apps/calendar/bus", Component: "apps/gone", Path: "/bus", Role: "writer"})
	b.bus.persist()
	publish("events/z")
	waitUntil(t, func() bool { return len(b.bus.forComponent("apps/gone")) == 0 })
	bts, _ := os.ReadFile(filepath.Join(b.Reg.Root, "data", "bus-subscriptions.json"))
	if strings.Contains(string(bts), "apps/gone") {
		t.Fatalf("pruned subscription persisted: %s", bts)
	}
}

// One delivery in flight; past busSubPerSec in a second the rest are dropped
// (the loop guard); a full queue drops the newest. Order is kept.
func TestBusSubsQueue(t *testing.T) {
	b := testBroker(t)
	block := make(chan struct{})
	calls := fakeBusDispatch(b, block)
	grantBusReader(t, b, "apps/email")
	_ = b.bus.put(busSub{Name: "cal", Resource: "res:apps/calendar/bus", Component: "apps/email", Path: "/bus", Role: "writer"})
	st := b.bus.subs[subKey("apps/email", "cal")]
	for i := 0; i < busSubPerSec+50; i++ {
		b.bus.publish("res:apps/calendar/bus", "t", i)
	}
	first := recv(t, calls) // in flight, blocked
	b.bus.mu.Lock()
	queued, dropped := len(st.queue), st.stats.Dropped
	b.bus.mu.Unlock()
	if queued != busSubPerSec-1 || dropped != 50 {
		t.Fatalf("loop guard: %d queued, %d dropped", queued, dropped)
	}
	// fill past the queue across fresh windows
	qlen := func() int { b.bus.mu.Lock(); defer b.bus.mu.Unlock(); return len(st.queue) }
	for qlen() < busSubQueue {
		b.bus.mu.Lock()
		st.win = time.Time{}
		b.bus.mu.Unlock()
		for i := 0; i < 50; i++ {
			b.bus.publish("res:apps/calendar/bus", "t", 1000)
		}
	}
	b.bus.mu.Lock()
	if len(st.queue) != busSubQueue {
		t.Fatalf("queue grew past its cap: %d", len(st.queue))
	}
	b.bus.mu.Unlock()
	close(block)
	if first.d.Data.(float64) != 0 {
		t.Fatalf("first delivered: %v", first.d.Data)
	}
	for i := 1; i < busSubPerSec; i++ {
		if c := recv(t, calls); c.d.Data.(float64) != float64(i) {
			t.Fatalf("out of order: got %v want %d", c.d.Data, i)
		}
	}
	waitUntil(t, func() bool { b.bus.mu.Lock(); defer b.bus.mu.Unlock(); return !st.busy })
}

// Subscriptions survive a restart; a new tile at a removed tile's path starts
// without its cron jobs and subscriptions.
func TestBusSubsPersistAndForget(t *testing.T) {
	b := testBroker(t)
	grantBusReader(t, b, "apps/email")
	_ = b.bus.put(busSub{Name: "cal", Resource: "res:apps/calendar/bus", Component: "apps/email", Path: "/bus", Role: "writer"})
	b.bus.persist()
	if err := b.cron.add(cronJob{Name: "sweep", Resource: "res:apps/calendar/cron", Schedule: "@hourly", Component: "apps/email", Path: "/sweep"}); err != nil {
		t.Fatal(err)
	}
	b.cron.persist()
	b.Close()
	b2, err := New(b.Reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if got := b2.bus.forComponent("apps/email"); len(got) != 1 || got[0].Path != "/bus" {
		t.Fatalf("after restart: %+v", got)
	}
	b2.assignOwner("apps/email", "")
	if len(b2.bus.forComponent("apps/email")) != 0 || len(b2.cronJobsFor("apps/email")) != 0 {
		t.Fatal("a new tile inherited the path's registrations")
	}
}

func mustComp(t *testing.T, b *Broker, path string) *registry.Component {
	t.Helper()
	c, ok := b.Reg.Component(path)
	if !ok {
		t.Fatalf("no component %s", path)
	}
	return c
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
