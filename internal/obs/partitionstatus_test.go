package obs

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers S12 C10 PD-43 — a person's partition's tile report
// (plans/partitions/02 §8-§9): stored per partition, keyed by the
// partition's id, never the tile card's status, its event naming the
// partition; GET answers the partition its own record (none: no entry) and
// everyone else the tile's; a recreated person (a new id for the same
// key) reads nothing of the old one's; a partition's own restart clears its
// record alone, telling it; a build-start of the tile clears every
// partition's; and a partition whose id can't be told can't report (403).
func TestPartitionTileStatus(t *testing.T) {
	o := testPlane(t) // apps/calendar, apps/email
	var mu sync.Mutex
	incarnation := map[util.Partition]string{"user:ana": "u-ana1", "user:bob": "u-bob1"}
	o.PartitionID = func(part util.Partition) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if id, ok := incarnation[part]; ok {
			return id, nil
		}
		return "", errors.New(string(part) + "'s partition identity can't be recorded")
	}
	ch, cancel := o.Hub.Subscribe(func(e events.Event) bool { return e.Type == "status" })
	defer cancel()
	ana := auth.Principal{Component: "apps/calendar", Via: "instance", Partition: "user:ana"}
	bob := auth.Principal{Component: "apps/calendar", UserID: "bob", Via: "frame", Partition: "user:bob"}
	global := auth.Principal{Component: "apps/calendar", Via: "instance"}
	set := func(p auth.Principal, body string) int {
		r := httptest.NewRequest("POST", "/tile-report", strings.NewReader(body))
		w := httptest.NewRecorder()
		o.apiStatusSet(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		return w.Code
	}
	list := func(p auth.Principal) map[string]statusRec {
		r := httptest.NewRequest("GET", "/tile-report", nil)
		w := httptest.NewRecorder()
		o.apiStatusList(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		var out struct{ Statuses map[string]statusRec }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.Statuses
	}
	next := func() events.Event {
		t.Helper()
		select {
		case e := <-ch:
			return e
		case <-time.After(2 * time.Second):
			t.Fatal("no status event")
		}
		return events.Event{}
	}
	if set(global, `{"level":"warn","message":"global"}`) != 200 || next().Partition != "" {
		t.Fatal("global's report")
	}
	if set(ana, `{"level":"error","message":"ana's"}`) != 200 {
		t.Fatal("ana's report")
	}
	if e := next(); e.Partition != "user:ana" || e.Component != "apps/calendar" {
		t.Errorf("ana's status event: %+v", e)
	}
	if s := list(auth.Principal{Owner: true})["apps/calendar"]; s.Message != "global" {
		t.Errorf("the tile card's status became a partition's: %+v", s)
	}
	if s := list(ana)["apps/calendar"]; s.Message != "ana's" {
		t.Errorf("ana's partition reads %+v, want its own", s)
	}
	if _, ok := list(bob)["apps/calendar"]; ok {
		t.Error("bob's partition reads another's (or global's) status of its tile")
	}
	mu.Lock()
	incarnation["user:ana"] = "u-ana2" // ana deleted and created again
	mu.Unlock()
	if s, ok := list(ana)["apps/calendar"]; ok {
		t.Errorf("a recreated ana reads the old one's status: %+v", s)
	}
	mu.Lock()
	incarnation["user:ana"] = "u-ana1"
	mu.Unlock()
	if code := set(auth.Principal{Component: "apps/calendar", Via: "instance", Partition: "user:eve"}, `{"level":"warn","message":"x"}`); code != 403 {
		t.Errorf("a partition without an id reported: %d", code)
	}

	// a partition's own restart clears its record alone
	if set(bob, `{"level":"warn","message":"bob's"}`) != 200 || next().Partition != "user:bob" {
		t.Fatal("bob's report")
	}
	go o.watchStatusRestarts()
	// until the watcher has subscribed: a restart that finds nothing to
	// clear publishes nothing, so a repeat is harmless
	var cleared events.Event
	for deadline := time.Now().Add(2 * time.Second); cleared.Type == "" && time.Now().Before(deadline); {
		o.Hub.Publish(events.Event{Type: "partitions", Component: "apps/calendar", Partition: "user:ana",
			Data: map[string]any{"op": "state", "partition": "user:ana", "event": "build-start"}})
		select {
		case cleared = <-ch:
		case <-time.After(20 * time.Millisecond):
		}
	}
	if cleared.Partition != "user:ana" {
		t.Errorf("the clear's event: %+v", cleared)
	}
	if _, ok := list(ana)["apps/calendar"]; ok {
		t.Error("ana's restart left her status")
	}
	if s := list(bob)["apps/calendar"]; s.Message != "bob's" {
		t.Errorf("ana's restart cleared bob's: %+v", s)
	}
	if s := list(auth.Principal{Owner: true})["apps/calendar"]; s.Message != "global" {
		t.Errorf("ana's restart cleared the tile card's: %+v", s)
	}
	// the tile's code restarts every partition
	o.Hub.Publish(events.Event{Type: "build-start", Component: "apps/calendar"})
	if e := next(); e.Partition != "user:bob" {
		t.Errorf("the build-start's clear: %+v", e)
	}
	if _, ok := list(bob)["apps/calendar"]; ok {
		t.Error("a build-start left bob's status")
	}
}
