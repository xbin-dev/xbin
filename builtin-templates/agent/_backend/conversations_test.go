package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
)

type convPage struct {
	Pinned []map[string]any `json:"pinned"`
	Items  []map[string]any `json:"items"`
	Next   string           `json:"next"`
}

func convIDs(list []map[string]any) []int64 {
	var out []int64
	for _, it := range list {
		out = append(out, int64(it["id"].(float64)))
	}
	return out
}

// chatAt starts alice's conversation with a fixed activity time.
func chatAt(t *testing.T, ag *Agent, owner, title string, activity int64) int64 {
	t.Helper()
	r, err := ag.startRunOpts(runOpts{Title: title, Cfg: defaultConfig(), Hold: true,
		Stamp: runStamp{Owner: owner, Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.db.q.Exec(`UPDATE runs SET activity_ms=? WHERE id=?`, activity, r.ID); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// Paging is by (activity, id): a conversation that becomes active while you
// page never shifts or repeats rows; pinned ones come first, once; archived
// ones only on request.
func TestConversationPaging(t *testing.T) {
	ag, mux := accessFixture(t)
	var ids []int64
	for i := 1; i <= 5; i++ {
		ids = append(ids, chatAt(t, ag, "alice", fmt.Sprintf("chat %d", i), int64(1000*i)))
	}
	get := func(q string) convPage {
		w := callAs(t, mux, asAlice, "GET", "/conversations?"+q, nil)
		var p convPage
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 200 {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body.String())
		}
		return p
	}
	// pin chat 2, archive chat 4
	callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", ids[1]), map[string]bool{"pinned": true})
	callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", ids[3]), map[string]bool{"archived": true})

	p1 := get("limit=2")
	if fmt.Sprint(convIDs(p1.Pinned)) != fmt.Sprint([]int64{ids[1]}) || fmt.Sprint(convIDs(p1.Items)) != fmt.Sprint([]int64{ids[4], ids[2]}) {
		t.Fatalf("page 1: pinned %v items %v", convIDs(p1.Pinned), convIDs(p1.Items))
	}
	// chat 1 comes alive between pages: it must not reappear or shift page 2
	_, _ = ag.db.q.Exec(`UPDATE runs SET activity_ms=99999 WHERE id=?`, ids[0])
	p2 := get("limit=2&cursor=" + url.QueryEscape(p1.Next))
	if len(p2.Pinned) != 0 || len(p2.Items) != 0 || p2.Next != "" {
		t.Fatalf("page 2 (chat 1 moved to the top; nothing else is left): %v next %q", convIDs(p2.Items), p2.Next)
	}
	if a := get("archived=1"); fmt.Sprint(convIDs(a.Items)) != fmt.Sprint([]int64{ids[3]}) {
		t.Fatalf("archived: %v", convIDs(a.Items))
	}
	// bob sees none of alice's private chats
	w := callAs(t, mux, asBob, "GET", "/conversations", nil)
	var pb convPage
	_ = json.Unmarshal(w.Body.Bytes(), &pb)
	if len(pb.Items)+len(pb.Pinned) != 0 {
		t.Fatalf("bob's list: %v", convIDs(pb.Items))
	}
}

// Search finds titles and anything said, among what the caller may see.
func TestConversationSearch(t *testing.T) {
	ag, mux := accessFixture(t)
	id := chatAt(t, ag, "alice", "quarterly budget", 1000)
	if _, err := ag.db.addMessage(&Message{RunID: id, Role: "assistant", Content: "the invoice from Acme is overdue"}); err != nil {
		t.Fatal(err)
	}
	find := func(c caller, q string) convPage {
		var p convPage
		_ = json.Unmarshal(callAs(t, mux, c, "GET", "/conversations?q="+url.QueryEscape(q), nil).Body.Bytes(), &p)
		return p
	}
	if p := find(asAlice, "budget"); len(p.Items) != 1 {
		t.Fatalf("title search: %v", p.Items)
	}
	p := find(asAlice, "acme overdue")
	if len(p.Items) != 1 || p.Items[0]["match"] == nil {
		t.Fatalf("content search with a snippet: %v", p.Items)
	}
	if p := find(asBob, "acme"); len(p.Items) != 0 {
		t.Fatalf("bob found alice's private content: %v", p.Items)
	}
}

// Unread is activity after you last looked — with the upgrade as everyone's
// floor; reading clears it; your own message doesn't make it unread for you.
func TestUnreadAndRename(t *testing.T) {
	ag, mux := accessFixture(t)
	_ = ag.db.putSetting("conv_epoch_ms", "5000")
	old := chatAt(t, ag, "alice", "old", 4000)
	fresh := chatAt(t, ag, "alice", "fresh", 6000)
	unread := func() map[int64]bool {
		var p convPage
		_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/conversations", nil).Body.Bytes(), &p)
		out := map[int64]bool{}
		for _, it := range p.Items {
			out[int64(it["id"].(float64))] = it["unread"] == true
		}
		return out
	}
	if u := unread(); u[old] || !u[fresh] {
		t.Fatalf("unread: %v", u)
	}
	callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/read", fresh), nil)
	if u := unread(); u[fresh] {
		t.Fatal("read clears it")
	}
	// rename: the owner's, and it sticks as the user's title
	if got := callAs(t, mux, asBob, "PATCH", fmt.Sprintf("/runs/%d", fresh), map[string]string{"title": "x"}).Code; got != 404 {
		t.Fatalf("bob renames alice's private chat: %d", got)
	}
	if got := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", fresh), map[string]string{"title": "Budget review"}).Code; got != 200 {
		t.Fatalf("rename: %d", got)
	}
	if r, _ := ag.db.getRun(fresh); r.Title != "Budget review" || r.TitleSrc != "user" {
		t.Fatalf("renamed: %q %q", r.Title, r.TitleSrc)
	}
	// share with the team as viewers: bob sees it under scope=team, read-only
	if got := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", fresh), map[string]string{"visibility": "team", "teamRole": "viewer"}).Code; got != 200 {
		t.Fatalf("share: %d", got)
	}
	var p convPage
	_ = json.Unmarshal(callAs(t, mux, asBob, "GET", "/conversations?scope=team", nil).Body.Bytes(), &p)
	if len(p.Items) != 1 || p.Items[0]["access"] != "viewer" {
		t.Fatalf("bob's team list: %v", p.Items)
	}
	if got := callAs(t, mux, asBob, "POST", fmt.Sprintf("/runs/%d/message", fresh), map[string]string{"text": "hi"}).Code; got != 403 {
		t.Fatalf("a team viewer writes: %d", got)
	}
}

// The shared view is sharing both ways: what you shared — with the team or
// with people — and what others shared with you; never a private one, and
// never someone else's shared only with a third person.
func TestSharedScope(t *testing.T) {
	ag, mux := accessFixture(t)
	team := chatAt(t, ag, "alice", "team plan", 3000)
	people := chatAt(t, ag, "alice", "for carol", 2000)
	chatAt(t, ag, "alice", "diary", 1000)
	if got := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", team), map[string]string{"visibility": "team", "teamRole": "viewer"}).Code; got != 200 {
		t.Fatalf("share with the team: %d", got)
	}
	if got := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/members", people), map[string]string{"user": "carol", "role": "viewer"}).Code; got != 200 {
		t.Fatalf("share with carol: %d", got)
	}
	shared := func(c caller) []int64 {
		var p convPage
		_ = json.Unmarshal(callAs(t, mux, c, "GET", "/conversations?scope=shared", nil).Body.Bytes(), &p)
		return convIDs(append(p.Pinned, p.Items...))
	}
	if got, want := fmt.Sprint(shared(asAlice)), fmt.Sprint([]int64{team, people}); got != want {
		t.Fatalf("alice's shared view (what she shared): %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(shared(asCarol)), fmt.Sprint([]int64{team, people}); got != want {
		t.Fatalf("carol's shared view (team + added): %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(shared(asBob)), fmt.Sprint([]int64{team}); got != want {
		t.Fatalf("bob's shared view (the team one only): %s, want %s", got, want)
	}
	// the older scope=team stays what it was: others' team conversations
	var p convPage
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/conversations?scope=team", nil).Body.Bytes(), &p)
	if len(p.Items) != 0 {
		t.Fatalf("alice's scope=team lists her own: %v", convIDs(p.Items))
	}
}
