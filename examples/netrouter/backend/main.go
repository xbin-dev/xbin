// A minimal net-provider (router) backend: enable IP forwarding between the
// per-client links buxond created (bxc0, bxc1, …) and our own egress (bx0), then
// serve a tiny status endpoint so the runner health-checks it. buxond does the
// L3 splice from each client into our bxcN; the kernel forwards to bx0; the
// terminal relay dials from the host. Add nftables/wireguard here for a
// firewall/VPN. See plans/interfaces.md.
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	buxon "github.com/magik6k/buxon/sdk"
)

func main() {
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "enable ip_forward:", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		fwd := strings.TrimSpace(readFile("/proc/sys/net/ipv4/ip_forward"))
		fmt.Fprintf(w, `{"forwarding":%q}`+"\n", fwd)
	})
	buxon.Serve(mux)
}

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }
