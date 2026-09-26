package relay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ActivityKit push tokens: longer than device tokens, and one per activity.
var (
	laToken  = strings.Repeat("c3", 80)
	laToken2 = strings.Repeat("d4", 80)
)

func (r *rig) child(parent, token string) (int, map[string]any) {
	r.t.Helper()
	code, out, _ := r.call("POST", "/v1/handles", "", map[string]string{"apnsToken": token, "topic": topic, "env": "production",
		"pushType": "liveactivity", "parent": parent})
	return code, out
}

func (r *rig) mustChild(parent, token string) string {
	r.t.Helper()
	code, out := r.child(parent, token)
	if code != 200 {
		r.t.Fatalf("child: %d %v", code, out)
	}
	return out["handle"].(string)
}

// startChild registers the app's push-to-start token under parent.
func (r *rig) startChild(parent, token string) string {
	r.t.Helper()
	code, out, _ := r.call("POST", "/v1/handles", "", map[string]any{"apnsToken": token, "topic": topic, "env": "production",
		"pushType": "liveactivity", "parent": parent, "start": true})
	if code != 200 {
		r.t.Fatalf("start child: %d %v", code, out)
	}
	return out["handle"].(string)
}

func (r *rig) start(since int64) map[string]any {
	return map[string]any{"event": "start", "timestamp": r.clock().Unix(), "ws": "wsPushId_1", "ref": "ref-7",
		"state": map[string]any{"phase": "running", "since": since, "pending": 0}}
}

func (r *rig) live(key, handle string, act map[string]any, extra map[string]any) (int, map[string]any) {
	r.t.Helper()
	body := map[string]any{"handle": handle, "type": "liveactivity", "activity": act}
	for k, v := range extra {
		body[k] = v
	}
	code, out, _ := r.call("POST", "/v1/push", key, body)
	return code, out
}

func (r *rig) update(phase string, since, pending int64) map[string]any {
	return map[string]any{"event": "update", "timestamp": r.clock().Unix(),
		"state": map[string]any{"phase": phase, "since": since, "pending": pending}}
}

// A liveactivity push goes out as ActivityKit wants it — push type and topic
// suffix, the content state under aps — and carries nothing the relay did
// not build from checked fields.
func TestLiveActivityPushShape(t *testing.T) {
	r := newRig(t, nil)
	key, parent := r.workspace(), r.handle(tokenOK)
	h := r.mustChild(parent, laToken)
	sh := r.startChild(parent, laToken2)
	now := r.clock().Unix()

	upd := r.update("waiting", now-42, 2)
	upd["staleDate"] = now + 3600
	if code, out := r.live(key, h, upd, map[string]any{"priority": 10}); code != 200 || out["apnsId"] == "" {
		t.Fatalf("update: %d %v", code, out)
	}
	end := map[string]any{"event": "end", "timestamp": now + 1, "dismissalDate": now + 600,
		"state": map[string]any{"phase": "idle", "since": now - 42, "pending": 0}}
	if code, out := r.live(key, h, end, nil); code != 200 {
		t.Fatalf("end: %d %v", code, out)
	}
	// the end retired the activity's handle: nothing more reaches it
	if code, out := r.live(key, h, r.update("idle", now-42, 0), nil); code != 404 || out["code"] != ErrHandleUnknown {
		t.Fatalf("an update after the end: %d %v", code, out)
	}
	start := map[string]any{"event": "start", "timestamp": now + 2, "ws": "wsPushId_1", "ref": "ref-7",
		"state": map[string]any{"phase": "running", "since": now, "pending": 0}}
	if code, out := r.live(key, sh, start, nil); code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	got := r.fake.pushes()
	if len(got) != 3 {
		t.Fatalf("fake got %d pushes", len(got))
	}
	for i, p := range got {
		tok := laToken
		if i == 2 {
			tok = laToken2
		}
		if p.token != tok || p.pushType != "liveactivity" || p.topic != topic+".push-type.liveactivity" || p.collapse != "" {
			t.Fatalf("push %d headers: %+v", i, p)
		}
	}
	if got[0].priority != "10" || got[1].priority != "10" {
		t.Fatalf("priorities: %s %s", got[0].priority, got[1].priority)
	}
	want := func(i int, js string) {
		t.Helper()
		var w map[string]any
		if err := json.Unmarshal([]byte(js), &w); err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(got[i].body)
		b, _ := json.Marshal(w)
		if string(a) != string(b) {
			t.Fatalf("push %d body\n got %s\nwant %s", i, a, b)
		}
	}
	want(0, `{"aps":{"timestamp":`+fmtInt(now)+`,"event":"update","stale-date":`+fmtInt(now+3600)+`,
		"content-state":{"phase":"waiting","since":`+fmtInt(now-42)+`,"pending":2}}}`)
	want(1, `{"aps":{"timestamp":`+fmtInt(now+1)+`,"event":"end","dismissal-date":`+fmtInt(now+600)+`,
		"content-state":{"phase":"idle","since":`+fmtInt(now-42)+`,"pending":0}}}`)
	want(2, `{"aps":{"timestamp":`+fmtInt(now+2)+`,"event":"start","input-push-token":1,
		"content-state":{"phase":"running","since":`+fmtInt(now)+`,"pending":0},
		"attributes-type":"AgentActivityAttributes",
		"attributes":{"ws":"wsPushId_1","ref":"ref-7","workspace":"","session":"","appWorkspace":"","sessionID":""},
		"alert":{"title":"xbin","body":"An agent is working."}}}`)
}

// Only generic, enumerated state passes: anything else is refused before
// APNs, and a handle takes only its own kind of push.
func TestLiveActivityRefusals(t *testing.T) {
	r := newRig(t, nil)
	key, parent := r.workspace(), r.handle(tokenOK)
	h := r.mustChild(parent, laToken)
	now := r.clock().Unix()
	st := func(phase string, since, pending int64) map[string]any {
		return map[string]any{"phase": phase, "since": since, "pending": pending}
	}
	for name, act := range map[string]map[string]any{
		"event":            {"event": "pause", "timestamp": now, "state": st("running", now, 0)},
		"no timestamp":     {"event": "update", "state": st("running", now, 0)},
		"future timestamp": {"event": "update", "timestamp": now + 7200, "state": st("running", now, 0)},
		"no state":         {"event": "update", "timestamp": now},
		"phase":            {"event": "update", "timestamp": now, "state": st("Deploying prod DB", now, 0)},
		"pending":          {"event": "update", "timestamp": now, "state": st("waiting", now, 100)},
		"negative since":   {"event": "update", "timestamp": now, "state": st("running", -5, 0)},
		"stale far out":    {"event": "update", "timestamp": now, "state": st("running", now, 0), "staleDate": now + 90000},
		"dismissal update": {"event": "update", "timestamp": now, "state": st("running", now, 0), "dismissalDate": now + 60},
		"ref on update":    {"event": "update", "timestamp": now, "state": st("running", now, 0), "ref": "x"},
		"start no ref":     {"event": "start", "timestamp": now, "state": st("running", now, 0), "ws": "w"},
		"start text":       {"event": "start", "timestamp": now, "state": st("running", now, 0), "ws": "w", "ref": "fix the login bug"},
	} {
		if code, out := r.live(key, h, act, nil); code != 400 || out["code"] != ErrBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// a Live Activity handle takes no alert, a device handle no activity;
	// a start goes to the push-to-start handle only, update and end to an
	// activity's only
	sh := r.startChild(parent, strings.Repeat("e5", 80))
	if code, _, _ := r.push(key, h, nil); code != 400 {
		t.Errorf("alert to a Live Activity handle: %d", code)
	}
	if code, _ := r.live(key, parent, r.update("running", now, 0), nil); code != 400 {
		t.Errorf("activity to a device handle: %d", code)
	}
	if code, _ := r.live(key, h, r.start(now), nil); code != 400 {
		t.Errorf("start to an activity's handle: %d", code)
	}
	if code, _ := r.live(key, sh, r.update("running", now, 0), nil); code != 400 {
		t.Errorf("update to the push-to-start handle: %d", code)
	}
	if code, _, _ := r.push(key, sh, nil); code != 400 {
		t.Errorf("alert to the push-to-start handle: %d", code)
	}
	if code, _ := r.live(key, h, r.update("running", now, 0), map[string]any{"collapseId": "c"}); code != 400 {
		t.Errorf("collapseId on a liveactivity push: %d", code)
	}
	if code, _, _ := r.call("POST", "/v1/push", key, map[string]any{"handle": h, "type": "voip", "envelope": envelope()}); code != 400 {
		t.Errorf("unknown push type: %d", code)
	}
	// registration: parent required, a device handle of the same topic/env
	for name, q := range map[string]map[string]string{
		"no parent":       {"apnsToken": laToken, "topic": topic, "env": "production", "pushType": "liveactivity"},
		"child parent":    {"apnsToken": laToken2, "topic": topic, "env": "production", "pushType": "liveactivity", "parent": h},
		"other env":       {"apnsToken": laToken2, "topic": topic, "env": "development", "pushType": "liveactivity", "parent": parent},
		"device + parent": {"apnsToken": tokenOK, "topic": topic, "env": "production", "parent": parent},
		"bad push type":   {"apnsToken": laToken2, "topic": topic, "env": "production", "pushType": "voip", "parent": parent},
		"short la token":  {"apnsToken": "abcd", "topic": topic, "env": "production", "pushType": "liveactivity", "parent": parent},
	} {
		if code, _, _ := r.call("POST", "/v1/handles", "", q); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _, _ := r.call("POST", "/v1/handles", "", map[string]any{"apnsToken": token2, "topic": topic, "env": "production",
		"start": true}); code != 400 {
		t.Errorf("a device handle marked push-to-start: %d", code)
	}
	if code, out := r.child("AAAAAAAAAAAAAAAAAAAAAA", laToken2); code != 404 || out["code"] != ErrHandleUnknown {
		t.Errorf("unknown parent: %d %v", code, out)
	}
	if n := len(r.fake.pushes()); n != 0 {
		t.Fatalf("%d refused pushes reached APNs", n)
	}
}

// A Live Activity handle belongs to its device handle's workspace, both
// ways: the first push to either binds both.
func TestLiveActivityBinding(t *testing.T) {
	r := newRig(t, nil)
	k1, k2 := r.workspace(), r.workspace()
	parent := r.handle(tokenOK)
	h := r.mustChild(parent, laToken)
	if code, out := r.live(k1, h, r.update("running", r.clock().Unix(), 0), nil); code != 200 {
		t.Fatalf("first push: %d %v", code, out)
	}
	if code, _, _ := r.push(k2, parent, nil); code != 403 {
		t.Fatalf("another workspace used the parent a child push bound: %d", code)
	}
	if code, _, _ := r.push(k1, parent, nil); code != 200 {
		t.Fatalf("parent for its own workspace: %d", code)
	}
	// and the other way round
	p2 := r.handle(token2)
	if code, _, _ := r.push(k2, p2, nil); code != 200 {
		t.Fatal(code)
	}
	h2 := r.mustChild(p2, laToken2)
	if code, out := r.live(k1, h2, r.update("running", r.clock().Unix(), 0), nil); code != 403 || out["code"] != ErrHandleBound {
		t.Fatalf("a child of another workspace's device handle: %d %v", code, out)
	}
	if code, _ := r.live(k2, h2, r.update("running", r.clock().Unix(), 0), nil); code != 200 {
		t.Fatalf("its own workspace: %d", code)
	}
}

// Live Activity handles live under their device handle: deleting it, or
// APNs killing its token, takes them along; a dead activity token takes
// only itself; a device handle holds a bounded number; the same token is
// the same handle; all of it survives a restart.
func TestLiveActivityLifecycle(t *testing.T) {
	r := newRig(t, func(c *Config) { c.HandleRate = Rate{PerHour: 36000, Burst: 1000} })
	key := r.workspace()
	parent := r.handle(tokenOK)
	h := r.mustChild(parent, laToken)
	if again := r.mustChild(parent, laToken); again != h {
		t.Fatalf("the same token under the same parent made a second handle")
	}
	// a dead activity token: only that handle goes
	r.fake.answer[laToken] = []answer{{410, "Unregistered"}}
	if code, out := r.live(key, h, r.update("running", r.clock().Unix(), 0), nil); code != 410 || out["code"] != ErrHandleGone {
		t.Fatalf("dead activity token: %d %v", code, out)
	}
	if code, _ := r.live(key, h, r.update("running", r.clock().Unix(), 0), nil); code != 404 {
		t.Fatalf("the dead activity's handle still pushes: %d", code)
	}
	if code, _, _ := r.push(key, parent, nil); code != 200 {
		t.Fatalf("the device handle went with its activity: %d", code)
	}

	// a child of a bound device handle is born bound: retention's
	// unbound rule leaves it alone
	fresh := r.mustChild(parent, laToken2)
	r.advance(DefaultUnboundHandleTTL + time.Hour)
	r.mustChild(parent, strings.Repeat("ee", 60)) // a registration runs the sweep
	if code, _ := r.live(key, fresh, r.update("running", r.clock().Unix(), 0), nil); code != 200 {
		t.Fatalf("a bound parent's unused child was swept: %d", code)
	}

	// the cap: the least recently used goes
	var kids []string
	for i := range maxChildren + 1 {
		r.advance(time.Second)
		kids = append(kids, r.mustChild(parent, strings.Repeat(string("0123456789abcdef"[i%16])+"e", 40)+fmtInt(int64(1000+i))))
	}
	if code, _ := r.live(key, kids[0], r.update("running", r.clock().Unix(), 0), nil); code != 404 {
		t.Fatalf("the oldest child past the cap: %d", code)
	}
	if code, _ := r.live(key, kids[maxChildren], r.update("running", r.clock().Unix(), 0), nil); code != 200 {
		t.Fatalf("the newest child: %d", code)
	}

	// a restart keeps the type, the parent and the index
	cfg := r.relay.cfg
	if err := r.relay.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.relay = s2
	r.srv.Config.Handler = s2
	if code, _ := r.live(key, kids[1], r.update("running", r.clock().Unix(), 0), nil); code != 200 {
		t.Fatalf("a child after the restart: %d", code)
	}
	if code, _, _ := r.push(key, kids[1], nil); code != 400 {
		t.Fatalf("the child's type was lost on reopen: %d", code)
	}
	// deleting the device handle takes its children along
	if code, _, _ := r.call("DELETE", "/v1/handles/"+parent, "", nil); code != 204 {
		t.Fatal(code)
	}
	for _, k := range kids[1:] {
		if code, _ := r.live(key, k, r.update("running", r.clock().Unix(), 0), nil); code != 404 {
			t.Fatalf("a child outlived its deleted parent: %d", code)
		}
	}
	if hs, _ := r.relay.Counts(); hs != 0 {
		t.Fatalf("%d handles left", hs)
	}

	// APNs killing the device token takes the children too
	p2 := r.handle(token2)
	c2 := r.mustChild(p2, laToken2)
	r.fake.answer[token2] = []answer{{410, "Unregistered"}}
	if code, _, _ := r.push(key, p2, nil); code != 410 {
		t.Fatal(code)
	}
	if code, _ := r.live(key, c2, r.update("running", r.clock().Unix(), 0), nil); code != 404 {
		t.Fatalf("a child outlived its dead device token: %d", code)
	}

	// a child made while its device handle was unbound (the app's first
	// launch: the push-to-start handle before any push) is bound by the
	// first alert to the parent, and retention keeps it while the parent
	// is in use — a month of daily alerts, then half a year more
	p3 := r.handle(strings.Repeat("f6", 32))
	st3 := r.startChild(p3, strings.Repeat("a7", 80))
	if code, _, _ := r.push(key, p3, nil); code != 200 {
		t.Fatal(code)
	}
	for range 32 {
		r.advance(24 * time.Hour)
		if code, _, _ := r.push(key, p3, nil); code != 200 {
			t.Fatal(code)
		}
	}
	r.handle(strings.Repeat("0f", 32)) // a registration runs the sweep
	if code, out := r.live(key, st3, r.start(r.clock().Unix()), nil); code != 200 {
		t.Fatalf("the push-to-start handle of a parent bound by its alerts was swept: %d %v", code, out)
	}
	for range 19 { // only the parent is pushed to: 190 more days
		r.advance(10 * 24 * time.Hour)
		if code, _, _ := r.push(key, p3, nil); code != 200 {
			t.Fatal(code)
		}
	}
	r.handle(strings.Repeat("1f", 32))
	if code, out := r.live(key, st3, r.start(r.clock().Unix()), nil); code != 200 {
		t.Fatalf("the push-to-start handle of a parent in use idled out: %d %v", code, out)
	}
	// and one another workspace pushes to is refused, both ways
	if code, out := r.live(r.workspace(), st3, r.start(r.clock().Unix()), nil); code != 403 || out["code"] != ErrHandleBound {
		t.Fatalf("another workspace's start: %d %v", code, out)
	}
	// state from before children were bound with their parent: a child the
	// store holds unbound under a bound parent is kept too
	r.relay.st.mu.Lock()
	r.relay.st.st.Handles[st3].Workspace = ""
	r.relay.st.mu.Unlock()
	r.advance(DefaultUnboundHandleTTL + time.Hour)
	if code, _, _ := r.push(key, p3, nil); code != 200 {
		t.Fatal(code)
	}
	r.handle(strings.Repeat("2f", 32))
	if code, out := r.live(key, st3, r.start(r.clock().Unix()), nil); code != 200 {
		t.Fatalf("an unbound child of a bound parent was swept: %d %v", code, out)
	}
}

// PUT keeps a handle's kind: a Live Activity handle is repointed as one
// (no APNs check), a device handle can't become one.
func TestLiveActivityRepoint(t *testing.T) {
	r := newRig(t, func(c *Config) { c.VerifyTokens = true })
	key, parent := r.workspace(), r.handle(tokenOK)
	h := r.mustChild(parent, laToken)
	checks := len(r.fake.pushes()) // the parent's token check
	if checks != 1 || r.fake.pushes()[0].pushType != "background" {
		t.Fatalf("the parent's token was not checked: %+v", r.fake.pushes())
	}
	body := func(tok, pt string) map[string]string {
		return map[string]string{"apnsToken": tok, "topic": topic, "env": "production", "pushType": pt}
	}
	if code, _, _ := r.call("PUT", "/v1/handles/"+h, "", body(laToken2, "liveactivity")); code != 200 {
		t.Fatalf("repoint a Live Activity handle: %d", code)
	}
	if code, _, _ := r.call("PUT", "/v1/handles/"+h, "", body(tokenOK, "")); code != 400 {
		t.Fatalf("a Live Activity handle repointed as a device handle: %d", code)
	}
	if code, _, _ := r.call("PUT", "/v1/handles/"+parent, "", body(laToken2, "liveactivity")); code != 400 {
		t.Fatalf("a device handle repointed as a Live Activity handle: %d", code)
	}
	if len(r.fake.pushes()) != checks {
		t.Fatalf("an ActivityKit token got a silent check push")
	}
	if code, _ := r.live(key, h, r.update("idle", 0, 0), nil); code != 200 {
		t.Fatal(code)
	}
	if got := r.fake.pushes(); got[len(got)-1].token != laToken2 {
		t.Fatalf("repointed Live Activity handle went to %s", got[len(got)-1].token)
	}
}

// The push-to-start handle is apart from the cards' handles: any number of
// cards never evicts it, a card's end retires its handle (so ended cards
// don't pile up under the parent), and a new push-to-start token replaces
// the old handle.
func TestLiveActivityStartHandleOutlivesCards(t *testing.T) {
	r := newRig(t, func(c *Config) { c.HandleRate = Rate{PerHour: 36000, Burst: 1000} })
	key, parent := r.workspace(), r.handle(tokenOK)
	sh := r.startChild(parent, laToken)
	if again := r.startChild(parent, laToken); again != sh {
		t.Fatal("the same push-to-start token made a second handle")
	}
	tok := func(i int) string { return strings.Repeat("c0", 70) + fmtInt(int64(100000+i)) }
	// more cards than the cap, each updated (xbind keeps them recent) and
	// never ended (the app lost them, xbind restarted, …)
	var cards []string
	for i := range maxChildren + 4 {
		r.advance(time.Minute)
		c := r.mustChild(parent, tok(i))
		if code, out := r.live(key, c, r.update("running", r.clock().Unix(), 0), nil); code != 200 {
			t.Fatalf("card %d: %d %v", i, code, out)
		}
		cards = append(cards, c)
	}
	if code, out := r.live(key, sh, r.start(r.clock().Unix()), nil); code != 200 {
		t.Fatalf("the push-to-start handle after %d cards: %d %v", len(cards), code, out)
	}
	for i, c := range cards {
		want := 200
		if i < 4 {
			want = 404 // the cap evicted the oldest cards
		}
		if code, _ := r.live(key, c, r.update("waiting", r.clock().Unix(), 1), nil); code != want {
			t.Fatalf("card %d: %d, want %d", i, code, want)
		}
	}
	// an end retires a card's handle
	for _, c := range cards[4:] {
		end := map[string]any{"event": "end", "timestamp": r.clock().Unix(), "state": map[string]any{"phase": "idle", "since": 0, "pending": 0}}
		if code, out := r.live(key, c, end, nil); code != 200 {
			t.Fatalf("end: %d %v", code, out)
		}
	}
	if hs, _ := r.relay.Counts(); hs != 2 {
		t.Fatalf("%d handles after every card ended, want the parent and its push-to-start handle", hs)
	}
	// a legacy push-to-start handle (registered as a card's) is made one by
	// registering its token as push-to-start: the same handle
	legacy := r.mustChild(parent, laToken2)
	if got := r.startChild(parent, laToken2); got != legacy {
		t.Fatal("re-registering a token as push-to-start made a new handle")
	}
	// … and one push-to-start handle per parent: the old one went
	if code, _ := r.live(key, sh, r.start(r.clock().Unix()), nil); code != 404 {
		t.Fatalf("the replaced push-to-start handle: %d", code)
	}
	if code, out := r.live(key, legacy, r.start(r.clock().Unix()), nil); code != 200 {
		t.Fatalf("the new push-to-start handle: %d %v", code, out)
	}
	// the kind survives a restart
	cfg := r.relay.cfg
	if err := r.relay.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.relay = s2
	r.srv.Config.Handler = s2
	if code, _ := r.live(key, legacy, r.update("running", r.clock().Unix(), 0), nil); code != 400 {
		t.Fatalf("the push-to-start kind was lost on reopen: %d", code)
	}
	if code, _ := r.live(key, legacy, r.start(r.clock().Unix()), nil); code != 200 {
		t.Fatalf("start after the reopen: %d", code)
	}
}
