//go:build linux

package sandbox

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// echoAgent accepts connections from the factory's child end until EOF,
// answering each line with "echo:<line>"; done gets AcceptFrom's final error.
func echoAgent(child *os.File) (done chan error) {
	done = make(chan error, 1)
	go func() {
		for {
			c, err := AcceptFrom(child)
			if err != nil {
				done <- err
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := io.WriteString(c, "echo:"+line); err != nil {
						return
					}
				}
			}()
		}
	}()
	return done
}

func roundTrip(t *testing.T, f *Factory, msg string) error {
	t.Helper()
	c, err := f.Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, msg+"\n"); err != nil {
		return err
	}
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if got != "echo:"+msg+"\n" {
		return fmt.Errorf("got %q, want echo of %q", got, msg)
	}
	return nil
}

func TestFactoryRoundTrip(t *testing.T) {
	f, child, err := NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	done := echoAgent(child)
	if err := roundTrip(t, f, "hello"); err != nil {
		t.Fatal(err)
	}
	// Half-close travels: the dialled end is a stream socket.
	c, err := f.Dial()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(*net.UnixConn); !ok {
		t.Fatalf("Dial returned %T, want *net.UnixConn", c)
	}
	c.Close()

	// xbind's end closed: the agent drains and sees io.EOF.
	f.Close()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("AcceptFrom after the factory closed: %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AcceptFrom did not return after the factory closed")
	}
	if _, err := f.Dial(); err == nil {
		t.Fatal("Dial on a closed factory succeeded")
	}
}

func TestFactoryConcurrentDials(t *testing.T) {
	f, child, err := NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	defer child.Close()
	echoAgent(child)
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- roundTrip(t, f, fmt.Sprintf("conn-%d", i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

// The connections queued before xbind's end closed are still accepted, then
// EOF: none is lost.
func TestFactoryDrainsBeforeEOF(t *testing.T) {
	f, child, err := NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	var conns []net.Conn
	for range 3 {
		c, err := f.Dial()
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	f.Close()
	for i := range 3 {
		c, err := AcceptFrom(child)
		if err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		c.Close()
	}
	if _, err := AcceptFrom(child); !errors.Is(err, io.EOF) {
		t.Fatalf("after the queue: %v, want io.EOF", err)
	}
	for _, c := range conns {
		c.Close()
	}
}

// The agent is gone (the child end closed everywhere): Dial fails at once.
func TestFactoryDialAfterPeerClose(t *testing.T) {
	f, child, err := NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	child.Close()
	if c, err := f.Dial(); err == nil {
		c.Close()
		t.Fatal("Dial succeeded with the agent gone")
	}
}

// An agent that stops accepting fills the factory's queue: Dial then fails
// promptly instead of blocking xbind.
func TestFactoryFullQueueIsAnError(t *testing.T) {
	f, child, err := NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	defer child.Close()
	deadline := time.Now().Add(20 * time.Second)
	for i := 0; ; i++ {
		c, err := f.Dial()
		if err != nil {
			t.Logf("queue full after %d dials: %v", i, err)
			return
		}
		c.Close()
		if i > 100000 || time.Now().After(deadline) {
			t.Fatalf("no error after %d dials with nobody accepting", i)
		}
	}
}

// A non-blocking child end (an agent may register it with the poller)
// accepts and sees EOF as a blocking one (what a sandbox inherits) does.
func TestAcceptFromNonBlocking(t *testing.T) {
	f, child, err := NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	fd, err := unix.Dup(int(child.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	nb := os.NewFile(uintptr(fd), "nb")
	defer nb.Close()
	done := echoAgent(nb)
	if err := roundTrip(t, f, "nonblocking"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	child.Close() // the dup is the agent's only copy now
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("AcceptFrom: %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a non-blocking AcceptFrom missed the EOF")
	}
}
