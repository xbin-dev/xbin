// Package noise is the xbin layer inside an app tunnel (D204,
// native/spec/tunnel.md §4–§5): Noise_IK_25519_ChaChaPoly_SHA256 on
// xbin/tunnel/1 and Noise_IKpsk2_25519_ChaChaPoly_SHA256 on xbin/pair/1, the
// device as initiator and xbind as responder, with the spec's prologue (both
// iroh EndpointIds), the pairing selector, the typed transport frames (data,
// rekey, close) and the confirmation code. It wraps github.com/flynn/noise so
// the library stays swappable; native/spec/tunnel-vectors.json pins the bytes.
package noise

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	fnoise "github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"
)

// Prologue labels (ASCII, no terminator), followed by the server and the
// device EndpointIds.
const (
	LabelTunnel = "xbin-tunnel-v1"
	LabelPair   = "xbin-pair-v1"
)

// Protocol names, as Noise spells them.
const (
	ProtocolTunnel = "Noise_IK_25519_ChaChaPoly_SHA256"
	ProtocolPair   = "Noise_IKpsk2_25519_ChaChaPoly_SHA256"
)

// HandshakeTimeout is how long xbind waits for a handshake (spec §4).
const HandshakeTimeout = 10 * time.Second

// SelectorLen is the pair/1 message-1 payload's size.
const SelectorLen = 16

// KeyLen is an X25519 key's, a psk's and an EndpointId's size.
const KeyLen = 32

var (
	// ErrLowOrder is an X25519 result of all zeros: a low-order point.
	ErrLowOrder = errors.New("noise: low-order point")
	// ErrHandshake is a handshake that didn't complete as the spec says.
	ErrHandshake = errors.New("noise: handshake failed")
)

// Key is an X25519 static key pair.
type Key struct {
	Private [KeyLen]byte
	Public  [KeyLen]byte
}

// GenerateKey makes a static key from rand (nil: crypto/rand).
func GenerateKey(r io.Reader) (Key, error) {
	if r == nil {
		r = rand.Reader
	}
	var priv [KeyLen]byte
	if _, err := io.ReadFull(r, priv[:]); err != nil {
		return Key{}, err
	}
	return KeyFromPrivate(priv[:])
}

// KeyFromPrivate rebuilds a key pair from its 32-byte private half.
func KeyFromPrivate(priv []byte) (Key, error) {
	if len(priv) != KeyLen {
		return Key{}, fmt.Errorf("noise: private key is %d bytes, want 32", len(priv))
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return Key{}, err
	}
	var k Key
	copy(k.Private[:], priv)
	copy(k.Public[:], pub)
	return k, nil
}

func (k Key) dh() fnoise.DHKey {
	return fnoise.DHKey{Private: append([]byte(nil), k.Private[:]...), Public: append([]byte(nil), k.Public[:]...)}
}

// dh25519 is X25519 that refuses an all-zero result explicitly (spec §4:
// a low-order point aborts the handshake on both sides), whatever the
// underlying library does about it.
type dh25519 struct{}

func (dh25519) GenerateKeypair(r io.Reader) (fnoise.DHKey, error) {
	return fnoise.DH25519.GenerateKeypair(r)
}

func (dh25519) DH(priv, pub []byte) ([]byte, error) {
	if len(pub) != KeyLen {
		return nil, ErrHandshake
	}
	out, err := curve25519.X25519(priv, pub)
	if err != nil {
		return nil, ErrLowOrder
	}
	var zero [KeyLen]byte
	if subtle.ConstantTimeCompare(out, zero[:]) == 1 {
		return nil, ErrLowOrder
	}
	return out, nil
}

func (dh25519) DHLen() int     { return KeyLen }
func (dh25519) DHName() string { return "25519" }

// Suite is the cipher suite both patterns use: 25519, ChaChaPoly, SHA256.
var Suite = fnoise.NewCipherSuite(dh25519{}, fnoise.CipherChaChaPoly, fnoise.HashSHA256)

// Prologue is label ‖ server EndpointId ‖ device EndpointId.
func Prologue(pair bool, server, device [KeyLen]byte) []byte {
	label := LabelTunnel
	if pair {
		label = LabelPair
	}
	p := make([]byte, 0, len(label)+2*KeyLen)
	p = append(p, label...)
	p = append(p, server[:]...)
	return append(p, device[:]...)
}

// Selector is SHA-256("xbin-pair-select-v1\n" ‖ code)[0:16]: how pair/1's
// message 1 names its pending pairing without carrying the code.
func Selector(code string) [SelectorLen]byte {
	h := sha256.Sum256([]byte("xbin-pair-select-v1\n" + code))
	var s [SelectorLen]byte
	copy(s[:], h[:SelectorLen])
	return s
}

// Config describes one side of one handshake.
type Config struct {
	Pair           bool         // pair/1 (IKpsk2) rather than tunnel/1 (IK)
	Static         Key          // this side's static key
	ServerKey      [KeyLen]byte // initiator: the server's static public key (the link's k)
	ServerEndpoint [KeyLen]byte // the server's EndpointId
	DeviceEndpoint [KeyLen]byte // the device's EndpointId
	PSK            []byte       // initiator, pair/1: the link's p (the responder sets it after message 1)
	Code           string       // initiator, pair/1: the link's c, for the selector
	Random         io.Reader    // the ephemeral's source (nil: crypto/rand); tests inject it
}

func (c Config) noiseConfig(initiator bool) fnoise.Config {
	nc := fnoise.Config{
		CipherSuite:   Suite,
		Random:        c.Random,
		Pattern:       fnoise.HandshakeIK,
		Initiator:     initiator,
		Prologue:      Prologue(c.Pair, c.ServerEndpoint, c.DeviceEndpoint),
		StaticKeypair: c.Static.dh(),
	}
	if initiator {
		nc.PeerStatic = append([]byte(nil), c.ServerKey[:]...)
	}
	if c.Pair {
		nc.PresharedKeyPlacement = 2
		if initiator {
			nc.PresharedKey = c.PSK
		}
	}
	return nc
}

// Session is a completed handshake: the two CipherStates and the handshake
// hash.
type Session struct {
	send, recv *fnoise.CipherState
	h          []byte
	peer       [KeyLen]byte
}

// HandshakeHash is h, Noise's channel binding.
func (s *Session) HandshakeHash() []byte { return append([]byte(nil), s.h...) }

// PeerStatic is the remote side's static public key, as the handshake
// authenticated it.
func (s *Session) PeerStatic() [KeyLen]byte { return s.peer }

// SAS is the confirmation code (spec §5): six digits, "012 345".
func (s *Session) SAS() string { return FormatSAS(SASNumber(s.h)) }

// SASOKM is HKDF-SHA256(ikm = h, salt = "", info = "xbin-sas-v1", L = 8).
func SASOKM(h []byte) []byte {
	okm, err := hkdf.Key(sha256.New, h, nil, "xbin-sas-v1", 8)
	if err != nil {
		panic(err) // 8 bytes from SHA-256 can't fail
	}
	return okm
}

// SASNumber is big-endian uint64(okm) mod 1 000 000.
func SASNumber(h []byte) uint32 {
	return uint32(binary.BigEndian.Uint64(SASOKM(h)) % 1_000_000)
}

// FormatSAS zero-pads and groups a code: 12345 → "012 345".
func FormatSAS(n uint32) string {
	s := fmt.Sprintf("%06d", n%1_000_000)
	return s[:3] + " " + s[3:]
}

// Initiator is the device side of a handshake.
type Initiator struct {
	cfg Config
	hs  *fnoise.HandshakeState
}

// NewInitiator prepares the device side. pair/1 needs PSK and Code.
func NewInitiator(cfg Config) (*Initiator, error) {
	if cfg.Pair && (len(cfg.PSK) != KeyLen || cfg.Code == "") {
		return nil, errors.New("noise: pair/1 needs the link's psk and code")
	}
	hs, err := fnoise.NewHandshakeState(cfg.noiseConfig(true))
	if err != nil {
		return nil, err
	}
	return &Initiator{cfg: cfg, hs: hs}, nil
}

// Message1 is the first handshake message (unframed): the selector as its
// payload for pair/1, empty for tunnel/1.
func (i *Initiator) Message1() ([]byte, error) {
	var payload []byte
	if i.cfg.Pair {
		sel := Selector(i.cfg.Code)
		payload = sel[:]
	}
	msg, _, _, err := i.hs.WriteMessage(nil, payload)
	return msg, err
}

// ReadMessage2 reads the responder's answer and completes the handshake.
func (i *Initiator) ReadMessage2(msg []byte) (*Session, error) {
	payload, cs1, cs2, err := i.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, fmt.Errorf("%w: message 2: %v", ErrHandshake, err)
	}
	if len(payload) != 0 || cs1 == nil {
		return nil, fmt.Errorf("%w: message 2 carries a payload", ErrHandshake)
	}
	return &Session{send: cs1, recv: cs2, h: i.hs.ChannelBinding(), peer: i.cfg.ServerKey}, nil
}

// Responder is xbind's side of a handshake.
type Responder struct {
	cfg  Config
	hs   *fnoise.HandshakeState
	peer [KeyLen]byte
}

// NewResponder prepares xbind's side; Static is the server's Noise key.
func NewResponder(cfg Config) (*Responder, error) {
	hs, err := fnoise.NewHandshakeState(cfg.noiseConfig(false))
	if err != nil {
		return nil, err
	}
	return &Responder{cfg: cfg, hs: hs}, nil
}

// ReadMessage1 reads the device's first message and returns its static key
// and payload: the selector for pair/1 (exactly 16 bytes), nothing for
// tunnel/1.
func (r *Responder) ReadMessage1(msg []byte) (peer [KeyLen]byte, payload []byte, err error) {
	payload, _, _, err = r.hs.ReadMessage(nil, msg)
	if err != nil {
		return peer, nil, fmt.Errorf("%w: message 1: %v", ErrHandshake, err)
	}
	want := 0
	if r.cfg.Pair {
		want = SelectorLen
	}
	if len(payload) != want {
		return peer, nil, fmt.Errorf("%w: message 1 payload is %d bytes, want %d", ErrHandshake, len(payload), want)
	}
	copy(r.peer[:], r.hs.PeerStatic())
	return r.peer, payload, nil
}

// SetPSK gives a pair/1 responder the psk its selector named.
func (r *Responder) SetPSK(psk []byte) error {
	if !r.cfg.Pair {
		return errors.New("noise: tunnel/1 has no psk")
	}
	return r.hs.SetPresharedKey(psk)
}

// WriteMessage2 answers (empty payload) and completes the handshake.
func (r *Responder) WriteMessage2() ([]byte, *Session, error) {
	msg, cs1, cs2, err := r.hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: message 2: %v", ErrHandshake, err)
	}
	return msg, &Session{send: cs2, recv: cs1, h: r.hs.ChannelBinding(), peer: r.peer}, nil
}

// --- handshake framing: len16(BE) ‖ message ---

// WriteFrame writes one length-prefixed handshake message.
func WriteFrame(w io.Writer, msg []byte) error {
	if len(msg) > MaxFrame {
		return errors.New("noise: message too long")
	}
	b := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(b, uint16(len(msg)))
	copy(b[2:], msg)
	_, err := w.Write(b)
	return err
}

// ReadFrame reads one length-prefixed message.
func ReadFrame(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	b := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// Dial runs the device side over c and returns the encrypted conn.
func Dial(c net.Conn, cfg Config) (*Conn, error) {
	in, err := NewInitiator(cfg)
	if err != nil {
		return nil, err
	}
	m1, err := in.Message1()
	if err != nil {
		return nil, err
	}
	if err := WriteFrame(c, m1); err != nil {
		return nil, err
	}
	m2, err := ReadFrame(c)
	if err != nil {
		return nil, fmt.Errorf("%w: reading message 2: %v", ErrHandshake, err)
	}
	s, err := in.ReadMessage2(m2)
	if err != nil {
		return nil, err
	}
	return NewConn(c, s), nil
}

// Accept runs xbind's side over c within HandshakeTimeout. check sees the
// device's static key and message 1's payload before xbind answers: it
// returns the psk for pair/1 (nil for tunnel/1), or an error to hang up
// without answering.
func Accept(c net.Conn, cfg Config, check func(peer [KeyLen]byte, payload []byte) ([]byte, error)) (*Conn, error) {
	_ = c.SetDeadline(time.Now().Add(HandshakeTimeout))
	r, err := NewResponder(cfg)
	if err != nil {
		return nil, err
	}
	m1, err := ReadFrame(c)
	if err != nil {
		return nil, fmt.Errorf("%w: reading message 1: %v", ErrHandshake, err)
	}
	peer, payload, err := r.ReadMessage1(m1)
	if err != nil {
		return nil, err
	}
	psk, err := check(peer, payload)
	if err != nil {
		return nil, err
	}
	if cfg.Pair {
		if err := r.SetPSK(psk); err != nil {
			return nil, err
		}
	}
	m2, s, err := r.WriteMessage2()
	if err != nil {
		return nil, err
	}
	if err := WriteFrame(c, m2); err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return NewConn(c, s), nil
}
