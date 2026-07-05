// Counter backend — the "hello world" of xbin Go backends.
// Save this file and xbind rebuilds + swaps it in about a second.
package main

import (
	"fmt"
	"net/http"
	"sync/atomic"

	xbin "github.com/magik6k/xbin/sdk"
)

func main() {
	var count atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("GET /count", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"count":%d,"servedTo":%q}`+"\n", count.Load(), xbin.Caller(r).From)
	})
	mux.Handle("POST /count", xbin.RoleFunc("writer", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"count":%d}`+"\n", count.Add(1))
	}))

	xbin.Serve(mux)
}
