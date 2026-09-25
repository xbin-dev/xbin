package main

import (
	"fmt"
	"testing"
)

func drainTypes(s *subscriber) []string {
	var out []string
	for _, ev := range s.drain() {
		out = append(out, ev.Type)
	}
	return out
}

// A run-list event reaches only those who may see the run; a follower who
// loses access is told and cut off; a list subscriber is told to drop it.
func TestStreamFiltersByAccess(t *testing.T) {
	h := newEventHub("g")
	alice := who{kind: whoUser, user: "alice", level: "read"}
	bob := who{kind: whoUser, user: "bob", level: "read"}
	sa, _, _ := h.subscribe(0, h.now(), alice)
	sb, _, _ := h.subscribe(0, h.now(), bob)
	private := &rootACL{root: 7, owner: "alice", visibility: visPrivate, teamRole: roleViewer, members: map[string]string{}}
	h.publish(&Event{Type: evRun, Run: 7, Root: 7, Data: map[string]any{"id": 7}, acl: private})
	if got := drainTypes(sa); len(got) != 1 {
		t.Fatalf("alice gets her run: %v", got)
	}
	if got := drainTypes(sb); len(got) != 0 {
		t.Fatalf("bob must not see alice's private run: %v", got)
	}
	// an event with no ACL reaches only the system
	sys, _, _ := h.subscribe(0, h.now(), who{kind: whoSystem})
	h.publish(&Event{Type: evRun, Run: 8, Root: 8, Data: map[string]any{"id": 8}})
	if len(drainTypes(sa)) != 0 || len(drainTypes(sys)) != 1 {
		t.Fatal("an unaccounted run-list event goes to the system only")
	}

	// bob follows a team conversation; it turns private
	team := &rootACL{root: 9, owner: "alice", visibility: visTeam, teamRole: roleViewer, members: map[string]string{}}
	follow, _, _ := h.subscribe(9, h.now(), bob)
	h.publish(&Event{Type: evMessage, Run: 9, Root: 9, Data: map[string]any{"id": 1}})
	if got := drainTypes(follow); len(got) != 1 {
		t.Fatalf("bob follows the team conversation: %v", got)
	}
	team.visibility = visPrivate
	h.revalidate(9, team)
	if got := drainTypes(follow); fmt.Sprint(got) != "[revoked bye]" {
		t.Fatalf("the follower who lost access: %v", got)
	}
	if got := drainTypes(sb); fmt.Sprint(got) != "[revoked]" {
		t.Fatalf("bob's list drops it: %v", got)
	}
	if got := drainTypes(sa); len(got) != 0 {
		t.Fatalf("alice keeps it: %v", got)
	}
}

// Following a conversation you can't see is a 404, like reading it.
func TestStreamOfAPrivateRunIs404(t *testing.T) {
	ag, mux := accessFixture(t)
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	if got := callAs(t, mux, asBob, "GET", fmt.Sprintf("/stream?run=%d", id), nil).Code; got != 404 {
		t.Fatalf("bob follows alice's private run: %d", got)
	}
	if got := callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/stream", id), nil).Code; got != 404 {
		t.Fatalf("…by path: %d", got)
	}
}
