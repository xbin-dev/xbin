//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// InitArg is the hidden subcommand buxond (and the test binary) must dispatch
// to RunInit *before* flag parsing — it is the re-exec target that runs inside
// the new namespaces.
const InitArg = "__sandbox-init"

// Available reports whether unprivileged user namespaces are usable here.
func Available() bool {
	if b, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		return len(b) > 0 && b[0] == '1'
	}
	// The sysctl is absent on kernels that always allow it; assume yes on Linux.
	return true
}

// Launch builds (but does not start) an *exec.Cmd that, when started, re-execs
// this binary as PID 1 of a fresh user+mount+pid+net+ipc+uts namespace set,
// where the init reads the spec and execs the backend. The caller keeps its own
// Stdout/Stderr/Start/Wait wiring. The returned cleanup removes the spec temp
// file; call it after the process has started (init unlinks it too).
func Launch(s *Spec) (*exec.Cmd, func(), error) {
	f, err := os.CreateTemp("", "bx-spec-*.json")
	if err != nil {
		return nil, func() {}, err
	}
	if err := json.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, func() {}, err
	}
	f.Close()
	cleanup := func() { os.Remove(f.Name()) }

	cmd := exec.Command("/proc/self/exe", InitArg, f.Name())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID |
			unix.CLONE_NEWNET | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: s.HostUID, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: s.HostGID, Size: 1}},
		GidMappingsEnableSetgroups: false, // rootless: setgroups=deny
	}
	return cmd, cleanup, nil
}

func must(err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
