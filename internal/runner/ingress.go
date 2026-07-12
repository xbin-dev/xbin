package runner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/magik6k/xbin/internal/registry"
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
// backend against the idle reaper until closed.
func (r *Runner) DialInto(ctx context.Context, comp, proto string, port int) (net.Conn, error) {
	c, ok := r.Reg.Component(comp)
	if !ok {
		return nil, fmt.Errorf("no such component: %s", comp)
	}
	if _, err := r.Ensure(ctx, c); err != nil {
		return nil, err
	}
	release := r.Track(comp)
	conn, err := r.dialCurrent(ctx, c, proto, port)
	if err != nil {
		release()
		return nil, err
	}
	return &releaseConn{Conn: conn, release: release}, nil
}

func (r *Runner) dialCurrent(ctx context.Context, c *registry.Component, proto string, port int) (net.Conn, error) {
	// Without per-component sandboxes (tier 1/2, `make dev`) — or when the
	// tile's net is bound to the host builtin — the backend listens on the
	// host itself: plain dial, no netns to reach into.
	if !r.Isolate || (r.NetHost != nil && r.NetHost(c)) {
		var d net.Dialer
		return d.DialContext(ctx, proto, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	s := r.state(c.Path)
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
