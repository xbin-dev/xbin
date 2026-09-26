// channel_keys.go — which session a channel message belongs to (D86): the
// OpenClaw-style keys, built here from the adapter's facts so an adapter can
// never name a session itself. Pure functions; channels.go applies them.
package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// channelPolicy is a channel's rules, as the owner set them (JSON in
// channels.policy). Zero values are the defaults; use the accessors.
type channelPolicy struct {
	DM struct {
		Policy  string `json:"policy,omitempty"`  // pairing (default) | linked | allowlist | open | disabled
		Scope   string `json:"scope,omitempty"`   // per-peer (default) | main
		Threads string `json:"threads,omitempty"` // parent (default) | thread
	} `json:"dm"`
	Groups struct {
		Policy         string   `json:"policy,omitempty"` // allowlist (default) | open | disabled
		Allow          []string `json:"allow,omitempty"`  // conversation ids (allowlist)
		RequireMention *bool    `json:"requireMention,omitempty"`
		FollowThreads  *bool    `json:"followThreads,omitempty"`
		Scope          string   `json:"scope,omitempty"`   // per-group (default) | per-user
		Threads        string   `json:"threads,omitempty"` // thread (default) | parent
		// LinkedOnly: in groups, only people who linked their xbin account
		// are heard (the others are ignored, silently — no code in public).
		LinkedOnly bool `json:"linkedOnly,omitempty"`
	} `json:"groups"`
	// PrivateLane lets trusted peers (DMs) and TrustedGroups reach the private
	// lane — internal data. Everything else runs in the web lane: a reply to a
	// chat is an egress, and a run never holds both (the lane firewall).
	PrivateLane   bool     `json:"privateLane,omitempty"`
	TrustedGroups []string `json:"trustedGroups,omitempty"`
	// TrustLinked counts people linked to an xbin account as trusted (the
	// private lane when it is open, /approve) — for a team's own workspace.
	TrustLinked bool   `json:"trustLinked,omitempty"`
	Reset       string `json:"reset,omitempty"`      // "" never | idle:<seconds> | daily:<hour>
	RatePerMin  int    `json:"ratePerMin,omitempty"` // per peer; 0 = 20
	// Deny is the tools a channel session never gets; nil = the default
	// (no schedules, no skill writes — a stranger's message must not leave
	// anything behind that outlives the conversation).
	Deny   []string `json:"deny,omitempty"`
	System string   `json:"system,omitempty"` // added to the system prompt
}

var defaultChannelDeny = []string{"schedule", "unschedule", "skill_manage"}

func parsePolicy(raw string) channelPolicy {
	var p channelPolicy
	_ = json.Unmarshal([]byte(raw), &p)
	return p
}

func (p channelPolicy) dmPolicy() string    { return orStr(p.DM.Policy, "pairing") }
func (p channelPolicy) groupPolicy() string { return orStr(p.Groups.Policy, "allowlist") }
func (p channelPolicy) requireMention() bool {
	return p.Groups.RequireMention == nil || *p.Groups.RequireMention
}
func (p channelPolicy) followThreads() bool {
	return p.Groups.FollowThreads == nil || *p.Groups.FollowThreads
}
func (p channelPolicy) ratePerMin() int {
	if p.RatePerMin > 0 {
		return p.RatePerMin
	}
	return 20
}
func (p channelPolicy) deny() []string {
	if p.Deny != nil {
		return p.Deny
	}
	return defaultChannelDeny
}

// validate refuses combinations that open the private lane to strangers.
func (p channelPolicy) validate() string {
	switch p.dmPolicy() {
	case "pairing", "linked", "allowlist", "open", "disabled":
	default:
		return "dm.policy is pairing, linked, allowlist, open or disabled"
	}
	switch p.groupPolicy() {
	case "allowlist", "open", "disabled":
	default:
		return "groups.policy is allowlist, open or disabled"
	}
	if p.PrivateLane && p.dmPolicy() == "open" {
		return "the private lane needs a closed DM policy (pairing or allowlist): with open DMs anyone could reach internal data"
	}
	if p.PrivateLane && p.groupPolicy() == "open" && len(p.TrustedGroups) > 0 {
		return "trusted groups need groups.policy allowlist"
	}
	if p.Reset != "" && !policyValid(p.Reset) {
		return "reset is idle:<seconds> or daily:<hour>"
	}
	return ""
}

func policyValid(s string) bool {
	kind, arg, _ := strings.Cut(s, ":")
	n, err := strconv.Atoi(arg)
	return err == nil && ((kind == "idle" && n > 0) || (kind == "daily" && n >= 0 && n < 24))
}

// adapterMsg is one inbound message as an adapter reports it
// (POST /adapter/message, docs/agent-inbox.md).
type adapterMsg struct {
	ChannelID    int64  `json:"channelId"`
	EventID      string `json:"eventId"`
	Conversation struct {
		ID   string `json:"id"`
		Type string `json:"type"` // dm | group | channel
		Name string `json:"name,omitempty"`
	} `json:"conversation"`
	Thread          string `json:"thread,omitempty"` // the thread's root message id
	MessageID       string `json:"messageId"`
	AssistantThread bool   `json:"assistantThread,omitempty"`
	Sender          struct {
		ID    string `json:"id"`
		Name  string `json:"name,omitempty"`
		IsBot bool   `json:"isBot,omitempty"`
	} `json:"sender"`
	Mentioned bool     `json:"mentioned,omitempty"`
	Text      string   `json:"text"`
	Command   string   `json:"command,omitempty"` // the adapter parsed a slash command: "new hello"
	Files     []string `json:"files,omitempty"`   // attachments, uploaded first (POST /adapter/files)
}

func (m *adapterMsg) dm() bool { return m.Conversation.Type == "dm" }

// channelAddr is where a reply goes, as the adapter reads it back.
type channelAddr struct {
	Conversation string `json:"conversation"`
	Type         string `json:"type"`
	Thread       string `json:"thread,omitempty"`
	User         string `json:"user,omitempty"` // the DM peer (some platforms post to users)
}

// keyPart escapes an id to the characters a key is made of (others become
// %XX), so an id holding a ':' can never forge another key, and two ids never
// share one.
func keyPart(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < 160; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// sessionKey is the session a message belongs to, and where its reply goes.
//
//	DM                    chan:<C>:dm:<user>          (dm.scope main: chan:<C>:main)
//	DM thread             the DM's key (dm.threads thread: …:thread:<root>)
//	assistant thread      chan:<C>:dm:<user>:thread:<root>, always
//	group or channel      chan:<C>:group:<conv>       (groups.scope per-user: …:user:<user>)
//	its threads           …:thread:<root>             (groups.threads parent: the group's key)
//
// In a group a top-level message starts a thread (its own id is the root),
// so each mention is a conversation of its own and the reply lands under it.
func sessionKey(chID int64, p channelPolicy, m *adapterMsg) (string, channelAddr) {
	base := "chan:" + strconv.FormatInt(chID, 10)
	addr := channelAddr{Conversation: m.Conversation.ID, Type: orStr(m.Conversation.Type, "dm")}
	if m.dm() {
		addr.User = m.Sender.ID
		key := base + ":dm:" + keyPart(m.Sender.ID)
		if p.DM.Scope == "main" {
			key = base + ":main"
		}
		addr.Thread = m.Thread
		if m.Thread != "" && (m.AssistantThread || p.DM.Threads == "thread") {
			if p.DM.Scope == "main" && m.AssistantThread {
				key = base + ":dm:" + keyPart(m.Sender.ID)
			}
			key += ":thread:" + keyPart(m.Thread)
		}
		return key, addr
	}
	key := base + ":group:" + keyPart(m.Conversation.ID)
	if p.Groups.Scope == "per-user" {
		key += ":user:" + keyPart(m.Sender.ID)
	}
	if p.Groups.Threads == "parent" {
		addr.Thread = m.Thread
		return key, addr
	}
	root := orStr(m.Thread, m.MessageID)
	addr.Thread = root
	return key + ":thread:" + keyPart(root), addr
}
