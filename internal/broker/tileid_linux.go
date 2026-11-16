//go:build linux

package broker

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// fsIocGetVersion is FS_IOC_GETVERSION — _IOR('v', 1, long) — on the 64-bit
// targets xbind ships for (amd64, arm64); where it isn't, or the
// filesystem has none, the ioctl is refused and the generation left out.
const fsIocGetVersion = 0x80087601

// dirIdentity is a directory's own identity — what tells it from another
// directory made later at the same path: its inode number, and where the
// filesystem says them its birth time and the inode's generation (an inode
// number given out again carries another). No device: a mount that comes
// back under another number is the same tree. "" when it can't be read —
// no directory there, or a link.
func dirIdentity(dir string) string {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	defer unix.Close(fd)
	var st unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_INO|unix.STATX_BTIME, &st); err != nil {
		return ""
	}
	id := fmt.Sprintf("i%x", st.Ino)
	if st.Mask&unix.STATX_BTIME != 0 {
		id += fmt.Sprintf(":b%d.%09d", st.Btime.Sec, st.Btime.Nsec)
	}
	if gen, err := unix.IoctlGetUint32(fd, fsIocGetVersion); err == nil {
		id += fmt.Sprintf(":g%x", gen)
	}
	return id
}
