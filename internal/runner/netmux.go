package runner

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// netMux is xbind's L3 backplane bookkeeping (plans/interfaces.md): it holds the
// per-client TUN fds a running net-provider tile handed back, keyed by
// provider→client, so a client spliced to that provider can find its link.
type netMux struct {
	mu    sync.Mutex
	links map[string]map[string]int // provider path → client path → provider-side TUN fd
}

func newNetMux() *netMux { return &netMux{links: map[string]map[string]int{}} }

// register records (and takes ownership of) a provider's client-link fd.
func (m *netMux) register(provider, client string, fd int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.links[provider] == nil {
		m.links[provider] = map[string]int{}
	}
	if old, ok := m.links[provider][client]; ok {
		unix.Close(old)
	}
	m.links[provider][client] = fd
}

// get peeks a provider's client-link fd (the splice keeps the fd open across
// client restarts; only clear/re-register closes it).
func (m *netMux) get(provider, client string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fd, ok := m.links[provider][client]
	return fd, ok
}

// clear drops and closes all of a provider's client-link fds (on its teardown).
func (m *netMux) clear(provider string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, fd := range m.links[provider] {
		unix.Close(fd)
	}
	delete(m.links, provider)
}

// ensureProvider makes sure a net-provider tile's primary is built and
// running (so its client-link TUNs are registered) before a client splices
// to it: a provider's roster is its primary's alone (09-fabric §5.8), and
// Ensure resolves the primary (F3).
func (r *Runner) ensureProvider(provider string) {
	c, ok := r.Reg.Component(provider)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _ = r.Ensure(ctx, c)
}

// netPlan is what a backend generation's network wiring takes from the
// spawn-time hooks, beside the egress policy (07-runtime §10.4).
type netPlan struct {
	clients  []sandbox.NetClient // a net provider's roster: one link per client, registered here
	provider string              // the net provider its egress is spliced to, when spliced
	spliced  bool
	links    []sandbox.NetLink              // its lan-ingress legs into providers
	fwd      map[int]string                 // its relay's gateway forwards (ingressFwd)
	dial     func(string) (net.Conn, error) // what the relay dials a forward with (hostDialFor)
}

// netPlanFor is the wiring of generation of deployment dep spawning from
// view c. The primary's is the hooks' answers, today's. A non-primary view
// never registers a roster, splices to a provider or takes lan-ingress legs,
// whatever a hook answers (D127o; 09-fabric §5.7–§5.8): L3 roles are the
// primary's, a second splicer on a provider's link would split its packets,
// and a roster entry would renumber the primary's links and restart the
// provider and its clients. Its relay keeps only stream-slot forwards, each
// dial checked as its deployment; log is its backend log.
func (r *Runner) netPlanFor(c *registry.Component, dep string, log io.Writer) netPlan {
	np := netPlan{fwd: r.ingressFwd(c), dial: r.hostDialFor(c.Path, dep, log)}
	if c.Deployment != "" {
		return np
	}
	if r.NetRoster != nil {
		np.clients = r.NetRoster(c)
	}
	if r.NetTarget != nil {
		np.provider, _, _, np.spliced = r.NetTarget(c)
	}
	if r.NetLinks != nil {
		np.links = r.NetLinks(c) // lan-ingress legs (plans/ingress.md)
	}
	return np
}

// The net verdict's reasons (09-fabric §5.8), written to the deployment's
// backend log.
const (
	whyHostShare = "host networking serves the tile's primary only; non-primary deployments get no egress"
	whySplice    = "net provider splices serve the tile's primary only; non-primary deployments get no egress"
)

// spawnEgress is the egress policy of a generation spawning from view c,
// with the net verdict applied at spawn (D127o; 09-fabric §5.8): the Egress
// hook's answer, except that a non-primary view gets no egress at all while
// the tile's net resolves to host sharing or is spliced through a provider
// tile, whatever the hook answers. Neither the host network nor a splice
// serves it, and a relay under the tile's grants in their place would be a
// way around both. why names the reason then. The primary's is the hook's,
// asked as today.
func (r *Runner) spawnEgress(c *registry.Component) (pol sandbox.EgressPolicy, why string) {
	if r.Egress != nil {
		pol = r.Egress(c)
	}
	if c.Deployment == "" {
		return pol, ""
	}
	if r.NetHost != nil && r.NetHost(c) {
		return sandbox.EgressPolicy{}, whyHostShare
	}
	if r.NetTarget != nil {
		if _, _, _, spliced := r.NetTarget(c); spliced {
			return sandbox.EgressPolicy{}, whySplice
		}
	}
	return pol, ""
}

// logVerdict writes the net verdict's reason, if any, to a backend log.
func logVerdict(w io.Writer, why string) {
	if why != "" {
		fmt.Fprintln(w, why)
	}
}
