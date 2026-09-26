package push

import (
	"crypto/ecdh"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// accounts is a mutable Options.Account for tests.
type accounts struct {
	mu sync.Mutex
	m  map[string]Account
}

func (a *accounts) get(user string) Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m[user]
}

func (a *accounts) set(user string, acct Account) {
	a.mu.Lock()
	a.m[user] = acct
	a.mu.Unlock()
}

// Signing a user out everywhere (or disabling them) drops their
// registrations and keeps their preferences; deleting them drops both. A
// disabled user gets nothing, an unknown user's registrations go, and a
// reused id does not inherit the earlier account's devices.
func TestSignOutDisableDeleteAndIDReuse(t *testing.T) {
	acc := &accounts{m: map[string]Account{"alice": {Exists: true, Created: 100}, "bob": {Exists: true, Created: 100}}}
	r := newRig(t, func(o *Options) { o.Account = acc.get })
	r.register(alice, "phone", "handle-alice")
	r.register(bob, "phone", "handle-bob")
	r.call(alice, "PUT", "/push/prefs", map[string]any{"mutedTiles": []string{"apps/x"}})
	if n := r.s.SignedOut("alice"); n != 1 || len(r.s.st.devices("alice")) != 0 || len(r.s.st.prefs("alice").MutedTiles) != 1 {
		t.Fatalf("signed out: %d removed, prefs %v", n, r.s.st.prefs("alice"))
	}
	r.register(alice, "phone", "handle-alice")
	r.s.ForgetUser("alice")
	if len(r.s.st.devices("alice")) != 0 || len(r.s.st.prefs("alice").MutedTiles) != 0 {
		t.Fatal("a deleted user's registrations or prefs stayed")
	}

	// disabled: agent events and tile notifications reach nothing, the
	// registration stays (re-enabling restores it)
	acc.set("bob", Account{Exists: true, Disabled: true, Created: 100})
	r.s.AgentEvent("bob", "s1", "apps/other", agentTurn())
	r.call(other, "POST", "/notify", map[string]any{"user": "bob", "title": "x"})
	time.Sleep(30 * time.Millisecond)
	if n := len(r.relay.pushes()); n != 0 {
		t.Fatalf("a disabled user got %d pushes", n)
	}
	acc.set("bob", Account{Exists: true, Created: 100})
	r.s.AgentEvent("bob", "s1", "apps/other", agentTurn())
	r.relay.waitPushes(1)

	// gone without the hook (edited offline): dropped at the next delivery
	acc.set("bob", Account{})
	r.s.enqueue(note{user: "bob", kind: KindTest, title: "x"})
	eventually(t, "the unknown user's registrations to go", func() bool { return len(r.s.st.devices("bob")) == 0 })

	// the id is reused by a new account: the old account's phone is not theirs
	r.register(bob, "old-phone", "handle-old")
	acc.set("bob", Account{Exists: true, Created: time.Now().Unix() + 10})
	r.s.enqueue(note{user: "bob", kind: KindTest, title: "for the new bob"})
	eventually(t, "the earlier account's registration to go", func() bool { return len(r.s.st.devices("bob")) == 0 })
	if n := len(r.relay.pushes()); n != 1 {
		t.Fatalf("the new account's push reached the old phone: %d pushes", n)
	}
}

// Admins list and revoke any user's registrations (a lost phone).
func TestAdminDeviceRoutes(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-alice-1")
	r.register(alice, "ipad", "handle-alice-2")
	r.register(bob, "phone", "handle-bob-1")
	for _, c := range []struct{ m, path string }{{"GET", "/push/devices"}, {"DELETE", "/push/devices/bob"}, {"DELETE", "/push/devices/bob/phone"}} {
		if code, _, _ := r.call(alice, c.m, c.path, nil); code != 403 {
			t.Fatalf("non-admin %s %s: %d", c.m, c.path, code)
		}
	}
	if code, out, _ := r.call(owner, "GET", "/push/devices", nil); code != 200 || len(out["devices"].([]any)) != 3 {
		t.Fatalf("all: %d %v", code, out)
	}
	code, out, _ := r.call(owner, "GET", "/push/devices?user=alice", nil)
	if devs := out["devices"].([]any); code != 200 || len(devs) != 2 || devs[0].(map[string]any)["user"] != "alice" || devs[0].(map[string]any)["handle"] != nil {
		t.Fatalf("alice's: %d %v", code, out)
	}
	if code, out, _ := r.call(owner, "DELETE", "/push/devices/alice/ipad", nil); code != 200 || out["removed"] != float64(1) {
		t.Fatalf("one: %d %v", code, out)
	}
	if code, out, _ := r.call(owner, "DELETE", "/push/devices/bob", nil); code != 200 || out["removed"] != float64(1) {
		t.Fatalf("all of bob's: %d %v", code, out)
	}
	if code, _, _ := r.call(owner, "DELETE", "/push/devices/bob", nil); code != 404 {
		t.Fatalf("again: %d", code)
	}
	if len(r.s.st.devices("alice")) != 1 || len(r.s.st.devices("bob")) != 0 {
		t.Fatal("revoke removed the wrong registrations")
	}
}

// Tiles — muted ones included — cannot spend the budget agent sessions use:
// muted notifications charge nothing, tiles share a per-user budget of
// their own, and a permission request still gets through.
func TestTilesCannotStarveAgentPushes(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Limits = Limits{Tile: Rate{PerHour: 1, Burst: 1000}, User: Rate{PerHour: 1, Burst: 3},
			Agent: Rate{PerHour: 1, Burst: 3}, Session: Rate{PerHour: 1, Burst: 10}, Test: Rate{PerHour: 1, Burst: 1}}
	})
	r.register(alice, "phone", "handle-phone")
	r.call(alice, "PUT", "/push/prefs", map[string]any{"mutedTiles": []string{"apps/cal", "apps/other"}})
	for i := 0; i < 40; i++ {
		for _, p := range []auth.Principal{cal, other} {
			if code, _, _ := r.call(p, "POST", "/notify", map[string]any{"user": "alice", "title": "noise"}); code != 202 {
				t.Fatalf("muted notify: %d", code)
			}
		}
	}
	if ok, _ := r.s.user.allow("alice"); !ok {
		t.Fatal("muted tiles spent the user's budget")
	}
	// unmuted tiles spend their shared budget (2 left), then drop quietly
	r.call(alice, "PUT", "/push/prefs", map[string]any{"mutedTiles": []string{}})
	for i := 0; i < 10; i++ {
		if code, _, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "tile"}); code != 202 {
			t.Fatalf("notify %d: %d", i, code)
		}
	}
	r.relay.waitPushes(2)
	r.s.AgentEvent("alice", "s1", "apps/cal", agent.New(agent.EvPermissionRequest, map[string]any{"pid": "p1", "toolCall": map[string]any{"title": "rm -rf build"}}))
	got := r.relay.waitPushes(3)
	if p := r.open(got[2]); p.Kind != KindAgentPermission {
		t.Fatalf("the permission request did not get through: %+v", p)
	}
	// and with push reaching no device, nothing is charged either
	r2 := newRig(t, func(o *Options) { o.Limits = Limits{User: Rate{PerHour: 1, Burst: 1}} })
	for i := 0; i < 5; i++ {
		r2.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "nobody listens"})
	}
	r2.register(alice, "phone", "handle-phone")
	r2.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "heard"})
	if p := r2.open(r2.relay.waitPushes(1)[0]); p.Title != "heard" {
		t.Fatalf("got %+v", p)
	}
}

// A tile's frontend (frame token) or a shell in it (terminal token) may
// notify only the person using it; the backend (instance token) notifies
// any reader.
func TestNotifyFromFrontends(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-alice")
	r.register(bob, "phone", "handle-bob")
	bobFrame := auth.Principal{Component: "apps/other", UserID: "bob", Via: "frame"}
	bobShell := auth.Principal{Component: "apps/other", UserID: "bob", Via: "terminal"}
	ownerFrame := auth.Principal{Component: "apps/other", Via: "frame"}
	cronish := auth.Principal{Component: "xbin/cron", Via: "cron"}
	for _, c := range []struct {
		p    auth.Principal
		user string
		want int
	}{
		{bobFrame, "alice", 403}, // alice reads apps/other, but bob's frontend cannot notify her
		{bobShell, "alice", 403},
		{ownerFrame, "alice", 403},
		{cronish, "alice", 403},
		{bobFrame, "bob", 202},
		{bobShell, "user:bob", 202},
		{ownerFrame, OwnerUser, 202},
		{other, "alice", 202}, // the backend
	} {
		if code, out, _ := r.call(c.p, "POST", "/notify", map[string]any{"user": c.user, "title": "x"}); code != c.want {
			t.Errorf("%s (%s) → %s: %d %v, want %d", c.p.From(), c.p.Via, c.user, code, out, c.want)
		}
	}
}

// Registrations are bound to the login that made them (review). A device
// session registers under its own device id only — a stolen phone's
// session can't plant "x" with an attacker's handle that survives removing
// the device. Any other login (a browser cookie, the app before enrolling)
// holds its registrations only while it lives: after its logout the
// attacker's handle gets nothing and the registration is gone. And no such
// login can take over an enrolled device's registration.
func TestRegistrationsFollowTheirLogin(t *testing.T) {
	var mu sync.Mutex
	dead := map[string]bool{}
	r := newRig(t, func(o *Options) {
		o.Live = func(user, gen string) bool { mu.Lock(); defer mu.Unlock(); return !dead[gen] }
	})
	phone := auth.Principal{UserID: "alice", Via: "device", DeviceID: "dev-phone", Gen: "s.phone"}
	k, _ := ecdh.X25519().GenerateKey(nil)
	pub := b64.EncodeToString(k.PublicKey().Bytes())
	r.register(phone, "dev-phone", "handle-phone")
	if code, out, _ := r.call(phone, "POST", "/devices/push", map[string]any{"deviceId": "x", "handle": "handle-attacker",
		"publicKey": pub}); code != 403 {
		t.Fatalf("a device session registering another id: %d %v", code, out)
	}
	// A browser session of alice's (a stolen cookie) plants "zz" with its
	// own handle; it can't take over the phone's.
	cookie := auth.Principal{UserID: "alice", Via: "session", Gen: "s.cookie"}
	r.register(cookie, "zz", "handle-attacker")
	if code, out, _ := r.call(cookie, "POST", "/devices/push", map[string]any{"deviceId": "dev-phone", "handle": "handle-attacker2",
		"publicKey": pub}); code != 409 {
		t.Fatalf("a browser session taking over the device's registration: %d %v", code, out)
	}
	// Removing the device drops its registration; the browser's "zz"
	// lives while that login does…
	if !r.s.ForgetDevice("alice", "dev-phone") {
		t.Fatal("the device's registration")
	}
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "one"})
	if got := r.relay.waitPushes(1); got[0].Handle != "handle-attacker" {
		t.Fatalf("delivered to %s", got[0].Handle)
	}
	// …and not a moment longer: the browser session logs out.
	mu.Lock()
	dead["s.cookie"] = true
	mu.Unlock()
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "secret"})
	time.Sleep(50 * time.Millisecond)
	if n := len(r.relay.pushes()); n != 1 {
		t.Fatalf("a signed-out login's registration still received pushes (%d)", n)
	}
	if code, out, _ := r.call(alice, "GET", "/devices/push", nil); code != 200 || len(out["devices"].([]any)) != 0 {
		t.Fatalf("the dead login's registration is listed: %v", out)
	}
	if len(r.s.st.devices("alice")) != 0 {
		t.Fatal("the dead login's registration stayed in the store")
	}
	// The device session re-registering its own id is fine, as often as it likes.
	r.register(phone, "dev-phone", "handle-phone")
	r.register(phone, "dev-phone", "handle-phone-2")
	if d := r.s.st.devices("alice"); len(d) != 1 || d[0].Session != "" || d[0].Handle != "handle-phone-2" {
		t.Fatalf("device registration: %+v", d)
	}
}

// A reader holding a shared tile's frame (or terminal) token can't spend
// the tile's /notify budget: frontends notify only their own person, from
// a bucket per tile and person, so the backend's notifications to everyone
// keep flowing (review).
func TestFrontendsCannotExhaustTheBackendsBudget(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Limits = DefaultLimits
		o.Limits.Tile = Rate{PerHour: 1, Burst: 3}
	})
	r.register(alice, "phone", "handle-alice")
	bobFrame := auth.Principal{Component: "apps/other", UserID: "bob", Via: "frame"}
	bobShell := auth.Principal{Component: "apps/other", UserID: "bob", Via: "terminal"}
	for i := 0; i < 3; i++ {
		if code, _, _ := r.call(bobFrame, "POST", "/notify", map[string]any{"user": "bob", "title": "spam"}); code != 202 {
			t.Fatalf("bob's frame %d: %d", i, code)
		}
	}
	for _, p := range []auth.Principal{bobFrame, bobShell} {
		if code, _, _ := r.call(p, "POST", "/notify", map[string]any{"user": "bob", "title": "spam"}); code != 429 {
			t.Fatalf("bob's %s over his bucket: %d", p.Via, code)
		}
	}
	if code, out, _ := r.call(other, "POST", "/notify", map[string]any{"user": "alice", "title": "for alice"}); code != 202 {
		t.Fatalf("the backend after a reader's spam: %d %v", code, out)
	}
	if p := r.open(r.relay.waitPushes(1)[0]); p.Title != "for alice" {
		t.Fatalf("got %+v", p)
	}
}

// One person can't turn their budgets into many times as many relay
// posts by registering many devices (review): User, Agent and Test count a
// post per device, and registering is itself limited.
func TestBudgetsCountRelayPosts(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Limits = Limits{User: Rate{PerHour: 1, Burst: 6}, Agent: Rate{PerHour: 1, Burst: 4}, Test: Rate{PerHour: 1, Burst: 4},
			Register: Rate{PerHour: 1, Burst: 4}}
	})
	for _, d := range []string{"a", "b", "c"} {
		r.register(alice, "dev-"+d, "handle-alice-"+d)
	}
	// tiles: 3 devices × 2 notes fill the 6-post budget; the third is dropped
	for i := 0; i < 3; i++ {
		if code, _, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "x"}); code != 202 {
			t.Fatalf("notify %d: %d", i, code)
		}
	}
	r.relay.waitPushes(6)
	time.Sleep(30 * time.Millisecond)
	if n := len(r.relay.pushes()); n != 6 || r.s.snd.stats().Limited != 1 {
		t.Fatalf("%d relay posts (limited %d), want 6 (1)", n, r.s.snd.stats().Limited)
	}
	// agent pushes: a budget of their own, per post too
	r.s.AgentEvent("alice", "s1", "apps/cal", agentTurn())
	r.relay.waitPushes(9)
	r.s.AgentEvent("alice", "s2", "apps/cal", agentTurn())
	time.Sleep(30 * time.Millisecond)
	if n := len(r.relay.pushes()); n != 9 {
		t.Fatalf("the second agent push went out: %d posts", n)
	}
	// the test route: 3 posts, then 429
	if code, out, _ := r.call(alice, "POST", "/push/test", nil); code != 202 || out["devices"] != float64(3) {
		t.Fatalf("test: %d %v", code, out)
	}
	if code, _, _ := r.call(alice, "POST", "/push/test", nil); code != 429 {
		t.Fatalf("second test: %d", code)
	}
	// registering: 3 above + 1 more, then 429
	r.register(alice, "dev-d", "handle-alice-d")
	k, _ := ecdh.X25519().GenerateKey(nil)
	if code, _, _ := r.call(alice, "POST", "/devices/push", map[string]any{"deviceId": "dev-e", "handle": "handle-alice-e",
		"publicKey": b64.EncodeToString(k.PublicKey().Bytes())}); code != 429 {
		t.Fatalf("registering past the limit: %d", code)
	}
}
