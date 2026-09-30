//go:build linux && integration

package isolated

// partitions_security_cases_test.go — TestPartitionsSecurity's longer cases
// (partitions_security_test.go has the suite, its fixture and the map of
// 10 §B.2 to the tests that make each attempt): events, terminals, a crash
// in alice's partition, a person who lost read, and a held credential. A
// "nothing reached X" check is worth only as much as X's socket: each
// case's other sockets get an event of their own first and last (secWatch's
// controls), so a dead or never-subscribed one fails instead of passing.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// secWatch is the /ws/events sockets a case watches: alice's own (her frame
// of secTile and her session) and everyone else's, each other one with the
// control that must reach it.
type secWatch struct {
	aliceFrame, aliceSess *secEvents
	others                []*secEvents
	control               map[*secEvents]string // the control's prefix; controls adds a tag
}

// secWatch opens the sockets: alice's frame and session; bob's frame
// (control: his partition's bus event) and session (his partition's
// status); carol's — an admin — session (her partition's status) and frame
// (her partition's bus event); and the root token (the global instance's
// status).
func (e *psEnv) secWatch(t *testing.T) *secWatch {
	t.Helper()
	w := &secWatch{
		aliceFrame: e.secEvents(t, "alice's frame", "?frame="+e.fr(t, secTile, "alice").V),
		aliceSess:  e.secEvents(t, "alice's session", "", e.as("alice")...),
		control:    map[*secEvents]string{},
	}
	for _, o := range []struct {
		s       *secEvents
		control string
	}{
		{e.secEvents(t, "bob's frame", "?frame="+e.fr(t, secTile, "bob").V), "bob-evt-"},
		{e.secEvents(t, "bob's session", "", e.as("bob")...), "bob-status-"},
		{e.secEvents(t, "carol's (admin) session", "", e.as("carol")...), "carol-status-"},
		{e.secEvents(t, "carol's frame", "?frame="+e.fr(t, secTile, "carol").V), "carol-evt-"},
		{e.secEvents(t, "the root token", ""), "global-status-"},
	} {
		w.others = append(w.others, o.s)
		w.control[o.s] = o.control
	}
	time.Sleep(500 * time.Millisecond) // the sockets are subscribed
	return w
}

// controls sends every other socket's control, tagged, and waits until
// each got its own: the socket is live and subscribed.
func (w *secWatch) controls(t *testing.T, e *psEnv, tag string) {
	t.Helper()
	api := "/api/" + secTile
	for _, c := range []struct {
		route, body string
		hdrs        []xbindtest.Header
	}{
		{"/publish?topic=t", "bob-evt-" + tag, []xbindtest.Header{e.fr(t, secTile, "bob")}},
		{"/status", "bob-status-" + tag, []xbindtest.Header{e.fr(t, secTile, "bob")}},
		{"/publish?topic=t", "carol-evt-" + tag, []xbindtest.Header{e.fr(t, secTile, "carol")}},
		{"/status", "carol-status-" + tag, []xbindtest.Header{e.fr(t, secTile, "carol")}},
		{"/status", "global-status-" + tag, nil},
	} {
		e.d.Must(t, "POST", api+c.route, c.body, 200, c.hdrs...)
	}
	for _, s := range w.others {
		want := w.control[s] + tag
		xbindtest.Eventually(t, 20*time.Second, s.name+" gets its control "+want, func() (bool, string) {
			got := s.with(want)
			return len(got) > 0, fmt.Sprint(got)
		})
	}
}

// none reports every other socket's frame holding any of frags.
func (w *secWatch) none(t *testing.T, what string, frags ...string) {
	t.Helper()
	for _, s := range w.others {
		for _, f := range frags {
			if got := s.with(f); len(got) != 0 {
				t.Errorf("BUG: %s got %s (%s): %q", s.name, what, f, got)
			}
		}
	}
}

// secEventsCase — 10 §B.2 "bob's /ws/events" (02 §9, PD-09): an event of
// alice's partition — a bus message in its data, its status, her terminal
// session's term events (opened, renamed, ended) — reaches her own sockets
// only: never bob's, an admin's (carol) or the root token's.
func secEventsCase(t *testing.T, e *psEnv) {
	d, api := e.d, "/api/"+secTile
	w := e.secWatch(t)
	w.controls(t, e, "before")
	d.Must(t, "POST", api+"/publish?topic=t", "alice-evt-2c9d", 200, e.fr(t, secTile, "alice"))
	d.Must(t, "POST", api+"/status", "alice-status-51fa", 200, e.fr(t, secTile, "alice"))
	term := w2OpenTerm(t, d, secTile, e.as("alice")[0])
	d.Must(t, "PATCH", "/api/xbin/term/sessions/"+term.id, map[string]string{"name": "alice-tab-4e1c"}, 200, e.as("alice")...)
	if r := d.Call(t, "DELETE", "/api/xbin/term/sessions/"+term.id, nil, e.as("alice")...); r.Status/100 != 2 {
		t.Fatalf("alice ends her own session: %d %s", r.Status, r)
	}
	xbindtest.Eventually(t, 20*time.Second, "alice's frame gets her partition's bus event", func() (bool, string) {
		got := w.aliceFrame.with("alice-evt-2c9d")
		return len(got) == 1 && strings.Contains(got[0], `"partition":"user:alice"`), fmt.Sprint(got)
	})
	xbindtest.Eventually(t, 20*time.Second, "alice's session gets her partition's status", func() (bool, string) {
		got := w.aliceSess.with("alice-status-51fa")
		return len(got) == 1 && strings.Contains(got[0], `"partition":"user:alice"`), fmt.Sprint(got)
	})
	for _, op := range []string{"open", "rename", "close"} {
		xbindtest.Eventually(t, 20*time.Second, "alice's session gets her terminal's "+op, func() (bool, string) {
			got := w.aliceSess.with(`"type":"term"`, `"op":"`+op+`"`, term.id)
			return len(got) == 1, fmt.Sprint(w.aliceSess.with(term.id))
		})
	}
	if got := w.aliceFrame.with("bob-evt-"); len(got) != 0 {
		t.Errorf("BUG: alice's frame got bob's partition's bus event: %q", got)
	}
	w.controls(t, e, "after") // what reached alice's has reached the others by now
	w.none(t, "alice's partition's event", "alice-evt-2c9d", "alice-status-51fa", `"partition":"user:alice"`, term.id, "alice-tab-4e1c")
}

// secTerminalCase — 10 §B.2 "bob's terminal: /tmp leftovers" and "admin:
// reattach, drive, rename" (PD-22, PD-09, S16): alice's change to her
// terminal's system (/opt) lands in her own layer — her next session sees
// it, bob's (holding a layer of his own) doesn't; each starts in its own
// $HOME, never the tile directory; carol, an admin, can't reattach, drive,
// read the events or log of, or rename alice's session, nor see its name,
// but may end it; bob may do none of it.
func secTerminalCase(t *testing.T, e *psEnv) {
	d := e.d
	alice, bob := e.as("alice")[0], e.as("bob")[0]
	held := func(person, id string) bool {
		t.Helper()
		var rows []struct {
			ID      string
			EnvHeld bool
		}
		d.Must(t, "GET", "/api/xbin/term/sessions?cwd="+secTile, nil, 200, e.as(person)...).Decode(t, &rows)
		for _, r := range rows {
			if r.ID == id {
				return r.EnvHeld
			}
		}
		t.Fatalf("%s's session %s isn't listed to them: %+v", person, id, rows)
		return false
	}
	// start: $HOME, which is $PWD, never the tile directory
	start := func(name, out string) {
		t.Helper()
		i := strings.Index(out, "home=")
		if i < 0 {
			t.Fatalf("%s: no home= in %q", name, out)
		}
		var home, pwd string
		if _, err := fmt.Sscanf(out[i:], "home=%s pwd=%s", &home, &pwd); err != nil || home == "" || home != pwd || strings.HasSuffix(pwd, "/"+secTile) {
			t.Errorf("%s starts in %q with $HOME %q (%v), want its $HOME, never the tile directory (shared code)", name, pwd, home, err)
		}
	}
	const where = `; printf 'home=%s pwd=%s\n' "$HOME" "$PWD"`
	// a session holding the person's layer: the first may find the layer
	// still held by a session ending (a second one meanwhile is ephemeral)
	open := func(person string, cred xbindtest.Header) *w2Term {
		t.Helper()
		var s *w2Term
		xbindtest.Eventually(t, 30*time.Second, person+"'s session holds their layer", func() (bool, string) {
			s = w2OpenTerm(t, d, secTile, cred)
			if held(person, s.id) {
				return true, ""
			}
			d.Call(t, "DELETE", "/api/xbin/term/sessions/"+s.id, nil, e.as(person)...)
			return false, "an ephemeral session (the layer is still held)"
		})
		return s
	}
	a1 := open("alice", alice)
	out, rc := a1.run(t, `mkdir -p /opt/i1 && echo alice-layer-9b2e > /opt/i1/f && cat /opt/i1/f`+where, time.Minute)
	if rc != 0 || !strings.Contains(out, "alice-layer-9b2e") {
		t.Fatalf("alice's shell writes her layer: exit %d %q", rc, out)
	}
	t.Logf("alice's first session: %q", out)
	start("alice's session", out)
	if r := d.Call(t, "DELETE", "/api/xbin/term/sessions/"+a1.id, nil, e.as("alice")...); r.Status/100 != 2 {
		t.Fatalf("alice ends her session: %d %s", r.Status, r)
	}
	b := open("bob", bob)
	out, _ = b.run(t, `cat /opt/i1/f 2>&1`+where, time.Minute)
	if strings.Contains(out, "alice-layer") || !strings.Contains(out, "No such file") {
		t.Errorf("BUG: bob's shell on %s (his own layer) sees alice's: %q", secTile, out)
	}
	t.Logf("bob's session: %q", out)
	start("bob's session", out)
	a2 := open("alice", alice) // alice's layer is hers: her next session sees her change
	if out, rc := a2.run(t, `cat /opt/i1/f`, time.Minute); rc != 0 || out != "alice-layer-9b2e" {
		t.Errorf("alice's next session doesn't see her layer (so bob's check proves nothing): exit %d %q", rc, out)
	}
	id := a2.id
	d.Must(t, "PATCH", "/api/xbin/term/sessions/"+id, map[string]string{"name": "alice-private-7d"}, 200, e.as("alice")...)

	notYours := "session belongs to another user: " + secTile + " keeps each person's data apart, so an admin can't open other people's sessions there (ending one is allowed)"
	for _, a := range []secAttempt{
		{"carol (admin)", e.as("carol"), notYours},
		{"bob", e.as("bob"), "session belongs to another user"},
	} {
		c, r, err := d.Dial(t, "/ws/term?session="+id, a.hdrs...)
		if err == nil {
			c.Close()
			t.Errorf("BUG: %s reattached to alice's session", a.name)
		} else if r.Status != 403 || !strings.Contains(r.String(), a.refusal) {
			t.Errorf("%s reattaching to alice's session: %d %s (%v), want 403 %q", a.name, r.Status, r, err, a.refusal)
		}
		// the drive routes: a shell's gate is an agent session's (MayDrive)
		for _, rt := range [][2]string{{"GET", ""}, {"GET", "/events"}, {"GET", "/log"}, {"POST", "/prompt"}} {
			var body any
			if rt[0] == "POST" {
				body = map[string]string{"text": "carol drives"}
			}
			secExpect(t, a.name+" "+rt[0]+" "+rt[1]+" on alice's session", d.Call(t, rt[0], "/api/xbin/term/sessions/"+id+rt[1], body, a.hdrs...),
				[]string{"alice-layer", "alice-private"}, psWant{403, a.refusal, false})
		}
		secExpect(t, a.name+" renames alice's session", d.Call(t, "PATCH", "/api/xbin/term/sessions/"+id, map[string]string{"name": "renamed"}, a.hdrs...),
			nil, psWant{403, "session belongs to another user", false})
	}
	// carol lists alice's sessions (an admin's ?user=): the row, never its name
	var listed []struct{ ID, Name, Partition string }
	d.Must(t, "GET", "/api/xbin/term/sessions?user=alice", nil, 200, e.as("carol")...).Decode(t, &listed)
	found := false
	for _, r := range listed {
		if r.ID == id {
			found = true
			if r.Name != "" || r.Partition != "user:alice" {
				t.Errorf("BUG: carol's listing of alice's session: %+v (want no name)", r)
			}
		}
	}
	if !found {
		t.Errorf("carol's listing of alice's sessions lacks it (so the name check proves nothing): %+v", listed)
	}
	secExpect(t, "bob lists alice's sessions", d.Call(t, "GET", "/api/xbin/term/sessions?user=alice", nil, e.as("bob")...), nil, psWant{403, "admin only", false})
	secExpect(t, "bob ends alice's session", d.Call(t, "DELETE", "/api/xbin/term/sessions/"+id, nil, e.as("bob")...), nil, psWant{403, "session belongs to another user", false})
	if r := d.Call(t, "DELETE", "/api/xbin/term/sessions/"+id, nil, e.as("carol")...); r.Status/100 != 2 {
		t.Errorf("carol (admin) ends alice's session (governance keeps kill): %d %s", r.Status, r)
	}
}

// secLostReadSetup registers alice's and frank's partitions' cron jobs and
// takes frank's read on the tile away (PD-20); secLostReadCase checks them
// a minute later. Registered before the other cases, so they run while it
// waits for the first tick.
func secLostReadSetup(t *testing.T, e *psEnv) time.Time {
	t.Helper()
	for _, p := range []string{"alice", "frank"} {
		e.d.Must(t, "PUT", "/api/xbin/cron/jobs", map[string]string{"name": "beat-" + p, "resource": "res:" + secTile + "/beat",
			"schedule": "@every 1m", "path": "/tick"}, 200, e.fr(t, secTile, p))
	}
	at := time.Now()
	e.d.Must(t, "PUT", "/api/xbin/access", map[string]string{"tile": secTile, "kind": "user", "id": "frank", "level": "none"}, 200)
	e.forget("frank")
	return at
}

// secLostReadCase — 10 §B.2 "a person who lost read" (PD-20): frank's
// partition's job is dormant while he can't read the tile — it never ticks
// — while alice's, registered with it, does.
func secLostReadCase(t *testing.T, e *psEnv, registered time.Time) {
	d, api := e.d, "/api/"+secTile
	secExpect(t, "frank mints a frame of the tile he lost", d.Call(t, "GET", "/api/xbin/frame-token?component="+secTile, nil, e.as("frank")...),
		nil, psWant{403, "", false})
	var tick psWho
	xbindtest.Eventually(t, time.Until(registered.Add(110*time.Second)), "alice's job ticks her partition", func() (bool, string) {
		v, st, body := e.value(t, api+"/kv/kv/tick", e.fr(t, secTile, "alice"))
		return st == 200 && json.Unmarshal([]byte(v), &tick) == nil, fmt.Sprint(st, " ", cut(body, 200))
	})
	if tick.Env != "user:alice" || tick.Caller.From != "xbin/cron" {
		t.Errorf("alice's tick: %+v", tick)
	}
	// frank reads his partition again — before his job's second minute —
	// and it never ticked
	d.Must(t, "PUT", "/api/xbin/access", map[string]string{"tile": secTile, "kind": "user", "id": "frank", "level": ""}, 200)
	v, st, body := e.value(t, api+"/kv/kv/tick", e.fr(t, secTile, "frank"))
	if since := time.Since(registered); since > 115*time.Second {
		t.Errorf("frank's tick read %v after the registration: his job's next minute may have come (inconclusive)", since)
	}
	if st != 404 {
		t.Errorf("BUG: frank's job ticked while he couldn't read %s: %d %q %s", secTile, st, v, cut(body, 200))
	}
	for _, p := range []string{"alice", "frank"} {
		d.Call(t, "DELETE", "/api/xbin/cron/jobs/beat-"+p, nil, e.fr(t, secTile, p))
	}
}

// secCrashCase — 10 §B.2 "a crash in alice's partition" (06 §5): her
// instance exits three times inside the breaker's window; the breaker's
// error names her partition, never a log path, and reaches her sockets
// only, as does everything of the exits (their log lines included);
// bob's instance of the tile runs on. It leaves alice's instance of
// secTile broken (a change to the tile's code retries it): it runs last.
func secCrashCase(t *testing.T, e *psEnv) {
	d, api := e.d, "/api/"+secTile
	alice := e.fr(t, secTile, "alice")
	e.who(t, api+"/who", alice) // her instance runs
	w := e.secWatch(t)
	w.controls(t, e, "crash-before")
	for i := 1; i <= 3; i++ { // each exit leaves it to restart on the next request
		r := d.Call(t, "POST", api+"/exit", nil, alice)
		if r.Status != 200 {
			t.Fatalf("alice's instance, exit %d: %d %s", i, r.Status, r)
		}
		time.Sleep(1500 * time.Millisecond) // it exits 200 ms after answering
	}
	const looping = "user:alice's instance is crash-looping (3 exits)"
	for _, s := range []*secEvents{w.aliceSess, w.aliceFrame} {
		xbindtest.Eventually(t, 30*time.Second, s.name+" gets the breaker's error", func() (bool, string) {
			got := s.with(`"type":"partitions"`, `"build-error"`, looping)
			return len(got) == 1 && !strings.Contains(got[0], ".xbin/log"), fmt.Sprint(s.with("build-error"))
		})
	}
	r := d.Call(t, "GET", api+"/who", nil, alice)
	t.Logf("alice's call after the crash loop: %d %s", r.Status, cut(r.String(), 300))
	if r.Status/100 != 5 || !strings.Contains(r.String(), looping) || strings.Contains(r.String(), ".xbin/") {
		t.Errorf("alice's call after the crash loop: %d %s (want the breaker's error naming her partition, no log path)", r.Status, r)
	}
	if w := e.who(t, api+"/who", e.fr(t, secTile, "bob")); w.Env != "user:bob" {
		t.Errorf("bob's instance after alice's crash loop: %+v", w)
	}
	w.controls(t, e, "crash-after")
	w.none(t, "alice's partition's crash", "crash-looping", "crash-probe", `"partition":"user:alice"`)
}

// secCredentialCase — 10 §B.2 "admin: a reset link / password / SSO email
// for alice" with credentialResetConfirm on (G2 remaining path 2, PD-07,
// PD-55): carol mints a link, sets a password and binds an email; each is
// held — carol redeeming the link is refused, her password signs nothing
// in, alice's own keeps working — and alice is told (her notices, her
// sockets, not carol's). Once alice allows the link in her own session it
// redeems; carol can't allow it for her.
func secCredentialCase(t *testing.T, e *psEnv) {
	d := e.d
	d.Must(t, "PUT", "/api/xbin/workspace-policies", map[string]bool{"credentialResetConfirm": true}, 200, e.as("carol")...)
	restored := false
	defer func() {
		d.Must(t, "PUT", "/api/xbin/workspace-policies", map[string]bool{"credentialResetConfirm": false}, 200, e.as("carol")...)
		if !restored { // alice's own password back (the owner's change, the policy off: not held)
			d.Must(t, "PATCH", "/api/xbin/users/alice", map[string]string{"password": psPassword("alice")}, 200)
		}
		e.sess["alice"] = d.Login(t, "alice", psPassword("alice"))
		e.forget("alice")
	}()
	aliceSock := e.secEvents(t, "alice's session", "", e.as("alice")...)
	carolSock := e.secEvents(t, "carol's (admin) session", "", e.as("carol")...)
	time.Sleep(500 * time.Millisecond)

	var inv struct {
		Held      bool   `json:"held"`
		HeldUntil string `json:"heldUntil"`
		Invite    string `json:"invite"`
	}
	d.Must(t, "POST", "/api/xbin/users/alice/invite", nil, 200, e.as("carol")...).Decode(t, &inv)
	if !inv.Held || inv.HeldUntil == "" || inv.Invite == "" {
		t.Fatalf("carol's reset link for alice isn't held: %+v", inv)
	}
	heldText := "waiting for alice to confirm this sign-in link (an admin made it; alice allows it from a signed-in device, or it works 24 hours after they were told)"
	redeem := map[string]string{"invite": inv.Invite, "password": "carol-owns-alice-1"}
	secExpect(t, "carol redeems alice's held link (the app's form)", secAnon(t, d, "POST", "/api/xbin/invite/redeem", redeem),
		[]string{`"token"`}, psWant{409, heldText, false})
	st, page, hdr := secInviteForm(t, d, inv.Invite, "carol-owns-alice-1")
	t.Logf("carol redeems alice's held link (the login page's form): %d, cookies %v, the page says the link waits: %v", st, hdr["Set-Cookie"], strings.Contains(page, heldText))
	if !strings.Contains(page, heldText) || strings.Contains(fmt.Sprint(hdr["Set-Cookie"]), "xbin_session") || st/100 == 3 {
		t.Errorf("carol redeems alice's held link (the login page's form): %d %v %s", st, hdr["Set-Cookie"], cut(page, 300))
	}
	for _, c := range []struct{ field, value, kind string }{
		{"password", "carol-knows-it-1", "password"},
		{"email", "carol-owns-alice@example.test", "email"},
	} {
		r := d.Call(t, "PATCH", "/api/xbin/users/alice", map[string]string{c.field: c.value}, e.as("carol")...)
		if r.Status != 200 || r.Header.Get("X-XBin-Credential-Held") != c.kind {
			t.Errorf("carol sets alice's %s: %d held %q %s", c.field, r.Status, r.Header.Get("X-XBin-Credential-Held"), r)
		}
	}
	for _, pw := range []string{"carol-knows-it-1", "carol-owns-alice-1"} {
		b, _ := json.Marshal(map[string]string{"username": "alice", "password": pw})
		if st, body := secLogin(t, d, b); st == 200 {
			t.Errorf("BUG: carol's held password %q signs in as alice: %d %s", pw, st, cut(body, 200))
		}
	}
	e.sess["alice"] = d.Login(t, "alice", psPassword("alice")) // her own still works
	// alice is told: her notices and held credentials, and her sockets
	var doc struct {
		Credentials []struct{ ID, Kind, By, Email string }
		Notices     []struct{ Kind, Text, Hold string }
	}
	d.Must(t, "GET", "/api/xbin/partitions", nil, 200, e.as("alice")...).Decode(t, &doc)
	ids := map[string]string{}
	for _, c := range doc.Credentials {
		if c.By == "carol" {
			ids[c.Kind] = c.ID
		}
	}
	if len(ids) != 3 || ids["invite"] == "" || ids["password"] == "" || ids["email"] == "" {
		t.Fatalf("alice's held credentials: %+v", doc.Credentials)
	}
	told := map[string]bool{}
	for _, n := range doc.Notices {
		for _, s := range []string{"A sign-in link for your account was created by carol", "A new password for your account was set by carol",
			"The single sign-on email carol-owns-alice@example.test was bound to your account by carol"} {
			if strings.Contains(n.Text, s) && n.Hold != "" {
				told[s] = true
			}
		}
	}
	if len(told) != 3 {
		t.Errorf("alice's notices: %+v", doc.Notices)
	}
	xbindtest.Eventually(t, 20*time.Second, "alice's socket gets the notices", func() (bool, string) {
		got := aliceSock.with(`"op":"notice"`, "by carol")
		return len(got) >= 3, fmt.Sprint(len(got))
	})
	if got := carolSock.with(`"op":"notice"`); len(got) != 0 {
		t.Errorf("BUG: carol's socket got alice's notices: %q", got)
	}
	// carol can't allow it for alice; alice can
	allow := map[string]any{"id": ids["invite"], "allow": true}
	secExpect(t, "carol allows alice's link", d.Call(t, "POST", "/api/xbin/partitions/credential-confirm", allow, e.as("carol")...), nil, psWant{404, "", false})
	secExpect(t, "the root token allows alice's link", d.Call(t, "POST", "/api/xbin/partitions/credential-confirm", allow), nil, psWant{403, secPersonOnly, false})
	secExpect(t, "carol redeems the link, still held", secAnon(t, d, "POST", "/api/xbin/invite/redeem", redeem),
		nil, psWant{409, heldText, false})
	d.Must(t, "POST", "/api/xbin/partitions/credential-confirm", allow, 200, e.as("alice")...)
	for _, k := range []string{"password", "email"} {
		d.Must(t, "POST", "/api/xbin/partitions/credential-confirm", map[string]any{"id": ids[k], "allow": false}, 200, e.as("alice")...)
	}
	if r := secAnon(t, d, "POST", "/api/xbin/invite/redeem", redeem); r.Status != 200 {
		t.Errorf("the link alice allowed doesn't redeem: %d %s", r.Status, r)
	}
	b, _ := json.Marshal(map[string]string{"username": "alice", "password": "carol-owns-alice-1"})
	if st, body := secLogin(t, d, b); st != 200 {
		t.Errorf("the allowed link's password doesn't sign alice in: %d %s", st, cut(body, 200))
	}
	// bob, a user, mints nothing for alice
	secExpect(t, "bob mints alice a reset link", d.Call(t, "POST", "/api/xbin/users/alice/invite", nil, e.as("bob")...), nil, psWant{403, "invites are minted by admins", false})
}

// secInviteForm posts the login page's invite form (POST /login/invite),
// no credential, not following redirects.
func secInviteForm(t *testing.T, d *xbindtest.Daemon, invite, password string) (int, string, http.Header) {
	t.Helper()
	form := url.Values{"invite": {invite}, "password": {password}, "password2": {password}}
	resp, err := secNoRedirects.PostForm(d.URL+"/login/invite", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// secAnon is a call with no credential at all (xbindtest's Call gives a
// request without one the owner's): whoever holds a link, say.
func secAnon(t *testing.T, d *xbindtest.Daemon, method, path string, body any) xbindtest.Resp {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(method, d.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := secNoRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return xbindtest.Resp{Status: resp.StatusCode, Header: resp.Header, Body: rb}
}

var secNoRedirects = &http.Client{Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
