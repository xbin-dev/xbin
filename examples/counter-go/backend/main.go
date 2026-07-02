// Counter backend — the "hello world" of buxon Go backends.
// Save this file and buxond rebuilds + swaps it in about a second.
package main

import (
	"fmt"
	"net/http"
	"sync/atomic"

	buxon "github.com/magik6k/buxon/sdk"
)

func main() {
	var count atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("GET /count", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"count":%d,"servedTo":%q}`+"\n", count.Load(), buxon.Caller(r).From)
	})
	mux.Handle("POST /count", buxon.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"count":%d}`+"\n", count.Add(1))
	}))

	buxon.Serve(mux)
}
