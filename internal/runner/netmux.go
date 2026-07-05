package runner

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sys/unix"
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

// ensureProvider makes sure a net-provider tile is built and running (so its
// client-link TUNs are registered) before a client splices to it.
func (r *Runner) ensureProvider(provider string) {
	c, ok := r.Reg.Component(provider)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _ = r.Ensure(ctx, c)
}
