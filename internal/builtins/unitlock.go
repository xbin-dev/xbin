package builtins

// unitlock.go — one update of a unit at a time, whichever its mode (D248
// re-review, P2a): safe, replace, merge, a proposal, a proposal's
// recording, a pin. Each decides from the record and the files and then
// writes them; two of one unit interleaved would write one's decision over
// the other's files, or record a base neither wrote. A caller waits at
// most unitWait (ErrBusy).

import (
	"sync"
	"time"
)

// unitLocks: the lock of each unit of each workspace (root and id).
var unitLocks sync.Map

// unitWait bounds how long an update waits for another of its unit.
var unitWait = 30 * time.Second

// lockUnit takes id's lock (an exact id): the unlock, or ErrBusy.
func (u *Updater) lockUnit(id string) (func(), error) {
	v, _ := unitLocks.LoadOrStore(u.root+"\x00"+id, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-time.After(unitWait):
		return nil, ErrBusy
	}
}
