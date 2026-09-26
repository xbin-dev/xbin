package relay

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// APNs endpoints (HTTP/2 only).
const (
	APNsProduction  = "https://api.push.apple.com"
	APNsDevelopment = "https://api.sandbox.push.apple.com"
)

// jwtLifetime is how long a provider token is reused. APNs rejects tokens
// older than an hour and throttles refreshes more often than every 20
// minutes, so 40 minutes sits safely between.
const jwtLifetime = 40 * time.Minute

// APNs sends notifications with token-based (.p8) authentication.
type APNs struct {
	KeyID  string
	TeamID string
	Key    *ecdsa.PrivateKey
	// Production / Development are the endpoints per environment; empty
	// means Apple's (tests point both at a fake).
	Production, Development string
	Client                  *http.Client // must speak HTTP/2 (the default transport does over TLS)
	Now                     func() time.Time

	mu    sync.Mutex
	token string
	iat   time.Time
}

// LoadP8 parses an APNs auth key (.p8: a PEM "PRIVATE KEY", PKCS#8 P-256).
func LoadP8(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("apns key: no PEM block")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns key: %w", err)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns key: not an EC key")
	}
	return ek, nil
}

func (a *APNs) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// providerToken returns the cached ES256 JWT, minting a new one when it is
// older than jwtLifetime or force is set (APNs said it expired).
func (a *APNs) providerToken(force bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if !force && a.token != "" && now.Sub(a.iat) < jwtLifetime {
		return a.token, nil
	}
	tok, err := signJWT(a.Key, a.KeyID, a.TeamID, now)
	if err != nil {
		return "", err
	}
	a.token, a.iat = tok, now
	return tok, nil
}

// signJWT builds the APNs provider token: header {alg ES256, kid}, claims
// {iss team, iat}, signed with the raw r||s form JWS requires.
func signJWT(key *ecdsa.PrivateKey, keyID, teamID string, now time.Time) (string, error) {
	if key == nil {
		return "", errors.New("apns: no signing key")
	}
	enc := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID})
	claims, _ := json.Marshal(map[string]any{"iss": teamID, "iat": now.Unix()})
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// Notification is one push to one device.
type Notification struct {
	Token      string
	Topic      string
	Env        string // production | development
	Payload    []byte // the JSON body
	CollapseID string
	Priority   int // 10 (immediate) or 5
	Expiration time.Time
}

// APNsError is a non-200 answer from APNs.
type APNsError struct {
	Status     int
	Reason     string
	RetryAfter time.Duration
}

func (e *APNsError) Error() string { return fmt.Sprintf("apns: %d %s", e.Status, e.Reason) }

// Dead reports an answer that means the device token will never work again
// (the app was uninstalled, or the token is not valid for this topic/env).
func (e *APNsError) Dead() bool {
	switch e.Reason {
	case "Unregistered", "BadDeviceToken", "DeviceTokenNotForTopic":
		return true
	}
	return e.Status == http.StatusGone
}

// Send posts n to APNs. On an expired provider token it refreshes and
// retries once. Returns the apns-id.
func (a *APNs) Send(ctx context.Context, n Notification) (string, error) {
	id, err := a.send(ctx, n, false)
	var ae *APNsError
	if errors.As(err, &ae) && ae.Status == http.StatusForbidden && (ae.Reason == "ExpiredProviderToken" || ae.Reason == "InvalidProviderToken") {
		id, err = a.send(ctx, n, true)
	}
	return id, err
}

func (a *APNs) send(ctx context.Context, n Notification, fresh bool) (string, error) {
	tok, err := a.providerToken(fresh)
	if err != nil {
		return "", err
	}
	base := a.Production
	if base == "" {
		base = APNsProduction
	}
	if n.Env == "development" {
		base = a.Development
		if base == "" {
			base = APNsDevelopment
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+n.Token, bytes.NewReader(n.Payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("apns-topic", n.Topic)
	req.Header.Set("apns-push-type", "alert")
	prio := n.Priority
	if prio != 5 {
		prio = 10
	}
	req.Header.Set("apns-priority", strconv.Itoa(prio))
	if !n.Expiration.IsZero() {
		req.Header.Set("apns-expiration", strconv.FormatInt(n.Expiration.Unix(), 10))
	}
	if n.CollapseID != "" {
		req.Header.Set("apns-collapse-id", n.CollapseID)
	}
	req.Header.Set("content-type", "application/json")
	c := a.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusOK {
		return resp.Header.Get("apns-id"), nil
	}
	ae := &APNsError{Status: resp.StatusCode}
	var r struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &r)
	ae.Reason = r.Reason
	if s, err := strconv.Atoi(resp.Header.Get("retry-after")); err == nil && s > 0 {
		ae.RetryAfter = time.Duration(s) * time.Second
	}
	return "", ae
}
