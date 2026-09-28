package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/xbin-dev/xbin/internal/sandbox/agentcore"
)

// cmdSbxAgent is `bx __sbx-agent --fd N [--lock M] [--fuse-pid P]`: PID 1 of
// a namespace-mode tile sandbox, serving xbind's connections to it
// (internal/sandbox/agentcore, plans/tile-sandbox-runtime.md §2.4). Not for
// humans — the sandbox's init execs it and appends the fd numbers, and the
// pid of the fuse-overlayfs it started when that serves the root.
func cmdSbxAgent(args []string) error {
	a, err := sbxAgentArgs(args)
	if err != nil {
		return err
	}
	os.Exit(agentcore.RunNamespace(a.fd, a.lock, a.fusePID))
	return nil
}

// sbxArgs are `bx __sbx-agent`'s flags (lock, fusePID 0: none).
type sbxArgs struct{ fd, lock, fusePID int }

// sbxAgentArgs parses `--fd N [--lock M] [--fuse-pid P]`: fds past stdio, a
// pid past the agent's own (1).
func sbxAgentArgs(args []string) (sbxArgs, error) {
	usage := fmt.Errorf("usage: bx __sbx-agent --fd N [--lock M] [--fuse-pid P]")
	var a sbxArgs
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return sbxArgs{}, usage
		}
		n, err := strconv.Atoi(args[i+1])
		if err != nil || n < 2 {
			return sbxArgs{}, usage
		}
		switch {
		case args[i] == "--fd" && a.fd == 0 && n > 2:
			a.fd = n
		case args[i] == "--lock" && a.lock == 0 && n > 2:
			a.lock = n
		case args[i] == "--fuse-pid" && a.fusePID == 0:
			a.fusePID = n
		default:
			return sbxArgs{}, usage
		}
	}
	if a.fd == 0 || a.fd == a.lock {
		return sbxArgs{}, usage
	}
	return a, nil
}
