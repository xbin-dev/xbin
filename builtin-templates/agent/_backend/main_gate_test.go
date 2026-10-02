package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A request that arrives mid-boot waits for boot, then is served.
func TestGateReadyHoldsUntilBoot(t *testing.T) {
	ready := make(chan struct{})
	h := gateReady(ready, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
		done <- rec.Code
	}()
	select {
	case <-done:
		t.Fatal("served before boot finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(ready)
	if code := <-done; code != http.StatusTeapot {
		t.Fatalf("code %d after boot, want the handler's 418", code)
	}
}

// A caller that gives up before boot finishes gets a 503, not a hang.
func TestGateReadyGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	gateReady(make(chan struct{}), http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil).WithContext(ctx))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code %d, want 503", rec.Code)
	}
}
