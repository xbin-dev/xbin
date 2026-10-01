//go:build linux

package agentcore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// What a session's process starts with: its directory, its user, its
// program, its OOM score.

// sessionCwd is the directory the session starts in: Cwd, or "/" when it
// doesn't name one — unless CwdStrict, which makes that an error.
func sessionCwd(ex proto.Exec) (string, error) {
	fi, err := os.Stat(ex.Cwd)
	if ex.Cwd != "" && err == nil && fi.IsDir() {
		return ex.Cwd, nil
	}
	if !ex.CwdStrict {
		return "/", nil
	}
	switch {
	case ex.Cwd == "":
		return "", errors.New("cwd: none given")
	case err != nil:
		return "", fmt.Errorf("cwd %s: %w", ex.Cwd, errors.Unwrap(err))
	}
	return "", fmt.Errorf("cwd %s: not a directory", ex.Cwd)
}

// idMaps is where credential reads the sandbox's id maps (tests point it
// elsewhere).
var idMaps = "/proc/self"

// credential is the session's uid/gid, refused when the sandbox doesn't map
// it; nil runs it as the agent.
func credential(ex proto.Exec) (*syscall.Credential, error) {
	if ex.UID == nil && ex.GID == nil {
		return nil, nil
	}
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	if ex.UID != nil {
		uid = *ex.UID
	}
	if ex.GID != nil {
		gid = *ex.GID
	}
	if !idMapped(filepath.Join(idMaps, "uid_map"), uid) {
		return nil, fmt.Errorf("uid %d is not mapped in this sandbox", uid)
	}
	if !idMapped(filepath.Join(idMaps, "gid_map"), gid) {
		return nil, fmt.Errorf("gid %d is not mapped in this sandbox", gid)
	}
	if os.Getuid() != 0 && uid == uint32(os.Getuid()) && gid == uint32(os.Getgid()) {
		return nil, nil // already so (and not root: the groups can't change)
	}
	// no supplementary groups — unless the user namespace denies setgroups
	// (a single-id map), where the agent's own can't be dropped anyway
	b, _ := os.ReadFile(filepath.Join(idMaps, "setgroups"))
	deny := strings.TrimSpace(string(b)) == "deny"
	return &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}, NoSetGroups: deny}, nil
}

// idMapped reports whether id is inside a line of a uid_map/gid_map.
func idMapped(file string, id uint32) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		first, err1 := strconv.ParseUint(f[0], 10, 32)
		n, err2 := strconv.ParseUint(f[2], 10, 32)
		if err1 == nil && err2 == nil && uint64(id) >= first && uint64(id) < first+n {
			return true
		}
	}
	return false
}

// defaultPATH is a session's PATH when its exec names none: the rootfs
// toolchains first, as terminals and backends have it.
const defaultPATH = sandbox.RootfsPATH

// sessionEnv is the environment a session starts with: exactly the exec's,
// plus defaultPATH when it names no PATH — never the agent's own (a nil
// os.ProcAttr.Env would inherit it).
func sessionEnv(env []string) []string {
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			return append([]string{}, env...)
		}
	}
	return append([]string{"PATH=" + defaultPATH}, env...)
}

// lookPath resolves argv0 against the session's PATH (the first in env).
func lookPath(argv0 string, env []string) (string, error) {
	if strings.Contains(argv0, "/") {
		return argv0, nil
	}
	path := defaultPATH
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			path = v
			break
		}
	}
	for _, dir := range filepath.SplitList(path) {
		p := filepath.Join(dir, argv0)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in PATH", argv0)
}

// ownOOMScore is the agent's own oom_score_adj (tests point it elsewhere).
var ownOOMScore = "/proc/self/oom_score_adj"

// spawnSession starts session id's process, one spawn at a time
// (Core.spawnMu). A session from 2 on starts with the core's
// SessionOOMScoreAdj: a process inherits its parent's score at the clone,
// before any of its code runs, so the agent takes that score for the spawn
// and goes back to its own once Start has returned (the clone done).
// Written to the session's process after the start instead, the score
// raced the session: whatever it forked before the write — a shell's first
// command — kept the agent's score for good, and a quick session already
// exiting failed the write.
//
// Raising a score needs no privilege, and the agent may always go back to
// its own: oom_score_adj_min, the floor a privileged writer sets (say
// xbind's unit's OOMScoreAdjust), is at most the score that writer gave.
// So taking the session's score fails only when that is below the floor:
// logged, and the session keeps the agent's score. For the moment of the
// clone the agent carries the session's score, which matters only to an
// OOM kill in that instant.
func (c *Core) spawnSession(id int, argv0 string, argv []string, attr *os.ProcAttr) (*os.Process, <-chan unix.WaitStatus, error) {
	c.spawnMu.Lock()
	defer c.spawnMu.Unlock()
	if adj := c.o.SessionOOMScoreAdj; adj != 0 && id >= 2 {
		if back := c.takeOOMScore(id, adj); back != nil {
			defer back()
		}
	}
	return c.o.Spawn.Start(argv0, argv, attr)
}

// takeOOMScore sets the agent's own oom_score_adj to adj for session id's
// spawn; it returns what puts the agent's own back, or nil when there is
// nothing to put back (already adj, or not set: logged).
func (c *Core) takeOOMScore(id, adj int) (back func()) {
	b, err := os.ReadFile(ownOOMScore)
	if err != nil {
		c.o.Logf("session %d: oom_score_adj %d: %v", id, adj, err)
		return nil
	}
	own := strings.TrimSpace(string(b))
	want := strconv.Itoa(adj)
	if own == want {
		return nil
	}
	if err := os.WriteFile(ownOOMScore, []byte(want), 0); err != nil {
		c.o.Logf("session %d: oom_score_adj %d: %v", id, adj, err)
		return nil
	}
	return func() {
		if err := os.WriteFile(ownOOMScore, []byte(own), 0); err != nil {
			c.o.Logf("the agent's own oom_score_adj back to %s after session %d's start: %v", own, id, err)
		}
	}
}
