// Command agentprobe is the entry of internal/sandbox's integration tests
// (launch_agent_linux_test.go): a stand-in for `bx __sbx-agent` that takes
// `--fd N --lock M` from its argv, performs "read:<path>" and
// "write:<path>" arguments, and then answers every connection xbind dials
// over the factory with a JSON report of what it saw, until the factory's
// EOF. Built statically by the test into a minimal lower.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

type report struct {
	Args         []string          `json:"args"`
	Hostname     string            `json:"hostname"`
	AgentFD      int               `json:"agentFd"`
	AgentCloexec bool              `json:"agentCloexec"` // as the entry received it
	LockFD       int               `json:"lockFd"`
	LockIno      uint64            `json:"lockIno"`
	Ops          map[string]string `json:"ops"`
}

func main() {
	r := report{Args: os.Args[1:], Ops: map[string]string{}}
	r.Hostname, _ = os.Hostname()
	for i, a := range os.Args {
		if i+1 < len(os.Args) {
			n, _ := strconv.Atoi(os.Args[i+1])
			switch a {
			case "--fd":
				r.AgentFD = n
			case "--lock":
				r.LockFD = n
			}
		}
		op, p, _ := strings.Cut(a, ":")
		switch op {
		case "read":
			if b, err := os.ReadFile(p); err != nil {
				r.Ops[a] = "ERR"
			} else {
				r.Ops[a] = string(b)
			}
		case "write":
			if err := os.WriteFile(p, []byte("w"), 0o644); err != nil {
				r.Ops[a] = "ERR"
			} else {
				r.Ops[a] = "ok"
			}
		}
	}
	if r.LockFD > 0 {
		var st unix.Stat_t
		if unix.Fstat(r.LockFD, &st) == nil {
			r.LockIno = st.Ino
		}
	}
	if r.AgentFD <= 0 {
		json.NewEncoder(os.Stdout).Encode(r)
		return
	}
	flags, err := unix.FcntlInt(uintptr(r.AgentFD), unix.F_GETFD, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent fd:", err)
		os.Exit(2)
	}
	r.AgentCloexec = flags&unix.FD_CLOEXEC != 0
	factory := os.NewFile(uintptr(r.AgentFD), "factory")
	for {
		c, err := sandbox.AcceptFrom(factory)
		if err != nil {
			fmt.Println("accept:", err) // io.EOF: xbind closed the factory
			return
		}
		json.NewEncoder(c).Encode(r)
		c.Close()
	}
}
