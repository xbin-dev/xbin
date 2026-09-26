package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// bridgeFrame is a call from the bridge tile's own page: the adapter's path
// and role, and the person xbind attributes (user, their level on the agent).
func bridgeFrame(t *testing.T, mux *http.ServeMux, user, level, viewedBy, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, target, bytes.NewReader(b))
	r.Header.Set("X-XBin-From", "apps/slack")
	r.Header.Set("X-XBin-Role", "channel")
	if user != "" {
		r.Header.Set("X-XBin-User", user)
		r.Header.Set("X-XBin-User-Level", level)
	}
	if viewedBy != "" {
		r.Header.Set("X-XBin-Viewed-By", viewedBy)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

var codeRe = regexp.MustCompile(`[A-Z2-9]{8}`)

func lastCode(ag *Agent, ch int64) string {
	n := outOfKind(ag, ch, "notice")
	if len(n) == 0 {
		return ""
	}
	return codeRe.FindString(n[len(n)-1].Body.Text)
}

// Linking: the code the bot gives proves the chat account, the signed-in
// page proves the xbin account — the agent takes the person from xbind, never
// from the bridge. Linked, a DM conversation is the person's own (listed with
// their chats, private to them) and group messages carry their id.
func TestChannelLinking(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(nil, say("hi"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"dm": map[string]any{"policy": "linked"}, "groups": map[string]any{"policy": "open", "linkedOnly": true}})

	if v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hello")); v.Reason != "link-required" {
		t.Fatalf("an unlinked DM under dm.policy linked: %+v", v)
	}
	code := lastCode(ag, ch)
	if code == "" || !strings.Contains(outOfKind(ag, ch, "notice")[0].Body.Text, "only talk with people who linked") {
		t.Fatalf("the link code: %+v", outOfKind(ag, ch, "notice"))
	}
	// the bridge backend (no person) can't link; nor view-as; nor a person who can't open the agent
	if w := adapterCall(t, mux, "apps/slack", "POST", "/adapter/link", map[string]string{"code": code}); w.Code != 403 {
		t.Fatalf("a link without a person: %d", w.Code)
	}
	if w := bridgeFrame(t, mux, "alice", "read", "mgr", "POST", "/adapter/link", map[string]string{"code": code}); w.Code != 403 {
		t.Fatalf("a link while viewing as: %d", w.Code)
	}
	if w := bridgeFrame(t, mux, "alice", "", "", "POST", "/adapter/link", map[string]string{"code": code}); w.Code != 403 {
		t.Fatalf("a link by someone who can't open the agent: %d", w.Code)
	}
	if w := bridgeFrame(t, mux, "alice", "read", "", "POST", "/adapter/link", map[string]string{"code": "WRONGCOD"}); w.Code != 404 {
		t.Fatalf("a wrong code: %d", w.Code)
	}
	if w := bridgeFrame(t, mux, "alice", "read", "", "POST", "/adapter/link", map[string]string{"code": strings.ToLower(code)}); w.Code != 200 {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	if n := outOfKind(ag, ch, "notice"); !strings.Contains(n[len(n)-1].Body.Text, "@alice") {
		t.Fatalf("the person is told: %+v", n[len(n)-1])
	}
	var links struct{ Links []linkView }
	_ = json.Unmarshal(bridgeFrame(t, mux, "alice", "read", "", "GET", "/adapter/links", nil).Body.Bytes(), &links)
	if len(links.Links) != 1 || links.Links[0].PeerID != "uma" {
		t.Fatalf("alice's links: %+v", links)
	}

	// linked: her DM is hers
	v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hello again"))
	run, _ := ag.db.getRun(v.RunID)
	if !v.Accepted || run.Owner != "alice" || run.Visibility != visPrivate {
		t.Fatalf("a linked DM: %+v owner %q", v, run.Owner)
	}
	var conv convPage
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/conversations", nil).Body.Bytes(), &conv)
	if ids := convIDs(conv.Items); len(ids) != 1 || ids[0] != v.RunID {
		t.Fatalf("alice's list has her Slack DM: %v", ids)
	}
	if w := callAs(t, mux, asMgr, "GET", fmt.Sprintf("/runs/%d/view", v.RunID), nil); w.Code != 404 {
		t.Fatalf("the channel's owner reads alice's DM: %d", w.Code)
	}
	// in a group: unlinked people aren't heard; alice is, by her id
	g := chMsg(ch, "channel", "C1", "bo", "anyone?")
	g["mentioned"] = true
	if v := chPost(t, mux, g); v.Reason != "link-required" {
		t.Fatalf("an unlinked group sender: %+v", v)
	}
	g = chMsg(ch, "channel", "C1", "uma", "status please")
	g["mentioned"] = true
	gv := chPost(t, mux, g)
	waitFor(t, "the group turn", func() bool { return strings.Contains(fullText(ag.db, gv.RunID), "Uma (@alice): status please") })

	// the person unlinks: the next DM goes back to asking
	if w := bridgeFrame(t, mux, "alice", "read", "", "DELETE", fmt.Sprintf("/adapter/links/%d/uma", ch), nil); w.Code != 200 {
		t.Fatalf("unlink: %d", w.Code)
	}
	if v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "still there?")); v.Reason != "link-required" {
		t.Fatalf("after unlinking: %+v", v)
	}
}

// /link gives an allowed person a code; linking a person the owner paired
// moves their DM to a conversation of their own.
func TestChannelLinkCommand(t *testing.T) {
	ag, mux := chanFixture(t)
	fakeOf(ag).on(nil, say("ok"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"dm": map[string]any{"policy": "open"}})
	before := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hello"))
	if r, _ := ag.db.getRun(before.RunID); r.Owner != "mgr" {
		t.Fatalf("an unlinked DM is the channel's: %q", r.Owner)
	}
	chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "/link"))
	code := lastCode(ag, ch)
	if w := bridgeFrame(t, mux, "bob", "read", "", "POST", "/adapter/link", map[string]string{"code": code}); w.Code != 200 {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
	after := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hello again"))
	if r, _ := ag.db.getRun(after.RunID); after.RunID == before.RunID || r.Owner != "bob" {
		t.Fatalf("linked, the DM continues in bob's own conversation: run %d (was %d) owner %q", after.RunID, before.RunID, r.Owner)
	}
}

// Files: an attachment uploaded first lands in the conversation's session
// files, attached to the message; a reply's attach_to_reply files ride its
// outbox row and download only for the adapter whose channel it is.
func TestChannelFiles(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(lastUser("chart"), callTools(tc("w1", "file_write", `{"path":"chart.svg","content":"<svg/>"}`))).once()
	f.on(lastIs("tool", "chart.svg"), callTools(tc("a1", "attach_to_reply", `{"paths":["chart.svg"]}`))).once()
	f.on(nil, say("here it is"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"dm": map[string]any{"policy": "open"}})

	r := httptest.NewRequest("POST", fmt.Sprintf("/adapter/files?channelId=%d&name=notes%%20v1.txt&mime=text/plain", ch), strings.NewReader("line one"))
	r.Header.Set("X-XBin-From", "apps/slack")
	r.Header.Set("X-XBin-Role", "channel")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var up struct{ FileID string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &up) != nil || up.FileID == "" {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	m := chMsg(ch, "dm", "D1", "uma", "read this and make a chart")
	m["files"] = []string{up.FileID}
	v := chPost(t, mux, m)
	waitFor(t, "the reply", func() bool { return len(outOfKind(ag, ch, "answer")) == 1 })
	if tx := fullText(ag.db, v.RunID); !strings.Contains(tx, "[attached: notes-v1.txt") {
		t.Fatalf("the attachment in the message: %s", tx)
	}
	if fl, err := ag.db.replFile(v.RunID, "notes-v1.txt"); err != nil || fl.Content != "line one" {
		t.Fatalf("the session file: %+v %v", fl, err)
	}
	a := outOfKind(ag, ch, "answer")[0]
	if a.Body.Text != "here it is" || len(a.Body.Files) != 1 || a.Body.Files[0].Name != "chart.svg" {
		t.Fatalf("the reply's files: %+v", a.Body)
	}
	get := func(from string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", fmt.Sprintf("/adapter/files/%d/0", a.ID), nil)
		r.Header.Set("X-XBin-From", from)
		r.Header.Set("X-XBin-Role", "channel")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := get("apps/slack"); w.Code != 200 || w.Body.String() != "<svg/>" {
		t.Fatalf("download: %d %q", w.Code, w.Body)
	}
	if w := get("apps/evil"); w.Code != 404 {
		t.Fatalf("another adapter downloaded it: %d", w.Code)
	}
	// only channel runs get the tool
	for _, s := range toolSpecs(defaultConfig(), 0, nil) {
		if s.Function.Name == "attach_to_reply" {
			t.Fatal("a plain run was offered attach_to_reply")
		}
	}
}
