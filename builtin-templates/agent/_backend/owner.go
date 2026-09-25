// owner.go — who drives runs, and keeping that process alive while it must.
//
//   - The engine lock: an exclusive flock on "<db>.engine", held for the
//     process lifetime. Sandboxes are namespaces on one kernel, and sqlite's
//     own locking across processes already depends on that, so the lock is
//     released the instant its holder exits or dies — the next owner, blocked
//     in flock, wakes then.
//   - The hold: xbind reaps a backend after 30 idle minutes, counting only
//     requests INTO it — an agent busy with its own outbound model calls looks
//     idle. While any run has work or a timer, the engine keeps one request to
//     itself open (GET /engine/hold); it is closed the moment work runs out.
//   - The resume job: a process that exits (xbind stopping, a disable) with
//     work still pending leaves a cron job that starts the backend again; the
//     next owner deletes it at takeover. It is the only cron the engine uses.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// acquireLock blocks until this process owns the engine lock. No lock path
// (an in-memory test database) owns immediately.
func (e *Engine) acquireLock() error {
	if e.lockPath == "" {
		return nil
	}
	f, err := os.OpenFile(e.lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return err
	}
	e.mu.Lock()
	if e.closing { // shut down while we waited: never take over
		e.mu.Unlock()
		f.Close()
		return fmt.Errorf("shutting down")
	}
	e.lockFile = f
	e.mu.Unlock()
	return nil
}

func (e *Engine) releaseLock() {
	e.mu.Lock()
	f := e.lockFile
	e.lockFile = nil
	e.mu.Unlock()
	if f != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

// generationID labels this process (event cursors, the lease marker).
func generationID() string {
	return fmt.Sprintf("%d-%d", time.Now().Unix(), time.Now().UnixNano()%1000000)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// --- the hold ----------------------------------------------------------------

// holder keeps one self-request open while wanted. open is injectable so
// tests can see the hold without a gateway; nil disables it.
type holder struct {
	mu     sync.Mutex
	open   func(ctx context.Context) error
	cancel context.CancelFunc
	done   bool
}

// updateHoldLocked (e.mu held) wants the hold while any actor runs or any
// timer is armed.
func (e *Engine) updateHoldLocked() {
	want := !e.closing && (len(e.actors) > 0 || len(e.timers) > 0)
	e.hold.set(want)
}

func (h *holder) set(want bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.open == nil || h.done {
		return
	}
	if want && h.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.loop(ctx)
	} else if !want && h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
}

// loop re-opens the hold if the gateway closes it early (a proxy restart),
// with a short backoff after a failure. It ends when the hold is released.
func (h *holder) loop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		_ = h.open(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (h *holder) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.done = true
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
}

// openSelfHold is the production hold: a request to our own component that
// the handler below keeps open.
func openSelfHold(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://xbin/api/"+xbin.Self()+"/engine/hold", nil)
	if err != nil {
		return err
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// handleHold keeps the request open until the caller lets go or the engine
// shuts down (so a draining process is never held up by it).
func handleHold(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	var closing <-chan struct{}
	if agent != nil && agent.eng != nil {
		closing = agent.eng.closingCh
	}
	select {
	case <-r.Context().Done():
	case <-closing:
	}
}

// --- the resume job ------------------------------------------------------------

// cronCallTimeout bounds a cron registration: the SDK client has no timeout
// of its own. A var so tests can shorten it.
var cronCallTimeout = 3 * time.Second

func (ag *Agent) cronPut(job map[string]any) bool {
	body, _ := json.Marshal(job)
	ctx, cancel := context.WithTimeout(context.Background(), cronCallTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://xbin/api/xbin/cron/jobs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (ag *Agent) cronDelete(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), cronCallTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, "http://xbin/api/xbin/cron/jobs/"+name, nil)
	if resp, err := xbin.Client().Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// registerResumeJob leaves a way back for a process exiting with work: the
// job lazy-starts the backend, whose takeover recovers everything and deletes
// the job. Only reached from Shutdown.
func (ag *Agent) registerResumeJob() {
	if ag.noGateway {
		return
	}
	ag.cronPut(map[string]any{
		"name": "resume", "resource": "res:" + xbin.Self() + "/beat",
		"schedule": "@every 1m", "path": "/tick", "role": "admin",
	})
}

// clearWakeJobs removes the resume job and the pre-engine "heartbeat" job:
// a running owner needs neither.
func (ag *Agent) clearWakeJobs() {
	if ag.noGateway {
		return
	}
	ag.cronDelete("resume")
	ag.cronDelete("heartbeat")
}
