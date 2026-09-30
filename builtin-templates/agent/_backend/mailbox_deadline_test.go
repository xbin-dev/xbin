package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// A hung xbind: the mailbox's reads and acks give up at the pull's deadline
// (the SDK's Context calls), so a doorbell can't hold mailMu past it.
func TestSDKMailGivesUpAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release) }) // before the gateway closes (cleanups run last-first)
	for name, call := range map[string]func(ctx context.Context) error{
		"Page": func(ctx context.Context) error { _, _, err := sdkMail{}.Page(ctx, "", mailPage); return err },
		"Ack":  func(ctx context.Context) error { return sdkMail{}.Ack(ctx, "0001") },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		start := time.Now()
		err := call(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
			t.Errorf("%s against a hung xbind: %v after %v", name, err, time.Since(start))
		}
	}
}
