package apptunnel

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
	"github.com/xbin-dev/xbin/internal/fsutil"
)

// Config is the app tunnel's settings (data/app-tunnel/config.json; D204):
// whether it is on, the relay, its access tokens (current and a staged
// next, for rotation) and whether app traffic is refused off the tunnel.
type Config struct {
	Enabled    bool   `json:"enabled"`
	Relay      Relay  `json:"relay"`
	Next       *Slot  `json:"next,omitempty"`
	TunnelOnly bool   `json:"tunnelOnly,omitempty"`
	Gen        uint64 `json:"gen"` // bumped whenever what a device must know changes (relay, tokens)
}

// Relay is the relay in force: n0's public relays, or a custom iroh-relay
// with an optional access token (iroh-relay's access.shared_token).
type Relay struct {
	Mode  string `json:"mode"` // "n0" | "custom"
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// Slot is a staged relay change: a new URL and/or token devices learn
// (GET /app-tunnel/client) before it is promoted.
type Slot struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

const (
	RelayN0     = "n0"
	RelayCustom = "custom"
)

// validRelayURL: https, or http on a loopback host (a relay beside xbind).
func validRelayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("relay url: want https://host[:port], got %q", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			return nil
		}
	}
	return fmt.Errorf("relay url: https only (http only on loopback), got %q", raw)
}

// validToken: 16–256 printable characters, no whitespace.
func validToken(t string) error {
	if n := len(t); n < 16 || n > 256 {
		return errors.New("relay token: 16–256 characters")
	}
	for _, r := range t {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return errors.New("relay token: printable ASCII, no whitespace")
		}
	}
	return nil
}

// validate checks a config as a whole.
func (c Config) validate() error {
	switch c.Relay.Mode {
	case RelayN0:
		if c.Relay.URL != "" || c.Relay.Token != "" {
			return errors.New("n0's relays take no url or token")
		}
	case RelayCustom:
		if err := validRelayURL(c.Relay.URL); err != nil {
			return err
		}
		if c.Relay.Token != "" {
			if err := validToken(c.Relay.Token); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("relay mode %q: n0 or custom", c.Relay.Mode)
	}
	if c.Next != nil {
		if c.Next.URL != "" {
			if err := validRelayURL(c.Next.URL); err != nil {
				return err
			}
		}
		if c.Next.Token != "" {
			if err := validToken(c.Next.Token); err != nil {
				return err
			}
		}
	}
	if c.TunnelOnly && !c.Enabled {
		return errors.New("refusing app traffic off the tunnel needs the tunnel on")
	}
	return nil
}

// wireRelay is what the sidecar is told.
func (c Config) wireRelay() wire.Relay {
	if c.Relay.Mode == RelayCustom {
		return wire.Relay{Mode: RelayCustom, URL: c.Relay.URL, Token: c.Relay.Token}
	}
	return wire.Relay{Mode: RelayN0}
}

// hint shows a token as its last four characters.
func hint(t string) string {
	if t == "" {
		return ""
	}
	if len(t) <= 4 {
		return "…"
	}
	return "…" + t[len(t)-4:]
}

// newToken is 32 random bytes, base64url: a relay access token.
func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// loadConfig reads config.json (absent: the defaults, tunnels off).
func loadConfig(dir string) (Config, error) {
	c := Config{Relay: Relay{Mode: RelayN0}}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("app-tunnel config.json: %w", err)
	}
	if c.Relay.Mode == "" {
		c.Relay.Mode = RelayN0
	}
	return c, nil
}

func saveConfig(dir string, c Config) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, "config.json"), append(b, '\n'), 0o600)
}

// envRelay is XBIN_IROH_RELAY: "" (the admin's), "disabled" (tests: direct
// addresses only), "n0", or a custom relay's URL.
func envRelay(v string) (wire.Relay, bool, error) {
	switch v = strings.TrimSpace(v); v {
	case "":
		return wire.Relay{}, false, nil
	case "disabled":
		return wire.Relay{Mode: "disabled"}, true, nil
	case RelayN0:
		return wire.Relay{Mode: RelayN0}, true, nil
	}
	if err := validRelayURL(v); err != nil {
		return wire.Relay{}, false, fmt.Errorf("XBIN_IROH_RELAY: %w", err)
	}
	return wire.Relay{Mode: RelayCustom, URL: v}, true, nil
}
