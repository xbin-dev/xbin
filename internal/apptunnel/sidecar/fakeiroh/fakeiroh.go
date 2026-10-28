// Package fakeiroh stands in for xbin-iroh in tests (D204,
// plans/app-tunnel.md): `serve --fd N` speaks the same control protocol
// (internal/apptunnel/wire: the init line, commands, events) and hands
// xbind every stream the same way (a fresh socketpair, the 48-byte header,
// one end over the factory with SCM_RIGHTS), so xbind's side runs
// unchanged without iroh, Rust or a network. Its "iroh" is plain loopback
// TCP: each TCP connection is one iroh connection carrying one stream, and
// opens with a one-line preface naming the ALPN and the device's
// EndpointId, which the fake takes as iroh would have authenticated it:
//
//	→ XBIN-FAKEIROH/1 <alpn> <endpoint hex>\n
//	← OK <connection serial>\n      (then raw bytes both ways)
//	← NO <reason>\n                  (the hook refused; closed)
//
// Its ticket is "fakeiroh:<host:port>". `client` is the device side for
// scripts and the Swift app-live tool: every local connection becomes one
// such stream. TEST ONLY: nothing here authenticates anyone.
//
// hack/fakeiroh is the binary; tests may also run Serve in a re-exec'd
// test binary (internal/apptunnel/sidecar's TestMain).
//
// Test hooks (environment): FAKEIROH_LISTEN (the TCP address, default
// 127.0.0.1:0), FAKEIROH_EXIT=before-ready|after-ready (exit 3 there),
// FAKEIROH_ACK_DELAY (a duration before each ack).
package fakeiroh

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
)

// Preface is the first line of every fake-iroh connection.
const Preface = "XBIN-FAKEIROH/1"

// SelftestALPN is the fake's own probe, as the real sidecar's.
const SelftestALPN = "xbin/selftest/1"

// TicketPrefix starts a fake ticket; the rest is host:port.
const TicketPrefix = "fakeiroh:"

type live struct {
	endpoint string
	alpn     string
	c        net.Conn
}

type server struct {
	out     *json.Encoder
	outMu   sync.Mutex
	factory int
	ln      net.Listener
	self    string

	mu      sync.Mutex
	allow   map[string]bool
	pairing bool
	conns   map[uint64]*live
	serial  uint64

	ackDelay time.Duration
}

func (s *server) emit(e wire.Event) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_ = s.out.Encode(e)
}

// Serve runs `fakeiroh serve --fd N` (args after "serve") and returns the
// exit code.
func Serve(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "--fd" {
		fmt.Fprintln(stderr, "usage: fakeiroh serve --fd N")
		return 2
	}
	fd, err := strconv.Atoi(args[1])
	if err != nil || fd < 3 {
		fmt.Fprintln(stderr, "fakeiroh: bad --fd")
		return 2
	}
	s := &server{out: json.NewEncoder(stdout), factory: fd, conns: map[uint64]*live{}}
	if d, err := time.ParseDuration(os.Getenv("FAKEIROH_ACK_DELAY")); err == nil {
		s.ackDelay = d
	}
	fatal := func(msg string) int {
		fmt.Fprintln(stderr, "fakeiroh:", msg)
		s.emit(wire.Event{T: "error", Msg: msg, Fatal: true})
		return 1
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
		return fatal("factory: " + err.Error())
	}
	unix.CloseOnExec(fd)
	lines := bufio.NewScanner(stdin)
	lines.Buffer(make([]byte, 64<<10), 1<<20)
	if !lines.Scan() {
		return fatal("stdin closed before init")
	}
	var init wire.Init
	if err := json.Unmarshal(lines.Bytes(), &init); err != nil || init.T != "init" || init.V != 1 {
		return fatal(fmt.Sprintf("init: %q %v", lines.Text(), err))
	}
	seed, err := hex.DecodeString(init.SecretKey)
	if err != nil || len(seed) != ed25519.SeedSize {
		return fatal("init: secretKey is not a 32-byte hex seed")
	}
	s.self = hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	s.setAllow(init.Allow)
	s.pairing = init.Pairing
	if os.Getenv("FAKEIROH_EXIT") == "before-ready" {
		fmt.Fprintln(stderr, "fakeiroh: exiting before ready (FAKEIROH_EXIT)")
		return 3
	}
	addr := os.Getenv("FAKEIROH_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	if s.ln, err = net.Listen("tcp", addr); err != nil {
		return fatal("listen: " + err.Error())
	}
	go s.acceptLoop()
	s.emit(wire.Event{T: "ready", EndpointID: s.self, Ticket: TicketPrefix + s.ln.Addr().String(), HomeRelay: init.Relay.URL, Version: "fakeiroh"})
	fmt.Fprintln(stderr, "fakeiroh: ready on", s.ln.Addr())
	if os.Getenv("FAKEIROH_EXIT") == "after-ready" {
		fmt.Fprintln(stderr, "fakeiroh: exiting after ready (FAKEIROH_EXIT)")
		return 3
	}

	stop := make(chan string, 3)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sig; stop <- "signal" }()
	go func() { // xbind never writes to the factory: readable means closed
		b := make([]byte, 16)
		for {
			n, _, err := unix.Recvfrom(fd, b, 0)
			if err == unix.EINTR {
				continue
			}
			if n <= 0 || err != nil {
				stop <- "factory closed"
				return
			}
		}
	}()
	go func() {
		for lines.Scan() {
			if strings.TrimSpace(lines.Text()) == "" {
				continue
			}
			var c wire.Command
			if err := json.Unmarshal(lines.Bytes(), &c); err != nil {
				s.emit(wire.Event{T: "error", Msg: "command: " + err.Error()})
				continue
			}
			s.command(c)
		}
		stop <- "stdin closed"
	}()
	why := <-stop
	fmt.Fprintln(stderr, "fakeiroh: stopping:", why)
	s.ln.Close()
	return 0
}

func (s *server) setAllow(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allow = map[string]bool{}
	for _, id := range ids {
		s.allow[strings.ToLower(id)] = true
	}
	for _, l := range s.conns {
		if l.alpn == wire.ALPNTunnelName && !s.allow[l.endpoint] {
			l.c.Close()
		}
	}
}

func (s *server) command(c wire.Command) {
	switch c.T {
	case "allow":
		s.setAllow(c.Allow)
		go func() {
			time.Sleep(s.ackDelay)
			s.emit(wire.Event{T: "ack", Seq: c.Seq})
		}()
	case "pairing":
		s.mu.Lock()
		s.pairing = c.Open
		s.mu.Unlock()
		if c.Seq != 0 { // acknowledged like an allow list (wire.Command)
			go func() {
				time.Sleep(s.ackDelay)
				s.emit(wire.Event{T: "ack", Seq: c.Seq})
			}()
		}
	case "close":
		s.mu.Lock()
		for _, l := range s.conns {
			if l.endpoint == strings.ToLower(c.Endpoint) {
				l.c.Close()
			}
		}
		s.mu.Unlock()
	case "status":
		s.mu.Lock()
		ev := wire.Event{T: "status"}
		for n, l := range s.conns {
			ev.Conns = append(ev.Conns, wire.ConnInfo{Endpoint: l.endpoint, Conn: n, Path: "direct"})
		}
		s.mu.Unlock()
		s.emit(ev)
	case "selftest":
		go s.selftest()
	default:
		s.emit(wire.Event{T: "error", Msg: "unknown command " + strconv.Quote(c.T)})
	}
}

func (s *server) selftest() {
	t0 := time.Now()
	c, _, err := dial(s.ln.Addr().String(), SelftestALPN, s.self)
	if err != nil {
		s.emit(wire.Event{T: "selftest", Error: err.Error()})
		return
	}
	c.Close()
	s.emit(wire.Event{T: "selftest", Reachable: true, Path: "direct", RTTMs: max(1, int(time.Since(t0).Milliseconds()))})
}

func (s *server) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serveConn(c)
	}
}

func (s *server) admits(alpn, endpoint string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch alpn {
	case wire.ALPNTunnelName:
		return s.allow[endpoint]
	case wire.ALPNPairName:
		return s.pairing
	case SelftestALPN:
		return endpoint == s.self
	}
	return false
}

func (s *server) serveConn(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	_ = c.SetReadDeadline(time.Time{})
	f := strings.Fields(line)
	if err != nil || len(f) != 3 || f[0] != Preface {
		c.Close()
		return
	}
	alpn, endpoint := f[1], strings.ToLower(f[2])
	raw, err := hex.DecodeString(endpoint)
	if err != nil || len(raw) != 32 {
		fmt.Fprintf(c, "NO bad endpoint\n")
		c.Close()
		return
	}
	if !s.admits(alpn, endpoint) {
		fmt.Fprintf(c, "NO not allowed\n")
		c.Close()
		s.emit(wire.Event{T: "reject", Endpoint: endpoint, ALPN: alpn})
		return
	}
	if alpn == SelftestALPN {
		fmt.Fprintf(c, "OK 0\n")
		c.Close()
		return
	}
	code := wire.ALPNTunnel
	if alpn == wire.ALPNPairName {
		code = wire.ALPNPair
	}
	s.mu.Lock()
	s.serial++
	n := s.serial
	s.conns[n] = &live{endpoint: endpoint, alpn: alpn, c: c}
	s.mu.Unlock()
	s.emit(wire.Event{T: "conn", Endpoint: endpoint, Conn: n, Open: true, Path: "direct", ALPN: alpn})
	defer func() {
		s.mu.Lock()
		delete(s.conns, n)
		s.mu.Unlock()
		s.emit(wire.Event{T: "conn", Endpoint: endpoint, Conn: n})
	}()
	h := wire.Header{ALPN: code, Conn: n}
	copy(h.Endpoint[:], raw)
	mine, err := s.handOver(h)
	if err != nil {
		fmt.Fprintf(c, "NO %v\n", err)
		c.Close()
		return
	}
	fmt.Fprintf(c, "OK %d\n", n)
	splice(c, br, mine)
}

// handOver makes the socketpair, writes the header on our end and sends
// the other to xbind.
func (s *server) handOver(h wire.Header) (net.Conn, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fds[0]), "fakeiroh-stream")
	mine, err := net.FileConn(f)
	f.Close()
	if err != nil {
		unix.Close(fds[1])
		return nil, err
	}
	if _, err := mine.Write(h.Encode()); err != nil {
		mine.Close()
		unix.Close(fds[1])
		return nil, err
	}
	err = unix.Sendmsg(s.factory, []byte{'c'}, unix.UnixRights(fds[1]), nil, 0)
	unix.Close(fds[1])
	if err != nil {
		mine.Close()
		return nil, fmt.Errorf("handing the stream to xbind: %w", err)
	}
	return mine, nil
}

// splice copies both ways; each side's EOF becomes a half-close of the
// other, and the stream ends when both are done.
func splice(a net.Conn, ar io.Reader, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	half := func(dst net.Conn, src io.Reader) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go half(b, ar)
	go half(a, b)
	wg.Wait()
	a.Close()
	b.Close()
}

// dial opens one fake-iroh stream: the preface, then the answer.
func dial(addr, alpn, endpoint string) (net.Conn, uint64, error) {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, 0, err
	}
	if _, err := fmt.Fprintf(c, "%s %s %s\n", Preface, alpn, endpoint); err != nil {
		c.Close()
		return nil, 0, err
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := readLine(c)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return nil, 0, err
	}
	if rest, ok := strings.CutPrefix(line, "OK "); ok {
		n, _ := strconv.ParseUint(rest, 10, 64)
		return c, n, nil
	}
	c.Close()
	return nil, 0, fmt.Errorf("fakeiroh: refused: %s", strings.TrimPrefix(line, "NO "))
}

// readLine reads up to '\n' a byte at a time, so nothing after it is
// buffered away from the caller.
func readLine(r io.Reader) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for len(b) < 256 {
		if _, err := io.ReadFull(r, one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return string(b), nil
		}
		b = append(b, one[0])
	}
	return "", errors.New("fakeiroh: preface answer too long")
}

// Dial opens one stream to a fake ticket as the device endpoint (hex), on
// alpn; the connection serial the server gave it comes back too.
func Dial(ticket, alpn, endpoint string) (net.Conn, uint64, error) {
	addr, ok := strings.CutPrefix(ticket, TicketPrefix)
	if !ok {
		return nil, 0, fmt.Errorf("fakeiroh: %q is not a fake ticket", ticket)
	}
	return dial(addr, alpn, endpoint)
}

// Client runs `fakeiroh client --ticket T --alpn A --listen tcp:H:P|unix:PATH
// [--endpoint HEX]`: each local connection becomes one stream. The device
// EndpointId is --endpoint, else derived from stdin's optional first line
// {"secretKey":"<hex seed>"}, else random. It prints one line,
// {"t":"listening","endpointId":…,"addr":…}, then an error line per refusal.
func Client(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var ticket, alpn, listen, endpoint string
	alpn = wire.ALPNTunnelName
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			fmt.Fprintln(stderr, "fakeiroh client: "+args[i]+" needs a value")
			return 2
		}
		switch args[i] {
		case "--ticket":
			ticket = args[i+1]
		case "--alpn":
			alpn = args[i+1]
		case "--listen":
			listen = args[i+1]
		case "--endpoint":
			endpoint = args[i+1]
		default:
			fmt.Fprintln(stderr, "fakeiroh client: unknown argument "+args[i])
			return 2
		}
		i++
	}
	if ticket == "" || listen == "" {
		fmt.Fprintln(stderr, "usage: fakeiroh client --ticket T --alpn A --listen tcp:H:P|unix:PATH [--endpoint HEX]")
		return 2
	}
	if endpoint == "" {
		var in struct {
			SecretKey string `json:"secretKey"`
		}
		if b, err := bufio.NewReader(stdin).ReadBytes('\n'); len(b) > 0 && (err == nil || err == io.EOF) {
			_ = json.Unmarshal(b, &in)
		}
		seed, _ := hex.DecodeString(in.SecretKey)
		if len(seed) != ed25519.SeedSize {
			seed = make([]byte, ed25519.SeedSize)
			_, _ = rand.Read(seed)
		}
		endpoint = hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	}
	network, addr, ok := strings.Cut(listen, ":")
	if !ok || (network != "tcp" && network != "unix") {
		fmt.Fprintln(stderr, "fakeiroh client: --listen is tcp:HOST:PORT or unix:PATH")
		return 2
	}
	if network == "unix" {
		_ = os.Remove(addr)
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		fmt.Fprintln(stderr, "fakeiroh client:", err)
		return 1
	}
	out := json.NewEncoder(stdout)
	var outMu sync.Mutex
	say := func(v map[string]any) {
		outMu.Lock()
		defer outMu.Unlock()
		_ = out.Encode(v)
	}
	say(map[string]any{"t": "listening", "endpointId": endpoint, "addr": ln.Addr().String()})
	for {
		local, err := ln.Accept()
		if err != nil {
			return 1
		}
		go func() {
			remote, _, err := Dial(ticket, alpn, endpoint)
			if err != nil {
				say(map[string]any{"t": "error", "msg": err.Error()})
				local.Close()
				return
			}
			splice(remote, remote, local)
		}()
	}
}
