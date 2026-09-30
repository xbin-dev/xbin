package main

// llm_gw_fairness_test.go — the model client against llm-gw's optional
// fairness limit (builtin-tiles/llm-gw API.md "Partitioned callers"): a
// call the limit holds waits without response headers for at most the
// gateway's limitWait (20 s), then is answered 429 with a Retry-After. The
// stream watchdog (llmIdleTimeout, 90 s) is armed before the headers, so
// that wait must stay under it; the 429 is retried like any other.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayFairnessLimitHeldThenRetried(t *testing.T) {
	fastWires(t)
	old := llmIdleTimeout
	llmIdleTimeout = 450 * time.Millisecond // 90 s, scaled
	t.Cleanup(func() { llmIdleTimeout = old })
	held := 100 * time.Millisecond // llm-gw's 20 s wait, scaled the same
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 { // held at the limit, then turned away
			time.Sleep(held)
			w.Header().Set("Retry-After", "2")
			http.Error(w, `{"error":"this partition has as many calls in flight as the gateway's fairness limit allows (1): try again shortly"}`, http.StatusTooManyRequests)
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"at last"}}]}`, `[DONE]`)
	}))
	defer srv.Close()
	rep, err := testGW(srv).Chat(context.Background(), LLMRequest{Model: "m", Stream: true, Wire: "chat"}, nil)
	if err != nil || asString(rep.Msg.Content) != "at last" || n.Load() != 3 {
		t.Fatalf("held twice, then served: %v %q after %d attempts", err, asString(rep.Msg.Content), n.Load())
	}

	// held past every retry: the turn fails naming the limit, not a stall
	n.Store(-100)
	_, err = testGW(srv).Chat(context.Background(), LLMRequest{Model: "m", Stream: true, Wire: "chat"}, nil)
	if err == nil || errors.Is(err, errLLMIdle) || !strings.Contains(err.Error(), "fairness limit") {
		t.Fatalf("always held: %v", err)
	}
}
