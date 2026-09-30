package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// codingParent is alice's conversation of the coding class — the agent's
// own loop — working in box, its first message text.
func codingParent(t *testing.T, mux *http.ServeMux, box *sbxSandbox, text string) *Run {
	t.Helper()
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": text, "class": "coding",
		"sandbox": map[string]any{"ref": sandboxRef("apps/cs", box.ID)}})
	var run Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil {
		t.Fatalf("POST /ask: %d %s", w.Code, w.Body)
	}
	return &run
}

// turnOnly narrows a matcher to the model's turns (not the titler's).
func turnOnly(m func(LLMRequest) bool) func(LLMRequest) bool {
	return func(r LLMRequest) bool { return r.Purpose == "turn" && m(r) }
}

// turnsOf are the model's turns for run.
func turnsOf(f *fakeLLM, run int64) []LLMRequest {
	var out []LLMRequest
	for _, c := range f.callsFor(run) {
		if c.Purpose == "turn" {
			out = append(out, c)
		}
	}
	return out
}

// toolText is the content of the last tool result a request carries.
func toolText(r LLMRequest) string {
	for i := len(r.Msgs) - 1; i >= 0; i-- {
		if r.Msgs[i].Role == "tool" {
			return stripReminder(asText(r.Msgs[i].Content))
		}
	}
	return ""
}

// kidsOf waits for n children of parent and returns them, oldest first.
func kidsOf(t *testing.T, ag *Agent, parent int64, n int) []*Run {
	t.Helper()
	var kids []*Run
	hwait(t, fmt.Sprintf("%d children of #%d", n, parent), func() bool {
		kids, _ = ag.db.queryRuns(`WHERE parent_id=? ORDER BY id`, parent)
		return len(kids) >= n
	})
	return kids
}

// waitSaid waits for run's transcript to hold s.
func waitSaid(t *testing.T, ag *Agent, run int64, s string) {
	t.Helper()
	hwait(t, fmt.Sprintf("#%d to say %q (it said: %s)", run, s, transcript(ag.db, run)), func() bool {
		return strings.Contains(fullText(ag.db, run), s) && statusOf(ag.db, run) == statusIdle
	})
}

// spawnSpec is subagent_spawn's parameters as offered.
func spawnSpec(specs []toolSpec) map[string]any {
	for _, s := range specs {
		if s.Function.Name == "subagent_spawn" {
			return s.Function.Parameters["properties"].(map[string]any)
		}
	}
	return nil
}

// The agent starts a coding agent in the foreground: `harness` offered,
// the child a harness run (the owner's setting its mode, the task its first
// prompt and its task) whose permission request waits for a PERSON — the
// parent waits on its link and is never offered it, the digest says what
// it waits for — and once a person approves, the child's turn end settles
// the link with its text, which the parent's call receives.
func TestHarnessSpawnForeground(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	f := fakeOf(ag)
	f.on(turnOnly(lastIs("user", "harness spawn")), callTools(tc("c1", "subagent_spawn",
		`{"task":"perm","label":"fake coder","harness":"fake","summary":"Ask the coding agent"}`))).once()
	f.on(turnOnly(lastIs("tool", "")), func(r LLMRequest) (LLMReply, error) {
		return say("The coding agent said: " + toolText(r))(r)
	})
	parent := codingParent(t, mux, box, "harness spawn")
	child := kidsOf(t, ag, parent.ID, 1)[0]
	if child.Engine != engineHarness || child.Title != "fake coder" {
		t.Fatalf("the child: %+v", child)
	}
	cfg, _ := ag.db.runConfig(child.ID)
	if cfg.Harness == nil || cfg.Harness.Provider != "fake" || cfg.Harness.Mode != "ask" || cfg.Harness.Ref != sandboxRef("apps/cs", box.ID) ||
		cfg.Engine != engineHarness || strings.Contains(cfg.System, "You are a subagent") {
		t.Fatalf("the child's config: %+v %+v", cfg.Harness, cfg)
	}
	props := spawnSpec(turnsOf(f, parent.ID)[0].Tools)
	if h, _ := props["harness"].(map[string]any); h == nil || fmt.Sprint(h["enum"]) != "[fake]" ||
		!strings.Contains(h["description"].(string), "A coding agent sees only the sandbox and the task, not this conversation") {
		t.Fatalf("the harness offered: %v", props["harness"])
	}

	p := parkOf(t, ag, child.ID, "approval")
	if st := statusOf(ag.db, parent.ID); st != statusAwait {
		t.Fatalf("the parent while its coding agent asks a person: %s", st)
	}
	if n := len(turnsOf(f, parent.ID)); n != 1 {
		t.Fatalf("the parent was asked %d times while its coding agent waits", n)
	}
	d := ag.eng.childDigest(ag.db, child.ID, true)
	if !strings.Contains(d, "waiting for a person for") || !strings.Contains(d, "harness fake · 1 tool call") ||
		!strings.Contains(d, "waiting for a person to approve: run ls") {
		t.Fatalf("the digest: %s", d)
	}
	if d := ag.eng.childDigest(ag.db, child.ID, false); !strings.Contains(d, "\n  waiting for a person to approve: run ls") {
		t.Fatalf("the short digest: %s", d)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", child.ID), map[string]any{"approve": true, "park": p.Park}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	hwait(t, "the parent's answer", func() bool {
		return statusOf(ag.db, parent.ID) == statusIdle && strings.Contains(ag.db.lastAssistant(parent.ID), "The coding agent said:")
	})
	if l := ag.db.latestLink(child.ID); l == nil || l.State != linkDone || l.Outcome != outcomeAnswered || l.Result != "listed" || !l.Delivered {
		t.Fatalf("the link: %+v", l)
	}
	if got := ag.db.lastAssistant(parent.ID); got != "The coding agent said: --- #"+itoa(child.ID)+" fake coder (done) ---\nlisted" {
		t.Fatalf("the parent's answer: %q", got)
	}
	asks, _ := ag.db.asks(child.ID)
	if len(asks) != 1 || asks[0].Source != "parent" || asks[0].Who != "#"+itoa(parent.ID) || asks[0].Text != "perm" {
		t.Fatalf("the child's task: %+v", asks)
	}
	if tr := transcript(ag.db, child.ID); !strings.HasPrefix(tr, "U:perm | ") {
		t.Fatalf("the child's transcript: %s", tr)
	}
}

// helloLimit wraps a manager so its hello says execsRunning is max.
func helloLimit(max int) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/hello") {
				h.ServeHTTP(w, r)
				return
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			var m map[string]any
			if json.Unmarshal(rec.Body.Bytes(), &m) == nil {
				if l, ok := m["limits"].(map[string]any); ok {
					l["execsRunning"] = max
				}
			}
			b, _ := json.Marshal(m)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rec.Code)
			_, _ = w.Write(b)
		})
	}
}

// Fanning out: three coding agents start in one step; a fourth is refused
// while they run (maxHarness), and — a sandbox short of room for another
// command — so is one that would crowd its exec limit.
func TestHarnessSpawnLimits(t *testing.T) {
	t.Run("maxHarness", func(t *testing.T) {
		ag, mux, box := harnessFixture(t, false)
		f := fakeOf(ag)
		var calls []toolCall
		for i, task := range []string{"stall", "stall", "perm", "stall"} {
			calls = append(calls, tc(fmt.Sprintf("f%d", i), "subagent_spawn", fmt.Sprintf(`{"task":%q,"harness":"fake","wait":false,"label":"coder %d"}`, task, i+1)))
		}
		f.on(turnOnly(lastIs("user", "harness fan out")), callTools(calls...)).once()
		f.on(turnOnly(lastIs("tool", "")), say("Started three coding agents.")).once()
		parent := codingParent(t, mux, box, "harness fan out")
		hwait(t, "the fan-out", func() bool { return statusOf(ag.db, parent.ID) == statusIdle })
		kids, _ := ag.db.queryRuns(`WHERE parent_id=? ORDER BY id`, parent.ID)
		if len(kids) != 3 {
			t.Fatalf("%d coding agents started", len(kids))
		}
		req := turnsOf(f, parent.ID)[1]
		var results []string
		for _, m := range req.Msgs {
			if m.Role == "tool" {
				results = append(results, stripReminder(asText(m.Content)))
			}
		}
		if len(results) != 4 || !strings.HasPrefix(results[0], fmt.Sprintf("started coding agent #%d (Fake agent (tests)) in the background", kids[0].ID)) ||
			results[3] != "error: 3 coding agents already run in this conversation (the limit) — wait for one or cancel one" {
			t.Fatalf("the spawns' results: %q", results)
		}
		// one ends: there is room again
		hwait(t, "the park", func() bool { return statusOf(ag.db, kids[2].ID) == statusWaiting })
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/cancel", kids[0].ID), map[string]any{}); w.Code != 200 {
			t.Fatalf("cancel: %d %s", w.Code, w.Body)
		}
		hwait(t, "the cancel", func() bool { return statusOf(ag.db, kids[0].ID) == statusCanceled })
		if n := ag.db.runningHarnesses(parent.ID); n != 2 {
			t.Fatalf("running coding agents: %d", n)
		}
		if l := ag.db.latestLink(kids[0].ID); l == nil || l.State != linkCanceled {
			t.Fatalf("the cancelled one's link: %+v", l)
		}
	})
	t.Run("execs", func(t *testing.T) {
		ag, mux, box, _ := harnessFixtureWith(t, helloLimit(5), false)
		f := fakeOf(ag)
		f.on(turnOnly(lastIs("user", "first")), callTools(tc("e1", "subagent_spawn", `{"task":"stall","harness":"fake","wait":false}`))).once()
		f.on(turnOnly(lastIs("user", "second")), callTools(tc("e2", "subagent_spawn", `{"task":"stall","harness":"fake","wait":false}`))).once()
		f.on(turnOnly(lastIs("tool", "")), func(r LLMRequest) (LLMReply, error) { return say("said: " + toolText(r))(r) })
		parent := codingParent(t, mux, box, "first")
		kid := kidsOf(t, ag, parent.ID, 1)[0]
		hwait(t, "its adapter", func() bool {
			hs, _ := ag.db.harnessSession(kid.ID)
			return hs != nil && hs.PromptState == "sent" && statusOf(ag.db, parent.ID) == statusIdle
		})
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", parent.ID), map[string]any{"text": "second"}); w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		hwait(t, "the refusal", func() bool {
			return strings.Contains(ag.db.lastAssistant(parent.ID), "said: error:")
		})
		// the first coding agent's adapter is the one command (5 − 4 = room for one)
		if got := ag.db.lastAssistant(parent.ID); got != "said: error: box runs 1 command (its limit is 5) — a coding agent needs room" {
			t.Fatalf("the refusal: %q", got)
		}
		if kids, _ := ag.db.queryRuns(`WHERE parent_id=?`, parent.ID); len(kids) != 1 {
			t.Fatalf("%d coding agents", len(kids))
		}
	})
}

// Steering a coding agent (subagent_message): one that steers takes it into
// its running turn; one that doesn't gets it when that turn ends; an idle
// one gets it as its next prompt (a new link: its answer comes back). The
// message is sent as is — no "[message from your parent]" wrapper.
func TestHarnessSpawnSteer(t *testing.T) {
	steer := func(t *testing.T, flags []string, task string, wantReply, wantText string) {
		ag, mux, box := harnessFixture(t, false, flags...)
		f := fakeOf(ag)
		var kid int64
		f.on(turnOnly(lastIs("user", "go")), callTools(tc("s1", "subagent_spawn", fmt.Sprintf(`{"task":%q,"harness":"fake","wait":false}`, task)))).once()
		f.on(turnOnly(lastIs("user", "steer it")), func(r LLMRequest) (LLMReply, error) {
			return callTools(tc("s2", "subagent_message", fmt.Sprintf(`{"id":%d,"text":"use tabs"}`, kid)))(r)
		}).once()
		f.on(turnOnly(lastIs("user", "again")), func(r LLMRequest) (LLMReply, error) {
			return callTools(tc("s3", "subagent_message", fmt.Sprintf(`{"id":%d,"text":"again please"}`, kid)))(r)
		}).once()
		f.on(turnOnly(lastIs("tool", "")), func(r LLMRequest) (LLMReply, error) { return say("said: " + toolText(r))(r) })
		parent := codingParent(t, mux, box, "go")
		kid = kidsOf(t, ag, parent.ID, 1)[0].ID
		hwait(t, "its turn", func() bool {
			hs, _ := ag.db.harnessSession(kid)
			s := ag.eng.harnessOf(kid)
			return hs != nil && hs.PromptState == "sent" && statusOf(ag.db, parent.ID) == statusIdle && (len(flags) == 0 || s != nil && s.steers())
		})
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", parent.ID), map[string]any{"text": "steer it"}); w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		waitSaid(t, ag, parent.ID, "said: "+fmt.Sprintf(wantReply, kid))
		hwait(t, "the steered text", func() bool { return turnOver(ag, kid)() && strings.Contains(fullText(ag.db, kid), wantText) })
		if tr := transcript(ag.db, kid); !strings.Contains(tr, "U:use tabs | ") || strings.Contains(fullText(ag.db, kid), "[message from your parent") {
			t.Fatalf("the child's transcript: %s", tr)
		}
		// idle now: the next message is its next prompt, and its answer comes back
		hwait(t, "its answer delivered", func() bool {
			l := ag.db.latestLink(kid)
			return l != nil && l.Delivered && statusOf(ag.db, parent.ID) == statusIdle
		})
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", parent.ID), map[string]any{"text": "again"}); w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		waitSaid(t, ag, parent.ID, fmt.Sprintf("said: sent as #%d's next prompt; its answer will arrive as a message", kid))
		waitSaid(t, ag, parent.ID, "echo: again please") // its answer, back as a notice
	}
	t.Run("injected", func(t *testing.T) {
		steer(t, []string{"--steer"}, "steer me", "steered into #%d's running turn", "steered: use tabs")
	})
	t.Run("queued", func(t *testing.T) {
		steer(t, nil, "slow", "queued until #%d's current turn ends", "echo: use tabs")
	})
}

// A person's direct message to a coding agent the agent started is told to
// the parent as an hnote: no turn of its own, nothing for hasWork, and the
// parent's next turn reads it as a notice before its first message. The
// parent agent's own message doesn't make one, and waits while the child
// waits for a person (it must never answer that).
func TestHarnessDirectNote(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	f := fakeOf(ag)
	var kid int64
	f.on(turnOnly(lastIs("user", "go")), callTools(tc("n1", "subagent_spawn", `{"task":"hello","harness":"fake","wait":false,"label":"coder"}`))).once()
	f.on(turnOnly(lastIs("user", "nudge")), func(r LLMRequest) (LLMReply, error) {
		return callTools(tc("n2", "subagent_message", fmt.Sprintf(`{"id":%d,"text":"go ahead"}`, kid)))(r)
	}).once()
	f.on(turnOnly(lastIs("tool", "")), func(r LLMRequest) (LLMReply, error) { return say("said: " + toolText(r))(r) })
	parent := codingParent(t, mux, box, "go")
	kid = kidsOf(t, ag, parent.ID, 1)[0].ID
	hwait(t, "its answer delivered", func() bool {
		l := ag.db.latestLink(kid)
		return turnOver(ag, kid)() && l != nil && l.Delivered && statusOf(ag.db, parent.ID) == statusIdle
	})
	before := len(turnsOf(f, parent.ID))
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", kid), map[string]any{"text": "again"}); w.Code != 200 {
		t.Fatalf("a direct message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the direct message's turn", func() bool { return turnOver(ag, kid)() && strings.Contains(fullText(ag.db, kid), "echo: again") })
	notes := ag.db.inboxRows(`WHERE run_id=? AND kind=?`, parent.ID, inboxHNote)
	if len(notes) != 1 || notes[0].Body.Text != fmt.Sprintf("[direct message to #%d (Fake agent (tests)) from alice]\nagain", kid) || notes[0].DeliveredAt != 0 {
		t.Fatalf("the parent's note: %+v", notes)
	}
	if st := statusOf(ag.db, parent.ID); st != statusIdle || len(turnsOf(f, parent.ID)) != before || ag.db.hasWork() {
		t.Fatalf("the note started something: %s, %d turns, hasWork %v", st, len(turnsOf(f, parent.ID)), ag.db.hasWork())
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", kid), map[string]any{"text": "perm"}); w.Code != 200 {
		t.Fatalf("a direct message: %d %s", w.Code, w.Body)
	}
	p := parkOf(t, ag, kid, "approval")
	// the parent's nudge waits for the person's answer (it doesn't reject it)
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", parent.ID), map[string]any{"text": "nudge"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	waitSaid(t, ag, parent.ID, fmt.Sprintf("said: queued: #%d is waiting for a person to approve: run ls — it gets your message once they have; "+
		"its answer will arrive as a message", kid))
	// the notices came before the nudge, as notices (no request of their own)
	turns := turnsOf(f, parent.ID)
	var saw []string
	for _, m := range turns[before].Msgs {
		if m.Role == "user" {
			saw = append(saw, stripReminder(asText(m.Content)))
		}
	}
	if n := len(saw); n < 3 || !strings.HasSuffix(saw[n-3], "\nagain") || !strings.HasSuffix(saw[n-2], "\nperm") || saw[n-1] != "nudge" {
		t.Fatalf("the parent's turn read: %q", saw)
	}
	if asks, _ := ag.db.asks(parent.ID); len(asks) != 2 || asks[1].Text != "nudge" {
		t.Fatalf("the parent's asks: %+v", asks)
	}
	if r, _ := ag.db.getRun(kid); r.Status != statusWaiting || parsePending(r.Pending).Park != p.Park {
		t.Fatalf("the parent's message answered the park: %s %s", r.Status, r.Pending)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", kid), map[string]any{"approve": true, "park": p.Park}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	hwait(t, "the nudge delivered after", func() bool {
		return turnOver(ag, kid)() && strings.Contains(fullText(ag.db, kid), "echo: go ahead")
	})
	if n := len(ag.db.inboxRows(`WHERE run_id=? AND kind=?`, parent.ID, inboxHNote)); n != 2 {
		t.Fatalf("the parent's own message made a note: %d", n)
	}
}

// harness_mode narrows the owner's setting and never widens it; an explicit
// mode is never the model's, nor are system, after or a coding agent the
// sandbox doesn't offer.
func TestHarnessSpawnModes(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	if w := callAs(t, mux, asAlice, "PUT", "/prefs/harness-mode/fake", map[string]any{"mode": "auto"}); w.Code != 200 {
		t.Fatalf("the setting: %d %s", w.Code, w.Body)
	}
	f := fakeOf(ag)
	f.on(turnOnly(lastIs("user", "modes")), callTools(
		tc("m1", "subagent_spawn", `{"task":"hello","harness":"fake","harness_mode":"yolo","wait":false}`),
		tc("m2", "subagent_spawn", `{"task":"hello","harness":"fake","system":"be brief","wait":false}`),
		tc("m3", "subagent_spawn", `{"task":"hello","harness":"claude","wait":false}`),
		tc("m4", "subagent_spawn", `{"task":"hello","harness_mode":"plan","wait":false}`),
		tc("m5", "subagent_spawn", `{"task":"hello","harness":"fake","wait":false,"label":"owner's"}`),
		tc("m6", "subagent_spawn", `{"task":"hello","harness":"fake","harness_mode":"approve","wait":false,"label":"approve"}`),
		tc("m7", "subagent_spawn", `{"task":"hello","harness":"fake","harness_mode":"plan","wait":false,"label":"plan"}`),
	)).once()
	f.on(turnOnly(lastIs("tool", "")), say("done")).once()
	parent := codingParent(t, mux, box, "modes")
	hwait(t, "the spawns", func() bool { return len(turnsOf(f, parent.ID)) >= 2 })
	var results []string
	for _, m := range turnsOf(f, parent.ID)[1].Msgs {
		if m.Role == "tool" {
			results = append(results, stripReminder(asText(m.Content)))
		}
	}
	want := []string{
		"error: harness_mode: approve or plan",
		"error: system: a coding agent keeps its own instructions — leave system out with harness",
		"error: harness: " + box.Name + " doesn't offer Claude Code (it offers fake)",
		"error: harness_mode goes with harness",
	}
	if len(results) != 7 {
		t.Fatalf("results: %q", results)
	}
	for i, w := range want {
		if results[i] != w {
			t.Fatalf("spawn %d: %q, want %q", i+1, results[i], w)
		}
	}
	modes := map[string]string{}
	for _, k := range kidsOf(t, ag, parent.ID, 3) {
		cfg, _ := ag.db.runConfig(k.ID)
		modes[k.Title] = cfg.Harness.Mode
	}
	if modes["owner's"] != "auto" || modes["approve"] != "ask" || modes["plan"] != "ask" {
		t.Fatalf("the modes: %v", modes)
	}
	// after:[…] is the agent's own subagents'
	ts := &turnState{run: parent, root: parent.ID}
	ts.cfg, _ = ag.db.runConfig(parent.ID)
	if _, err := ag.eng.harnessSpawnOf(t.Context(), ts, map[string]any{"harness": "fake", "after": []any{float64(1)}}, nil); err == nil ||
		!strings.Contains(err.Error(), "after: a coding agent starts at once") {
		t.Fatalf("after: %v", err)
	}
	if m := ag.eng.harnessChildMode(parent.ID, acp.Fake(nil), ""); m != "auto" {
		t.Fatalf("the owner's setting: %q", m)
	}
}

// `harness` is offered only when the class holds the harness toolset and a
// sandbox the spawn may use offers a coding agent the class allows,
// reaching out.
func TestHarnessSpawnOffered(t *testing.T) {
	useClasses(t,
		agentClass{ID: "box", Name: "Box", Toolsets: []string{tsSandbox, tsSubagents}, SandboxEgress: []string{"none", "internet"}},
		agentClass{ID: "boxh", Name: "BoxH", Toolsets: []string{tsSandbox, tsSubagents, tsHarness}, SandboxEgress: []string{"none", "internet"},
			Harnesses: classSet{Names: []string{"fake", "codex"}}},
		agentClass{ID: "all", Name: "All", Toolsets: []string{tsSandbox, tsSubagents, tsHarness}, SandboxEgress: []string{"internet"},
			Harnesses: classSet{All: true}})
	enum := func(class string, active SandboxBinding, attached ...SandboxBinding) any {
		cfg := defaultConfig()
		cfg.Class, cfg.Sandbox, cfg.Attached = class, &active, append([]SandboxBinding{active}, attached...)
		props := spawnSpec(toolSpecs(cfg, 0, nil))
		if props == nil {
			t.Fatalf("%s: no subagent_spawn", class)
		}
		if _, ok := props["harness_mode"]; ok != (props["harness"] != nil) {
			t.Fatalf("%s: harness_mode without harness", class)
		}
		if h, ok := props["harness"].(map[string]any); ok {
			return h["enum"]
		}
		return nil
	}
	fake := SandboxBinding{Ref: "apps/cs|sb-1", Name: "a", Egress: "internet", Harnesses: []string{"fake", "claude"}}
	codex := SandboxBinding{Ref: "apps/cs|sb-2", Name: "b", Egress: "internet", Harnesses: []string{"codex"}}
	shut := SandboxBinding{Ref: "apps/cs|sb-3", Name: "c", Egress: "none", Harnesses: []string{"codex"}}
	old := SandboxBinding{Ref: "apps/cs|sb-4", Name: "d", Egress: "internet"}
	for _, c := range []struct {
		name string
		got  any
		want string
	}{
		{"a class without harness", enum("box", fake), "<nil>"},
		{"the class's own", enum("boxh", fake), "[fake]"},
		{"and an attached sandbox's", enum("boxh", fake, codex), "[fake codex]"},
		{"not one without egress", enum("boxh", shut), "<nil>"},
		{"nor an attached one without", enum("boxh", fake, shut), "[fake]"},
		{"none offered", enum("all", SandboxBinding{Ref: "apps/cs|sb-5", Egress: "internet", Harnesses: []string{}}), "<nil>"},
		{"a manager that says nothing: the catalog", enum("all", old), "[claude codex gemini opencode]"},
	} {
		if s := fmt.Sprint(c.got); s != c.want {
			t.Errorf("%s: %s, want %s", c.name, s, c.want)
		}
	}
	var buf bytes.Buffer
	cfg := defaultConfig()
	cfg.Class, cfg.Sandbox, cfg.Attached = "all", &fake, []SandboxBinding{fake}
	for _, s := range toolSpecs(cfg, 0, nil) {
		if s.Function.Name == "subagent_spawn" {
			_ = json.NewEncoder(&buf).Encode(s)
		}
	}
	if !strings.Contains(buf.String(), "With harness it starts a coding agent in the sandbox instead.") {
		t.Fatalf("the spawn's description: %s", buf.String())
	}
}
