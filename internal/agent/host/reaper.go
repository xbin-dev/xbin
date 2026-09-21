package host

import (
	"os"
	"os/signal"
	"sync"

	"golang.org/x/sys/unix"
)

// Status is how a child ended.
type Status struct {
	Code   int    // exit code (-1 when killed by a signal)
	Signal string // the signal's name when killed by one ("" otherwise)
}

// Reaper is the PID-1 duty: one wait4(-1) loop that hands every child's
// exit status to whoever claimed the pid. Children are Start()ed and never
// Wait()ed by their owners — a second waiter would race the loop and one of
// them would see ECHILD. An exit that lands before its claim is kept, so
// Claim never misses a status. Under isolation the host is PID 1 of the
// namespace; outside (dev, tests) it makes itself a sub-reaper so orphaned
// helpers still re-parent to it.
type Reaper struct {
	mu     sync.Mutex
	claims map[int]chan Status
	done   map[int]Status
}

var (
	reaperOnce sync.Once
	reaper     *Reaper
)

// StartReaper installs the SIGCHLD loop — once per process: wait4(-1) is
// process-wide, so two loops would each hold half the statuses.
func StartReaper() *Reaper {
	reaperOnce.Do(func() {
		reaper = &Reaper{claims: map[int]chan Status{}, done: map[int]Status{}}
		_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) // a no-op as PID 1
		c := make(chan os.Signal, 16)
		signal.Notify(c, unix.SIGCHLD)
		go func() {
			for range c {
				reaper.drain()
			}
		}()
	})
	return reaper
}

// drain reaps every exited child (the signal coalesces; each SIGCHLD may
// stand for several exits).
func (r *Reaper) drain() {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
		st := Status{Code: -1}
		if ws.Exited() {
			st.Code = ws.ExitStatus()
		} else if ws.Signaled() {
			st.Signal = unix.SignalName(ws.Signal())
		}
		r.mu.Lock()
		if ch := r.claims[pid]; ch != nil {
			delete(r.claims, pid)
			ch <- st
		} else {
			r.done[pid] = st // exited before anyone claimed it (or nobody will)
		}
		r.mu.Unlock()
	}
}

// Claim returns a channel that yields pid's exit status once (already
// buffered when the child is gone before the claim).
func (r *Reaper) Claim(pid int) <-chan Status {
	ch := make(chan Status, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.done[pid]; ok {
		delete(r.done, pid)
		ch <- st
		return ch
	}
	r.claims[pid] = ch
	return ch
}

// Forget drops a leftover pre-claim status (a pid nobody will claim).
func (r *Reaper) Forget(pid int) {
	r.mu.Lock()
	delete(r.done, pid)
	r.mu.Unlock()
}
