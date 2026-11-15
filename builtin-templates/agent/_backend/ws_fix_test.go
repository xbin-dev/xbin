package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// --- the person, scripted --------------------------------------------------------

// pending is the one pending request (the test fails when there isn't one).
func (f *fakeXbin) pending(t *testing.T) *fakeRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out *fakeRequest
	for _, r := range f.requests {
		if r.State == "pending" {
			if out != nil {
				t.Fatal("more than one request is pending")
			}
			out = r
		}
	}
	if out == nil {
		t.Fatal("no request is pending")
	}
	return out
}

func (f *fakeXbin) decide(id, state string) { f.decideFor(id, state, "once") }

// decideFor is the person's decision with how long it holds: grant once,
// hour or always (the last two leave a standing approval of that call).
func (f *fakeXbin) decideFor(id, state, grant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.requests[id]
	if r == nil || r.State != "pending" {
		return
	}
	r.State, r.Decided, r.Via = state, time.Now().Unix(), "session"
	if state != "approved" {
		return
	}
	r.Grant = grant
	if grant == "hour" || grant == "always" {
		st := wsStanding{ID: f.id("st"), Tile: r.Tile, Scope: "call", Method: r.call.Method, Path: r.call.Path, Request: r.ID}
		if grant == "hour" {
			st.Until = time.Now().Add(time.Hour).Unix()
		}
		f.standing[fakeKey(r.call)] = true
		f.standings = append(f.standings, st)
	}
}

func (f *fakeXbin) Standing(_ context.Context, sid string) ([]wsStanding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.handle(sid); err != nil {
		return nil, err
	}
	f.note("Standing")
	return append([]wsStanding(nil), f.standings...), nil
}

func (f *fakeXbin) setAgent(id string, fn func(a *wsAgent)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.agents[id])
}

// --- the fixture -----------------------------------------------------------------

var wsAlice = caller{from: "user:alice", user: "alice", level: "read"}

// wsFix is a workspace agent under test: the backend in workspace mode,
// its routes behind the gate, xbind faked.
type wsFix struct {
	ag  *Agent
	mux http.Handler
	raw *http.ServeMux
	x   *fakeXbin
}

func setWsMode(t *testing.T, on bool) {
	t.Helper()
	old := wsModeOn.Load()
	wsModeOn.Store(on)
	t.Cleanup(func() { wsModeOn.Store(old) })
}

func wsSyncReset() {
	for i := 0; i < 500; i++ { // let a running pass end
		wsSync.Lock()
		running := wsSync.running
		wsSync.Unlock()
		if !running {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	wsSync.Lock()
	if wsSync.timer != nil {
		wsSync.timer.Stop()
		wsSync.timer = nil
	}
	wsSync.dirty = false
	wsSync.Unlock()
	wsSync.passes.Store(0)
}

func newWsFix(t *testing.T) *wsFix {
	t.Helper()
	setWsMode(t, true)
	t.Setenv("XBIN_COMPONENT", "xbin/wsagent")
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	x := newFakeXbin()
	oldX := wsX
	wsX = x
	oldPoll, oldWake := wsPollEvery.Load(), coordWakeEvery.Load()
	wsPollEvery.Store(int64(time.Hour)) // a test rings the doorbell itself
	coordWakeEvery.Store(int64(30 * time.Millisecond))
	coordWakerReset()
	wsSyncReset()
	wsAllowAt.Range(func(k, _ any) bool { wsAllowAt.Delete(k); return true }) // run ids repeat from one test's database to the next
	wsOffForget()
	t.Cleanup(wsOffForget)
	t.Cleanup(func() {
		wsSyncReset()
		// The engine publishes through the package's agent, which
		// useGlobalAgent's cleanup — run after this one, before the engine's
		// own shutdown — puts back: its actors end first, seen under the lock
		// their exit takes.
		ag.eng.BeginShutdown()
		for i := 0; i < 1000; i++ {
			ag.eng.mu.Lock()
			n := len(ag.eng.actors)
			ag.eng.mu.Unlock()
			if n == 0 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		wsX = oldX
		wsPollEvery.Store(oldPoll)
		coordWakeEvery.Store(oldWake)
		coordWakerReset()
	})
	mux := http.NewServeMux()
	routes(mux)
	return &wsFix{ag: ag, mux: wsGate(mux, mux), raw: mux, x: x}
}

// call makes a request as c through the gate.
func (fx *wsFix) call(t *testing.T, c caller, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.from != "" {
		r.Header.Set("X-XBin-From", c.from)
		r.Header.Set("X-XBin-Role", "admin")
	}
	if c.user != "" {
		r.Header.Set("X-XBin-User", c.user)
		r.Header.Set("X-XBin-User-Level", c.level)
	}
	w := httptest.NewRecorder()
	fx.mux.ServeHTTP(w, r)
	return w
}

func (fx *wsFix) json(t *testing.T, w *httptest.ResponseRecorder, want int, out any) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d (want %d): %s", w.Code, want, w.Body)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%v: %s", err, w.Body)
		}
	}
}

// main is alice's Main session, made through the route.
func (fx *wsFix) main(t *testing.T) *wsSession {
	t.Helper()
	var out struct {
		Session struct{ ID int64 } `json:"session"`
	}
	fx.json(t, fx.call(t, wsAlice, "POST", "/ws/sessions", map[string]any{"main": true}), 200, &out)
	s, err := fx.ag.db.wsSession(out.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// say sends alice's message to a session, as the page does.
func (fx *wsFix) say(t *testing.T, s *wsSession, text string) {
	t.Helper()
	fx.json(t, fx.call(t, wsAlice, "POST", fmt.Sprintf("/runs/%d/message", s.Run), map[string]any{"text": text}), 200, nil)
}

// notify rings xbind's doorbell and waits for the reconcile to go quiet.
func (fx *wsFix) notify(t *testing.T) {
	t.Helper()
	fx.ring(t)
	fx.settled(t)
}

func (fx *wsFix) ring(t *testing.T) {
	t.Helper()
	if w := fx.call(t, caller{from: "xbin/mail"}, "POST", "/delegate/notify", map[string]any{"partition": "", "events": []map[string]string{{"kind": "resync"}}}); w.Code != 202 {
		t.Fatalf("notify: %d %s", w.Code, w.Body)
	}
}

func (fx *wsFix) settled(t *testing.T) {
	t.Helper()
	waitFor(t, "the reconcile to end", func() bool {
		wsSync.Lock()
		defer wsSync.Unlock()
		return !wsSync.running
	})
}

// idle waits until the session's conversation rests with nothing queued.
func (fx *wsFix) idle(t *testing.T, s *wsSession) {
	t.Helper()
	waitFor(t, "the session to rest", func() bool {
		r, err := fx.ag.db.getRun(s.Run)
		return err == nil && !active(r.Status) && len(fx.ag.db.undelivered(s.Run)) == 0
	})
}

// rest waits until what the session is owed has reached it and it rests:
// an outcome is delivered by a turn the engine starts after the decision
// was recorded, so "idle" alone can be the moment before that turn.
func (fx *wsFix) rest(t *testing.T, s *wsSession) {
	t.Helper()
	waitFor(t, "the session's updates to reach it", func() bool {
		r, err := fx.ag.db.getRun(s.Run)
		return err == nil && !wsOwes(fx.ag.db, r) && !active(r.Status) && len(fx.ag.db.undelivered(s.Run)) == 0
	})
}

// toolResult is the stored result of the latest call of tool in s.
func (fx *wsFix) toolResult(t *testing.T, s *wsSession, tool string) string {
	t.Helper()
	var content string
	if err := fx.ag.db.q.QueryRow(`SELECT content FROM messages WHERE run_id=? AND role='tool' AND name=? ORDER BY seq DESC LIMIT 1`, s.Run, tool).Scan(&content); err != nil {
		t.Fatalf("no result of %s: %v", tool, err)
	}
	return content
}

// lastToolMsg matches a model call whose last message is tool's result.
func lastTool(tool string) func(LLMRequest) bool {
	return func(r LLMRequest) bool {
		if len(r.Msgs) == 0 {
			return false
		}
		m := r.Msgs[len(r.Msgs)-1]
		return m.Role == "tool" && m.Name == tool
	}
}

// systemOf is the system prompt of a model call.
func systemOf(r LLMRequest) string {
	if len(r.Msgs) == 0 {
		return ""
	}
	return asString(r.Msgs[0].Content)
}

// fakeRefusal is xbind's refusal with that code, as the SDK returns it.
func fakeRefusal(refusal, msg string) error {
	return &xbin.DelegateError{Status: 403, Refusal: refusal, Message: msg}
}
