// Package wire is the contract between xbind and its iroh sidecar,
// xbin-iroh (D204, native/spec/tunnel.md, plans/app-tunnel.md): the header
// the sidecar writes at the start of every stream it hands xbind, and the
// JSON-lines control messages on the sidecar's stdin and stdout. The Rust
// side (sidecar/xbin-iroh) mirrors it; the tests here pin the bytes.
package wire

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// HeaderLen is the stream header's size: magic(4) version(1) alpn(1)
// reserved(2) remote EndpointId(32) connection serial(8).
const HeaderLen = 48

// Magic opens every stream header.
var Magic = [4]byte{'X', 'B', 'I', '1'}

// Version is the header version this xbind reads.
const Version = 1

// ALPN codes in the header.
const (
	ALPNTunnel byte = 1 // "xbin/tunnel/1": a paired device's traffic
	ALPNPair   byte = 2 // "xbin/pair/1": a pairing
)

// ALPN names, as iroh negotiates them.
const (
	ALPNTunnelName = "xbin/tunnel/1"
	ALPNPairName   = "xbin/pair/1"
)

// Header is what the sidecar says about a stream before splicing it: the
// ALPN its connection negotiated and the remote EndpointId iroh
// authenticated. xbind treats the EndpointId as a claim — it enters the
// Noise prologue and is checked against the device record, never trusted
// alone.
type Header struct {
	ALPN     byte
	Endpoint [32]byte
	Conn     uint64 // the sidecar's serial of the iroh connection (streams of one connection share it)
}

// EndpointHex is the EndpointId as iroh prints it (64 lowercase hex).
func (h Header) EndpointHex() string { return hex.EncodeToString(h.Endpoint[:]) }

// Encode returns the header's 48 bytes.
func (h Header) Encode() []byte {
	b := make([]byte, HeaderLen)
	copy(b[0:4], Magic[:])
	b[4] = Version
	b[5] = h.ALPN
	copy(b[8:40], h.Endpoint[:])
	binary.BigEndian.PutUint64(b[40:48], h.Conn)
	return b
}

// ErrHeader is a header xbind refuses.
var ErrHeader = errors.New("app tunnel: bad stream header")

// ReadHeader reads and checks one header: the magic, the version, a known
// ALPN and zero reserved bytes, strictly.
func ReadHeader(r io.Reader) (Header, error) {
	var b [HeaderLen]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return Header{}, err
	}
	return ParseHeader(b[:])
}

// ParseHeader checks and decodes a header's bytes.
func ParseHeader(b []byte) (Header, error) {
	if len(b) != HeaderLen {
		return Header{}, fmt.Errorf("%w: %d bytes", ErrHeader, len(b))
	}
	if [4]byte(b[0:4]) != Magic {
		return Header{}, fmt.Errorf("%w: magic", ErrHeader)
	}
	if b[4] != Version {
		return Header{}, fmt.Errorf("%w: version %d", ErrHeader, b[4])
	}
	if b[5] != ALPNTunnel && b[5] != ALPNPair {
		return Header{}, fmt.Errorf("%w: alpn %d", ErrHeader, b[5])
	}
	if b[6] != 0 || b[7] != 0 {
		return Header{}, fmt.Errorf("%w: reserved", ErrHeader)
	}
	var h Header
	h.ALPN = b[5]
	copy(h.Endpoint[:], b[8:40])
	h.Conn = binary.BigEndian.Uint64(b[40:48])
	return h, nil
}

// ---- control: xbind → sidecar (stdin, one JSON object per line) ----

// Relay is the sidecar's relay configuration.
type Relay struct {
	Mode  string `json:"mode"`            // "n0" | "custom" | "disabled" (tests: direct only)
	URL   string `json:"url,omitempty"`   // custom
	Token string `json:"token,omitempty"` // custom: iroh-relay access.shared_token
}

// Init is the first stdin line. SecretKey is the endpoint's Ed25519 seed,
// hex — the only place it travels (never argv, never env).
type Init struct {
	T         string   `json:"t"` // "init"
	V         int      `json:"v"` // 1
	SecretKey string   `json:"secretKey"`
	Relay     Relay    `json:"relay"`
	Allow     []string `json:"allow"`   // EndpointIds (hex) admitted on xbin/tunnel/1
	Pairing   bool     `json:"pairing"` // xbin/pair/1 admitted
}

// Command is every later stdin line: T is "allow" (Allow replaces the whole
// list; Seq is acknowledged; a tunnel/1 connection whose endpoint left the
// list is closed), "pairing" (Open; a non-zero Seq is acknowledged too:
// allow and pairing share xbind's one sequence), "close" (Endpoint: drop its
// connections — the device was revoked), "status", "selftest". Lines sent
// before ready are read once the endpoint is bound, in order.
type Command struct {
	T        string   `json:"t"`
	Seq      uint64   `json:"seq,omitempty"`
	Allow    []string `json:"allow,omitempty"`
	Open     bool     `json:"open,omitempty"`
	Endpoint string   `json:"endpoint,omitempty"`
}

// ---- control: sidecar → xbind (stdout, one JSON object per line) ----

// Event is one stdout line. T is:
//   - "ready": EndpointID, Ticket (relay-only EndpointTicket), HomeRelay, Version.
//     It comes again, with the new ticket, when n0's preset moves the home
//     relay. With relays disabled (tests) the ticket carries the endpoint's
//     loopback sockets instead.
//   - "ack": Seq (an allow list is in force)
//   - "status": HomeRelay, RelayConnected, Conns — on "status", and whenever
//     the home relay or its connection changes
//   - "conn": Endpoint, Conn, Open (a connection opened or closed), Path, ALPN
//   - "reject": Endpoint, ALPN (refused by the after-handshake hook;
//     rate-limited: one per endpoint and ALPN a minute, 20 a minute in all)
//   - "selftest": Reachable, Path, RTTMs, Error (a throwaway endpoint dialed
//     this one through its home relay only, on the sidecar's own
//     "xbin/selftest/1", which it admits from that probe alone and never
//     hands to xbind)
//   - "error": Msg, Fatal (fatal: the sidecar exits)
type Event struct {
	T              string     `json:"t"`
	EndpointID     string     `json:"endpointId,omitempty"`
	Ticket         string     `json:"ticket,omitempty"`
	HomeRelay      string     `json:"homeRelay,omitempty"`
	Version        string     `json:"version,omitempty"`
	Seq            uint64     `json:"seq,omitempty"`
	RelayConnected bool       `json:"relayConnected,omitempty"`
	Conns          []ConnInfo `json:"conns,omitempty"`
	Endpoint       string     `json:"endpoint,omitempty"`
	Conn           uint64     `json:"conn,omitempty"`
	Open           bool       `json:"open,omitempty"`
	Path           string     `json:"path,omitempty"` // "direct" | "relay"
	ALPN           string     `json:"alpn,omitempty"`
	Reachable      bool       `json:"reachable,omitempty"`
	RTTMs          int        `json:"rttMs,omitempty"`
	Error          string     `json:"error,omitempty"`
	Msg            string     `json:"msg,omitempty"`
	Fatal          bool       `json:"fatal,omitempty"`
}

// ConnInfo is one live iroh connection in a status event.
type ConnInfo struct {
	Endpoint string `json:"endpoint"`
	Conn     uint64 `json:"conn"`
	Path     string `json:"path"`
	RTTMs    int    `json:"rttMs"`
}
