package broker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// The broker's close waits for a doorbell ring in flight, and ends its
// delivery. closeMail once marked mail closed and returned: a ring already
// on its way outlived Broker.Close, and what it did next — count the inbox,
// which opens (and creates) the tile's mail.db; deliver, for up to the
// ring's timeout — happened in a workspace that was shut down, being booted
// again or being removed. Here a delivery is in flight when mail closes: it
// is cancelled, and the close returns only once the ring is over.
func TestCloseWaitsForMailRing(t *testing.T) {
	w := mailWS(t)
	b := w.b
	fakeMailDispatch(t, b, nil) // the short delays
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release) // (a ring nothing cancelled is not left waiting)
	var cancelled, over atomic.Bool
	ms := b.mail()
	ms.bellMu.Lock()
	ms.dispatch = func(ctx context.Context, _ auth.Principal, _, _ string, _ []byte) (int, string) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			cancelled.Store(true)
		case <-release:
		}
		time.Sleep(20 * time.Millisecond) // the close must wait for the ring, not only cancel it
		over.Store(true)
		return 499, ""
	}
	ms.bellMu.Unlock()

	mailSendOK(t, b, instanceOf("apps/pg", ""), "user:alice", "dm", 1)
	u, _ := b.Users.Get("alice")
	b.notePartitionStart("apps/pg", "main", "user:alice", w.pkeyOf("alice"), u.UID) // her start rings
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no doorbell was delivered")
	}
	b.closeMail() // Broker.Close's mail half
	if !cancelled.Load() {
		t.Error("the close did not end a delivery in flight")
	}
	if !over.Load() {
		t.Error("the close returned while a doorbell ring was still in flight")
	}
	ms.bellMu.Lock()
	n := len(ms.bells)
	ms.bellMu.Unlock()
	if n != 0 {
		t.Errorf("%d doorbells armed after the close", n)
	}
}
