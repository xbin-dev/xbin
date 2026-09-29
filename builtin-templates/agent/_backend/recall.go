// recall.go — getting back what left the model's window (D133): recall
// (full-text search over the run's whole transcript and its summary history,
// ranked by bm25) and message_get (one message's full text, compacted or
// masked ones included). The notes (memory_*) are here too: they are the
// model's own, and never the task.
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type recallOpts struct {
	Query string
	Any   bool   // any word matches (default: every word)
	Order string // relevant (bm25, the default) | oldest | newest
	Limit int
}

type recallHit struct {
	Summary bool // a compaction summary, not a message
	SumID   int64
	Seq     int // the message's seq; a summary's last folded seq
	Role    string
	Name    string
	Content string
	Snippet string
	Score   float64 // bm25: lower is better
}

// recallQuery is the FTS5 expression: each word a quoted literal, all of them
// required, or any one with Any.
func recallQuery(q string, anyWord bool) string {
	s := ftsQuery(q)
	if anyWord {
		s = strings.Join(strings.Fields(s), " OR ")
	}
	return s
}

// recallSearch searches a run's whole transcript — compacted and masked
// messages included — and its summary history. Earlier recall and
// message_get results are skipped (they only repeat what they found) and so
// is the system prompt.
func (d *DB) recallSearch(runID int64, o recallOpts) ([]recallHit, error) {
	if o.Limit <= 0 || o.Limit > 20 {
		o.Limit = 8
	}
	q := recallQuery(o.Query, o.Any)
	if q == "" {
		return nil, fmt.Errorf("recall needs a query")
	}
	order := `bm25(messages_fts)`
	sumOrder := `bm25(summaries_fts)`
	switch o.Order {
	case "oldest":
		order, sumOrder = `m.seq ASC`, `s.id ASC`
	case "newest":
		order, sumOrder = `m.seq DESC`, `s.id DESC`
	}
	rows, err := d.q.Query(`SELECT m.seq, m.role, m.name, m.content, bm25(messages_fts),
		  snippet(messages_fts, 0, '«', '»', '…', 40)
		  FROM messages_fts JOIN messages m ON m.id = messages_fts.msg_id
		 WHERE messages_fts MATCH ? AND messages_fts.run_id = ?
		   AND m.role <> 'system' AND NOT (m.role = 'tool' AND m.name IN ('recall', 'message_get'))
		 ORDER BY `+order+` LIMIT ?`, q, runID, o.Limit)
	if err != nil {
		return nil, err
	}
	var hits []recallHit
	for rows.Next() {
		var h recallHit
		if err := rows.Scan(&h.Seq, &h.Role, &h.Name, &h.Content, &h.Score, &h.Snippet); err != nil {
			rows.Close()
			return nil, err
		}
		hits = append(hits, h)
	}
	rows.Close()
	srows, err := d.q.Query(`SELECT s.id, s.upto_seq, s.text, bm25(summaries_fts),
		  snippet(summaries_fts, 0, '«', '»', '…', 40)
		  FROM summaries_fts JOIN summaries s ON s.id = summaries_fts.sum_id
		 WHERE summaries_fts MATCH ? AND summaries_fts.run_id = ?
		 ORDER BY `+sumOrder+` LIMIT 3`, q, runID)
	if err == nil {
		for srows.Next() {
			h := recallHit{Summary: true}
			if srows.Scan(&h.SumID, &h.Seq, &h.Content, &h.Score, &h.Snippet) == nil {
				hits = append(hits, h)
			}
		}
		srows.Close()
	}
	switch o.Order {
	case "oldest":
		sort.SliceStable(hits, func(i, j int) bool { return hitPos(hits[i]) < hitPos(hits[j]) })
	case "newest":
		sort.SliceStable(hits, func(i, j int) bool { return hitPos(hits[i]) > hitPos(hits[j]) })
	default:
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score < hits[j].Score })
	}
	if len(hits) > o.Limit {
		hits = hits[:o.Limit]
	}
	return hits, nil
}

// hitPos orders a summary just after the last message it folded.
func hitPos(h recallHit) float64 {
	if h.Summary {
		return float64(h.Seq) + 0.5
	}
	return float64(h.Seq)
}

const recallShowWhole = 700 // a hit this short is shown whole, a longer one as a snippet

func (ag *Agent) toolRecall(run *Run, args map[string]any) (string, error) {
	o := recallOpts{Query: strings.TrimSpace(str(args["query"])), Any: str(args["match"]) == "any",
		Order: str(args["order"]), Limit: toInt(args["limit"])}
	if o.Query == "" {
		return "", fmt.Errorf("recall needs a query")
	}
	switch o.Order {
	case "", "relevant", "oldest", "newest":
	default:
		return "", fmt.Errorf("order is relevant, oldest or newest")
	}
	hits, err := ag.db.recallSearch(run.ID, o)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		if !o.Any && len(strings.Fields(o.Query)) > 1 {
			return `(no matches with every word — try match:"any")`, nil
		}
		return "(no matches)", nil
	}
	var b strings.Builder
	switch o.Order {
	case "oldest":
		fmt.Fprintf(&b, "%d match(es), oldest first:\n", len(hits))
	case "newest":
		fmt.Fprintf(&b, "%d match(es), newest first:\n", len(hits))
	default:
		fmt.Fprintf(&b, "%d match(es), most relevant first:\n", len(hits))
	}
	for _, h := range hits {
		if h.Summary {
			fmt.Fprintf(&b, "[summary %d · turns up to #%d] %s\n", h.SumID, h.Seq, flatClip(h.Snippet, 600))
			continue
		}
		who := h.Role
		if h.Name != "" {
			who += " " + h.Name
		}
		if len(h.Content) <= recallShowWhole {
			fmt.Fprintf(&b, "[#%d %s] %s\n", h.Seq, who, h.Content)
			continue
		}
		fmt.Fprintf(&b, "[#%d %s · %s] %s (message_get {\"seq\": %d} has all of it)\n",
			h.Seq, who, humanBytes(len(h.Content)), flatClip(h.Snippet, 600), h.Seq)
	}
	return strings.TrimSpace(b.String()), nil
}

// --- message_get -------------------------------------------------------------------

const messageGetMax = 12000 // characters per call; offset pages through the rest

func (d *DB) messageBySeq(runID int64, seq int) (*Message, error) {
	return scanMessage(d.q.QueryRow(`SELECT `+msgCols+` FROM messages WHERE run_id=? AND seq=? ORDER BY id LIMIT 1`, runID, seq).Scan)
}

func (ag *Agent) toolMessageGet(run *Run, args map[string]any) (string, error) {
	raw := args["seq"]
	if s, ok := raw.(string); ok {
		raw = strings.TrimPrefix(strings.TrimSpace(s), "#")
	}
	seq := toInt(raw)
	m, err := ag.db.messageBySeq(run.ID, seq)
	if err != nil {
		return "", fmt.Errorf("this conversation has no message #%d", seq)
	}
	text := m.Content
	if m.Role == "assistant" && m.ToolCalls != "" {
		var calls []toolCall
		_ = json.Unmarshal([]byte(m.ToolCalls), &calls)
		for _, c := range calls {
			text += fmt.Sprintf("\n→ %s %s", c.Function.Name, c.Function.Arguments)
		}
	}
	var state []string
	if m.Compacted {
		state = append(state, "compacted")
	}
	if m.Masked {
		state = append(state, "hidden as a stub")
	}
	who := m.Role
	if m.Name != "" {
		who += " " + m.Name
	}
	head := fmt.Sprintf("[#%d %s · %d characters", m.Seq, who, len(text))
	if len(state) > 0 {
		head += " · " + strings.Join(state, ", ")
	}
	off := toInt(args["offset"])
	if off < 0 || (off > 0 && off >= len(text)) {
		return "", fmt.Errorf("offset %d is past the end (%d characters)", off, len(text))
	}
	end := min(off+messageGetMax, len(text))
	if off > 0 || end < len(text) {
		head += fmt.Sprintf(" · characters %d-%d", off, end)
	}
	out := head + "]\n" + strings.ToValidUTF8(text[off:end], "")
	if end < len(text) {
		out += fmt.Sprintf("\n…[%d more — message_get {\"seq\": %d, \"offset\": %d}]", len(text)-end, m.Seq, end)
	}
	return out, nil
}

// --- notes (memory) -----------------------------------------------------------------

const noteMax = 8000 // characters in one note; more belongs in a file

// sortedKeys is a map's keys in order: the notes render the same way every
// call, so the prompt prefix — and the provider's cache — holds.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// notesBlock is the # Your notes section of the system prompt.
func notesBlock(mem map[string]string) string {
	if len(mem) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n# Your notes (you wrote these; the task above outranks them)\n")
	for _, k := range sortedKeys(mem) {
		fmt.Fprintf(&b, "- %s: %s\n", k, mem[k])
	}
	return b.String()
}
