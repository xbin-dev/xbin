package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// authErr is err as the authenticate route would answer it.
func authErr(err error) (int, string) {
	var ae *hAuthErr
	if errors.As(err, &ae) {
		return ae.Code, ae.Msg
	}
	if err != nil {
		return 500, err.Error()
	}
	return 200, ""
}

// signedOutFixture asks a coding agent that isn't signed in (the fake's
// --require-login) and waits for its sign-in park.
func signedOutFixture(t *testing.T, flags ...string) (*Agent, *http.ServeMux, *sbxSandbox, *Run) {
	ag, mux, box := harnessFixture(t, false, append([]string{"--require-login"}, flags...)...)
	run := askHarness(t, mux, box, "echo hi")
	p := parkOf(t, ag, run.ID, "login")
	var l hLogin
	if json.Unmarshal(p.Harness.Login, &l) != nil || len(l.Methods) != 3 || l.Command == "" {
		t.Fatalf("the login park: %s", p.Harness.Login)
	}
	kinds := map[string]string{}
	for _, m := range l.Methods {
		kinds[m.ID] = m.Kind
	}
	if kinds["fake-login"] != "terminal" || kinds["fake-api-key"] != "api-key" || kinds["fake-device"] != "device-code" {
		t.Fatalf("the methods: %+v", l.Methods)
	}
	hs, _ := ag.db.harnessSession(run.ID)
	if hs.State != hsLogin || !strings.Contains(hs.Held, "echo hi") || hs.PromptState != "" {
		t.Fatalf("the session: %+v", hs)
	}
	sum := viewOf(t, mux, run.ID)["run"].(map[string]any)["harness"].(map[string]any)
	if sum["state"] != "login" || sum["login"] == nil || sum["pending"].(map[string]any)["kind"] != "login" {
		t.Fatalf("the summary: %v", sum)
	}
	return ag, mux, box, run
}

// Signed out: the prompt fails -32000 and the run parks on its sign-in
// with the prompt held; a terminal sign-in (the credentials file) and
// /resume start a fresh adapter that sends the held prompt — once.
func TestHarnessLoginResume(t *testing.T) {
	ag, mux, box, run := signedOutFixture(t)
	if box.Home == "" {
		t.Fatal("no sandbox home")
	}
	cred := filepath.Join(box.Home, ".fakeacp", "credentials")
	if err := os.MkdirAll(filepath.Dir(cred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cred, []byte(`{"method":"fake-code"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/resume", run.ID), nil); w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	hwait(t, "the held prompt", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo hi")
	})
	hs, _ := ag.db.harnessSession(run.ID)
	if hs.Gen != 2 || hs.State != hsLive || hs.Held != "" || hs.Login != "" || strings.Count(transcript(ag.db, run.ID), "U:echo hi") != 1 {
		t.Fatalf("after the retry: %+v %s", hs, transcript(ag.db, run.ID))
	}
}

// Signing in through the adapter with an API key: refusals first, then the
// key (never stored) signs it in and the held prompt goes to the same
// adapter.
func TestHarnessAuthenticateAPIKey(t *testing.T) {
	ag, _, _, run := signedOutFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		method, key string
		code        int
		want        string
	}{
		{"nope", "", 400, "method: one of fake-api-key, fake-device"},
		{"fake-login", "", 400, "method: one of"},
		{"fake-api-key", "", 400, "apiKey: needed for API key"},
		{"fake-device", "k", 400, "apiKey: only for an API-key method"},
		{"fake-api-key", "bad", 502, "invalid API key"},
	} {
		_, err := ag.eng.harnessAuthenticate(ctx, run, c.method, c.key)
		if code, msg := authErr(err); code != c.code || !strings.Contains(msg, c.want) {
			t.Fatalf("%s %q: %d %s", c.method, c.key, code, msg)
		}
	}
	const key = "sk-fake-secret-4711"
	res, err := ag.eng.harnessAuthenticate(ctx, run, "fake-api-key", key)
	if err != nil || res.State != "ready" {
		t.Fatalf("authenticate: %+v %v", res, err)
	}
	hwait(t, "the held prompt", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo hi")
	})
	hs, _ := ag.db.harnessSession(run.ID)
	if hs.Gen != 1 || hs.State != hsLive || hs.Held != "" {
		t.Fatalf("after the sign-in: %+v", hs)
	}
	if _, err := ag.eng.harnessAuthenticate(ctx, run, "fake-api-key", key); err == nil || !strings.Contains(err.Error(), "is signed in") {
		t.Fatalf("signed in already: %v", err)
	}
	for _, q := range []string{`SELECT count(*) FROM harness_sessions WHERE snapshot||login||held||rules LIKE '%` + key + `%'`,
		`SELECT count(*) FROM inbox WHERE body LIKE '%` + key + `%'`, `SELECT count(*) FROM messages WHERE content||meta LIKE '%` + key + `%'`} {
		var n int
		if err := ag.db.q.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("the key was stored (%s): %d %v", q, n, err)
		}
	}
}

// A device-code sign-in: the adapter's URL elicitation, during an
// authenticate AgTT started, is the device code (harness.login.device); a
// second sign-in meanwhile is refused; once the person finishes, the run
// leaves login by itself and the held prompt goes.
func TestHarnessAuthenticateDevice(t *testing.T) {
	ag, mux, _, run := signedOutFixture(t, "--device-ms=400")
	ctx := context.Background()
	res, err := ag.eng.harnessAuthenticate(ctx, run, "fake-device", "")
	if err != nil || res.Device == nil || res.Device.URL != "https://example.invalid/device" || !strings.Contains(res.Device.Message, "FAKE-1234") {
		t.Fatalf("the device code: %+v %v", res, err)
	}
	if _, err := ag.eng.harnessAuthenticate(ctx, run, "fake-api-key", "k"); err == nil || !strings.Contains(err.Error(), "already under way") {
		t.Fatalf("a second sign-in: %v", err)
	}
	sum := viewOf(t, mux, run.ID)["run"].(map[string]any)["harness"].(map[string]any)
	if l, _ := sum["login"].(map[string]any); l == nil || l["device"] == nil {
		t.Fatalf("the summary's device: %v", sum["login"])
	}
	hwait(t, "the held prompt", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo hi")
	})
	if hs, _ := ag.db.harnessSession(run.ID); hs.State != hsLive || hs.Login != "" {
		t.Fatalf("after the device sign-in: %+v", hs)
	}
}

// Lost (the exec gone: an xbind restart): the session is lost, the next
// prompt starts a new generation that reopens the adapter's session
// (session/load) — its replay isn't written again.
func TestHarnessLostResumes(t *testing.T) {
	k := &hpCutter{}
	ag, mux, box, m := harnessFixtureWith(t, k.wrap, true, "--persist")
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	before, _ := ag.db.harnessSession(run.ID)
	old := ag.eng.harnessOf(run.ID)
	m.FailNext("stdio", http.StatusGone, "lost", "the exec is gone (its sandbox restarted)")
	k.cut()
	hwait(t, "lost", func() bool { hs, _ := ag.db.harnessSession(run.ID); return hs.State == hsLost })
	_ = old.pipe.t.Conn.ExecDelete(context.Background(), old.pipe.t.ID, old.pipe.ExecID())
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo two"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the new generation's turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo two")
	})
	hs, _ := ag.db.harnessSession(run.ID)
	text := transcript(ag.db, run.ID)
	if hs.Gen != 2 || hs.ACPSession != before.ACPSession || !hs.Loadable || strings.Contains(fullText(ag.db, run.ID), "couldn't reopen") ||
		strings.Count(text, "A:echo: echo one") != 1 || strings.Count(text, "U:") != 2 {
		t.Fatalf("the resumed session: %+v\n%s", hs, text)
	}
}

// A prompt a handoff left `sending` may never have reached the adapter:
// the successor fails it once ("send it again") and never sends it.
func TestHarnessSendingOnReattach(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET prompt_state='sending' WHERE run_id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.db.q.Exec(`UPDATE runs SET status='running' WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	ag.eng.BeginShutdown()
	b := successor(t, ag)
	hwait(t, "the failed prompt", func() bool { return statusOf(ag.db, run.ID) == statusError && b.harnessOf(run.ID) != nil })
	r := mustRun(t, ag, run.ID)
	hs, _ := ag.db.harnessSession(run.ID)
	if !strings.Contains(r.Result, "send it again") || hs.PromptState != "" {
		t.Fatalf("after the reattach: %q %+v", r.Result, hs)
	}
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(fullText(ag.db, run.ID), "echo:"); n != 1 {
		t.Fatalf("%d answers", n)
	}
	var errs int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM steps WHERE run_id=? AND kind='error'`, run.ID).Scan(&errs)
	if errs != 1 {
		t.Fatalf("%d errors journaled", errs)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo two"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the next turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo two")
	})
}

// hDropper wraps the stdio sockets a manager serves: a socket can be made
// a black hole (nothing the client sends reaches the manager) or deaf to
// the manager's pongs (nothing it took is acknowledged), then cut.
type hDropper struct {
	mu    sync.Mutex
	conns []*hDropConn
}

type hDropConn struct {
	net.Conn
	hole, mute atomic.Bool
	closed     chan struct{}
	once       sync.Once
	wmu        sync.Mutex
	hdr        []byte // a text frame's header, held until its payload says whether it is a pong
}

func (d *hDropper) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stdio") {
			w = &hDropHijack{ResponseWriter: w, d: d}
		}
		h.ServeHTTP(w, r)
	})
}

type hDropHijack struct {
	http.ResponseWriter
	d *hDropper
}

func (h *hDropHijack) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(h.ResponseWriter).Hijack()
	if err != nil {
		return c, rw, err
	}
	dc := &hDropConn{Conn: c, closed: make(chan struct{})}
	h.d.mu.Lock()
	h.d.conns = append(h.d.conns, dc)
	h.d.mu.Unlock()
	return dc, rw, nil
}

func (d *hDropper) each(f func(*hDropConn)) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		f(c)
	}
	return len(d.conns)
}

func (d *hDropper) cut() {
	d.mu.Lock()
	cs := d.conns
	d.conns = nil
	d.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}

func (c *hDropConn) Read(b []byte) (int, error) {
	for {
		if c.hole.Load() {
			<-c.closed
			return 0, net.ErrClosed
		}
		n, err := c.Conn.Read(b)
		if err != nil || !c.hole.Load() {
			return n, err
		}
	}
}

func (c *hDropConn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.hdr != nil {
		hdr := c.hdr
		c.hdr = nil
		if bytes.Contains(b, []byte(`"pong"`)) {
			return len(b), nil // swallowed, header and all
		}
		if _, err := c.Conn.Write(hdr); err != nil {
			return 0, err
		}
		return c.Conn.Write(b)
	}
	if c.mute.Load() && len(b) == 2 && b[0] == 0x81 {
		c.hdr = append([]byte(nil), b...)
		return len(b), nil
	}
	return c.Conn.Write(b)
}

func (c *hDropConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// A stdio socket that drops never makes the adapter answer a prompt twice:
// one it took whose pong the drop swallowed isn't sent again; one that
// never reached it fails at once — "send it again" — and the next works.
func TestHarnessStdioDropNoDoubleSend(t *testing.T) {
	d := &hDropper{}
	ag, mux, box, m := harnessFixtureWith(t, d.wrap, true)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	s := ag.eng.harnessOf(run.ID)
	stdio := "/sbx/sandboxes/" + box.ID + "/execs/" + s.pipe.ExecID() + "/stdio"
	send := func(text string) {
		t.Helper()
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": text}); w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
	}

	// taken, its pong swallowed: not sent again on the next socket
	d.each(func(c *hDropConn) { c.mute.Store(true) })
	send("echo once")
	hwait(t, "its turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo once")
	})
	n := m.count("GET", stdio)
	d.cut()
	hwait(t, "the socket attached again", func() bool { return m.count("GET", stdio) > n })
	time.Sleep(500 * time.Millisecond)
	if c := strings.Count(fullText(ag.db, run.ID)+draftText(ag.eng, run.ID), "echo: echo once"); c != 1 || statusOf(ag.db, run.ID) != statusIdle {
		t.Fatalf("the prompt answered %d times: %s", c, transcript(ag.db, run.ID))
	}

	// never reached the adapter: the prompt fails at once, never sent twice
	d.each(func(c *hDropConn) { c.hole.Store(true) })
	send("echo lost")
	hwait(t, "the prompt on its way", func() bool { hs, _ := ag.db.harnessSession(run.ID); return hs.PromptState == "sent" })
	d.cut()
	hwait(t, "the failed prompt", func() bool { return statusOf(ag.db, run.ID) == statusError })
	if r := mustRun(t, ag, run.ID); !strings.Contains(r.Result, "send it again") {
		t.Fatalf("the failure: %q", r.Result)
	}
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(fullText(ag.db, run.ID)+draftText(ag.eng, run.ID), "echo: echo lost") {
		t.Fatalf("the dropped prompt reached the adapter: %s", transcript(ag.db, run.ID))
	}
	send("echo after")
	hwait(t, "the next turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo after")
	})
	if hs, _ := ag.db.harnessSession(run.ID); hs.Gen != 1 {
		t.Fatalf("respawned: %+v", hs)
	}
}

// Idle reclaim: an adapter with no turn and no park is stopped once
// harnessIdleMin passes (state stopped, its exec ended) — once; a parked
// one is kept until it rests.
func TestHarnessIdleReclaim(t *testing.T) {
	harnessIdleTest = 300 * time.Millisecond
	t.Cleanup(func() { harnessIdleTest = 0 }) // after the engine settles (cleanups run last first)
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	s := ag.eng.harnessOf(run.ID)
	hwait(t, "the reclaim", func() bool { hs, _ := ag.db.harnessSession(run.ID); return hs.State == hsStopped })
	select {
	case <-s.pipe.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the reclaimed adapter runs on")
	}
	time.Sleep(600 * time.Millisecond)
	if hs, _ := ag.db.harnessSession(run.ID); hs.State != hsStopped || hs.Gen != 1 || ag.eng.harnessOf(run.ID) != nil || statusOf(ag.db, run.ID) != statusIdle {
		t.Fatalf("after the reclaim: %+v", hs)
	}
	sum := viewOf(t, mux, run.ID)["run"].(map[string]any)["harness"].(map[string]any)
	if sum["state"] != "stopped" {
		t.Fatalf("the summary: %v", sum["state"])
	}

	parked := askHarness(t, mux, box, "perm")
	p := parkOf(t, ag, parked.ID, "approval")
	time.Sleep(900 * time.Millisecond)
	if hs, _ := ag.db.harnessSession(parked.ID); hs.State != hsLive || ag.eng.harnessOf(parked.ID) == nil {
		t.Fatalf("reclaimed while parked: %+v", hs)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", parked.ID), map[string]any{"approve": true, "park": p.Park}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	hwait(t, "the reclaim after the turn", func() bool { hs, _ := ag.db.harnessSession(parked.ID); return hs.State == hsStopped })
	if !strings.Contains(fullText(ag.db, parked.ID), "listed") {
		t.Fatalf("the turn: %s", transcript(ag.db, parked.ID))
	}
}

// A sandbox detached from the conversation (storeBinding) stops the
// coding agents working in it — the turn ends with why, the exec ends; a
// prompt re-checks the conversation's use of the sandbox first.
func TestHarnessDetachStops(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "stall")
	hwait(t, "stalling", func() bool { return draftText(ag.eng, run.ID) == "stalling" })
	s := ag.eng.harnessOf(run.ID)
	if err := ag.db.Tx(func(t *DB) error {
		return storeBinding(t, run.ID, func(c *Config) error { c.Sandbox, c.Attached = nil, nil; return nil })
	}); err != nil {
		t.Fatal(err)
	}
	hwait(t, "the stop", func() bool { return statusOf(ag.db, run.ID) == statusError })
	r := mustRun(t, ag, run.ID)
	hs, _ := ag.db.harnessSession(run.ID)
	if !strings.Contains(r.Result, "was detached from the conversation") || hs.State != hsFailed || ag.eng.harnessOf(run.ID) != nil {
		t.Fatalf("after the detach: %q %+v", r.Result, hs)
	}
	select {
	case <-s.pipe.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the adapter outlived its sandbox's binding")
	}

	// the binding gone behind the engine's back: the next prompt's check
	other := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, other.ID))
	s = ag.eng.harnessOf(other.ID)
	cfg, _ := ag.db.runConfig(other.ID)
	cfg.Sandbox, cfg.Attached = nil, nil
	raw, _ := json.Marshal(cfg)
	if _, err := ag.db.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), other.ID); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", other.ID), map[string]any{"text": "echo two"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the refused prompt", func() bool { return statusOf(ag.db, other.ID) == statusError })
	if r := mustRun(t, ag, other.ID); !strings.Contains(r.Result, "no longer attached") || strings.Contains(fullText(ag.db, other.ID), "echo: echo two") {
		t.Fatalf("the prompt's check: %q", r.Result)
	}
	select {
	case <-s.pipe.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the refused adapter runs on")
	}

	// … and on durable events, at most once a minute: a refusal stops it
	third := askHarness(t, mux, box, "echo three")
	hwait(t, "the turn", turnOver(ag, third.ID))
	s = ag.eng.harnessOf(third.ID)
	cfg, _ = ag.db.runConfig(third.ID)
	cfg.Sandbox, cfg.Attached = nil, nil
	raw, _ = json.Marshal(cfg)
	if _, err := ag.db.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), third.ID); err != nil {
		t.Fatal(err)
	}
	s.recheckSoon() // checked at the start: not again within the minute
	time.Sleep(200 * time.Millisecond)
	if hs, _ := ag.db.harnessSession(third.ID); hs.State != hsLive {
		t.Fatalf("re-checked within the minute: %+v", hs)
	}
	s.mu.Lock()
	s.checked = time.Now().Add(-2 * hRecheckEvery)
	s.mu.Unlock()
	s.recheckSoon()
	hwait(t, "the stop", func() bool { hs, _ := ag.db.harnessSession(third.ID); return hs.State == hsFailed })
	select {
	case <-s.pipe.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the refused adapter runs on")
	}
}

// Cancel: session/cancel, the exec killed, the run canceled — and a
// harness child's link settles canceled.
func TestHarnessCancelChild(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	root := askHarness(t, mux, box, "echo one")
	hwait(t, "the root's turn", turnOver(ag, root.ID))
	cfg, _ := ag.db.runConfig(root.ID)
	raw, _ := json.Marshal(cfg)
	var kid int64
	if err := ag.db.Tx(func(t2 *DB) error {
		var err error
		if kid, err = t2.createRunStamped("kid", string(raw), root.ID, statusIdle, runStamp{Engine: engineHarness}); err != nil {
			return err
		}
		if _, err := t2.q.Exec(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, deadline, label, created) VALUES (?, ?, 'call-kid', 'bg', 0, 'kid', ?)`,
			root.ID, kid, now()); err != nil {
			return err
		}
		_, _, err = t2.enqueue(kid, inboxHPrompt, inboxBody{Text: "stall", Source: "parent", From: root.ID}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ag.eng.Poke(kid)
	hwait(t, "the child stalling", func() bool { return draftText(ag.eng, kid) == "stalling" })
	s := ag.eng.harnessOf(kid)
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/cancel", kid), map[string]any{"scope": "node"}); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	hwait(t, "canceled", func() bool { return statusOf(ag.db, kid) == statusCanceled })
	select {
	case <-s.pipe.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the child's adapter runs on")
	}
	var state string
	_ = ag.db.q.QueryRow(`SELECT state FROM links WHERE child_id=?`, kid).Scan(&state)
	if state != linkCanceled || ag.eng.harnessOf(kid) != nil {
		t.Fatalf("the link: %q", state)
	}
	if hs, _ := ag.db.harnessSession(kid); hs.State != hsStopped || hs.PromptState != "" {
		t.Fatalf("the child's session: %+v", hs)
	}
}
