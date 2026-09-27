package tilesbx

// command.go — what a command in a tile sandbox runs with
// (plans/tile-sandbox-runtime.md §3.5–3.6, §8.3): its argv (a cmd through
// the sandbox's shell), a cwd that must exist, its user, and its
// environment — IN_SANDBOX, SANDBOX_ID, SANDBOX_NAME and HOME from xbind,
// the definition's defaults.env and the command's env over them, never an
// XBIN_* variable. The agent adds only a PATH, when none is named. And the
// signals: the ones a manager may send, and the names of those a sandbox
// reports.

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// signals are the signals a manager may send (the contract's enum).
var signals = map[string]syscall.Signal{"INT": syscall.SIGINT, "TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "HUP": syscall.SIGHUP}

// linuxSignals names the signals a sandbox reports (Linux numbering: the
// agent runs on Linux, whatever runs xbind).
var linuxSignals = map[int]string{1: "HUP", 2: "INT", 3: "QUIT", 4: "ILL", 5: "TRAP", 6: "ABRT", 7: "BUS", 8: "FPE",
	9: "KILL", 10: "USR1", 11: "SEGV", 12: "USR2", 13: "PIPE", 14: "ALRM", 15: "TERM", 16: "STKFLT", 17: "CHLD",
	18: "CONT", 19: "STOP", 20: "TSTP", 21: "TTIN", 22: "TTOU", 23: "URG", 24: "XCPU", 25: "XFSZ", 26: "VTALRM",
	27: "PROF", 28: "WINCH", 29: "IO", 30: "PWR", 31: "SYS"}

// signalWord is how the contract names signal n ("KILL"; "SIG40" past the table).
func signalWord(n int) string {
	if s, ok := linuxSignals[n]; ok {
		return s
	}
	return "SIG" + strconv.Itoa(n)
}

// newBoot is this xbind start's exec-id prefix: 6 random hex.
func newBoot() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("tilesbx: no randomness for exec ids: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// command is what a run, an exec or a tty asks to run.
type command struct {
	Cmd      string
	Argv     []string
	Cwd      string
	Env      map[string]string
	UID, GID *uint32
	ForUser  string
}

// shellOf is the sandbox's shell: a cmd runs as `<shell> -lc <cmd>`.
func shellOf(d *Def) string {
	if d.Defaults.Shell != "" {
		return d.Defaults.Shell
	}
	return "/bin/sh"
}

// execOf checks a command against its sandbox and builds its session:
// cmd or argv, not both; a cwd that must exist (defaults.cwd, else /); a
// user the sandbox runs; argv and env within argvEnvMax; no XBIN_* in env.
// Every tile-sandbox session skips the agent's per-exit sync (a stop syncs).
func (m *Manager) execOf(d *Def, c command) (proto.Exec, error) {
	switch {
	case c.Cmd != "" && len(c.Argv) > 0:
		return proto.Exec{}, refuse(RefInvalid, "give cmd or argv, not both")
	case c.Cmd == "" && len(c.Argv) == 0:
		return proto.Exec{}, refuse(RefInvalid, "cmd or argv is required")
	case len(c.Argv) > 0 && c.Argv[0] == "":
		return proto.Exec{}, refuse(RefInvalid, "argv[0] is empty")
	}
	argv := c.Argv
	if c.Cmd != "" {
		argv = []string{shellOf(d), "-lc", c.Cmd}
	}
	size := 0
	for _, a := range argv {
		if strings.ContainsRune(a, 0) {
			return proto.Exec{}, refuse(RefInvalid, "argv holds a NUL byte")
		}
		size += len(a) + 1
	}
	if err := checkEnv(c.Env, "env"); err != nil {
		return proto.Exec{}, err
	}
	for k, v := range c.Env {
		size += len(k) + len(v) + 2
	}
	if size > argvEnvMax {
		return proto.Exec{}, refuse(RefTooLarge, "argv and env are %d bytes, over %d", size, argvEnvMax)
	}
	cwd := c.Cwd
	if cwd == "" {
		cwd = d.Defaults.Cwd
	}
	if cwd == "" {
		cwd = "/"
	}
	if err := checkAbs(cwd); err != nil {
		return proto.Exec{}, refuse(RefInvalid, "cwd: %v", err)
	}
	if err := m.checkIDs(d.Mode, c.UID, c.GID, ""); err != nil {
		return proto.Exec{}, err
	}
	if len(c.ForUser) > maxClaim || hasControl(c.ForUser) {
		return proto.Exec{}, refuse(RefInvalid, "forUser must be at most %d printable characters", maxClaim)
	}
	uid, gid := c.UID, c.GID
	if uid == nil {
		uid = d.Defaults.UID
	}
	if gid == nil {
		gid = d.Defaults.GID
	}
	return proto.Exec{Argv: argv, Env: sessionEnv(d, c.Env, uid), Cwd: cwd, CwdStrict: true,
		UID: uid, GID: gid, NoSync: true}, nil
}

// sessionEnv is a command's environment (§8.3): IN_SANDBOX=1 always,
// SANDBOX_ID and SANDBOX_NAME (the sandbox's name), HOME (/root for root,
// / for any other user) and a PATH, then the definition's defaults.env
// and the command's env over them — never an XBIN_* variable (validate.go
// refuses them; this drops one that got past).
func sessionEnv(d *Def, env map[string]string, uid *uint32) []string {
	home := "/root"
	if uid != nil && *uid != 0 {
		home = "/"
	}
	vars := map[string]string{"PATH": defaultPATH, "HOME": home, "SANDBOX_ID": d.Name, "SANDBOX_NAME": d.Name}
	for _, layer := range []map[string]string{d.Defaults.Env, env} {
		for k, v := range layer {
			if checkEnv(map[string]string{k: v}, "") == nil {
				vars[k] = v
			}
		}
	}
	vars["IN_SANDBOX"] = "1"
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
