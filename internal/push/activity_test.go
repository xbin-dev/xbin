package push

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// sessions is a mutable Options.Session for tests.
type sessions struct {
	mu sync.Mutex
	m  map[string]SessionInfo
}

func (s *sessions) get(id string) (SessionInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.m[id]
	return i, ok
}

func (s *sessions) set(id string, i SessionInfo) {
	s.mu.Lock()
	s.m[id] = i
	s.mu.Unlock()
}

const t0 = int64(1_800_000_000_000) // unix ms

func pubKey(t *testing.T) string {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return b64.EncodeToString(k.PublicKey().Bytes())
}

func agentEv(typ string, ts int64, d map[string]any) agent.Event {
	e := agent.New(typ, d)
	e.TS = ts
	return e
}

// liveRig is a rig with agent sessions (s1: alice's, s2: bob's) and no
// push-to-start unless start is set.
func liveRig(t *testing.T, start time.Duration, mod func(*Options)) (*rig, *sessions) {
	ss := &sessions{m: map[string]SessionInfo{
		"s1": {Owner: "alice", Status: agent.StatusRunning},
		"s2": {Owner: "bob", Status: agent.StatusRunning},
	}}
	r := newRig(t, func(o *Options) {
		o.Session = ss.get
		o.StartAfter = start
		if mod != nil {
			mod(o)
		}
	})
	r.mux.HandleFunc("POST /devices/push/activities", r.s.APIActivity)
	r.mux.HandleFunc("DELETE /devices/push/{deviceId}/activities/{session}", r.s.APIActivityDelete)
	return r, ss
}

// livePushes are the relay's Live Activity pushes so far.
func livePushes(r *rig) []relayPush {
	var out []relayPush
	for _, p := range r.relay.pushes() {
		if p.Type == "liveactivity" {
			out = append(out, p)
		}
	}
	return out
}

func waitLive(t *testing.T, r *rig, n int) []relayPush {
	t.Helper()
	eventually(t, "Live Activity pushes", func() bool { return len(livePushes(r)) >= n })
	time.Sleep(20 * time.Millisecond) // and no more
	got := livePushes(r)
	if len(got) != n {
		t.Fatalf("%d Live Activity pushes, want %d: %+v", len(got), n, got)
	}
	return got
}

func (r *rig) activity(p auth.Principal, body map[string]any) (int, map[string]any) {
	r.t.Helper()
	code, out, _ := r.call(p, "POST", "/devices/push/activities", body)
	return code, out
}

func deviceActivities(r *rig, p auth.Principal, deviceID string) []string {
	_, out, _ := r.call(p, "GET", "/devices/push", nil)
	for _, d := range out["devices"].([]any) {
		m := d.(map[string]any)
		if m["deviceId"] == deviceID {
			var s []string
			for _, a := range asSlice(m["activities"]) {
				s = append(s, a.(string))
			}
			return s
		}
	}
	return nil
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// What a Live Activity push may carry: the relay's fields, nothing more.
func checkGeneric(t *testing.T, p relayPush) {
	t.Helper()
	for k := range p.Raw {
		if !slices.Contains([]string{"handle", "type", "activity", "priority"}, k) {
			t.Fatalf("a Live Activity push carries %q: %v", k, p.Raw)
		}
	}
	act := p.Raw["activity"].(map[string]any)
	for k := range act {
		if !slices.Contains([]string{"event", "timestamp", "state", "staleDate", "dismissalDate", "ws", "ref"}, k) {
			t.Fatalf("an activity carries %q: %v", k, act)
		}
	}
	for k := range act["state"].(map[string]any) {
		if !slices.Contains([]string{"phase", "since", "pending"}, k) {
			t.Fatalf("a content state carries %q", k)
		}
	}
}

// A turn, followed: the app registers its activity; waiting and pending
// counts reach it (waiting at priority 10, the rest at 5); the turn's end
// ends it and drops the registration. Timestamps only grow, and nothing but
// generic state goes out.
func TestLiveActivityFollowsTheTurn(t *testing.T) {
	r, _ := liveRig(t, -1, nil)
	r.register(alice, "phone", "handle-phone")
	ev := func(typ string, ms int64, d map[string]any) {
		r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(typ, t0+ms, d))
	}
	ev(agent.EvMessageDelta, 0, map[string]any{"role": "user", "text": "fix the login bug"})
	ev(agent.EvStatus, 4, map[string]any{"status": "running"})
	if st, busy := r.s.liveState("s1"); !busy || st != (ActivityState{Phase: PhaseRunning, Since: t0 / 1000}) {
		t.Fatalf("turn start: %+v %v", st, busy)
	}
	// the card's since does not move a turn xbind saw begin
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-1", "since": 5}); code != 200 ||
		out["activity"].(map[string]any)["session"] != "s1" {
		t.Fatalf("register: %d %v", code, out)
	}
	if got := deviceActivities(r, alice, "phone"); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("listed activities: %v", got)
	}
	ev(agent.EvMessageDelta, 10, map[string]any{"role": "assistant", "text": "Looking…"})
	ev(agent.EvPermissionRequest, 20, map[string]any{"pid": "p1", "toolCall": map[string]any{"title": "rm -rf build"}})
	ev(agent.EvElicitRequest, 30, map[string]any{"eid": "e1", "message": "Which branch?"})
	ev(agent.EvPermissionResolved, 40, map[string]any{"pid": "p1"})
	ev(agent.EvElicitResolved, 50, map[string]any{"eid": "e1"})
	ev(agent.EvStatus, 60, map[string]any{"status": "running"}) // no change: nothing
	ev(agent.EvStatus, 61, map[string]any{"usage": map[string]any{"used": 5}})
	ev(agent.EvTurnEnd, 70, map[string]any{"turn": 1, "stopReason": "end_turn"})
	ev(agent.EvStatus, 71, map[string]any{"status": "idle"})
	got := waitLive(t, r, 5)
	// two sender workers may post out of order: the device orders by
	// timestamp, and so does this test
	slices.SortFunc(got, func(a, b relayPush) int { return int(a.Activity.Timestamp - b.Activity.Timestamp) })
	since := t0 / 1000
	want := []struct {
		event string
		state ActivityState
		prio  int
	}{
		{"update", ActivityState{PhaseWaiting, since, 1}, 10},
		{"update", ActivityState{PhaseWaiting, since, 2}, 10},
		{"update", ActivityState{PhaseWaiting, since, 1}, 10},
		{"update", ActivityState{PhaseRunning, since, 0}, 5},
		{"end", ActivityState{PhaseIdle, since, 0}, 10},
	}
	var last int64
	for i, p := range got {
		checkGeneric(t, p)
		a := p.Activity
		if p.Handle != "la-handle-1" || a.Event != want[i].event || a.State != want[i].state || p.Priority != want[i].prio {
			t.Fatalf("push %d: %s %+v prio %d, want %+v", i, p.Handle, a, p.Priority, want[i])
		}
		if a.Timestamp == last {
			t.Fatalf("push %d: timestamp %d twice", i, a.Timestamp)
		}
		last = a.Timestamp
		if (a.Event == "update") != (a.StaleDate > 0) || (a.Event == "end") != (a.DismissalDate > 0) {
			t.Fatalf("push %d dates: %+v", i, a)
		}
	}
	if got := deviceActivities(r, alice, "phone"); len(got) != 0 {
		t.Fatalf("the ended activity is still registered: %v", got)
	}
}

// A long turn starts its activity by push on devices with a push-to-start
// handle and none for the session (and kinds that take agent pushes); the
// app registers the new activity's token by the start's ref and gets what
// changed since. A turn that ends first starts nothing.
func TestLiveActivityPushToStart(t *testing.T) {
	r, _ := liveRig(t, 60*time.Millisecond, nil)
	reg := func(p auth.Principal, dev, handle string, start string, kinds ...string) {
		t.Helper()
		body := map[string]any{"deviceId": dev, "handle": handle, "publicKey": pubKey(t), "startHandle": start}
		if kinds != nil {
			body["kinds"] = kinds
		}
		if code, out, _ := r.call(p, "POST", "/devices/push", body); code != 200 {
			t.Fatalf("register %s: %d %v", dev, code, out)
		}
	}
	reg(alice, "phone", "handle-phone", "start-handle-phone")
	reg(alice, "ipad", "handle-ipad", "")                             // no push-to-start
	reg(alice, "watch", "handle-watch", "start-handle-watch", "tile") // takes no agent pushes
	reg(alice, "mac", "handle-mac", "start-handle-mac")
	ev := func(typ string, ms int64, d map[string]any) {
		r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(typ, t0+ms, d))
	}

	// a quick turn: no start
	ev(agent.EvMessageDelta, -500, map[string]any{"role": "user", "text": "hi"})
	ev(agent.EvStatus, 0, map[string]any{"status": "running"})
	if st, _ := r.s.liveState("s1"); st.Since != (t0-500)/1000 {
		t.Fatalf("the turn's start is its prompt: %+v", st)
	}
	ev(agent.EvTurnEnd, 10, map[string]any{"turn": 1, "stopReason": "end_turn"})
	time.Sleep(120 * time.Millisecond)
	if n := len(livePushes(r)); n != 0 {
		t.Fatalf("a quick turn started %d activities", n)
	}
	// a long one (a prompt from before the last turn ended is not its
	// start); the mac shows its own (local) activity already
	ev(agent.EvStatus, 1000, map[string]any{"status": "running"})
	if code, out := r.activity(alice, map[string]any{"deviceId": "mac", "session": "s1", "handle": "la-handle-mac"}); code != 200 {
		t.Fatalf("mac: %d %v", code, out)
	}
	got := waitLive(t, r, 1)
	p := got[0]
	checkGeneric(t, p)
	if p.Handle != "start-handle-phone" || p.Activity.Event != "start" || p.Activity.WS != r.s.Workspace() || p.Activity.Ref == "" ||
		p.Activity.State != (ActivityState{PhaseRunning, (t0 + 1000) / 1000, 0}) || p.Priority != 10 {
		t.Fatalf("start: %+v %+v", p, p.Activity)
	}
	ref := p.Activity.Ref
	if got := deviceActivities(r, alice, "phone"); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("the started activity is not listed: %v", got)
	}
	// a question before the app registers the token: no handle yet, so
	// nothing goes — and the mac's local activity gets it
	ev(agent.EvElicitRequest, 2000, map[string]any{"eid": "e1", "message": "?"})
	got = waitLive(t, r, 2)
	if got[1].Handle != "la-handle-mac" || got[1].Activity.State.Phase != PhaseWaiting {
		t.Fatalf("mac update: %+v", got[1])
	}
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "ref": ref, "handle": "la-handle-phone"}); code != 200 ||
		out["activity"].(map[string]any)["session"] != "s1" {
		t.Fatalf("register by ref: %d %v", code, out)
	}
	got = waitLive(t, r, 3)
	if got[2].Handle != "la-handle-phone" || got[2].Activity.Event != "update" || got[2].Activity.State != (ActivityState{PhaseWaiting, (t0 + 1000) / 1000, 1}) {
		t.Fatalf("catch-up after the ref registration: %+v", got[2].Activity)
	}
	if code, _ := r.activity(alice, map[string]any{"deviceId": "phone", "ref": "unknownref123", "handle": "la-handle-xx"}); code != 404 {
		t.Fatalf("unknown ref: %d", code)
	}
	// the end reaches both
	ev(agent.EvTurnEnd, 3000, map[string]any{"turn": 2, "stopReason": "end_turn"})
	got = waitLive(t, r, 5)
	ends := []string{got[3].Handle, got[4].Handle}
	slices.Sort(ends)
	if !slices.Equal(ends, []string{"la-handle-mac", "la-handle-phone"}) || got[3].Activity.Event != "end" || got[4].Activity.Event != "end" {
		t.Fatalf("ends: %+v %+v", got[3], got[4])
	}
}

// Only the caller's own sessions, from the caller's own registration.
func TestLiveActivityAuthorization(t *testing.T) {
	r, _ := liveRig(t, -1, func(o *Options) { o.Limits = DefaultLimits; o.Limits.Activities = Rate{PerHour: 1, Burst: 12} })
	r.register(alice, "phone", "handle-phone")
	phone := auth.Principal{UserID: "alice", Via: "device", DeviceID: "phone", Gen: "d.phone"}
	for name, c := range map[string]struct {
		p    auth.Principal
		body map[string]any
		want int
	}{
		"bob's session":      {alice, map[string]any{"deviceId": "phone", "session": "s2", "handle": "la-handle-01"}, 404},
		"unknown session":    {alice, map[string]any{"deviceId": "phone", "session": "s9", "handle": "la-handle-01"}, 404},
		"unregistered":       {alice, map[string]any{"deviceId": "tablet", "session": "s1", "handle": "la-handle-01"}, 404},
		"another device's":   {phone, map[string]any{"deviceId": "tablet", "session": "s1", "handle": "la-handle-01"}, 403},
		"another sign-in's":  {auth.Principal{UserID: "alice", Via: "session", Gen: "s.other"}, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-01"}, 403},
		"a tile":             {cal, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-01"}, 403},
		"the device handle":  {alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "handle-phone"}, 400},
		"session and ref":    {alice, map[string]any{"deviceId": "phone", "session": "s1", "ref": "abcdefgh", "handle": "la-handle-01"}, 400},
		"neither":            {alice, map[string]any{"deviceId": "phone", "handle": "la-handle-01"}, 400},
		"bad handle":         {alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "a b"}, 400},
		"own session, owned": {alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-01"}, 200},
	} {
		if code, out := r.activity(c.p, c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", name, code, out, c.want)
		}
	}
	if code, _, _ := r.call(bob, "DELETE", "/devices/push/phone/activities/s1", nil); code != 404 {
		t.Fatalf("bob deleting alice's: %d", code)
	}
	if code, _, _ := r.call(alice, "DELETE", "/devices/push/phone/activities/s1", nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	// again: nothing registered, but the dismissal of the running turn is
	// noted all the same; a session with neither is 404
	if code, _, _ := r.call(alice, "DELETE", "/devices/push/phone/activities/s1", nil); code != 204 {
		t.Fatalf("delete again: %d", code)
	}
	if code, _, _ := r.call(alice, "DELETE", "/devices/push/phone/activities/s9", nil); code != 404 {
		t.Fatalf("delete an unknown session's: %d", code)
	}
	// registrations are rate-limited per person
	for i := 0; ; i++ {
		code, _ := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-01"})
		if code == 429 {
			break
		}
		if i > 20 {
			t.Fatal("no limit on activity registrations")
		}
	}
}

// The relay's word on a handle ends that activity's registration (or the
// push-to-start handle); a new device handle drops both, re-registering
// the same one keeps them.
func TestLiveActivityHandlesFollowTheRelay(t *testing.T) {
	r, _ := liveRig(t, 30*time.Millisecond, nil)
	// kinds: Live Activities only, so no alert takes the relay answers
	// queued below
	regBody := func(handle string, start any) map[string]any {
		b := map[string]any{"deviceId": "phone", "handle": handle, "publicKey": pubKey(t), "kinds": []string{KindAgentActivity}}
		if start != nil {
			b["startHandle"] = start
		}
		return b
	}
	if code, out, _ := r.call(alice, "POST", "/devices/push", regBody("handle-phone", "start-handle-phone")); code != 200 ||
		out["device"].(map[string]any)["pushToStart"] != true {
		t.Fatalf("register: %d %v", code, out)
	}
	ev := func(typ string, ms int64, d map[string]any) {
		r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(typ, t0+ms, d))
	}
	ev(agent.EvStatus, 0, map[string]any{"status": "running"})
	if code, _ := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-01"}); code != 200 {
		t.Fatal(code)
	}
	// the activity's token is dead: its registration goes
	r.relay.answer(fakeAnswer{410, `{"code":"handle_gone"}`})
	ev(agent.EvPermissionRequest, 10, map[string]any{"pid": "p1"})
	eventually(t, "the dead activity dropped", func() bool {
		d, ok := r.s.st.device("alice", "phone")
		return ok && len(d.Activities) == 0 // the registration stays, the activity goes
	})
	// a new turn: push-to-start, which the relay no longer knows
	ev(agent.EvTurnEnd, 20, map[string]any{"turn": 1, "stopReason": "end_turn"})
	r.relay.answer(fakeAnswer{404, `{"code":"handle_unknown"}`})
	ev(agent.EvStatus, 30, map[string]any{"status": "running"})
	eventually(t, "the push-to-start handle dropped", func() bool {
		d, _ := r.s.st.device("alice", "phone")
		return d.StartHandle == ""
	})
	// the same device handle again keeps what hangs off it; a new one drops it
	r.call(alice, "POST", "/devices/push", regBody("handle-phone", "start-handle-2"))
	if code, _ := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-02"}); code != 200 {
		t.Fatal(code)
	}
	r.call(alice, "POST", "/devices/push", regBody("handle-phone", nil))
	if d, _ := r.s.st.device("alice", "phone"); d.StartHandle != "start-handle-2" || len(d.Activities) != 1 {
		t.Fatalf("re-registering the same handle lost them: %+v", d)
	}
	r.call(alice, "POST", "/devices/push", regBody("handle-phone-2", nil))
	if d, _ := r.s.st.device("alice", "phone"); d.StartHandle != "" || len(d.Activities) != 0 {
		t.Fatalf("a new device handle kept its old children: %+v", d)
	}
	r.call(alice, "POST", "/devices/push", regBody("handle-phone-2", "start-handle-3"))
	r.call(alice, "POST", "/devices/push", regBody("handle-phone-2", ""))
	if d, _ := r.s.st.device("alice", "phone"); d.StartHandle != "" {
		t.Fatalf("startHandle \"\" kept it: %+v", d)
	}
	// a push-to-start handle the relay takes no start on (registered as a
	// card's): dropped, so the app registers it again
	ev(agent.EvTurnEnd, 40, map[string]any{"turn": 2, "stopReason": "end_turn"})
	r.call(alice, "POST", "/devices/push", regBody("handle-phone-2", "start-handle-4"))
	r.relay.answer(fakeAnswer{400, `{"code":"bad_request"}`})
	ev(agent.EvStatus, 50, map[string]any{"status": "running"})
	eventually(t, "the card-kind push-to-start handle dropped", func() bool {
		d, _ := r.s.st.device("alice", "phone")
		return d.StartHandle == ""
	})
}

// An activity registered after its turn ended gets its end at once; a
// session that closes ends its activities; a restart ends every one
// registered (agent sessions do not outlive xbind).
func TestLiveActivityEnds(t *testing.T) {
	r, ss := liveRig(t, -1, nil)
	r.register(alice, "phone", "handle-phone")
	ev := func(sess string, typ string, ms int64, d map[string]any) {
		r.s.AgentEvent("alice", sess, "apps/cal", agentEv(typ, t0+ms, d))
	}
	// s1: ended before the registration arrived
	ev("s1", agent.EvStatus, 0, map[string]any{"status": "running"})
	ev("s1", agent.EvTurnEnd, 10, map[string]any{"turn": 1, "stopReason": "end_turn"})
	ss.set("s1", SessionInfo{Owner: "alice", Status: agent.StatusIdle})
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-late"}); code != 200 ||
		out["activity"].(map[string]any)["ended"] != true {
		t.Fatalf("a registration after the turn: %d %v", code, out)
	}
	got := waitLive(t, r, 1)
	if got[0].Handle != "la-handle-late" || got[0].Activity.Event != "end" {
		t.Fatalf("late registration: %+v", got[0].Activity)
	}
	// s3: a turn xbind did not follow (no device when it began) is taken
	// from the directory
	ss.set("s3", SessionInfo{Owner: "alice", Status: agent.StatusRunning})
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s3", "handle": "la-handle-03", "since": 1_790_000_000}); code != 200 ||
		out["activity"].(map[string]any)["ended"] != nil {
		t.Fatalf("a registration mid-turn: %d %v", code, out)
	}
	if st, busy := r.s.liveState("s3"); !busy || st != (ActivityState{Phase: PhaseRunning, Since: 1_790_000_000}) {
		t.Fatalf("unfollowed turn: %+v %v", st, busy)
	}
	r.s.SessionClosed("s3")
	got = waitLive(t, r, 2)
	if got[1].Handle != "la-handle-03" || got[1].Activity.Event != "end" {
		t.Fatalf("session closed: %+v", got[1].Activity)
	}
	// a registration left from a run before a restart
	ev("s1", agent.EvStatus, 100, map[string]any{"status": "running"})
	if code, _ := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-restart"}); code != 200 {
		t.Fatal(code)
	}
	raw, _ := os.ReadFile(filepath.Join(r.s.o.Dir, "push.json"))
	var file fileState
	if json.Unmarshal(raw, &file) != nil || len(file.Devices) != 1 || len(file.Devices[0].Activities) != 1 {
		t.Fatalf("activity not persisted: %s", raw)
	}
	o := r.s.o
	s2, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Close)
	s2.EndActivities()
	eventually(t, "the restart's end", func() bool {
		for _, p := range livePushes(r) {
			if p.Handle == "la-handle-restart" && p.Activity.Event == "end" {
				return true
			}
		}
		return false
	})
	if d, _ := s2.st.device("alice", "phone"); len(d.Activities) != 0 {
		t.Fatalf("registrations kept over a restart: %+v", d.Activities)
	}
}

// Push off: nothing is followed or sent, and registering still works.
func TestLiveActivityPushOff(t *testing.T) {
	r, _ := liveRig(t, 10*time.Millisecond, func(o *Options) { o.RelayURL, o.RelayKey = "", "" })
	r.register(alice, "phone", "handle-phone")
	r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(agent.EvStatus, t0, map[string]any{"status": "running"}))
	if code, _ := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-01"}); code != 200 {
		t.Fatal(code)
	}
	time.Sleep(50 * time.Millisecond)
	if len(r.relay.pushes()) != 0 {
		t.Fatal("pushed while off")
	}
}

// xbind and the app count a turn's start the same way (the card's clock
// must not jump when an update comes from the other side): the captured
// agent sessions the app's own tests replay (XbinAgent's Fixtures; its
// LiveActivityTests.capturedSessions checks the same numbers) give every
// turn its prompt's second here too.
func TestLiveActivityTurnStartMatchesTheApp(t *testing.T) {
	for _, name := range []string{"basic", "cancel"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "native", "ios", "Packages", "XbinAgent", "Tests", "XbinAgentTests", "Fixtures", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var fx struct{ Events []agent.Event }
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatal(err)
		}
		r, _ := liveRig(t, -1, nil)
		r.register(alice, "phone", "handle-phone")
		var prompt int64
		turns := 0
		for _, e := range fx.Events {
			var d struct{ Role, Parent string }
			if e.Type == agent.EvMessageDelta && json.Unmarshal(e.Data, &d) == nil && d.Role == "user" && d.Parent == "" {
				prompt = e.TS
			}
			_, was := r.s.liveState("s1")
			r.s.AgentEvent("alice", "s1", "apps/cal", e)
			if st, busy := r.s.liveState("s1"); busy && !was {
				turns++
				if st.Since != prompt/1000 {
					t.Fatalf("%s seq %d: since %d, want the prompt's %d", name, e.Seq, st.Since, prompt/1000)
				}
			}
		}
		if turns < 2 {
			t.Fatalf("%s: %d turns seen", name, turns)
		}
	}
}

// testClock is a settable Options.Now.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// A push-started card whose turn ends before the app registers its token
// still gets its end: the registration by its ref answers the session and
// ended, and xbind pushes the end (idle, the turn's start, after the
// start's timestamp) — for endedRefTTL, to that device of that person
// only; after that its ref is unknown (404) and the app ends the card
// itself.
func TestLiveActivityPushStartedEndsBeforeItsToken(t *testing.T) {
	clock := &testClock{t: time.Now()}
	r, _ := liveRig(t, 40*time.Millisecond, func(o *Options) { o.Now = clock.now })
	reg := func(p auth.Principal, dev, handle, start string) {
		t.Helper()
		body := map[string]any{"deviceId": dev, "handle": handle, "publicKey": pubKey(t), "startHandle": start}
		if code, out, _ := r.call(p, "POST", "/devices/push", body); code != 200 {
			t.Fatalf("register %s: %d %v", dev, code, out)
		}
	}
	reg(alice, "phone", "handle-phone", "start-handle-phone")
	reg(alice, "mac", "handle-mac", "")
	reg(bob, "phone", "handle-bob-phone", "")
	ev := func(typ string, ms int64, d map[string]any) {
		r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(typ, t0+ms, d))
	}
	ev(agent.EvStatus, 1000, map[string]any{"status": "running"})
	start := waitLive(t, r, 1)[0]
	if start.Handle != "start-handle-phone" || start.Activity.Event != "start" {
		t.Fatalf("start: %+v", start)
	}
	ref := start.Activity.Ref
	ev(agent.EvTurnEnd, 5000, map[string]any{"turn": 1, "stopReason": "end_turn"})
	time.Sleep(30 * time.Millisecond)
	if n := len(livePushes(r)); n != 1 {
		t.Fatalf("%d pushes for a card with no token yet", n)
	}
	// the token comes after the turn
	code, out := r.activity(alice, map[string]any{"deviceId": "phone", "ref": ref, "handle": "la-handle-phone", "since": 1_790_000_000})
	if act, _ := out["activity"].(map[string]any); code != 200 || act["session"] != "s1" || act["ended"] != true {
		t.Fatalf("the late registration by ref: %d %v", code, out)
	}
	end := waitLive(t, r, 2)[1]
	checkGeneric(t, end)
	if end.Handle != "la-handle-phone" || end.Activity.Event != "end" || end.Activity.Timestamp <= start.Activity.Timestamp ||
		end.Activity.State != (ActivityState{Phase: PhaseIdle, Since: (t0 + 1000) / 1000}) || end.Activity.DismissalDate == 0 {
		t.Fatalf("the end: %+v %+v", end, end.Activity)
	}
	if got := deviceActivities(r, alice, "phone"); got != nil {
		t.Fatalf("an ended card stays registered: %v", got)
	}
	// the ref is that device's, of that person
	if code, _ := r.activity(alice, map[string]any{"deviceId": "mac", "ref": ref, "handle": "la-handle-mac"}); code != 404 {
		t.Fatalf("another device's ref: %d", code)
	}
	if code, _ := r.activity(bob, map[string]any{"deviceId": "phone", "ref": ref, "handle": "la-handle-bob"}); code != 404 {
		t.Fatalf("another person's ref: %d", code)
	}
	// and it is remembered for a while only
	clock.advance(endedRefTTL + time.Second)
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "ref": ref, "handle": "la-handle-phone"}); code != 404 {
		t.Fatalf("after the TTL: %d %v", code, out)
	}
	if n := len(livePushes(r)); n != 2 {
		t.Fatalf("%d pushes, want the start and one end", n)
	}
}

// An update the per-handle limit refuses is not lost: once the limit
// allows again, the card gets the turn's state as it is then — a wait for
// the user that came while the bucket was empty still reaches the lock
// screen.
func TestLiveActivityLimitedUpdateGoesLater(t *testing.T) {
	r, _ := liveRig(t, -1, func(o *Options) {
		o.Limits = DefaultLimits
		o.Limits.ActivityPush = Rate{PerHour: 20 * 3600, Burst: 2} // a token every 50 ms
	})
	r.register(alice, "phone", "handle-phone")
	ev := func(typ string, ms int64, d map[string]any) {
		r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(typ, t0+ms, d))
	}
	ev(agent.EvMessageDelta, 0, map[string]any{"role": "user", "text": "go"})
	ev(agent.EvStatus, 1, map[string]any{"status": "running"})
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-1"}); code != 200 {
		t.Fatalf("register: %d %v", code, out)
	}
	// approved at once, then asked again: the third update finds the
	// bucket empty
	ev(agent.EvPermissionRequest, 10, map[string]any{"pid": "p1"})
	ev(agent.EvPermissionResolved, 20, map[string]any{"pid": "p1"})
	ev(agent.EvPermissionRequest, 30, map[string]any{"pid": "p2"})
	if r.s.snd.limited.Load() == 0 {
		t.Fatal("the limit held nothing back (the test would prove nothing)")
	}
	got := waitLive(t, r, 3)
	slices.SortFunc(got, func(a, b relayPush) int { return int(a.Activity.Timestamp - b.Activity.Timestamp) })
	since := t0 / 1000
	for i, want := range []ActivityState{{PhaseWaiting, since, 1}, {PhaseRunning, since, 0}, {PhaseWaiting, since, 1}} {
		if got[i].Activity.Event != "update" || got[i].Activity.State != want {
			t.Fatalf("push %d: %+v, want %+v", i, got[i].Activity, want)
		}
	}
	if got[2].Priority != 10 {
		t.Fatalf("the late wait's priority: %d", got[2].Priority)
	}
	// a held-back change that is undone before the limit allows sends
	// nothing (the card shows the state already)
	ev(agent.EvPermissionResolved, 40, map[string]any{"pid": "p2"}) // running/0: sent at once or held
	ev(agent.EvPermissionRequest, 50, map[string]any{"pid": "p3"})
	ev(agent.EvPermissionResolved, 60, map[string]any{"pid": "p3"})
	eventually(t, "the card catches up", func() bool {
		ps := livePushes(r)
		slices.SortFunc(ps, func(a, b relayPush) int { return int(a.Activity.Timestamp - b.Activity.Timestamp) })
		return ps[len(ps)-1].Activity.State == ActivityState{PhaseRunning, since, 0}
	})
	time.Sleep(150 * time.Millisecond)
	ps := livePushes(r)
	slices.SortFunc(ps, func(a, b relayPush) int { return int(a.Activity.Timestamp - b.Activity.Timestamp) })
	if last := ps[len(ps)-1].Activity.State; last != (ActivityState{PhaseRunning, since, 0}) {
		t.Fatalf("the card ends on %+v", last)
	}
}

// A card the user swiped away stays away for that turn (native/spec/push.md
// §7.1): the app's DELETE keeps xbind's push-to-start off that device for
// the turn — also when the app's own card never got registered — and the
// next turn starts one as usual.
// fireStart runs session's armed push-to-start now, as its timer would: a
// test that must set things up before it fires arms it far off and fires
// it itself, instead of racing a short timer on a loaded runner.
func fireStart(t *testing.T, s *Service, session string) {
	t.Helper()
	s.lmu.Lock()
	ls := s.turns[session]
	armed := ls != nil && ls.timer != nil
	var turn int
	if armed {
		ls.timer.Stop()
		turn = ls.turn
	}
	s.lmu.Unlock()
	if !armed {
		t.Fatalf("no push-to-start armed for %s", session)
	}
	s.pushToStart(session, ls, turn)
}

// The dismissals land before the push-to-start fires: the test fires it
// itself (fireStart). With a 60 ms timer, a loaded CI runner sometimes
// took longer to register and dismiss, and the phone got a push.
func TestLiveActivityDismissedCardStaysAway(t *testing.T) {
	r, _ := liveRig(t, time.Hour, nil)
	for _, d := range []string{"phone", "ipad", "mac"} {
		body := map[string]any{"deviceId": d, "handle": "handle-" + d, "publicKey": pubKey(t), "startHandle": "start-handle-" + d}
		if code, out, _ := r.call(alice, "POST", "/devices/push", body); code != 200 {
			t.Fatalf("register %s: %d %v", d, code, out)
		}
	}
	ev := func(typ string, ms int64, d map[string]any) {
		r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(typ, t0+ms, d))
	}
	ev(agent.EvMessageDelta, 0, map[string]any{"role": "user", "text": "go"})
	ev(agent.EvStatus, 1, map[string]any{"status": "running"})
	// the phone's card registered, then swiped away; the iPad's never got
	// registered (its registration failed) and was swiped away too
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "session": "s1", "handle": "la-handle-phone"}); code != 200 {
		t.Fatalf("register: %d %v", code, out)
	}
	for _, d := range []string{"phone", "ipad"} {
		if code, _, _ := r.call(alice, "DELETE", "/devices/push/"+d+"/activities/s1", nil); code != 204 {
			t.Fatalf("dismiss on %s: %d", d, code)
		}
	}
	fireStart(t, r.s, "s1")
	got := waitLive(t, r, 1)
	time.Sleep(100 * time.Millisecond)
	if n := len(livePushes(r)); n != 1 || got[0].Handle != "start-handle-mac" || got[0].Activity.Event != "start" {
		t.Fatalf("push-to-start after a dismissal: %d pushes, first %+v", n, got[0])
	}
	// the next turn is a new card everywhere
	ev(agent.EvTurnEnd, 1000, map[string]any{"turn": 1, "stopReason": "end_turn"})
	ev(agent.EvStatus, 1001, map[string]any{"status": "idle"})
	ev(agent.EvMessageDelta, 2000, map[string]any{"role": "user", "text": "more"})
	ev(agent.EvStatus, 2001, map[string]any{"status": "running"})
	fireStart(t, r.s, "s1")
	got = waitLive(t, r, 4)
	var starts []string
	for _, p := range got[1:] {
		if p.Activity.Event == "start" {
			starts = append(starts, p.Handle)
		}
	}
	slices.Sort(starts)
	if !slices.Equal(starts, []string{"start-handle-ipad", "start-handle-mac", "start-handle-phone"}) {
		t.Fatalf("turn 2 starts: %v", starts)
	}
}

// A turn that ends while its user has no registration (signed out
// everywhere, the app's registration removed) is not carried into the next
// one: when the device registers again, the next turn starts afresh — its
// own start, no requests of the old turn pending, and a push-to-start.
func TestLiveActivityTurnAfterRegistrationsWent(t *testing.T) {
	// The registration must go before turn 1's push-to-start fires — a few
	// synchronous calls after the turn starts. 60 ms (the other tests')
	// was not always enough on a loaded CI runner under -race: the push
	// went to the still-registered device.
	const start = 400 * time.Millisecond
	r, _ := liveRig(t, start, nil)
	reg := func() {
		t.Helper()
		body := map[string]any{"deviceId": "phone", "handle": "handle-phone", "publicKey": pubKey(t), "startHandle": "start-handle-phone"}
		if code, out, _ := r.call(alice, "POST", "/devices/push", body); code != 200 {
			t.Fatalf("register: %d %v", code, out)
		}
	}
	ev := func(typ string, ms int64, d map[string]any) {
		r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(typ, t0+ms, d))
	}
	for _, gone := range []struct {
		name string
		drop func()
	}{
		{"sign-out everywhere", func() { r.s.SignedOut("alice") }},
		{"unregistered", func() { r.call(alice, "DELETE", "/devices/push/phone", nil) }},
	} {
		base := int64(len(livePushes(r)))
		off := base * 1_000_000 // each round later than the last
		reg()
		ev(agent.EvMessageDelta, off, map[string]any{"role": "user", "text": "go"})
		ev(agent.EvStatus, off+1, map[string]any{"status": "running"})
		ev(agent.EvPermissionRequest, off+2, map[string]any{"pid": "p1"})
		gone.drop()
		ev(agent.EvTurnEnd, off+1000, map[string]any{"turn": 1, "stopReason": "end_turn"})
		ev(agent.EvStatus, off+1001, map[string]any{"status": "idle"})
		time.Sleep(2 * start) // turn 1's push-to-start would have gone
		if n := int64(len(livePushes(r))); n != base {
			t.Fatalf("%s: %d pushes for a turn with no device", gone.name, n-base)
		}
		reg()
		ev(agent.EvMessageDelta, off+600_000, map[string]any{"role": "user", "text": "again"})
		ev(agent.EvStatus, off+600_001, map[string]any{"status": "running"})
		want := ActivityState{Phase: PhaseRunning, Since: (t0 + off + 600_000) / 1000}
		if st, busy := r.s.liveState("s1"); !busy || st != want {
			t.Fatalf("%s: turn 2 reads %+v %v, want %+v", gone.name, st, busy, want)
		}
		got := waitLive(t, r, int(base)+1)
		if p := got[base]; p.Handle != "start-handle-phone" || p.Activity.Event != "start" || p.Activity.State != want {
			t.Fatalf("%s: turn 2's push-to-start: %+v", gone.name, p.Activity)
		}
		ev(agent.EvTurnEnd, off+700_000, map[string]any{"turn": 2, "stopReason": "end_turn"})
		ev(agent.EvStatus, off+700_001, map[string]any{"status": "idle"})
	}
}

// A registration that goes takes its cards along: nothing would reach
// them any more, so xbind ends them now (idle, dismissed at once) instead
// of leaving them frozen until they go stale — the panel's remove, the
// device signing out, sign-out-everywhere.
func TestLiveActivityEndsWithItsRegistration(t *testing.T) {
	r, _ := liveRig(t, -1, nil)
	r.register(alice, "phone", "handle-phone")
	r.register(alice, "ipad", "handle-ipad")
	r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(agent.EvMessageDelta, t0, map[string]any{"role": "user", "text": "go"}))
	r.s.AgentEvent("alice", "s1", "apps/cal", agentEv(agent.EvStatus, t0+1, map[string]any{"status": "running"}))
	for _, d := range []string{"phone", "ipad"} {
		if code, out := r.activity(alice, map[string]any{"deviceId": d, "session": "s1", "handle": "la-handle-" + d}); code != 200 {
			t.Fatalf("register %s: %d %v", d, code, out)
		}
	}
	if code, _, _ := r.call(alice, "DELETE", "/devices/push/phone", nil); code != 204 {
		t.Fatalf("unregister: %d", code)
	}
	got := waitLive(t, r, 1)
	checkGeneric(t, got[0])
	if a := got[0].Activity; got[0].Handle != "la-handle-phone" || a.Event != "end" ||
		a.State != (ActivityState{Phase: PhaseIdle, Since: t0 / 1000}) || a.DismissalDate == 0 {
		t.Fatalf("the removed registration's card: %s %+v", got[0].Handle, a)
	}
	r.s.SignedOut("alice")
	got = waitLive(t, r, 2)
	if got[1].Handle != "la-handle-ipad" || got[1].Activity.Event != "end" || got[1].Activity.Timestamp <= got[0].Activity.Timestamp {
		t.Fatalf("sign-out-everywhere's: %s %+v", got[1].Handle, got[1].Activity)
	}
}
