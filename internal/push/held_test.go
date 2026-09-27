package push

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// heldRig mounts POST /notify through NotifyHandler, with a Holder
// answering as the deployments plane does: apps/cal has main (the primary)
// and dev; apps/other has main and dev (the primary).
func heldRig(t *testing.T, mod func(*Options)) (*rig, *Holder, <-chan events.Event) {
	t.Helper()
	r := newRig(t, mod)
	hub := events.NewHub()
	seen, stop := hub.Subscribe(func(e events.Event) bool { return e.Type == "deployments" })
	t.Cleanup(stop)
	primary := map[string]string{"apps/cal": "main", "apps/other": "dev"}
	h := &Holder{Hub: hub,
		Now:     func() time.Time { return time.Date(2026, 9, 27, 10, 0, 0, 0, time.FixedZone("x", 3600)) },
		Primary: func(tile string) string { return primary[tile] },
		Addressed: func(p auth.Principal, tile string) (string, error) {
			switch {
			case p.Component != tile:
				return primary[tile], nil
			case p.Deployment == "" && p.Via != "terminal":
				return util.MainDeployment, nil
			case p.Deployment == "":
				return primary[tile], nil
			case p.Deployment != "dev" && p.Deployment != util.MainDeployment:
				return "", util.NoDeployment(tile, p.Deployment)
			}
			return p.Deployment, nil
		}}
	r.mux.HandleFunc("POST /held/notify", r.s.NotifyHandler(h))
	return r, h, seen
}

// notifyHeld posts a notification through the holder's route.
func notifyHeld(r *rig, p auth.Principal, body map[string]any) (int, string) {
	r.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/held/notify", bytes.NewReader(b))
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// covers P13 T6 SC-DORMANT — notifications from a non-primary deployment
// are never pushed (TestNonPrimaryNotifyNeverPushed): after today's
// validation they answer 202 with suppressed:true, reach no device and
// spend none of the tile's budget, whoever they name; each is kept as
// "would notify" in a list per (tile, deployment), newest first, the last 20,
// and announced in the deployments event, op notify; the primary's are
// pushed as today, including a primary that isn't main, and main beside it
// is held; a frontend still notifies only its own person; a credential of a
// gone deployment gets 404; without a holder the route is today's.
func TestNotifyNotPushed(t *testing.T) {
	r, h, seen := heldRig(t, func(o *Options) {
		o.Limits = Limits{Tile: Rate{PerHour: 3600, Burst: 2}, User: Rate{PerHour: 3600, Burst: 10}}
	})
	r.register(alice, "phone", "handle-alice")
	r.register(bob, "phone-b", "handle-bob")
	calDev := auth.Principal{Component: "apps/cal", Via: "instance", Deployment: "dev"}

	// more held notifications than the tile's burst: all 202, none pushed or charged
	for i := 0; i < 5; i++ {
		code, body := notifyHeld(r, calDev, map[string]any{"user": "alice", "title": fmt.Sprintf("dev %d", i)})
		if code != 202 || body != `{"ok":true,"suppressed":true}` {
			t.Fatalf("dev's notification %d: %d %s", i, code, body)
		}
	}
	// bob can't read apps/cal: held all the same, and no one is asked
	if code, body := notifyHeld(r, calDev, map[string]any{"user": "bob", "title": "to bob"}); code != 202 || !strings.Contains(body, "suppressed") {
		t.Fatalf("dev to a non-reader: %d %s", code, body)
	}
	// the owner is "owner", not "user:owner"
	if code, _ := notifyHeld(r, calDev, map[string]any{"user": OwnerUser, "title": "to the owner"}); code != 202 {
		t.Fatal(code)
	}
	// main, the primary: pushed, with the tile's whole budget
	for i := 0; i < 2; i++ {
		if code, body := notifyHeld(r, cal, map[string]any{"user": "alice", "title": "main"}); code != 202 || body != `{"ok":true}` {
			t.Fatalf("main's notification %d: %d %s", i, code, body)
		}
	}
	if code, _ := notifyHeld(r, cal, map[string]any{"user": "alice", "title": "main"}); code != 429 {
		t.Fatalf("main over the tile's burst: %d (held notifications were charged?)", code)
	}
	// apps/other's primary is dev: dev pushes, main beside it is held
	otherDev := auth.Principal{Component: "apps/other", Via: "instance", Deployment: "dev"}
	if code, body := notifyHeld(r, otherDev, map[string]any{"user": "bob", "title": "primary dev"}); code != 202 || body != `{"ok":true}` {
		t.Fatalf("a dev primary: %d %s", code, body)
	}
	if code, body := notifyHeld(r, other, map[string]any{"user": "bob", "title": "main beside"}); code != 202 || !strings.Contains(body, "suppressed") {
		t.Fatalf("main beside a dev primary: %d %s", code, body)
	}
	r.relay.waitPushes(3)
	time.Sleep(30 * time.Millisecond)
	got := r.relay.pushes()
	if len(got) != 3 {
		t.Fatalf("%d pushes, want main's two and apps/other dev's one", len(got))
	}
	for _, p := range got {
		if title := r.open(p).Title; title != "main" && title != "primary dev" {
			t.Errorf("a held notification was pushed: %q", title)
		}
	}

	// the would-notify lists and the events
	list := h.WouldNotify("apps/cal", "dev")
	if len(list) != 7 || list[0] != (WouldNotify{At: "2026-09-27T09:00:00Z", To: "owner", Title: "to the owner"}) ||
		list[1].To != "user:bob" || list[6].Title != "dev 0" {
		t.Errorf("apps/cal dev's list: %+v", list)
	}
	if l := h.WouldNotify("apps/other", "main"); len(l) != 1 || l[0].Title != "main beside" {
		t.Errorf("apps/other main's list: %+v", l)
	}
	if l := h.WouldNotify("apps/cal", "main"); len(l) != 0 {
		t.Errorf("the primary's list: %+v", l)
	}
	var evs []string
	for len(evs) < 8 {
		select {
		case e := <-seen:
			b, _ := json.Marshal(e)
			evs = append(evs, string(b))
		case <-time.After(time.Second):
			t.Fatalf("events: %d, want 8:\n%s", len(evs), strings.Join(evs, "\n"))
		}
	}
	if want := `{"type":"deployments","component":"apps/cal","data":{"at":"2026-09-27T09:00:00Z","deployment":"dev","op":"notify","title":"dev 0","to":"user:alice"}}`; evs[0] != want {
		t.Errorf("the first event:\n%s\nwant\n%s", evs[0], want)
	}
	if !strings.Contains(evs[7], `"component":"apps/other"`) || !strings.Contains(evs[7], `"deployment":"main"`) {
		t.Errorf("apps/other main's event: %s", evs[7])
	}

	// bounded: the last 20
	for i := 0; i < 30; i++ {
		notifyHeld(r, calDev, map[string]any{"user": "alice", "title": fmt.Sprintf("n%d", i)})
	}
	if l := h.WouldNotify("apps/cal", "dev"); len(l) != wouldNotifyMax || l[0].Title != "n29" || l[19].Title != "n10" {
		t.Errorf("the bounded list: %d, first %q, last %q", len(l), l[0].Title, l[len(l)-1].Title)
	}
	h.Drop("apps/cal", "dev")
	if l := h.WouldNotify("apps/cal", "dev"); len(l) != 0 {
		t.Errorf("after Drop: %+v", l)
	}

	// today's validation first; a gone deployment
	devFrame := auth.Principal{Component: "apps/cal", Via: "frame", UserID: "alice", Deployment: "dev"}
	if code, _ := notifyHeld(r, devFrame, map[string]any{"user": "bob", "title": "x"}); code != 403 {
		t.Errorf("dev's frontend notifying another person: %d", code)
	}
	if code, _ := notifyHeld(r, calDev, map[string]any{"user": "alice", "title": ""}); code != 400 {
		t.Errorf("dev without a title: %d", code)
	}
	gone := auth.Principal{Component: "apps/cal", Via: "instance", Deployment: "gone"}
	if code, body := notifyHeld(r, gone, map[string]any{"user": "alice", "title": "x"}); code != 404 || !strings.Contains(body, `no deployment \"gone\"`) {
		t.Errorf("a gone deployment: %d %s", code, body)
	}
	// without a holder: today's route, which never holds
	otherMain := auth.Principal{Component: "apps/other", Via: "instance"}
	if code, out, _ := r.call(otherMain, "POST", "/notify", map[string]any{"user": "bob", "title": "old route"}); code != 202 || out["suppressed"] != nil {
		t.Errorf("APINotify: %d %v", code, out)
	}
}
