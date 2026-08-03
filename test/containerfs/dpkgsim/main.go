// dpkgsim replays dpkg's unpack syscall sequence (create 0600 → write →
// [stat] → fchmod 0755 → rename) for many files, waits out the kernel FUSE
// attr cache, then execs every file. Any "permission denied" here is the
// container-build failure of D44 in miniature. Used by the containerfs
// integration suite against overlay-on-gocryptfs stacks.
//
// Usage: dpkgsim DIR TOOL [N]
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func die(f string, a ...any) { fmt.Printf("FAIL: "+f+"\n", a...); os.Exit(1) }

func main() {
	if len(os.Args) < 3 {
		die("usage: dpkgsim DIR TOOL [N]")
	}
	dir := os.Args[1]  // target dir (gocryptfs mount or overlay merged dir)
	tool := os.Args[2] // static test binary to install
	n := 40
	if len(os.Args) > 3 {
		n, _ = strconv.Atoi(os.Args[3])
	}

	payload, err := os.ReadFile(tool)
	if err != nil {
		die("read tool: %v", err)
	}

	variants := []string{"plain", "fstat", "pathstat", "reopen"}
	for _, variant := range variants {
		vdir := filepath.Join(dir, variant)
		if err := os.MkdirAll(vdir, 0755); err != nil {
			die("mkdir: %v", err)
		}
		for i := 0; i < n; i++ {
			final := filepath.Join(vdir, fmt.Sprintf("prog%03d", i))
			tmp := final + ".dpkg-new"
			// dpkg: open(O_CREAT|O_EXCL|O_WRONLY, 0600)
			f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				die("[%s] create %s: %v", variant, tmp, err)
			}
			if _, err := f.Write(payload); err != nil {
				die("[%s] write: %v", variant, err)
			}
			switch variant {
			case "fstat":
				if _, err := f.Stat(); err != nil { // fstat(fd) mid-sequence
					die("[%s] fstat: %v", variant, err)
				}
			case "pathstat":
				if _, err := os.Stat(tmp); err != nil { // fresh LOOKUP/GETATTR
					die("[%s] stat: %v", variant, err)
				}
			case "reopen":
				f.Close()
				f, err = os.OpenFile(tmp, os.O_WRONLY, 0)
				if err != nil {
					die("[%s] reopen: %v", variant, err)
				}
			}
			// dpkg: fchmod(fd, final mode)
			if err := f.Chmod(0755); err != nil {
				die("[%s] fchmod: %v", variant, err)
			}
			if err := f.Sync(); err != nil {
				die("[%s] fsync: %v", variant, err)
			}
			f.Close()
			// dpkg: rename into place
			if err := os.Rename(tmp, final); err != nil {
				die("[%s] rename: %v", variant, err)
			}
		}
		fmt.Printf("[%s] unpacked %d files\n", variant, n)
	}

	// Let the kernel's FUSE attr/entry caches expire so exec-time GETATTR
	// really hits the daemon (and its in-memory identity cache) — within
	// the ~1s window the kernel answers from the Setattr reply and masks
	// daemon-side staleness.
	time.Sleep(2500 * time.Millisecond)

	bad := 0
	for _, variant := range variants {
		for i := 0; i < n; i++ {
			p := filepath.Join(dir, variant, fmt.Sprintf("prog%03d", i))
			out, err := exec.Command(p).CombinedOutput()
			if err != nil || string(out) != "EXEC-OK\n" {
				bad++
				var st syscall.Stat_t
				serr := syscall.Stat(p, &st)
				fmt.Printf("EXEC FAIL [%s] %s: err=%v out=%q stat(mode=%o uid=%d gid=%d err=%v)\n",
					variant, p, err, out, st.Mode, st.Uid, st.Gid, serr)
				if bad > 5 {
					os.Exit(1)
				}
			}
		}
	}
	if bad != 0 {
		os.Exit(1)
	}
	fmt.Println("ALL-EXECS-OK")
}
