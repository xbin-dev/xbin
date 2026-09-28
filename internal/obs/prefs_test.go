package obs

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
)

func prefsReq(t *testing.T, o *Plane, p auth.Principal, method, key, body, writer string) int {
	t.Helper()
	r := httptest.NewRequest(method, "/prefs/"+key, strings.NewReader(body))
	r.SetPathValue("key", key)
	if writer != "" {
		r.Header.Set("X-Prefs-Writer", writer)
	}
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	switch method {
	case "PUT":
		o.apiPrefsPut(w, r)
	case "DELETE":
		o.apiPrefsDelete(w, r)
	}
	return w.Code
}

// Concurrent writes of DIFFERENT keys to one bucket are a read-modify-write
// of one file each: without the per-bucket lock they lose each other's
// update. Every key must survive.
func TestPrefsConcurrentKeysSurvive(t *testing.T) {
	o := testPlane(t)
	p := auth.Principal{UserID: "alice"}
	const n = 64
	// a key to delete concurrently with the writes
	if c := prefsReq(t, o, p, "PUT", "doomed", `1`, ""); c != 200 {
		t.Fatalf("seed: %d", c)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if c := prefsReq(t, o, p, "PUT", fmt.Sprintf("k%d", i), fmt.Sprintf(`{"i":%d}`, i), ""); c != 200 {
				t.Errorf("put k%d: %d", i, c)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if c := prefsReq(t, o, p, "DELETE", "doomed", "", ""); c != 200 {
			t.Errorf("delete: %d", c)
		}
	}()
	wg.Wait()
	m, err := o.prefsRead(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		var v struct{ I int }
		if err := json.Unmarshal(m[fmt.Sprintf("k%d", i)], &v); err != nil || v.I != i {
			t.Fatalf("k%d lost or wrong: %s (%v)", i, m[fmt.Sprintf("k%d", i)], err)
		}
	}
	if _, ok := m["doomed"]; ok {
		t.Fatal("the concurrent delete was lost")
	}
	if len(m) != n {
		t.Fatalf("want %d keys, got %d", n, len(m))
	}
}

// A successful write publishes `prefs` {component, data:{key, writer}},
// visible only to the bucket owner's own clients: every human session of
// the user, an element only for its own bucket, never another user (not
// even an admin).
func TestPrefsEventScoped(t *testing.T) {
	o := testPlane(t)
	visible := func(p auth.Principal) events.Filter { // what server.handleEventsWS applies
		return func(e events.Event) bool {
			v, ok := e.Data.(interface{ VisibleTo(auth.Principal) bool })
			return e.Type == "prefs" && ok && v.VisibleTo(p)
		}
	}
	type sub struct {
		name string
		p    auth.Principal
		want bool
	}
	subs := []sub{
		{"alice browser", auth.Principal{UserID: "alice", Via: "session"}, true},
		{"alice app", auth.Principal{UserID: "alice", Via: "device"}, true},
		{"alice in calendar", auth.Principal{UserID: "alice", Component: "apps/calendar", Via: "frame"}, false},
		{"bob", auth.Principal{UserID: "bob", Via: "session"}, false},
		{"admin root token", auth.Principal{Owner: true}, false},
		{"admin user", auth.Principal{UserID: "adm", Via: "session", User: nil}, false},
	}
	chans := make([]<-chan events.Event, len(subs))
	for i, s := range subs {
		ch, cancel := o.Hub.Subscribe(visible(s.p))
		defer cancel()
		chans[i] = ch
	}
	if c := prefsReq(t, o, auth.Principal{UserID: "alice", Via: "session"}, "PUT", "layout", `{"screens":[]}`, "tab-1"); c != 200 {
		t.Fatalf("put: %d", c)
	}
	for i, s := range subs {
		select {
		case e := <-chans[i]:
			if !s.want {
				t.Fatalf("%s saw alice's prefs event", s.name)
			}
			bts, _ := json.Marshal(e)
			if string(bts) != `{"type":"prefs","component":"root","data":{"key":"layout","writer":"tab-1"}}` {
				t.Fatalf("%s: wire %s", s.name, bts)
			}
		case <-time.After(50 * time.Millisecond):
			if s.want {
				t.Fatalf("%s missed the prefs event", s.name)
			}
		}
	}

	// A tile's own bucket: the tile's frame principal sees it, another
	// tile of the same user doesn't; the user's browser does.
	cal, cancelCal := o.Hub.Subscribe(visible(auth.Principal{UserID: "alice", Component: "apps/calendar"}))
	defer cancelCal()
	mail, cancelMail := o.Hub.Subscribe(visible(auth.Principal{UserID: "alice", Component: "apps/email"}))
	defer cancelMail()
	if c := prefsReq(t, o, auth.Principal{UserID: "alice", Component: "apps/calendar"}, "DELETE", "view", "", ""); c != 200 {
		t.Fatalf("delete: %d", c)
	}
	select {
	case e := <-cal:
		if e.Component != "apps/calendar" || e.Data.(prefsChange).Key != "view" {
			t.Fatalf("calendar event: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("calendar missed its own prefs event")
	}
	select {
	case e := <-mail:
		t.Fatalf("email saw calendar's prefs event: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case e := <-chans[0]:
		if e.Component != "apps/calendar" {
			t.Fatalf("alice browser: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("alice's browser missed her calendar bucket's event")
	}
}
