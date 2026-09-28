package main

// fake_tty_test.go — the fake backend's terminals (TEST ONLY; see
// fake_backend_test.go): host pseudo-terminals opened with the standard
// library (Linux: /dev/ptmx), served on the /ws/term wire with the SDK's
// sdk/ws — what the runtime's TTY WebSocket speaks, so the manager's relay
// is the same call either way. Where the host has none, the fake offers no
// `tty`.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/ws"
)

func (s *fkSandbox) RelayTTY(w http.ResponseWriter, r *http.Request, execID string, o xbin.TTYOptions) {
	if !s.f.hasCap("tty") {
		xbin.WriteSandboxError(w, fkErr(http.StatusNotImplemented, "unsupported", "no terminals here"))
		return
	}
	_, e, err := s.exec("tty", execID)
	if err != nil {
		xbin.WriteSandboxError(w, err)
		return
	}
	tty := e.info.TTY
	s.f.mu.Unlock()
	if !tty {
		xbin.WriteSandboxError(w, fkErr(http.StatusBadRequest, "invalid", "this exec has no terminal (started without tty)"))
		return
	}
	s.serve(w, r, e, o)
}

func (s *fkSandbox) RelayNewTTY(w http.ResponseWriter, r *http.Request, o xbin.TTYStart) {
	if !s.f.hasCap("tty") {
		xbin.WriteSandboxError(w, fkErr(http.StatusNotImplemented, "unsupported", "no terminals here"))
		return
	}
	if !ws.IsUpgrade(r) {
		xbin.WriteSandboxError(w, fkErr(http.StatusBadRequest, "invalid", "a terminal is a WebSocket upgrade"))
		return
	}
	q := xbin.ExecRequest{Cmd: o.Cmd, Cwd: o.Cwd, TTY: true, Rows: o.Rows, Cols: o.Cols, Label: "terminal", UID: o.UID, GID: o.GID, ForUser: o.ForUser}
	if q.Cmd == "" {
		q.Argv = []string{"/bin/sh", "-l"} // the sandbox's shell, a login one
	}
	e, err := s.launch(q)
	if err != nil {
		xbin.WriteSandboxError(w, err)
		return
	}
	s.serve(w, r, e, xbin.TTYOptions{SessionID: o.SessionID, SandboxID: o.SandboxID, ForUser: o.ForUser})
}

// serve speaks the /ws/term wire for a tty exec: the session frame, the
// ring from its oldest byte and then live output as binary frames, the exit
// frame when the command has ended and all its output is out. From the
// client: keystrokes (binary), resize and ping. A client that leaves
// doesn't end the command.
func (s *fkSandbox) serve(w http.ResponseWriter, r *http.Request, e *fkExec, o xbin.TTYOptions) {
	c, err := ws.Upgrade(w, r, &ws.UpgradeOptions{MaxMessageSize: 1 << 20,
		Error: func(w http.ResponseWriter, _ *http.Request, status int, reason string) {
			xbin.WriteSandboxError(w, fkErr(status, "invalid", "%s", reason))
		}})
	if err != nil {
		return
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term := func() *os.File { // the terminal while the command runs
		s.f.mu.Lock()
		defer s.f.mu.Unlock()
		if e.State != "running" {
			return nil
		}
		return e.pty
	}
	hello, _ := json.Marshal(map[string]any{"op": "session", "id": orStr(o.SessionID, e.info.ID), "sandbox": orStr(o.SandboxID, s.name), "echoAck": false})
	if c.WriteMessage(ws.TextMessage, hello) != nil {
		return
	}
	go func() {
		defer cancel()
		for {
			typ, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if typ == ws.BinaryMessage {
				if f := term(); f != nil {
					_, _ = f.Write(data)
				}
				continue
			}
			var ctl struct {
				Op   string          `json:"op"`
				Cols int             `json:"cols"`
				Rows int             `json:"rows"`
				T    json.RawMessage `json:"t"`
			}
			if json.Unmarshal(data, &ctl) != nil {
				continue
			}
			switch ctl.Op { // anything else is ignored
			case "resize":
				if f := term(); f != nil && ctl.Cols > 0 && ctl.Rows > 0 {
					_ = fkSetSize(f, ctl.Rows, ctl.Cols)
				}
			case "ping":
				pong, _ := json.Marshal(map[string]any{"op": "pong", "t": ctl.T})
				_ = c.WriteMessage(ws.TextMessage, pong)
			}
		}
	}()
	for since := int64(0); !e.ring.ended(since); {
		_, end, _, _, data := e.ring.read(ctx, since, 64<<10, 30*time.Second)
		if ctx.Err() != nil {
			return // the client left; the command runs on
		}
		if len(data) > 0 && c.WriteMessage(ws.BinaryMessage, data) != nil {
			return
		}
		since = end
	}
	<-e.done
	s.f.mu.Lock()
	exit := map[string]any{"op": "exit", "code": e.info.ExitCode} // null when a signal ended it
	if e.info.Signal != "" {
		exit["signal"] = e.info.Signal
	}
	s.f.mu.Unlock()
	b, _ := json.Marshal(exit)
	_ = c.WriteMessage(ws.TextMessage, b)
}

// Linux's terminal ioctls (the generic numbers: amd64, arm64, 386, arm,
// riscv64, loong64) — literal, so the file still builds elsewhere.
const (
	fkTIOCGPTN   = 0x80045430
	fkTIOCSPTLCK = 0x40045431
	fkTIOCSWINSZ = 0x5414
)

// fkHasPTY: the host gives the fake terminals.
var fkHasPTY = sync.OnceValue(func() bool {
	switch runtime.GOARCH {
	case "amd64", "arm64", "386", "arm", "riscv64", "loong64":
	default:
		return false
	}
	master, slave, err := fkOpenPTY(24, 80)
	if err != nil {
		return false
	}
	master.Close()
	slave.Close()
	return true
})

// fkOpenPTY opens a pseudo-terminal: its master (ours: pollable, so a Close
// ends a blocked read) and its slave (the command's), rows × cols.
func fkOpenPTY(rows, cols int) (master, slave *os.File, err error) {
	if runtime.GOOS != "linux" {
		return nil, nil, errors.New("terminals need Linux")
	}
	if master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0); err != nil {
		return nil, nil, err
	}
	var unlock int32
	var n uint32
	if err = fkIoctl(master, fkTIOCSPTLCK, unsafe.Pointer(&unlock)); err == nil {
		err = fkIoctl(master, fkTIOCGPTN, unsafe.Pointer(&n))
	}
	if err == nil {
		slave, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err == nil {
		err = fkSetSize(master, rows, cols)
	}
	if err != nil {
		master.Close()
		if slave != nil {
			slave.Close()
		}
		return nil, nil, err
	}
	return master, slave, nil
}

// fkSetSize sets a terminal's window (0: 24 × 80); the command gets SIGWINCH.
func fkSetSize(f *os.File, rows, cols int) error {
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}
	ws := struct{ row, col, x, y uint16 }{uint16(min(rows, 0xffff)), uint16(min(cols, 0xffff)), 0, 0}
	return fkIoctl(f, fkTIOCSWINSZ, unsafe.Pointer(&ws))
}

// fkIoctl without f.Fd(), which would make the file blocking.
func fkIoctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}
