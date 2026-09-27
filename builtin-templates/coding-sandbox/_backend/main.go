// coding-sandbox — the builtin sandbox manager (D115, D122): it serves the
// sandbox-manager contract (docs/sandbox-manager.md, protocol 1) to the tiles
// bound to it — the agent, sandbox-terminal, anyone — and runs their
// sandboxes on a Backend: xbind's own tile-sandbox runtime by default (VMs
// where they run), or anything else a copy adds (AGENTS.md). It is a
// TEMPLATE: each copy is a manager with its own images, sizes and quotas.
// See API.md.
//
// Layout: backend.go is the seam (Fleet + Box, the SDK's shapes);
// manager.go, sandboxes.go, commands.go and files.go are the contract layer
// (partitions, people, ids, versions, clientIds, egress words); images.go
// builds images; quotas.go bounds consumers and people; operator.go is the
// operators' own API; store.go the sqlite table.
package main

import (
	"log"
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	dbPath := xbin.Resource("db")
	if dbPath == "" {
		log.Fatal("no db resource (grant res:<self>/db writer) — see scope.json")
	}
	st, err := openStore(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	m, err := newManager(st, nil)
	if err != nil {
		log.Fatalf("manager: %v", err)
	}
	if m.beErr != nil { // it answers 503 unavailable, saying why, until an operator fixes the config
		log.Printf("backend %s: %v", m.cfg.backendName(), m.beErr)
	}
	m.resume()
	mux := http.NewServeMux()
	mux.Handle("/sbx/", consumerGuard(m.contractHandler()))
	m.operatorRoutes(mux)
	xbin.Serve(mux)
	m.Close()
}

// consumerGuard admits the contract's callers: a bound consumer (its
// binding grants the consumer role) and the tile itself (its own page acts
// as a consumer of its own).
func consumerGuard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := xbin.Caller(r)
		if c.From == "" || c.From == "xbin/cron" || (!xbin.RoleSatisfies(c.Role, "consumer") && !xbin.RoleSatisfies(c.Role, "admin")) {
			fail(w, http.StatusForbidden, "not-allowed", "the sandbox-manager routes need the consumer role: bind this tile's sandboxes provide (service sandbox-manager)")
			return
		}
		h.ServeHTTP(w, r)
	})
}
