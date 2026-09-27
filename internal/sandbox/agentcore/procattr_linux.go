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

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// What a session's process starts with: its directory, its user, its
// program.

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

// lookPath resolves argv0 against the session's PATH (the agent's own
// environment has none).
func lookPath(argv0 string, env []string) (string, error) {
	if strings.Contains(argv0, "/") {
		return argv0, nil
	}
	path := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			path = v
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
