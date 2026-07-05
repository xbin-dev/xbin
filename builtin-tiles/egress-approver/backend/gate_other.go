//go:build !linux

package main

import "net"

// The dataplane is Linux-only (AF_PACKET + policy routing in a network
// namespace). On other platforms the control plane still builds and runs — the
// UI shows state — but nothing is gated or metered. xbind only ever runs this
// under Linux isolation anyway; these stubs keep `go build` green on dev hosts.

func discoverLinks() (egress string, clients int) { return "", 0 }

func setupGate(egress string) error { return nil }

func applyGate(egress string, approved []string) error { return nil }

func sniffClients(onFlow func(remote net.IP, inbound bool, n int)) {}
