package main

// epoch.go — reading the engine epoch, the fence every engine transaction
// passes (engine.go). A read of it that fails is an error, never an epoch: a
// failed read once scanned as 0, which fenced looked at as "another engine
// bumped the epoch" — the sole engine logged a takeover and stopped driving
// runs (seen in CI on TestHarnessQueuedPark). Now the transaction that read
// it is tried again, a few times with a short pause, and an error that
// stays is returned (and logged) with nothing written; only an epoch that
// was read and differs is a takeover.

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// epochReadTries bounds the tries of a transaction whose epoch read failed.
// The pauses between them grow (20, 40, 60, 80 ms): a read fails on a
// transient condition, or not at all.
const epochReadTries = 5

// epochReadError is a failed read of the engine epoch.
type epochReadError struct{ err error }

func (e *epochReadError) Error() string { return "the engine epoch can't be read: " + e.err.Error() }
func (e *epochReadError) Unwrap() error { return e.err }

// readEpoch is the engine epoch at key: 0 when no engine has taken over yet
// (no row). Any other failure is an epochReadError.
func readEpoch(q queryer, key string) (int64, error) {
	var ep int64
	err := q.QueryRow(`SELECT CAST(v AS INTEGER) FROM settings WHERE k=?`, key).Scan(&ep)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, &epochReadError{err}
	}
	return ep, nil
}

// epochIn reads e's epoch through q (readEpoch, or the tests' seam).
func (e *Engine) epochIn(q queryer) (int64, error) {
	if e.readEpoch != nil {
		return e.readEpoch(q, e.epochName())
	}
	return readEpoch(q, e.epochName())
}

// epochPause is the pause before try n+1 (tests shorten it).
var epochPause = func(n int) time.Duration { return time.Duration(n) * 20 * time.Millisecond }

// retryEpochRead runs op — which reads the epoch first, and writes nothing
// when that read fails — again while the read is what failed, up to
// epochReadTries times. Any other answer is op's.
func retryEpochRead(op func() error) error {
	var err error
	for n := 1; ; n++ {
		err = op()
		var re *epochReadError
		if !errors.As(err, &re) || n == epochReadTries {
			return err
		}
		time.Sleep(epochPause(n))
	}
}

// epochNow is the database's engine epoch for e, outside any transaction
// (the harness pipe's guard): read again on a failed read, as fenced does.
func (e *Engine) epochNow() (int64, error) {
	var ep int64
	err := retryEpochRead(func() error {
		var err error
		ep, err = e.epochIn(e.db.q)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("%w (tried %d times)", err, epochReadTries)
	}
	return ep, nil
}
