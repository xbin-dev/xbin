// settle.go — copies the substrate makes off the request. xbind's runtime
// answers a snapshot that is still being copied as pending (202), a clone
// whose copy runs as `creating`, and a restore still copying as the
// sandbox with a "busy: …" stateDetail, once its limits.waitMaxSec ran
// out (docs/protocol.md §Tile sandboxes, Snapshots and clones). The
// contract answers each when it is done, so the manager waits it out here,
// polling, for as long as the caller's context lasts.
package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// settlePoll is how often a copy's end is asked after (tests shorten it).
var settlePoll = 500 * time.Millisecond

// pause waits settlePoll, or until ctx ends.
func pause(ctx context.Context) error {
	t := time.NewTimer(settlePoll)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// settleSnapshot waits until snapshot s of box is taken: s itself when it
// isn't pending. One that leaves the list meanwhile wasn't taken.
func settleSnapshot(ctx context.Context, box Box, s *xbin.Snapshot) (*xbin.Snapshot, error) {
	for s.Pending {
		if err := pause(ctx); err != nil {
			return nil, err
		}
		list, err := box.Snapshots(ctx)
		if err != nil {
			return nil, err
		}
		var now *xbin.Snapshot
		for i := range list {
			if list[i].ID == s.ID {
				now = &list[i]
			}
		}
		if now == nil {
			return nil, errf(http.StatusServiceUnavailable, "unavailable", "the snapshot %s wasn't taken: the substrate's copy failed", s.ID)
		}
		s = now
	}
	return s, nil
}

// settleCreated waits until the substrate's sandbox name, answered
// `creating` (a clone copying), is made: stopped or running — or error,
// when its copy failed.
func settleCreated(ctx context.Context, be Backend, in *xbin.SandboxInfo) (*xbin.SandboxInfo, error) {
	for in.State == "creating" {
		if err := pause(ctx); err != nil {
			return nil, err
		}
		next, err := be.Get(ctx, in.Name)
		if err != nil {
			return nil, err
		}
		in = next
	}
	if in.State == "error" {
		return nil, errf(http.StatusServiceUnavailable, "unavailable", "the copy it starts from failed: %s", in.StateDetail)
	}
	return in, nil
}

// settleBusy waits until the substrate's sandbox in is no longer busy with
// a copy (a restore: stateDetail "busy: …").
func settleBusy(ctx context.Context, be Backend, in *xbin.SandboxInfo) (*xbin.SandboxInfo, error) {
	for strings.HasPrefix(in.StateDetail, "busy:") {
		if err := pause(ctx); err != nil {
			return nil, err
		}
		next, err := be.Get(ctx, in.Name)
		if err != nil {
			return nil, err
		}
		in = next
	}
	return in, nil
}
