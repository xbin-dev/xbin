// Package devclient is the device side of an app tunnel in Go (D204,
// native/spec/tunnel.md): parsing the pairing link, pairing over
// xbin/pair/1 (IKpsk2, the selector, the confirmation code, the JSON
// messages), opening xbin/tunnel/1 streams (IK) and running HTTP over them,
// and device login. xbind's tests use it, so does hack/fakeiroh, and it is
// the reference the iOS app's Tunnel.swift / NoiseTunnel.swift follow. The
// iroh part is the caller's: a Dialer opens one bi-stream per call.
package devclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/xbin-dev/xbin/internal/apptunnel/noise"
	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
)

// Version is the tunnel version this client speaks (the link's tv).
const Version = 1

// Device is one app install's keys for one workspace: the iroh identity
// (its public key is the EndpointId), the Noise static key, and the
// device-login P-256 key (the Secure Enclave's, on a phone).
type Device struct {
	Iroh     ed25519.PrivateKey
	Noise    noise.Key
	LoginKey *ecdsa.PrivateKey
	Name     string
	Platform string
}

// New makes a device with fresh keys.
func New(name string) (*Device, error) {
	_, iroh, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	nk, err := noise.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	lk, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Device{Iroh: iroh, Noise: nk, LoginKey: lk, Name: name, Platform: "ios"}, nil
}

// EndpointID is the device's iroh EndpointId.
func (d *Device) EndpointID() [32]byte {
	var id [32]byte
	copy(id[:], d.Iroh.Public().(ed25519.PublicKey))
	return id
}

// EndpointHex is the EndpointId as iroh prints it.
func (d *Device) EndpointHex() string { id := d.EndpointID(); return hex.EncodeToString(id[:]) }

// NoiseKeyB64 is the Noise public key as the upgrade route takes it.
func (d *Device) NoiseKeyB64() string { return base64.RawURLEncoding.EncodeToString(d.Noise.Public[:]) }

// PublicKeyB64 is the device-login key as SPKI DER, base64url.
func (d *Device) PublicKeyB64() string {
	der, err := x509.MarshalPKIXPublicKey(&d.LoginKey.PublicKey)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(der)
}

// Dialer opens one bi-stream to the workspace's endpoint on alpn
// (wire.ALPNPairName or wire.ALPNTunnelName). On a phone that is iroh's
// connect + openBi; in tests, a pipe into xbind's listener.
type Dialer func(ctx context.Context, alpn string) (net.Conn, error)

// Link is a parsed xbin://enroll link (spec §2).
type Link struct {
	Origin     string // u
	Code       string // c
	Version    int    // tv; 0: a classic link
	Ticket     string // t
	ServerKey  [32]byte
	PSK        []byte
	RelayToken string // r
	Required   bool   // rq=1
}

// Tunnel reports a tunnel link (t, k and p present).
func (l Link) Tunnel() bool { return l.Version != 0 }

// ParseLink parses a pairing link strictly: if t, k or p is present, all
// three must be, and well-formed, with a known tv — never a quiet fallback
// to classic enrollment.
func ParseLink(s string) (Link, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "xbin" || u.Host != "enroll" {
		return Link{}, errors.New("not an xbin://enroll link")
	}
	q := u.Query()
	l := Link{Origin: q.Get("u"), Code: q.Get("c")}
	if l.Origin == "" || l.Code == "" {
		return Link{}, errors.New("the link has no u or c")
	}
	t, k, p := q.Get("t"), q.Get("k"), q.Get("p")
	if t == "" && k == "" && p == "" && q.Get("tv") == "" {
		return l, nil
	}
	if q.Get("tv") != "1" {
		return Link{}, fmt.Errorf("tunnel version %q: update the app", q.Get("tv"))
	}
	l.Version = 1
	if t == "" || k == "" || p == "" {
		return Link{}, errors.New("a tunnel link needs t, k and p")
	}
	kb, err := base64.RawURLEncoding.DecodeString(k)
	if err != nil || len(kb) != 32 {
		return Link{}, errors.New("k: want 32 bytes, base64url")
	}
	copy(l.ServerKey[:], kb)
	if l.PSK, err = base64.RawURLEncoding.DecodeString(p); err != nil || len(l.PSK) != 32 {
		return Link{}, errors.New("p: want 32 bytes, base64url")
	}
	if !strings.HasPrefix(t, "endpoint") {
		return Link{}, errors.New("t: not an EndpointTicket")
	}
	l.Ticket, l.RelayToken, l.Required = t, q.Get("r"), q.Get("rq") == "1"
	return l, nil
}

// Enrolled is xbind's "enrolled" message (spec §6.3).
type Enrolled struct {
	DeviceID string `json:"deviceId"`
	User     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
	Origin string     `json:"origin"`
	Name   string     `json:"name"`
	Tunnel TunnelInfo `json:"tunnel"`
}

// TunnelInfo is how the device reaches the tunnel afterwards.
type TunnelInfo struct {
	Ticket   string `json:"ticket"`
	Relay    Relay  `json:"relay"`
	Required bool   `json:"required"`
}

// Relay is a relay URL and access token (both optional: n0's needs neither).
type Relay struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// Rejected is a pairing xbind refused (spec §6.4).
type Rejected struct {
	Reason string `json:"reason"` // mismatch | expired | limit | refused
	Err    string `json:"error"`
}

func (r *Rejected) Error() string { return "pairing rejected (" + r.Reason + "): " + r.Err }

// PairOptions are what pairing needs beyond the link.
type PairOptions struct {
	// ServerEndpoint is the workspace's EndpointId (from the ticket: the
	// dialer checked the remote id against it).
	ServerEndpoint [32]byte
	// OnSAS is called with the confirmation code ("012 345") and the
	// pairing's expiry once xbind says it awaits the computer's
	// confirmation: the phone shows it.
	OnSAS func(code, expires string)
}

// Pair runs pair/1 (spec §5–§6) and returns the enrollment once the
// computer confirmed the code.
func (d *Device) Pair(ctx context.Context, dial Dialer, link Link, opt PairOptions) (*Enrolled, error) {
	if !link.Tunnel() {
		return nil, errors.New("not a tunnel link")
	}
	st, err := dial(ctx, wire.ALPNPairName)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { st.Close() })
	defer stop()
	c, err := noise.Dial(st, noise.Config{Pair: true, Static: d.Noise, ServerKey: link.ServerKey,
		ServerEndpoint: opt.ServerEndpoint, DeviceEndpoint: d.EndpointID(), PSK: link.PSK, Code: link.Code})
	if err != nil {
		st.Close()
		return nil, err
	}
	defer c.Close()
	hello, _ := json.Marshal(map[string]any{"v": 1, "name": d.Name, "platform": d.Platform, "publicKey": d.PublicKeyB64()})
	if err := c.WriteMessage(hello); err != nil {
		return nil, err
	}
	for {
		b, err := c.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("pairing: %w", err)
		}
		var m struct {
			Type    string `json:"type"`
			Expires string `json:"expires"`
			Enrolled
			Rejected
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("pairing: %w", err)
		}
		switch m.Type {
		case "await":
			if opt.OnSAS != nil {
				opt.OnSAS(c.Session().SAS(), m.Expires)
			}
		case "enrolled":
			e := m.Enrolled
			return &e, nil
		case "rejected":
			r := m.Rejected
			return nil, &r
		default:
			return nil, fmt.Errorf("pairing: unexpected message %q", m.Type)
		}
	}
}

// Tunnel opens tunnel/1 streams to one workspace.
type Tunnel struct {
	d         *Device
	dial      Dialer
	server    [32]byte
	serverKey [32]byte
}

// Tunnel returns the device's tunnel to the workspace whose EndpointId and
// Noise key are given.
func (d *Device) Tunnel(dial Dialer, serverEndpoint, serverKey [32]byte) *Tunnel {
	return &Tunnel{d: d, dial: dial, server: serverEndpoint, serverKey: serverKey}
}

// DialConn opens one stream and runs IK over it.
func (t *Tunnel) DialConn(ctx context.Context) (*noise.Conn, error) {
	st, err := t.dial(ctx, wire.ALPNTunnelName)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { st.Close() })
	defer stop()
	c, err := noise.Dial(st, noise.Config{Static: t.d.Noise, ServerKey: t.serverKey,
		ServerEndpoint: t.server, DeviceEndpoint: t.d.EndpointID()})
	if err != nil {
		st.Close()
		return nil, err
	}
	return c, nil
}
