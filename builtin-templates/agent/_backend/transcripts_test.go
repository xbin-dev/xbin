package main

// The shared chat transcripts through this backend's mapping (D147 §3.4):
// the event logs of the sdk's testdata/transcripts — the same ones the
// Agent tab's fold is held to (hack/chat-fixtures.test.mjs) — applied in
// order by hsess.apply (harness_map.go, harness_park.go), and the run's
// view (GET /runs/{id}/view) taken at each of a log's checkpoints. The views
// are kept beside the logs as <name>.agtt.json; this test fails when the
// backend no longer maps a log to them, and hack/chat-fixtures-agtt.test.mjs
// folds them with model/fold.js and holds the blocks to the same
// expectations as the Agent tab's.
//
// Regenerate after a backend change (review the diff):
//
//	CHAT_TRANSCRIPTS_WRITE=1 TILE_TEST_FLAGS="-count=1 -run TestChatTranscripts" ./hack/tile-check.sh agent
//
// No adapter runs here, so what a session does around the events is said
// by hand, each as the code it stands in for does it:
//
//   - a user message.delta is the prompt's echo, which apply ignores: the
//     user row and the turn's start are the pass's (harness_pass.go);
//   - a permission.request is filed in the session's acp.Permissions first,
//     as the client does before it emits the event (a rule's answer leaves
//     nothing pending), and a person's permission.resolved resolves it there;
//   - a url question and its resolutions are left out: AgTT answers one
//     through its client (declined, or a sign-in's device code:
//     harness_login.go), never from the transcript;
//   - there is no acp.Client, so a turn refused signed out ends as a failed
//     turn (its end can't ask the client's state) and the sign-in park
//     comes with the status after it;
//   - a row's `created` is the seq of the event that wrote it, and ids of a
//     park, clocks and offsets are zeroed: a view is the same on every run.
//
// A view holds what model/fold.js and the harness models read: run (its
// status, pendingState and harness summary), messages, steps, links, drafts.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// chatTranscriptsDir is the sdk's testdata/transcripts as this build sees
// the sdk ("" when it has none: an sdk without the fixtures).
func chatTranscriptsDir() string {
	pc := reflect.ValueOf(acp.NewEvent).Pointer()
	file, _ := runtime.FuncForPC(pc).FileLine(pc)
	dir := filepath.Join(filepath.Dir(file), "testdata", "transcripts")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	return dir
}

type chatTranscript struct {
	Name        string      `json:"name"`
	Checkpoints []uint64    `json:"checkpoints"`
	Events      []acp.Event `json:"events"`
}

type chatView struct {
	Seq  uint64 `json:"seq"`
	View any    `json:"view"`
}

func TestChatTranscripts(t *testing.T) {
	dir := chatTranscriptsDir()
	if dir == "" {
		t.Skip("the sdk this builds against has no testdata/transcripts")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	n := 0
	for _, f := range files {
		if strings.Count(filepath.Base(f), ".") != 1 { // the files beside a log: <name>.records.json, …
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var tr chatTranscript
		if err := json.Unmarshal(raw, &tr); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		n++
		t.Run(tr.Name, func(t *testing.T) { chatTranscriptViews(t, dir, tr) })
	}
	if n == 0 {
		t.Fatalf("no transcripts in %s", dir)
	}
}

func chatTranscriptViews(t *testing.T, dir string, tr chatTranscript) {
	ag, mux := accessFixture(t)
	// the events are applied here, in the log's order: no pass of the engine's runs
	ag.eng.mu.Lock()
	ag.eng.owned = false
	epoch := ag.eng.epoch
	ag.eng.mu.Unlock()
	_ = ag.db.setHarnessMode("alice", "fake", hmApprove)
	cfg := defaultConfig()
	cfg.Engine, cfg.Class = engineHarness, "coding"
	cfg.Harness = &HarnessConfig{Provider: "fake", Ref: "apps/cs/sb-1", Cwd: "/w/apps/x", By: "alice"}
	run, err := ag.startRunOpts(runOpts{Title: tr.Name, Cfg: cfg, Hold: true,
		Stamp: runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.db.putHarnessSession(&harnessSession{RunID: run.ID, RootID: run.ID, Ref: cfg.Harness.Ref, Cwd: cfg.Harness.Cwd,
		Provider: "fake", Gen: 1, State: hsLive}); err != nil {
		t.Fatal(err)
	}
	s := newHsess(ag.eng, run, 1, epoch, acp.Fake([]string{"fakeacp"}))
	ag.eng.mu.Lock()
	ag.eng.harness[run.ID] = s // driven here: its summary says what it is doing
	ag.eng.mu.Unlock()
	t.Cleanup(func() { // before the engine's shutdown (it has no adapter to let go)
		s.halt()
		ag.eng.mu.Lock()
		delete(ag.eng.harness, run.ID)
		ag.eng.mu.Unlock()
	})

	stamp := func(seq uint64) { // rows written since the last event are that event's
		for _, table := range []string{"messages", "steps"} {
			if _, err := ag.db.q.Exec(`UPDATE `+table+` SET created=? WHERE run_id=? AND created>=?`, int64(seq), run.ID, chatStampFloor); err != nil {
				t.Fatal(err)
			}
		}
	}
	checks := map[uint64]bool{}
	for _, c := range tr.Checkpoints {
		checks[c] = true
	}
	urlAsks := map[string]bool{} // the url questions left out, by eid
	var views []chatView
	for _, ev := range tr.Events {
		var d map[string]any
		_ = json.Unmarshal(ev.Data, &d)
		rpc, _ := json.Marshal(fmt.Sprintf("rpc-%d", ev.Seq))
		switch {
		case ev.Type == acp.EvMessageDelta && d["role"] == "user":
			chatPrompt(t, ag, run.ID, fmt.Sprint(d["text"]))
		case ev.Type == acp.EvPermissionRequest:
			var p struct {
				PID      string                 `json:"pid"`
				ToolCall acp.ToolCallRef        `json:"toolCall"`
				Options  []acp.PermissionOption `json:"options"`
			}
			_ = json.Unmarshal(ev.Data, &p)
			if pd, _ := s.perms.Request(p.ToolCall, p.Options, rpc); pd.PID != p.PID {
				t.Fatalf("seq %d: the request is %s here, %s in the log", ev.Seq, pd.PID, p.PID)
			}
			ev.Wire = &acp.Wire{RPCID: rpc}
			s.apply(ev)
		case ev.Type == acp.EvPermissionResolved:
			if by, _ := d["by"].(string); by != "auto" && by != "cancel" {
				if _, err := s.perms.Resolve(fmt.Sprint(d["pid"]), fmt.Sprint(d["optionId"]), "", by); err != nil {
					t.Fatalf("seq %d: %v", ev.Seq, err)
				}
			}
			s.apply(ev)
		case ev.Type == acp.EvElicitRequest && d["mode"] == "url":
			urlAsks[fmt.Sprint(d["eid"])] = true
		case ev.Type == acp.EvElicitResolved && urlAsks[fmt.Sprint(d["eid"])]:
		case ev.Type == acp.EvElicitRequest:
			ev.Wire = &acp.Wire{RPCID: rpc}
			s.apply(ev)
		default:
			s.apply(ev)
		}
		stamp(ev.Seq)
		if checks[ev.Seq] {
			// what a client holds now: the rows as stored, and the calls' output
			// published since (publishDirty) — stored by a durable step
			if err := s.commit(nil, nil); err != nil {
				t.Fatalf("seq %d: %v", ev.Seq, err)
			}
			views = append(views, chatView{Seq: ev.Seq, View: chatViewOf(t, mux, run.ID)})
		}
	}
	if len(views) != len(tr.Checkpoints) {
		t.Fatalf("%d views for %d checkpoints", len(views), len(tr.Checkpoints))
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(map[string]any{"name": tr.Name,
		"about": "the run's view (GET /runs/{id}/view: run, messages, steps, links, drafts) after each checkpoint of " + tr.Name +
			".json, as the agent template's backend maps the log (its transcripts_test.go)",
		"views": views}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, tr.Name+".agtt.json")
	if os.Getenv("CHAT_TRANSCRIPTS_WRITE") != "" {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — write it: see this file's header", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Fatalf("the backend no longer maps %s.json to %s (first difference at byte %d, near %q) — if that is meant, regenerate it (this file's header) "+
			"and run hack/chat-fixtures-agtt.test.mjs", tr.Name, filepath.Base(path), chatFirstDiff(want, buf.Bytes()), chatNear(buf.Bytes(), chatFirstDiff(want, buf.Bytes())))
	}
}

// chatStampFloor: a `created` at or above it is a clock's (unix seconds),
// not yet an event's seq.
const chatStampFloor = 1_000_000_000

// chatPrompt is the pass delivering a prompt (harness_pass.go): the user
// row, the turn begun, the prompt on its way.
func chatPrompt(t *testing.T, ag *Agent, run int64, text string) {
	t.Helper()
	err := ag.eng.fenced(func(tx *DB) error {
		m := &Message{RunID: run, Role: "user", Content: text}
		m.Meta, _ = json.Marshal(msgMeta{Sender: "alice"})
		if _, err := tx.addMessage(m); err != nil {
			return err
		}
		hs, err := tx.harnessSession(run)
		if err != nil {
			return err
		}
		hs.State, hs.Login = hsLive, ""
		hs.TurnSeq, hs.PromptState, hs.PromptRPC, hs.Held = int64(m.Seq), "sent", "", ""
		if err := tx.putHarnessSession(hs); err != nil {
			return err
		}
		_, err = tx.q.Exec(`UPDATE runs SET status=?, pending='', settled_at=0, outcome='' WHERE id=?`, statusRunning, run)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// chatViewOf is GET /runs/{id}/view as alice, cut to what the fold and the
// harness models read, with what differs from run to run zeroed.
func chatViewOf(t *testing.T, mux *http.ServeMux, run int64) any {
	t.Helper()
	w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/view", run), nil)
	var v map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("view: %d %s", w.Code, w.Body)
	}
	if os.Getenv("CHAT_TRANSCRIPTS_RAW") != "" {
		t.Logf("raw view: %s", w.Body)
	}
	out := map[string]any{}
	for _, k := range []string{"messages", "steps", "links", "drafts"} {
		if x, ok := v[k]; ok && x != nil {
			out[k] = chatScrub(k, x)
		}
	}
	r, _ := v["run"].(map[string]any)
	run2 := map[string]any{}
	for _, k := range []string{"id", "status", "engine", "title", "result", "pendingState", "harness", "outcome"} {
		if x, ok := r[k]; ok {
			run2[k] = chatScrub(k, x)
		}
	}
	out["run"] = run2
	return out
}

// chatZero are the keys whose values are a clock's, an offset's or a random
// id's.
var chatZero = map[string]bool{"since": true, "at": true, "started": true, "thinkStart": true, "thinkEnd": true, "park": true,
	"rpcId": true, "updated": true, "lastActive": true, "tokens": true}

func chatScrub(key string, x any) any {
	switch v := x.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = chatScrub(k, e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = chatScrub(key, e)
		}
		return out
	case float64:
		if chatZero[key] {
			return 0
		}
	case string:
		if chatZero[key] && v != "" {
			return key
		}
	}
	return x
}

func chatFirstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func chatNear(b []byte, i int) string {
	return string(b[max(0, i-60):min(len(b), i+60)])
}
