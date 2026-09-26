// agent backend — a durable, debuggable agentic loop persisted in
// in-component sqlite and driven against the llm-gw component, with tools,
// subagents, MCP, compaction and self-scheduling. This is a TEMPLATE:
// instantiate it and build it up. See API.md.
//
// Layout: engine.go/actor.go move runs (no tickers — events and one-shot
// timers only), inbox.go takes their inputs, links.go/subagent_tools.go are
// delegation, stream.go is the tile's live view, llm*.go talk to models,
// tools.go and friends are what the model can do.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Agent holds what tools need (the store, the REPL, the blob store) and the
// engine that drives runs.
type Agent struct {
	db  *DB
	eng *Engine

	// repl holds the live goja VMs, one per run — a pure cache rebuilt from
	// its replay log on next use (repl.go).
	repl *replRegistry
	// toolSem bounds tool goroutines across ALL runs, not just one batch.
	toolSem chan struct{}
	// blobs holds the bytes of binary session files; blobCache keeps recent
	// ones, since every step re-assembles the context.
	blobs     blobStore
	blobCache *blobCache
	// noGateway is set in tests: no cron, bus or hold calls.
	noGateway bool
	// acl caches who may see which conversation (acl.go).
	acl aclCache
	// needs pushes "Needs you" to people's phones (needs_push.go); nil: off.
	needs *needsPusher
}

var agent *Agent

func main() {
	dbPath := xbin.Resource("db")
	if dbPath == "" {
		log.Fatal("no db resource (grant res:<self>/db writer) — see scope.json")
	}
	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	agent = &Agent{db: db, repl: newReplRegistry(), toolSem: make(chan struct{}, maxToolsGlobal),
		blobs: gatewayBlobs{}, blobCache: newBlobCache(48 << 20), needs: newNeedsPusher(xbin.NotifyUserWith)}
	if db.getSetting("config") == "" {
		b, _ := json.Marshal(defaultConfig())
		_ = db.putSetting("config", string(b))
	}
	eng := newEngine(db, agent, newGatewayLLM(), dbPath+".engine")
	eng.hold.open = openSelfHold
	eng.Start()
	go agent.reRegisterSchedules()
	go agent.reRegisterTriggers()

	// SIGTERM (a save's blue/green swap, a stop, an idle reap): stop driving
	// at once so the successor — already booted and waiting on the engine
	// lock — takes over the moment this process exits.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	go func() {
		<-sig
		eng.BeginShutdown()
	}()

	mux := http.NewServeMux()
	routes(mux)
	adapterRoutes(mux) // chat adapters: the channel role (channels.go)

	xbin.Serve(mux)
	// Serve returns at SIGTERM without waiting for anything; the engine gets
	// a bounded moment to unwind and hand over.
	eng.Shutdown(2 * time.Second)
}
