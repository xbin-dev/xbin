// Command fakeiroh stands in for xbin-iroh, xbind's iroh sidecar, in tests
// and the Swift app-live tool: the same control protocol and stream
// handoff over plain loopback TCP (internal/apptunnel/sidecar/fakeiroh has
// the details). TEST ONLY.
//
//	fakeiroh serve --fd N          what xbind runs (XBIN_IROH=bin/fakeiroh)
//	fakeiroh client --ticket T …   the device side: local connections → streams
//	fakeiroh version
package main

import (
	"fmt"
	"os"

	"github.com/xbin-dev/xbin/internal/apptunnel/sidecar/fakeiroh"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		os.Exit(fakeiroh.Serve(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "client":
		os.Exit(fakeiroh.Client(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "version", "--version":
		fmt.Println("fakeiroh (xbin-iroh's test stand-in)")
	default:
		fmt.Fprintln(os.Stderr, "usage: fakeiroh serve --fd N | client --ticket T --alpn A --listen tcp:H:P|unix:PATH [--endpoint HEX] | version")
		os.Exit(2)
	}
}
