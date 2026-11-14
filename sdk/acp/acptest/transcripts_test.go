package acptest

// The shared chat transcripts (sdk/acp/testdata/transcripts/<name>.json):
// event logs in the shape xbind serves them ({seq, ts, type, data}), made
// by playing this package's scripts through a real acp.Client over a pipe
// — the handshake, the tool-meta lift, the permission and question
// bookkeeping are the client's own, nothing is written by hand. Three
// consumers fold them and are held to the expectations beside each log
// (<name>.records.json, <name>.blocks.json): the Agent tab's fold and the
// agent template's (hack/chat-fixtures*.test.mjs), the template's backend
// (its transcripts_test.go, which writes <name>.agtt.json), and later
// sdk/acp's tool record and the Swift port.
//
// TestTranscripts plays every scenario and compares it with its file, so a
// change to the event vocabulary fails here first. Regenerate (review the
// diff, then the expectations: hack/chat-fixtures.test.mjs says how):
//
//	ACP_TRANSCRIPTS_WRITE=1 go test ./acp/acptest -run TestTranscripts -count=1   (in sdk/)
//
// What makes a log reproducible: a scenario acts only at a point the log
// itself names (the status after a turn's end, a request waiting for its
// answer), the scripts' pauses are skipped, seq counts 1, 2, … and ts is
// synthetic — txEpoch + 100 ms per event — so a thought's duration is the
// number of events it spans, not a clock's.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

const (
	txDir   = "../testdata/transcripts"
	txEpoch = int64(1767225600000) // 2026-01-01T00:00:00Z
	txStep  = int64(100)
)

// txEvent is an event as a log file holds it (acp.Event's JSON, data kept
// as the client marshalled it).
type txEvent struct {
	Seq  uint64          `json:"seq"`
	TS   int64           `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// txFile is one transcript: checkpoints are the seqs after which the
// consumers compare what they folded so far (the last event always is one).
type txFile struct {
	Name        string    `json:"name"`
	About       string    `json:"about"`
	Source      string    `json:"source"`
	Flags       []string  `json:"flags"`
	Steps       []string  `json:"steps"`
	Checkpoints []uint64  `json:"checkpoints"`
	Events      []txEvent `json:"events"`
}

// txRun is a client on the scripted agent, its events numbered as they come.
type txRun struct {
	t     *testing.T
	c     *acp.Client
	perms *acp.Permissions
	file  txFile

	mu   sync.Mutex
	evs  []txEvent
	more chan struct{}
	at   int // the next event until() looks at
}

func startTx(t *testing.T, name, about string, flags []string, url bool) *txRun {
	t.Helper()
	home := t.TempDir()
	o := ParseArgs(flags)
	o.Getenv = func(k string) string { return map[string]string{"HOME": home}[k] }
	o.wait = quick
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() {
		err := Serve(inR, outW, o)
		_ = outW.Close()
		served <- err
	}()
	caps := acp.DefaultCaps()
	if url {
		caps.Elicitation.URL = &struct{}{}
	}
	r := &txRun{t: t, perms: acp.NewPermissions(), more: make(chan struct{}, 1),
		file: txFile{Name: name, About: about, Flags: append([]string{}, flags...),
			Source: "sdk/acp/acptest TestTranscripts: the scripted agent through acp.Client; seq and ts are the test's (ts = 2026-01-01T00:00:00Z + 100 ms per event)"}}
	r.c = acp.NewWith(acp.ClientOptions{Caps: &caps})
	go func() {
		for ev := range r.c.Events() {
			r.mu.Lock()
			n := uint64(len(r.evs) + 1)
			r.evs = append(r.evs, txEvent{Seq: n, TS: txEpoch + int64(n)*txStep, Type: ev.Type, Data: ev.Data})
			r.mu.Unlock()
			select {
			case r.more <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = inR.Close()
		select {
		case <-served:
		case <-time.After(wait):
		}
	})
	err := r.c.Start(context.Background(), acp.Config{Provider: acp.Fake([]string{"fakeacp"}), Cwd: "/w/apps/x", Perms: r.perms, Version: "test",
		Spawn: func(context.Context, acp.Config) (*acp.Process, error) {
			return &acp.Process{Stdin: inW, Stdout: outR, Wait: func() error { return nil }, Kill: func() { _ = inW.Close() }}, nil
		}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// the slash commands arrive a moment after session/new's answer
	r.until("the slash commands", func(e txEvent, d map[string]any) bool { return e.Type == acp.EvStatus && d["commands"] != nil })
	return r
}

// until reads on from the last event it returned to the first one pred
// accepts, and returns it.
func (r *txRun) until(what string, pred func(e txEvent, d map[string]any) bool) (txEvent, map[string]any) {
	r.t.Helper()
	deadline := time.After(wait)
	for {
		r.mu.Lock()
		for r.at < len(r.evs) {
			e := r.evs[r.at]
			r.at++
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			if pred(e, d) {
				r.mu.Unlock()
				return e, d
			}
		}
		r.mu.Unlock()
		select {
		case <-r.more:
		case <-deadline:
			r.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (r *txRun) step(s string) { r.file.Steps = append(r.file.Steps, s) }

// checkpoint marks the last event until() returned.
func (r *txRun) checkpoint() {
	r.mu.Lock()
	r.file.Checkpoints = append(r.file.Checkpoints, r.evs[r.at-1].Seq)
	r.mu.Unlock()
}

// prompt sends text; the turn's events follow.
func (r *txRun) prompt(text string) {
	r.t.Helper()
	r.step("prompt: " + text)
	if err := r.c.Send(context.Background(), text); err != nil {
		r.t.Fatalf("prompt %q: %v", text, err)
	}
}

// rest waits for the turn's end and the status after it: nothing follows
// until the next step.
func (r *txRun) rest() {
	r.t.Helper()
	r.until("the turn's end", func(e txEvent, _ map[string]any) bool { return e.Type == acp.EvTurnEnd })
	r.until("the status after it", func(e txEvent, _ map[string]any) bool { return e.Type == acp.EvStatus })
}

// turn is a prompt and its whole turn.
func (r *txRun) turn(text string) {
	r.t.Helper()
	r.prompt(text)
	r.rest()
}

// nth waits for the n-th event of typ from here on (pred narrows which count).
func (r *txRun) nth(n int, typ string, pred func(d map[string]any) bool) map[string]any {
	r.t.Helper()
	var d map[string]any
	for i := 0; i < n; i++ {
		_, d = r.until(typ, func(e txEvent, d map[string]any) bool { return e.Type == typ && (pred == nil || pred(d)) })
	}
	return d
}

// permit answers the permission request the log reached: the request
// waits, so its status (waiting_permission) is read first.
func (r *txRun) permit(d map[string]any, option, by string) {
	r.t.Helper()
	pid, _ := d["pid"].(string)
	r.until("waiting for the answer", func(e txEvent, d map[string]any) bool {
		return e.Type == acp.EvStatus && d["status"] == acp.StatusWaiting
	})
	r.checkpoint()
	r.step("permission " + pid + ": " + option + " by " + by)
	res, err := r.perms.Resolve(pid, option, "", by)
	if err == nil {
		err = r.c.RespondPermission(res)
	}
	if err != nil {
		r.t.Fatalf("permit %s: %v", pid, err)
	}
}

// answer answers the question the log reached.
func (r *txRun) answer(d map[string]any, waits bool, action, content, by string) {
	r.t.Helper()
	eid, _ := d["eid"].(string)
	if waits { // (a url question outside a turn holds no turn up: no status follows it)
		r.until("waiting for the answer", func(e txEvent, d map[string]any) bool {
			return e.Type == acp.EvStatus && d["status"] == acp.StatusWaiting
		})
	}
	r.checkpoint()
	r.step("question " + eid + ": " + action + " by " + by)
	var c json.RawMessage
	if content != "" {
		c = json.RawMessage(content)
	}
	if err := r.c.RespondElicitation(eid, action, c, by); err != nil {
		r.t.Fatalf("answer %s: %v", eid, err)
	}
}

// done compares the log with its file (or writes it).
func (r *txRun) done() {
	r.t.Helper()
	r.mu.Lock()
	r.file.Events = append([]txEvent(nil), r.evs[:r.at]...)
	extra := len(r.evs) - r.at
	last := r.evs[r.at-1].Seq
	r.mu.Unlock()
	if extra != 0 {
		r.t.Fatalf("%d event(s) after the scenario's last step: it didn't wait for them", extra)
	}
	if n := len(r.file.Checkpoints); n == 0 || r.file.Checkpoints[n-1] != last {
		r.file.Checkpoints = append(r.file.Checkpoints, last)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(r.file); err != nil {
		r.t.Fatal(err)
	}
	path := filepath.Join(txDir, r.file.Name+".json")
	if os.Getenv("ACP_TRANSCRIPTS_WRITE") != "" {
		if err := os.MkdirAll(txDir, 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			r.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatalf("%v — write it: ACP_TRANSCRIPTS_WRITE=1 go test ./acp/acptest -run TestTranscripts", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		r.t.Fatalf("%s no longer is what the scripted agent plays through acp.Client (first difference at byte %d) — regenerate it and the "+
			"expectations beside it: ACP_TRANSCRIPTS_WRITE=1 go test ./acp/acptest -run TestTranscripts, then hack/chat-fixtures.test.mjs's header",
			path, firstDiff(want, buf.Bytes()))
	}
}

func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func TestTranscripts(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		r := startTx(t, "text", "a turn of thinking then text (six thought chunks, then the answer); a turn whose answer streams as three chunks of one message; "+
			"each turn ends with its usage", nil, false)
		r.prompt("think about it")
		r.nth(3, acp.EvThoughtDelta, nil)
		r.checkpoint() // mid-thought
		r.rest()
		r.prompt("paras 3")
		r.nth(2, acp.EvMessageDelta, func(d map[string]any) bool { return d["role"] == "agent" })
		r.checkpoint() // mid-answer
		r.rest()
		r.done()
	})

	t.Run("tools", func(t *testing.T) {
		r := startTx(t, "tools", "one completed call of every kind (a read with a location, an edit with a diff, a command with terminal output and exit 0, …); "+
			"a command whose output streams as deltas; a subagent (Claude's Task) with its thought, a call and its text nested under it", nil, false)
		r.turn("cards")
		r.prompt("chatty 4")
		r.nth(2, acp.EvToolUpdate, func(d map[string]any) bool { return d["outputDelta"] != nil })
		r.checkpoint() // a command running, two chunks of output in
		r.rest()
		r.prompt("subagent explore")
		r.until("the subagent's call", func(e txEvent, d map[string]any) bool { return e.Type == acp.EvToolCall && d["id"] == "read1" })
		r.checkpoint() // inside the subagent
		r.rest()
		r.done()
	})

	t.Run("plan", func(t *testing.T) {
		r := startTx(t, "plan", "the agent's plan moving along: three entries pending, one in progress, all completed", nil, false)
		r.prompt("todo list")
		r.nth(2, acp.EvPlan, nil)
		r.checkpoint() // one entry in progress
		r.rest()
		r.done()
	})

	t.Run("permission", func(t *testing.T) {
		r := startTx(t, "permission", "a permission rejected; one answered allow always (a session rule); the next one answered by that rule — the call id t1 "+
			"used in all three turns; then a plan approval (switch_mode, meta.title \"Ready to code?\") answered with the option that raises the mode to auto",
			[]string{"--auto-mode"}, false)
		r.prompt("perm one")
		r.permit(r.nth(1, acp.EvPermissionRequest, nil), "no", "user:bob")
		r.rest()
		r.prompt("perm two")
		r.permit(r.nth(1, acp.EvPermissionRequest, nil), "always", "user:alice")
		r.rest()
		r.turn("perm three") // the rule answers it
		r.prompt("plan the work")
		r.permit(r.nth(1, acp.EvPermissionRequest, nil), "auto", "user:alice")
		r.rest()
		r.done()
	})

	t.Run("questions", func(t *testing.T) {
		r := startTx(t, "questions", "Claude's AskUserQuestion as a form elicitation (a single choice and a multiple one, each with its Other box): answered; "+
			"then skipped (decline)", nil, false)
		r.prompt("ask me")
		r.answer(r.nth(1, acp.EvElicitRequest, nil), true, "accept", `{"question_0":"SQLite","question_1":["Metrics"],"question_1_custom":"Admin UI"}`, "user:alice")
		r.rest()
		r.prompt("ask again")
		r.answer(r.nth(1, acp.EvElicitRequest, nil), true, "decline", "", "user:bob")
		r.rest()
		r.done()
	})

	t.Run("signin", func(t *testing.T) {
		r := startTx(t, "signin", "a signed-out agent: the prompt fails with auth required (the status carries login); a device-code sign-in through a url "+
			"elicitation, accepted, then completed by the agent; the prompt again, answered", []string{"--require-login", "--device-ms=0"}, true)
		r.prompt("echo hi")
		r.rest()
		r.checkpoint() // signed out
		r.step("authenticate: fake-device")
		authed := make(chan error, 1)
		go func() { authed <- r.c.Authenticate(context.Background(), "fake-device", nil) }()
		r.answer(r.nth(1, acp.EvElicitRequest, nil), false, "accept", "", "user:alice")
		r.until("the agent's completion", func(e txEvent, d map[string]any) bool {
			return e.Type == acp.EvElicitResolved && d["action"] == "complete"
		})
		if err := <-authed; err != nil {
			t.Fatalf("authenticate: %v", err)
		}
		r.until("signed in", func(e txEvent, d map[string]any) bool { return e.Type == acp.EvStatus && d["login"] == nil })
		r.turn("echo hi")
		r.done()
	})

	t.Run("id-reuse", func(t *testing.T) {
		r := startTx(t, "id-reuse", "the same call id in two turns (a scripted agent reuses its ids; ACP says an id is unique in a session): two commands "+
			"with streamed output, each a call of its own", nil, false)
		r.turn("chatty 2")
		r.turn("chatty 3")
		r.done()
	})
}
