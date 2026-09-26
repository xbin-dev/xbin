package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// maxFile is the largest attachment the agent takes.
const maxFile = 16 << 20

// spoolItem is one inbound event, stored before the platform is acknowledged.
type spoolItem struct {
	Key   string `json:"-"`
	Event Event  `json:"event"`
}

// spool stores an event (the platform acknowledges it only after this) and
// queues it. Inline file bytes over the agent's limit are dropped here, with a
// note, rather than stored.
func (t *Tile) spool(e Event) error {
	if e.Sender.Bot {
		return nil
	}
	for i := range e.Files {
		if len(e.Files[i].Data) > maxFile {
			e.Files[i].Data = nil
			e.Files[i].Size = -1
		}
	}
	it := spoolItem{Key: fmt.Sprintf("spool/%020d-%s", time.Now().UnixNano(), keySafe(e.ID)), Event: e}
	b, err := json.Marshal(it)
	if err != nil {
		return err
	}
	if err := t.kv.Put(it.Key, b); err != nil {
		return err
	}
	t.work <- it
	return nil
}

func keySafe(s string) string {
	out := []byte(s)
	for i, c := range out {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			out[i] = '_'
		}
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return string(out)
}

// replaySpool queues what a previous process stored but never delivered, in
// arrival order.
func (t *Tile) replaySpool() {
	keys, err := t.kv.List("spool/")
	if err != nil || len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	go func() {
		for _, k := range keys {
			b, err := t.kv.Get(k)
			var it spoolItem
			if err != nil || json.Unmarshal(b, &it) != nil {
				_ = t.kv.Delete(k)
				continue
			}
			it.Key = k
			t.work <- it
		}
	}()
}

// ensureWorker starts the one worker (once an account is known): events go to
// the agent in order, each until it is answered.
func (t *Tile) ensureWorker() {
	t.wo.Do(func() {
		go func() {
			for it := range t.work {
				t.deliverIn(it.Event)
				_ = t.kv.Delete(it.Key)
			}
		}()
	})
}

// deliverIn hands one event to the agent: its attachments first (uploaded,
// then named in the message), then the message. A refusal is final; failing
// to reach the agent is retried (1 s doubling to a minute, about an hour).
func (t *Tile) deliverIn(e Event) {
	m := agentMsg{EventID: e.ID, Conversation: e.Conversation, Thread: e.Thread, MessageID: e.MessageID,
		AssistantThread: e.AssistantThread, Mentioned: e.Mentioned, Text: e.Text, Command: e.Command}
	m.Sender.ID, m.Sender.Name = e.Sender.ID, e.Sender.Name
	where := orStr(e.Conversation.Name, e.Conversation.ID)
	if e.Conversation.Type == "dm" {
		where = "DM"
	}
	backoff := time.Second
	for attempt := 0; attempt < 80; attempt++ {
		ch, ok := t.channelOf(e.Account)
		if !ok {
			t.sleep(&backoff) // the account isn't announced yet (a replay before the platform started)
			continue
		}
		m.ChannelID = ch.ChannelID
		if m.Files == nil && len(e.Files) > 0 {
			ids, err := t.uploadFiles(ch.ChannelID, e)
			if err != nil {
				t.sleep(&backoff)
				continue
			}
			m.Files = ids
		}
		v, err := t.agent.message(m)
		if err == nil {
			verdict := "accepted"
			switch {
			case v.Dup:
				verdict = "seen before"
			case !v.Accepted:
				verdict = v.Reason
			case v.Command != "":
				verdict = "/" + v.Command
			}
			text := orStr(e.Text, "/"+e.Command)
			if len(e.Files) > 0 {
				text += fmt.Sprintf(" [+%d file(s)]", len(e.Files))
			}
			// the verdict tells the channel's state too (the page shows it)
			if state := "active"; !v.Dup {
				if v.Reason == "unclaimed" || v.Reason == "disabled" {
					state = v.Reason
				}
				if state != ch.Claimed {
					t.channelState(ch.ChannelID, state)
				}
			}
			t.logEvent(eventLog{Kind: "in", Where: where, Who: e.Sender.Name, Text: text, Verdict: verdict})
			return
		}
		if errors.Is(err, errUnknownChannel) {
			if p := t.platform(); p != nil {
				_ = bridge{t}.Account(context.Background(), ch.Account) // the agent forgot it: hello again
			}
			m.Files = nil // staged under the old channel
		}
		t.sleep(&backoff)
	}
	t.logEvent(eventLog{Kind: "error", Where: where, Text: "gave up reaching the agent: " + clip(e.Text, 60)})
}

func (t *Tile) sleep(backoff *time.Duration) {
	time.Sleep(*backoff)
	if *backoff *= 2; *backoff > time.Minute {
		*backoff = time.Minute
	}
}

// uploadFiles stages each attachment with the agent. A file that can't be
// read or is too large is skipped with a note — the message still goes.
func (t *Tile) uploadFiles(chID int64, e Event) ([]string, error) {
	var ids []string
	for _, f := range e.Files {
		data := f.Data
		if data == nil && f.Size >= 0 {
			p := t.platform()
			if p == nil {
				return nil, errors.New("no platform")
			}
			rc, err := p.Fetch(context.Background(), e.Account, f)
			if err != nil {
				t.logEvent(eventLog{Kind: "error", Text: "couldn't fetch " + f.Name + ": " + err.Error()})
				continue
			}
			data, err = io.ReadAll(io.LimitReader(rc, maxFile+1))
			rc.Close()
			if err != nil {
				continue
			}
		}
		if data == nil || len(data) > maxFile {
			t.logEvent(eventLog{Kind: "error", Text: f.Name + " is over 16 MiB: not passed on"})
			continue
		}
		id, err := t.agent.upload(chID, orStr(f.Name, "file"), f.Mime, data)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
