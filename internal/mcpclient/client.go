// Package mcpclient is xbind's client of the MCP servers outside the
// workspace that an admin lists for people's workspace agents (D240;
// docs/protocol/delegation.md §Outside MCP servers). It speaks MCP's
// streamable HTTP for tools alone — initialize, ping, tools/list,
// tools/call — to one listed URL per server:
//
//   - dialled through internal/webfetch's guarded dialer, with the URL's
//     host the one name it dials and every address judged when its socket
//     connects (the web's rule, or a private network's where the admin
//     said so); TLS verified; never a proxy of the environment;
//   - a redirect only to the same scheme, host and port (307 and 308, the
//     POST sent again); any other is refused;
//   - an answer of at most MaxAnswer bytes in Timeout: JSON, or an event
//     stream read event by event until the request's own response —
//     notifications skipped, and a request FROM the server (sampling,
//     elicitation, roots, a ping) ends the exchange: xbind answers no
//     server, it calls tools;
//   - the requests built here from values the caller judged, never bytes
//     an agent wrote; what a server answers scrubbed of its secret.
//
// It holds no policy: which server, for whom, under which approval is the
// caller's (internal/server/mcpext.go).
package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/webfetch"
)

// ProtocolVersion is the version xbind asks for at initialize.
const ProtocolVersion = "2025-06-18"

// Bounds of one exchange.
const (
	MaxAnswer    = 1 << 20 // bytes of one answer, an event stream's whole body counted
	ListTimeout  = 20 * time.Second
	CallTimeout  = 60 * time.Second
	maxRedirects = 3
	maxToolPages = 10
	maxExcerpt   = 300
)

// Server is one listed server as the client reaches it.
type Server struct {
	URL *url.URL // the listed endpoint, as delegation keeps it
	// Header and Value are the credential: "Authorization" and "Bearer
	// <secret>", or the admin's header and the secret; "" none.
	Header, Value string
	// Secret is the credential's secret; with the URL's token-like parts
	// (Hidden) it is scrubbed out of whatever the server answers.
	Secret string
	// Private: the admin marked the server as on a private network.
	Private bool
}

// Session is an initialized session with a server: xbind's, never shown.
type Session struct {
	ID            string // Mcp-Session-Id ("" a server that keeps none)
	Version       string // the protocol version the server answered
	ServerName    string
	ServerVersion string
}

// Client makes exchanges. The zero value is ready.
type Client struct {
	// Resolver, Public, Local: as webfetch.Fetcher's (tests: a resolver of
	// their own; their servers' loopback address). Public is the rule for a
	// server that isn't on a private network; one that is takes
	// webfetch.PrivateOK.
	Resolver webfetch.Resolver
	Public   func(netip.Addr) bool
	Local    func() []netip.Addr
	// DialIP, in tests, records what is dialled (webfetch.Fetcher's).
	DialIP func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLS is the client's TLS configuration (tests: their server's CA);
	// nil: the system's roots, verified.
	TLS *tls.Config
	// MaxAnswer, Timeout: 0 the package's.
	MaxAnswer int64
	Timeout   time.Duration

	ids atomic.Int64
}

// Error is an exchange that failed. Code, Status and Msg are xbind's own
// words: nothing a server wrote is ever in them. What the server itself
// wrote is apart — Said, what the agent may read as an outsider's words
// (a JSON-RPC error's message, the method of a request it sent, where it
// redirects: scheme and host), and Page, an excerpt of its error page for
// an admin's Test alone — both scrubbed of what the server must not echo
// (Server.Hidden). Heard: an answer came back, so the server's bytes
// reached xbind (whoever reads Said or Page has read the server).
type Error struct {
	Code   string
	Status int
	Msg    string
	Said   string
	Page   string
	Heard  bool
	// RPC: the server answered a JSON-RPC error (its code): for tools/call,
	// the tool's own failure.
	RPC    bool
	RPCErr int
}

func (e *Error) Error() string { return e.Msg }

// Heard reports whether err (nil: an answer) came after the server's bytes
// reached xbind.
func Heard(err error) bool {
	var e *Error
	return err == nil || errors.As(err, &e) && e.Heard || errors.Is(err, ErrSessionGone)
}

// ErrSessionGone: the server no longer knows the session (404): open a new one.
var ErrSessionGone = errors.New("the server ended the session")

func refuse(code string, status int, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Msg: fmt.Sprintf(format, args...)}
}

// heard is e marked as coming after an answer.
func heard(e *Error) *Error { e.Heard = true; return e }

func (c *Client) maxAnswer() int64 {
	if c.MaxAnswer > 0 {
		return c.MaxAnswer
	}
	return MaxAnswer
}

// fetcher is the dialer's rule for srv.
func (c *Client) fetcher(srv Server) *webfetch.Fetcher {
	f := &webfetch.Fetcher{Resolver: c.Resolver, Public: c.Public, Local: c.Local, DialIP: c.DialIP}
	if srv.Private {
		f.Public, f.Local = webfetch.PrivateOK, func() []netip.Addr { return nil }
	}
	return f
}

// transport dials srv's host alone, through the guarded dialer.
func (c *Client) transport(srv Server) *http.Transport {
	f, want := c.fetcher(srv), strings.ToLower(srv.URL.Hostname())
	tr := &http.Transport{
		Proxy: nil, // never the environment's: a proxy dials on its own, past every check here
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil || !strings.EqualFold(strings.Trim(host, "[]"), strings.Trim(want, "[]")) {
				return nil, refuse("mcp-address", http.StatusForbidden, "%s isn't the listed server's host", host)
			}
			return f.Dial(ctx, network, addr)
		},
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	if c.TLS != nil {
		tr.TLSClientConfig = c.TLS.Clone()
	}
	return tr
}

// minHidden: a part of the URL this long is treated as a secret (a path or
// a query can hold the credential: https://…/s/<key>/mcp).
const minHidden = 12

// Hidden is what nothing the server answers may carry on: the secret, and
// every part of the URL's path and query of minHidden bytes or more.
func (srv Server) Hidden() []string {
	var out []string
	if len(srv.Secret) >= 4 {
		out = append(out, srv.Secret)
	}
	if srv.URL != nil {
		for _, seg := range strings.FieldsFunc(srv.URL.EscapedPath()+"?"+srv.URL.RawQuery, func(r rune) bool { return strings.ContainsRune("/?&=;", r) }) {
			if len(seg) >= minHidden {
				out = append(out, seg)
				if u, err := url.PathUnescape(seg); err == nil && u != seg {
					out = append(out, u)
				}
			}
		}
	}
	return out
}

// Scrub is s with every occurrence of each hidden string replaced by
// [secret] — as written, JSON-escaped (\/ too) and percent-encoded.
func Scrub(s string, hidden ...string) string {
	for _, h := range hidden {
		if len(h) < 4 {
			continue
		}
		j, _ := json.Marshal(h)
		js := string(j[1 : len(j)-1])
		for _, form := range []string{h, js, strings.ReplaceAll(js, "/", `\/`), url.QueryEscape(h), url.PathEscape(h)} {
			s = strings.ReplaceAll(s, form, "[secret]")
		}
	}
	return s
}

// scrubJSON is raw with the hidden strings scrubbed out of every string in
// it — decoded first, so an escaped spelling doesn't slip past.
func scrubJSON(raw json.RawMessage, hidden ...string) json.RawMessage {
	if len(hidden) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return json.RawMessage(`null`)
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			return Scrub(t, hidden...)
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, e := range t {
				out[Scrub(k, hidden...)] = walk(e)
			}
			return out
		}
		return x
	}
	b, err := json.Marshal(walk(v))
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}

func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// origin is how a message names a URL: its scheme and host — never its
// path or query (either can hold the credential).
func origin(u *url.URL) string { return u.Scheme + "://" + u.Host }

// exchange sends one message — a request with an id, or a notification
// without — and answers the response's result (nil for a notification).
func (c *Client) exchange(ctx context.Context, srv Server, sess *Session, method string, params any, notify bool) (json.RawMessage, http.Header, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	var id int64
	if !notify {
		id = c.ids.Add(1)
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, nil, err
	}
	tr := c.transport(srv)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u := srv.URL
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			return nil, nil, refuse("mcp-unreachable", http.StatusBadGateway, "the server's address can't be used")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("User-Agent", webfetch.UserAgent)
		if srv.Header != "" {
			req.Header.Set(srv.Header, srv.Value)
		}
		if sess != nil && sess.ID != "" {
			req.Header.Set("Mcp-Session-Id", sess.ID)
		}
		if sess != nil && sess.Version != "" {
			req.Header.Set("MCP-Protocol-Version", sess.Version)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, c.netErr(ctx, srv, err)
		}
		if loc := resp.Header.Get("Location"); resp.StatusCode >= 300 && resp.StatusCode < 400 && loc != "" {
			resp.Body.Close()
			next, err := u.Parse(loc)
			switch {
			case err != nil:
				return nil, nil, heard(refuse("mcp-redirect", http.StatusBadGateway, "the server redirects to something that isn't a URL"))
			case next.Scheme != u.Scheme || !strings.EqualFold(next.Host, u.Host):
				e := heard(refuse("mcp-redirect", http.StatusBadGateway, "the server redirects elsewhere: xbin speaks only to the listed address (%s)", origin(srv.URL)))
				e.Said = "redirects to " + Scrub(origin(next), srv.Hidden()...)
				return nil, nil, e
			case resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusPermanentRedirect:
				return nil, nil, heard(refuse("mcp-redirect", http.StatusBadGateway, "the server answers %d, moving its endpoint: an admin lists the address it moved to", resp.StatusCode))
			case hop >= maxRedirects:
				return nil, nil, heard(refuse("mcp-redirect", http.StatusBadGateway, "the server redirects more than %d times", maxRedirects))
			}
			u = next
			continue
		}
		defer resp.Body.Close()
		res, err := c.read(srv, sess, resp, id, notify)
		if err != nil && ctx.Err() != nil {
			err = c.netErr(ctx, srv, ctx.Err())
		}
		var e *Error
		if errors.As(err, &e) {
			e.Heard = true // an answer came back: the server's bytes reached xbind
		}
		return res, resp.Header, err
	}
}

// netErr is a failed request in the client's words.
func (c *Client) netErr(ctx context.Context, srv Server, err error) error {
	var r *webfetch.Refusal
	switch {
	case errors.As(err, &r):
		switch r.Code {
		case "web-address":
			why := "mark it \"on a private network\" on the workspace agent's card if it is one you run there"
			if srv.Private {
				why = "even on a private network, xbin never dials a link-local, multicast or unspecified address"
			}
			return refuse("mcp-address", http.StatusForbidden, "%s — %s", strings.TrimSuffix(strings.ReplaceAll(r.Msg, ": a workspace agent reads the public web only", ""), "."), why)
		case "web-unreachable":
			return refuse("mcp-unreachable", http.StatusBadGateway, "%s", r.Msg)
		}
		return refuse(strings.Replace(r.Code, "web-", "mcp-", 1), r.Status, "%s", r.Msg)
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded):
		return refuse("mcp-timeout", http.StatusGatewayTimeout, "%s didn't answer within the time an MCP call has", srv.URL.Host)
	case ctx.Err() != nil:
		return ctx.Err()
	}
	msg := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) {
		msg = ue.Err.Error()
	}
	var cv *tls.CertificateVerificationError
	var rec tls.RecordHeaderError
	if errors.As(err, &cv) || errors.As(err, &rec) || strings.Contains(msg, "x509:") || strings.Contains(msg, "tls:") {
		// a certificate's names are the server's words: the admin's Test shows them
		e := refuse("mcp-unreachable", http.StatusBadGateway, "%s: its TLS certificate isn't one xbin trusts for that name", srv.URL.Host)
		e.Page = clip(Scrub(msg, srv.Hidden()...), maxExcerpt)
		return e
	}
	return refuse("mcp-unreachable", http.StatusBadGateway, "%s: can't connect (%s)", srv.URL.Host, clip(Scrub(msg, srv.Hidden()...), 160))
}

// read takes the answer to the message of id.
func (c *Client) read(srv Server, sess *Session, resp *http.Response, id int64, notify bool) (json.RawMessage, error) {
	max := c.maxAnswer()
	switch {
	case resp.StatusCode == http.StatusNotFound && sess != nil && sess.ID != "":
		return nil, ErrSessionGone
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, refuse("mcp-auth", http.StatusBadGateway, "the server refused xbin's credentials (%d): an admin checks this server's secret on the workspace agent's card", resp.StatusCode)
	case resp.StatusCode == http.StatusAccepted && notify:
		return nil, nil
	case resp.StatusCode >= 300 || resp.StatusCode < 200:
		// its error page: for the admin's Test alone, scrubbed whole before it is cut
		e := refuse("mcp-unreachable", http.StatusBadGateway, "the server answered %d", resp.StatusCode)
		e.Page = c.page(resp.Body, srv)
		return nil, e
	case notify:
		return nil, nil // a notification taken with a 200: nothing to read
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch strings.ToLower(mt) {
	case "application/json":
		raw, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
		if int64(len(raw)) > max {
			return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server answered more than %d KiB", max>>10)
		}
		if err != nil {
			return nil, refuse("mcp-unreachable", http.StatusBadGateway, "the answer broke off")
		}
		return answerOf(srv, bytes.TrimSpace(raw), id)
	case "text/event-stream":
		return c.stream(srv, resp.Body, id, max)
	}
	return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server answered with a content type that is neither JSON nor an event stream")
}

// pagePeek is how much of an error page is read before it is scrubbed
// and cut: the excerpt's own bytes and room for a hidden string across the cut.
const pagePeek = 2 << 10

// page is an excerpt of an error page: read past the cut by the longest
// hidden string, scrubbed, then cut.
func (c *Client) page(body io.Reader, srv Server) string {
	hidden := srv.Hidden()
	extra := 0
	for _, h := range hidden {
		extra = max(extra, 3*len(h)) // its longest escaped form
	}
	raw, _ := io.ReadAll(io.LimitReader(body, int64(pagePeek+extra)))
	text := Scrub(strings.TrimSpace(strings.ToValidUTF8(string(raw), "")), hidden...)
	return clip(text, maxExcerpt)
}

// stream reads an event stream until the response with id: at most max
// bytes in all.
func (c *Client) stream(srv Server, body io.Reader, id int64, max int64) (json.RawMessage, error) {
	lr := &io.LimitedReader{R: body, N: max + 1}
	br := bufio.NewReaderSize(lr, 16<<10)
	var data []string
	event := ""
	dispatch := func() (json.RawMessage, bool, error) {
		defer func() { data, event = nil, "" }()
		if len(data) == 0 || event != "" && event != "message" {
			return nil, false, nil
		}
		raw := []byte(strings.Join(data, "\n"))
		var probe struct {
			ID     json.RawMessage `json:"id"`
			Method *string         `json:"method"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			return nil, false, refuse("mcp-protocol", http.StatusBadGateway, "the server's event stream carries something that isn't a JSON-RPC message")
		}
		hasID := len(probe.ID) > 0 && string(probe.ID) != "null"
		switch {
		case probe.Method != nil && hasID:
			e := refuse("mcp-protocol", http.StatusBadGateway, "the server asked xbin something mid-call: xbin's client calls tools and answers no server's requests (sampling, elicitation, roots), so the call was ended")
			e.Said = "asked " + clip(Scrub(*probe.Method, srv.Hidden()...), 80)
			return nil, false, e
		case probe.Method != nil:
			return nil, false, nil // a notification (progress, a log line): skipped
		case string(probe.ID) != fmt.Sprint(id):
			return nil, false, nil // another request's answer
		}
		res, err := answerOf(srv, raw, id)
		return res, true, err
	}
	for {
		line, err := br.ReadString('\n')
		if lr.N <= 0 {
			return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server's event stream ran past %d KiB without the answer", max>>10)
		}
		if line != "" || err == nil {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if res, done, derr := dispatch(); derr != nil || done {
					return res, derr
				}
			case strings.HasPrefix(line, ":"): // a comment
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "data":
					data = append(data, value)
				case "event":
					event = value
				}
			}
		}
		if err != nil {
			if res, done, derr := dispatch(); derr != nil || done { // a last event without its blank line
				return res, derr
			}
			return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server's event stream ended without the answer")
		}
	}
}

// answerOf reads one JSON-RPC response to id: its result, or its error.
func answerOf(srv Server, raw []byte, id int64) (json.RawMessage, error) {
	if len(raw) > 0 && raw[0] == '[' {
		return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server answered a batch: xbin sends one request at a time")
	}
	var rr struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rr); err != nil || rr.JSONRPC != "2.0" {
		return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server's answer isn't a JSON-RPC 2.0 response")
	}
	if string(rr.ID) != fmt.Sprint(id) {
		return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server answered another request than the one xbin sent")
	}
	if rr.Error != nil {
		e := refuse("mcp-error", http.StatusBadGateway, "the server answered an error (JSON-RPC %d)", rr.Error.Code)
		e.Said, e.RPC, e.RPCErr = clip(Scrub(rr.Error.Message, srv.Hidden()...), 1000), true, rr.Error.Code
		return nil, e
	}
	if len(rr.Result) == 0 {
		return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server's answer has no result")
	}
	return scrubJSON(rr.Result, srv.Hidden()...), nil
}

var versionForm = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Open initializes a session with srv: initialize, then the initialized
// notification.
func (c *Client) Open(ctx context.Context, srv Server) (Session, error) {
	ctx, cancel := c.bound(ctx, ListTimeout)
	defer cancel()
	res, hdr, err := c.exchange(ctx, srv, nil, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "xbin-workspace-agent", "version": "1"}}, false)
	if err != nil {
		return Session{}, err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if json.Unmarshal(res, &init) != nil || !versionForm.MatchString(init.ProtocolVersion) {
		return Session{}, refuse("mcp-protocol", http.StatusBadGateway, "the server's initialize answer names no protocol version xbin can send back")
	}
	s := Session{Version: init.ProtocolVersion, ServerName: clip(init.ServerInfo.Name, 120), ServerVersion: clip(init.ServerInfo.Version, 40)}
	if sid := hdr.Get("Mcp-Session-Id"); sid != "" {
		if len(sid) > 512 || strings.ContainsFunc(sid, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
			return Session{}, refuse("mcp-protocol", http.StatusBadGateway, "the server's session id isn't one xbin can send back")
		}
		s.ID = sid
	}
	if _, _, err := c.exchange(ctx, srv, &s, "notifications/initialized", nil, true); err != nil {
		return Session{}, err
	}
	return s, nil
}

func (c *Client) bound(ctx context.Context, def time.Duration) (context.Context, context.CancelFunc) {
	d := c.Timeout
	if d <= 0 {
		d = def
	}
	return context.WithTimeout(ctx, d)
}

// Tools lists srv's tools in sess, following its pages (at most
// maxToolPages): each tool's definition as the server wrote it, scrubbed.
func (c *Client) Tools(ctx context.Context, srv Server, sess Session) ([]map[string]any, error) {
	ctx, cancel := c.bound(ctx, ListTimeout)
	defer cancel()
	var out []map[string]any
	cursor := ""
	for page := 0; page < maxToolPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, _, err := c.exchange(ctx, srv, &sess, "tools/list", params, false)
		if err != nil {
			return nil, err
		}
		var list struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if json.Unmarshal(res, &list) != nil {
			return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server's tools/list can't be read")
		}
		for _, raw := range list.Tools {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			var def map[string]any
			if dec.Decode(&def) != nil {
				return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server's tools/list can't be read")
			}
			if name, ok := def["name"].(string); !ok || name == "" || len(name) > 200 {
				return nil, refuse("mcp-protocol", http.StatusBadGateway, "the server lists a tool without a name xbin can use")
			}
			out = append(out, def)
		}
		if list.NextCursor == "" {
			return out, nil
		}
		cursor = list.NextCursor
	}
	return out, nil
}

// Call calls tool with args in sess: the result as the server answered it,
// scrubbed.
func (c *Client) Call(ctx context.Context, srv Server, sess Session, tool string, args map[string]any) (json.RawMessage, error) {
	ctx, cancel := c.bound(ctx, CallTimeout)
	defer cancel()
	if args == nil {
		args = map[string]any{}
	}
	res, _, err := c.exchange(ctx, srv, &sess, "tools/call", map[string]any{"name": tool, "arguments": args}, false)
	return res, err
}

// Ping asks srv whether sess is alive.
func (c *Client) Ping(ctx context.Context, srv Server, sess Session) error {
	ctx, cancel := c.bound(ctx, ListTimeout)
	defer cancel()
	_, _, err := c.exchange(ctx, srv, &sess, "ping", map[string]any{}, false)
	return err
}
