// Command nsprobe is what agentcore's namespace integration test
// (ns_linux_test.go) runs as sessions of `bx __sbx-agent`: a static binary
// in a minimal lower, doing one thing per invocation and printing it.
//
//	env | pwd | fds | echo <text>… | cat | exit <code> | sleep <s>
//	read <file>      its content
//	ln <target> <link>
//	sleeper          start a child in this process group, print its pid, wait
//	                 for a signal (the child's end doesn't end it)
//	alive <pid>      "alive" or "gone"
//	stop <comm>      SIGSTOP the processes named comm
//	tty              the tty's size, a line read, the size again
//	attack           try to reach PID 1 (the agent) and the other
//	                 processes that aren't sessions; a JSON report
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: nsprobe <op> …")
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "env":
		for _, e := range os.Environ() {
			fmt.Println(e)
		}
	case "pwd":
		wd, err := os.Getwd()
		check(err)
		fmt.Println(wd)
	case "fds":
		fds()
	case "echo":
		fmt.Println(strings.Join(args, " "))
	case "cat":
		_, err := io.Copy(os.Stdout, os.Stdin)
		check(err)
		fmt.Fprintln(os.Stderr, "cat: done")
	case "exit":
		n, _ := strconv.Atoi(args[0])
		os.Exit(n)
	case "ln":
		check(os.Symlink(args[0], args[1]))
	case "read":
		b, err := os.ReadFile(args[0])
		check(err)
		fmt.Print(string(b))
	case "sleep":
		n, _ := strconv.Atoi(args[0])
		time.Sleep(time.Duration(n) * time.Second)
	case "sleeper":
		c := exec.Command("/proc/self/exe", "sleep", "600")
		check(c.Start())
		fmt.Println(c.Process.Pid)
		_ = c.Wait()
		// Only a signal ends the sleeper: a group signal that ends its child
		// first must not let it return from main (exit 0) before the
		// signal sent to it too gets to act, however late it runs.
		for {
			time.Sleep(time.Hour)
		}
	case "alive":
		pid, _ := strconv.Atoi(args[0])
		if unix.Kill(pid, 0) == nil {
			fmt.Println("alive")
		} else {
			fmt.Println("gone")
		}
	case "stop":
		stop(args[0])
	case "tty":
		ttySizes()
	case "attack":
		attack()
	default:
		fail("unknown op " + os.Args[1])
	}
}

// fds prints this process's descriptors past stdio and what they are.
func fds() {
	var out []string
	for fd := 3; fd < 1024; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			continue
		}
		l, _ := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		out = append(out, strconv.Itoa(fd)+"="+l)
	}
	fmt.Println(strings.Join(out, " "))
}

func stop(comm string) {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if b, _ := os.ReadFile("/proc/" + e.Name() + "/comm"); err == nil && strings.TrimSpace(string(b)) == comm {
			check(unix.Kill(pid, unix.SIGSTOP))
			fmt.Println("stopped", pid)
		}
	}
}

func ttySizes() {
	size := func() string {
		ws, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
		if err != nil {
			return "none"
		}
		return fmt.Sprintf("%dx%d", ws.Row, ws.Col)
	}
	_, err := unix.IoctlGetTermios(0, unix.TCGETS)
	fmt.Printf("tty=%v size=%s\n", err == nil, size())
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Printf("then size=%s line=%s\n", size(), strings.TrimSpace(line))
}

// attack is what a session could try against the agent (PID 1) and every
// other process of the sandbox that isn't a session (fuse-overlayfs, which
// holds the host's lower and upper directories): take their descriptors,
// trace them, read them, kill the agent, or leave the user namespace.
func attack() {
	r := map[string]any{}
	var peers, linked, opened, got, traced, rooted []string
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		comm, _ := os.ReadFile("/proc/" + e.Name() + "/comm")
		peers = append(peers, e.Name()+"="+strings.TrimSpace(string(comm)))
		fds, _ := os.ReadDir("/proc/" + e.Name() + "/fd")
		for _, f := range fds {
			p := filepath.Join("/proc", e.Name(), "fd", f.Name())
			if l, err := os.Readlink(p); err == nil {
				linked = append(linked, e.Name()+":"+f.Name()+"="+l)
			}
			if f, err := os.Open(p); err == nil {
				opened = append(opened, p)
				f.Close()
			}
		}
		for _, f := range []string{"root", "cwd", "environ", "mem"} {
			if _, err := os.Readlink("/proc/" + e.Name() + "/" + f); err == nil {
				rooted = append(rooted, e.Name()+"/"+f)
			} else if f, err := os.Open("/proc/" + e.Name() + "/" + f); err == nil {
				rooted = append(rooted, f.Name())
				f.Close()
			}
		}
		if pidfd, err := unix.PidfdOpen(pid, 0); err == nil {
			for fd := 0; fd < 64; fd++ {
				if n, err := unix.PidfdGetfd(pidfd, fd, 0); err == nil {
					got = append(got, e.Name()+":"+strconv.Itoa(fd))
					unix.Close(n)
				}
			}
			unix.Close(pidfd)
		}
		if ptraceAttach(pid) == nil {
			traced = append(traced, e.Name())
		}
	}
	r["peers"], r["readlinked"], r["opened"], r["pidfdGetfd"], r["ptraced"], r["proc"] = peers, linked, opened, got, traced, rooted

	// a fresh user or mount namespace, from a single-threaded child
	for name, flag := range map[string]uintptr{"unshareUser": unix.CLONE_NEWUSER, "unshareMount": unix.CLONE_NEWNS} {
		c := exec.Command("/proc/self/exe", "exit", "0")
		c.SysProcAttr = &syscall.SysProcAttr{Cloneflags: flag}
		r[name] = errString(c.Run())
	}

	sent := map[string]string{}
	for _, s := range []unix.Signal{unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT, unix.SIGUSR1, unix.SIGUSR2,
		unix.SIGABRT, unix.SIGSEGV, unix.SIGBUS, unix.SIGTRAP, unix.SIGSYS, unix.SIGPIPE, unix.SIGALRM, unix.SIGSTOP, unix.SIGKILL} {
		sent[unix.SignalName(s)] = errString(unix.Kill(1, s))
		time.Sleep(20 * time.Millisecond)
	}
	r["signals"] = sent
	time.Sleep(300 * time.Millisecond) // let a signal that would end it do so
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func ptraceAttach(pid int) error {
	runtime.LockOSThread() // a tracer is a thread
	defer runtime.UnlockOSThread()
	if err := unix.PtraceAttach(pid); err != nil {
		return err
	}
	_ = unix.PtraceDetach(pid)
	return nil
}

func errString(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "nsprobe: "+msg)
	os.Exit(1)
}
