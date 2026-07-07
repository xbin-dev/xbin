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
