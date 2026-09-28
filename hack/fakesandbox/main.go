// fakesandbox is a sandbox manager for tests and the UI harness: the
// sandbox-manager contract, protocol 1 (docs/sandbox-manager.md), with every
// sandbox a directory on the host, every command a host process and every
// terminal a host pseudo-terminal — TEST ONLY, nothing is isolated. Its
// tests run the contract's conformance suite (sdk/sandboxcontract).
//
// As a tile (the harness's apps/fakesbx) it serves XBIN_SOCKET and keeps its
// sandboxes in XBIN_RES_BOXES (or $XBIN_DATA/boxes); standalone:
//
//	go run ./hack/fakesandbox -addr 127.0.0.1:18978 -root /tmp/fsb
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	addr := flag.String("addr", "", "listen on this TCP address (default: $XBIN_SOCKET)")
	root := flag.String("root", "", "keep sandboxes here (default: $XBIN_RES_BOXES, else $XBIN_DATA/boxes)")
	from := flag.String("from", "", "the consumer of calls without X-XBin-From")
	flag.Parse()
	if *root == "" {
		*root = os.Getenv("XBIN_RES_BOXES")
	}
	if *root == "" && os.Getenv("XBIN_DATA") != "" {
		*root = filepath.Join(os.Getenv("XBIN_DATA"), "boxes")
	}
	if *root == "" {
		log.Fatal("fakesandbox: no -root (or XBIN_RES_BOXES / XBIN_DATA)")
	}
	if err := os.MkdirAll(*root, 0o755); err != nil {
		log.Fatal(err)
	}
	m := &fsbManager{Root: *root, DefaultFrom: *from}
	var ln net.Listener
	var err error
	if *addr != "" {
		ln, err = net.Listen("tcp", *addr)
	} else if sock := os.Getenv("XBIN_SOCKET"); sock != "" {
		_ = os.Remove(sock)
		ln, err = net.Listen("unix", sock)
	} else {
		log.Fatal("fakesandbox: no -addr and no XBIN_SOCKET")
	}
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("fakesandbox: serving %s, sandboxes in %s", ln.Addr(), *root)
	log.Fatal(http.Serve(ln, m))
}
