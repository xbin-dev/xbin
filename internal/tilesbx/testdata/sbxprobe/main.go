// sbxprobe is the tile-sandbox integration tests' probe: a static binary
// run inside a sandbox (bound in read-only through a {source:true} mount),
// so the tests need no tools in the sandbox's root.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: sbxprobe <op> [args]")
	}
	a := os.Args[2:]
	switch os.Args[1] {
	case "write": // path content
		if err := os.WriteFile(a[0], []byte(a[1]), 0o644); err != nil {
			fail(err.Error())
		}
		fmt.Println("ok")
	case "mkdir":
		if err := os.MkdirAll(a[0], 0o755); err != nil {
			fail(err.Error())
		}
		fmt.Println("ok")
	case "cat":
		b, err := os.ReadFile(a[0])
		if err != nil {
			fail(err.Error())
		}
		os.Stdout.Write(b)
	case "owner": // path: its uid:gid (a final symlink itself)
		var st syscall.Stat_t
		if err := syscall.Lstat(a[0], &st); err != nil {
			fail(err.Error())
		}
		fmt.Printf("%d:%d\n", st.Uid, st.Gid)
	case "chown-r": // path: it and everything under it back to root
		err := filepath.WalkDir(a[0], func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(p, 0, 0)
		})
		if err != nil {
			fail(err.Error())
		}
		fmt.Println("ok")
	case "tcp": // addr: how a connect ends, and how fast
		start := time.Now()
		c, err := net.DialTimeout("tcp", a[0], 3*time.Second)
		ms := time.Since(start).Milliseconds()
		switch {
		case err == nil:
			c.Close()
			fmt.Printf("connected %d\n", ms)
		case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET):
			fmt.Printf("reset %d\n", ms)
		default:
			fmt.Printf("error %d %v\n", ms, err)
		}
	case "dns": // name server: the answer's rcode, and how fast
		start := time.Now()
		rcode, err := query(a[0], a[1])
		ms := time.Since(start).Milliseconds()
		if err != nil {
			fmt.Printf("error %d %v\n", ms, err)
			return
		}
		fmt.Printf("rcode %d %d\n", rcode, ms)
	case "unshare":
		if err := syscall.Unshare(syscall.CLONE_NEWUSER); err != nil {
			fmt.Println("refused", err)
			return
		}
		fmt.Println("allowed")
	case "alloc": // MiB, touched
		n, _ := strconv.Atoi(a[0])
		b := make([]byte, n<<20)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		fmt.Println("allocated", len(b)>>20)
	case "sleep":
		d, _ := time.ParseDuration(a[0])
		time.Sleep(d)
	default:
		fail("unknown op " + os.Args[1])
	}
}

// query sends one A query for name to server (host:port) over UDP and
// returns the answer's rcode.
func query(name, server string) (int, error) {
	msg := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range splitLabels(name) {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0, 0, 1, 0, 1)
	c, err := net.Dial("udp", server)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(msg); err != nil {
		return 0, err
	}
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 4 || binary.BigEndian.Uint16(buf) != 0x1234 {
		return 0, errors.New("a bad answer")
	}
	return int(buf[3] & 0x0f), nil
}

func splitLabels(name string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			if i > start {
				out = append(out, name[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "sbxprobe:", msg)
	os.Exit(1)
}
