package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Once someone other than the owner speaks, every person's message carries
// their id and the system prompt says the conversation is shared; before
// that, nothing changes (a lone owner, or a legacy run with one sender).
func TestSendersArePrefixedWhenShared(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: defaultConfig(), Hold: true,
		Stamp: runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer}})
	if err != nil {
		t.Fatal(err)
	}
	say := func(who, text string) {
		meta, _ := json.Marshal(msgMeta{Sender: who})
		if _, err := db.addMessage(&Message{RunID: r.ID, Role: "user", Content: text, Meta: meta}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := func() []wireMsg {
		run, _ := db.getRun(r.ID)
		msgs, err := ag.assembleContext(t.Context(), run, defaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		return msgs
	}
	say("alice", "just me")
	msgs := ctx()
	if strings.Contains(msgs[0].Content.(string), "shared") || msgs[len(msgs)-1].Content != "just me" {
		t.Fatalf("alone: %+v", msgs)
	}
	say("bob", "hi from bob")
	msgs = ctx()
	if !strings.Contains(msgs[0].Content.(string), "This conversation is shared") {
		t.Fatal("the system prompt says it is shared")
	}
	var got []string
	for _, m := range msgs[1:] {
		got = append(got, m.Content.(string))
	}
	if strings.Join(got, "|") != "[alice] just me|[bob] hi from bob" {
		t.Fatalf("messages: %q", got)
	}
}
