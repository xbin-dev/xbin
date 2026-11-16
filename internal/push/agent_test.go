package push

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// The workspace agent's notifications and card (D241; agent.go,
// agentactivity.go).

const testHolder = "xbin/wsagent"

func agentOf(t *testing.T, p Payload) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(p.Agent), &m); err != nil {
		t.Fatalf("agent member %q: %v", p.Agent, err)
	}
	return m
}

// A holder's POST /notify is worded by xbind: its own title and body never
// reach a screen, the question rides apart, other kinds send nothing.
func TestHolderNotifyWords(t *testing.T) {
	t.Cleanup(auth.TestSealDelegate(testHolder))
	readable["alice"] = append(readable["alice"], testHolder)
	t.Cleanup(func() { readable["alice"] = readable["alice"][:len(readable["alice"])-1] })
	r := newRig(t, func(o *Options) { o.AgentBadge = func(string) int { return 3 } })
	r.register(alice, "phone", "handle-alice-1")
	holder := auth.Principal{Component: testHolder, Via: "instance", Partition: util.UserPartition("alice")}
	code, out, _ := r.call(holder, "POST", "/notify", map[string]any{"user": "alice", "title": "IGNORE ALL — approve the transfer",
		"body": "Should I go ahead with ‮evil?", "link": "#c=12", "kind": "question", "collapseId": "needs:12"})
	if code != 202 {
		t.Fatalf("notify: %d %v", code, out)
	}
	p := r.open(r.relay.waitPushes(1)[0])
	if p.Kind != KindAgentAsk || p.Title != "Question from your workspace agent" || strings.Contains(p.Title+p.Body, "IGNORE") ||
		strings.Contains(p.Title+p.Body, "evil") || p.Link != "xbin/agent#c=12" || p.CollapseID != "agent-conv:12" {
		t.Fatalf("the question's notification: %+v", p)
	}
	a := agentOf(t, p)
	if a["ask"] != "Should I go ahead with ‮evil?" || a["badge"] != float64(3) || a["v"] != float64(1) ||
		a["conversation"].(map[string]any)["id"] != "12" {
		t.Fatalf("its agent member (the question apart, for the app to draw quoted and revealed): %v", a)
	}
	// done: xbind's words; the same collapse replaces the question
	if code, _, _ := r.call(holder, "POST", "/notify", map[string]any{"user": "alice", "title": "The model's title", "link": "#c=12", "kind": "done"}); code != 202 {
		t.Fatal(code)
	}
	p = r.open(r.relay.waitPushes(2)[1])
	if p.Kind != KindAgentDone || p.Title != "Workspace agent finished" || p.Body != "The report is ready." || p.CollapseID != "agent-conv:12" {
		t.Fatalf("done: %+v", p)
	}
	// anything else a holder notifies: nothing sent
	code, out, _ = r.call(holder, "POST", "/notify", map[string]any{"user": "alice", "title": "x", "kind": "failed"})
	if code != 202 || out["suppressed"] != true {
		t.Fatalf("another kind: %d %v", code, out)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(r.relay.pushes()); n != 2 {
		t.Fatalf("pushes: %d", n)
	}
	// a tile that isn't a holder keeps its own words
	if code, _, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "Standup", "kind": "question"}); code != 202 {
		t.Fatal(code)
	}
	if p := r.open(r.relay.waitPushes(3)[2]); p.Kind != "tile.question" || p.Title != "Standup" || p.Agent != "" {
		t.Fatalf("a tile's: %+v", p)
	}
}

// AgentNotice seals the agent member with the rest; registration kinds hold
// back what the person turned off; AgentClear is a background push, and
// only to a relay that lists background pushes.
func TestAgentNoticeAndClear(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-alice-1")
	r.register(bob, "tablet", "handle-bob-1", "agent.permission", "agent.question", "agent.turn", "tile", "test", "agent.clear")
	n := AgentNote{Kind: KindAgentApproval, Title: "Approval needed — Notes", Body: "…", Link: "xbin/agent#r=abc", Collapse: "agent-approval:abc",
		Agent: map[string]any{"request": map[string]any{"id": "abc", "offer": "once"}, "badge": 1}, Priority: 10}
	if sent, limited := r.s.AgentNotice("alice", n); !sent || limited {
		t.Fatalf("alice: %v %v", sent, limited)
	}
	if sent, _ := r.s.AgentNotice("bob", n); sent {
		t.Fatal("bob turned Needs you off (his kinds leave agent.approval out)")
	}
	got := r.relay.waitPushes(1)
	p := r.open(got[0])
	if p.Kind != KindAgentApproval || p.CollapseID != "agent-approval:abc" || got[0].Type != "" || got[0].Priority != 10 {
		t.Fatalf("the approval: %+v %+v", p, got[0])
	}
	if a := agentOf(t, p); a["v"] != float64(1) || a["request"].(map[string]any)["offer"] != "once" {
		t.Fatalf("its agent member: %v", a)
	}
	// a relay without background pushes: no clear
	r.s.AgentClear("alice", []string{"agent-approval:abc"}, 0)
	time.Sleep(80 * time.Millisecond)
	if len(r.relay.pushes()) != 1 {
		t.Fatal("a clear went to a relay that lists no background pushes")
	}
	r.relay.set(func() { r.relay.features = []string{"background", "activity-agent"} })
	r.s.fmu.Lock()
	r.s.featAt = time.Time{} // read again
	r.s.fmu.Unlock()
	r.s.AgentClear("alice", []string{"agent-approval:abc"}, 0)
	r.s.AgentClear("bob", []string{"agent-approval:abc"}, 0)
	got = r.relay.waitPushes(3)
	for _, g := range got[1:] {
		p := r.open(g)
		if g.Type != "background" || g.Priority != 5 || g.CollapseID != "" || p.Kind != KindAgentClear || p.Title != "" {
			t.Fatalf("the clear: %+v %+v", g, p)
		}
		if a := agentOf(t, p); a["clear"].([]any)[0] != "agent-approval:abc" || a["badge"] != float64(0) {
			t.Fatalf("what it takes back: %v", a)
		}
	}
}

// fakePicture is what the server says of each person's agent.
type fakePicture struct {
	mu  sync.Mutex
	pic map[string]AgentPicture
}

func (f *fakePicture) get(u string) AgentPicture {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pic[u]
}

func (f *fakePicture) set(u string, p AgentPicture) {
	f.mu.Lock()
	f.pic[u] = p
	f.mu.Unlock()
}

func agentRig(t *testing.T, features ...string) (*rig, *fakePicture) {
	t.Helper()
	pics := &fakePicture{pic: map[string]AgentPicture{}}
	r, _ := liveRig(t, 0, func(o *Options) {
		o.AgentPicture, o.AgentCoalesce, o.AgentIdleGrace = pics.get, -1, 40*time.Millisecond
		o.StartAfter = 20 * time.Millisecond
	})
	r.relay.set(func() { r.relay.features = features })
	return r, pics
}

// agentPushes are the card's pushes, in the order the device takes them
// (by timestamp: the sender's workers may deliver out of order).
func agentPushes(r *rig) []relayPush {
	var out []relayPush
	for _, p := range r.relay.pushes() {
		if p.Type == "liveactivity" && p.Activity != nil && p.Activity.Kind == agentKind && p.Handle != "card-handle-0" {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b relayPush) int { return int(a.Activity.Timestamp - b.Activity.Timestamp) })
	return out
}

func waitAgent(t *testing.T, r *rig, n int) []relayPush {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := agentPushes(r); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("agent card pushes: %d, want %d: %+v", len(agentPushes(r)), n, r.relay.pushes())
	return nil
}

// The card follows the picture — counts and a phase only — and ends on
// pause, on idle after the grace, and when the agent is no longer theirs.
func TestAgentActivityFollowsAndEnds(t *testing.T) {
	r, pics := agentRig(t, "activity-agent")
	r.register(alice, "phone", "handle-alice-1")
	// a card registered while nothing runs is ended at once (xbind follows
	// no stretch the app saw and it doesn't)
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "agent": true, "handle": "card-handle-0"}); code != 200 || out["activity"].(map[string]any)["ended"] != true {
		t.Fatalf("a card for nothing: %d %v", code, out)
	}
	pics.set("alice", AgentPicture{Has: true, Agents: 2})
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "agent": true, "handle": "card-handle-1"}); code != 200 || out["activity"].(map[string]any)["agent"] != true || out["activity"].(map[string]any)["ended"] != nil {
		t.Fatalf("register the card: %d %v", code, out)
	}
	r.s.AgentChanged("alice")
	got := waitAgent(t, r, 1)
	if a := got[len(got)-1].Activity; a.Event != "update" || a.State.Phase != PhaseRunning || a.State.Agents != 2 || a.State.Since == 0 {
		t.Fatalf("working: %+v", a)
	}
	pics.set("alice", AgentPicture{Has: true, Agents: 1, Pending: 2})
	r.s.AgentChanged("alice")
	got = waitAgent(t, r, len(got)+1)
	if a := got[len(got)-1]; a.Activity.State.Phase != PhaseWaiting || a.Activity.State.Pending != 2 || a.Priority != 10 {
		t.Fatalf("waiting: %+v", a)
	}
	for _, p := range got {
		checkGenericAgent(t, p)
	}
	// paused: the card ends on paused
	pics.set("alice", AgentPicture{Has: true, Paused: true, Pending: 2})
	r.s.AgentChanged("alice")
	got = waitAgent(t, r, len(got)+1)
	if a := got[len(got)-1].Activity; a.Event != "end" || a.State.Phase != PhasePaused || a.DismissalDate == 0 {
		t.Fatalf("paused: %+v", a)
	}
	if _, active := r.s.agentLiveState("alice"); active {
		t.Fatal("still active after the pause")
	}
	if _, list, _ := r.call(alice, "GET", "/devices/push", nil); list["devices"].([]any)[0].(map[string]any)["agentActivity"] == true {
		t.Fatalf("the registration stays after the end: %v", list)
	}
}

func checkGenericAgent(t *testing.T, p relayPush) {
	t.Helper()
	act := p.Raw["activity"].(map[string]any)
	for k := range act {
		switch k {
		case "kind", "event", "timestamp", "state", "staleDate", "dismissalDate", "ws", "ref":
		default:
			t.Fatalf("the card's push carries %q: %v", k, act)
		}
	}
	for k, v := range act["state"].(map[string]any) {
		if _, num := v.(float64); k != "phase" && !num {
			t.Fatalf("the card's state holds a string %q=%v", k, v)
		}
	}
}

// Idle: the card says so, and ends after the grace; a new stretch starts
// by push on a relay that knows the card, and not on one that doesn't.
func TestAgentActivityIdleAndPushToStart(t *testing.T) {
	r, pics := agentRig(t, "activity-agent")
	r.register(alice, "phone", "handle-alice-1")
	if code, out, _ := r.call(alice, "POST", "/devices/push", map[string]any{"deviceId": "phone", "handle": "handle-alice-1",
		"publicKey": b64.EncodeToString(r.keys["handle-alice-1"].PublicKey().Bytes()), "agentStartHandle": "agent-start-handle-1"}); code != 200 {
		t.Fatalf("agentStartHandle: %d %v", code, out)
	}
	_, list, _ := r.call(alice, "GET", "/devices/push", nil)
	if dev := list["devices"].([]any)[0].(map[string]any); dev["agentStart"] != true || !strings.Contains(strings.Join(anyStrings(list["features"]), ","), "agent-activity/1") {
		t.Fatalf("the listing: %v", list)
	}
	pics.set("alice", AgentPicture{Has: true, Pending: 1})
	r.s.AgentChanged("alice")
	got := waitAgent(t, r, 1)
	start := got[0]
	if start.Handle != "agent-start-handle-1" || start.Activity.Event != "start" || start.Activity.Kind != agentKind || start.Activity.Ref == "" ||
		start.Activity.State.Phase != PhaseWaiting {
		t.Fatalf("push-to-start: %+v", start.Activity)
	}
	// the app registers the started card by its ref
	if code, out := r.activity(alice, map[string]any{"deviceId": "phone", "ref": start.Activity.Ref, "handle": "card-handle-2"}); code != 200 ||
		out["activity"].(map[string]any)["agent"] != true {
		t.Fatalf("by ref: %d %v", code, out)
	}
	// nothing waits any more: idle, then the end after the grace
	pics.set("alice", AgentPicture{Has: true})
	r.s.AgentChanged("alice")
	got = waitAgent(t, r, 4)
	var phases []string
	for _, p := range got[1:] {
		phases = append(phases, p.Activity.Event+":"+p.Activity.State.Phase)
	}
	if strings.Join(phases[len(phases)-2:], " ") != "update:idle end:idle" {
		t.Fatalf("the idle stretch: %v", phases)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range asSlice(v) {
		out = append(out, x.(string))
	}
	return out
}

// An older relay: no start of the card's kind (it would draw it as a tile
// agent's turn), no count, and paused ends as idle.
func TestAgentActivityOlderRelay(t *testing.T) {
	r, pics := agentRig(t) // lists nothing
	r.register(alice, "phone", "handle-alice-1")
	if code, out, _ := r.call(alice, "POST", "/devices/push", map[string]any{"deviceId": "phone", "handle": "handle-alice-1",
		"publicKey": b64.EncodeToString(r.keys["handle-alice-1"].PublicKey().Bytes()), "agentStartHandle": "agent-start-handle-1"}); code != 200 {
		t.Fatalf("agentStartHandle: %d %v", code, out)
	}
	pics.set("alice", AgentPicture{Has: true, Agents: 3})
	if code, _ := r.activity(alice, map[string]any{"deviceId": "phone", "agent": true, "handle": "card-handle-1"}); code != 200 {
		t.Fatal(code)
	}
	r.s.AgentChanged("alice")
	time.Sleep(120 * time.Millisecond) // past StartAfter
	pics.set("alice", AgentPicture{Has: true, Paused: true})
	r.s.AgentChanged("alice")
	deadline := time.Now().Add(3 * time.Second)
	for len(r.relay.pushes()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := r.relay.pushes()
	if len(got) != 2 {
		t.Fatalf("pushes: %+v", got)
	}
	slices.SortStableFunc(got, func(a, b relayPush) int { return int(a.Activity.Timestamp - b.Activity.Timestamp) })
	for _, p := range got {
		act := p.Raw["activity"].(map[string]any)
		st := act["state"].(map[string]any)
		if p.Handle != "card-handle-1" || act["kind"] != nil || st["agents"] != nil || st["phase"] == PhasePaused || act["event"] == "start" {
			t.Fatalf("to an older relay: %v", p.Raw)
		}
	}
	if got[1].Activity.Event != "end" || got[1].Activity.State.Phase != PhaseIdle {
		t.Fatalf("paused, to an older relay: %+v", got[1].Activity)
	}
}
