package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// serve is the ports capability's server in a sandbox (D135): /ws echoes
// WebSocket messages; any other path answers what reached it — the
// escaped path, the raw query, Host, and whether xbin's credentials or
// forwarding headers did (they must not) — and tries to set a cookie and
// an X-XBin-* header (which must not come back).
func serve(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fail(err.Error())
	}
	fmt.Println("listening", ln.Addr())
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			t, p, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(t, append([]byte("echo:"), p...)); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var leaked []string
		for k := range r.Header {
			ck := http.CanonicalHeaderKey(k)
			if strings.HasPrefix(ck, "X-Xbin-") || strings.HasPrefix(ck, "X-Forwarded-") ||
				ck == "Authorization" || ck == "Cookie" || ck == "Sbx-User" || ck == "Forwarded" {
				leaked = append(leaked, ck)
			}
		}
		w.Header().Set("Set-Cookie", "sbx=1; Path=/")
		w.Header().Set("X-XBin-User", "forged")
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "method=%s path=%s query=%s host=%s leaked=%s body=%d\n",
			r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Host, strings.Join(leaked, ","), r.ContentLength)
	})
	_ = http.Serve(ln, mux)
}
