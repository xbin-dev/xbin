package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// The runner's half of the ingress plane (plans/ingress.md): reaching a port
// INSIDE a component's netns. The egress relay's userspace stack is attached
// to the component's TUN, so an inbound flow is just an outbound dial from
// that stack — no setns, no privilege, and the backend sees an ordinary
// connection from its gateway.

// dialRetry smooths the spawn race: a backend whose unix socket is healthy
// may open its stream port a beat later; a refused connect retries briefly.
const (
	dialRetries = 4
	dialBackoff = 250 * time.Millisecond
)

// DialInto connects to (proto, port) inside a component's network namespace,
// spawning the backend first if it is idle (an inbound connection wakes a
// tile exactly like an HTTP request does). The returned conn holds the
// backend against the idle reaper until closed. It reaches the tile's
// primary, resolved once: the one it ensures is the one it tracks and dials
// (F3; 09-fabric §1), so L4 streams, hairpin and stream interfaces never
// reach a non-primary deployment.
func (r *Runner) DialInto(ctx context.Context, comp, proto string, port int) (net.Conn, error) {
	c, ok := r.Reg.Component(comp)
	if !ok {
		return nil, fmt.Errorf("no such component: %s", comp)
	}
	dep := r.primary(comp)
	if _, err := r.ensurePrimary(ctx, c, dep); err != nil {
		return nil, err
	}
	s := r.stateOf(comp, dep)
	release := r.track(s)
	conn, err := r.dialCurrent(ctx, c, s, proto, port)
	if err != nil {
		release()
		return nil, err
	}
	return &releaseConn{Conn: conn, release: release}, nil
}

// dialCurrent dials (proto, port) in the netns of state s's current
// generation of c.
func (r *Runner) dialCurrent(ctx context.Context, c *registry.Component, s *state, proto string, port int) (net.Conn, error) {
	// Without per-component sandboxes (tier 1/2, `make dev`) — or when the
	// tile's net is bound to the host builtin — the backend listens on the
	// host itself: plain dial, no netns to reach into.
	if !r.Isolate || (r.NetHost != nil && r.NetHost(c)) {
		var d net.Dialer
		return d.DialContext(ctx, proto, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	var lastErr error
	for i := 0; i < dialRetries; i++ {
		if i > 0 {
			select {
			case <-time.After(dialBackoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s.mu.Lock()
		inst := s.cur
		s.mu.Unlock()
		if inst == nil {
			lastErr = fmt.Errorf("backend for %s is not running", c.Path)
			continue
		}
		if inst.relay == nil {
			if inst.provider != "" {
				return nil, fmt.Errorf("%s's net is spliced to provider %s — runtime ingress can't reach it (use the provider's lan-ingress, or rebind net)", c.Path, inst.provider)
			}
			return nil, fmt.Errorf("%s has no ingress network plumbing (restart it after binding)", c.Path)
		}
		conn, err := inst.relay.DialIn(ctx, proto, port)
		if err == nil {
			return conn, nil
		}
		lastErr = err // connect refused while the backend finishes booting → retry
	}
	return nil, lastErr
}

// releaseConn pairs a netns conn with its Track release so long-lived streams
// keep the backend alive and the release fires exactly once on close.
type releaseConn struct {
	net.Conn
	release func()
}

func (c *releaseConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}

// hostDial resolves the relay gateway-forward targets the broker wires up:
//
//	unix:<path>            — a host unix socket (a terminator's forward door)
//	stream:<comp>:<port>   — a sibling tile's exposed stream port (direct
//	                         tile→tile binding; xbind splices both netns's)
//	<host>:<port>          — plain TCP (the terminal-style xbind forward)
func (r *Runner) hostDial(dst string) (net.Conn, error) {
	switch {
	case strings.HasPrefix(dst, "unix:"):
		var d net.Dialer
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeoutHost)
		defer cancel()
		return d.DialContext(ctx, "unix", strings.TrimPrefix(dst, "unix:"))
	case strings.HasPrefix(dst, "stream:"):
		rest := strings.TrimPrefix(dst, "stream:")
		i := strings.LastIndexByte(rest, ':')
		if i < 0 {
			return nil, errors.New("bad stream target " + dst)
		}
		port, err := strconv.Atoi(rest[i+1:])
		if err != nil {
			return nil, errors.New("bad stream port in " + dst)
		}
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeoutHost)
		defer cancel()
		return r.DialInto(ctx, rest[:i], "tcp", port)
	default:
		var d net.Dialer
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeoutHost)
		defer cancel()
		return d.DialContext(ctx, "tcp", dst)
	}
}

const dialTimeoutHost = 15 * time.Second

// ingressFwd is the relay's gateway forwards for a generation spawning from
// view c (IngressFwd). A non-primary view keeps only its stream-slot
// forwards, whose every dial its edge refuses (hostDialFor): a terminator's
// forward door, the ingress path, is its primary's alone, and inbound edges
// reach only the primary (09-fabric §1, §5.7).
func (r *Runner) ingressFwd(c *registry.Component) map[int]string {
	if r.IngressFwd == nil {
		return nil
	}
	m := r.IngressFwd(c)
	if c.Deployment == "" && !c.UserPartition() {
		return m
	}
	var out map[int]string
	for port, dst := range m {
		if strings.HasPrefix(dst, "stream:") {
			if out == nil {
				out = map[int]string{}
			}
			out[port] = dst
		}
	}
	return out
}

// hostDialFor is hostDial for the relay of one generation of deployment dep
// of tile: a relay belongs to one generation, so each dial acts as its
// deployment, whose role is looked up at the dial (F2; 09-fabric §5.7). The
// primary's dials are hostDial's. Any other deployment's are refused at
// once, with one line in log, its backend log: a stream slot reaches into
// another tile's primary as raw L4 traffic the read clamp can't narrow, so
// v1 blocks it with no override (D127o), and a forward door is the primary's.
// A generation that stopped being the primary loses its forwards the same
// way until it restarts. A person's partition (part) is never the primary's
// generation: its dials are refused as a non-primary deployment's.
func (r *Runner) hostDialFor(tile, dep string, part bool, log io.Writer) func(dst string) (net.Conn, error) {
	return func(dst string) (net.Conn, error) {
		if !part && dep == r.primary(tile) {
			return r.hostDial(dst)
		}
		err := errors.New("the ingress forward door serves the tile's primary only")
		if t, ok := strings.CutPrefix(dst, "stream:"); ok {
			err = fmt.Errorf("stream slot to %s blocked by edge policy (a non-primary deployment reaches no stream slot)", t)
		}
		if log != nil {
			fmt.Fprintln(log, err)
		}
		return nil, err
	}
}
