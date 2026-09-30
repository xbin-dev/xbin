package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// A hung xbind: the mailbox's reads and acks give up at the pull's deadline
// (the SDK's Context calls), so a doorbell can't hold mailMu past it. A call
// that ignores its ctx fails here by name (within), not at go test's timeout.
func TestSDKMailGivesUpAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release) }) // before the gateway closes (cleanups run last-first)
	for name, call := range map[string]func(ctx context.Context) error{
		"Page": func(ctx context.Context) error { _, _, err := sdkMail{}.Page(ctx, "", mailPage); return err },
		"Ack":  func(ctx context.Context) error { return sdkMail{}.Ack(ctx, "0001") },
	} {
		var err error
		within(t, "sdkMail."+name+" against a hung xbind", 5*time.Second, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err = call(ctx)
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s against a hung xbind: %v, want an error wrapping the deadline", name, err)
		}
	}
}
