package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func (e *env) devicePolls() int {
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	n := 0
	for _, d := range e.gh.devices {
		n += d.polls
	}
	return n
}

// The device flow, start to end, in alice's partition.
func TestSigninDeviceFlow(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.user("alice")
	u := s.routes()
	var st signinState
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin", nil), &st)
	if st.State != "none" {
		t.Fatalf("before: %+v", st)
	}
	r := e.call(u, pageC("alice"), "POST", "/scm/signin", nil)
	ok(t, r, 200)
	decode(t, r, &st)
	if st.State != "pending" || st.Signin.UserCode != "WDJB-MJHT" || st.Signin.IntervalMs != 5000 || !strings.HasPrefix(st.Signin.PollID, "p_") ||
		!strings.HasSuffix(st.Signin.URL, "/login/device") || st.Signin.ExpiresAt != e.clock.now().Add(15*time.Minute).UnixMilli() {
		t.Fatalf("start: %+v %+v", st, st.Signin)
	}
	poll := st.Signin.PollID
	// A second start continues the same flow; GET reads it, starting nothing.
	decode(t, e.call(u, personC("alice"), "POST", "/scm/signin", nil), &st)
	if st.Signin.PollID != poll {
		t.Fatal("a second flow started")
	}
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin", nil), &st)
	if st.State != "pending" || st.Signin.PollID != poll {
		t.Fatalf("GET: %+v", st)
	}
	// Not due yet: GitHub isn't asked.
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin/"+poll, nil), &st)
	if st.State != "pending" || e.devicePolls() != 0 || st.RetryAfterMs <= 0 {
		t.Fatalf("early poll: %+v, %d polls", st, e.devicePolls())
	}
	e.clock.advance(5 * time.Second)
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin/"+poll, nil), &st)
	if st.State != "pending" || e.devicePolls() != 1 {
		t.Fatalf("pending poll: %+v, %d polls", st, e.devicePolls())
	}
	// slow_down: five more seconds (GitHub's own interval wins when longer).
	e.gh.mu.Lock()
	for _, d := range e.gh.devices {
		d.slowDown = 1
	}
	e.gh.mu.Unlock()
	e.clock.advance(5 * time.Second)
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin/"+poll, nil), &st)
	if st.Signin.IntervalMs != 10000 {
		t.Fatalf("slow_down: interval %d", st.Signin.IntervalMs)
	}
	e.gh.approve("octocat", "approved")
	e.clock.advance(10 * time.Second)
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin/"+poll, nil), &st)
	if st.State != "done" || st.Identity == nil || st.Identity.Login != "octocat" || st.Identity.ID != 583231 {
		t.Fatalf("done: %+v", st)
	}
	// Kept: the pair in the partition's vault, the summary in its state,
	// the identity at global (from GitHub, with the partition id).
	if _, _, err := s.userTokens(); err != nil {
		t.Fatal("no pair in the vault")
	}
	if rec := s.personRecord(); rec == nil || rec.Login != "octocat" || rec.Epoch != 1 || !rec.Registered {
		t.Fatalf("person: %+v", rec)
	}
	if id := e.global.ident("alice"); id == nil || id.Login != "octocat" || id.ID != 583231 || id.PID != "pid-alice" {
		t.Fatalf("ident: %+v", id)
	}
	decode(t, e.call(u, pageC("alice"), "POST", "/scm/signin", nil), &st)
	if st.State != "done" {
		t.Fatalf("after: %+v", st)
	}
	var h helloResp
	decode(t, e.call(u, personC("alice"), "GET", "/scm/hello", nil), &h)
	if h.You.Person == nil || h.You.Person.Login != "octocat" || h.You.SigninExpiresAt == 0 {
		t.Fatalf("hello: %+v", h.You)
	}
	// The ended poll still answers a while, then not.
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin/"+poll, nil), &st)
	if st.State != "done" {
		t.Fatalf("ended poll: %+v", st)
	}
	refusal(t, e.call(u, pageC("alice"), "GET", "/scm/signin/p_nope", nil), 404, "not-found")
	// The device code is never in an answer, and the partition holds no
	// App secret.
	if strings.Contains(e.call(u, pageC("alice"), "GET", "/scm/signin", nil).Body.String(), "dc_") {
		t.Fatal("device code in an answer")
	}
	if _, err := s.vault.Get(vaultAppSecret); err == nil {
		t.Fatal("the partition holds the client secret")
	}
}

func TestSigninExpiredDenied(t *testing.T) {
	e := newEnv(t)
	e.setup()
	u := e.user("alice").routes()
	var st signinState
	decode(t, e.call(u, pageC("alice"), "POST", "/scm/signin", nil), &st)
	e.gh.approve("octocat", "denied")
	e.clock.advance(6 * time.Second)
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin/"+st.Signin.PollID, nil), &st)
	if st.State != "denied" {
		t.Fatalf("denied: %+v", st)
	}
	decode(t, e.call(u, pageC("alice"), "POST", "/scm/signin", nil), &st)
	poll := st.Signin.PollID
	e.gh.approve("octocat", "expired")
	e.clock.advance(6 * time.Second)
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin/"+poll, nil), &st)
	if st.State != "expired" {
		t.Fatalf("expired at GitHub: %+v", st)
	}
	// Expired here too: past expiresAt nobody asks GitHub.
	decode(t, e.call(u, pageC("alice"), "POST", "/scm/signin", nil), &st)
	poll = st.Signin.PollID
	e.clock.advance(16 * time.Minute)
	n := e.devicePolls()
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin/"+poll, nil), &st)
	if st.State != "expired" || e.devicePolls() != n {
		t.Fatalf("expired here: %+v", st)
	}
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin", nil), &st)
	if st.State != "none" {
		t.Fatalf("after expiry: %+v", st)
	}
}

func TestSigninDisabled(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.gh.mu.Lock()
	e.gh.deviceOff = true
	e.gh.mu.Unlock()
	u := e.user("alice").routes()
	refusal(t, e.call(u, pageC("alice"), "POST", "/scm/signin", nil), 503, "setup")
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 503, "setup")
	var chk map[string]bool
	decode(t, e.call(e.gH, ownerC, "POST", "/setup/check", nil), &chk)
	if chk["deviceFlow"] {
		t.Fatal("check says on")
	}
	var h helloResp
	decode(t, e.call(u, personC("alice"), "GET", "/scm/hello", nil), &h)
	if !strings.Contains(strings.Join(h.Notes, " "), "Device Flow is off") {
		t.Fatalf("notes: %v", h.Notes)
	}
	e.gh.mu.Lock()
	e.gh.deviceOff = false
	e.gh.mu.Unlock()
	decode(t, e.call(e.gH, ownerC, "POST", "/setup/check", nil), &chk)
	if !chk["deviceFlow"] || e.global.public().DeviceFlow != "on" {
		t.Fatal("check says off")
	}
	// Not set up at all: setup.
	e2 := newEnv(t)
	refusal(t, e2.call(e2.user("bob").routes(), pageC("bob"), "POST", "/scm/signin", nil), 503, "setup")
}

func TestTokenWithoutSigninStartsFlow(t *testing.T) {
	e := newEnv(t)
	e.setup()
	u := e.user("alice").routes()
	x := refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "write"}), 409, "signin")
	if x.Signin == nil || x.Signin.URL == "" {
		t.Fatalf("no sign-in: %+v", x)
	}
	var st signinState
	decode(t, e.call(u, pageC("alice"), "GET", "/scm/signin", nil), &st)
	if st.State != "pending" || st.Signin.PollID != x.Signin.PollID {
		t.Fatalf("the started flow: %+v", st)
	}
	// Reads as the person start one too.
	x2 := refusal(t, e.call(u, personC("alice"), "GET", "/scm/pulls?repo=acme/web", nil), 409, "signin")
	if x2.Signin.PollID != x.Signin.PollID {
		t.Fatal("a second flow")
	}
}

// The background poller and a page's poll both arrive at NextAt: one of
// them asks GitHub. A late failure never turns a done sign-in into one.
func TestSigninPollOncePerInterval(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.user("alice")
	u := s.routes()
	var st signinState
	decode(t, e.call(u, pageC("alice"), "POST", "/scm/signin", nil), &st)
	poll := st.Signin.PollID
	e.clock.advance(5 * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.pollOnce(context.Background(), poll)
		}()
	}
	wg.Wait()
	if n := e.devicePolls(); n != 1 {
		t.Fatalf("%d polls in one interval", n)
	}
	s.endSignin("p_x", endedSignin{state: "done", ident: &identity{Kind: asPerson, Login: "octocat"}})
	s.endSignin("p_x", endedSignin{state: "error", err: "expired_token"})
	if got := s.signin.ended["p_x"]; got.state != "done" {
		t.Fatalf("a done sign-in became %q", got.state)
	}
}
