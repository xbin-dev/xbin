// The backend of the sealed fixture components (table_fixture.go): what it
// runs as, and a kv store of its own.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /who", func(w http.ResponseWriter, r *http.Request) {
		exe, _ := os.Executable()
		wd, _ := os.Getwd()
		reply(w, 200, map[string]any{
			"self": xbin.Self(), "partition": os.Getenv("XBIN_PARTITION"), "user": xbin.PartitionUser(),
			"caller": xbin.Caller(r), "exe": exe, "cwd": wd, "code": "fixture-1",
		})
	})
	mux.HandleFunc("GET /kv/{key}", func(w http.ResponseWriter, r *http.Request) {
		b, err := xbin.KV(xbin.Resource("kv")).Get(r.PathValue("key"))
		switch {
		case errors.Is(err, xbin.ErrNotFound):
			reply(w, 404, map[string]string{"error": "not found"})
		case err != nil:
			reply(w, 502, map[string]string{"error": err.Error()})
		default:
			reply(w, 200, map[string]string{"value": string(b)})
		}
	})
	mux.HandleFunc("PUT /kv/{key}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := xbin.KV(xbin.Resource("kv")).Put(r.PathValue("key"), b); err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		reply(w, 200, map[string]string{"ok": "stored"})
	})
	// call asks xbind or another tile for a path, with this backend's own
	// credentials
	mux.HandleFunc("GET /call", func(w http.ResponseWriter, r *http.Request) {
		resp, err := xbin.Client().Get("http://xbin" + r.URL.Query().Get("path"))
		if err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		reply(w, 200, map[string]any{"status": resp.StatusCode, "body": string(b)})
	})
	xbin.Serve(mux)
}
