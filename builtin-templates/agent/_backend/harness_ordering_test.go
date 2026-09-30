package main

// What a handoff may catch between an event and its commit, or between an
// answer's commit and its reply: a question the snapshot already holds
// ahead of read_off (it parks again), an answer committed before its reply
// was on the wire (sent again), a steer whose answer was lost (never sent
// twice), a device-code sign-in its predecessor started (completed here).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hCommit is one committed harness_sessions row (and the run's status and
// pending at that commit): what a handoff right after it leaves.
type hCommit struct {
	readOff, errOff, turn                                int64
	snapshot, promptState, draft, rpc, queue, status, pp string
}

// recordCommits keeps every committed harness_sessions row of the test's
// database (a trigger), for commitsOf.
func recordCommits(t *testing.T, ag *Agent) {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE hcommits(n INTEGER PRIMARY KEY AUTOINCREMENT, run_id INT, read_off INT, err_off INT, snapshot TEXT,
			prompt_state TEXT, draft TEXT, prompt_rpc TEXT, turn INT, queue TEXT, status TEXT, pending TEXT)`,
		`CREATE TRIGGER hcommits_u AFTER UPDATE ON harness_sessions BEGIN INSERT INTO hcommits(run_id, read_off, err_off, snapshot,
			prompt_state, draft, prompt_rpc, turn, queue, status, pending) VALUES (NEW.run_id, NEW.read_off, NEW.err_off, NEW.snapshot,
			NEW.prompt_state, NEW.draft, NEW.prompt_rpc, NEW.turn, NEW.queue, (SELECT status FROM runs WHERE id=NEW.run_id),
			(SELECT pending FROM runs WHERE id=NEW.run_id)); END`,
	} {
		if _, err := ag.db.q.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func commitsOf(t *testing.T, ag *Agent, run int64) []hCommit {
	t.Helper()
	rows, err := ag.db.q.Query(`SELECT read_off, err_off, snapshot, prompt_state, draft, prompt_rpc, turn, queue, status, pending
		FROM hcommits WHERE run_id=? ORDER BY n`, run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []hCommit
	for rows.Next() {
		var c hCommit
		if err := rows.Scan(&c.readOff, &c.errOff, &c.snapshot, &c.promptState, &c.draft, &c.rpc, &c.turn, &c.queue, &c.status, &c.pp); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// A form question's frame is past read_off while the snapshot committed
// with an earlier event already holds it (the client's read loop filed it
// before the consumer applied the event before it): the successor reads
// the frame again and parks on it — it isn't restored as filed already,
// with nobody asked.
func TestHarnessQuestionAheadOfOffset(t *testing.T) {
	transports(t, func(t *testing.T, stdio bool) {
		ag, mux, box := harnessFixture(t, stdio)
		recordCommits(t, ag)
		run := askHarness(t, mux, box, "ask")
		parkOf(t, ag, run.ID, "question")
		handOff(t, ag, run.ID)
		cs := commitsOf(t, ag, run.ID)
		at := -1
		for i, c := range cs {
			if strings.Contains(c.pp, `"kind":"question"`) {
				at = i
				break
			}
		}
		if at < 1 || cs[at-1].pp != "" || cs[at-1].promptState != "sent" || !strings.Contains(cs[at].snapshot, `"elicitations"`) {
			t.Fatalf("no commit before the park (at %d of %d)", at, len(cs))
		}
		// the handoff right after the commit before the park, its snapshot
		// already holding the question
		c := cs[at-1]
		if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET read_off=?, err_off=?, snapshot=?, prompt_state=?, draft=?, prompt_rpc=?,
			turn=?, queue=? WHERE run_id=?`, c.readOff, c.errOff, cs[at].snapshot, c.promptState, c.draft, c.rpc, c.turn, c.queue, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ag.db.q.Exec(`UPDATE runs SET status=?, pending=? WHERE id=?`, c.status, c.pp, run.ID); err != nil {
			t.Fatal(err)
		}
		successor(t, ag)
		p := parkOf(t, ag, run.ID, "question")
		if _, _, err := ag.queue(run.ID, inboxHAnswer, inboxBody{Park: p.Park, Action: "accept", Sender: "alice",
			Content: json.RawMessage(`{"question_0":"Postgres"}`)}, ""); err != nil {
			t.Fatal(err)
		}
		hwait(t, "the answer", func() bool {
			return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), `answers: {"question_0":"Postgres"}`)
		})
	})
}

// stdinBusy answers the exec's stdin POSTs 503 while busy (a command that
// isn't reading).
func stdinBusy(busy *atomic.Bool) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if busy.Load() && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/stdin") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":"not reading","refusal":"unavailable"}`))
				return
			}
			h.ServeHTTP(w, r)
		})
	}
}

// An answer is committed as resolved (the park cleared, the run running)
// before its reply is on the wire: a handoff while the reply still retries
// doesn't lose it — the answer stays recorded until the adapter has it and
// the successor sends it again; the turn goes on. Also an answer recorded
// before it went at all (its park still in force): the successor answers
// it, and the park clears.
func TestHarnessAnswerOnItsWayHandoff(t *testing.T) {
	cases := []struct {
		name, script, kind, want string
		answer                   func(t *testing.T, ag *Agent, mux *http.ServeMux, run *Run, p pendingState)
	}{
		{"approval", "perm", "approval", "listed", func(t *testing.T, _ *Agent, mux *http.ServeMux, run *Run, p pendingState) {
			if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": true, "park": p.Park}); w.Code != 200 {
				t.Fatalf("approve: %d %s", w.Code, w.Body)
			}
		}},
		{"question", "ask", "question", `answers: {"question_0":"Postgres"}`, func(t *testing.T, ag *Agent, _ *http.ServeMux, run *Run, p pendingState) {
			if _, _, err := ag.queue(run.ID, inboxHAnswer, inboxBody{Park: p.Park, Action: "accept", Sender: "alice",
				Content: json.RawMessage(`{"question_0":"Postgres"}`)}, ""); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var busy atomic.Bool
			ag, mux, box, _ := harnessFixtureWith(t, stdinBusy(&busy), false)
			run := askHarness(t, mux, box, tc.script)
			p := parkOf(t, ag, run.ID, tc.kind)
			busy.Store(true) // the reply retries at the manager
			tc.answer(t, ag, mux, run, p)
			hwait(t, "the resolution committed", func() bool { return statusOf(ag.db, run.ID) == statusRunning })
			if recorded(ag, run.ID) == "" {
				t.Fatal("the answer on its way isn't recorded")
			}
			handOff(t, ag, run.ID)
			busy.Store(false)
			successor(t, ag)
			hwait(t, "the turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), tc.want) })
			hwait(t, "the answer forgotten", func() bool { return recorded(ag, run.ID) == "" })
		})
	}
	t.Run("stdio-acknowledged", func(t *testing.T) { // over stdio an answer is forgotten once a pong says the adapter has it
		ag, mux, box := harnessFixture(t, true)
		run := askHarness(t, mux, box, "perm")
		p := parkOf(t, ag, run.ID, "approval")
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": true, "park": p.Park}); w.Code != 200 {
			t.Fatalf("approve: %d %s", w.Code, w.Body)
		}
		hwait(t, "the turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "listed") })
		hwait(t, "the answer forgotten", func() bool { return recorded(ag, run.ID) == "" })
	})
	t.Run("recorded-not-sent", func(t *testing.T) {
		ag, mux, box := harnessFixture(t, false)
		run := askHarness(t, mux, box, "perm")
		p := parkOf(t, ag, run.ID, "approval")
		handOff(t, ag, run.ID)
		hs, _ := ag.db.harnessSession(run.ID)
		b, _ := json.Marshal([]hAnswer{{Gen: hs.Gen, Kind: "approval", Park: p.Harness, Option: pickOption(p.Harness.Options, true), By: "user:alice"},
			{Gen: hs.Gen - 1, Kind: "approval", Park: &hPark{PID: "p9", RPCID: "9"}, Option: "x", By: "user:alice"}}) // an older adapter's: not sent
		if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET answers=? WHERE run_id=?`, string(b), run.ID); err != nil {
			t.Fatal(err)
		}
		successor(t, ag)
		hwait(t, "the turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "listed") })
		hwait(t, "the answer forgotten", func() bool { return !strings.Contains(recorded(ag, run.ID), `"pid":"p1"`) })
		if r, _ := ag.db.getRun(run.ID); r.Status != statusIdle || r.Pending != "" {
			t.Fatalf("after the answer: %s %q", r.Status, r.Pending)
		}
	})
	t.Run("pid-reused", func(t *testing.T) {
		// a recorded answer to a request whose park cleared before the
		// handoff, and a later request the adapter sent that a successor
		// (its pid counter starts over) filed under the same pid: the
		// answer goes to its own request (by rpc id), never to the one
		// parked now, and doesn't clear that park
		ag, mux, box := harnessFixture(t, false)
		run := askHarness(t, mux, box, "perm")
		p := parkOf(t, ag, run.ID, "approval")
		handOff(t, ag, run.ID)
		hs, _ := ag.db.harnessSession(run.ID)
		old := &hPark{PID: p.Harness.PID, RPCID: `"x-old"`, Options: p.Harness.Options}
		b, _ := json.Marshal([]hAnswer{{Gen: hs.Gen, Kind: "approval", Park: old, Option: pickOption(p.Harness.Options, true), By: "user:alice"}})
		if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET answers=? WHERE run_id=?`, string(b), run.ID); err != nil {
			t.Fatal(err)
		}
		successor(t, ag)
		hwait(t, "the old answer sent", func() bool { return recorded(ag, run.ID) == "" })
		time.Sleep(500 * time.Millisecond) // an approval of the parked request would be answered by now
		if r, _ := ag.db.getRun(run.ID); r.Status != statusWaiting || parsePending(r.Pending).Park != p.Park || strings.Contains(fullText(ag.db, run.ID), "listed") {
			t.Fatalf("the parked request was answered by another's answer: %s %q: %s", r.Status, r.Pending, transcript(ag.db, run.ID))
		}
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": true, "park": p.Park}); w.Code != 200 {
			t.Fatalf("approve: %d %s", w.Code, w.Body)
		}
		hwait(t, "the turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "listed") })
	})
}

// recorded is run's harness_sessions.answers.
func recorded(ag *Agent, id int64) string {
	var a string
	_ = ag.db.q.QueryRow(`SELECT answers FROM harness_sessions WHERE run_id=?`, id).Scan(&a)
	return a
}

// holdOutput delays the adapter's output reads (exec routes) for 1.5 s once
// a steer frame reached the manager, until released: its answer isn't read
// before the handoff.
func holdOutput(seen *atomic.Int64, released *atomic.Bool) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/stdin") {
				b, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(b))
				h.ServeHTTP(w, r)
				if bytes.Contains(b, []byte("_session/steering")) {
					seen.CompareAndSwap(0, time.Now().UnixNano())
				}
				return
			}
			if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/output") {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				if at := seen.Load(); at != 0 && !released.Load() {
					if d := time.Until(time.Unix(0, at).Add(1500 * time.Millisecond)); d > 0 {
						time.Sleep(d)
					}
				}
				for k, v := range rec.Header() {
					w.Header()[k] = v
				}
				w.WriteHeader(rec.Code)
				_, _ = w.Write(rec.Body.Bytes())
				return
			}
			h.ServeHTTP(w, r)
		})
	}
}

// steeredOnce: the message went to the adapter once — steered into the
// turn, never prompted — and is one user row, with the note that the agent
// may not have it; nothing is left queued or marked.
func steeredOnce(t *testing.T, ag *Agent, run int64, msg string) {
	t.Helper()
	text, tr := fullText(ag.db, run), transcript(ag.db, run)
	var notes, steer int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM steps WHERE run_id=? AND kind='note' AND detail LIKE '%may not have received%'`, run).Scan(&notes)
	_ = ag.db.q.QueryRow(`SELECT steer_row FROM harness_sessions WHERE run_id=?`, run).Scan(&steer)
	switch {
	case strings.Contains(text, "echo: "+msg):
		t.Fatalf("the steered message was prompted again: %s", tr)
	case strings.Count(text, "steered: "+msg) != 1:
		t.Fatalf("steered %d times: %s", strings.Count(text, "steered: "+msg), tr)
	case strings.Count(tr, "U:"+msg) != 1:
		t.Fatalf("%d user rows: %s", strings.Count(tr, "U:"+msg), tr)
	case notes != 1:
		t.Fatalf("%d notes: %s", notes, tr)
	case steer != 0 || len(ag.db.undelivered(run)) != 0:
		t.Fatalf("left: steer_row %d, %d rows", steer, len(ag.db.undelivered(run)))
	}
}

// A steer is at most once, as a prompt: a handoff while its answer is on
// its way (the adapter took it) — the message isn't delivered again after
// the turn; it is the run's user message, with a note. A steer whose answer
// doesn't come in time (hSteerFor) is settled the same way, handoff or not;
// one a predecessor marked and never settled is settled by the successor.
func TestHarnessSteerOnItsWay(t *testing.T) {
	t.Run("handoff", func(t *testing.T) {
		var seen atomic.Int64
		var released atomic.Bool
		ag, mux, box, _ := harnessFixtureWith(t, holdOutput(&seen, &released), false, "--steer")
		run := askHarness(t, mux, box, "steer")
		hwait(t, "ticking", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick 1") })
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "use tabs"}); w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		hwait(t, "the steer at the manager", func() bool { return seen.Load() != 0 })
		handOff(t, ag, run.ID) // its answer not read yet
		released.Store(true)
		successor(t, ag)
		hwait(t, "the turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "steers:") })
		time.Sleep(time.Second) // a prompt of it would go now
		hwait(t, "rest", turnOver(ag, run.ID))
		steeredOnce(t, ag, run.ID, "use tabs")
	})
	t.Run("no-answer", func(t *testing.T) {
		old := hSteerFor
		hSteerFor = 300 * time.Millisecond // before the fixture: its passes read it until its cleanup
		t.Cleanup(func() { hSteerFor = old })
		var seen atomic.Int64
		var released atomic.Bool
		ag, mux, box, _ := harnessFixtureWith(t, holdOutput(&seen, &released), false, "--steer")
		run := askHarness(t, mux, box, "steer")
		hwait(t, "ticking", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick 1") })
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "use tabs"}); w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		hwait(t, "the turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "steers:") })
		time.Sleep(time.Second)
		hwait(t, "rest", turnOver(ag, run.ID))
		steeredOnce(t, ag, run.ID, "use tabs")
	})
	t.Run("marked-by-the-predecessor", func(t *testing.T) {
		ag, mux, box := harnessFixture(t, false, "--steer")
		run := askHarness(t, mux, box, "echo one")
		hwait(t, "the turn", turnOver(ag, run.ID))
		handOff(t, ag, run.ID)
		// the predecessor marked a message's steer on its way and let go
		id, _, err := ag.db.enqueue(run.ID, inboxHPrompt, inboxBody{Text: "use tabs", Source: "human", Sender: "alice"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET steer_row=? WHERE run_id=?`, id, run.ID); err != nil {
			t.Fatal(err)
		}
		b := successor(t, ag)
		b.Poke(run.ID)
		hwait(t, "the settled steer", func() bool { return strings.Contains(transcript(ag.db, run.ID), "U:use tabs") })
		time.Sleep(time.Second)
		hwait(t, "rest", turnOver(ag, run.ID))
		var notes int
		_ = ag.db.q.QueryRow(`SELECT count(*) FROM steps WHERE run_id=? AND kind='note' AND detail LIKE '%may not have received%replaced%'`, run.ID).Scan(&notes)
		if strings.Contains(fullText(ag.db, run.ID), "echo: use tabs") || notes != 1 || len(ag.db.undelivered(run.ID)) != 0 {
			t.Fatalf("the predecessor's steer: %d notes, %s", notes, transcript(ag.db, run.ID))
		}
	})
}

// A device-code sign-in across a handoff: the predecessor's authenticate
// dies with it, the person completes the sign-in — the adapter's
// elicitation/complete wakes the run here: the held prompt goes, once, and
// the session is live. A device code the successor has no question for
// (nothing will complete it here) isn't shown any more.
func TestHarnessDeviceSignInHandoff(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		ag, _, _, run := signedOutFixture(t, "--device-ms=1500")
		res, err := ag.eng.harnessAuthenticate(context.Background(), run, "fake-device", "")
		if err != nil || res.Device == nil {
			t.Fatalf("the device code: %+v %v", res, err)
		}
		handOff(t, ag, run.ID)
		successor(t, ag)
		hwait(t, "the held prompt's turn", func() bool {
			return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo hi")
		})
		hs, _ := ag.db.harnessSession(run.ID)
		r, _ := ag.db.getRun(run.ID)
		if hs.State != hsLive || hs.Login != "" || hs.Held != "" || r.Status != statusIdle || strings.Count(fullText(ag.db, run.ID), "echo: echo hi") != 1 {
			t.Fatalf("after the sign-in: session %s login %q held %q, run %s: %s", hs.State, hs.Login, hs.Held, r.Status, transcript(ag.db, run.ID))
		}
	})
	t.Run("nothing-to-complete", func(t *testing.T) {
		ag, _, _, run := signedOutFixture(t, "--device-ms=600000")
		res, err := ag.eng.harnessAuthenticate(context.Background(), run, "fake-device", "")
		if err != nil || res.Device == nil {
			t.Fatalf("the device code: %+v %v", res, err)
		}
		handOff(t, ag, run.ID)
		hs, _ := ag.db.harnessSession(run.ID)
		var st map[string]any
		_ = json.Unmarshal([]byte(hs.Snapshot), &st)
		delete(st, "elicitations") // the adapter's URL question isn't in the snapshot
		b, _ := json.Marshal(st)
		if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET snapshot=? WHERE run_id=?`, string(b), run.ID); err != nil {
			t.Fatal(err)
		}
		e := successor(t, ag)
		hwait(t, "the attach", func() bool { return e.harnessOf(run.ID) != nil })
		hwait(t, "the device code taken away", func() bool {
			hs, _ := ag.db.harnessSession(run.ID)
			r, _ := ag.db.getRun(run.ID)
			p := parsePending(r.Pending)
			return hs.State == hsLogin && !loginDevice(hs.Login) && p.Kind == "login" && !loginDevice(string(p.Harness.Login))
		})
	})
}
