package broker

import "net/http"

// nsEnter is the namespace seams of a kv or blob request that reached
// scope's namespace in dep (08-data §8.2): 503 with Retry-After while an act
// holds it (nsAvailable), and for a writer the write gate held shared until
// release, so an act's copy or wipe never races an API write (nsWriting).
// ok false: the refusal is written. A namespace no act ever held passes at
// once, which is every namespace of a tile without deployments.
func (b *Broker) nsEnter(w http.ResponseWriter, scope, dep, want string) (release func(), ok bool) {
	if !b.nsAvailable(w, scope, dep) {
		return nil, false
	}
	if want != "writer" {
		return func() {}, true
	}
	return b.nsWriting(w, scope, dep)
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
