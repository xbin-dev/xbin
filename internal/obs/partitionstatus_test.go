package obs

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
)

// covers S12 C10 — a person's partition's tile report (plans/partitions/02
// §8-§9): stored per partition, never the tile card's status, its event
// naming the partition; GET answers the partition its own record (none: no
// entry) and everyone else the tile's; a build-start (the watcher's
// clearPartitionStatuses) clears it, telling the partition.
func TestPartitionTileStatus(t *testing.T) {
	o := testPlane(t) // apps/calendar, apps/email
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
	o.clearPartitionStatuses("apps/calendar") // what the watcher does on a build-start
	if e := next(); e.Partition != "user:ana" {
		t.Errorf("the clear's event: %+v", e)
	}
	if _, ok := list(ana)["apps/calendar"]; ok {
		t.Error("a build-start left ana's status")
	}
	if s := list(auth.Principal{Owner: true})["apps/calendar"]; s.Message != "global" {
		t.Errorf("clearing the partitions cleared global's: %+v", s)
	}
}
