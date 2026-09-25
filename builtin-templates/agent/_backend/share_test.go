package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Sharing by invite and by link: a member reads (viewer) or talks
// (participant); removing them takes it away; a link's token is never
// stored, is single-use when asked, can be revoked, and never lowers a role.
func TestSharing(t *testing.T) {
	ag, mux := accessFixture(t)
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	view := func(c caller) int { return callAs(t, mux, c, "GET", fmt.Sprintf("/runs/%d/view", id), nil).Code }
	talk := func(c caller) int {
		return callAs(t, mux, c, "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "hi"}).Code
	}

	// invite
	if got := callAs(t, mux, asBob, "POST", fmt.Sprintf("/runs/%d/members", id), map[string]string{"user": "bob"}).Code; got != 404 {
		t.Fatalf("bob can't add himself to alice's private chat: %d", got)
	}
	if got := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/members", id), map[string]string{"user": "bob", "role": "viewer"}).Code; got != 200 {
		t.Fatalf("invite: %d", got)
	}
	if view(asBob) != 200 || talk(asBob) != 403 {
		t.Fatal("a viewer reads, doesn't talk")
	}
	callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/members", id), map[string]string{"user": "bob", "role": "participant"})
	if talk(asBob) != 200 {
		t.Fatal("a participant talks")
	}
	if got := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/members", id), map[string]string{"user": "Not A User!"}).Code; got != 400 {
		t.Fatalf("a bad id: %d", got)
	}

	// links
	w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/links", id), map[string]any{"role": "viewer", "maxUses": 1})
	var link struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &link)
	var stored int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM share_links WHERE token_hash=?`, link.Token).Scan(&stored)
	if link.Token == "" || stored != 0 {
		t.Fatalf("the token is shown once and never stored: %q (%d rows)", link.Token, stored)
	}
	if got := callAs(t, mux, asCarol, "POST", "/join", map[string]string{"token": link.Token}).Code; got != 200 {
		t.Fatalf("carol joins: %d", got)
	}
	if view(asCarol) != 200 || talk(asCarol) != 403 {
		t.Fatal("carol joined as a viewer")
	}
	if got := callAs(t, mux, asDave, "POST", "/join", map[string]string{"token": link.Token}).Code; got != 404 {
		t.Fatalf("a single-use link is used up: %d", got)
	}
	if got := callAs(t, mux, asElement, "POST", "/join", map[string]string{"token": link.Token}).Code; got != 403 {
		t.Fatalf("only a person joins: %d", got)
	}
	// a viewer link never lowers bob (participant)
	w = callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/links", id), map[string]any{"role": "viewer"})
	_ = json.Unmarshal(w.Body.Bytes(), &link)
	callAs(t, mux, asBob, "POST", "/join", map[string]string{"token": link.Token})
	if talk(asBob) != 200 {
		t.Fatal("joining by a viewer link lowered a participant")
	}
	callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d/links/%d", id, link.ID), nil)
	if got := callAs(t, mux, asDave, "POST", "/join", map[string]string{"token": link.Token}).Code; got != 404 {
		t.Fatalf("a revoked link: %d", got)
	}

	// members list: the owner sees links, a member doesn't
	var ml map[string]any
	_ = json.Unmarshal(callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/members", id), nil).Body.Bytes(), &ml)
	if _, has := ml["links"]; has || len(ml["members"].([]any)) != 2 {
		t.Fatalf("bob's view of the members: %v", ml)
	}

	// leave, and removal by the owner
	if got := callAs(t, mux, asCarol, "DELETE", fmt.Sprintf("/runs/%d/members/bob", id), nil).Code; got != 403 {
		t.Fatalf("carol removes bob: %d", got)
	}
	if got := callAs(t, mux, asCarol, "DELETE", fmt.Sprintf("/runs/%d/members/carol", id), nil).Code; got != 200 || view(asCarol) != 404 {
		t.Fatalf("carol leaves: %d, then sees it: %d", got, view(asCarol))
	}
	callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d/members/bob", id), nil)
	if view(asBob) != 404 {
		t.Fatal("removed: gone")
	}
}
