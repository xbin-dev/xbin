package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// envelope is one Socket Mode frame.
type envelope struct {
	Type         string          `json:"type"` // hello | events_api | slash_commands | interactive | disconnect
	EnvelopeID   string          `json:"envelope_id"`
	Payload      json.RawMessage `json:"payload"`
	Reason       string          `json:"reason"` // disconnect: warning | refresh_requested | link_disabled
	RetryAttempt int             `json:"retry_attempt"`
}

// Keepalive: a ping every pingEvery; a connection silent (no frame, no pong)
// for socketIdle is dead and replaced.
const (
	pingEvery  = 30 * time.Second
	socketIdle = 90 * time.Second
)

// socket runs one Socket Mode connection until it ends. Every event is
// spooled (kv) BEFORE it is acked — Slack wants the ack within 3 s and
// won't resend an acked event, so the spool is what survives a crash — and
// handed to the worker, which talks to the agent. A disconnect frame means
// Slack is about to close this connection: the caller opens a new one at
// once (events in the gap are retried by Slack; the agent dedupes them).
func (t *Tile) socket(api *slackAPI, app string) error {
	u, err := api.connectionsOpen(app)
	if err != nil {
		return err
	}
	d := websocket.Dialer{HandshakeTimeout: 20 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, _, err := d.Dial(u, nil)
	if err != nil {
		return fmt.Errorf("connecting to Slack: %w", err)
	}
	defer conn.Close()
	var wmu sync.Mutex
	send := func(v any) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(v)
	}
	alive := func() { _ = conn.SetReadDeadline(time.Now().Add(socketIdle)) }
	conn.SetPongHandler(func(string) error { alive(); return nil })
	conn.SetPingHandler(func(data string) error {
		alive()
		wmu.Lock()
		defer wmu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})
	alive()
	done := make(chan struct{})
	defer close(done)
	go func() {
		tk := time.NewTicker(pingEvery)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				wmu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				wmu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("the Slack connection dropped: %w", err)
		}
		alive()
		var env envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case "hello":
			t.setPhase("connected", "")
		case "disconnect":
			if env.Reason == "link_disabled" {
				return errLinkDisabled
			}
			return errRefresh
		case "events_api", "slash_commands":
			ack := map[string]any{"envelope_id": env.EnvelopeID}
			if env.Type == "slash_commands" {
				if msg := slashReply(env.Payload); msg != "" {
					ack["payload"] = map[string]string{"text": msg}
				}
			}
			if err := t.spool(env); err != nil {
				t.logEvent(eventLog{Kind: "error", Text: "couldn't keep an event (it will come again): " + err.Error()})
				continue // not acked: Slack retries it
			}
			if err := send(ack); err != nil {
				return fmt.Errorf("the Slack connection dropped: %w", err)
			}
		default:
			if env.EnvelopeID != "" {
				_ = send(map[string]string{"envelope_id": env.EnvelopeID})
			}
		}
	}
}

// --- the spool and the worker ------------------------------------------------------

type spoolItem struct {
	Key     string          `json:"-"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func (t *Tile) spool(env envelope) error {
	it := spoolItem{Key: fmt.Sprintf("spool/%020d-%s", time.Now().UnixNano(), env.EnvelopeID), Type: env.Type, Payload: env.Payload}
	b, _ := json.Marshal(it)
	if err := t.kv.Put(it.Key, b); err != nil {
		return err
	}
	t.work <- it
	return nil
}

// replaySpool queues what a previous process acked but never delivered, in
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

// ensureWorker starts the one worker (once the agent knows this channel):
// events go to the agent in order, each until it is answered.
func (t *Tile) ensureWorker() {
	t.wo.Do(func() {
		go func() {
			for it := range t.work {
				t.deliverIn(it)
				_ = t.kv.Delete(it.Key)
			}
		}()
	})
}

// deliverIn hands one event to the agent. A refusal is final; a failure to
// reach the agent is retried (1 s doubling to a minute, for about an hour —
// the agent may be restarting). An unknown channel (the agent forgot it)
// says hello again first.
func (t *Tile) deliverIn(it spoolItem) {
	m, ok := t.toMessage(it)
	if !ok {
		return
	}
	backoff := time.Second
	for attempt := 0; attempt < 80; attempt++ {
		t.mu.Lock()
		m.ChannelID = t.st.ChannelID
		t.mu.Unlock()
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
			t.logEvent(eventLog{Kind: "in", Where: m.where(), Who: m.Sender.Name, Text: orStr(m.Text, "/"+m.Command), Verdict: verdict})
			return
		}
		if errors.Is(err, errUnknownChannel) {
			t.rehello()
		}
		time.Sleep(backoff)
		if backoff *= 2; backoff > time.Minute {
			backoff = time.Minute
		}
	}
	t.logEvent(eventLog{Kind: "error", Where: m.where(), Text: "gave up reaching the agent: " + clip(m.Text, 60)})
}

func (t *Tile) rehello() {
	t.mu.Lock()
	who := authInfo{Team: t.st.Team, TeamID: t.st.TeamID, User: t.st.BotName, UserID: t.st.BotUser}
	t.mu.Unlock()
	if who.TeamID == "" {
		return
	}
	if ch, err := t.agent.hello(who); err == nil {
		t.mu.Lock()
		t.st.ChannelID, t.st.Claimed = ch.ChannelID, ch.State
		t.mu.Unlock()
	}
}

func (m *agentMsg) where() string {
	if m.Conversation.Type == "dm" {
		return "DM"
	}
	return "#" + strings.TrimPrefix(orStr(m.Conversation.Name, m.Conversation.ID), "#")
}
