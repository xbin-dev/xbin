package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// outRow is one reply to post (GET /adapter/outbox, docs/agent-inbox.md).
type outRow struct {
	ID         int64   `json:"id"`
	ChannelID  int64   `json:"channelId"`
	SessionKey string  `json:"sessionKey"`
	Kind       string  `json:"kind"`
	Address    Address `json:"address"`
	Body       struct {
		Text  string `json:"text"`
		Files []struct {
			Name  string `json:"name"`
			Mime  string `json:"mime"`
			Bytes int    `json:"bytes"`
		} `json:"files"`
	} `json:"body"`
}

type statusEv struct {
	ChannelID int64   `json:"channelId"`
	Address   Address `json:"address"`
	State     string  `json:"state"` // working | idle
}

// channelEv: the owner claimed, switched off or on, or removed a channel on
// the agent.
type channelEv struct {
	ChannelID int64  `json:"channelId"`
	State     string `json:"state"` // unclaimed | active | disabled | removed
}

var errBye = errors.New("the agent is handing over")

// outboxLoop keeps the agent's outbox stream open: at once after a handover
// (bye), with a backoff after a failure.
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
		t.sleep(&backoff)
	}
}

// readOutbox reads one stream. since advances as rows arrive, so a reconnect
// doesn't bring back what this process holds; a fresh process passes 0 and
// gets every row not yet acked (the ones it posted it recognises by its own
// record, posted/<id>).
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
					t.typing(ev)
				}
			case "channel":
				var ev channelEv
				if json.Unmarshal([]byte(data), &ev) == nil {
					t.channelState(ev.ChannelID, ev.State)
				}
			case "bye":
				return errBye
			}
			event, data = "", ""
		case strings.HasPrefix(line, "event: "):
			event = line[7:]
		case strings.HasPrefix(line, "data: "):
			data += line[6:]
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the agent closed the outbox stream")
}

func postedKey(id int64) string { return "posted/" + strconv.FormatInt(id, 10) }

// deliverOut posts one row — its text formatted and split for the platform,
// its files on the last piece — and acks it with the first message's ref. The
// ref is recorded before the ack: a crash between the two replays the row,
// which is then only acked.
func (t *Tile) deliverOut(row outRow) {
	if ref, err := t.kv.Get(postedKey(row.ID)); err == nil {
		if t.agent.ack(ackItem{ID: row.ID, OK: true, Ref: string(ref)}) == nil {
			_ = t.kv.Delete(postedKey(row.ID))
		}
		return
	}
	fail := func(err error) {
		t.logEvent(eventLog{Kind: "error", Where: row.Address.Conversation, Text: "couldn't post a reply: " + err.Error()})
		_ = t.agent.ack(ackItem{ID: row.ID, OK: false, Error: clip(err.Error(), 300)})
	}
	p := t.platform()
	account, ok := t.accountOf(row.ChannelID)
	if p == nil || !ok {
		fail(errors.New("no platform account for channel " + strconv.FormatInt(row.ChannelID, 10)))
		return
	}
	var files []OutFile
	for i, f := range row.Body.Files {
		data, mime, err := t.agent.download(row.ID, i)
		if err != nil {
			fail(err)
			return
		}
		files = append(files, OutFile{Name: f.Name, Mime: orStr(f.Mime, mime), Data: data})
	}
	pieces := splitText(p.Format(row.Body.Text), p.Limit())
	if len(pieces) == 0 {
		pieces = []string{""} // files only
	}
	var first string
	for i, piece := range pieces {
		msg := Outgoing{Kind: row.Kind, Text: piece}
		if i == len(pieces)-1 {
			msg.Files = files
		}
		ref, err := t.sendRetrying(p, account, row.Address, msg)
		if err != nil {
			if i == 0 {
				fail(err)
				return
			}
			t.logEvent(eventLog{Kind: "error", Where: row.Address.Conversation, Text: "the rest of a long reply was lost: " + err.Error()})
			break // what was posted stands
		}
		if i == 0 {
			first = ref
		}
	}
	_ = t.kv.Put(postedKey(row.ID), []byte(first))
	text := row.Body.Text
	if len(files) > 0 {
		text += " [+" + strconv.Itoa(len(files)) + " file(s)]"
	}
	t.logEvent(eventLog{Kind: "out", Where: row.Address.Conversation, Text: text, Verdict: row.Kind})
	if t.agent.ack(ackItem{ID: row.ID, OK: true, Ref: first}) == nil {
		_ = t.kv.Delete(postedKey(row.ID))
	}
}

// sendRetrying sends, waiting out rate limits and retrying transient failures
// (about a minute in all); a permanent error comes back at once.
func (t *Tile) sendRetrying(p Platform, account string, to Address, msg Outgoing) (string, error) {
	var err error
	wait := time.Second
	for attempt := 0; attempt < 6; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		var ref string
		ref, err = p.Send(ctx, account, to, msg)
		cancel()
		if err == nil {
			return ref, nil
		}
		if isPermanent(err) {
			return "", err
		}
		if d, ok := retryAfter(err); ok {
			time.Sleep(d)
			continue
		}
		time.Sleep(wait)
		wait *= 2
	}
	return "", err
}

// typing passes the agent's working/idle hint on.
func (t *Tile) typing(ev statusEv) {
	p := t.platform()
	account, ok := t.accountOf(ev.ChannelID)
	if p == nil || !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p.Typing(ctx, account, ev.Address, ev.State == "working")
}

// splitText cuts text into pieces of at most max bytes at line breaks (mid-line
// only for a line longer than max); a piece that ends inside a ``` block
// closes it and the next reopens it. max <= 0 means no limit.
func splitText(text string, max int) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if max <= 0 || len(text) <= max {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	inCode := false
	flush := func() {
		s := cur.String()
		if inCode {
			s += "\n```"
		}
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimRight(s, "\n"))
		}
		cur.Reset()
		if inCode {
			cur.WriteString("```\n")
		}
	}
	for _, line := range strings.Split(text, "\n") {
		for len(line) > max-8 {
			cut := max - 8
			for cut > 0 && line[cut]&0xC0 == 0x80 { // not inside a UTF-8 rune
				cut--
			}
			if cur.Len() > 0 {
				flush()
			}
			cur.WriteString(line[:cut])
			flush()
			line = line[cut:]
		}
		if cur.Len()+len(line)+1 > max-4 {
			flush()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
		}
	}
	inCode = false
	flush()
	return out
}
