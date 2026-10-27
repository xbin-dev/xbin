package noise

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	fnoise "github.com/flynn/noise"
)

func fixed(label string, n int) []byte {
	h := sha256.Sum256([]byte("xbin-tunnel-vector/" + label))
	return h[:n]
}

func fixedKey(t testing.TB, label string) Key {
	k, err := KeyFromPrivate(fixed(label, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func endpoint(label string) (ed25519.PrivateKey, [32]byte) {
	priv := ed25519.NewKeyFromSeed(fixed(label, 32))
	var id [32]byte
	copy(id[:], priv.Public().(ed25519.PublicKey))
	return priv, id
}

type pairSetup struct {
	server, device Key
	sEP, dEP       [32]byte
	psk            []byte
	code           string
}

func newSetup(t testing.TB) pairSetup {
	_, sEP := endpoint("server iroh")
	_, dEP := endpoint("device iroh")
	return pairSetup{server: fixedKey(t, "server noise"), device: fixedKey(t, "device noise"),
		sEP: sEP, dEP: dEP, psk: fixed("psk", 32), code: "ABCDEFGHIJKLMNOPQRSTUVWXYZ"}
}

func (p pairSetup) devCfg(pair bool) Config {
	c := Config{Pair: pair, Static: p.device, ServerKey: p.server.Public, ServerEndpoint: p.sEP, DeviceEndpoint: p.dEP}
	if pair {
		c.PSK, c.Code = p.psk, p.code
	}
	return c
}

func (p pairSetup) srvCfg(pair bool) Config {
	return Config{Pair: pair, Static: p.server, ServerEndpoint: p.sEP, DeviceEndpoint: p.dEP}
}

// handshake runs both sides over a pipe.
func handshake(t *testing.T, dev, srv Config, check func([32]byte, []byte) ([]byte, error)) (*Conn, *Conn, error, error) {
	t.Helper()
	a, b := net.Pipe()
	var wg sync.WaitGroup
	var sc *Conn
	var serr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc, serr = Accept(b, srv, check)
		if serr != nil {
			b.Close()
		}
	}()
	dc, derr := Dial(a, dev)
	if derr != nil {
		a.Close()
	}
	wg.Wait()
	return dc, sc, derr, serr
}

func TestHandshakeTunnelAndPair(t *testing.T) {
	p := newSetup(t)
	for _, pair := range []bool{false, true} {
		var gotPeer [32]byte
		var gotPayload []byte
		dc, sc, derr, serr := handshake(t, p.devCfg(pair), p.srvCfg(pair), func(peer [32]byte, payload []byte) ([]byte, error) {
			gotPeer, gotPayload = peer, payload
			if pair {
				return p.psk, nil
			}
			return nil, nil
		})
		if derr != nil || serr != nil {
			t.Fatalf("pair=%v: %v / %v", pair, derr, serr)
		}
		if gotPeer != p.device.Public {
			t.Fatalf("pair=%v: responder saw another static key", pair)
		}
		sel := Selector(p.code)
		if pair && !bytes.Equal(gotPayload, sel[:]) || !pair && len(gotPayload) != 0 {
			t.Fatalf("pair=%v: payload %x", pair, gotPayload)
		}
		if dc.Session().SAS() != sc.Session().SAS() || !bytes.Equal(dc.Session().HandshakeHash(), sc.Session().HandshakeHash()) {
			t.Fatalf("pair=%v: the two sides disagree on h", pair)
		}
		if dc.Session().PeerStatic() != p.server.Public || sc.Session().PeerStatic() != p.device.Public {
			t.Fatalf("pair=%v: peer statics", pair)
		}
		roundTrip(t, dc, sc)
	}
}

// roundTrip sends data both ways, then closes both directions.
func roundTrip(t *testing.T, a, b *Conn) {
	t.Helper()
	big := bytes.Repeat([]byte("0123456789abcdef"), 10000) // > MaxBody: split into frames
	go func() {
		a.Write([]byte("hello"))
		a.Write(big)
		a.CloseWrite()
	}()
	got, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, append([]byte("hello"), big...)) {
		t.Fatalf("got %d bytes", len(got))
	}
	go func() {
		b.Write([]byte("back"))
		b.Close()
	}()
	got, err = io.ReadAll(a)
	if err != nil || string(got) != "back" {
		t.Fatalf("back: %q %v", got, err)
	}
	a.Close()
}

func TestHandshakeRefusals(t *testing.T) {
	p := newSetup(t)
	psk := func([32]byte, []byte) ([]byte, error) { return p.psk, nil }
	none := func([32]byte, []byte) ([]byte, error) { return nil, nil }

	// a wrong psk: the device can't read message 2
	dev := p.devCfg(true)
	dev.PSK = fixed("another psk", 32)
	if _, _, derr, _ := handshake(t, dev, p.srvCfg(true), psk); !errors.Is(derr, ErrHandshake) {
		t.Errorf("wrong psk: %v", derr)
	}
	// another device EndpointId in xbind's prologue (a forged header claim)
	srv := p.srvCfg(false)
	_, srv.DeviceEndpoint = endpoint("someone else")
	if _, _, _, serr := handshake(t, p.devCfg(false), srv, none); !errors.Is(serr, ErrHandshake) {
		t.Errorf("wrong prologue: %v", serr)
	}
	// pair/1 and tunnel/1 don't mix
	if _, _, _, serr := handshake(t, p.devCfg(true), p.srvCfg(false), none); serr == nil {
		t.Error("an IKpsk2 initiator completed against an IK responder")
	}
	// a device that doesn't know the server's key
	dev = p.devCfg(false)
	dev.ServerKey = fixedKey(t, "not the server").Public
	if _, _, _, serr := handshake(t, dev, p.srvCfg(false), none); !errors.Is(serr, ErrHandshake) {
		t.Errorf("wrong server key: %v", serr)
	}
	// check refuses (an unknown device): xbind hangs up without answering
	refuse := errors.New("unknown device")
	_, _, derr, serr := handshake(t, p.devCfg(false), p.srvCfg(false), func([32]byte, []byte) ([]byte, error) { return nil, refuse })
	if !errors.Is(serr, refuse) || derr == nil {
		t.Errorf("refused: %v / %v", serr, derr)
	}
}

// lowOrder is a point of order 8 on Curve25519 (X25519 of it is zero).
var lowOrder = [32]byte{0xe0, 0xeb, 0x7a, 0x7c, 0x3b, 0x41, 0xb8, 0xae, 0x16, 0x56, 0xe3, 0xfa, 0xf1, 0x9f, 0xc4, 0x6a,
	0xda, 0x09, 0x8d, 0xeb, 0x9c, 0x32, 0xb1, 0xfd, 0x86, 0x62, 0x05, 0x16, 0x5f, 0x49, 0xb8, 0x00}

func TestLowOrder(t *testing.T) {
	p := newSetup(t)
	for _, pt := range [][32]byte{{}, lowOrder} {
		if _, err := (dh25519{}).DH(p.device.Private[:], pt[:]); !errors.Is(err, ErrLowOrder) {
			t.Fatalf("DH with %x: %v", pt[:2], err)
		}
		// the device refuses a low-order server key
		dev := p.devCfg(false)
		dev.ServerKey = pt
		in, err := NewInitiator(dev)
		if err == nil {
			_, err = in.Message1()
		}
		if err == nil {
			t.Fatal("message 1 written to a low-order server key")
		}
		// xbind refuses a low-order ephemeral
		in, _ = NewInitiator(p.devCfg(false))
		m1, _ := in.Message1()
		copy(m1[:32], pt[:])
		r, _ := NewResponder(p.srvCfg(false))
		if _, _, err := r.ReadMessage1(m1); !errors.Is(err, ErrHandshake) || !strings.Contains(err.Error(), "low-order") {
			t.Fatalf("low-order ephemeral: %v", err)
		}
	}
}

func TestPayloadLengthChecked(t *testing.T) {
	p := newSetup(t)
	for _, c := range []struct {
		pair    bool
		payload []byte
	}{{false, []byte("app data")}, {true, nil}, {true, make([]byte, 17)}} {
		hs, err := fnoise.NewHandshakeState(p.devCfg(c.pair).noiseConfig(true))
		if err != nil {
			t.Fatal(err)
		}
		m1, _, _, _ := hs.WriteMessage(nil, c.payload)
		r, _ := NewResponder(p.srvCfg(c.pair))
		if _, _, err := r.ReadMessage1(m1); !errors.Is(err, ErrHandshake) {
			t.Errorf("pair=%v payload %d: %v", c.pair, len(c.payload), err)
		}
	}
}

func TestTruncationAndClose(t *testing.T) {
	p := newSetup(t)
	dc, sc, derr, serr := handshake(t, p.devCfg(false), p.srvCfg(false), func([32]byte, []byte) ([]byte, error) { return nil, nil })
	if derr != nil || serr != nil {
		t.Fatal(derr, serr)
	}
	go func() {
		dc.Write([]byte("partial"))
		dc.Underlying().Close() // no close frame: truncation
	}()
	got, err := io.ReadAll(sc)
	if err != io.ErrUnexpectedEOF || string(got) != "partial" {
		t.Fatalf("truncation: %q %v", got, err)
	}
	if _, err := sc.Read(make([]byte, 1)); err != io.ErrUnexpectedEOF {
		t.Fatalf("sticky: %v", err)
	}
}

func TestDeadlineMidFrameResumes(t *testing.T) {
	p := newSetup(t)
	dc, sc, derr, serr := handshake(t, p.devCfg(false), p.srvCfg(false), func([32]byte, []byte) ([]byte, error) { return nil, nil })
	if derr != nil || serr != nil {
		t.Fatal(derr, serr)
	}
	// capture one frame's bytes from the device side
	var buf bytes.Buffer
	rc := &recConn{w: &buf}
	dc2 := NewConn(rc, dc.Session())
	dc2.Write([]byte("resumed"))
	frame := buf.Bytes()
	under := dc.Underlying()
	go func() {
		under.Write(frame[:5]) // the length and 3 bytes of ciphertext
		time.Sleep(50 * time.Millisecond)
		under.Write(frame[5:])
	}()
	sc.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := sc.Read(make([]byte, 16)); !isTimeout(err) {
		t.Fatalf("want a timeout, got %v", err)
	}
	sc.SetReadDeadline(time.Time{})
	b := make([]byte, 16)
	n, err := sc.Read(b)
	if err != nil || string(b[:n]) != "resumed" {
		t.Fatalf("resume: %q %v", b[:n], err)
	}
}

func TestRekeyPolicy(t *testing.T) {
	p := newSetup(t)
	dc, sc, derr, serr := handshake(t, p.devCfg(false), p.srvCfg(false), func([32]byte, []byte) ([]byte, error) { return nil, nil })
	if derr != nil || serr != nil {
		t.Fatal(derr, serr)
	}
	clock := time.Now()
	dc.now = func() time.Time { return clock }
	dc.keyed = clock
	dc.rekeyMsgs, dc.rekeyBytes = 3, 100
	go func() {
		for i := 0; i < 10; i++ {
			dc.Write([]byte("msg"))
		}
		dc.Write(bytes.Repeat([]byte("x"), 150)) // bytes threshold
		dc.Write([]byte("y"))
		clock = clock.Add(RekeyAge) // time threshold
		dc.Write([]byte("z"))
		dc.CloseWrite()
	}()
	got, err := io.ReadAll(sc)
	if err != nil || len(got) != 30+150+2 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	// the receiving side's state followed every rekey: the next frame decrypts
	go sc.Write([]byte("ok"))
	b := make([]byte, 2)
	if _, err := io.ReadFull(dc, b); err != nil || string(b) != "ok" {
		t.Fatalf("after: %q %v", b, err)
	}
}

func TestBadFrames(t *testing.T) {
	p := newSetup(t)
	for name, mut := range map[string]func(*Conn, net.Conn){
		"flipped byte": func(dc *Conn, under net.Conn) {
			var buf bytes.Buffer
			NewConn(&recConn{w: &buf}, dc.Session()).Write([]byte("data"))
			b := buf.Bytes()
			b[4] ^= 1
			under.Write(b)
		},
		"unknown type": func(dc *Conn, under net.Conn) {
			dc.wmu.Lock()
			dc.sealLocked(0x03, nil)
			dc.wmu.Unlock()
		},
		"close with a body": func(dc *Conn, under net.Conn) {
			dc.wmu.Lock()
			dc.sealLocked(TypeClose, []byte("x"))
			dc.wmu.Unlock()
		},
		"short frame": func(dc *Conn, under net.Conn) {
			under.Write([]byte{0, 3, 1, 2, 3})
		},
	} {
		dc, sc, derr, serr := handshake(t, p.devCfg(false), p.srvCfg(false), func([32]byte, []byte) ([]byte, error) { return nil, nil })
		if derr != nil || serr != nil {
			t.Fatal(derr, serr)
		}
		go mut(dc, dc.Underlying())
		if _, err := sc.Read(make([]byte, 8)); !errors.Is(err, ErrFrame) {
			t.Errorf("%s: %v", name, err)
		}
		dc.Underlying().Close()
	}
}

func TestSASFormat(t *testing.T) {
	if FormatSAS(12345) != "012 345" || FormatSAS(0) != "000 000" || FormatSAS(999999) != "999 999" {
		t.Fatal("format")
	}
	h := fixed("h", 32)
	if n := SASNumber(h); n >= 1_000_000 {
		t.Fatal(n)
	}
	if len(SASOKM(h)) != 8 {
		t.Fatal("okm length")
	}
}

func TestSelector(t *testing.T) {
	s := Selector("ABC")
	h := sha256.Sum256([]byte("xbin-pair-select-v1\nABC"))
	if !bytes.Equal(s[:], h[:16]) {
		t.Fatal("selector")
	}
}

// recConn is a net.Conn that records writes and reads from r.
type recConn struct {
	w io.Writer
	r io.Reader
}

func (c *recConn) Read(b []byte) (int, error) {
	if c.r == nil {
		return 0, io.EOF
	}
	return c.r.Read(b)
}
func (c *recConn) Write(b []byte) (int, error)      { return c.w.Write(b) }
func (c *recConn) Close() error                     { return nil }
func (c *recConn) LocalAddr() net.Addr              { return nil }
func (c *recConn) RemoteAddr() net.Addr             { return nil }
func (c *recConn) SetDeadline(time.Time) error      { return nil }
func (c *recConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recConn) SetWriteDeadline(time.Time) error { return nil }
