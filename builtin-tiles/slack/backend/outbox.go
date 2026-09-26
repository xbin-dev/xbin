package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// outRow is one reply to post (GET /adapter/outbox, docs/agent-inbox.md).
type outRow struct {
	ID         int64  `json:"id"`
	ChannelID  int64  `json:"channelId"`
	SessionKey string `json:"sessionKey"`
	Kind       string `json:"kind"`
	Address    struct {
		Conversation string `json:"conversation"`
		Type         string `json:"type"`
		Thread       string `json:"thread"`
		User         string `json:"user"`
	} `json:"address"`
	Body struct {
		Text string `json:"text"`
	} `json:"body"`
}

type statusEv struct {
	ChannelID int64 `json:"channelId"`
	Address   struct {
		Conversation string `json:"conversation"`
		Thread       string `json:"thread"`
	} `json:"address"`
	State string `json:"state"` // working | idle
}

var errBye = errors.New("the agent is handing over")

// outboxLoop keeps the agent's outbox stream open and posts what comes: at
// once after a handover (bye), with a backoff after a failure.
func (t *Tile) outboxLoop() {
	var since int64
	backoff := time.Second
	for {
		started := time.Now()
		err := t.readOutbox(&since)
		if errors.Is(err, errBye) {
			backoff = time.Second
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		time.Sleep(jitter(backoff))
		if backoff *= 2; backoff > time.Minute {
			backoff = time.Minute
		}
	}
}

// readOutbox reads one stream. since advances as rows arrive, so a reconnect
// doesn't bring back what this process already holds; a fresh process
// passes 0 and gets every row not yet acked (posted ones it recognises by
// its own record — posted/<id>).
func (t *Tile) readOutbox(since *int64) error {
	resp, err := t.agent.outbox(*since)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var event, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			switch event {
			case "out":
				var row outRow
				if json.Unmarshal([]byte(data), &row) == nil {
					t.deliverOut(row)
					*since = row.ID
				}
			case "status":
				var ev statusEv
				if json.Unmarshal([]byte(data), &ev) == nil {
					t.showStatus(ev)
				}
			case "bye":
				return errBye
			}
			event, data = "", ""
		case len(line) > 7 && line[:7] == "event: ":
			event = line[7:]
		case len(line) > 6 && line[:6] == "data: ":
			data += line[6:]
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the agent closed the outbox stream")
}

func postedKey(id int64) string { return "posted/" + strconv.FormatInt(id, 10) }

// deliverOut posts one row (split at Slack's length) and acks it with the
// first message's ts. The ts is recorded before the ack: a crash between the
// two replays the row, which is then only acked.
func (t *Tile) deliverOut(row outRow) {
	if ref, err := t.kv.Get(postedKey(row.ID)); err == nil {
		if t.agent.ack(ackItem{ID: row.ID, OK: true, Ref: string(ref)}) == nil {
			_ = t.kv.Delete(postedKey(row.ID))
		}
		return
	}
	bot := t.secret(secretBot)
	var first string
	for i, part := range splitMessage(toMrkdwn(row.Body.Text), maxMessage) {
		ts, err := t.postRetrying(bot, row.Address.Conversation, row.Address.Thread, part)
		if err != nil {
			t.logEvent(eventLog{Kind: "error", Where: row.Address.Conversation, Text: "couldn't post a reply: " + err.Error()})
			if i == 0 {
				_ = t.agent.ack(ackItem{ID: row.ID, OK: false, Error: err.Error()})
				return
			}
			break // the rest of a long reply is lost; what was posted stands
		}
		if i == 0 {
			first = ts
		}
	}
	_ = t.kv.Put(postedKey(row.ID), []byte(first))
	t.logEvent(eventLog{Kind: "out", Where: row.Address.Conversation, Text: row.Body.Text, Verdict: row.Kind})
	if t.agent.ack(ackItem{ID: row.ID, OK: true, Ref: first}) == nil {
		_ = t.kv.Delete(postedKey(row.ID))
	}
}

// postRetrying posts, waiting out rate limits and retrying transient
// failures (about a minute in all); a permanent error comes back at once.
func (t *Tile) postRetrying(bot, channel, thread, text string) (string, error) {
	var err error
	wait := time.Second
	for attempt := 0; attempt < 6; attempt++ {
		var ts string
		ts, err = t.api().postMessage(bot, channel, thread, text)
		if err == nil {
			return ts, nil
		}
		var ae *apiError
		var re *rateError
		switch {
		case errors.As(err, &re):
			time.Sleep(re.after)
			continue
		case errors.As(err, &ae) && ae.permanent():
			return "", err
		}
		time.Sleep(wait)
		wait *= 2
	}
	return "", err
}

// showStatus shows "is thinking…" in an assistant thread while the agent
// works (Slack clears it when the reply lands). Elsewhere there is nothing to
// show it in.
func (t *Tile) showStatus(ev statusEv) {
	a := ev.Address
	if a.Thread == "" || !t.isAssistant(a.Conversation, a.Thread) {
		return
	}
	status := ""
	if ev.State == "working" {
		status = "is thinking…"
	}
	_ = t.api().setStatus(t.secret(secretBot), a.Conversation, a.Thread, status)
}
