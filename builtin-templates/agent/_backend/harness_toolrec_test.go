package main

// The tool record's rules as this backend applies them (sdk/acp's
// ToolRecord, StartsToolRecord — docs/sdk.md "The session rules"): the
// halves the shared transcripts don't reach.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// toolRows are the run's tool rows as GET /runs/{id}/view shows them, by
// stored call id (after a durable step: a streamed output is stored then).
func toolRows(t *testing.T, mux *http.ServeMux, s *hsess) map[string]map[string]any {
	t.Helper()
	if err := s.commit(nil, nil); err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]any{}
	for _, m := range msgsOf(viewOf(t, mux, s.run)) {
		if m["role"] == "tool" {
			rows[m["toolCallId"].(string)] = m
		}
	}
	return rows
}

func feedEv(typ, data string) acp.Event { return acp.Event{Type: typ, Data: json.RawMessage(data)} }

// An id used again in a later turn is a new call even when the earlier one
// never said it ended (the Agent tab's half of the rule; this backend had
// only "ended → new"): the earlier call keeps its rows.
func TestHarnessCallIDOfAnEarlierTurn(t *testing.T) {
	ag, mux, run, s := chatFeed(t, "id of an earlier turn")
	chatPrompt(t, ag, run.ID, "one")
	s.apply(feedEv(acp.EvToolCall, `{"id":"bg","title":"sleep 600","kind":"execute","status":"in_progress"}`))
	s.apply(feedEv(acp.EvToolCall, `{"id":"bg","title":"sleep 600 &"}`)) // said again while it runs, the same turn: the same call
	if rows := toolRows(t, mux, s); len(rows) != 1 || rows["h1:bg"]["acp"].(map[string]any)["title"] != "sleep 600 &" {
		t.Fatalf("one call, re-announced: %v", rows)
	}
	s.apply(feedEv(acp.EvTurnEnd, `{"turn":1,"stopReason":"end_turn"}`))
	chatPrompt(t, ag, run.ID, "two")
	s.apply(feedEv(acp.EvToolUpdate, `{"id":"bg","outputDelta":"tick\n"}`)) // an update never starts a second record
	if rows := toolRows(t, mux, s); len(rows) != 1 || rows["h1:bg"]["acp"].(map[string]any)["output"] != "tick\n" {
		t.Fatalf("an update of the next turn lands on the call: %v", rows)
	}
	s.apply(feedEv(acp.EvToolCall, `{"id":"bg","title":"ls","kind":"execute","status":"pending"}`))
	s.apply(feedEv(acp.EvToolUpdate, `{"id":"bg","status":"completed","output":"a.txt\n","exitCode":0}`))
	rows := toolRows(t, mux, s)
	first, second := rows["h1:bg"], rows["h1:bg#2"]
	if len(rows) != 2 || first == nil || second == nil {
		t.Fatalf("two calls, one per turn: %v", rows)
	}
	if a := first["acp"].(map[string]any); a["title"] != "sleep 600 &" || a["status"] != "in_progress" || a["output"] != "tick\n" {
		t.Fatalf("the earlier turn's call kept what it was: %v", a)
	}
	if a := second["acp"].(map[string]any); a["title"] != "ls" || a["status"] != "completed" || a["output"] != "a.txt\n" ||
		!strings.Contains(second["content"].(string), "[exit 0]") {
		t.Fatalf("the new call: %v", second)
	}
}

// What a record keeps here (hToolPolicy): the newest hOutputMax bytes of a
// call's output, cut between characters, the dropped ones counted; a
// result's tail likewise; a kind ACP doesn't know reads as other.
func TestHarnessToolRecordPolicy(t *testing.T) {
	ag, mux, run, s := chatFeed(t, "policy")
	chatPrompt(t, ag, run.ID, "go")
	s.apply(feedEv(acp.EvToolCall, `{"id":"big","title":"yes","kind":"teleport","status":"in_progress"}`))
	chunk, _ := json.Marshal(strings.Repeat("€", hOutputMax/3)) // 3 bytes a character: the cut can't split one
	for i := 0; i < 2; i++ {
		s.apply(feedEv(acp.EvToolUpdate, `{"id":"big","outputDelta":`+string(chunk)+`}`))
	}
	s.apply(feedEv(acp.EvToolUpdate, `{"id":"big","outputDelta":"ab"}`))
	s.apply(feedEv(acp.EvToolUpdate, `{"id":"big","status":"completed"}`))
	row := toolRows(t, mux, s)["h1:big"]
	a := row["acp"].(map[string]any)
	out := a["output"].(string)
	if a["kind"] != "other" || row["name"] != "acp:other" {
		t.Fatalf("an unknown kind: %v %v", a["kind"], row["name"])
	}
	if len(out) > hOutputMax || !strings.HasPrefix(out, "€") || !strings.HasSuffix(out, "€ab") || strings.ContainsRune(out, '�') {
		t.Fatalf("the kept output: %d bytes, %q … %q", len(out), out[:6], out[len(out)-8:])
	}
	total := 2*3*(hOutputMax/3) + 2
	if got := int(a["outputTruncated"].(float64)); got != total-len(out) || got == 0 {
		t.Fatalf("outputTruncated %d, want %d", got, total-len(out))
	}
	if c := row["content"].(string); len(c) > hResultTail || strings.ContainsRune(c, '�') || !strings.HasSuffix(c, "€ab") {
		t.Fatalf("the result's tail: %d bytes", len(c))
	}
}

// A permission request for a call the adapter has not announced makes the
// call from what the request says of it — its arguments and content too.
func TestHarnessCallAnnouncedByItsPermission(t *testing.T) {
	ag, _, run, s := chatFeed(t, "asked first")
	chatPrompt(t, ag, run.ID, "go")
	tc := acp.ToolCallRef{ID: "plan1", Title: "Ready to code?", Kind: acp.KindSwitchMode,
		Content: json.RawMessage(`[{"type":"content","content":{"type":"text","text":"1. do it"}}]`)}
	opts := []acp.PermissionOption{{OptionID: "yes", Name: "Yes", Kind: acp.AllowOnce}, {OptionID: "no", Name: "No", Kind: acp.RejectOnce}}
	rpc := json.RawMessage(`"rpc-1"`)
	pd, _ := s.perms.Request(tc, opts, rpc)
	ev := acp.NewEvent(acp.EvPermissionRequest, map[string]any{"pid": pd.PID, "toolCall": tc, "options": opts})
	ev.Wire = &acp.Wire{RPCID: rpc}
	s.apply(ev)
	r, err := ag.db.getRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := parsePending(r.Pending)
	if p.Harness == nil || !p.Harness.PlanApproval || p.Harness.Plan != "1. do it" {
		t.Fatalf("the plan approval's park: %+v", p.Harness)
	}
}
