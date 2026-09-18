package main

import "testing"

// newTestAgent is an Agent with every map the loop writes to, so a test can
// drive real code paths without a nil-map panic standing in for the bug.
func newTestAgent(t *testing.T, db *DB) *Agent {
	t.Helper()
	return &Agent{db: db, driving: map[int64]bool{}, stop: map[int64]bool{},
		watcherRounds: map[int64]*watcherRound{}, drafts: map[int64]string{},
		repl: newReplRegistry(), gen: generationID(),
		kickCh: make(chan struct{}, 1), toolSem: make(chan struct{}, maxToolsGlobal),
		// Without this the admission path panics on a nil map write, which is
		// exactly why every drop point in dispatchRun shipped untested.
		cancels: map[int64]cancelReg{},
		blobs:   newMemBlobs(), blobCache: newBlobCache(8 << 20)}
}
