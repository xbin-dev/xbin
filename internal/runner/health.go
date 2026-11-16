package runner

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// errNeverListened is a backend that was still running at the health
// deadline but had never opened its socket: it hadn't called Serve. The
// text keeps today's "timeout dialing backend socket" first, as logs and
// people grep for it.
var errNeverListened = errors.New("timeout dialing backend socket: the backend is running but never started listening")

// xbindCallGrace is the most time a starting generation's calls to xbind's
// own API add to its health deadline (docs/sdk.md → Startup); a var for the
// tests.
var xbindCallGrace = 60 * time.Second

// waitHealthy dials the backend's socket until it answers, the process exits
// (exited closes: a VM that dies at boot fails at once, not after the whole
// health timeout) or the timeout passes. Time the generation spends waiting
// on xbind's answer to a call of its own (waiting reports one in flight;
// nil: never) doesn't count, up to xbindCallGrace more: a backend that
// registers its cron jobs or reads its config before Serve isn't failed
// because xbind was slow to answer it.
func waitHealthy(sock string, exited <-chan struct{}, timeout time.Duration, waiting func() bool) error {
	last := time.Now()
	deadline := last.Add(timeout)
	limit := deadline.Add(xbindCallGrace)
	was := false // a call was in flight at the last look: the time since is xbind's
	for {
		now := time.Now()
		busy := waiting != nil && waiting()
		if busy || was {
			if deadline = deadline.Add(now.Sub(last)); deadline.After(limit) {
				deadline = limit
			}
		}
		last, was = now, busy
		if !now.Before(deadline) {
			break
		}
		conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-exited:
			return errExited
		case <-time.After(50 * time.Millisecond):
		}
	}
	if _, err := os.Stat(sock); errors.Is(err, os.ErrNotExist) {
		return errNeverListened
	}
	return errors.New("timeout dialing backend socket: the backend listens but never accepted a connection")
}

// waitingOnXbind reports whether inst has a call to xbind's own API in
// flight; nil without auth (no instance tokens).
func (r *Runner) waitingOnXbind(inst *instance) func() bool {
	if r.Auth == nil {
		return nil
	}
	return func() bool { return len(r.Auth.InstanceCalls(inst.token)) > 0 }
}

// unhealthy is the error of generation inst of view v that didn't become
// healthy: for one that runs but never listened, what it was doing — the
// calls to xbind it is still waiting on, whether it printed anything — and
// where the startup rules are, also written into its log so `bx logs` says
// it after the start marker. Called before the generation is stopped, while
// its calls are still in flight. Never the log's content: the error reaches
// every build-error subscriber, and output can carry secrets.
func (r *Runner) unhealthy(v *registry.Component, inst *instance, err error) error {
	if !errors.Is(err, errNeverListened) {
		return fmt.Errorf("backend did not become healthy: %w", err)
	}
	what := "it may be blocked in startup"
	var calls []string
	if r.Auth != nil {
		for _, c := range r.Auth.InstanceCalls(inst.token) {
			calls = append(calls, fmt.Sprintf("%s (for %s)", c.Route, time.Since(c.Since).Round(100*time.Millisecond)))
		}
	}
	switch {
	case len(calls) > 0:
		what = "it is waiting on xbind: " + strings.Join(calls, ", ")
	case inst.out.path != "" && logSize(inst.out.path) <= inst.out.at:
		what += ", and has printed nothing since it started"
	case inst.out.path != "":
		logs := "bx logs " + v.Path
		if dep := r.viewDeployment(v); dep != util.MainDeployment {
			logs += " --deployment " + dep
		}
		what += " (its last output is in its log: " + logs + ")"
	}
	err = fmt.Errorf("%w within %s (it has not called Serve): %s; if it calls xbind or another service before Serve, see /docs/sdk.md#startup",
		err, time.Since(inst.started).Round(100*time.Millisecond), what)
	if inst.out.path != "" {
		if f, ferr := os.OpenFile(inst.out.path, os.O_APPEND|os.O_WRONLY, 0o644); ferr == nil {
			fmt.Fprintf(f, "--- gen %d stopped: %v ---\n", inst.gen, err)
			f.Close()
		}
	}
	return fmt.Errorf("backend did not become healthy: %w", err)
}

// logRef is a generation's log: its path, and its size once xbind wrote the
// start marker (and the network verdict), before the process started.
type logRef struct {
	path string
	at   int64
}

// logSize is the size of the log at path; -1 when it can't be read.
func logSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}
