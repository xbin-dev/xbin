// The workspace agent's UI tests' note (native/ios/UITests/
// XbinWorkspaceAgentTests, D233; the tile of test/isolated/wsagent_test.go):
// it holds one note and says who touched it — the person and, when the call
// is their agent's, the agent (X-XBin-Agent).
package main

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func main() {
	var mu sync.Mutex
	note, by, reads, writes := "first", "nobody", 0, 0
	who := func(r *http.Request) string {
		c := xbin.Caller(r)
		return fmt.Sprintf("user=%s agent=%s role=%s", c.User, c.Agent, c.Role)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /note", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		fmt.Fprintf(w, "note=%s read by %s", note, who(r))
	})
	mux.HandleFunc("PUT /note", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		note, by = string(b), who(r)
		writes++
		fmt.Fprintf(w, "written by %s", by)
	})
	// what happened to the note: it counts nothing
	mux.HandleFunc("GET /seen", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "note=%s reads=%d writes=%d last write by %s", note, reads, writes, by)
	})
	xbin.Serve(mux)
}
