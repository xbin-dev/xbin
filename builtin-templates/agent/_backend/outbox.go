// outbox.go — replies to chat channels (D86). What a channel session says —
// an answer, a question, "waiting for approval", a pairing code — is a row
// written in the SAME transaction that ended the turn or parked it, so a
// reply is never lost to a crash and never sent twice by the agent. The
// adapter pulls rows over GET /adapter/outbox (an SSE stream, only its own
// channels), posts them, and acks each with the platform's message id:
// delivery is at-least-once, and the adapter makes it effectively once by
// remembering what it posted. No timers: rows are pruned when new ones are
// written, and streams are woken by the writes themselves.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// OutRow is one outbound message, as the adapter receives it.
type OutRow struct {
	ID         int64           `json:"id"`
	ChannelID  int64           `json:"channelId"`
	SessionKey string          `json:"sessionKey,omitempty"`
	RunID      int64           `json:"runId,omitempty"`
	Kind       string          `json:"kind"` // answer | question | approval | error | notice
	Address    json.RawMessage `json:"address"`
	Body       outBody         `json:"body"`
	Created    int64           `json:"created"`
	// the owner's view (failed deliveries)
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
	Ref   string `json:"ref,omitempty"`
}

type outBody struct {
	Text   string `json:"text"`
	Format string `json:"format"` // markdown
}

const outCols = `id, channel_id, session_key, run_id, kind, address, body, created, state, error, ref`

func scanOut(scan func(dest ...any) error) (*OutRow, error) {
	o := &OutRow{}
	var addr, body string
	if err := scan(&o.ID, &o.ChannelID, &o.SessionKey, &o.RunID, &o.Kind, &addr, &body, &o.Created, &o.State, &o.Error, &o.Ref); err != nil {
		return nil, err
	}
	o.Address = json.RawMessage(orStr(addr, "{}"))
	_ = json.Unmarshal([]byte(body), &o.Body)
	return o, nil
}

func (d *DB) outRows(where string, args ...any) []*OutRow {
	rows, err := d.q.Query(`SELECT `+outCols+` FROM outbox `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []*OutRow{}
	for rows.Next() {
		if o, err := scanOut(rows.Scan); err == nil {
			out = append(out, o)
		}
	}
	return out
}

// outboxAdd writes a row (in the caller's transaction) and wakes the
// streams once it commits. Old delivered and failed rows are pruned here.
func (d *DB) outboxAdd(chID int64, key string, runID int64, kind, addr, text string) {
	body, _ := json.Marshal(outBody{Text: text, Format: "markdown"})
	if _, err := d.q.Exec(`INSERT INTO outbox (channel_id, session_key, run_id, kind, address, body, created) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		chID, key, runID, kind, addr, string(body), now()); err != nil {
		logf("outbox: %v", err)
		return
	}
	_, _ = d.q.Exec(`DELETE FROM outbox WHERE state<>'pending' AND created<?`, now()-7*86400)
	d.AfterCommit(outboxKick)
}

// noReply: the model chose silence (a group message not meant for it).
func noReply(text string) bool {
	t := strings.TrimSpace(text)
	return t == "" || strings.TrimRight(t, ".") == "NO_REPLY"
}

// replyTarget is where a channel run's reply goes: the address of the last
// message it took in, when that came from a channel. A message typed into
// the run from the web UI is answered there, not posted.
func (d *DB) replyTarget(run *Run) (chID int64, addr string, ok bool) {
	if run.ParentID != 0 || run.Origin != "channel" {
		return 0, "", false
	}
	rows := d.inboxRows(`WHERE run_id=? AND kind='user' AND delivered_at<>0 ORDER BY id DESC LIMIT 1`, run.ID)
	if len(rows) == 0 || rows[0].Body.Source != "channel" || rows[0].Body.Addr == "" {
		return 0, "", false
	}
	return rows[0].Body.OriginID, rows[0].Body.Addr, true
}

// channelTurnEnd posts what a channel conversation's turn produced
// (endTurnTx). A discarded watcher round and NO_REPLY post nothing; an error
// is posted generically — its text stays in the run.
func (e *Engine) channelTurnEnd(t *DB, run *Run, why, result string) {
	chID, addr, ok := t.replyTarget(run)
	if !ok {
		return
	}
	kind, text := "answer", result
	switch why {
	case endAnswered, endFinished:
		if strings.TrimSpace(text) == "" {
			text = t.lastAssistant(run.ID)
		}
	case endError:
		kind, text = "error", "Sorry — something went wrong on my side. Try again, or send /new to start over."
	case endCap:
		kind, text = "notice", "I stopped after the step limit for one turn. Send a message to let me continue."
	}
	if !noReply(text) {
		t.outboxAdd(chID, run.SessionKey, run.ID, kind, addr, text)
	}
	if ch, err := t.getChannel(chID); err == nil {
		t.AfterCommit(func() {
			outStatus(ch.Adapter, outStatusEv{ChannelID: chID, SessionKey: run.SessionKey, Address: json.RawMessage(addr), State: "idle"})
		})
	}
}

// channelAsk posts a question (ask_user: the peer's next message answers it)
// or says an approval is pending (the operator decides in the run's page, or
// a trusted peer with /approve).
func (e *Engine) channelAsk(t *DB, run *Run, kind, text string) {
	if chID, addr, ok := t.replyTarget(run); ok {
		t.outboxAdd(chID, run.SessionKey, run.ID, kind, addr, text)
	}
}

// --- streams ------------------------------------------------------------------

// outStatusEv is a transient signal (not stored): the agent is working on a
// session, or went quiet — for a typing indicator.
type outStatusEv struct {
	ChannelID  int64           `json:"channelId"`
	SessionKey string          `json:"sessionKey"`
	Address    json.RawMessage `json:"address"`
	State      string          `json:"state"` // working | idle
}

type outSub struct {
	adapter string
	wake    chan struct{}
	status  chan outStatusEv
}

var outHub = struct {
	mu   sync.Mutex
	subs map[*outSub]bool
}{subs: map[*outSub]bool{}}

// outboxKick wakes every stream to look for new rows (after a write, and when
// this process takes the engine over — rows its predecessor wrote).
func outboxKick() {
	outHub.mu.Lock()
	defer outHub.mu.Unlock()
	for s := range outHub.subs {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// outStatus sends a status signal to the adapter's streams; dropped when a
// stream is behind (it is only a hint).
func outStatus(adapter string, ev outStatusEv) {
	outHub.mu.Lock()
	defer outHub.mu.Unlock()
	for s := range outHub.subs {
		if s.adapter == adapter {
			select {
			case s.status <- ev:
			default:
			}
		}
	}
}

// handleAdapterOutbox streams the adapter's pending rows, oldest first:
//
//	event: hello   {cursor, channels}
//	event: out     OutRow      (pending rows with id > since, then new ones)
//	event: status  {channelId, sessionKey, address, state}
//	event: bye     {}          (this process is handing over: reconnect)
//
// since is the last id this connection's predecessor received; a fresh
// adapter passes 0 and gets every row it has not acked.
func handleAdapterOutbox(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		xbin.WriteError(w, 500, "streaming unsupported")
		return
	}
	adapter := adapterOf(r)
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	sub := &outSub{adapter: adapter, wake: make(chan struct{}, 1), status: make(chan outStatusEv, 32)}
	outHub.mu.Lock()
	outHub.subs[sub] = true
	outHub.mu.Unlock()
	defer func() {
		outHub.mu.Lock()
		delete(outHub.subs, sub)
		outHub.mu.Unlock()
	}()
	var closing <-chan struct{}
	if agent.eng != nil {
		closing = agent.eng.closingCh
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	}
	var ids []int64
	for _, c := range agent.db.listChannels() {
		if c.Adapter == adapter {
			ids = append(ids, c.ID)
		}
	}
	send("hello", map[string]any{"cursor": since, "channels": orInt64s(ids)})
	fl.Flush()
	for {
		rows := agent.db.outRows(`WHERE state='pending' AND id>? AND channel_id IN (SELECT id FROM channels WHERE adapter=?) ORDER BY id LIMIT 100`,
			since, adapter)
		for _, o := range rows {
			o.State = ""
			send("out", o)
			since = o.ID
		}
		fl.Flush()
		if len(rows) == 100 {
			continue
		}
		select {
		case <-sub.wake:
		case ev := <-sub.status:
			send("status", ev)
		case <-closing:
			send("bye", map[string]any{})
			fl.Flush()
			return
		case <-r.Context().Done():
			return
		}
	}
}

func orInt64s(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}

// handleAdapterAck settles rows the adapter posted (ok, with the platform's
// message id as ref) or gave up on (ok false: retrying is the adapter's job;
// an ack false is final). Only its own channels' rows.
//
//	POST /adapter/ack {acks:[{id, ok, ref?, error?}]}
func handleAdapterAck(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Acks []struct {
			ID    int64  `json:"id"`
			OK    bool   `json:"ok"`
			Ref   string `json:"ref"`
			Error string `json:"error"`
		} `json:"acks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	adapter, settled, failed := adapterOf(r), 0, map[int64]bool{}
	err := agent.db.Tx(func(t *DB) error {
		for _, a := range b.Acks {
			state := "delivered"
			if !a.OK {
				state = "failed"
			}
			res, err := t.q.Exec(`UPDATE outbox SET state=?, ref=?, error=?, acked_at=? WHERE id=? AND state='pending'
				AND channel_id IN (SELECT id FROM channels WHERE adapter=?)`, state, clip(a.Ref, 200), clip(a.Error, 500), now(), a.ID, adapter)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				settled++
				if !a.OK {
					var ch int64
					_ = t.q.QueryRow(`SELECT channel_id FROM outbox WHERE id=?`, a.ID).Scan(&ch)
					failed[ch] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	for ch := range failed {
		emitAutomation("channel", ch)
	}
	xbin.WriteJSON(w, 200, map[string]int{"settled": settled})
}
