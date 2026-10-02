package main

// A cassette is a JSON Lines file: a header line, then one line per
// exchange, appended (and synced) as each exchange ends — a recording cut
// short keeps every exchange that finished. Exchanges land in the order they
// ended; Seq says the order they arrived in, per lane, and loading sorts by
// it.
//
// It holds request bodies and model output verbatim (prompts, file contents
// an agent read, tool output), never a credential: request headers are kept
// from a short allowlist without Authorization, x-api-key or cookies, and so
// are response headers. README.md §Security.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const cassetteVersion = 1

// header is a cassette's first line.
type header struct {
	LLMReplay int       `json:"llmreplay"` // the format version
	Created   time.Time `json:"created"`
	Note      string    `json:"note,omitempty"`
}

// Exchange is one request and the response it got.
type Exchange struct {
	Lane    string    `json:"lane"`
	Seq     int       `json:"seq"` // arrival order within the lane, from 0
	Started time.Time `json:"started"`
	Method  string    `json:"method"`
	Path    string    `json:"path"` // without the lane prefix
	Query   string    `json:"query,omitempty"`

	ReqHeaders http.Header     `json:"reqHeaders,omitempty"` // allowlisted (reqHeaderKeep)
	ReqBody    json.RawMessage `json:"reqBody,omitempty"`    // a JSON body
	ReqText    string          `json:"reqText,omitempty"`    // any other body

	Status  int         `json:"status"`
	Error   string      `json:"error,omitempty"`   // the upstream failed (no response, or a stream cut off)
	Headers http.Header `json:"headers,omitempty"` // allowlisted (respHeaderKeep)
	HeadMs  float64     `json:"headMs"`            // request sent → response headers
	Chunks  []Chunk     `json:"chunks,omitempty"`  // an event stream, event by event
	Body    string      `json:"body,omitempty"`    // any other body, verbatim
	BodyEnc string      `json:"bodyEnc,omitempty"` // "base64" when Body isn't UTF-8
	BodyMs  float64     `json:"bodyMs,omitempty"`  // headers → body end; for a stream, last event → its end
	Aborted bool        `json:"aborted,omitempty"` // the caller hung up before the response ended
}

// Chunk is one server-sent event as received, its blank-line terminator
// included.
type Chunk struct {
	Ms   float64 `json:"ms"` // since the previous event (the first: since the headers)
	Data string  `json:"data"`
	Enc  string  `json:"enc,omitempty"` // "base64" when Data isn't UTF-8
}

func newChunk(ms float64, b []byte) Chunk {
	if utf8.Valid(b) {
		return Chunk{Ms: ms, Data: string(b)}
	}
	return Chunk{Ms: ms, Data: base64.StdEncoding.EncodeToString(b), Enc: "base64"}
}

func (c Chunk) bytes() []byte {
	if c.Enc == "base64" {
		b, _ := base64.StdEncoding.DecodeString(c.Data)
		return b
	}
	return []byte(c.Data)
}

func (ex *Exchange) setBody(b []byte) {
	if utf8.Valid(b) {
		ex.Body, ex.BodyEnc = string(b), ""
		return
	}
	ex.Body, ex.BodyEnc = base64.StdEncoding.EncodeToString(b), "base64"
}

func (ex *Exchange) body() []byte {
	if ex.BodyEnc == "base64" {
		b, _ := base64.StdEncoding.DecodeString(ex.Body)
		return b
	}
	return []byte(ex.Body)
}

func (ex *Exchange) setReqBody(b []byte) {
	if len(bytes.TrimSpace(b)) == 0 {
		return
	}
	if json.Valid(b) {
		ex.ReqBody = append(json.RawMessage(nil), b...)
		return
	}
	ex.ReqText = string(b)
}

func (ex *Exchange) reqBody() []byte {
	if len(ex.ReqBody) > 0 {
		return ex.ReqBody
	}
	return []byte(ex.ReqText)
}

// stream: the response was an event stream.
func (ex *Exchange) stream() bool {
	return len(ex.Chunks) > 0 || isEventStream(ex.Headers)
}

// transient: a failure a client retries (429, 5xx, no response at all) — the
// upstream's hiccup, not the agent's behaviour; replay skips them unless
// -keep-errors. A call its caller gave up on isn't one: the caller hung up,
// and the replay keeps that.
func (ex *Exchange) transient() bool {
	if ex.Aborted {
		return false
	}
	return ex.Status == 0 || ex.Status == http.StatusTooManyRequests || ex.Status >= 500
}

func isEventStream(h http.Header) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(h.Get("Content-Type"))), "text/event-stream")
}

// reqHeaderKeep are the request headers a cassette keeps (for inspection;
// matching reads the body only).
var reqHeaderKeep = []string{"Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta", "Openai-Beta", "User-Agent", "X-App"}

// respHeaderKeep are the response headers a cassette keeps and replays:
// what a client may read off a model call. Prefixes end in "-".
var respHeaderKeep = []string{"Content-Type", "Cache-Control", "Retry-After", "X-Should-Retry", "Request-Id", "X-Request-Id",
	"Openai-Processing-Ms", "Openai-Version", "Anthropic-Ratelimit-", "X-Ratelimit-"}

func keepHeaders(h http.Header, keep []string) http.Header {
	out := http.Header{}
	for k, vs := range h {
		ck := http.CanonicalHeaderKey(k)
		for _, want := range keep {
			if ck == want || (strings.HasSuffix(want, "-") && strings.HasPrefix(ck, want)) {
				out[ck] = append([]string(nil), vs...)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- recording ---------------------------------------------------------------

type recorder struct {
	mu   sync.Mutex
	f    *os.File
	path string
	n    map[string]int // exchanges written, per lane
}

// openRecorder opens path for recording: a new file (0600, its directory
// 0700), or, with appendTo, an existing one whose lanes' sequences go on
// (nextSeq); overwrite replaces an existing file. Without either, an
// existing file is an error: a take is never recorded over by accident.
func openRecorder(path string, appendTo, overwrite bool) (*recorder, map[string]int, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	next := map[string]int{}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	_, statErr := os.Stat(path)
	exists := statErr == nil
	switch {
	case exists && appendTo:
		c, err := loadCassette(path)
		if err != nil {
			return nil, nil, fmt.Errorf("appending to %s: %w", path, err)
		}
		for _, ex := range c.Exchanges {
			next[ex.Lane] = max(next[ex.Lane], ex.Seq+1)
		}
		flags = os.O_WRONLY | os.O_APPEND
	case exists && overwrite:
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	case exists:
		return nil, nil, fmt.Errorf("%s exists: pass -append to add to it, or -overwrite to record over it", path)
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, nil, err
	}
	r := &recorder{f: f, path: path, n: map[string]int{}}
	if !(exists && appendTo) {
		if err := r.writeLine(header{LLMReplay: cassetteVersion, Created: time.Now().UTC().Truncate(time.Second)}); err != nil {
			f.Close()
			return nil, nil, err
		}
	}
	return r, next, nil
}

func (r *recorder) writeLine(v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Write(b.Bytes()); err != nil {
		return err
	}
	return r.f.Sync()
}

func (r *recorder) add(ex *Exchange) error {
	if err := r.writeLine(ex); err != nil {
		return err
	}
	r.mu.Lock()
	r.n[ex.Lane]++
	r.mu.Unlock()
	return nil
}

func (r *recorder) counts() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.n))
	for k, v := range r.n {
		out[k] = v
	}
	return out
}

func (r *recorder) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// --- loading -----------------------------------------------------------------

type cassette struct {
	Header    header
	Exchanges []*Exchange // by lane, then Seq
	Skipped   int         // unreadable lines (a recording killed mid-write)
}

func loadCassette(path string) (*cassette, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := &cassette{}
	br := bufio.NewReaderSize(f, 1<<20)
	for n := 1; ; n++ {
		line, rerr := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			if n == 1 {
				if err := json.Unmarshal(line, &c.Header); err != nil || c.Header.LLMReplay == 0 {
					return nil, fmt.Errorf("%s is not an llmreplay cassette (line 1)", path)
				}
				if c.Header.LLMReplay > cassetteVersion {
					return nil, fmt.Errorf("%s is cassette format %d; this llmreplay reads up to %d", path, c.Header.LLMReplay, cassetteVersion)
				}
			} else {
				ex := &Exchange{}
				if err := json.Unmarshal(line, ex); err != nil || ex.Lane == "" {
					c.Skipped++
				} else {
					c.Exchanges = append(c.Exchanges, ex)
				}
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, rerr
		}
	}
	if c.Header.LLMReplay == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	sort.SliceStable(c.Exchanges, func(i, j int) bool {
		a, b := c.Exchanges[i], c.Exchanges[j]
		if a.Lane != b.Lane {
			return a.Lane < b.Lane
		}
		return a.Seq < b.Seq
	})
	return c, nil
}
