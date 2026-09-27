//go:build linux

// Package guest is xbin-vmagent, PID 1 inside a VM sandbox
// (plans/vm-sandbox.md). It boots from an initramfs holding only itself,
// mounts the kernel filesystems, and waits on vsock for the host shim. The
// first control message (Config) turns the anonymous guest into this
// sandbox: the root overlay over the read-only rootfs image, the host binds
// (FUSE, served by the shim) at their host paths, the network, the clock. Then it runs sessions
// (the terminal shell, an agent host, a backend) and reports their exits.
//
// The sessions, their streams and the file operations are the exec core
// every sandbox agent shares (internal/sandbox/agentcore); this package is
// what only a VM guest does: the boot, Config, the FUSE relay, the gateway
// bridge and the dump.
//
// Nothing tile-specific exists before Config, which is what lets one booted
// guest be snapshotted as a template and restored for any tile.
package guest

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/agentcore"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// Main runs the agent; it never returns.
func Main() {
	if len(os.Args) > 1 && os.Args[1] == relayArg {
		relayMain() // relay_linux.go: the FUSE relay's own process
		return
	}
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "xbin-vmagent: must run as PID 1 inside a VM sandbox")
		os.Exit(2)
	}
	if err := earlyMounts(); err != nil {
		fatal("early mounts: %v", err)
	}
	a := &agent{spawn: agentcore.PID1Spawner()}
	if err := a.startRelay(); err != nil {
		fatal("FUSE relay: %v", err)
	}
	a.core = agentcore.New(agentcore.Options{
		Spawn:     a.spawn,
		Configure: a.configure,
		Sync:      unix.Sync,
		Root:      "/", // the sandbox's, once configure switched into it
		// an emulated guest can take a while between a session's first
		// stream and its exec (the shim dials each stream in turn)
		StreamWait: 60 * time.Second,
		Gateway:    serveGateway,
		Dump:       func() string { return a.dump(dumpBudget) },
		// a resident VM's execs (sessions from 2) go before the agent under
		// memory pressure; session 1, a backend's or a terminal's, keeps 0
		SessionOOMScoreAdj: 500,
		Logf:               logf,
	})
	ln, err := listenVsock(proto.AgentPort)
	if err != nil {
		fatal("vsock listen: %v", err)
	}
	logf("agent listening on vsock %d", proto.AgentPort)
	if fd, err := dialHost(proto.ReadyPort); err == nil {
		unix.Close(fd) // the shim connects now
	} else {
		logf("ready call: %v", err)
	}
	for {
		c, err := ln.accept()
		if err != nil {
			logf("accept: %v", err)
			continue
		}
		go a.core.Handle(c)
	}
}

// agent is the guest's process-wide state.
type agent struct {
	core  *agentcore.Core
	spawn agentcore.Spawner // PID 1's: every child and orphan is reaped there
	relay *relay            // the FUSE relay process (relay_linux.go)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "xbin-vmagent: "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	logf(format, args...)
	// A dead PID 1 panics the kernel, which Firecracker turns into an exit:
	// the shim reports the serial tail.
	os.Exit(1)
}
