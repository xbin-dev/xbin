package push

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
)

// eventually waits for cond (the sender is asynchronous).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// adminRig is a rig whose relay an admin configures (no environment key).
func adminRig(t *testing.T) *rig {
	return newRig(t, func(o *Options) { o.RelayURL, o.RelayKey = "", "" })
}

func agentTurn() agent.Event {
	return agent.New(agent.EvTurnEnd, map[string]any{"turn": 1, "stopReason": "end_turn"})
}

func TestAdminConfig(t *testing.T) {
	r := adminRig(t)
	relay := r.relay
	if code, _, _ := r.call(alice, "GET", "/push/config", nil); code != 403 {
		t.Fatalf("non-admin: %d", code)
	}
	code, out, _ := r.call(owner, "GET", "/push/config", nil)
	if code != 200 || out["enabled"] != false {
		t.Fatalf("off: %d %v", code, out)
	}
	// push off: notify still answers 202, nothing is sent
	r.register(alice, "phone", "handle-phone")
	if code, _, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "x"}); code != 202 {
		t.Fatal(code)
	}
	if code, _, _ := r.call(alice, "POST", "/push/test", nil); code != 409 {
		t.Fatalf("test while off: %d", code)
	}
	for _, bad := range []string{"http://relay.example", "ftp://x", "not a url", "https://u:p@relay.example"} {
		if code, _, _ := r.call(owner, "PUT", "/push/config", map[string]any{"relay": bad}); code != 400 {
			t.Fatalf("%q accepted: %d", bad, code)
		}
	}
	code, out, _ = r.call(owner, "PUT", "/push/config", map[string]any{"relay": relay.srv.URL + "/"})
	if code != 200 || out["enabled"] != true || out["source"] != "admin" || out["relayWorkspace"] != "wsid" || out["keySet"] != true || relay.regs != 1 {
		t.Fatalf("opt in: %d %v (regs %d)", code, out, relay.regs)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), "xbr_fresh") {
		t.Fatal("the relay key is shown")
	}
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "on"})
	if got := relay.waitPushes(1); got[0].Key != "xbr_fresh" {
		t.Fatalf("pushed with key %q", got[0].Key)
	}
	// configured by the environment: read-only
	e := newRig(t, nil)
	for _, m := range []string{"PUT", "DELETE"} {
		if code, _, _ := e.call(owner, m, "/push/config", map[string]any{"relay": relay.srv.URL}); code != 409 {
			t.Fatalf("env-configured %s: %d", m, code)
		}
	}
	if code, out, _ := e.call(owner, "GET", "/push/config", nil); code != 200 || out["source"] != "env" || out["keySet"] != nil {
		t.Fatalf("env view: %v", out)
	}
	// URL alone in the environment: the default an opt-in uses
	d := newRig(t, func(o *Options) { o.RelayURL, o.RelayKey = relay.srv.URL, "" })
	if code, out, _ := d.call(owner, "PUT", "/push/config", nil); code != 200 || out["relay"] != relay.srv.URL {
		t.Fatalf("default relay: %d %v", code, out)
	}
}

// Off and on again reuses the relay key: every registration keeps working
// and no relay workspace is minted. (The review's high finding: off/on used
// to mint a new relay workspace, the relay answered 403 for every handle,
// and xbind deleted every registration.)
func TestConfigOffOnKeepsRegistrations(t *testing.T) {
	r := adminRig(t)
	if code, out, _ := r.call(owner, "PUT", "/push/config", map[string]any{"relay": r.relay.srv.URL}); code != 200 {
		t.Fatalf("on: %d %v", code, out)
	}
	r.register(alice, "phone", "handle-phone")
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "one"})
	r.relay.waitPushes(1)
	if code, _, _ := r.call(owner, "DELETE", "/push/config", nil); code != 204 || r.s.Enabled() {
		t.Fatalf("off: %d", code)
	}
	code, out, _ := r.call(owner, "GET", "/push/config", nil)
	if code != 200 || out["enabled"] != false || out["keySet"] != true || out["relay"] != r.relay.srv.URL {
		t.Fatalf("off keeps the configuration: %v", out)
	}
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "while off"})
	for i := 0; i < 2; i++ { // on, and PUT again while on
		if code, out, _ := r.call(owner, "PUT", "/push/config", map[string]any{}); code != 200 || out["enabled"] != true {
			t.Fatalf("on again: %d %v", code, out)
		}
	}
	if r.relay.regs != 1 || r.relay.probes != 3 { // the mint's own probe, then one per PUT
		t.Fatalf("on again minted a relay workspace: regs %d probes %d", r.relay.regs, r.relay.probes)
	}
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "two"})
	got := r.relay.waitPushes(2)
	if got[1].Key != "xbr_fresh" || r.open(got[1]).Title != "two" {
		t.Fatalf("after off/on: %+v", got[1])
	}
	if code, out, _ := r.call(alice, "GET", "/devices/push", nil); code != 200 || len(out["devices"].([]any)) != 1 ||
		out["devices"].([]any)[0].(map[string]any)["needsNewHandle"] != nil {
		t.Fatalf("the registration after off/on: %v", out)
	}
	// the relay forgot the key: say so, do not mint behind the admin's back
	r.relay.set(func() { r.relay.revoked["xbr_fresh"] = true })
	if code, out, _ := r.call(owner, "PUT", "/push/config", map[string]any{}); code != 409 || !strings.Contains(out["error"].(string), "rotate") {
		t.Fatalf("a forgotten key: %d %v", code, out)
	}
}

// Rotating mints a new relay workspace: handles that delivered under the
// old key are bound to it, so their registrations read needsNewHandle (and
// are skipped, never deleted) until the app renews the handle; a handle
// that never delivered keeps working.
func TestRotateMarksBoundHandlesStale(t *testing.T) {
	r := adminRig(t)
	r.call(owner, "PUT", "/push/config", map[string]any{"relay": r.relay.srv.URL})
	r.register(alice, "phone", "handle-phone")
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "one"})
	r.relay.waitPushes(1)
	eventually(t, "the binding", func() bool { return r.s.st.devices("alice")[0].Bound != "" })
	r.register(alice, "ipad", "handle-ipad", "agent") // registered, never delivered
	if code, _, _ := r.call(owner, "PUT", "/push/config", map[string]any{"rotate": true, "key": "x"}); code != 400 {
		t.Fatalf("rotate with a key: %d", code)
	}
	if code, out, _ := r.call(owner, "PUT", "/push/config", map[string]any{"rotate": true}); code != 200 || r.relay.regs != 2 || out["staleDevices"] != float64(1) {
		t.Fatalf("rotate: %d %v regs %d", code, out, r.relay.regs)
	}
	_, out, _ := r.call(alice, "GET", "/devices/push", nil)
	byID := map[string]map[string]any{}
	for _, d := range out["devices"].([]any) {
		byID[d.(map[string]any)["deviceId"].(string)] = d.(map[string]any)
	}
	if byID["phone"]["needsNewHandle"] != true || byID["phone"]["relayError"] != "handle_bound" || byID["ipad"]["needsNewHandle"] != nil {
		t.Fatalf("after rotate: %v", out)
	}
	// the phone is skipped (the relay would refuse it); the ipad delivers under the new key
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "two", "kind": "x"}) // phone only: skipped
	r.s.AgentEvent("alice", "s1", "apps/cal", agentTurn())
	got := r.relay.waitPushes(2)
	time.Sleep(30 * time.Millisecond)
	if got = r.relay.pushes(); len(got) != 2 || got[1].Handle != "handle-ipad" || got[1].Key != "xbr_fresh2" {
		t.Fatalf("after rotate: %+v", got)
	}
	if len(r.s.st.devices("alice")) != 2 {
		t.Fatal("a stale registration was deleted")
	}
	// the app renews its handle: working again, under the new key
	r.register(alice, "phone", "handle-phone-2")
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "three", "kind": "x"})
	if got := r.relay.waitPushes(3); got[2].Handle != "handle-phone-2" || got[2].Key != "xbr_fresh2" {
		t.Fatalf("renewed: %+v", got[2])
	}
}

// A new XBIN_PUSH_RELAY_KEY is a new relay workspace too.
func TestEnvKeyChangeMarksBoundHandlesStale(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-phone")
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "one"})
	r.relay.waitPushes(1)
	eventually(t, "the binding", func() bool { return r.s.st.devices("alice")[0].Bound != "" })
	r.s.Close()
	s2, err := New(Options{Dir: r.s.o.Dir, RelayURL: r.relay.srv.URL, RelayKey: "k2"})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if d := s2.st.devices("alice"); len(d) != 1 || !d[0].stale(s2.currentEpoch()) {
		t.Fatalf("a new key: %+v", d)
	}
	s3, _ := New(Options{Dir: r.s.o.Dir, RelayURL: r.relay.srv.URL, RelayKey: "k1"})
	defer s3.Close()
	if d := s3.st.devices("alice"); d[0].stale(s3.currentEpoch()) {
		t.Fatal("the same key again: stale")
	}
}

// Only the relay's own word changes a registration: 410 removes it, 403
// handle_bound / 404 handle_unknown mark it; a bare 403 or 404 (a proxy, a
// WAF, a wrong URL) only fails the notification.
func TestRelayRefusalsKeepRegistrations(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-phone")
	send := func(title string) {
		t.Helper()
		if code, _, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": title}); code != 202 {
			t.Fatal(code)
		}
	}
	for i, a := range []fakeAnswer{{403, "<html>blocked</html>"}, {404, "not found"}, {403, `{"error":"no","code":"something_else"}`}, {401, "{}"}} {
		r.relay.answer(a)
		send("x")
		eventually(t, "the failure", func() bool { return r.s.snd.stats().Failed == int64(i+1) })
		if d := r.s.st.devices("alice"); len(d) != 1 || d[0].RelayErr != "" {
			t.Fatalf("a bare %d changed the registration: %+v", a.status, d)
		}
	}
	if st := r.s.snd.stats(); !strings.Contains(st.LastError, "401") {
		t.Fatalf("lastError %q", st.LastError)
	}
	// the relay's handle_unknown: kept, marked, skipped
	r.relay.set(func() { r.relay.unknown["handle-phone"] = true })
	send("y")
	eventually(t, "the mark", func() bool { return r.s.st.devices("alice")[0].RelayErr == "handle_unknown" })
	_, out, _ := r.call(alice, "GET", "/devices/push", nil)
	if d := out["devices"].([]any)[0].(map[string]any); d["needsNewHandle"] != true || d["relayError"] != "handle_unknown" {
		t.Fatalf("view: %v", d)
	}
	failed := r.s.snd.stats().Failed
	send("z")
	time.Sleep(30 * time.Millisecond)
	if r.s.snd.stats().Failed != failed {
		t.Fatal("a stale registration was tried again")
	}
	// 410: the device is gone for good
	r.register(alice, "phone", "handle-phone-2")
	r.relay.answer(fakeAnswer{410, `{"error":"gone","code":"handle_gone"}`})
	send("w")
	eventually(t, "the removal", func() bool { return len(r.s.st.devices("alice")) == 0 })
}

// A key an admin hands over is checked with the relay first.
func TestConfigKeyProbe(t *testing.T) {
	r := adminRig(t)
	r.relay.set(func() { r.relay.keys["xbr_given"] = "given-ws" })
	if code, out, _ := r.call(owner, "PUT", "/push/config", map[string]any{"relay": r.relay.srv.URL, "key": "xbr_wrong"}); code != 400 || r.s.Enabled() {
		t.Fatalf("unknown key: %d %v", code, out)
	}
	if code, out, _ := r.call(owner, "PUT", "/push/config", map[string]any{"relay": r.relay.srv.URL + "/xbin", "key": "xbr_given"}); code != 502 || r.s.Enabled() {
		t.Fatalf("a wrong path: %d %v", code, out)
	}
	code, out, _ := r.call(owner, "PUT", "/push/config", map[string]any{"relay": r.relay.srv.URL, "key": "xbr_given"})
	if code != 200 || out["relayWorkspace"] != "given-ws" || r.relay.regs != 0 {
		t.Fatalf("a good key: %d %v", code, out)
	}
	// unreachable
	dead := adminRig(t)
	dead.relay.srv.Close()
	if code, _, _ := dead.call(owner, "PUT", "/push/config", map[string]any{"relay": dead.relay.srv.URL}); code != http.StatusBadGateway {
		t.Fatalf("unreachable: %d", code)
	}
}

// The key in force is checked with the relay at start and daily (review):
// a key set through the environment was never checked, the relay deletes
// keys nobody used within 30 days, and the first push weeks later failed
// with a hint (PUT {rotate:true}) that answers 409 for an environment key.
// Now each check counts as a use, a forgotten key shows at once in GET
// /push/config (keyError), and the hint follows where the key comes from.
func TestKeyCheck(t *testing.T) {
	r := newRig(t, func(o *Options) { o.KeyCheck = 10 * time.Millisecond })
	r.relay.set(func() { r.relay.keys["k1"] = "wsid-env" })
	r.s.StartKeyCheck()
	probes := func() int { r.relay.mu.Lock(); defer r.relay.mu.Unlock(); return r.relay.probes }
	eventually(t, "two key checks", func() bool { return probes() >= 2 })
	if code, out, _ := r.call(owner, "GET", "/push/config", nil); code != 200 || out["keyChecked"] == nil || out["keyError"] != nil {
		t.Fatalf("a known key: %d %v", code, out)
	}
	r.relay.set(func() { r.relay.revoked["k1"] = true })
	eventually(t, "the key error", func() bool {
		_, out, _ := r.call(owner, "GET", "/push/config", nil)
		e, _ := out["keyError"].(string)
		return strings.Contains(e, "XBIN_PUSH_RELAY_KEY")
	})
	// a push with the forgotten key: the hint names the environment, not PUT
	r.register(alice, "phone", "handle-alice")
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "x"})
	eventually(t, "the refusal", func() bool { return r.s.snd.stats().Failed > 0 })
	if e := r.s.snd.stats().LastError; !strings.Contains(e, "XBIN_PUSH_RELAY_KEY") || strings.Contains(e, "rotate") {
		t.Fatalf("env key hint: %q", e)
	}
	r.s.Close()
	n := probes()
	time.Sleep(40 * time.Millisecond)
	if probes() > n+1 {
		t.Fatal("the key check outlived Close")
	}

	// an admin's key: the hint is PUT {rotate:true}
	a := adminRig(t)
	if code, _, _ := a.call(owner, "PUT", "/push/config", map[string]any{"relay": a.relay.srv.URL}); code != 200 {
		t.Fatal(code)
	}
	a.relay.set(func() { a.relay.revoked["xbr_fresh"] = true })
	a.s.checkKey()
	if _, out, _ := a.call(owner, "GET", "/push/config", nil); !strings.Contains(out["keyError"].(string), "rotate:true") {
		t.Fatalf("admin key hint: %v", out)
	}
}
