package broker

import (
	"cmp"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// nsEnter is the namespace seams of a kv or blob request that reached
// scope's namespace in dep (08-data §8.2): 503 with Retry-After while an act
// holds it (nsAvailable), and for a writer the write gate held shared until
// release, so an act's copy or wipe never races an API write (nsWriting).
// ok false: the refusal is written. A namespace no act ever held passes at
// once, which is every namespace of a tile without deployments.
func (b *Broker) nsEnter(w http.ResponseWriter, scope, dep, want string) (release func(), ok bool) {
	return b.nsEnterID(w, nsOf(scope, dep), want)
}

// nsEnterID is nsEnter of namespace id: a user partition's as well.
func (b *Broker) nsEnterID(w http.ResponseWriter, id nsID, want string) (release func(), ok bool) {
	if !b.nsAvailableID(w, id) {
		return nil, false
	}
	if want != "writer" {
		return func() {}, true
	}
	return b.nsWritingID(w, id)
}

// nsAvailableID answers whether a data-plane request may reach namespace
// id; while held, it writes 503 with Retry-After (§8.2).
func (b *Broker) nsAvailableID(w http.ResponseWriter, id nsID) bool {
	act := b.busyAct(id)
	if act == "" {
		return true
	}
	w.Header().Set("Retry-After", nsRetryAfter)
	server.WriteError(w, http.StatusServiceUnavailable, nsBusy(id.dep, act).Error(), "/docs/protocol.md")
	return false
}

// nsWritingID holds id's write gate shared for one API write (kv, blob);
// past nsWriteWait it writes 503 with Retry-After and answers false.
func (b *Broker) nsWritingID(w http.ResponseWriter, id nsID) (release func(), ok bool) {
	g := b.nsTab().gate(id)
	for deadline := time.Now().Add(nsWriteWait); !g.TryRLock(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			w.Header().Set("Retry-After", nsRetryAfter)
			server.WriteError(w, http.StatusServiceUnavailable,
				cmp.Or(id.dep, util.MainDeployment)+"'s data is being copied; retry shortly", "/docs/protocol.md")
			return nil, false
		}
	}
	return g.RUnlock, true
}

// nsHeld is whether deployment dep of tile, in scope, may not start
// because an act holds its own namespace or left it partial (08-data §8.5):
// only once the tile has a deployment beyond main, so a zero-state or
// main-only start reads no namespace metadata.
func (b *Broker) nsHeld(tile, scope, dep string) bool {
	if _, names := b.deploymentsOf(tile); len(names) < 2 || scope == "" {
		return false
	}
	return b.nsStartBlocked(scope, dep) != nil
}
