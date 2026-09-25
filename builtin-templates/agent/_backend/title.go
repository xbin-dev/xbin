// title.go — naming a conversation (D83). A new chat is titled with the start
// of its first message; after its first answer a cheap model (the memory
// tier) names it in a few words — once, in the background, never ahead of a
// person's model call, and never over a name someone gave it.
package main

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

const titlePrompt = "Name this conversation in 3 to 7 words, in the language it is written in. " +
	"Reply with the title only — no quotes, no punctuation at the end."

// maybeTitle is called after a root's turn ends with an answer.
func (e *Engine) maybeTitle(runID int64) {
	e.titleMu.Lock()
	if e.titling[runID] {
		e.titleMu.Unlock()
		return
	}
	e.titling[runID] = true
	e.titleMu.Unlock()
	go func() {
		defer func() { e.titleMu.Lock(); delete(e.titling, runID); e.titleMu.Unlock() }()
		e.autoTitle(runID)
	}()
}

func (e *Engine) autoTitle(runID int64) {
	run, err := e.db.getRun(runID)
	if err != nil || run.ParentID != 0 || run.TitleSrc != "clip" {
		return
	}
	cfg, err := e.db.runConfig(runID)
	if err != nil || !cfg.feature("titles") {
		return
	}
	release := e.gate.tryBackground()
	if release == nil {
		return // busy: the next answer tries again
	}
	defer release()
	var first, answer string
	if msgs, err := e.db.messages(runID, false); err == nil {
		for _, m := range msgs {
			switch {
			case m.Role == "user" && first == "":
				first = m.Content
			case m.Role == "assistant" && answer == "" && strings.TrimSpace(m.Content) != "":
				answer = m.Content
			}
		}
	}
	if first == "" {
		return
	}
	ctx, cancel := context.WithTimeout(e.base, 60*time.Second)
	defer cancel()
	reply, err := e.llm.Chat(ctx, LLMRequest{
		Run: runID, Purpose: "title", Model: modelFor(ctx, cfg, "memory"), Wire: cfg.Wire,
		Msgs: []wireMsg{{Role: "system", Content: titlePrompt},
			{Role: "user", Content: "First message:\n" + clip(first, 1200) + "\n\nFirst answer:\n" + clip(answer, 1200)}},
	}, nil)
	if err != nil {
		return
	}
	title := cleanTitle(asString(reply.Msg.Content))
	if title == "" {
		return
	}
	_ = e.fenced(func(t *DB) error {
		// compare-and-swap: a rename in the meantime wins
		res, err := t.q.Exec(`UPDATE runs SET title=?, title_src='auto' WHERE id=? AND title_src='clip'`, title, runID)
		if err != nil || rowsAffected(res) != 1 {
			return err
		}
		t.addRunCost(runID, reply.Usage.PromptTokens, reply.Usage.CompletionTokens)
		e.emitRun(t, runID)
		return nil
	})
}

// cleanTitle keeps the first line, without quotes, markdown or a "Title:"
// lead-in; empty when the model answered with something that isn't a title.
func cleanTitle(s string) string {
	s = strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "Title:"), "title:"))
	s = strings.Trim(s, "\"'`*#_ “”‘’")
	s = strings.TrimRight(s, ".!")
	if s == "" || len(strings.Fields(s)) > 12 {
		return ""
	}
	if utf8.RuneCountInString(s) > 60 {
		r := []rune(s)
		s = string(r[:60])
	}
	return s
}
