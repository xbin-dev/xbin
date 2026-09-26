package main

import (
	"encoding/json"
	"strings"
	"time"
)

// eventPayload is an events_api envelope's payload (the Events API callback).
type eventPayload struct {
	EventID string `json:"event_id"`
	TeamID  string `json:"team_id"`
	Event   struct {
		Type            string `json:"type"`
		Subtype         string `json:"subtype"`
		Channel         string `json:"channel"`
		ChannelType     string `json:"channel_type"` // im | mpim | channel | group
		User            string `json:"user"`
		BotID           string `json:"bot_id"`
		Text            string `json:"text"`
		TS              string `json:"ts"`
		ThreadTS        string `json:"thread_ts"`
		AssistantThread struct {
			UserID    string `json:"user_id"`
			ChannelID string `json:"channel_id"`
			ThreadTS  string `json:"thread_ts"`
		} `json:"assistant_thread"`
	} `json:"event"`
}

// slashPayload is a slash_commands envelope's payload.
type slashPayload struct {
	Command   string `json:"command"`
	Text      string `json:"text"`
	UserID    string `json:"user_id"`
	UserName  string `json:"user_name"`
	ChannelID string `json:"channel_id"`
	TriggerID string `json:"trigger_id"`
}

// slashReply is what the person who typed /agent sees at once (ephemeral):
// the command works in a DM with the bot; in a channel, mention the bot.
func slashReply(raw json.RawMessage) string {
	var p slashPayload
	if json.Unmarshal(raw, &p) != nil || strings.HasPrefix(p.ChannelID, "D") {
		return ""
	}
	return "Use " + orStr(p.Command, "/agent") + " in a direct message with me. In a channel, mention me — e.g. `@me /new`."
}

// agentMsg is POST /adapter/message (docs/agent-inbox.md).
type agentMsg struct {
	ChannelID    int64  `json:"channelId"`
	EventID      string `json:"eventId"`
	Conversation struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	} `json:"conversation"`
	Thread          string `json:"thread,omitempty"`
	MessageID       string `json:"messageId"`
	AssistantThread bool   `json:"assistantThread,omitempty"`
	Sender          struct {
		ID    string `json:"id"`
		Name  string `json:"name,omitempty"`
		IsBot bool   `json:"isBot,omitempty"`
	} `json:"sender"`
	Mentioned bool   `json:"mentioned,omitempty"`
	Text      string `json:"text"`
	Command   string `json:"command,omitempty"`
}

// convType maps a Slack conversation to the contract's: DMs are D…, and a
// multi-person DM is an mpim (a group); everything else is a channel.
func convType(id, channelType string) string {
	switch {
	case channelType == "im" || strings.HasPrefix(id, "D"):
		return "dm"
	case channelType == "mpim":
		return "group"
	}
	return "channel"
}

// toMessage turns a spooled event into what the agent takes, or false when
// it is nothing for the agent: the bot's own or another bot's message, an
// edit or a join, a channel message that neither mentions the bot (the
// app_mention event carries those) nor replies in a thread.
func (t *Tile) toMessage(it spoolItem) (agentMsg, bool) {
	var m agentMsg
	t.mu.Lock()
	botUser := t.st.BotUser
	t.mu.Unlock()
	if it.Type == "slash_commands" {
		var p slashPayload
		if json.Unmarshal(it.Payload, &p) != nil || !strings.HasPrefix(p.ChannelID, "D") {
			return m, false
		}
		m.EventID, m.MessageID = "slash:"+p.TriggerID, ""
		m.Conversation.ID, m.Conversation.Type = p.ChannelID, "dm"
		m.Sender.ID, m.Sender.Name = p.UserID, t.userName(p.UserID)
		m.Mentioned, m.Command = true, orStr(strings.TrimSpace(p.Text), "help")
		return m, true
	}
	var p eventPayload
	if json.Unmarshal(it.Payload, &p) != nil {
		return m, false
	}
	e := p.Event
	switch e.Type {
	case "assistant_thread_started":
		t.markAssistant(e.AssistantThread.ChannelID, e.AssistantThread.ThreadTS)
		return m, false
	case "app_mention", "message":
	default:
		return m, false
	}
	switch e.Subtype {
	case "", "file_share", "thread_broadcast":
	default:
		return m, false // edits, deletions, joins, bot_message…
	}
	if e.User == "" || e.BotID != "" || e.User == botUser {
		return m, false
	}
	typ := convType(e.Channel, e.ChannelType)
	mentions := botUser != "" && strings.Contains(e.Text, "<@"+botUser+">")
	if e.Type == "message" && typ != "dm" && (mentions || e.ThreadTS == "") {
		return m, false
	}
	m.EventID, m.MessageID, m.Thread = p.EventID, e.TS, e.ThreadTS
	m.Conversation.ID, m.Conversation.Type = e.Channel, typ
	if typ != "dm" {
		m.Conversation.Name = t.channelName(e.Channel)
	}
	m.Sender.ID, m.Sender.Name = e.User, t.userName(e.User)
	m.Mentioned = typ == "dm" || e.Type == "app_mention"
	m.AssistantThread = typ == "dm" && e.ThreadTS != "" && t.isAssistant(e.Channel, e.ThreadTS)
	m.Text = fromSlack(e.Text, botUser, t.userName)
	return m, true
}

// --- assistant threads (Slack's AI pane): a session per thread -----------------

func assistKey(channel, ts string) string { return "assist/" + channel + "/" + ts }

func (t *Tile) markAssistant(channel, ts string) {
	if channel == "" || ts == "" {
		return
	}
	t.mu.Lock()
	t.assist[channel+"/"+ts] = true
	t.mu.Unlock()
	_ = t.kv.Put(assistKey(channel, ts), []byte("1"))
}

func (t *Tile) isAssistant(channel, ts string) bool {
	t.mu.Lock()
	known, ok := t.assist[channel+"/"+ts]
	t.mu.Unlock()
	if ok {
		return known
	}
	_, err := t.kv.Get(assistKey(channel, ts))
	t.mu.Lock()
	t.assist[channel+"/"+ts] = err == nil
	t.mu.Unlock()
	return err == nil
}

// --- names (a day's cache) ---------------------------------------------------------

type nameEntry struct {
	name string
	at   time.Time
}

func (t *Tile) cachedName(key string, fetch func() (string, error)) string {
	t.mu.Lock()
	e, ok := t.names[key]
	t.mu.Unlock()
	if ok && time.Since(e.at) < 24*time.Hour {
		return e.name
	}
	name, err := fetch()
	if err != nil {
		return e.name // "" the first time; the id stands in
	}
	t.mu.Lock()
	t.names[key] = nameEntry{name: name, at: time.Now()}
	t.mu.Unlock()
	return name
}

func (t *Tile) userName(id string) string {
	if id == "" {
		return ""
	}
	return t.cachedName("u:"+id, func() (string, error) { return t.api().userName(t.secret(secretBot), id) })
}

func (t *Tile) channelName(id string) string {
	return t.cachedName("c:"+id, func() (string, error) { return t.api().channelName(t.secret(secretBot), id) })
}
