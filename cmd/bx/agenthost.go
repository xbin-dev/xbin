package main

import (
	"os"

	"github.com/xbin-dev/xbin/internal/agent/host"
)

// cmdAgentHost is `bx __agent-host`: the process that replaces the shell in
// an agent session's sandbox (internal/agent/host, D74). Not for humans —
// xbind runs it and speaks to it over stdio.
func cmdAgentHost() error {
	os.Exit(host.New(os.Stdin, os.Stdout, os.Stderr).Run())
	return nil
}
