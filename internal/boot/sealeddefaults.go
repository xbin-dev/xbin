package boot

// sealeddefaults.go — a sealed component's default bindings (D227): a slot
// its table row binds to a builtin provider while no admin has bound it and
// the provider is available. The registry applies them when the workspace
// manifest is read (registry.sealedView); boot says which providers are
// available, and restarts a component when that changes under it — a
// backend's bindings are its environment, read at start.

import (
	"sort"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/util"
)

// sealedDefaults remembers, per sealed component, which of its defaults
// applied when it was last admitted to start.
type sealedDefaults struct {
	mu   sync.Mutex
	seen map[string]string
}

// wireSealedDefaults tells the registry which builtin providers a default
// may bind to now: the LLM connector once it is configured. Called once the
// connector exists; before that no default applies.
func (st *State) wireSealedDefaults() {
	brk := st.Broker
	st.Reg.SealedProvider = func(ref string) bool { // under the registry's lock: nothing here reads the registry
		return ref == broker.LLMRef && brk.LLMOffered != nil && brk.LLMOffered()
	}
}

// sealedAdmit is the sealed set's gate of a start of comp (sealed.Admit),
// and where a change in what its defaults bind is noticed: a component
// that started before the LLM connector was configured, or outlived its
// removal, restarts onto the bindings it has now.
func (st *State) sealedAdmit(comp string) string {
	why := st.Sealed.Admit(comp)
	if why != "" || !util.SealedPath(comp) {
		return why
	}
	sc, ok := st.Sealed.Component(comp)
	if !ok || len(sc.Defaults) == 0 {
		return ""
	}
	var parts []string
	for slot := range sc.Defaults {
		if _, applied := st.Reg.SealedDefault(comp, slot); applied {
			parts = append(parts, slot)
		}
	}
	sort.Strings(parts)
	now := strings.Join(parts, ",")
	d := &st.sealedDefaults
	d.mu.Lock()
	prev, had := d.seen[comp]
	if d.seen == nil {
		d.seen = map[string]string{}
	}
	d.seen[comp] = now
	d.mu.Unlock()
	if had && prev != now {
		if c, ok := st.Reg.Component(comp); ok {
			go st.Run.ChangedTile(c) // not under the caller's locks
		}
	}
	return ""
}
