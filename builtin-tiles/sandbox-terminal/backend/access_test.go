package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// keyMarks reads everyone's keys (as a manager): id → inactive since.
func (r *rig) keyMarks() map[string]int64 {
	r.t.Helper()
	st, b := r.do("GET", "/keys?all=1", "carol", "write", nil)
	var l struct{ Keys []keyRec }
	if st != 200 || json.Unmarshal(b, &l) != nil {
		r.t.Fatalf("GET /keys?all=1: %d %s", st, b)
	}
	out := map[string]int64{}
	for _, k := range l.Keys {
		out[k.ID] = k.Inactive
	}
	return out
}

// Removed from the tile, disabled, or xbind not answering: the key logs
// nobody in — the person is told why, and their keys are kept, inactive,
// until access comes back.
func TestSSHAccessRevoked(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.sandbox("alice", "api-dev", shared("*"))
	key := r.register("alice")
	id := keyID(key.PublicKey())
	login := func() (string, string, int) {
		t.Helper()
		c := r.mustDial("api-dev", key)
		defer c.Close()
		return run(t, c, "echo in", false, "")
	}
	if out, _, code := login(); out != "in\n" || code != 0 {
		t.Fatalf("with access: %q %d", out, code)
	}

	r.setAccess("alice", "none", true) // taken off the tile
	if out, errOut, code := login(); code != 1 || out != "" || !strings.Contains(errOut, "access revoked: you no longer have access") {
		t.Fatalf("without access: %q %q %d", out, errOut, code)
	}
	if m := r.keyMarks(); m[id] == 0 {
		t.Fatalf("the key isn't marked inactive: %v", m)
	}
	// a terminal session is refused the same way
	c := r.mustDial("api-dev", key)
	if _, errOut, code := run(t, c, "", true, ""); code != 1 || !strings.Contains(errOut, "access revoked") {
		t.Fatalf("a terminal without access: %q %d", errOut, code)
	}
	c.Close()

	r.setAccess("alice", "read", false) // the account disabled
	if _, errOut, code := login(); code != 1 || !strings.Contains(errOut, "account is disabled or was removed") {
		t.Fatalf("a disabled account: %q %d", errOut, code)
	}

	r.xbind.mu.Lock()
	r.xbind.err = errors.New("xbind: 502 Bad Gateway")
	r.xbind.mu.Unlock()
	r.setAccess("alice", "read", true)
	if _, errOut, code := login(); code != 1 || !strings.Contains(errOut, "can't be checked right now") {
		t.Fatalf("xbind not answering (fails closed): %q %d", errOut, code)
	}
	r.xbind.mu.Lock()
	r.xbind.err = nil
	r.xbind.mu.Unlock()
	r.forget()

	if out, _, code := login(); out != "in\n" || code != 0 {
		t.Fatalf("access back: %q %d", out, code)
	}
	if m := r.keyMarks(); m[id] != 0 {
		t.Fatalf("the key is still inactive: %v", m)
	}

	// answers are kept: a login a moment later doesn't ask again
	r.xbind.mu.Lock()
	before := r.xbind.calls
	r.xbind.mu.Unlock()
	login()
	r.xbind.mu.Lock()
	after := r.xbind.calls
	r.xbind.mu.Unlock()
	if after != before {
		t.Fatalf("asked xbind again within the cache: %d → %d", before, after)
	}

	// taken off and given back: the page's own request (its level is
	// xbind's answer now) lets them in at once, not 30 s later
	r.setAccess("alice", "none", true)
	if _, _, code := login(); code != 1 {
		t.Fatal("no access, logged in")
	}
	r.xbind.mu.Lock()
	r.xbind.m["alice"] = xbin.UserAccess{User: "alice", Level: "read", Active: true} // the tile still keeps "none"
	r.xbind.mu.Unlock()
	if st, _ := r.do("GET", "/me", "alice", "read", nil); st != 200 {
		t.Fatalf("GET /me: %d", st)
	}
	if out, _, code := login(); out != "in\n" || code != 0 {
		t.Fatalf("after the page saw access back: %q %d", out, code)
	}
	if m := r.keyMarks(); m[id] != 0 {
		t.Fatalf("the key is still inactive: %v", m)
	}
}

// Registering a key needs access now — the page's own level, and xbind's.
func TestKeyNeedsAccess(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pub := func() map[string]any { return map[string]any{"publicKey": authorized(newKey(t), "")} }
	if st, b := r.do("POST", "/keys", "bob", "", pub()); st != 403 || !strings.Contains(string(b), "no longer have access") {
		t.Fatalf("a frame token that outlived bob's access (no level): %d %s", st, b)
	}
	if st, _ := r.do("GET", "/sandboxes", "bob", "", nil); st != 403 {
		t.Fatalf("sandboxes listed for a person without access: %d", st)
	}
	r.setAccess("bob", "none", true)
	if st, b := r.do("POST", "/keys", "bob", "read", pub()); st != 403 {
		t.Fatalf("xbind says no: %d %s", st, b)
	}
	r.xbind.mu.Lock()
	r.xbind.err = errors.New("down")
	r.xbind.mu.Unlock()
	r.forget()
	if st, b := r.do("POST", "/keys", "bob", "read", pub()); st != 503 {
		t.Fatalf("xbind not answering: %d %s", st, b)
	}
	r.xbind.mu.Lock()
	r.xbind.err = nil
	r.xbind.mu.Unlock()
	r.setAccess("bob", "read", true)
	if st, b := r.do("POST", "/keys", "bob", "read", pub()); st != 201 {
		t.Fatalf("with access: %d %s", st, b)
	}
}

// A live connection is cut once xbind says its person lost access — its
// sessions are told why.
func TestSSHLiveCutOnRevoke(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(t *Tile) { t.accessEvery = 50 * time.Millisecond })
	r.sandbox("alice", "api-dev", shared("*"))
	key := r.register("alice")
	c := r.mustDial("api-dev", key)
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var errb strings.Builder
	var mu atomic.Pointer[string]
	stderr, _ := s.StderrPipe()
	done := make(chan struct{})
	go func() {
		b, _ := io.ReadAll(stderr)
		errb.Write(b)
		str := errb.String()
		mu.Store(&str)
		close(done)
	}()
	if err := s.Start("echo started; sleep 30"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // a few checks pass while access holds
	if _, err := c.NewSession(); err != nil {
		t.Fatalf("cut while access held: %v", err)
	}
	r.setAccess("alice", "none", true)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the connection lives on after access was removed")
	}
	if s.Wait() == nil {
		t.Fatal("the session ended cleanly")
	}
	if got := *mu.Load(); !strings.Contains(got, "access revoked") {
		t.Fatalf("not told why: %q", got)
	}
	// and a check that fails doesn't cut a connection
	r2 := newRig(t, func(t *Tile) { t.accessEvery = 50 * time.Millisecond })
	r2.sandbox("alice", "api-dev", shared("*"))
	c2 := r2.mustDial("api-dev", r2.register("alice"))
	r2.xbind.mu.Lock()
	r2.xbind.err = errors.New("down")
	r2.xbind.mu.Unlock()
	r2.forget()
	time.Sleep(300 * time.Millisecond)
	if _, err := c2.NewSession(); err != nil {
		t.Fatalf("a failed check cut the connection: %v", err)
	}
}

// Handshakes in flight: over the cap a random older one is dropped, never
// the new one, so connections that never finish can't keep people out; and
// one that never finishes is closed after the login grace.
func TestSSHHandshakeFlood(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(t *Tile) { t.preauth.max = 4; t.loginGrace = 2 * time.Second })
	r.sandbox("alice", "api-dev", shared("*"))
	key := r.register("alice")
	var idle []net.Conn
	for range 4 {
		nc, err := net.Dial("tcp", r.sshAddr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { nc.Close() })
		idle = append(idle, nc)
	}
	eventually(t, 5*time.Second, "4 handshakes in flight", func() bool { return r.tile.preauth.inFlight() == 4 })
	c := r.mustDial("api-dev", key)
	if out, _, code := run(t, c, "echo in", false, ""); out != "in\n" || code != 0 {
		t.Fatalf("a login past a full cap: %q %d", out, code)
	}
	// every idle one is gone within the grace (one of them dropped at once)
	start := time.Now()
	for _, nc := range idle {
		_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 512)
		for {
			if _, err := nc.Read(buf); err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					t.Fatal("an idle handshake outlived the login grace")
				}
				break
			}
		}
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("the idle handshakes took %s to go", d)
	}
}

// pending drops a random older handshake, never the one arriving.
func TestPendingDropsOlder(t *testing.T) {
	t.Parallel()
	dropped := map[int]int{}
	for range 200 {
		p := &pending{max: 3}
		cs := make([]*fakeConn, 4)
		for i := range cs {
			cs[i] = &fakeConn{}
			p.admit(cs[i])
		}
		if p.inFlight() != 3 || cs[3].closed.Load() {
			t.Fatalf("in flight %d, the new one closed %v", p.inFlight(), cs[3].closed.Load())
		}
		n := 0
		for i, c := range cs[:3] {
			if c.closed.Load() {
				n++
				dropped[i]++
			}
		}
		if n != 1 {
			t.Fatalf("%d older ones dropped", n)
		}
		p.done(cs[3])
		p.done(cs[3]) // twice: nothing more
		if p.inFlight() != 2 {
			t.Fatalf("in flight after done: %d", p.inFlight())
		}
	}
	if len(dropped) != 3 {
		t.Fatalf("the drop isn't random: %v", dropped)
	}
}

type fakeConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *fakeConn) Close() error { c.closed.Store(true); return nil }

// The tarpit is per claimed name: a flood of bad keys against one name
// (all from the relay's one address) doesn't slow another's failures.
func TestSSHTarpitPerName(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(t *Tile) {
		t.limit = &limiter{burst: 2, every: time.Hour, delay: 2 * time.Second, buckets: map[string]*bucket{}}
	})
	r.sandbox("alice", "api-dev", shared("*"))
	key := r.register("alice")
	stranger := newKey(t)
	for range 2 {
		if _, err := r.dial("x", stranger); err == nil {
			t.Fatal("a stranger logged in")
		}
	}
	start := time.Now()
	if _, err := r.dial("x", stranger); err == nil {
		t.Fatal("a stranger logged in")
	}
	if d := time.Since(start); d < 2*time.Second {
		t.Fatalf("over the rate for x, answered in %s", d)
	}
	start = time.Now()
	if _, err := r.dial("api-dev", newKey(t)); err == nil {
		t.Fatal("an old key logged in")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a failure for another name was slowed: %s", d)
	}
	c := r.mustDial("api-dev", key)
	if out, _, code := run(t, c, "echo in", false, ""); out != "in\n" || code != 0 {
		t.Fatalf("the good key: %q %d", out, code)
	}
}
