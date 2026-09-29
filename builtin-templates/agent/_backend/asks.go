// asks.go — the task ledger (D133): every request a run was given, verbatim,
// written in the transaction that delivers it into the transcript. The model
// reads it — pinned near the top of its context (# Your task) and recited at
// the end of every call (the reminder) — but no tool writes it, and
// compaction never folds it into a summary.
//
// Why: the request used to be only the first user message, compacted like any
// other; after a few compactions an agent optimised a reconstructed goal.
// See plans/DECISIONS.md D133 for the research this follows (a fixed slot,
// verbatim, recited at the end; never rewritten by an LLM).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const askSchemaSQL = `
-- The task ledger (D133): one row per request delivered to a run. msg_id is
-- the transcript message it arrived as (seq can move under a repair; msg_id
-- does not); seq is its position when it arrived, kept for a row whose
-- message is gone.
CREATE TABLE IF NOT EXISTS asks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  msg_id INTEGER NOT NULL DEFAULT 0,
  seq INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT '',
  who TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_asks_run ON asks(run_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_asks_msg ON asks(msg_id) WHERE msg_id<>0;
-- Every compaction summary a run had, oldest first (runs.summary is the
-- latest, for readers that know only it). recall searches them.
CREATE TABLE IF NOT EXISTS summaries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  text TEXT NOT NULL DEFAULT '',
  upto_seq INTEGER NOT NULL DEFAULT 0,
  messages INTEGER NOT NULL DEFAULT 0,
  at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_summaries_run ON summaries(run_id, id);
CREATE VIRTUAL TABLE IF NOT EXISTS summaries_fts USING fts5(
  text, run_id UNINDEXED, sum_id UNINDEXED, tokenize='porter'
);
`

// addAskSchema is the D133 migration: additive (new tables, two columns an
// older binary ignores) and idempotent.
func (d *DB) addAskSchema() error {
	if _, err := d.q.Exec(askSchemaSQL); err != nil {
		return err
	}
	for _, q := range []string{
		// a tool result shown to the model as a restorable stub (stage 1 of
		// compaction); its content stays, in the row and in the search index
		`ALTER TABLE messages ADD COLUMN masked INTEGER NOT NULL DEFAULT 0`,
		// the next model call gets the one-time "context was compacted" note
		`ALTER TABLE runs ADD COLUMN compact_note INTEGER NOT NULL DEFAULT 0`,
	} {
		_, _ = d.q.Exec(q)
	}
	if d.getSetting("asks_backfill") != "1" {
		if n := d.backfillAsks(0); n > 0 {
			logf("task ledger: recorded the first request of %d existing run(s)", n)
		}
		if err := d.putSetting("asks_backfill", "1"); err != nil {
			return err
		}
	}
	return nil
}

// metaStr reads a string field of messages.meta (empty when meta is not JSON:
// json_extract on an empty string is an error, not NULL).
func metaStr(col, field string) string {
	return `COALESCE(CASE WHEN json_valid(` + col + `) THEN json_extract(` + col + `, '$.` + field + `') END, '')`
}

// backfillAsks records a run's first user message as its first ask, for runs
// that have none (runID 0: every run). Runs made before the ledger existed,
// and runs an older binary made during a blue/green overlap, get their
// original request pinned this way. A watcher's "check now" is not a request
// (its job is in its system prompt).
func (d *DB) backfillAsks(runID int64) int {
	filter, args := "", []any{}
	if runID != 0 {
		filter, args = ` AND m.run_id=?`, append(args, runID)
	}
	res, err := d.q.Exec(`INSERT OR IGNORE INTO asks (run_id, msg_id, seq, source, who, text, at)
		SELECT m.run_id, m.id, m.seq,
		  CASE WHEN r.parent_id<>0 THEN 'parent' ELSE COALESCE(NULLIF(`+metaStr("m.meta", "origin")+`, ''), 'human') END,
		  CASE WHEN r.parent_id<>0 THEN '#' || r.parent_id
		       ELSE COALESCE(NULLIF(`+metaStr("m.meta", "sender")+`, ''), `+metaStr("m.meta", "label")+`) END,
		  m.content, m.created
		  FROM messages m JOIN runs r ON r.id=m.run_id
		 WHERE m.role='user'`+filter+`
		   AND m.id = (SELECT m2.id FROM messages m2 WHERE m2.run_id=m.run_id AND m2.role='user' ORDER BY m2.seq, m2.id LIMIT 1)
		   AND `+metaStr("m.meta", "origin")+` <> 'watch'
		   AND NOT EXISTS (SELECT 1 FROM asks a WHERE a.run_id=m.run_id)`, args...)
	if err != nil {
		logf("task ledger backfill: %v", err)
		return 0
	}
	return int(rowsAffected(res))
}

// Ask is one request in the ledger.
type Ask struct {
	ID     int64  `json:"id"`
	RunID  int64  `json:"runId"`
	MsgID  int64  `json:"msgId"`
	Seq    int    `json:"seq"`    // the message's position now (message_get #seq)
	Source string `json:"source"` // human | parent | schedule | trigger | channel | learn
	Who    string `json:"who,omitempty"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
	// Live: its message is still in the model's window (not compacted).
	Live bool `json:"live"`
}

// recordAsk adds a delivered message to the ledger. Call it in the
// transaction that wrote m (it needs m.ID and m.Seq).
func (d *DB) recordAsk(m *Message, source, who string) error {
	if source == "" {
		source = "human"
	}
	_, err := d.q.Exec(`INSERT OR IGNORE INTO asks (run_id, msg_id, seq, source, who, text, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.RunID, m.ID, m.Seq, source, who, m.Content, now())
	return err
}

// asks is a run's ledger, oldest first, with each message's current seq and
// whether it is still live. A run with none gets its first user message
// recorded now (a run an older binary made while this one was deployed).
func (d *DB) asks(runID int64) ([]*Ask, error) {
	out, err := d.readAsks(runID)
	if err == nil && len(out) == 0 && d.backfillAsks(runID) > 0 {
		out, err = d.readAsks(runID)
	}
	return out, err
}

func (d *DB) readAsks(runID int64) ([]*Ask, error) {
	rows, err := d.q.Query(`SELECT a.id, a.run_id, a.msg_id, COALESCE(m.seq, a.seq), a.source, a.who, a.text, a.at,
		  COALESCE(m.compacted, 1)
		  FROM asks a LEFT JOIN messages m ON m.id=a.msg_id
		 WHERE a.run_id=? ORDER BY a.id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Ask
	for rows.Next() {
		a := &Ask{}
		var compacted int
		if err := rows.Scan(&a.ID, &a.RunID, &a.MsgID, &a.Seq, &a.Source, &a.Who, &a.Text, &a.At, &compacted); err != nil {
			return nil, err
		}
		a.Live = compacted == 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// askSource is the ledger source and who for an inbox row's message.
func askSource(b inboxBody) (source, who string) {
	source = orStr(b.Source, "human")
	switch source {
	case "human":
		who = b.Sender
	case "parent":
		if b.From != 0 {
			who = fmt.Sprintf("#%d", b.From)
		}
	default:
		who = orStr(b.Sender, b.Label)
	}
	return source, who
}

// --- the prompt --------------------------------------------------------------

// Caps on the # Your task block, in characters. A capped item names the
// message_get call that has the rest.
const (
	taskFirstMax = 6000
	taskItemMax  = 1500
	taskBlockMax = 12000
	anchorAskMax = 400
)

const taskHeading = "# Your task (verbatim — it outranks your notes and the summary)"

// taskBlock is the pinned task: the first request, then later requests whose
// turns were compacted, newest last. Requests still in the conversation are
// not repeated here — so the block, and the whole prefix after it, changes
// only when a compaction runs (which breaks the provider's prompt cache
// anyway), never when a message arrives.
func taskBlock(asks []*Ask) string {
	if len(asks) == 0 {
		return ""
	}
	first := asks[0]
	var later []*Ask
	for _, a := range asks[1:] {
		if !a.Live {
			later = append(later, a)
		}
	}
	var b strings.Builder
	b.WriteString("\n\n" + taskHeading + "\n")
	if len(later) > 0 {
		b.WriteString("The request that started this conversation, then later requests whose turns were compacted (newest last). Requests since then are in the conversation below.\n")
	}
	b.WriteString(askItem(first, taskFirstMax))
	left := taskBlockMax - min(len(first.Text), taskFirstMax)
	// newest first into the budget, printed newest last
	items := make([]string, len(later))
	for i := len(later) - 1; i >= 0; i-- {
		a := later[i]
		if left > 0 {
			items[i] = askItem(a, min(taskItemMax, max(left, 200)))
			left -= min(len(a.Text), taskItemMax)
			continue
		}
		items[i] = fmt.Sprintf("\n[#%d · %s — %d characters, not shown: message_get {\"seq\": %d}] %s\n",
			a.Seq, askWho(a), len(a.Text), a.Seq, flatClip(a.Text, 100))
	}
	for _, s := range items {
		b.WriteString(s)
	}
	return b.String()
}

func askItem(a *Ask, maxChars int) string {
	text := a.Text
	if len(text) > maxChars {
		text = fmt.Sprintf("%s\n… [cut — %d more characters: message_get {\"seq\": %d}]",
			strings.ToValidUTF8(text[:maxChars], ""), len(text)-maxChars, a.Seq)
	}
	return fmt.Sprintf("\n[#%d · %s · %s]\n%s\n", a.Seq, askWho(a), time.Unix(a.At, 0).UTC().Format("2006-01-02 15:04Z"), text)
}

func askWho(a *Ask) string {
	if a.Who == "" {
		return a.Source
	}
	return a.Source + " " + a.Who
}

// flatClip is s on one line, clipped.
func flatClip(s string, n int) string {
	return clip(strings.Join(strings.Fields(s), " "), n)
}

// The reminder is appended to the LAST message of every request and never
// stored: recency for the task without touching the cached prefix (only the
// last message differs from what the previous call sent). The tags make it
// unmistakable in a tool result, and let tools that read transcripts (the
// harness's fake model, tests) strip it.
const (
	reminderOpen  = "<task-reminder>"
	reminderClose = "</task-reminder>"
	compactedNote = "Earlier turns were compacted: old tool outputs are now short stubs (message_get restores one) and older turns are summarized. Re-read # Your task before you continue."
)

// reminderText is the reminder for a call, "" when there is nothing to say:
// the task line is left out when the last message IS the latest request.
func reminderText(title string, latest *Ask, showTask, note bool) string {
	var lines []string
	if note {
		lines = append(lines, compactedNote)
	}
	if showTask && latest != nil {
		line := "Current task"
		if t := strings.TrimSpace(title); t != "" {
			line += fmt.Sprintf(" — %q", flatClip(t, 80))
		}
		line += fmt.Sprintf(". Latest request (#%d, %s): %q. Its full text: # Your task, the conversation, or message_get {\"seq\": %d}.",
			latest.Seq, askWho(latest), flatClip(latest.Text, anchorAskMax), latest.Seq)
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + reminderOpen + "\n" + strings.Join(lines, "\n") + "\n" + reminderClose
}

// withReminder appends the reminder to the request's last message: to a
// string content, to a parts array as one more text part, or — after an
// assistant message, which no call should end on — as a user message.
func withReminder(msgs []wireMsg, text string) []wireMsg {
	if text == "" || len(msgs) == 0 {
		return msgs
	}
	i := len(msgs) - 1
	last := msgs[i]
	switch last.Role {
	case "user", "tool":
		switch c := last.Content.(type) {
		case string:
			last.Content = c + text
		case nil:
			last.Content = strings.TrimLeft(text, "\n")
		case json.RawMessage:
			var parts []json.RawMessage
			if last.Role == "user" && json.Unmarshal(c, &parts) == nil {
				last.Content = partsOf(parts, strings.TrimLeft(text, "\n"), nil)
			} else {
				last.Content = asText(c) + text
			}
		default:
			last.Content = asText(c) + text
		}
		out := make([]wireMsg, len(msgs))
		copy(out, msgs)
		out[i] = last
		return out
	}
	return append(msgs, wireMsg{Role: "user", Content: strings.TrimLeft(text, "\n")})
}

// stripReminder removes a reminder from a text (tests and the fake model).
func stripReminder(s string) string {
	if i := strings.Index(s, "\n\n"+reminderOpen); i >= 0 {
		return s[:i]
	}
	return s
}

// --- the view ------------------------------------------------------------------

// taskView is the pinned task as the run header shows it: the first request,
// the latest when there are more, and how many there are.
func (d *DB) taskView(runID int64) map[string]any {
	asks, err := d.asks(runID)
	if err != nil || len(asks) == 0 {
		return nil
	}
	v := map[string]any{"count": len(asks), "first": askView(asks[0], 4000)}
	if len(asks) > 1 {
		v["latest"] = askView(asks[len(asks)-1], 1000)
	}
	return v
}

func askView(a *Ask, n int) map[string]any {
	return map[string]any{"id": a.ID, "seq": a.Seq, "source": a.Source, "who": a.Who,
		"text": clip(a.Text, n), "cut": len(a.Text) > n, "at": a.At, "live": a.Live}
}

// handleAsks is GET /runs/{id}/asks: the whole ledger, read-only.
func handleAsks(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := agent.db.getRun(id); err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	asks, err := agent.db.asks(id)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if asks == nil {
		asks = []*Ask{}
	}
	xbin.WriteJSON(w, 200, map[string]any{"asks": asks})
}
