// Package proto is the wire contract between the VM sandbox's host shim
// (`bx __vm-host`, PID 1 of the namespace sandbox that holds Firecracker) and
// the guest agent (xbin-vmagent, PID 1 inside the VM). plans/vm-sandbox.md.
//
// Everything rides the VM's vsock, whose host side is a unix socket
// (Firecracker's own, or vhost-device-vsock's under emulation — the same
// protocol): the shim reaches the agent by connecting to the VM's vsock UDS
// and sending "CONNECT <AgentPort>\n", once the agent has called ReadyPort;
// the guest reaches the shim's listeners (that call, the FUSE file server,
// the xbind gateway for backends) by dialing CID 2, which the VMM forwards
// to "<uds>_<port>".
//
// Every connection to the agent starts with one JSON Hello line. A "ctl"
// connection then carries JSON Msg lines in both directions; a "stream"
// connection carries one session's raw bytes (a PTY, or one of stdin/
// stdout/stderr); a "file" connection carries one file operation (file.go).
// Sessions are numbered by the shim from 1, so one VM can run more than one
// process — the terminal is session 1 today; a tile sandbox's execs are
// sessions 2 and up (plans/tile-sandbox-runtime.md §2).
//
// The same wire serves a namespace-mode tile sandbox, whose agent (the same
// exec core, internal/sandbox/agentcore) xbind reaches through a connection
// factory instead of vsock. Every field added since the first version is
// omitempty and ignored by an older peer, so backends and terminals, which
// send none of them, behave as they always have.
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
	FilesPort   = 564  // host: the FUSE file server (one connection per mount)
	GatewayPort = 1025 // host: xbind's gateway socket (backends)
	// ReadyPort (host): the agent calls it once it listens on AgentPort, and
	// the shim connects to the agent only after that call. An emulated VM's
	// vsock backend can wedge for good on a host connection made before the
	// guest's vsock driver is up.
	ReadyPort = 1026
)

// Line bounds (plans/tile-sandbox-runtime.md §2.6): nobody on either side of
// the wire trusts the other to keep its lines short.
const (
	MaxHello   = 4 << 10  // a Hello line, every connection's first
	MaxEvent   = 64 << 10 // an event line from the agent (but "dump", which only a shim asks for)
	MaxCommand = 1 << 20  // a control line to the agent ("exec" carries argv and env)
	MaxResult  = 2 << 20  // a FileResult line (a listing's entries)
)

// ErrLineTooLong is RecvMax's and ReadHelloMax's answer to a line past its
// bound; the connection is not usable after it.
var ErrLineTooLong = errors.New("proto: line too long")

// Hello opens every connection to the agent.
type Hello struct {
	Kind    string  `json:"kind"`              // "ctl" | "stream" | "listen" (a connection for Exec.Listen) | "file"
	Session int     `json:"session,omitempty"` // stream: the session it belongs to
	Stream  string  `json:"stream,omitempty"`  // stream: "pty" | "stdin" | "stdout" | "stderr"
	File    *FileOp `json:"file,omitempty"`    // file: the operation (file.go)
}

// Config turns a booted (or template-restored) guest into this sandbox. The
// guest holds nothing tile-specific before it arrives.
type Config struct {
	Time     int64    `json:"time"` // host wall clock, unix nanoseconds
	Hostname string   `json:"hostname,omitempty"`
	Net      *Net     `json:"net,omitempty"` // nil: no network interface (loopback only)
	Root     Root     `json:"root"`
	Mounts   []Mount  `json:"mounts,omitempty"` // file mounts, parents first
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

// FilesHello opens a FilesPort connection: the export it mounts. FUSE
// messages follow, each framed by its own leading length field.
type FilesHello struct {
	Path string `json:"path"`
}

// Mount is one host path the guest mounts (FUSE, served by the shim) at the
// same path.
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

	// CwdStrict makes a missing (or non-directory) Cwd an error; without
	// it the session falls back to "/".
	CwdStrict bool `json:"cwdStrict,omitempty"`
	// UID and GID run the session as that user and group, with no
	// supplementary groups; one left nil keeps the agent's own. An id the
	// sandbox doesn't map is an error.
	UID *uint32 `json:"uid,omitempty"`
	GID *uint32 `json:"gid,omitempty"`
	// Merge sends stderr to the stdout stream: one combined stream, and no
	// "stderr" stream expected.
	Merge bool `json:"merge,omitempty"`
	// NoStdin gives the process /dev/null; no "stdin" stream is expected.
	NoStdin bool `json:"noStdin,omitempty"`
	// NoSync skips the flush before "exited" (a resident sandbox isn't
	// killed when a session ends).
	NoSync bool `json:"noSync,omitempty"`
}

// Msg is one control message. Host → guest ops: "config", "exec",
// "resize", "signal" (Group: the session's whole process group), "sync"
// (the VM is about to be killed: hang up session Session — none for 0 —
// and flush the disks), "dump" (describe what the guest is doing).
// Guest → host ops: "ready" (answers config; an agent that takes no config
// sends it as soon as a ctl connects), "started" (Pid), "listening",
// "exited" (Code, and Signal when a signal ended it), "synced", "dump"
// (answers dump, in Dump), "error" (Session names the session it ended,
// 0 for none).
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
	Dump    string  `json:"dump,omitempty"` // dump: the guest's report, plain text
	// Group makes "signal" kill(-pgid): every session leads its own
	// process group (setsid), so the group is its pid's. It still reaches
	// the group for 15 s after the session's process ended — the
	// members that outlived it (a timeout's KILL after its grace).
	Group bool `json:"group,omitempty"`
	// Pid is "started"'s process id, in the sandbox's pid namespace.
	Pid int `json:"pid,omitempty"`
	// On "exited", Signal > 0 names the signal that ended the session
	// (Code still says 128+Signal, for older readers).
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

// Recv reads one message line into v, however long.
func (c *Conn) Recv(v any) error {
	line, err := readLine(c.r, 0)
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}

// RecvMax is Recv with the line bounded to max bytes: past it the
// connection is closed (nothing after a cut line can be trusted to be in
// step) and ErrLineTooLong returned.
func (c *Conn) RecvMax(v any, max int) error {
	line, err := readLine(c.r, max)
	if err == ErrLineTooLong {
		c.c.Close()
		return err
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}

// Reader is the connection's buffered reader: what follows a line read by
// Recv (a file operation's data frames) is read from it.
func (c *Conn) Reader() *bufio.Reader { return c.r }

// Writer is the raw connection, for what follows a line sent by Send.
func (c *Conn) Writer() io.Writer { return c.c }

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close() }

// ReadHello reads the first line of an accepted agent connection. The reader
// it returns holds anything buffered past the line.
func ReadHello(c io.Reader) (Hello, *bufio.Reader, error) {
	return ReadHelloMax(c, 0)
}

// ReadHelloMax is ReadHello with the line bounded to max bytes (0: no
// bound); past it, ErrLineTooLong (the caller drops the connection).
func ReadHelloMax(c io.Reader, max int) (Hello, *bufio.Reader, error) {
	r := bufio.NewReader(c)
	var h Hello
	line, err := readLine(r, max)
	if err != nil {
		return h, nil, err
	}
	if err := json.Unmarshal(line, &h); err != nil {
		return h, nil, fmt.Errorf("bad hello: %w", err)
	}
	return h, r, nil
}

// readLine reads up to and including '\n', holding at most max bytes (0: no
// bound). A line cut short by EOF is io.ErrUnexpectedEOF.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		if max > 0 && len(line)+len(frag) > max {
			return nil, ErrLineTooLong
		}
		line = append(line, frag...)
		switch {
		case err == nil:
			return line, nil
		case err == bufio.ErrBufferFull:
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
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
