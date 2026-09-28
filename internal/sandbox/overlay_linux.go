//go:build linux

package sandbox

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// mountOverlay mounts a kernel overlay, waiting out a predecessor's teardown:
// overlayfs refuses (EBUSY) an upper or work dir another overlay still uses,
// and a sandbox that just exited on the same dirs releases them only when its
// mount namespace is torn down, asynchronously and after its flock has gone
// (exit_files runs before exit_task_namespaces). A restart that follows the
// lock at once would otherwise fail for a moment.
func mountOverlay(newroot, opt string) error {
	err := unix.Mount("overlay", newroot, "overlay", 0, opt)
	for i := 0; errors.Is(err, unix.EBUSY) && i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		err = unix.Mount("overlay", newroot, "overlay", 0, opt)
	}
	return err
}
