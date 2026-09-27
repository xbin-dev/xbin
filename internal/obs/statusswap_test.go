package obs

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
)

// covers P13 P9 — the primary's stored status clears at the swap of a deploy
// that puts a checkpoint on it, never at its start, since such a deploy emits
// no build-start (11-contract §3.1, 07-runtime §8.1): the deploy's earlier
// phases and a failed deploy leave it, so the old generation keeps serving
// with its status; the swap clears it once and publishes today's bare clear,
// and the same swap's later events (its ok result, its reader form) leave a
// status the new generation reported since; a non-primary deployment's swap
// never touches it; a restart through build-start clears it as today, and
// the swap that build leads to (a resume's) clears nothing more. Built on
// testPlane.
func TestStatusClearsAtPrimarySwap(t *testing.T) {
	o := testPlane(t) // apps/calendar, apps/email
	go o.watchStatusRestarts()
	seen, stop := o.Hub.Subscribe(func(e events.Event) bool { return e.Type == "status" })
	defer stop()

	// barrier publishes a build-start on a fresh probe tile that holds a
	// status and returns the other status events published before the
	// watcher cleared the probe: it handles events in order, so everything
	// published before the probe has been handled by then.
	probes := 0
	barrier := func(wait time.Duration) ([]string, bool) {
		probes++
		name := fmt.Sprintf("probe-%d", probes)
		o.statusMu.Lock()
		o.statuses[name] = statusRec{Level: "warn", Message: "probe"}
		o.statusMu.Unlock()
		o.Hub.Publish(events.Event{Type: "build-start", Component: name})
		var out []string
		timeout := time.After(wait)
		for {
			select {
			case e := <-seen:
				switch {
				case e.Component == name:
					return out, true
				case strings.HasPrefix(e.Component, "probe-"): // an earlier probe's
				default:
					b, _ := json.Marshal(e)
					out = append(out, strings.Split(string(b), `,"ts":`)[0])
				}
			case <-timeout:
				return out, false
			}
		}
	}
	for i := 0; ; i++ { // the watcher is up once it clears a probe
		if _, ok := barrier(20 * time.Millisecond); ok {
			break
		}
		if i == 500 {
			t.Fatal("the watcher never subscribed")
		}
	}

	report := func(msg string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/tile-report", strings.NewReader(`{"level":"error","message":"`+msg+`"}`))
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Component: "apps/calendar", Via: "instance"}))
		w := httptest.NewRecorder()
		o.apiStatusSet(w, r)
		if w.Code != 200 {
			t.Fatalf("report: %d %s", w.Code, w.Body.String())
		}
		for e := range seen { // its own status event, past stale probes'
			if e.Component == "apps/calendar" {
				return
			}
		}
	}
	stored := func() string {
		o.statusMu.Lock()
		defer o.statusMu.Unlock()
		return o.statuses["apps/calendar"].Message
	}
	// publish sends the events and returns the status events the watcher
	// published for them.
	publish := func(what string, es ...events.Event) []string {
		t.Helper()
		for _, e := range es {
			o.Hub.Publish(e)
		}
		out, ok := barrier(10 * time.Second)
		if !ok {
			t.Fatalf("%s: the watcher never handled the probe", what)
		}
		return out
	}
	deploy := func(dep, checkpoint, result, phase string, full bool) events.Event {
		d := map[string]any{"op": "deploy", "deployment": dep, "checkpoint": checkpoint, "result": result,
			"by": "user:ana"}
		if phase != "" {
			d["phase"] = phase
		}
		if full {
			d["id"], d["how"], d["session"] = 43, "deploy", "s1"
		}
		return events.Event{Type: "deployments", Component: "apps/calendar", Data: d}
	}
	check := func(what string, got []string, cleared bool, want string) {
		t.Helper()
		clear := `{"type":"status","component":"apps/calendar","data":{"level":"ok","message":""`
		if cleared && (len(got) != 1 || got[0] != clear) || !cleared && len(got) != 0 || stored() != want {
			t.Fatalf("%s: published %v (want the clear: %v), stored %q (want %q)", what, got, cleared, stored(), want)
		}
	}

	report("old generation")
	check("before the swap", publish("before the swap",
		deploy("main", "c:1111111", "queued", "", true),
		deploy("main", "c:1111111", "running", "checkpoint", true), deploy("main", "c:1111111", "running", "checkpoint", false),
		deploy("main", "c:1111111", "running", "materialize", true),
		deploy("main", "c:1111111", "running", "build", true), deploy("main", "c:1111111", "running", "build", false),
		deploy("main", "c:1111111", "running", "start", true)), false, "old generation")
	check("the swap", publish("the swap", deploy("main", "c:1111111", "running", "swap", true)), true, "")
	report("new generation")
	check("the same swap's later events", publish("later events",
		deploy("main", "c:1111111", "running", "swap", false),
		deploy("main", "c:1111111", "ok", "swap", true), deploy("main", "c:1111111", "ok", "swap", false)),
		false, "new generation")

	// a failed deploy keeps the old generation serving, with its status
	check("a failed deploy", publish("a failed deploy",
		deploy("main", "c:2222222", "running", "build", true), deploy("main", "c:2222222", "failed", "build", true),
		deploy("main", "c:2222222", "failed", "build", false)), false, "new generation")

	// a non-primary deployment's swap, and its runner activity
	check("dev's swap", publish("dev's swap",
		deploy("dev", "c:3333333", "running", "swap", true), deploy("dev", "c:3333333", "ok", "swap", true),
		events.Event{Type: "deployments", Component: "apps/calendar", Data: struct {
			Op         string `json:"op"`
			Deployment string `json:"deployment"`
			Phase      string `json:"phase"`
		}{"build", "dev", "start"}}), false, "new generation")

	// a swap reported only by its ok result, with data of any Go type; then a
	// restart onto that same checkpoint swaps, and clears, again
	type deployData struct {
		Op         string `json:"op"`
		Deployment string `json:"deployment"`
		Checkpoint string `json:"checkpoint"`
		Result     string `json:"result"`
		Phase      string `json:"phase"`
	}
	check("an ok swap", publish("an ok swap",
		events.Event{Type: "deployments", Component: "apps/calendar", Data: deployData{"deploy", "main", "c:4444444", "running", "start"}},
		events.Event{Type: "deployments", Component: "apps/calendar", Data: deployData{"deploy", "main", "c:4444444", "ok", "swap"}}),
		true, "")
	report("third generation")
	check("a restart of the same checkpoint", publish("a restart",
		deploy("main", "c:4444444", "running", "start", true), deploy("main", "c:4444444", "running", "swap", true)),
		true, "")

	// a resume: today's build-start clears; the swap it leads to doesn't again
	report("pinned generation")
	check("a resume's build-start", publish("a resume's build",
		events.Event{Type: "build-start", Component: "apps/calendar"}), true, "")
	report("work-tree generation")
	check("a resume's swap", publish("a resume's swap",
		deploy("main", "c:5555555", "ok", "swap", true), deploy("main", "c:5555555", "ok", "swap", false)),
		false, "work-tree generation")

	// with no stored status, a swap publishes nothing, as a build-start doesn't
	o.statusMu.Lock()
	delete(o.statuses, "apps/calendar")
	o.statusMu.Unlock()
	check("nothing stored", publish("nothing stored",
		deploy("main", "c:6666666", "running", "build", true), deploy("main", "c:6666666", "running", "swap", true)),
		false, "")
}
