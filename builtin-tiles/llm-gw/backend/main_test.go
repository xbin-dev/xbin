package main

import (
	"context"
	"testing"
)

func TestCostOf(t *testing.T) {
	c := gwConfig{Pricing: map[string]price{"gpt-4o": {In: 2.5, Out: 10}}}
	// 1M in @ $2.5 + 0.5M out @ $10 = 2.5 + 5.0.
	if got, want := costOf(c, "gpt-4o", 1_000_000, 500_000), 7.5; got != want {
		t.Fatalf("cost: got %v want %v", got, want)
	}
	if costOf(c, "unpriced", 1000, 1000) != 0 {
		t.Fatal("an unpriced model must cost 0, not error")
	}
}

func TestRetryableStatus(t *testing.T) {
	for _, code := range []int{429, 500, 502, 503, 504} {
		if !retryableStatus(code) {
			t.Fatalf("%d should be retryable", code)
		}
	}
	for _, code := range []int{200, 400, 401, 404} {
		if retryableStatus(code) {
			t.Fatalf("%d should not be retryable", code)
		}
	}
}

func TestSleepBackoff(t *testing.T) {
	// A canceled context returns immediately without sleeping.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepBackoff(ctx, 5, "") {
		t.Fatal("canceled ctx should return false")
	}
	// A zero Retry-After completes.
	if !sleepBackoff(context.Background(), 0, "0") {
		t.Fatal("zero-delay backoff should complete")
	}
}

func TestUsageOf(t *testing.T) {
	cases := []struct {
		body    string
		in, out int64
	}{
		{`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":34,"total_tokens":46}}`, 12, 34},
		// a chat stream: usage rides the last chunk
		{"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":6}}\n\ndata: [DONE]\n\n", 5, 6},
		// the Responses API, streamed: usage only on response.completed
		{"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"usage\":null}}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens\":40,\"output_tokens_details\":{\"reasoning_tokens\":30},\"total_tokens\":140}}}\n\n", 100, 40},
		// the Responses API, not streamed
		{`{"status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":8,"total_tokens":15}}`, 7, 8},
		{`{"data":[{"id":"gpt-5"}]}`, 0, 0},
	}
	for i, c := range cases {
		if in, out := usageOf([]byte(c.body)); in != c.in || out != c.out {
			t.Errorf("case %d: got %d/%d, want %d/%d", i, in, out, c.in, c.out)
		}
	}
}
