// Package proto is the wire contract between the VM sandbox's host shim
// (`bx __vm-host`, PID 1 of the namespace sandbox that holds Firecracker) and
// the guest agent (xbin-vmagent, PID 1 inside the VM). plans/vm-sandbox.md.
//
// Everything rides Firecracker's vsock, whose host side is a unix socket:
// the shim reaches the agent by connecting to the VM's vsock UDS and sending
// "CONNECT <AgentPort>\n"; the guest reaches the shim's listeners (the 9P
// file server, the xbind gateway for backends) by dialing CID 2, which
// Firecracker forwards to "<uds>_<port>".
//
// Every connection to the agent starts with one JSON Hello line. A "ctl"
// connection then carries JSON Msg lines in both directions; a "stream"
// connection carries one session's raw bytes (a PTY, or one of stdin/
// stdout/stderr). Sessions are numbered by the shim from 1, so one VM can run
// more than one process — the terminal is session 1 today; exec into a
// running VM (more tabs, backend-managed sub-sandboxes) reuses the same shape.
package proto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Vsock ports.
const (
	AgentPort   = 1024 // guest: the agent's listener (control + streams)
	P9Port      = 564  // host: the 9P2000.L file server
	GatewayPort = 1025 // host: xbind's gateway socket (backends)
)

// Hello opens every connection to the agent.
type Hello struct {
	Kind    string `json:"kind"`              // "ctl" | "stream" | "listen" (a connection for Exec.Listen)
	Session int    `json:"session,omitempty"` // stream: the session it belongs to
	Stream  string `json:"stream,omitempty"`  // stream: "pty" | "stdin" | "stdout" | "stderr"
}

// Config turns a booted (or template-restored) guest into this sandbox. The
// guest holds nothing tile-specific before it arrives.
type Config struct {
	Time     int64    `json:"time"` // host wall clock, unix nanoseconds
	Hostname string   `json:"hostname,omitempty"`
	Net      *Net     `json:"net,omitempty"` // nil: no network interface (loopback only)
	Root     Root     `json:"root"`
	Mounts   []Mount  `json:"mounts,omitempty"` // 9P mounts, parents first
	Local    []string `json:"local,omitempty"`  // guest-local tmpfs dirs at host paths (a backend's run dir: its sockets)
	Env      []string `json:"env,omitempty"`    // extra environment for every session
}

// Net is the guest side of the routed link into the sandbox's netns.
type Net struct {
	Addr  string `json:"addr"`  // guest address, e.g. "10.0.2.15"
	Gw    string `json:"gw"`    // on-link gateway, e.g. "10.0.2.2"
	GwMAC string `json:"gwMac"` // the host TAP's MAC: a permanent neighbour
	DNS   string `json:"dns"`   // nameserver for /etc/resolv.conf
	MTU   int    `json:"mtu,omitempty"`
}

// Root describes the guest root: an overlay over the read-only image.
type Root struct {
	Image     string `json:"image"`               // block device of the rootfs image, e.g. /dev/vda
	ImageType string `json:"imageType,omitempty"` // "erofs" (default) | "ext4"
	Upper     string `json:"upper,omitempty"`     // block device of the persistent upper ("" = tmpfs)
}

// Mount is one host path the guest mounts over 9P at the same path.
type Mount struct {
	Path string `json:"path"`
	RO   bool   `json:"ro,omitempty"`
	File bool   `json:"file,omitempty"` // a single file, bound from its export dir
}

// Exec starts one session.
type Exec struct {
	Session int      `json:"session"`
	Path    string   `json:"path,omitempty"` // the program (default: Argv[0] via PATH)
	Argv    []string `json:"argv"`
	Env     []string `json:"env,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	TTY     bool     `json:"tty,omitempty"`
	Rows    uint16   `json:"rows,omitempty"`
	Cols    uint16   `json:"cols,omitempty"`
	// Listen, if set, is a unix socket path the session's process will listen
	// on; the agent reports "listening" once it accepts, and bridges each
	// "listen" connection to it (backends: XBIN_SOCKET).
	Listen string `json:"listen,omitempty"`
	// Gateway, if set, is a unix socket path the agent serves inside the guest
	// and bridges to the host's GatewayPort (backends: XBIN_GATEWAY).
	Gateway string `json:"gateway,omitempty"`
}

// Msg is one control message. Host → guest ops: "config", "exec",
// "resize", "signal", "sync" (the VM is about to be killed: hang up session
// Session and flush the disks). Guest → host ops: "ready" (answers config),
// "started", "listening", "exited", "synced", "error".
type Msg struct {
	Op      string  `json:"op"`
	Config  *Config `json:"config,omitempty"`
	Exec    *Exec   `json:"exec,omitempty"`
	Session int     `json:"session,omitempty"`
	Rows    uint16  `json:"rows,omitempty"`
	Cols    uint16  `json:"cols,omitempty"`
	Signal  int     `json:"signal,omitempty"`
	Code    int     `json:"code,omitempty"` // exited: the exit status (128+sig when killed)
	Error   string  `json:"error,omitempty"`
}

// Conn is a line-delimited JSON channel over one connection (a unix socket
// on the host, a vsock *os.File in the guest).
type Conn struct {
	c io.ReadWriteCloser
	r *bufio.Reader
}

// NewConn wraps c; r is the reader that already consumed the Hello line (nil
// = start fresh).
func NewConn(c io.ReadWriteCloser, r *bufio.Reader) *Conn {
	if r == nil {
		r = bufio.NewReader(c)
	}
	return &Conn{c: c, r: r}
}

// Send writes one message line.
func (c *Conn) Send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.c.Write(append(b, '\n'))
	return err
}

// Recv reads one message line into v.
func (c *Conn) Recv(v any) error {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return json.Unmarshal(line, v)
}

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close() }

// ReadHello reads the first line of an accepted agent connection. The reader
// it returns holds anything buffered past the line.
func ReadHello(c io.Reader) (Hello, *bufio.Reader, error) {
	r := bufio.NewReader(c)
	var h Hello
	line, err := r.ReadBytes('\n')
	if err != nil {
		return h, nil, err
	}
	if err := json.Unmarshal(line, &h); err != nil {
		return h, nil, fmt.Errorf("bad hello: %w", err)
	}
	return h, r, nil
}

// DialVsock opens a host-initiated connection to a guest vsock port through
// Firecracker's vsock unix socket (the "CONNECT <port>" handshake).
func DialVsock(uds string, port int, timeout time.Duration) (net.Conn, error) {
	c, err := net.DialTimeout("unix", uds, timeout)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err != nil {
		c.Close()
		return nil, err
	}
	// The reply is "OK <host port>\n", read byte by byte so nothing past the
	// line is consumed from the stream.
	var line []byte
	var b [1]byte
	for {
		if _, err := c.Read(b[:]); err != nil {
			c.Close()
			return nil, fmt.Errorf("vsock connect %d: %w", port, err)
		}
		if b[0] == '\n' {
			break
		}
		line = append(line, b[0])
		if len(line) > 64 {
			c.Close()
			return nil, fmt.Errorf("vsock connect %d: bad reply", port)
		}
	}
	if !strings.HasPrefix(string(line), "OK ") {
		c.Close()
		return nil, fmt.Errorf("vsock connect %d: %q", port, line)
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}
