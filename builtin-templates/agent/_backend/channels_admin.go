// channels_admin.go — the owner's side of chat channels (D86): claiming a
// channel an adapter announced, its rules, the people it knows (pairing
// approvals, trust), its sessions and the replies that could not be
// delivered. Channels are also an automation kind: the Automations page
// lists them with their conversations.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// evAutomation tells list streams that an automation changed ({kind, id}):
// a channel announced itself, a stranger asked to pair, a delivery failed.
const evAutomation = "automation"

func emitAutomation(kind string, id int64) {
	if agent == nil || agent.eng == nil {
		return
	}
	var ch *Channel
	if kind == "channel" {
		ch, _ = agent.db.getChannel(id)
	}
	agent.eng.hub.publishTo(func(s *subscriber) bool {
		if s.root != 0 {
			return false
		}
		if ch != nil && ch.access(s.w) >= lvViewer {
			return true
		}
		return s.w.manager() && s.w.viewedBy == ""
	}, &Event{Type: evAutomation, Data: map[string]any{"kind": kind, "id": id}})
}

func init() {
	registerAutomationKind(automationKind{Kind: "channel", Origin: "channel", List: channelItems})
}

// channelItems lists channels as automations: yours and the team's; for a
// manager also the others' (that they exist, not how they are set up) and
// the unclaimed ones, to claim.
func channelItems(w who) []AutomationItem {
	var out []AutomationItem
	for _, c := range agent.db.listChannels() {
		it := AutomationItem{Kind: "channel", ID: c.ID, Name: orStr(c.Name, c.platformName()+" · "+orStr(c.AccountName, c.AccountID)),
			Owner: c.Owner, Visibility: c.Visibility, Enabled: c.State == chActive, Mode: c.State,
			Summary: c.platformName() + " · " + orStr(c.AccountName, c.AccountID) + " via " + c.Adapter}
		switch lv := c.access(w); {
		case c.State == chUnclaimed && !(w.manager() && w.viewedBy == ""):
			continue
		case c.State == chUnclaimed:
			it.Access, it.Attention = "claim", 1
			it.Config = channelDetail(c, false)
		case lv >= lvOwner:
			it.Access = "owner"
			d := channelDetail(c, true)
			it.Config, it.Attention = d, d["pendingPeers"].(int)+d["failedDeliveries"].(int)
		case lv >= lvViewer:
			it.Access = "viewer"
			it.Config = channelDetail(c, false)
		case w.manager() && w.viewedBy == "":
			it.Access = "oversee"
		default:
			continue
		}
		out = append(out, it)
	}
	return out
}

// channelDetail is what the Automations page shows about a channel; the
// rules and the pairing queue are the owner's.
func channelDetail(c *Channel, owner bool) map[string]any {
	d := map[string]any{"adapter": c.Adapter, "platform": c.Platform, "accountId": c.AccountID, "accountName": c.AccountName,
		"botName": c.BotName, "features": orSlice(c.Features), "state": c.State, "lastSeen": c.LastSeen, "claimedAt": c.ClaimedAt}
	if owner {
		d["policy"] = c.Policy
		var pending, failed int
		_ = agent.db.q.QueryRow(`SELECT count(*) FROM channel_peers WHERE channel_id=? AND state='pending' AND code_expires>?`, c.ID, now()).Scan(&pending)
		_ = agent.db.q.QueryRow(`SELECT count(*) FROM outbox WHERE channel_id=? AND state='failed'`, c.ID).Scan(&failed)
		d["pendingPeers"], d["failedDeliveries"] = pending, failed
	}
	return d
}

// channelFor resolves {id} with at least min access (404 when the caller
// may not see it at all — managers see every channel exist).
func channelFor(w http.ResponseWriter, r *http.Request, min level) (*Channel, who, bool) {
	c := callerOf(r)
	ch, err := agent.db.getChannel(pathID(r))
	lv := lvNone
	if err == nil {
		lv = ch.access(c)
	}
	if err != nil || (lv == lvNone && !c.manager()) {
		xbin.WriteError(w, 404, "no such channel")
		return nil, c, false
	}
	if lv < min {
		xbin.WriteError(w, 403, "only the channel's owner can do that")
		return nil, c, false
	}
	return ch, c, true
}

type channelPatch struct {
	Name       *string        `json:"name"`
	Visibility *string        `json:"visibility"`
	Policy     *channelPolicy `json:"policy"`
	Enabled    *bool          `json:"enabled"`
}

// handleChannelClaim makes an announced channel an automation: its owner is
// the claiming manager, and it starts with the safe defaults (DMs by pairing,
// groups by allowlist, the web lane) unless the claim sets rules.
//
//	POST /channels/{id}/claim {name?, visibility?, policy?}
func handleChannelClaim(w http.ResponseWriter, r *http.Request) {
	ch, c, ok := channelFor(w, r, lvNone)
	if !ok {
		return
	}
	if ch.State != chUnclaimed {
		xbin.WriteError(w, 409, "this channel is already claimed")
		return
	}
	var p channelPatch
	_ = json.NewDecoder(r.Body).Decode(&p)
	ch.Owner, ch.State, ch.Visibility, ch.ClaimedAt = c.tag(), chActive, visPrivate, now()
	if msg := applyChannelPatch(ch, p); msg != "" {
		xbin.WriteError(w, 400, msg)
		return
	}
	if err := agent.db.saveChannel(ch); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	emitAutomation("channel", ch.ID)
	outChannel(ch, ch.State)
	xbin.WriteJSON(w, 200, ch)
}

func applyChannelPatch(ch *Channel, p channelPatch) string {
	if p.Name != nil {
		ch.Name = clip(strings.TrimSpace(*p.Name), 80)
	}
	if p.Visibility != nil {
		if *p.Visibility != visPrivate && *p.Visibility != visTeam {
			return "visibility is private or team"
		}
		ch.Visibility = *p.Visibility
	}
	if p.Policy != nil {
		if msg := p.Policy.validate(); msg != "" {
			return msg
		}
		ch.Policy = *p.Policy
	}
	return ""
}

func (d *DB) saveChannel(ch *Channel) error {
	pol, _ := json.Marshal(ch.Policy)
	_, err := d.q.Exec(`UPDATE channels SET state=?, owner=?, visibility=?, name=?, policy=?, claimed_at=? WHERE id=?`,
		ch.State, ch.Owner, ch.Visibility, ch.Name, string(pol), ch.ClaimedAt, ch.ID)
	return err
}

// handleChannelUpdate changes a channel: its owner everything, a manager only
// whether it runs. Its conversations follow a visibility change.
//
//	PUT /channels/{id} {name?, visibility?, policy?, enabled?}
func handleChannelUpdate(w http.ResponseWriter, r *http.Request) {
	ch, c, ok := channelFor(w, r, lvNone)
	if !ok {
		return
	}
	var p channelPatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	if ch.State == chUnclaimed {
		xbin.WriteError(w, 409, "claim the channel first")
		return
	}
	owner := ch.access(c) >= lvOwner
	if !owner && !c.manager() {
		xbin.WriteError(w, 403, "only the channel's owner or a manager can change it")
		return
	}
	if !owner && (p.Name != nil || p.Visibility != nil || p.Policy != nil) {
		xbin.WriteError(w, 403, "only the channel's owner can change its settings")
		return
	}
	prevVis, prevState := ch.Visibility, ch.State
	if msg := applyChannelPatch(ch, p); msg != "" {
		xbin.WriteError(w, 400, msg)
		return
	}
	if p.Enabled != nil {
		ch.State = map[bool]string{true: chActive, false: chDisabled}[*p.Enabled]
	}
	if err := agent.db.saveChannel(ch); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if ch.Visibility != prevVis && ch.Owner != "" {
		_, _ = agent.db.q.Exec(`UPDATE runs SET visibility=? WHERE origin='channel' AND origin_id=?`, ch.Visibility, ch.ID)
		_, _ = agent.db.q.Exec(`UPDATE sessions SET visibility=? WHERE origin='channel' AND origin_id=?`, ch.Visibility, ch.ID)
		agent.acl.flush(0)
	}
	emitAutomation("channel", ch.ID)
	if ch.State != prevState {
		outChannel(ch, ch.State)
	}
	xbin.WriteJSON(w, 200, ch)
}

// handleChannelDelete forgets a channel (its conversations stay, listed under
// no automation). A still-bound adapter's next hello announces it again,
// unclaimed.
func handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	ch, c, ok := channelFor(w, r, lvNone)
	if !ok {
		return
	}
	if ch.access(c) < lvOwner && !c.manager() {
		xbin.WriteError(w, 403, "only the channel's owner or a manager can remove it")
		return
	}
	err := agent.db.Tx(func(t *DB) error {
		for _, q := range []string{`DELETE FROM channels WHERE id=?`, `DELETE FROM channel_peers WHERE channel_id=?`,
			`DELETE FROM channel_events WHERE channel_id=?`, `DELETE FROM outbox WHERE channel_id=?`} {
			if _, err := t.q.Exec(q, ch.ID); err != nil {
				return err
			}
		}
		_, err := t.q.Exec(`UPDATE sessions SET run_id=0 WHERE origin='channel' AND origin_id=?`, ch.ID)
		return err
	})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	emitAutomation("channel", ch.ID)
	outChannel(ch, "removed")
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleChannelPeers lists the people the channel knows, pending first.
func handleChannelPeers(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := channelFor(w, r, lvOwner)
	if !ok {
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"peers": agent.db.listPeers(ch.ID)})
}

// handleChannelPair approves the stranger holding a pairing code.
//
//	POST /channels/{id}/pair {code}
func handleChannelPair(w http.ResponseWriter, r *http.Request) {
	ch, c, ok := channelFor(w, r, lvOwner)
	if !ok {
		return
	}
	var b struct{ Code string }
	_ = json.NewDecoder(r.Body).Decode(&b)
	code := strings.ToUpper(strings.TrimSpace(b.Code))
	var peer *chanPeer
	err := agent.db.Tx(func(t *DB) error {
		p, err := scanPeer(t.q.QueryRow(`SELECT `+peerCols+` FROM channel_peers WHERE channel_id=? AND state='pending' AND code=? AND code<>'' AND code_expires>?`,
			ch.ID, code, now()).Scan)
		if err != nil {
			return err
		}
		peer = p
		_, err = t.q.Exec(`UPDATE channel_peers SET state='allowed', code='', code_expires=0, approved_by=? WHERE channel_id=? AND peer_id=?`,
			orStr(c.tag(), "system"), ch.ID, p.PeerID)
		return err
	})
	if err != nil || peer == nil {
		xbin.WriteError(w, 404, "no pending pairing with that code (it may have expired)")
		return
	}
	emitAutomation("channel", ch.ID)
	xbin.WriteJSON(w, 200, map[string]any{"ok": "true", "peerId": peer.PeerID, "name": peer.Name})
}

// handleChannelPeerPut sets a person's standing: allowed or blocked, and
// trusted (the private lane, when the channel opens it; /approve).
//
//	PUT /channels/{id}/peers/{peer} {state?, trusted?, name?, unlink?}
func handleChannelPeerPut(w http.ResponseWriter, r *http.Request) {
	ch, c, ok := channelFor(w, r, lvOwner)
	if !ok {
		return
	}
	var b struct {
		State   *string `json:"state"`
		Trusted *bool   `json:"trusted"`
		Name    *string `json:"name"`
		Unlink  bool    `json:"unlink"` // forget its xbin account (only the person can link one)
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		xbin.WriteError(w, 400, "bad json")
		return
	}
	peerID := r.PathValue("peer")
	if peerID == "" || len(peerID) > 200 {
		xbin.WriteError(w, 400, "bad peer id")
		return
	}
	if b.State != nil && *b.State != "allowed" && *b.State != "blocked" {
		xbin.WriteError(w, 400, "state is allowed or blocked")
		return
	}
	err := agent.db.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`INSERT INTO channel_peers (channel_id, peer_id, state, created) VALUES (?, ?, 'allowed', ?)
			ON CONFLICT DO NOTHING`, ch.ID, peerID, now()); err != nil {
			return err
		}
		if b.State != nil {
			_, _ = t.q.Exec(`UPDATE channel_peers SET state=?, code='', code_expires=0, approved_by=? WHERE channel_id=? AND peer_id=?`,
				*b.State, orStr(c.tag(), "system"), ch.ID, peerID)
		}
		if b.Trusted != nil {
			_, _ = t.q.Exec(`UPDATE channel_peers SET trusted=? WHERE channel_id=? AND peer_id=?`, b2i(*b.Trusted), ch.ID, peerID)
		}
		if b.Name != nil {
			_, _ = t.q.Exec(`UPDATE channel_peers SET name=? WHERE channel_id=? AND peer_id=?`, clip(*b.Name, 80), ch.ID, peerID)
		}
		if b.Unlink {
			_, _ = t.q.Exec(`UPDATE channel_peers SET xbin_user='', linked_at=0 WHERE channel_id=? AND peer_id=?`, ch.ID, peerID)
		}
		return nil
	})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	emitAutomation("channel", ch.ID)
	xbin.WriteJSON(w, 200, agent.db.getPeer(ch.ID, peerID))
}

func handleChannelPeerDelete(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := channelFor(w, r, lvOwner)
	if !ok {
		return
	}
	_, _ = agent.db.q.Exec(`DELETE FROM channel_peers WHERE channel_id=? AND peer_id=?`, ch.ID, r.PathValue("peer"))
	emitAutomation("channel", ch.ID)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleChannelSessions lists the channel's sessions: key, current run,
// resets, last message.
func handleChannelSessions(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := channelFor(w, r, lvViewer)
	if !ok {
		return
	}
	rows, err := agent.db.q.Query(`SELECT key, run_id, resets, reset_policy, created, last_in, address FROM sessions
		WHERE origin='channel' AND origin_id=? ORDER BY last_in DESC LIMIT 200`, ch.ID)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key, policy, addr string
		var run, created, lastIn int64
		var resets int
		if rows.Scan(&key, &run, &resets, &policy, &created, &lastIn, &addr) == nil {
			out = append(out, map[string]any{"key": key, "runId": run, "resets": resets, "reset": policy, "created": created,
				"lastIn": lastIn, "address": json.RawMessage(orStr(addr, "{}"))})
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{"sessions": out})
}

// handleChannelSessionReset starts a session afresh (like /new from the
// chat).
//
//	POST /channels/{id}/sessions/reset {key}
func handleChannelSessionReset(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := channelFor(w, r, lvOwner)
	if !ok {
		return
	}
	var b struct{ Key string }
	_ = json.NewDecoder(r.Body).Decode(&b)
	if !strings.HasPrefix(b.Key, "chan:"+strconv.FormatInt(ch.ID, 10)+":") {
		xbin.WriteError(w, 400, "not one of this channel's sessions")
		return
	}
	agent.db.resetSession(b.Key)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleChannelOutbox lists the channel's undelivered replies: pending
// (?state=pending) or failed (the default).
func handleChannelOutbox(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := channelFor(w, r, lvOwner)
	if !ok {
		return
	}
	state := orStr(r.URL.Query().Get("state"), "failed")
	if state != "failed" && state != "pending" {
		xbin.WriteError(w, 400, "state is failed or pending")
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": agent.db.outRows(`WHERE channel_id=? AND state=? ORDER BY id DESC LIMIT 100`, ch.ID, state)})
}

// handleChannelRetry puts a failed reply back in the queue.
func handleChannelRetry(w http.ResponseWriter, r *http.Request) {
	ch, _, ok := channelFor(w, r, lvOwner)
	if !ok {
		return
	}
	oid, _ := strconv.ParseInt(r.PathValue("oid"), 10, 64)
	res, err := agent.db.q.Exec(`UPDATE outbox SET state='pending', error='' WHERE id=? AND channel_id=? AND state='failed'`, oid, ch.ID)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		xbin.WriteError(w, 404, fmt.Sprintf("no failed reply #%d", oid))
		return
	}
	outboxKick()
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}
