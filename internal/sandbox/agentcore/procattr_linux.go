//go:build linux

package agentcore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

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
const defaultPATH = "/usr/local/go/bin:/usr/local/node/bin:/usr/local/bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

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

// adjustOOM gives session id's process, just started, the core's
// SessionOOMScoreAdj — sessions from 2 on only. Raising a score needs no
// privilege, so the write fails only when the process is already gone (a
// quick exec: not logged) or when the target is below the floor a
// privileged writer set for the agent (oom_score_adj_min, say xbind's unit's
// OOMScoreAdjust: logged). Until the write lands the session has the
// agent's score, which matters only to an OOM kill in that instant.
func (c *Core) adjustOOM(id, pid int) {
	adj := c.o.SessionOOMScoreAdj
	if adj == 0 || id < 2 {
		return
	}
	proc := "/proc/" + strconv.Itoa(pid)
	if err := os.WriteFile(proc+"/oom_score_adj", []byte(strconv.Itoa(adj)), 0); err != nil && !ended(proc) {
		c.o.Logf("session %d: oom_score_adj %d: %v", id, adj, err)
	}
}

// ended reports whether the process at proc (/proc/<pid>) is gone or a
// zombie — whose oom_score_adj is root's and has nothing to adjust.
func ended(proc string) bool {
	b, err := os.ReadFile(proc + "/stat")
	i := bytes.LastIndexByte(b, ')') // the state follows the comm: "pid (comm) S …"
	return err != nil || i < 0 || i+2 >= len(b) || b[i+2] == 'Z' || b[i+2] == 'X'
}
