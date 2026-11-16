package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The shared transcripts (testdata/transcripts): every log folded by
// ToolRecords gives exactly the records beside it (<name>.records.json —
// written by the rules' reference, hack/chat-fixtures/reference.mjs, and
// held to by the browser's fold too).
func TestToolRecordsMatchTheSharedFixtures(t *testing.T) {
	logs, _ := filepath.Glob("testdata/transcripts/*.json")
	n := 0
	for _, f := range logs {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if strings.Contains(name, ".") {
			continue // an expectation, not a log
		}
		n++
		t.Run(name, func(t *testing.T) {
			var log struct {
				Events []Event `json:"events"`
			}
			readJSON(t, f, &log)
			var want struct {
				Records []any `json:"records"`
			}
			readJSON(t, filepath.Join("testdata/transcripts", name+".records.json"), &want)
			var recs ToolRecords
			for _, e := range log.Events {
				recs.Apply(e)
			}
			raw, err := json.Marshal(recs.Entries())
			if err != nil {
				t.Fatal(err)
			}
			var got []any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want.Records) {
				t.Fatalf("%d records, want %d\n%s", len(got), len(want.Records), raw)
			}
			for i := range got {
				if !reflect.DeepEqual(got[i], want.Records[i]) {
					g, _ := json.MarshalIndent(got[i], "", " ")
					w, _ := json.MarshalIndent(want.Records[i], "", " ")
					t.Errorf("record %d:\n got %s\nwant %s", i, g, w)
				}
			}
		})
	}
	if n < 8 {
		t.Fatalf("%d transcripts under testdata/transcripts, want the 8 shared ones", n)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func toolEv(typ string, data string) Event { return Event{Type: typ, Data: json.RawMessage(data)} }

// Rule 5: which event begins a record.
func TestStartsToolRecord(t *testing.T) {
	rec := func(status string) *ToolRecord { return &ToolRecord{ID: "t", Status: status} }
	for _, c := range []struct {
		name     string
		typ      string
		held     *ToolRecord
		sameTurn bool
		want     bool
	}{
		{"no record yet: a call", EvToolCall, nil, true, true},
		{"no record yet: an update", EvToolUpdate, nil, true, true},
		{"a call again, still running", EvToolCall, rec(ToolInProgress), true, false},
		{"a call for an id that completed", EvToolCall, rec(ToolCompleted), true, true},
		{"a call for an id that failed", EvToolCall, rec(ToolFailed), true, true},
		{"a call for an id that was cancelled", EvToolCall, rec(ToolCancelled), true, true},
		{"a call for an id of an earlier turn, never finished", EvToolCall, rec(ToolPending), false, true},
		{"an update never starts a second record: finished", EvToolUpdate, rec(ToolCompleted), true, false},
		{"an update never starts a second record: an earlier turn", EvToolUpdate, rec(ToolInProgress), false, false},
	} {
		if got := StartsToolRecord(c.typ, c.held, c.sameTurn); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestToolRecordsIDReuse(t *testing.T) {
	var r ToolRecords
	steps := []struct {
		ev      Event
		isNew   bool
		n, turn int
	}{
		{toolEv(EvToolCall, `{"id":"a","status":"pending","title":"one"}`), true, 1, 1},
		{toolEv(EvToolUpdate, `{"id":"a","status":"completed"}`), false, 1, 1},
		{toolEv(EvToolUpdate, `{"id":"a","title":"late"}`), false, 1, 1},                // an update lands on the finished record
		{toolEv(EvToolCall, `{"id":"a","status":"pending","title":"two"}`), true, 2, 1}, // the id again, same turn, the first over
		{toolEv(EvToolCall, `{"id":"a","title":"two again"}`), false, 2, 1},             // still running: the same record
		{toolEv(EvTurnEnd, `{"turn":1,"stopReason":"end_turn"}`), false, 0, 0},
		{toolEv(EvToolUpdate, `{"id":"a","status":"in_progress"}`), false, 2, 1},          // an update of the next turn
		{toolEv(EvToolCall, `{"id":"a","status":"pending","title":"three"}`), true, 3, 2}, // an earlier turn's id, never finished
		{toolEv(EvTurnEnd, `{"stopReason":"end_turn"}`), false, 0, 0},                     // no turn number: the next one all the same
		{toolEv(EvToolCall, `{"id":"a","status":"pending","title":"four"}`), true, 4, 3},
		{toolEv(EvToolUpdate, `{"id":"b","status":"in_progress"}`), true, 1, 3}, // an update for an id never seen is its record
		{toolEv(EvToolCall, `{"status":"pending"}`), false, 0, 0},               // names no call
	}
	for i, s := range steps {
		e, isNew := r.Apply(s.ev)
		if isNew != s.isNew {
			t.Fatalf("step %d: new %v, want %v", i, isNew, s.isNew)
		}
		if s.n == 0 {
			if e != nil {
				t.Fatalf("step %d: an entry for an event that names no call", i)
			}
			continue
		}
		if e == nil || e.N != s.n || e.Turn != s.turn {
			t.Fatalf("step %d: entry %+v, want call %d of turn %d", i, e, s.n, s.turn)
		}
	}
	if got := len(r.Entries()); got != 5 {
		t.Fatalf("%d records, want 5", got)
	}
	if e := r.Entries()[0]; e.Rec.Title != "late" || e.Rec.Status != ToolCompleted {
		t.Fatalf("the first record: %+v", e.Rec)
	}
	if e := r.Newest("a"); e.Rec.Title != "four" {
		t.Fatalf("newest of a: %+v", e.Rec)
	}
	if r.Newest("nope") != nil {
		t.Fatal("a record for an id never seen")
	}
}

// Rules 1–3: what an event's fields do to a record.
func TestToolRecordMerge(t *testing.T) {
	apply := func(p ToolPolicy, evs ...string) *ToolRecord {
		t.Helper()
		r := NewToolRecords(p)
		var e *ToolEntry
		for i, d := range evs {
			typ := EvToolUpdate
			if i == 0 {
				typ = EvToolCall
			}
			if e, _ = r.Apply(toolEv(typ, d)); e == nil {
				t.Fatalf("event %d names no call: %s", i, d)
			}
		}
		return e.Rec
	}
	str := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	for _, c := range []struct {
		name string
		p    ToolPolicy
		evs  []string
		want string // the record's JSON
	}{
		{"an empty call", ToolPolicy{}, []string{`{"id":"t"}`}, `{"id":"t","kind":"other","title":"","status":"pending"}`},
		{"an unknown kind reads as other; a known one stays", ToolPolicy{},
			[]string{`{"id":"t","kind":"teleport","title":"x"}`}, `{"id":"t","kind":"other","title":"x","status":"pending"}`},
		{"fields replace; parent is the first one said; flags stick", ToolPolicy{},
			[]string{`{"id":"t","kind":"execute","title":"a","label":"l1","name":"Bash","parent":"p1","subagent":true,"planReview":true,"rawInput":{"command":"ls"}}`,
				`{"id":"t","title":"b","label":"l2","parent":"p2","status":"completed","rawInput":null,"exitCode":0}`},
			`{"id":"t","kind":"execute","title":"b","label":"l2","name":"Bash","status":"completed","parent":"p1","subagent":true,"planReview":true,"rawInput":{"command":"ls"},"exitCode":0}`},
		{"a field in a shape we don't know is dropped alone", ToolPolicy{},
			[]string{`{"id":"t","title":"kept","label":7,"status":"in_progress"}`}, `{"id":"t","kind":"other","title":"kept","status":"in_progress"}`},
		{"content: text joined by a blank line, terminal, a later list without text keeps it", ToolPolicy{},
			[]string{`{"id":"t","content":[{"type":"content","content":{"type":"text","text":"one"}},{"type":"text","text":"two"},{"type":"content","content":{"type":"text","text":""}},{"type":"terminal","terminalId":"x"}]}`,
				`{"id":"t","content":[{"type":"content","content":{"type":"image"}}]}`},
			`{"id":"t","kind":"other","title":"","status":"pending","text":"one\n\ntwo","terminal":true}`},
		{"content: a later list without diffs keeps the diffs (K7); files are locations then diffs, each once", ToolPolicy{},
			[]string{`{"id":"t","kind":"edit","locations":[{"path":"/w/a","line":3},{"path":""},{"path":"/w/b"}],"content":[{"type":"diff","path":"/w/b","oldText":"x\n","newText":"x\ny\n"},{"type":"diff","path":"/w/c","newText":"n\n"}]}`,
				`{"id":"t","status":"completed","content":[{"type":"content","content":{"type":"text","text":"done"}}]}`},
			`{"id":"t","kind":"edit","title":"","status":"completed","locations":[{"path":"/w/a","line":3},{"path":"/w/b"}],"files":["/w/a","/w/b","/w/c"],"text":"done",` +
				`"diffs":[{"path":"/w/b","status":"modified","add":1,"oldText":"x\n","newText":"x\ny\n"},{"path":"/w/c","status":"added","add":1,"newText":"n\n"}]}`},
		{"a delete's diff: deleted, every old line", ToolPolicy{},
			[]string{`{"id":"t","kind":"delete","content":[{"type":"diff","path":"/w/a","oldText":"1\n2\n","newText":""}]}`},
			`{"id":"t","kind":"delete","title":"","status":"pending","files":["/w/a"],"diffs":[{"path":"/w/a","status":"deleted","del":2,"oldText":"1\n2\n"}]}`},
		{"output: deltas append, output replaces", ToolPolicy{},
			[]string{`{"id":"t","outputDelta":"a"}`, `{"id":"t","outputDelta":"b"}`, `{"id":"t","output":"whole"}`, `{"id":"t","outputDelta":"+"}`},
			`{"id":"t","kind":"other","title":"","status":"pending","output":"whole+"}`},
		{"a capping reducer keeps the newest bytes, cut between characters", ToolPolicy{OutputMax: 5},
			[]string{`{"id":"t","outputDelta":"abc"}`, `{"id":"t","outputDelta":"dé€"}`},
			`{"id":"t","kind":"other","title":"","status":"pending","output":"é€","outputTruncated":4}`},
		{"a capping reducer: output replaces the count too", ToolPolicy{OutputMax: 4},
			[]string{`{"id":"t","outputDelta":"abcdefgh"}`, `{"id":"t","output":"xy"}`},
			`{"id":"t","kind":"other","title":"","status":"pending","output":"xy"}`},
		{"text is cut on a character", ToolPolicy{TextMax: 4},
			[]string{`{"id":"t","content":[{"type":"content","content":{"type":"text","text":"abcédef"}}]}`},
			`{"id":"t","kind":"other","title":"","status":"pending","text":"abc…"}`},
		{"a reducer's own diff form", ToolPolicy{Diff: func(path string, old *string, newText string, deleted bool) ToolDiff {
			return ToolDiff{Path: path, Status: "modified", Add: 9, Patch: "@@ " + *old + newText}
		}},
			[]string{`{"id":"t","content":[{"type":"diff","path":"p","oldText":"a","newText":"b"}]}`},
			`{"id":"t","kind":"other","title":"","status":"pending","files":["p"],"diffs":[{"path":"p","status":"modified","add":9,"patch":"@@ ab"}]}`},
	} {
		if got := str(apply(c.p, c.evs...)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestToolRecordChanged(t *testing.T) {
	var r ToolRecords
	r.Apply(toolEv(EvToolCall, `{"id":"run","status":"completed"}`))
	r.Apply(toolEv(EvTurnEnd, `{"turn":1}`))
	if e, _ := r.Apply(toolEv(EvFilesChanged, `{"turn":1,"changes":[{"path":"a","status":"added","add":1,"del":0}]}`)); e != nil {
		t.Fatal("a turn's files.changed is no call's")
	}
	e, isNew := r.Apply(toolEv(EvFilesChanged, `{"toolCallId":"run","changes":[{"path":"a","status":"added","add":1,"del":0}],"patch":{"format":"git_patch","text":"+a\n","truncated":true}}`))
	if e == nil || isNew || e.Rec.Changed == nil {
		t.Fatalf("the call's files.changed after its turn: %+v", e)
	}
	b, _ := json.Marshal(e.Rec.Changed)
	if want := `{"files":[{"path":"a","status":"added","add":1}],"patch":{"text":"+a\n","truncated":true}}`; string(b) != want {
		t.Fatalf("changed: %s\nwant    %s", b, want)
	}
	if e, _ := r.Apply(toolEv(EvFilesChanged, `{"toolCallId":"other","changes":[]}`)); e != nil {
		t.Fatal("a files.changed for a call never seen")
	}
}

func TestOutputChunk(t *testing.T) {
	chunk := func(data string) (OutputChunk, bool) {
		d, ok := ParseToolEvent(json.RawMessage(data))
		if !ok {
			return OutputChunk{}, false
		}
		return d.OutputChunk()
	}
	for data, want := range map[string]bool{
		`{"id":"t","outputDelta":"x"}`:                         true,
		`{"id":"t","outputDelta":"x","parent":"p"}`:            true,
		`{"id":"t","outputDelta":""}`:                          false,
		`{"id":"t","outputDelta":"x","status":"in_progress"}`:  false,
		`{"id":"t","outputDelta":"x","exitCode":0}`:            false,
		`{"id":"t","output":"x"}`:                              false,
		`{"outputDelta":"x"}`:                                  false,
		`[1]`:                                                  false,
		`{"id":"t","outputDelta":"x","somethingNew":{"a":[]}}`: false,
	} {
		if _, ok := chunk(data); ok != want {
			t.Errorf("%s: a bare chunk %v, want %v", data, ok, want)
		}
	}
	a, _ := chunk(`{"id":"t","outputDelta":"one "}`)
	b, _ := chunk(`{"id":"t","outputDelta":"two"}`)
	if !a.Add(b) || a.OutputDelta != "one two" {
		t.Fatalf("two chunks of one call: %+v", a)
	}
	for _, other := range []string{`{"id":"u","outputDelta":"x"}`, `{"id":"t","outputDelta":"x","parent":"p"}`} {
		o, _ := chunk(other)
		if a.Add(o) || a.OutputDelta != "one two" {
			t.Fatalf("merged with another's chunk %s: %+v", other, a)
		}
	}
	if _, ok := (ToolEvent{ID: "t", OutputDelta: "x"}).OutputChunk(); ok {
		t.Fatal("a ToolEvent built by hand has no fields to judge by")
	}
}

func TestLineCounts(t *testing.T) {
	for _, c := range []struct {
		a, b     string
		add, del int
	}{
		{"", "", 0, 0},
		{"", "a\nb\n", 2, 0},
		{"a\nb\n", "", 0, 2},
		{"a\nb\nc\n", "a\nB\nc\n", 1, 1},
		{"a\nb\nc\n", "a\nc\n", 0, 1},
		{"a\nc", "a\nb\nc", 1, 0},                 // no final newline
		{"1\n2\n3\n4\n", "0\n1\n3\n5\n4\n", 2, 1}, // an LCS between head and tail
		{"x\ny\n", "y\nx\n", 1, 1},
	} {
		if add, del := lineCounts(textLines(c.a), textLines(c.b)); add != c.add || del != c.del {
			t.Errorf("%q → %q: +%d −%d, want +%d −%d", c.a, c.b, add, del, c.add, c.del)
		}
	}
}

func TestOutputTail(t *testing.T) {
	r := &ToolRecord{Output: "aé€z"}
	for n, want := range map[int]string{0: "", 1: "z", 2: "z", 4: "€z", 6: "é€z", 7: "aé€z", 99: "aé€z"} {
		if got := r.OutputTail(n); got != want {
			t.Errorf("tail %d: %q, want %q", n, got, want)
		}
	}
}
