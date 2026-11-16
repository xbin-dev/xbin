// An MCP server tile (API.md, docs/mcp.md): tools an agent calls. The wire is
// mcp.go, the tools are tools.go's list — adding one is a function (dice.go
// is the example) and a line there. Nothing else to wire: the manifest's
// "provides": {"mcp": …} is what an agent's `mcp` slot binds.
package main

import (
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	xbin.Serve(routes())
}

// routes is every route this backend serves.
func routes() http.Handler {
	mux := http.NewServeMux()
	// MCP over streamable HTTP: one JSON-RPC message (or a batch) per POST,
	// answered as JSON. This server opens no event stream and keeps no
	// sessions, so GET and DELETE on /mcp are 405 (the transport allows it).
	mux.HandleFunc("POST /mcp", serveMCP)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		xbin.WriteError(w, http.StatusMethodNotAllowed, "POST a JSON-RPC message to /mcp (this server opens no event stream and keeps no sessions)")
	})
	return mux
}
