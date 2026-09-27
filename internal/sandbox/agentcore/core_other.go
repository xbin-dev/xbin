//go:build !linux

// Package agentcore is the exec core that runs inside a Linux sandbox (the
// VM guest's agent, `bx __sbx-agent`); elsewhere it only refuses.
package agentcore

import (
	"fmt"
	"os"
)

// RunNamespace refuses: tile sandboxes are Linux-only.
func RunNamespace(agentFD, lockFD, fusePID int) int {
	fmt.Fprintln(os.Stderr, "sbx-agent: tile sandboxes are Linux-only")
	return 125
}
