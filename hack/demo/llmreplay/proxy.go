package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxReqBody  = 64 << 20
	maxRespBody = 256 << 20

	// laneHeader picks a request's lane when its path doesn't (/lane/<name>/…);
	// it never reaches the upstream.
	laneHeader  = "X-Replay-Lane"
	defaultLane = "default"

	controlPrefix = "/_llmreplay/"
)

var laneRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type options struct {
	mode       string // record | replay | passthrough
	listen     string
	cassette   string
	appendRec  bool          // record: add to an existing cassette
	overwrite  bool          // record: replace an existing cassette
	timing     float64       // replay: every recorded pause × this (1 original, 0 instant)
	maxWait    time.Duration // replay: no single pause longer than this (0: no cap)
	drift      float64       // replay: a match less similar than this is reported as drift
	window     int           // replay: how far past the take's furthest exchange a fallback match may reach
	onMiss     string        // replay: error | passthrough
	keepErrors bool          // replay: replay recorded 429s, 5xxs and failed calls too
	token      string        // callers must present it as their API key (LLMREPLAY_TOKEN); "" = anyone who reaches the address
}

// upstream is where record and passthrough send a wire's calls.
type upstream struct {
	name   string // openai | anthropic
	base   string
	key    string // "" = pass the caller's own credentials through (when there is no proxy token)
	keyEnv string // the variable it came from, for the startup line (never the value)
	header string // Authorization (as a Bearer token) or X-Api-Key
}

type upstreams struct{ openai, anthropic upstream }

// upstreamsFromEnv reads the upstreams: LLMREPLAY_OPENAI_BASE_URL (default
// https://api.openai.com/v1; any OpenAI-compatible provider) with
// LLMREPLAY_OPENAI_API_KEY or OPENAI_API_KEY, and LLMREPLAY_ANTHROPIC_BASE_URL
// (default https://api.anthropic.com) with LLMREPLAY_ANTHROPIC_API_KEY or
// ANTHROPIC_API_KEY (sent as x-api-key), else ANTHROPIC_AUTH_TOKEN (Bearer).
// The base URLs have names of their own so a shell that points Claude Code
// at this proxy (ANTHROPIC_BASE_URL) never points the proxy at itself.
func upstreamsFromEnv(getenv func(string) string) upstreams {
	pick := func(names ...string) (string, string) {
		for _, n := range names {
			if v := strings.TrimSpace(getenv(n)); v != "" {
				return v, n
			}
		}
		return "", ""
	}
	u := upstreams{
		openai:    upstream{name: "openai", base: "https://api.openai.com/v1", header: "Authorization"},
		anthropic: upstream{name: "anthropic", base: "https://api.anthropic.com", header: "X-Api-Key"},
	}
	if v, _ := pick("LLMREPLAY_OPENAI_BASE_URL"); v != "" {
		u.openai.base = v
	}
	if v, _ := pick("LLMREPLAY_ANTHROPIC_BASE_URL"); v != "" {
		u.anthropic.base = v
	}
	u.openai.key, u.openai.keyEnv = pick("LLMREPLAY_OPENAI_API_KEY", "OPENAI_API_KEY")
	u.anthropic.key, u.anthropic.keyEnv = pick("LLMREPLAY_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY")
	if u.anthropic.key == "" {
		if k, env := pick("ANTHROPIC_AUTH_TOKEN"); k != "" {
			u.anthropic.key, u.anthropic.keyEnv, u.anthropic.header = k, env, "Authorization"
		}
	}
	return u
}

type server struct {
	opt    options
	ups    upstreams
	log    *log.Logger
	client *http.Client
	rec    *recorder // record mode
	play   *player   // replay mode

	seqMu sync.Mutex
	seq   map[string]int // record: the next Seq per lane

	// sleep waits d unless ctx ends first (false then); replay's clock, which
	// tests replace.
	sleep func(ctx context.Context, d time.Duration) bool
}

func newServer(opt options, ups upstreams, logger *log.Logger) *server {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true // Accept-Encoding: identity — every event as the upstream flushed it
	return &server{opt: opt, ups: ups, log: logger, seq: map[string]int{}, sleep: realSleep,
		client: &http.Client{Transport: tr}} // no timeout: a call lives as long as its caller waits
}

func realSleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeAPIError(w, r, r.URL.Path, http.StatusUnauthorized, "llmreplay: this proxy wants its token (LLMREPLAY_TOKEN) as the API key")
		return
	}
	if strings.HasPrefix(r.URL.Path, controlPrefix) {
		s.control(w, r)
		return
	}
	lane, path, err := laneOf(r)
	if err != nil {
		writeAPIError(w, r, r.URL.Path, http.StatusBadRequest, "llmreplay: "+err.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReqBody))
	if err != nil {
		writeAPIError(w, r, path, http.StatusRequestEntityTooLarge, "llmreplay: reading the request: "+err.Error())
		return
	}
	switch s.opt.mode {
	case "record":
		s.forward(w, r, lane, path, body, true)
	case "passthrough":
		s.forward(w, r, lane, path, body, false)
	default:
		s.replay(w, r, lane, path, body)
	}
}

// laneOf splits a request's lane from its path: /lane/<name>/v1/… (what a
// base URL can carry — llm-gw's backend URL, ANTHROPIC_BASE_URL), else the
// X-Replay-Lane header (ANTHROPIC_CUSTOM_HEADERS), else "default".
func laneOf(r *http.Request) (lane, path string, err error) {
	path = r.URL.Path
	if rest, ok := strings.CutPrefix(path, "/lane/"); ok {
		name, tail, _ := strings.Cut(rest, "/")
		if !laneRe.MatchString(name) {
			return "", "", fmt.Errorf("bad lane %q in the path (letters, digits, . _ -)", name)
		}
		return name, "/" + tail, nil
	}
	if h := strings.TrimSpace(r.Header.Get(laneHeader)); h != "" {
		if !laneRe.MatchString(h) {
			return "", "", fmt.Errorf("bad lane %q in %s (letters, digits, . _ -)", h, laneHeader)
		}
		return h, path, nil
	}
	return defaultLane, path, nil
}

func (s *server) authorized(r *http.Request) bool {
	if s.opt.token == "" {
		return true
	}
	for _, v := range []string{r.Header.Get("X-Api-Key"), bearer(r.Header.Get("Authorization")), bearer(r.Header.Get("Proxy-Authorization"))} {
		if v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(s.opt.token)) == 1 {
			return true
		}
	}
	return false
}

func bearer(v string) string {
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

// anthropicStyle: the caller speaks the Messages API (its errors take that
// shape, and record sends it to the Anthropic upstream).
func anthropicStyle(r *http.Request, path string) bool {
	return r.Header.Get("Anthropic-Version") != "" || r.Header.Get("X-Api-Key") != "" ||
		apiOf(path) == apiMessages || strings.HasPrefix(path, "/v1/messages") || strings.HasPrefix(path, "/api/")
}

func (s *server) upstreamFor(r *http.Request, path string) *upstream {
	if anthropicStyle(r, path) {
		return &s.ups.anthropic
	}
	return &s.ups.openai
}

// joinURL joins a base URL and a /v1/… path without doubling /v1 (bases are
// given both ways: https://api.openai.com/v1, https://api.anthropic.com).
func joinURL(base, path, rawQuery string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	if rawQuery != "" {
		return b + path + "?" + rawQuery
	}
	return b + path
}

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// outboundHeaders are a request's headers as the upstream gets them: no
// hop-by-hop headers, no lane, no cookies and no credentials (setAuth
// decides those).
func outboundHeaders(in http.Header) http.Header {
	out := in.Clone()
	for _, f := range strings.Split(in.Get("Connection"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			out.Del(f)
		}
	}
	for _, h := range append(hopHeaders, "Host", "Content-Length", "Accept-Encoding", "Cookie", laneHeader, "Authorization", "X-Api-Key") {
		out.Del(h)
	}
	return out
}

// setAuth puts the upstream's key on an outbound request — or, with no key
// configured and no proxy token, the caller's own credentials (a signed-in
// Claude Code, a gateway's token): the proxy then holds no key at all.
func (s *server) setAuth(out, in *http.Request, up *upstream) {
	switch {
	case up.key != "":
		if up.header == "Authorization" {
			out.Header.Set("Authorization", "Bearer "+up.key)
		} else {
			out.Header.Set("X-Api-Key", up.key)
		}
		// a subscription sign-in's beta flag says "OAuth token", which this key isn't
		if b := out.Header.Get("Anthropic-Beta"); b != "" {
			var keep []string
			for _, f := range strings.Split(b, ",") {
				if f = strings.TrimSpace(f); f != "" && !strings.HasPrefix(f, "oauth-") {
					keep = append(keep, f)
				}
			}
			if len(keep) == 0 {
				out.Header.Del("Anthropic-Beta")
			} else {
				out.Header.Set("Anthropic-Beta", strings.Join(keep, ","))
			}
		}
	case s.opt.token == "":
		if v := in.Header.Get("Authorization"); v != "" {
			out.Header.Set("Authorization", v)
		}
		if v := in.Header.Get("X-Api-Key"); v != "" {
			out.Header.Set("X-Api-Key", v)
		}
	}
}

func (s *server) nextSeq(lane string) int {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	n := s.seq[lane]
	s.seq[lane] = n + 1
	return n
}

// forward sends a call to its upstream and relays the answer as it comes —
// an event stream event by event, each flushed — recording the exchange
// (with every event's arrival time) when record is set.
func (s *server) forward(w http.ResponseWriter, r *http.Request, lane, path string, body []byte, record bool) {
	up := s.upstreamFor(r, path)
	ctx := r.Context()
	ex := &Exchange{Lane: lane, Seq: -1, Method: r.Method, Path: path, Query: r.URL.RawQuery, ReqHeaders: keepHeaders(r.Header, reqHeaderKeep)}
	ex.setReqBody(body)
	if record {
		ex.Seq = s.nextSeq(lane)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, joinURL(up.base, path, r.URL.RawQuery), bytes.NewReader(body))
	if err != nil {
		writeAPIError(w, r, path, http.StatusBadGateway, "llmreplay: "+err.Error())
		return
	}
	req.Header = outboundHeaders(r.Header)
	req.Header.Set("Accept-Encoding", "identity")
	s.setAuth(req, r, up)
	call := bodyLabel(r.Method, path, body) // not a digest: nothing slow before the upstream call
	start := time.Now()
	ex.Started = start.UTC()
	resp, err := s.client.Do(req)
	if err != nil {
		ex.HeadMs, ex.Error, ex.Aborted = msSince(start), err.Error(), ctx.Err() != nil
		if !ex.Aborted {
			writeAPIError(w, r, path, http.StatusBadGateway, fmt.Sprintf("llmreplay: the %s upstream: %v", up.name, err))
		}
		s.logf("%s %s: %s failed: %v", s.verb(record), laneRef(lane, ex.Seq), call, err)
		s.save(record, ex)
		return
	}
	defer resp.Body.Close()
	ex.HeadMs, ex.Status = msSince(start), resp.StatusCode
	var rd io.Reader = resp.Body
	decoded := false
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		if gz, err := gzip.NewReader(resp.Body); err == nil {
			rd, decoded = gz, true
		}
	}
	ex.Headers = keepHeaders(resp.Header, respHeaderKeep)
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && !decoded && !strings.EqualFold(ce, "identity") {
		if ex.Headers == nil {
			ex.Headers = http.Header{}
		}
		ex.Headers.Set("Content-Encoding", ce) // replayed bytes need it too
	}
	for k, vs := range resp.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Set-Cookie", "Alt-Svc", "Connection", "Keep-Alive", "Transfer-Encoding", "Trailer", "Upgrade", "Proxy-Authenticate":
			continue
		case "Content-Encoding":
			if decoded {
				continue
			}
		}
		w.Header()[k] = vs
	}
	if isEventStream(resp.Header) {
		w.WriteHeader(resp.StatusCode)
		flush(w)
		er := &eventReader{br: bufio.NewReaderSize(rd, 64<<10)}
		last, gone := time.Now(), false
		for {
			ev, rerr := er.next()
			if len(ev) > 0 {
				now := time.Now()
				ex.Chunks = append(ex.Chunks, newChunk(ms(now.Sub(last)), ev))
				last = now
				if _, werr := w.Write(ev); werr != nil {
					gone = true
				} else {
					flush(w)
				}
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					if ctx.Err() != nil {
						ex.Aborted = true
					} else {
						ex.Error = "the stream broke off: " + rerr.Error()
					}
				}
				break
			}
			if gone {
				ex.Aborted = true
				break
			}
		}
		ex.BodyMs = msSince(last)
	} else {
		b, rerr := io.ReadAll(io.LimitReader(rd, maxRespBody))
		ex.setBody(b)
		ex.BodyMs = msSince(start) - ex.HeadMs
		if rerr != nil {
			if ctx.Err() != nil {
				ex.Aborted = true
			} else {
				ex.Error = "the body broke off: " + rerr.Error()
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(b)
	}
	s.logf("%s %s: %s → %s", s.verb(record), laneRef(lane, ex.Seq), call, describeAnswer(ex))
	s.save(record, ex)
}

func (s *server) verb(record bool) string {
	if record {
		return "rec "
	}
	return "pass"
}

func (s *server) save(record bool, ex *Exchange) {
	if !record || s.rec == nil {
		return
	}
	if err := s.rec.add(ex); err != nil {
		s.logf("ERROR writing the cassette: %v", err)
	}
}

func (s *server) logf(format string, args ...any) {
	if s.log != nil {
		s.log.Printf(format, args...)
	}
}

// eventReader reads a server-sent event stream event by event: each next()
// is one event's bytes as received, its blank-line terminator included
// (blank lines before an event ride with it), and an unterminated end of
// stream is the last one.
type eventReader struct{ br *bufio.Reader }

func (e *eventReader) next() ([]byte, error) {
	var ev []byte
	content := false
	for {
		line, err := e.br.ReadBytes('\n')
		ev = append(ev, line...)
		if err != nil {
			return ev, err
		}
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			if content {
				return ev, nil
			}
			continue
		}
		content = true
	}
}

func flush(w http.ResponseWriter) { _ = http.NewResponseController(w).Flush() }

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func msSince(t time.Time) float64 { return ms(time.Since(t)) }

func laneRef(lane string, seq int) string {
	if seq < 0 {
		return lane
	}
	return fmt.Sprintf("%s #%d", lane, seq)
}

// describeCall is a call in a log line: method, path, model, stream.
func (d *digest) describeCall() string { return label(d.method, d.path, d.model, d.stream) }

// bodyLabel is describeCall from a body, reading only its model and stream.
func bodyLabel(method, path string, body []byte) string {
	var b struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &b)
	return label(method, path, b.Model, b.Stream)
}

func label(method, path, model string, stream bool) string {
	s := method + " " + path
	if model != "" {
		s += " " + model
	}
	if stream {
		s += " stream"
	}
	return s
}

// describeAnswer is a response in a log line.
func describeAnswer(ex *Exchange) string {
	s := fmt.Sprintf("%d after %s", ex.Status, secs(ex.HeadMs))
	if ex.stream() {
		total := ex.BodyMs
		for _, c := range ex.Chunks {
			total += c.Ms
		}
		s += fmt.Sprintf(", %d events over %s", len(ex.Chunks), secs(total))
	} else {
		s += fmt.Sprintf(", %d bytes", len(ex.body()))
	}
	if ex.Error != "" {
		s += " (" + ex.Error + ")"
	}
	if ex.Aborted {
		s += " (the caller hung up)"
	}
	return s
}

func secs(msv float64) string { return strconv.FormatFloat(msv/1000, 'f', 2, 64) + "s" }

// writeAPIError answers in the caller's wire's error shape, so an agent shows
// the message rather than a parse error.
func writeAPIError(w http.ResponseWriter, r *http.Request, path string, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var v any
	if anthropicStyle(r, path) {
		typ := "api_error"
		switch status {
		case http.StatusUnauthorized:
			typ = "authentication_error"
		case http.StatusBadRequest, http.StatusNotFound:
			typ = "invalid_request_error"
		}
		v = map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": message}}
	} else {
		v = map[string]any{"error": map[string]any{"message": message, "type": "llmreplay_error", "code": "llmreplay"}}
	}
	_ = json.NewEncoder(w).Encode(v)
}

// --- control -----------------------------------------------------------------

// control serves GET /_llmreplay/status and POST /_llmreplay/reset[?lane=]
// (replay: start the take over — every exchange unused again).
func (s *server) control(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == controlPrefix+"status" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, s.status())
	case r.URL.Path == controlPrefix+"reset" && r.Method == http.MethodPost:
		if s.play == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "not replaying"})
			return
		}
		lane := r.URL.Query().Get("lane")
		s.play.reset(lane)
		s.logf("reset: %s starts over", or(lane, "every lane"))
		writeJSON(w, http.StatusOK, s.status())
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "GET " + controlPrefix + "status, POST " + controlPrefix + "reset[?lane=]"})
	}
}

type laneStatus struct {
	Recorded int      `json:"recorded,omitempty"` // record: exchanges written
	Calls    int      `json:"calls,omitempty"`    // replay: model calls in the cassette
	Served   int      `json:"served"`             // replay: … answered this take
	Left     int      `json:"left"`               // replay: … not yet
	Next     []int    `json:"next,omitempty"`     // replay: the first unused seqs
	Misses   int      `json:"misses,omitempty"`
	Drifts   []string `json:"drifts,omitempty"`
}

type statusOut struct {
	Mode     string                `json:"mode"`
	PID      int                   `json:"pid"`
	Cassette string                `json:"cassette,omitempty"`
	Timing   *float64              `json:"timing,omitempty"`
	Lanes    map[string]laneStatus `json:"lanes"`
}

func (s *server) status() statusOut {
	st := statusOut{Mode: s.opt.mode, PID: os.Getpid(), Cassette: s.opt.cassette, Lanes: map[string]laneStatus{}}
	switch {
	case s.rec != nil:
		for lane, n := range s.rec.counts() {
			st.Lanes[lane] = laneStatus{Recorded: n}
		}
	case s.play != nil:
		t := s.opt.timing
		st.Timing = &t
		st.Lanes = s.play.status()
	}
	return st
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
