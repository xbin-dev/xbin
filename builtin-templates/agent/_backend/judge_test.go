package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// D198: a coding agent's answered turn is judged by the memory tier; one
// that stopped part-way marks its conversation "not finished" — its row
// (list and stream), Needs you (reason "unfinished"), a push — until it is
// dismissed (for everyone) or a new turn starts; a delivered one marks
// nothing; with the feature off, or without a memory model, nothing is asked.
func TestJudgeUnfinishedTurn(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	log := withPushes(ag, 0)
	if w := callAs(t, mux, asMgr, "PUT", "/config", map[string]any{"models": map[string]any{"memory": "m-memory"}}); w.Code != 200 {
		t.Fatalf("config: %d %s", w.Code, w.Body)
	}
	f := fakeOf(ag)
	var judged atomic.Int32
	verdict := `{"delivered": false, "reason": "it only made a plan"}`
	f.on(func(r LLMRequest) bool { return r.Purpose == "judge" }, func(r LLMRequest) (LLMReply, error) {
		judged.Add(1)
		c := asString(r.Msgs[1].Content) // the ask, then the coding agent's answer (the fake echoes it)
		ask := strings.SplitN(strings.TrimPrefix(c, "The person's last request:\n"), "\n", 2)[0]
		if r.Model != "m-memory" || ask == "" || !strings.HasSuffix(c, "The agent's final message:\necho: "+ask) {
			t.Errorf("the judge's request: %s %+v", r.Model, r.Msgs)
		}
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: verdict}}, nil
	})
	run := askHarness(t, mux, box, "make the plan")
	hwait(t, "the verdict", func() bool { v := ag.db.verdictOf(run.ID); return v != nil && !v.Delivered })
	row := func() map[string]any {
		var d struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/conversations", nil).Body.Bytes(), &d)
		for _, it := range d.Items {
			if it["id"] == float64(run.ID) {
				return it
			}
		}
		t.Fatalf("no row for #%d", run.ID)
		return nil
	}
	if u, _ := row()["unfinished"].(map[string]any); u == nil || u["reason"] != "it only made a plan" {
		t.Fatalf("the row: %+v", row())
	}
	needs := func() []map[string]any {
		var d struct {
			Items []map[string]any `json:"items"`
		}
		_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/needs", nil).Body.Bytes(), &d)
		return d.Items
	}
	if n := needs(); len(n) != 1 || n[0]["reason"] != needUnfinished {
		t.Fatalf("needs: %+v", n)
	}
	hwait(t, "a push", func() bool { return len(log.all()) == 1 })
	if p := log.all()[0]; p.Kind != needUnfinished || p.User != "alice" || !strings.Contains(p.Body, "stopped before finishing: it only made a plan") {
		t.Fatalf("the push: %+v", p)
	}
	// dismissed: for everyone, the row and Needs you clear
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/unfinished/dismiss", run.ID), nil); w.Code != 200 {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body)
	}
	if u := row()["unfinished"]; u != nil {
		t.Fatalf("dismissed, the row still says: %v", u)
	}
	if n := needs(); len(n) != 0 {
		t.Fatalf("dismissed, needs: %+v", n)
	}
	// a delivered turn marks nothing; a new turn took the old verdict down first
	verdict = `{"delivered": true, "reason": "done"}`
	before := judged.Load()
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "go on"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the next verdict", func() bool { v := ag.db.verdictOf(run.ID); return judged.Load() > before && v != nil && v.Delivered })
	if u := row()["unfinished"]; u != nil || len(needs()) != 0 {
		t.Fatalf("delivered: %v %+v", u, needs())
	}
	// the feature off: not asked
	if w := callAs(t, mux, asMgr, "PUT", "/config", map[string]any{"features": map[string]bool{"judge": false}}); w.Code != 200 {
		t.Fatalf("config: %d %s", w.Code, w.Body)
	}
	off := askHarness(t, mux, box, "another plan")
	hwait(t, "its turn", turnOver(ag, off.ID))
	n := judged.Load()
	ag.eng.judge(off.ID) // as the turn end would, synchronously: nothing
	if judged.Load() != n || ag.db.verdictOf(off.ID) != nil {
		t.Fatalf("judged with the feature off")
	}
}

func TestParseVerdict(t *testing.T) {
	for _, c := range []struct {
		in        string
		delivered bool
		reason    string
		ok        bool
	}{
		{`{"delivered": false, "reason": "asks which DB to use"}`, false, "asks which DB to use", true},
		{"```json\n{\"delivered\":true,\"reason\":\"done\"}\n```", true, "done", true},
		{"Here: {\"delivered\": false, \"reason\": \"  stopped\\n mid-way \"}", false, "stopped mid-way", true},
		{"no — it only planned", false, "it only planned", true},
		{"Yes.", true, "", true},
		{"maybe", false, "", false},
		{`{"reason": "no verdict"}`, false, "", false},
	} {
		d, r, ok := parseVerdict(c.in)
		if d != c.delivered || r != c.reason || ok != c.ok {
			t.Errorf("%q: %v %q %v", c.in, d, r, ok)
		}
	}
}
