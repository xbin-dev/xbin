//go:build unix

// Package portlease reserves loopback ports among the test harnesses of one
// machine.
//
// A test that starts a program on a port of its choosing (xbind's --listen,
// a fake upstream, a relay) picks the port first and the program binds it
// later — for xbind seconds later, after it initialized its workspace. A
// port found free by binding :0 and closing is the kernel's to hand out
// again in that window, and with suites running side by side it handed the
// same one to two harnesses: one program failed to bind, or — worse — a
// test went on talking to another test's daemon.
//
// A lease closes the window among everyone who takes one: a port is held by
// an flock'ed file, one per port, in a directory all harnesses of this user
// share, from the moment it is picked until its holder lets go (the test's
// end). The kernel still doesn't know about leases: a process that binds :0
// itself and keeps the socket can be given a leased port nobody has bound
// yet. Harnesses that pick a port for something else to bind all come here,
// so that is no harness's doing any more; xbindtest's daemons survive it
// too (a first boot that loses its port picks another).
package portlease

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// dir is where the leases live: one lock file per port. Not under TMPDIR,
// which tests and runs set for themselves — every harness of this user must
// meet in one place. The name is the one test/xbindtest used when the lease
// was its own, so a checkout from before this package honours the same
// leases.
func dir() string { return fmt.Sprintf("/tmp/xbindtest-ports-%d", os.Getuid()) }

// lease takes port's lease: nil when another holder has it.
func lease(port int) (*os.File, error) {
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir(), strconv.Itoa(port)+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

// Reserve picks a port on 127.0.0.1 that is free now and that no other
// holder of a lease has or is about to bind, and answers it as host:port.
// The port is the caller's until release, which may be called more than
// once; a process that ends without it has let go too.
func Reserve() (addr string, release func(), err error) {
	for range 100 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", nil, err
		}
		tcp := ln.Addr().(*net.TCPAddr)
		f, err := lease(tcp.Port)
		ln.Close() // held until the lease is: nobody is given the port in between
		if err != nil {
			return "", nil, err
		}
		if f != nil {
			return tcp.String(), func() { f.Close() }, nil
		}
		// another harness holds it: its program is starting, or stopped and
		// about to start again
	}
	return "", nil, errors.New("portlease: no free port that another harness doesn't hold")
}

// Addr is Reserve for a test: host:port, held until the test and its
// cleanups are over.
func Addr(t testing.TB) string {
	t.Helper()
	addr, release, err := Reserve()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return addr
}

// Port is Addr's port.
func Port(t testing.TB) int {
	t.Helper()
	_, p, _ := net.SplitHostPort(Addr(t))
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
