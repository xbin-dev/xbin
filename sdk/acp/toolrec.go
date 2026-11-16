package acp

// The tool record: one tool call as its tool.call and tool.update events
// say it, merged. Every consumer of a session's events keeps this record —
// a transcript's tool card, a store of rows, a pager looking for the event
// that opened a card — and the rules are the same for all of them (the
// served docs: docs/sdk.md "The session rules"; the shared fixtures under
// testdata/transcripts hold this file, the browser's /vendor/chat/tools.js
// and the Swift port to one reading):
//
//  1. A field an event carries replaces the record's. outputDelta appends
//     to the output; output replaces what was gathered. parent is the
//     first one said. An unknown kind reads as "other".
//  2. content (ACP's list) is read into Text (its text items, joined by a
//     blank line), Diffs (its diff items) and Terminal; a list with no
//     text keeps the text there was, one with no diffs keeps the diffs.
//  3. Files is the paths of the locations, then of the diffs, each once.
//  4. A diff is "added" when it has no old text, "deleted" when the call is
//     a delete, else "modified"; Add and Del count the lines the two texts
//     do not share.
//  5. An id used again: a tool.call for an id whose record is over, or was
//     made in an earlier turn, starts a new record (StartsToolRecord). A
//     tool.update never starts a second one.
//  6. A files.changed event naming a call sets the newest record of that
//     id's Changed (the turn may be over).
//  7. What a reducer keeps is its own choice (ToolPolicy): all of the
//     output, or its newest bytes — cut between characters, the bytes
//     dropped counted in OutputTruncated.
//
// Nothing here does I/O or keeps a clock: a ToolRecords is a fold over the
// events it is given.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// EvFilesChanged is an embedder's event, not a Client's: what a call (data
// toolCallId) or a turn (data turn) changed on disk, as xbind's snapshots
// report it ({changes, patch?, toolCallId? | turn?}; docs/protocol/agents.md).
// A ToolRecords reads the per-call ones into ToolRecord.Changed.
const EvFilesChanged = "files.changed"

// Tool call statuses (ACP's ToolCallStatus).
const (
	ToolPending    = "pending"
	ToolInProgress = "in_progress"
	ToolCompleted  = "completed"
	ToolFailed     = "failed"
	ToolCancelled  = "cancelled"
)

// toolKinds are ACP's tool kinds; anything else reads as "other".
var toolKinds = map[string]bool{"read": true, "edit": true, "delete": true, "move": true, "search": true, "execute": true,
	"think": true, "fetch": true, KindSwitchMode: true, "other": true}

// ToolDone reports whether a tool call's status is final.
func ToolDone(status string) bool {
	return status == ToolCompleted || status == ToolFailed || status == ToolCancelled
}

// ToolRecord is one tool call: its tool.call and tool.update events merged.
// The JSON form leaves a field at its zero value out, except id, kind,
// title and status.
type ToolRecord struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"` // an ACP tool kind; "other" when unknown
	Title           string          `json:"title"`
	Label           string          `json:"label,omitempty"` // the adapter's own description (shown instead of the title)
	Name            string          `json:"name,omitempty"`  // the programmatic tool name (Bash, Task, ExitPlanMode)
	Status          string          `json:"status"`          // pending | in_progress | completed | failed | cancelled
	Parent          string          `json:"parent,omitempty"`
	Subagent        bool            `json:"subagent,omitempty"`
	PlanReview      bool            `json:"planReview,omitempty"`
	RawInput        json.RawMessage `json:"rawInput,omitempty"` // the tool's arguments as the agent sent them
	Locations       []ToolLocation  `json:"locations,omitempty"`
	Files           []string        `json:"files,omitempty"` // the locations' paths, then the diffs', each once
	Text            string          `json:"text,omitempty"`  // its text content, the items joined by a blank line
	Terminal        bool            `json:"terminal,omitempty"`
	Output          string          `json:"output,omitempty"`          // terminal output, raw (ANSI kept)
	OutputTruncated int64           `json:"outputTruncated,omitempty"` // bytes dropped before Output
	ExitCode        *int            `json:"exitCode,omitempty"`
	Diffs           []ToolDiff      `json:"diffs,omitempty"`
	Changed         *ChangeSet      `json:"changed,omitempty"` // an embedder's snapshot diff (EvFilesChanged)
}

// ToolLocation is a place a call touches.
type ToolLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// ToolDiff is one file a call edits: the two texts (OldText nil: a new
// file), or — from a reducer whose ToolPolicy.Diff makes one — a unified
// Patch (Truncated: cut at a hunk).
type ToolDiff struct {
	Path      string  `json:"path"`
	Status    string  `json:"status"` // added | modified | deleted
	Add       int     `json:"add,omitempty"`
	Del       int     `json:"del,omitempty"`
	Patch     string  `json:"patch,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	OldText   *string `json:"oldText,omitempty"`
	NewText   string  `json:"newText,omitempty"`
}

// ChangeSet is what a call changed on disk (EvFilesChanged).
type ChangeSet struct {
	Files []FileChange `json:"files"`
	Patch *ChangePatch `json:"patch,omitempty"`
}

// FileChange is one file of a ChangeSet.
type FileChange struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"` // a rename's source
	Status  string `json:"status"`            // added | modified | deleted | renamed | typechange
	Add     int    `json:"add,omitempty"`
	Del     int    `json:"del,omitempty"`
	Binary  bool   `json:"binary,omitempty"`
}

// ChangePatch is a ChangeSet's patch text.
type ChangePatch struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// NewToolRecord is an empty record for the call id: kind "other", pending.
func NewToolRecord(id string) *ToolRecord {
	return &ToolRecord{ID: id, Kind: "other", Status: ToolPending}
}

// Done reports whether the call is over.
func (r *ToolRecord) Done() bool { return ToolDone(r.Status) }

// OutputTail is the newest n bytes of the output, cut between characters.
func (r *ToolRecord) OutputTail(n int) string { return tailBytes(r.Output, n) }

// ToolPolicy is what a reducer keeps of a call. The zero value keeps
// everything, diffs as their two texts.
type ToolPolicy struct {
	// OutputMax is how many bytes of terminal output a record keeps — the
	// newest; 0 keeps all.
	OutputMax int
	// TextMax bounds Text (bytes; cut on a character with an ellipsis); 0
	// keeps all.
	TextMax int
	// Diff makes a record's ToolDiff from an edit's texts (old nil: a new
	// file; deleted: the call removes the file) — a reducer that stores a
	// patch instead of two texts. nil: both texts, the lines counted.
	Diff func(path string, old *string, newText string, deleted bool) ToolDiff
}

// ToolEvent is a tool.call or tool.update event's data (ParseToolEvent).
type ToolEvent struct {
	ID          string          `json:"id"`
	Title       *string         `json:"title,omitempty"`
	Kind        *string         `json:"kind,omitempty"`
	Status      string          `json:"status,omitempty"`
	Content     json.RawMessage `json:"content,omitempty"`
	Locations   json.RawMessage `json:"locations,omitempty"`
	RawInput    json.RawMessage `json:"rawInput,omitempty"`
	Name        string          `json:"name,omitempty"`
	Label       string          `json:"label,omitempty"`
	Parent      string          `json:"parent,omitempty"`
	Subagent    bool            `json:"subagent,omitempty"`
	PlanReview  bool            `json:"planReview,omitempty"`
	Output      *string         `json:"output,omitempty"`
	OutputDelta string          `json:"outputDelta,omitempty"`
	ExitCode    *int            `json:"exitCode,omitempty"`

	fields []string // the keys the event's data had, as sent
}

// ParseToolEvent reads a tool.call / tool.update event's data; false when
// it is not an object naming a call. A field in a shape this package does
// not know is left out alone (the ACP extensibility rule).
func ParseToolEvent(data json.RawMessage) (ToolEvent, bool) {
	var d ToolEvent
	var keys map[string]json.RawMessage
	if json.Unmarshal(data, &keys) != nil || len(keys["id"]) == 0 {
		return d, false
	}
	if err := json.Unmarshal(data, &d); err != nil {
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			return ToolEvent{}, false
		}
	}
	d.fields = make([]string, 0, len(keys))
	for k := range keys {
		d.fields = append(d.fields, k)
	}
	return d, d.ID != ""
}

// Fields are the keys the event's data carried (ParseToolEvent), in no
// order; nil for a ToolEvent built by hand.
func (d ToolEvent) Fields() []string { return d.fields }

// OutputChunk is a tool.update that carries nothing but output (and the
// subagent call it runs under): what a log may merge with the next chunk of
// the same call, exactly as a record does with the two.
type OutputChunk struct {
	ID          string `json:"id"`
	OutputDelta string `json:"outputDelta"`
	Parent      string `json:"parent,omitempty"`
}

// OutputChunk is d as a bare output chunk; false when d says anything else.
func (d ToolEvent) OutputChunk() (OutputChunk, bool) {
	if d.ID == "" || d.OutputDelta == "" || len(d.fields) == 0 {
		return OutputChunk{}, false
	}
	for _, k := range d.fields {
		if k != "id" && k != "outputDelta" && k != "parent" {
			return OutputChunk{}, false
		}
	}
	return OutputChunk{ID: d.ID, OutputDelta: d.OutputDelta, Parent: d.Parent}, true
}

// Add appends next to c when both are of one call (and one subagent);
// false: next is another's — c is unchanged.
func (c *OutputChunk) Add(next OutputChunk) bool {
	if c.ID != next.ID || c.Parent != next.Parent {
		return false
	}
	c.OutputDelta += next.OutputDelta
	return true
}

// StartsToolRecord is rule 5: whether a tool event of type evType
// (EvToolCall or EvToolUpdate) begins a new record rather than updating
// held, the newest record of its id (nil: none). sameTurn: held was made in
// the turn that runs now.
func StartsToolRecord(evType string, held *ToolRecord, sameTurn bool) bool {
	return held == nil || (evType == EvToolCall && (held.Done() || !sameTurn))
}

// Merge folds one event into the record (rules 1–4 and 7).
func (r *ToolRecord) Merge(d ToolEvent, p ToolPolicy) {
	if d.Title != nil {
		r.Title = *d.Title
	}
	if d.Kind != nil {
		r.Kind = "other"
		if toolKinds[*d.Kind] {
			r.Kind = *d.Kind
		}
	}
	if d.Status != "" {
		r.Status = d.Status
	}
	if d.Name != "" {
		r.Name = d.Name
	}
	if d.Label != "" {
		r.Label = d.Label
	}
	if d.Parent != "" && r.Parent == "" {
		r.Parent = d.Parent
	}
	r.Subagent = r.Subagent || d.Subagent
	r.PlanReview = r.PlanReview || d.PlanReview
	if raw := bytes.TrimSpace(d.RawInput); len(raw) > 0 && string(raw) != "null" {
		r.RawInput = append(json.RawMessage(nil), raw...)
	}
	if locs, ok := toolLocations(d.Locations); ok {
		r.Locations = locs
	}
	r.mergeContent(d.Content, p)
	if d.Output != nil {
		r.Output, r.OutputTruncated = "", 0
		r.addOutput(*d.Output, p.OutputMax)
	}
	if d.OutputDelta != "" {
		r.addOutput(d.OutputDelta, p.OutputMax)
	}
	if d.ExitCode != nil {
		c := *d.ExitCode
		r.ExitCode = &c
	}
	r.Files = nil
	add := func(path string) {
		if path == "" {
			return
		}
		for _, f := range r.Files {
			if f == path {
				return
			}
		}
		r.Files = append(r.Files, path)
	}
	for _, l := range r.Locations {
		add(l.Path)
	}
	for _, df := range r.Diffs {
		add(df.Path)
	}
}

// toolLocations reads an event's locations (ok false: it carried no list).
func toolLocations(raw json.RawMessage) ([]ToolLocation, bool) {
	var list []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &list) != nil {
		return nil, false
	}
	var out []ToolLocation
	for _, it := range list {
		var l ToolLocation
		if json.Unmarshal(it, &l) == nil && l.Path != "" {
			out = append(out, l)
		}
	}
	return out, true
}

// mergeContent is rule 2.
func (r *ToolRecord) mergeContent(raw json.RawMessage, p ToolPolicy) {
	var items []map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '[' {
		return
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	for _, it := range list {
		var m map[string]json.RawMessage
		if json.Unmarshal(it, &m) == nil && m != nil {
			items = append(items, m)
		}
	}
	var text []string
	var diffs []ToolDiff
	for _, it := range items {
		typ, _ := jsonString(it["type"])
		switch typ {
		case "diff":
			path, _ := jsonString(it["path"])
			newText, _ := jsonString(it["newText"])
			var old *string
			if s, ok := jsonString(it["oldText"]); ok {
				old = &s
			}
			deleted := old != nil && r.Kind == "delete"
			if p.Diff != nil {
				diffs = append(diffs, p.Diff(path, old, newText, deleted))
			} else {
				diffs = append(diffs, textDiff(path, old, newText, deleted))
			}
		case "terminal":
			r.Terminal = true
		default:
			var inner map[string]json.RawMessage
			_ = json.Unmarshal(it["content"], &inner)
			s, ok := jsonString(inner["text"])
			if !ok && typ == "text" {
				s, ok = jsonString(it["text"])
			}
			if ok && s != "" {
				text = append(text, s)
			}
		}
	}
	if len(text) > 0 {
		r.Text = strings.Join(text, "\n\n")
		if p.TextMax > 0 {
			r.Text = clipText(r.Text, p.TextMax)
		}
	}
	if len(diffs) > 0 {
		r.Diffs = diffs
	}
}

// jsonString is raw as a JSON string (false: absent, null or not one).
func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// addOutput gathers output, keeping its newest max bytes (0: all).
func (r *ToolRecord) addOutput(s string, max int) {
	r.Output += s
	if max <= 0 || len(r.Output) <= max {
		return
	}
	kept := tailBytes(r.Output, max)
	r.OutputTruncated += int64(len(r.Output) - len(kept))
	r.Output = kept
}

// tailBytes is the last n bytes of s at most, cut between characters.
func tailBytes(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}

// textDiff is rule 4 with both texts kept.
func textDiff(path string, old *string, newText string, deleted bool) ToolDiff {
	d := ToolDiff{Path: path, Status: "modified", NewText: newText}
	var a, b []string
	switch {
	case old == nil:
		d.Status = "added"
		b = textLines(newText)
	case deleted:
		d.Status, d.OldText = "deleted", old
		a = textLines(*old)
	default:
		d.OldText = old
		a, b = textLines(*old), textLines(newText)
	}
	d.Add, d.Del = lineCounts(a, b)
	return d
}

// textLines is a text's lines: a final newline ends the last line, it
// doesn't start another.
func textLines(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.Split(s, "\n")
	if out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// diffCells: past this an edit's changed middle counts as replaced whole.
const diffCells = 4_000_000

// lineCounts is the lines of b not in a (add) and of a not in b (del), by
// the longest common subsequence between their common head and tail.
func lineCounts(a, b []string) (add, del int) {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	n, m := len(a)-p-s, len(b)-p-s
	if n == 0 || m == 0 || n*m > diffCells {
		return m, n
	}
	prev, cur := make([]int32, m+1), make([]int32, m+1)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[p+i] == b[p+j] {
				cur[j] = prev[j+1] + 1
			} else {
				cur[j] = max(prev[j], cur[j+1])
			}
		}
		prev, cur = cur, prev
		clear(cur)
	}
	return m - int(prev[0]), n - int(prev[0])
}

// ToolEntry is a record in a ToolRecords: Seq the event that began it (an
// event log's; 0 from a Client), Turn the turn it was made in (1, 2, …), N
// which call of its id it is (1; 2 for the first reuse).
type ToolEntry struct {
	Seq  uint64      `json:"seq"`
	Turn int         `json:"turn"`
	N    int         `json:"-"`
	Rec  *ToolRecord `json:"rec"`
}

// ToolRecords folds a session's events into its tool records, the id-reuse
// rule included. The zero value is ready (it keeps everything); not safe
// for concurrent use.
type ToolRecords struct {
	Policy ToolPolicy

	turn int // the running turn, from 0
	held map[string]*ToolEntry
	list []*ToolEntry
}

// NewToolRecords is a ToolRecords with a policy.
func NewToolRecords(p ToolPolicy) *ToolRecords { return &ToolRecords{Policy: p} }

// Apply folds one event: a tool.call or tool.update into its record (the
// entry, and whether the event began it), a files.changed into its call's
// Changed, a turn.end into the turn count. Any other event — and a tool
// event naming no call — is (nil, false).
func (t *ToolRecords) Apply(ev Event) (*ToolEntry, bool) {
	switch ev.Type {
	case EvToolCall, EvToolUpdate:
		d, ok := ParseToolEvent(ev.Data)
		if !ok {
			return nil, false
		}
		e, isNew := t.Observe(ev.Type, d.ID, "")
		if isNew {
			e.Seq = ev.Seq
		}
		e.Rec.Merge(d, t.Policy)
		return e, isNew
	case EvFilesChanged:
		var d struct {
			ToolCallID string       `json:"toolCallId"`
			Changes    []FileChange `json:"changes"`
			Patch      *struct {
				Text      *string `json:"text"`
				Truncated bool    `json:"truncated"`
			} `json:"patch"`
		}
		if json.Unmarshal(ev.Data, &d) != nil || d.ToolCallID == "" {
			return nil, false
		}
		e := t.held[d.ToolCallID]
		if e == nil {
			return nil, false
		}
		set := &ChangeSet{Files: d.Changes}
		if set.Files == nil {
			set.Files = []FileChange{}
		}
		if d.Patch != nil && d.Patch.Text != nil {
			set.Patch = &ChangePatch{Text: *d.Patch.Text, Truncated: d.Patch.Truncated}
		}
		e.Rec.Changed = set
		return e, false
	case EvTurnEnd:
		var d struct {
			Turn uint64 `json:"turn"`
		}
		_ = json.Unmarshal(ev.Data, &d)
		t.EndTurn(d.Turn)
	}
	return nil, false
}

// Observe is the id-reuse rule alone, for a reader that needs only which
// record an event lands on (a pager): the entry a tool event of evType for
// id belongs to, begun now when the rule says so, its status moved to
// status when the event carries one. Apply calls it; a caller of both
// mixes them freely.
func (t *ToolRecords) Observe(evType, id, status string) (*ToolEntry, bool) {
	if id == "" {
		return nil, false
	}
	e := t.held[id]
	var held *ToolRecord
	if e != nil {
		held = e.Rec
	}
	isNew := StartsToolRecord(evType, held, e != nil && e.Turn == t.turn+1)
	if isNew {
		n := 1
		if e != nil {
			n = e.N + 1
		}
		e = &ToolEntry{Turn: t.turn + 1, N: n, Rec: NewToolRecord(id)}
		if t.held == nil {
			t.held = map[string]*ToolEntry{}
		}
		t.held[id] = e
		t.list = append(t.list, e)
	}
	if status != "" {
		e.Rec.Status = status
	}
	return e, isNew
}

// EndTurn is a turn's end (turn: its number when the event says one, else
// 0): what follows is the next turn's. Apply calls it for a turn.end.
func (t *ToolRecords) EndTurn(turn uint64) {
	if turn > 0 {
		t.turn = int(turn)
		return
	}
	t.turn++
}

// Newest is the newest record of id (nil: none).
func (t *ToolRecords) Newest(id string) *ToolEntry { return t.held[id] }

// Entries are the records in the order their calls began. The slice is the
// fold's own: read it, don't keep it across an Apply.
func (t *ToolRecords) Entries() []*ToolEntry { return t.list }
