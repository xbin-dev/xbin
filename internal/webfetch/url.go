package webfetch

import (
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// Limits of one fetch.
const (
	MaxURL       = 2 << 10 // bytes of a URL, as given and at every redirect
	MaxBody      = 2 << 20 // bytes read of an answer (after decompression)
	MaxText      = 64 << 10
	MaxRedirects = 5
)

// Refusal is why a fetch wasn't made or was stopped: Code is the
// refusal's word on the wire (web-url, web-host, web-address, web-type,
// web-redirect, web-unreachable, web-timeout), Status the HTTP status the
// route answers it with.
type Refusal struct {
	Code   string
	Status int
	Msg    string
}

func (r *Refusal) Error() string { return r.Msg }

func refuse(code string, status int, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Status: status, Msg: fmt.Sprintf(format, args...)}
}

// CheckURL is raw as a URL a fetch may name, or why not: http or https, at
// most MaxURL bytes, a host and no user info, the host a DNS name or an
// IP address in its canonical form (a dotted quad, a bracketed IPv6
// address without a zone) — never one of the numeric forms a resolver
// would read as an address (numericHost). The host comes back in its
// ASCII form (IDNA), lower case; the fragment is dropped (it is never
// sent).
func CheckURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, refuse("web-url", http.StatusBadRequest, "url: an absolute http or https URL")
	}
	if len(raw) > MaxURL {
		return nil, refuse("web-url", http.StatusBadRequest, "url: at most %d bytes", MaxURL)
	}
	for _, c := range raw {
		if c < 0x21 || c == 0x7f {
			return nil, refuse("web-url", http.StatusBadRequest, "url: no spaces or control characters (percent-encode them)")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, refuse("web-url", http.StatusBadRequest, "url: not a URL (%v)", err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, refuse("web-url", http.StatusBadRequest, "url: http or https only, not %q", u.Scheme)
	case u.Opaque != "" || u.Host == "":
		return nil, refuse("web-url", http.StatusBadRequest, "url: an absolute URL with a host")
	case u.User != nil:
		return nil, refuse("web-url", http.StatusBadRequest, "url: no user name or password in it — a fetch carries no credentials")
	}
	host := u.Hostname()
	port := u.Port()
	if port != "" {
		for _, c := range port {
			if c < '0' || c > '9' {
				return nil, refuse("web-url", http.StatusBadRequest, "url: the port is a number")
			}
		}
		if n, _ := strconv.Atoi(port); len(port) > 5 || n < 1 || n > 65535 {
			return nil, refuse("web-url", http.StatusBadRequest, "url: no such port")
		}
	}
	if strings.Contains(u.Host, "[") { // an IPv6 literal: canonical, no zone
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return nil, refuse("web-url", http.StatusBadRequest, "url: %q isn't an IPv6 address a fetch can name", host)
		}
		u.Host = "[" + ip.String() + "]"
		if port != "" {
			u.Host += ":" + port
		}
		u.Fragment, u.RawFragment = "", ""
		return u, nil
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is4() {
		u.Fragment, u.RawFragment = "", ""
		return u, nil // a canonical dotted quad: judged by its address
	}
	ascii, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
	if err != nil || ascii == "" || len(ascii) > 253 {
		return nil, refuse("web-url", http.StatusBadRequest, "url: %q isn't a host name", host)
	}
	ascii = strings.ToLower(ascii)
	if numericHost(ascii) {
		return nil, refuse("web-url", http.StatusBadRequest, "url: %q reads as an address in a non-standard form: name the host, or write the address as a.b.c.d", host)
	}
	if localName(ascii) {
		return nil, refuse("web-address", http.StatusForbidden, "%s names this machine or a local network, not the public web", ascii)
	}
	if port != "" {
		u.Host = ascii + ":" + port
	} else {
		u.Host = ascii
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

// localNames are the names (and the names under them) that are never on
// the public web: this machine (RFC 6761), the local link's (mDNS), home
// networks' (RFC 8375) and the private-use top-level domain.
var localNames = []string{"localhost", "local", "home.arpa", "internal", "localdomain"}

func localName(host string) bool {
	for _, n := range localNames {
		if host == n || strings.HasSuffix(host, "."+n) {
			return true
		}
	}
	return false
}

// Pattern is a host pattern of an allow or deny list: "example.com" is the
// host and every name under it; "*.example.com" every name under it but
// not the host itself. Patterns are kept in their ASCII form, lower case.
type Pattern string

// ParsePattern checks a pattern and puts it in its canonical form.
func ParsePattern(s string) (Pattern, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	sub := strings.HasPrefix(s, "*.")
	name := strings.TrimPrefix(s, "*.")
	name = strings.TrimSuffix(name, ".")
	if name == "" || strings.ContainsAny(name, "*/:@ ") {
		return "", fmt.Errorf("%q isn't a host pattern (example.com, or *.example.com for the names under it)", s)
	}
	ascii, err := idna.Lookup.ToASCII(name)
	if err != nil || len(ascii) > 253 || numericHost(ascii) {
		return "", fmt.Errorf("%q isn't a host name", s)
	}
	if _, err := netip.ParseAddr(ascii); err == nil {
		return "", fmt.Errorf("%q is an address: a list names hosts", s)
	}
	if sub {
		return Pattern("*." + ascii), nil
	}
	return Pattern(ascii), nil
}

// Match reports whether host (ASCII, lower case, no port) is p's.
func (p Pattern) Match(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if name, ok := strings.CutPrefix(string(p), "*."); ok {
		return strings.HasSuffix(host, "."+name)
	}
	return host == string(p) || strings.HasSuffix(host, "."+string(p))
}

// MatchAny reports whether host matches one of ps.
func MatchAny(ps []Pattern, host string) bool {
	for _, p := range ps {
		if p.Match(host) {
			return true
		}
	}
	return false
}
