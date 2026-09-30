// llmslots.go — the tile-wide cap on model calls of a partitioned agent
// (API.md "Partitioned instances"). Each person's
// partition is its own process with its own gate (llmgate.go), so without a
// shared cap N people would multiply the upstream concurrency by N. The cap
// is a flock semaphore: `limit` lock files "llm.slot.<i>" in the directory of
// the `team` resource (shared read-write by every partition and the global
// instance). A model call try-locks any free slot — polling with backoff when
// none is — and unlocks it on return; a process that dies drops its slots
// with its fds. No sqlite write on the hot path. The limit is the config's
// maxActiveRuns (default 4), read at each call. Unpartitioned instances have
// only their gate, as before.
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// llmSlots is the semaphore.
type llmSlots struct {
	dir   string
	limit func() int
}

// slotPollMin/Max bound the wait between tries while every slot is taken.
var (
	slotPollMin = 20 * time.Millisecond
	slotPollMax = 400 * time.Millisecond
)

func newLLMSlots(dir string, limit func() int) *llmSlots { return &llmSlots{dir: dir, limit: limit} }

func (s *llmSlots) n() int { return clampCfg(s.limit(), defaultMaxActiveRuns, 32) }

// try takes a free slot without waiting (nil: none free, or the directory
// can't hold them).
func (s *llmSlots) try() func() {
	n := s.n()
	start := rand.IntN(n) // spread the processes over the files
	for k := range n {
		i := (start + k) % n
		f, err := os.OpenFile(filepath.Join(s.dir, fmt.Sprintf("llm.slot.%d", i)), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			continue
		}
		released := false
		return func() {
			if released {
				return
			}
			released = true
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		}
	}
	return nil
}

// errSlotsUnusable: the slot files can't be made (a missing or read-only
// directory). The call goes ahead without the tile-wide cap rather than fail.
var errSlotsUnusable = errors.New("llm slots: the team directory can't hold the lock files")

// acquire waits for a free slot (until ctx ends).
func (s *llmSlots) acquire(ctx context.Context) (func(), error) {
	if st, err := os.Stat(s.dir); err != nil || !st.IsDir() {
		return nil, errSlotsUnusable
	}
	wait := slotPollMin
	for {
		if rel := s.try(); rel != nil {
			return rel, nil
		}
		t := time.NewTimer(wait + time.Duration(rand.Int64N(int64(wait))))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, context.Cause(ctx)
		case <-t.C:
		}
		if wait *= 2; wait > slotPollMax {
			wait = slotPollMax
		}
	}
}

// slots is this process's semaphore (nil: legacy, or no team resource).
var slots *llmSlots

// acquireLLM takes what a model call needs: a place in this process's gate,
// then (partitioned) a tile-wide slot. release gives back both.
func (e *Engine) acquireLLM(ctx context.Context, top bool) (func(), error) {
	release, err := e.gate.acquire(ctx, top)
	if err != nil || slots == nil {
		return release, err
	}
	rel, err := slots.acquire(ctx)
	switch {
	case errors.Is(err, errSlotsUnusable):
		return release, nil
	case err != nil:
		release()
		return nil, err
	}
	return func() { rel(); release() }, nil
}

// tryBackgroundLLM is tryBackground (a title's call) with a tile-wide slot,
// never waiting for one.
func (e *Engine) tryBackgroundLLM() func() {
	release := e.gate.tryBackground()
	if release == nil || slots == nil {
		return release
	}
	rel := slots.try()
	if rel == nil {
		release()
		return nil
	}
	return func() { rel(); release() }
}

// userGateCap is a person's partition's own gate: at most this many of its
// model calls at once (global keeps the configured limit).
const userGateCap = 2

// gateLimit is this process's gate limit for cfg.
func gateLimit(cfg Config) int {
	n := cfg.maxActiveRuns()
	if userMode() && n > userGateCap {
		n = userGateCap
	}
	return n
}
