package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A provider that fails for a moment doesn't stop a conversation. A model
// call that failed with something worth another try (502/503/504, 429, a
// call that never got an answer) is made again by the turn itself — a
// bounded number of times, further apart each time, and said in the
// transcript; the turn then goes on as if nothing happened. Before, the
// conversation stood in "error" until its person wrote again — and a wake
// takes no conversation in error, so a tile agent that finished meanwhile
// was never reported (the real-model run, 2026-11-17: a gateway's 502s).
// A failure only a person can lift is not tried again: the turn stops with
// what to do and who can.
func TestWsEvalProviderFailureRetries(t *testing.T) {
	waits := turnRetryWaits
	turnRetryWaits = []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
	t.Cleanup(func() { turnRetryWaits = waits })
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	notes := func() string {
		return strings.Join(dbStrings(t, fx.ag.db, `SELECT detail FROM steps WHERE run_id=? AND kind='note' ORDER BY seq`, s.Run), "\n")
	}
	bad := func(status int, body string) error {
		return &llmFailed{err: fmt.Errorf("llm-gw %d: %s", status, body), Status: status, Transient: llmTransient(status, body)}
	}

	// two 502s, then the answer: the turn retried by itself and said so
	n := 0
	f.on(forRun(s.Run, lastUser("how many tiles")), func(LLMRequest) (LLMReply, error) {
		if n++; n <= 2 {
			return LLMReply{}, bad(502, `{"error":{"message":"the upstream main answered 502 Bad Gateway","code":"xbin_llm_upstream"}}`)
		}
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: "Six tiles."}}, nil
	})
	fx.say(t, s, "how many tiles do I have?")
	fx.idle(t, s)
	if got := fx.lastAssistant(t, s); got != "Six tiles." || n != 3 || statusOf(fx.ag.db, s.Run) == statusError {
		t.Fatalf("after two 502s: answer %q after %d calls, status %s", got, n, statusOf(fx.ag.db, s.Run))
	}
	if got := notes(); strings.Count(got, "trying again in") != 2 || !strings.Contains(got, "502") || !strings.Contains(got, "try 2 of 4") || !strings.Contains(got, "try 3 of 4") {
		t.Fatalf("the transcript's notes of the retries:\n%s", got)
	}

	// a provider that stays down: a bounded number of tries, then the turn stops — saying what was tried,
	// that nothing is lost, and who can look
	n = 0
	f.on(forRun(s.Run, lastUser("and how many orgs")), func(LLMRequest) (LLMReply, error) {
		n++
		return LLMReply{}, bad(503, "upstream connect error")
	})
	fx.say(t, s, "and how many orgs?")
	waitFor(t, "the turn stops", func() bool { return statusOf(fx.ag.db, s.Run) == statusError })
	r, _ := fx.ag.db.getRun(s.Run)
	if n != len(turnRetryWaits)+1 {
		t.Fatalf("a provider that stays down was called %d times, want %d", n, len(turnRetryWaits)+1)
	}
	for _, want := range []string{"503", "4 tries", "Nothing is lost", "write again", "an admin", "/xbin/llm"} {
		if !strings.Contains(r.Result, want) {
			t.Errorf("the stop after a provider stayed down lacks %q: %s", want, r.Result)
		}
	}

	// failures only a person can lift: one call, no retry, and whose it is to fix
	for _, c := range []struct {
		ask    string
		err    error
		wants  []string
		calls  int
		status string
	}{
		{"a rejected key", bad(401, `{"error":{"message":"Incorrect API key provided"}}`), []string{"401", "key", "an admin", "/xbin/llm"}, 1, statusError},
		{"a quota", bad(429, `{"error":{"message":"your LLM quota for today is used up: it resets at 00:00 UTC","code":"xbin_llm_quota_exceeded"}}`), []string{"quota", "resets at 00:00 UTC", "an admin"}, 1, statusError},
		{"a refused request", bad(400, `{"error":{"message":"maximum context length is 8192 tokens"}}`), []string{"400", "maximum context length", "won't help"}, 1, statusError},
		{"an error that is no provider's", errors.New("the transcript cannot be sent"), []string{"the transcript cannot be sent"}, 1, statusError},
	} {
		n = 0
		f.on(forRun(s.Run, lastUser(c.ask)), func(LLMRequest) (LLMReply, error) { n++; return LLMReply{}, c.err }).once()
		before := strings.Count(notes(), "trying again in")
		fx.say(t, s, c.ask)
		waitFor(t, c.ask+": the turn stops", func() bool {
			r, _ := fx.ag.db.getRun(s.Run)
			return r.Status == statusError && strings.Contains(r.Result, c.wants[0])
		})
		r, _ := fx.ag.db.getRun(s.Run)
		for _, want := range c.wants {
			if !strings.Contains(r.Result, want) {
				t.Errorf("%s: the stop lacks %q: %s", c.ask, want, r.Result)
			}
		}
		if n != c.calls || strings.Count(notes(), "trying again in") != before {
			t.Errorf("%s: %d model call(s), %d retry notes — want one call and no retry", c.ask, n, strings.Count(notes(), "trying again in")-before)
		}
	}
}
