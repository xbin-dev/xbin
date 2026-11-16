package webfetch

import (
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// UserAgent is what every fetch says it is. Fixed: nothing the agent
// writes reaches a request header.
const UserAgent = "xbin-workspace-agent/1 (a person's workspace agent; https://xbin.dev/docs/workspace-agent.md)"

// Timeout bounds one fetch, its redirects included.
const Timeout = 20 * time.Second

// Types are the content types a fetch reads; anything else is refused
// (web-type) without its body being read.
var Types = []string{
	"text/html", "application/xhtml+xml", "text/plain", "text/markdown", "text/csv",
	"application/json", "application/xml", "text/xml", "application/rss+xml", "application/atom+xml",
}

// Resolver resolves a host name to addresses (net.Resolver has this
// method; tests give one of their own).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Fetcher makes fetches. The zero value is ready: the system's resolver,
// public addresses only, the limits above.
type Fetcher struct {
	// Resolver resolves names; nil: net.DefaultResolver.
	Resolver Resolver
	// Check judges a URL beyond CheckURL — the first one and every
	// redirect's: whose hosts may be fetched (the lists), the workspace's
	// own hosts. nil: none. Its error is the refusal (a *Refusal keeps its
	// code; anything else is web-host).
	Check func(u *url.URL) error
	// Public says an address may be dialled; nil: PublicAddr. Tests that
	// fetch from a local server widen it for that server's address alone.
	Public func(netip.Addr) bool
	// DialIP connects to one checked address (ip:port); nil: a dialer
	// whose Control refuses a socket whose peer isn't Public. Tests record
	// what is dialled with it.
	DialIP func(ctx context.Context, network, addr string) (net.Conn, error)
	// Timeout, MaxBody, MaxText: 0 is the package's default.
	Timeout time.Duration
	MaxBody int64
	MaxText int
}

// Result is what a fetch read.
type Result struct {
	URL         string   `json:"url"`
	FinalURL    string   `json:"finalUrl"`
	Status      int      `json:"status"`
	ContentType string   `json:"contentType"`
	Title       string   `json:"title,omitempty"`
	Text        string   `json:"text"`
	Bytes       int64    `json:"bytes"`     // read of the answer's body
	Truncated   bool     `json:"truncated"` // the body was longer than MaxBody, or its text than MaxText
	Redirects   []string `json:"redirects"`
}

// Dials reports whether f would dial ip: a URL that names an address can
// be judged before anything is asked of anyone.
func (f *Fetcher) Dials(ip netip.Addr) bool { return f.public(ip) }

func (f *Fetcher) public(ip netip.Addr) bool {
	if f.Public != nil {
		return f.Public(ip)
	}
	return PublicAddr(ip)
}

func (f *Fetcher) resolver() Resolver {
	if f.Resolver != nil {
		return f.Resolver
	}
	return net.DefaultResolver
}

// dial is the transport's DialContext: the name resolved, every address
// judged, one of the judged addresses connected to — by its address, so
// nothing resolves the name again — and the socket's own peer judged
// again by the dialer's Control.
func (f *Fetcher) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = f.resolver().LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, refuse("web-unreachable", http.StatusBadGateway, "%s: the name doesn't resolve (%v)", host, trimDNS(err))
		}
		if len(ips) == 0 {
			return nil, refuse("web-unreachable", http.StatusBadGateway, "%s: the name has no address", host)
		}
	}
	for _, ip := range ips {
		if !f.public(ip) {
			// one private answer is enough to refuse the name: a host that
			// resolves into the network may be meant to
			return nil, refuse("web-address", http.StatusForbidden, "%s is %s, which isn't a public address: a workspace agent reads the public web only", host, ip.Unmap())
		}
	}
	dialIP := f.DialIP
	if dialIP == nil {
		d := &net.Dialer{Timeout: 10 * time.Second, Control: f.control}
		dialIP = d.DialContext
	}
	var last error
	for _, ip := range ips {
		c, err := dialIP(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	var r *Refusal
	if errors.As(last, &r) {
		return nil, r
	}
	return nil, refuse("web-unreachable", http.StatusBadGateway, "%s: can't connect (%v)", host, last)
}

// control refuses a socket about to connect to an address that isn't
// public — whatever chose that address.
func (f *Fetcher) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !f.public(ap.Addr()) {
		return refuse("web-address", http.StatusForbidden, "%s isn't a public address: a workspace agent reads the public web only", address)
	}
	return nil
}

func trimDNS(err error) string {
	var de *net.DNSError
	if errors.As(err, &de) {
		if de.IsNotFound {
			return "no such host"
		}
		return de.Err
	}
	return err.Error()
}

// Fetch reads raw: a GET, followed through at most MaxRedirects redirects,
// each judged as the first. A refusal before anything was sent, or at a
// redirect, is a *Refusal; an answer of any status is a Result.
func (f *Fetcher) Fetch(ctx context.Context, raw string) (Result, error) {
	u, err := CheckURL(raw)
	if err != nil {
		return Result{}, err
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tr := &http.Transport{
		Proxy:                  nil, // never the environment's: a proxy dials on its own, past every check here
		DialContext:            f.dial,
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  15 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res := Result{URL: u.String(), Redirects: []string{}}
	for hop := 0; ; hop++ {
		if f.Check != nil {
			if err := f.Check(u); err != nil {
				var r *Refusal
				if errors.As(err, &r) {
					return res, r
				}
				return res, refuse("web-host", http.StatusForbidden, "%s", err.Error())
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return res, refuse("web-url", http.StatusBadRequest, "url: %v", err)
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "text/html, application/xhtml+xml, text/plain;q=0.9, text/markdown;q=0.9, application/json;q=0.8, */*;q=0.1")
		resp, err := client.Do(req)
		if err != nil {
			return res, fetchErr(ctx, u, err)
		}
		if loc := resp.Header.Get("Location"); isRedirect(resp.StatusCode) && loc != "" {
			resp.Body.Close()
			if hop >= MaxRedirects {
				return res, refuse("web-redirect", http.StatusBadGateway, "%s redirects more than %d times", res.URL, MaxRedirects)
			}
			next, err := u.Parse(loc)
			if err != nil {
				return res, refuse("web-redirect", http.StatusBadGateway, "a redirect to something that isn't a URL")
			}
			if u, err = CheckURL(next.String()); err != nil {
				return res, refuse("web-redirect", http.StatusForbidden, "a redirect was refused — %s", err.Error())
			}
			res.Redirects = append(res.Redirects, u.String())
			continue
		}
		defer resp.Body.Close()
		return f.read(res, u, resp)
	}
}

func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// fetchErr is a failed request in a fetch's words: the refusal the dialer
// gave, the timeout, or the network's error.
func fetchErr(ctx context.Context, u *url.URL, err error) error {
	var r *Refusal
	switch {
	case errors.As(err, &r):
		return r
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded):
		return refuse("web-timeout", http.StatusGatewayTimeout, "%s didn't answer within the time a fetch has", u.Host)
	case ctx.Err() != nil:
		return ctx.Err()
	}
	msg := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) {
		msg = ue.Err.Error()
	}
	return refuse("web-unreachable", http.StatusBadGateway, "%s: %s", u.Host, msg)
}

// read takes the answer's body: its type judged first, at most MaxBody
// bytes, turned into text.
func (f *Fetcher) read(res Result, u *url.URL, resp *http.Response) (Result, error) {
	res.FinalURL, res.Status = u.String(), resp.StatusCode
	mt, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.Header.Get("Content-Type") == "" {
		mt, err = "text/plain", nil // an answer that names no type is read as text, and judged as text below
	}
	if err != nil {
		return res, refuse("web-type", http.StatusUnsupportedMediaType, "%s answered with a content type that can't be read (%q)", u.Host, resp.Header.Get("Content-Type"))
	}
	mt = strings.ToLower(mt)
	res.ContentType = mt
	if !allowedType(mt) {
		return res, refuse("web-type", http.StatusUnsupportedMediaType, "%s is %s, which a workspace agent doesn't read (it reads %s)", res.FinalURL, mt, strings.Join(Types, ", "))
	}
	if cs := strings.ToLower(params["charset"]); cs != "" && cs != "utf-8" && cs != "utf8" && cs != "us-ascii" && cs != "iso-8859-1" && cs != "latin1" && cs != "windows-1252" {
		return res, refuse("web-type", http.StatusUnsupportedMediaType, "%s is in %s, a character set a workspace agent doesn't read", res.FinalURL, cs)
	}
	max := f.MaxBody
	if max <= 0 {
		max = MaxBody
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil && len(body) == 0 {
		return res, refuse("web-unreachable", http.StatusBadGateway, "%s: the answer broke off (%v)", u.Host, err)
	}
	if int64(len(body)) > max {
		body, res.Truncated = body[:max], true
	}
	res.Bytes = int64(len(body))
	latin := params["charset"] != "" && !strings.HasPrefix(strings.ToLower(params["charset"]), "utf") && strings.ToLower(params["charset"]) != "us-ascii"
	text := decode(body, latin)
	if mt == "text/html" || mt == "application/xhtml+xml" {
		res.Title, text = HTMLText(text, u)
	} else {
		text = plain(text)
	}
	maxText := f.MaxText
	if maxText <= 0 {
		maxText = MaxText
	}
	if len(text) > maxText {
		cut := maxText
		for cut > 0 && (text[cut]&0xC0) == 0x80 { // not inside a character
			cut--
		}
		text, res.Truncated = text[:cut], true
	}
	res.Text = text
	return res, nil
}

func allowedType(mt string) bool {
	for _, t := range Types {
		if mt == t {
			return true
		}
	}
	return false
}

// decode is body as UTF-8: Latin-1 (and windows-1252, close enough for
// reading) mapped byte by byte, anything else made valid.
func decode(body []byte, latin bool) string {
	if !latin {
		return strings.ToValidUTF8(string(body), "�")
	}
	var b strings.Builder
	b.Grow(len(body))
	for _, c := range body {
		b.WriteRune(rune(c))
	}
	return b.String()
}

// plain is text with its control characters (but line breaks and tabs)
// dropped and its line ends made \n.
func plain(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return -1
		}
		return r
	}, s)
}

// Describe is the audit row's path for a fetched URL: its host and path,
// no query (a URL's query is often what a page is about, and the log keeps
// what was called, not what was sent), at most 512 bytes.
func Describe(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	out := u.Scheme + "://" + u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?…"
	}
	if len(out) > 512 {
		out = out[:512]
	}
	return out
}
