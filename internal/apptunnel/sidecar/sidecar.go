// Package sidecar runs xbind's iroh endpoint (D204, plans/app-tunnel.md):
// the xbin-iroh binary embedded in xbind, extracted to a memfd and started
// in a bare namespace sandbox with the host network, then supervised —
// restarted with backoff, re-told its allowlist after every start. xbind
// gets each iroh stream as a net.Conn whose first bytes were the wire
// header; Noise and HTTP happen in xbind (internal/apptunnel), never here.
//
// This file is the contract the service codes against; the process
// supervisor implements it (supervisor.go), and tests use fakes.
package sidecar

import (
	"context"
	"crypto/ed25519"
	"net"

	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
)

// Config starts (or restarts) the endpoint.
type Config struct {
	Key     ed25519.PrivateKey // the endpoint's identity; sent on stdin only
	Relay   wire.Relay
	Allow   []string // EndpointIds (hex) admitted on xbin/tunnel/1
	Pairing bool     // xbin/pair/1 admitted
}

// State is the supervisor's lifecycle state, as the admin view names it.
type State string

const (
	StateDisabled    State = "disabled"    // not started (tunnels off)
	StateUnavailable State = "unavailable" // no binary in this build and no XBIN_IROH
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateDegraded    State = "degraded" // running, but the relay isn't connected
	StateFailed      State = "failed"   // gave up for now: Error says why; retried with backoff
)

// Status is the supervisor's view for the admin console and alerts.
type Status struct {
	State          State
	Error          string
	Confined       bool   // runs in the namespace sandbox
	Exec           string // how the binary was run: "memfd" | "rundir" | "workspace" | "path"
	Restarts       int
	EndpointID     string // hex, from the last ready
	Ticket         string // relay-only EndpointTicket, from the last ready
	HomeRelay      string
	RelayConnected bool
	Conns          []wire.ConnInfo
	Version        string
}

// Stream is one iroh bi-stream handed to xbind: Conn carries the spliced
// bytes after the header (already read and checked).
type Stream struct {
	Header wire.Header
	Conn   net.Conn
}

// Runner is the iroh endpoint as the service sees it.
type Runner interface {
	// Start launches the sidecar with cfg and keeps it running (restarting
	// with backoff) until Stop or ctx ends. A second Start replaces the
	// config and restarts the process.
	Start(ctx context.Context, cfg Config) error
	// SetAllow replaces the tunnel/1 allowlist and returns once the sidecar
	// acknowledged it (or ctx ended): a device told "enrolled" must not be
	// refused on its first connect.
	SetAllow(ctx context.Context, ids []string) error
	// SetPairing opens or closes xbin/pair/1.
	SetPairing(open bool)
	// Drop closes every connection of an endpoint (a revoked device).
	Drop(endpoint string)
	// SelfTest dials the endpoint through its relay from a throwaway one.
	SelfTest(ctx context.Context) (wire.Event, error)
	// Streams delivers accepted streams until Stop.
	Streams() <-chan Stream
	Status() Status
	Stop()
}
