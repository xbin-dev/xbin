// The backend of the sealed fixture components (table_fixture.go): what it
// runs as, and a kv store of its own.
package main

import (
	"bytes"
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
	// as-person makes one call as the person this partition belongs to (a
	// delegate call: the component must hold delegation), with a ticket
	// when one is given; ask asks that person to approve a call.
	mux.HandleFunc("POST /as-person", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		body, _ := io.ReadAll(r.Body)
		req, err := http.NewRequestWithContext(r.Context(), q.Get("method"), "http://xbin"+q.Get("path"), bytes.NewReader(body))
		if err != nil {
			reply(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		if t := q.Get("ticket"); t != "" {
			xbin.WithTicket(req, t)
		}
		resp, err := xbin.AsPerson().Do(req)
		if err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		reply(w, 200, map[string]any{"status": resp.StatusCode, "body": string(b)})
	})
	mux.HandleFunc("POST /ask", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		body, _ := io.ReadAll(r.Body)
		a, err := xbin.RequestApproval(r.Context(), q.Get("method"), q.Get("path"), body, "the fixture asks")
		if err != nil {
			reply(w, 502, map[string]string{"error": err.Error()})
			return
		}
		reply(w, 200, a)
	})
	// env answers the fixed environment its table row gives it
	mux.HandleFunc("GET /env", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]string{"mode": os.Getenv("FIXTURE_MODE"), "component": os.Getenv("XBIN_COMPONENT")})
	})
	xbin.Serve(mux)
}
