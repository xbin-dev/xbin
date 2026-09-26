// channel_links.go — linking a chat account to an xbin account (D86). A
// person messages the bot and gets a one-hour code (unasked when they are new,
// or with /link); signed in to xbin, they paste it on the bridge tile's page,
// whose frame calls POST /adapter/link. The agent takes the person from
// xbind's attribution (X-XBin-User) — never from the request — so a bridge
// can't claim anyone: the code proves the chat account, the signed-in session
// proves the xbin account. Linked, the person speaks as themselves: their DM
// conversation is theirs, their group messages carry their id, and the
// channel can admit only linked people (dm.policy linked, groups.linkedOnly)
// or trust them (trustLinked).
package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// linker is the person linking: attributed, able to use this agent, and not
// an admin viewing as someone else.
func linker(w http.ResponseWriter, r *http.Request) (string, bool) {
	c := xbin.Caller(r)
	switch {
	case c.User == "":
		xbin.WriteError(w, 403, "linking is done from the bridge's page, signed in: the person pasting the code is who it links to")
	case c.ViewedBy != "":
		xbin.WriteError(w, 403, "not while viewing as someone else")
	case c.UserLevel == "":
		xbin.WriteError(w, 403, "you can't open this agent, so a chat account can't act as you here")
	default:
		return c.User, true
	}
	return "", false
}

type linkView struct {
	ChannelID int64  `json:"channelId"`
	PeerID    string `json:"peerId"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	Account   string `json:"account"`
	LinkedAt  int64  `json:"linkedAt"`
}

// handleAdapterLink links the chat account holding the code to the caller.
//
//	POST /adapter/link {code} → {channelId, peerId, name, platform, account}
func handleAdapterLink(w http.ResponseWriter, r *http.Request) {
	user, ok := linker(w, r)
	if !ok {
		return
	}
	if !chanRate.allow("link\x00"+user, 10) {
		xbin.WriteError(w, 429, "too many tries — wait a minute")
		return
	}
	var b struct{ Code string }
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&b)
	code := strings.ToUpper(strings.TrimSpace(b.Code))
	var v linkView
	var ch *Channel
	var dmAddr string
	err := agent.db.Tx(func(t *DB) error {
		p, err := scanPeer(t.q.QueryRow(`SELECT `+prefixed("p.", peerCols)+` FROM channel_peers p JOIN channels c ON c.id=p.channel_id
			WHERE c.adapter=? AND p.code=? AND p.code<>'' AND p.code_expires>?`, adapterOf(r), code, now()).Scan)
		if err != nil {
			return err
		}
		if ch, err = t.getChannel(p.ChannelID); err != nil {
			return err
		}
		if _, err := t.q.Exec(`UPDATE channel_peers SET xbin_user=?, linked_at=?, state=CASE WHEN state='blocked' THEN state ELSE 'allowed' END,
			code='', code_expires=0, approved_by=CASE WHEN approved_by='' THEN ? ELSE approved_by END WHERE channel_id=? AND peer_id=?`,
			user, now(), "link:"+user, p.ChannelID, p.PeerID); err != nil {
			return err
		}
		dmAddr = p.DMAddr
		if dmAddr != "" {
			t.outboxAdd(ch.ID, "", 0, "notice", dmAddr, "Linked: I know you as @"+user+" now.")
		}
		v = linkView{ChannelID: p.ChannelID, PeerID: p.PeerID, Name: p.Name, Platform: ch.Platform, Account: orStr(ch.AccountName, ch.AccountID), LinkedAt: now()}
		return nil
	})
	if err != nil {
		xbin.WriteError(w, 404, "no chat account holds that code (it may have expired — send /link to the bot for a new one)")
		return
	}
	emitAutomation("channel", v.ChannelID)
	xbin.WriteJSON(w, 200, v)
}

// handleAdapterLinks lists the caller's linked chat accounts on this adapter.
func handleAdapterLinks(w http.ResponseWriter, r *http.Request) {
	user, ok := linker(w, r)
	if !ok {
		return
	}
	rows, err := agent.db.q.Query(`SELECT p.channel_id, p.peer_id, p.name, c.platform, CASE WHEN c.account_name<>'' THEN c.account_name ELSE c.account_id END, p.linked_at
		FROM channel_peers p JOIN channels c ON c.id=p.channel_id WHERE c.adapter=? AND p.xbin_user=? ORDER BY p.linked_at`, adapterOf(r), user)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []linkView{}
	for rows.Next() {
		var l linkView
		if rows.Scan(&l.ChannelID, &l.PeerID, &l.Name, &l.Platform, &l.Account, &l.LinkedAt) == nil {
			out = append(out, l)
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{"user": user, "links": out})
}

// handleAdapterUnlink undoes one of the caller's links.
//
//	DELETE /adapter/links/{cid}/{peer}
func handleAdapterUnlink(w http.ResponseWriter, r *http.Request) {
	user, ok := linker(w, r)
	if !ok {
		return
	}
	cid, _ := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	res, err := agent.db.q.Exec(`UPDATE channel_peers SET xbin_user='', linked_at=0 WHERE channel_id=? AND peer_id=? AND xbin_user=?
		AND channel_id IN (SELECT id FROM channels WHERE adapter=?)`, cid, r.PathValue("peer"), user, adapterOf(r))
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		xbin.WriteError(w, 404, "no such link of yours")
		return
	}
	emitAutomation("channel", cid)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// prefixed qualifies a column list: "a, b" → "p.a, p.b".
func prefixed(p, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
