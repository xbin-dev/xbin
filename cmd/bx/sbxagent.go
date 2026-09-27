package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/xbin-dev/xbin/internal/sandbox/agentcore"
)

// cmdSbxAgent is `bx __sbx-agent --fd N [--lock M]`: PID 1 of a
// namespace-mode tile sandbox, serving xbind's connections to it
// (internal/sandbox/agentcore, plans/tile-sandbox-runtime.md §2.4). Not for
// humans — the sandbox's init execs it and appends the fd numbers.
func cmdSbxAgent(args []string) error {
	fd, lock, err := sbxAgentArgs(args)
	if err != nil {
		return err
	}
	os.Exit(agentcore.RunNamespace(fd, lock))
	return nil
}

// sbxAgentArgs parses `--fd N [--lock M]` (lock 0: none).
func sbxAgentArgs(args []string) (fd, lock int, err error) {
	usage := fmt.Errorf("usage: bx __sbx-agent --fd N [--lock M]")
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return 0, 0, usage
		}
		n, err := strconv.Atoi(args[i+1])
		if err != nil || n < 3 {
			return 0, 0, usage
		}
		switch {
		case args[i] == "--fd" && fd == 0:
			fd = n
		case args[i] == "--lock" && lock == 0:
			lock = n
		default:
			return 0, 0, usage
		}
	}
	if fd == 0 || fd == lock {
		return 0, 0, usage
	}
	return fd, lock, nil
}
