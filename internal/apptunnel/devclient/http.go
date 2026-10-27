package devclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Transport is plain HTTP/1.1 over the tunnel (spec §7): every connection
// the transport opens is one tunnel/1 stream. Requests go to the device's
// loopback address (http://127.0.0.1:<port>), whatever host they name.
func (t *Tunnel) Transport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return t.DialConn(ctx)
		},
		ForceAttemptHTTP2:   false,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
}

// Client is an http.Client over Transport that never follows redirects
// (the app rewrites them itself).
func (t *Tunnel) Client() *http.Client {
	return &http.Client{Transport: t.Transport(), Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Session is the token response of device login.
type Session struct {
	Token    string `json:"token"`
	DeviceID string `json:"deviceId"`
	User     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"user"`
}

// HTTPError is a non-2xx answer, with xbind's JSON body.
type HTTPError struct {
	Status int
	Body   map[string]any
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %v", e.Status, e.Body["error"]) }

// Tunnel is the body's "tunnel" field ("device", "required", "bound"), if any.
func (e *HTTPError) Tunnel() string { s, _ := e.Body["tunnel"].(string); return s }

// Do sends a JSON request (body nil: none) with an optional bearer and
// decodes a JSON answer into out (nil: discarded).
func Do(ctx context.Context, c *http.Client, method, url, bearer string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		e := &HTTPError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, &e.Body)
		return e
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Login runs device login (native/spec/device-login.md §4) against base
// (e.g. http://127.0.0.1:52100): a challenge, the signature over origin,
// deviceID and the nonce, the session.
func (d *Device) Login(ctx context.Context, c *http.Client, base, origin, deviceID string) (*Session, error) {
	var ch struct{ Nonce string }
	if err := Do(ctx, c, "POST", base+"/login/device/challenge", "", map[string]string{"deviceId": deviceID}, &ch); err != nil {
		return nil, err
	}
	sig, err := d.Sign(origin, deviceID, ch.Nonce)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := Do(ctx, c, "POST", base+"/login/device", "", map[string]string{"deviceId": deviceID, "nonce": ch.Nonce, "signature": sig}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Sign signs a device-login challenge: ECDSA P-256 over SHA-256 of the
// message, ASN.1 DER, base64url.
func (d *Device) Sign(origin, deviceID, nonce string) (string, error) {
	digest := sha256.Sum256(auth.DeviceLoginMessage(origin, deviceID, nonce))
	sig, err := ecdsa.SignASN1(nil, d.LoginKey, digest[:])
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// ClientConfig is GET /api/xbin/app-tunnel/client (spec §7).
type ClientConfig struct {
	EndpointID string `json:"endpointId"`
	Ticket     string `json:"ticket"`
	ServerKey  string `json:"serverKey"`
	Relay      struct {
		Relay
		Next *Relay `json:"next,omitempty"`
	} `json:"relay"`
	Required bool `json:"required"`
}

// Upgrade registers this device's tunnel keys on an enrolled device (spec
// §9): its own fresh session, over a direct connection.
func (d *Device) Upgrade(ctx context.Context, c *http.Client, base, bearer, deviceID string) (*ClientConfig, error) {
	var cc ClientConfig
	err := Do(ctx, c, "POST", base+"/api/xbin/devices/"+deviceID+"/tunnel", bearer,
		map[string]string{"noiseKey": d.NoiseKeyB64(), "endpoint": d.EndpointHex()}, &cc)
	if err != nil {
		return nil, err
	}
	return &cc, nil
}
