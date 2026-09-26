//go:build linux

// Package host is the VM sandbox's host shim, `bx __vm-host`
// (plans/vm-sandbox.md). The namespace sandbox execs it as its PID 1 in
// place of the workload; it boots Firecracker as its child, serves the
// sandbox's binds to the guest over 9P, and makes the guest's session look
// like an ordinary process to xbind: its stdio is the host PTY (or pipes),
// SIGWINCH resizes the guest PTY, SIGTERM reaches the guest process, and the
// shim exits with the guest process's status. Killing the shim tears down the
// pid namespace and Firecracker with it.
package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/p9fs"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// Main runs the shim from the spec at specPath and returns its exit code.
func Main(specPath string) int {
	b, err := os.ReadFile(specPath)
	if err != nil {
		return fail(nil, "read spec: %v", err)
	}
	_ = os.Remove(specPath)
	var hs proto.HostSpec
	if err := json.Unmarshal(b, &hs); err != nil {
		return fail(nil, "parse spec: %v", err)
	}
	s := &shim{hs: hs, serial: newRing(64 << 10)}
	return s.run()
}

type shim struct {
	hs     proto.HostSpec
	serial *ring // Firecracker's stdout/stderr: the guest console and VMM log
	fc     *exec.Cmd
	fcDone chan struct{}
	ctl    *proto.Conn
	tty    bool
	raw    *term.State
}

func (s *shim) run() int {
	if err := os.MkdirAll(s.hs.RunDir, 0o700); err != nil {
		return fail(s, "run dir: %v", err)
	}
	stopFiles, err := s.serveFiles()
	if err != nil {
		return fail(s, "file server: %v", err)
	}
	defer stopFiles()
	started := time.Now()
	if err := s.startFirecracker(); err != nil {
		return fail(s, "start firecracker: %v", err)
	}
	defer s.killFirecracker()

	ctl, err := s.dialAgent(proto.Hello{Kind: "ctl"}, 20*time.Second)
	if err != nil {
		return fail(s, "guest agent: %v", err)
	}
	s.ctl = proto.NewConn(ctl, nil)
	if s.hs.Debug {
		fmt.Fprintf(os.Stderr, "[vm] agent up after %s\r\n", time.Since(started).Round(time.Millisecond))
	}
	cfg := proto.Config{
		Time:     time.Now().UnixNano(),
		Hostname: s.hs.Hostname,
		Net:      s.hs.Net,
		Root:     proto.Root{Image: "/dev/vda", ImageType: s.hs.ImageType},
		Mounts:   s.hs.Mounts,
	}
	if s.hs.Disk != "" {
		cfg.Root.Upper = "/dev/vdb"
	}
	if err := s.ctl.Send(proto.Msg{Op: "config", Config: &cfg}); err != nil {
		return fail(s, "configure guest: %v", err)
	}
	if m, err := s.expect("ready"); err != nil {
		return fail(s, "configure guest: %v", err)
	} else if m.Op == "error" {
		return fail(s, "configure guest: %s", m.Error)
	}
	if s.hs.Debug {
		fmt.Fprintf(os.Stderr, "[vm] guest configured after %s\r\n", time.Since(started).Round(time.Millisecond))
	}
	return s.session()
}

// session runs session 1 and returns its exit code.
func (s *shim) session() int {
	ex := s.hs.Guest
	ex.Session = 1
	s.tty = ex.TTY && term.IsTerminal(0)
	ex.TTY = s.tty
	if s.tty {
		if ws, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ); err == nil {
			ex.Rows, ex.Cols = ws.Row, ws.Col
		}
	}
	var streams sync.WaitGroup
	if s.tty {
		c, err := s.dialAgent(proto.Hello{Kind: "stream", Session: 1, Stream: "pty"}, 5*time.Second)
		if err != nil {
			return fail(s, "pty stream: %v", err)
		}
		if st, err := term.MakeRaw(0); err == nil {
			s.raw = st
		}
		go func() { _, _ = io.Copy(c, os.Stdin) }()
		streams.Add(1)
		go func() { defer streams.Done(); _, _ = io.Copy(os.Stdout, c) }()
	} else {
		in, err := s.dialAgent(proto.Hello{Kind: "stream", Session: 1, Stream: "stdin"}, 5*time.Second)
		if err != nil {
			return fail(s, "stdin stream: %v", err)
		}
		out, err := s.dialAgent(proto.Hello{Kind: "stream", Session: 1, Stream: "stdout"}, 5*time.Second)
		if err != nil {
			return fail(s, "stdout stream: %v", err)
		}
		errc, err := s.dialAgent(proto.Hello{Kind: "stream", Session: 1, Stream: "stderr"}, 5*time.Second)
		if err != nil {
			return fail(s, "stderr stream: %v", err)
		}
		go func() {
			_, _ = io.Copy(in, os.Stdin)
			if uc, ok := in.(*net.UnixConn); ok {
				_ = uc.CloseWrite()
			} else {
				in.Close()
			}
		}()
		streams.Add(2)
		go func() { defer streams.Done(); _, _ = io.Copy(os.Stdout, out) }()
		go func() { defer streams.Done(); _, _ = io.Copy(os.Stderr, errc) }()
	}

	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, unix.SIGWINCH, unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT)
	go func() {
		for sig := range sigs {
			if sig == unix.SIGWINCH {
				if ws, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ); err == nil {
					_ = s.ctl.Send(proto.Msg{Op: "resize", Session: 1, Rows: ws.Row, Cols: ws.Col})
				}
				continue
			}
			_ = s.ctl.Send(proto.Msg{Op: "signal", Session: 1, Signal: int(sig.(unix.Signal))})
		}
	}()

	if err := s.ctl.Send(proto.Msg{Op: "exec", Exec: &ex}); err != nil {
		return fail(s, "exec: %v", err)
	}
	for {
		m, err := s.recv()
		if err != nil {
			return fail(s, "guest: %v", err)
		}
		switch m.Op {
		case "error":
			return fail(s, "%s", m.Error)
		case "exited":
			done := make(chan struct{})
			go func() { streams.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
			s.restore()
			return m.Code
		}
	}
}

// expect reads control messages until op (or an error) arrives.
func (s *shim) expect(op string) (proto.Msg, error) {
	for {
		m, err := s.recv()
		if err != nil {
			return m, err
		}
		if m.Op == op || m.Op == "error" {
			return m, nil
		}
	}
}

// recv reads one control message, or fails when Firecracker dies first.
func (s *shim) recv() (proto.Msg, error) {
	type res struct {
		m   proto.Msg
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var m proto.Msg
		err := s.ctl.Recv(&m)
		ch <- res{m, err}
	}()
	select {
	case r := <-ch:
		return r.m, r.err
	case <-s.fcDone:
		return proto.Msg{}, errors.New("the VM exited")
	}
}

// dialAgent connects to the agent's vsock port and sends hello, retrying
// until the agent listens (a cold guest is still booting).
func (s *shim) dialAgent(h proto.Hello, timeout time.Duration) (net.Conn, error) {
	uds := filepath.Join(s.hs.RunDir, "v.sock")
	deadline := time.Now().Add(timeout)
	for {
		c, err := proto.DialVsock(uds, proto.AgentPort, time.Second)
		if err == nil {
			b, _ := json.Marshal(h)
			if _, err = c.Write(append(b, '\n')); err == nil {
				return c, nil
			}
			c.Close()
		}
		select {
		case <-s.fcDone:
			return nil, errors.New("the VM exited while booting")
		default:
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no answer after %s: %v", timeout, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// serveFiles starts the 9P server on the vsock port the guest dials.
func (s *shim) serveFiles() (func(), error) {
	var exports []p9fs.Export
	for _, m := range s.hs.Mounts {
		exports = append(exports, p9fs.Export{Path: m.Path, RO: m.RO})
	}
	srv, err := p9fs.New(exports)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", filepath.Join(s.hs.RunDir, fmt.Sprintf("v.sock_%d", proto.P9Port)))
	if err != nil {
		srv.Close()
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = srv.Serve(c) }()
		}
	}()
	return func() { ln.Close(); srv.Close() }, nil
}

func (s *shim) restore() {
	if s.raw != nil {
		_ = term.Restore(0, s.raw)
		s.raw = nil
	}
}

// fail reports err on the shim's stderr (the terminal, or the backend log)
// with the tail of the guest console, and returns the exit code.
func fail(s *shim, format string, args ...any) int {
	msg := fmt.Sprintf(format, args...)
	nl := "\n"
	if s != nil {
		s.restore()
		if s.tty {
			nl = "\r\n"
		}
		if tail := s.serial.tail(4 << 10); tail != "" && (s.hs.Debug || s.fcExited()) {
			fmt.Fprintf(os.Stderr, "--- VM console ---%s%s%s", nl, crlf(tail, s.tty), nl)
		}
	}
	fmt.Fprintf(os.Stderr, "vm sandbox: %s%s", msg, nl)
	return 125
}

func crlf(s string, tty bool) string {
	if !tty {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && (i == 0 || s[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, s[i])
	}
	return string(out)
}
