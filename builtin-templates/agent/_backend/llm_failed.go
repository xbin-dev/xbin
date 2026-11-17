package main

// llm_failed.go — a model call that failed for good, and what a turn does
// about it: a failure worth another try (the provider's 5xx or 429, a call
// that never got an answer) is tried again by the turn itself, a bounded
// number of times and further apart each time, and said in the transcript;
// one only a person can lift stops the turn with what to do and whose it
// is to do.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// llmFailed is the error of an LLM's Chat when the provider — or xbin's
// connector in front of it — failed the call. Its text is the failure as
// it always read ("llm-gw 502 Bad Gateway: …").
type llmFailed struct {
	err error
	// Status is the HTTP status; 0 when there was none (the network, a
	// stream that stalled, a deadline).
	Status int
	// Transient: worth another try later (llmTransient).
	Transient bool
}

func (f *llmFailed) Error() string { return f.err.Error() }
func (f *llmFailed) Unwrap() error { return f.err }

// llmTransient reports whether a failure with this status and body may be
// gone a moment later: no status at all, or a 429/5xx — except the
// refusals of xbin's connector that only a person lifts (a quota used up,
// a connector that is off, unset or without its key).
func llmTransient(status int, body string) bool {
	if status != 0 && !retryableLLM(status) {
		return false
	}
	for _, code := range []string{"xbin_llm_quota", "xbin_llm_disabled", "xbin_llm_not_configured", "xbin_llm_upstream_key"} {
		if strings.Contains(body, code) {
			return false
		}
	}
	return true
}

// turnRetryWaits are the waits before a turn makes a failed model call
// again (each call has its own quick retries first: llmRetries): so many
// more tries, so far apart. A var: tests shorten it.
var turnRetryWaits = []time.Duration{10 * time.Second, 30 * time.Second, 90 * time.Second}

// turnRetryFor bounds the whole of it: no further try once the call has
// been failing for this long since its first failure (a provider that
// says nothing fails each call only after the idle watchdog's 90 s, four
// times over).
const turnRetryFor = 5 * time.Minute

// turnRetry: whether the turn tries the call again after err, its try'th
// failure (from 0) and failing for `failing` since the first, and how long
// it waits first.
func turnRetry(err error, try int, failing time.Duration) (time.Duration, bool) {
	var f *llmFailed
	if !errors.As(err, &f) || !f.Transient || try >= len(turnRetryWaits) || failing+turnRetryWaits[try] > turnRetryFor {
		return 0, false
	}
	return turnRetryWaits[try], true
}

// llmStopWords is what a turn that stops on err says: the failure, then —
// for a provider's — whether trying again helps, that nothing is lost, and
// who can fix it. tries is how many times the call was made.
func llmStopWords(err error, tries int) string {
	var f *llmFailed
	if !errors.As(err, &f) {
		return err.Error()
	}
	base := err.Error()
	const kept = "Nothing is lost — write again"
	switch {
	case f.Transient:
		return fmt.Sprintf("%s\nThe model's provider kept failing: %d tries, further apart each time. %s and the conversation goes on from here. If it keeps happening, an admin can test the provider on /xbin/llm.", base, tries, kept)
	case strings.Contains(base, "xbin_llm_quota"):
		return base + "\nThat is a quota on model use, not a fault: it lifts when it resets, or an admin can raise it on /xbin/llm. " + kept + " then."
	case strings.Contains(base, "xbin_llm_"):
		return base + "\nxbin's model connector refused the call — its words above say why. An admin changes what it allows on /xbin/llm. " + kept + " once that is done."
	case f.Status == http.StatusUnauthorized || f.Status == http.StatusForbidden:
		return base + "\nThe model's provider refused xbin's key or access, so trying again won't help: an admin fixes the provider's key on /xbin/llm. " + kept + " once that is done."
	case f.Status >= 400 && f.Status < 500:
		return base + "\nThe provider refused the request itself, so sending it again as it is won't help. " + kept + " to try another way; if it repeats, an admin can check the model on /xbin/llm."
	}
	return base
}

// waitCtx waits d; false when ctx ended first.
func waitCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
