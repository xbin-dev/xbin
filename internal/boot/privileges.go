package boot

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// Privileges is the process-identity side of boot: decision D13(b), xbind
// started as root on a workspace owned by someone else becomes that user
// so bind-mounted workspaces keep sane file ownership. An interface so a
// test boot never touches the process (NoPrivileges).
type Privileges interface {
	// Euid is the effective uid boot decides by (0 = root).
	Euid() int
	// DropToOwner becomes the workspace directory's owner (no-op when the
	// workspace is root's).
	DropToOwner(ws string) error
}

// OSPrivileges is the real thing.
type OSPrivileges struct{}

func (OSPrivileges) Euid() int { return os.Geteuid() }

func (OSPrivileges) DropToOwner(ws string) error {
	fi, err := os.Stat(ws)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		return nil
	}
	slog.Info("dropping privileges to workspace owner", "uid", st.Uid, "gid", st.Gid)
	if err := syscall.Setgroups([]int{int(st.Gid)}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(int(st.Gid)); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(int(st.Uid)); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	return nil
}

// NoPrivileges never drops and reports a non-root uid: the test boot.
type NoPrivileges struct{}

func (NoPrivileges) Euid() int                { return 1 }
func (NoPrivileges) DropToOwner(string) error { return nil }
