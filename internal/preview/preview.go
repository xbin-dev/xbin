// Package preview renders a page of the workspace the way one person sees
// it — "see what the user sees" (plans/wsagent/s3-preview.md, plans/
// workspace-agent.md §5.5): a headless Chromium from the base rootfs, in a
// throwaway confine sandbox with an empty network namespace, talking to
// xbind over one unix socket that exists for that render alone.
//
// What this package knows: how to run the browser confined and read its
// answer. What it does not: who the person is to xbind. The Host opens the
// render's transport — a handler that already acts as the person's one-shot
// read-only session (internal/server/preview.go) — so no credential passes
// through here, into the sandbox, or back to the caller.
//
// SPIKE: limits are a per-user and a global concurrency cap and a hard
// timeout (the sandbox's init is killed, everything inside with it). The
// memory cap (a cgroup leaf per render) is production work.
package preview

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

//go:embed runner.mjs
var runnerJS []byte

const (
	mark       = "@@XBIN_PREVIEW@@"
	nodePath   = "/usr/local/node/bin/node"
	browsers   = "/usr/local/ms-playwright"
	maxPNG     = 16 << 20
	maxSide    = 4096
	defaultTTL = 45 * time.Second
)

// ErrUnavailable: this xbind cannot render previews (why is in the text).
var ErrUnavailable = errors.New("previews are unavailable")

// ErrBusy: the person (or the workspace) is at its concurrent-render cap.
var ErrBusy = errors.New("a preview is already being rendered — try again when it is done")

// Host is xbind as the renderer sees it.
type Host interface {
	// OpenPreview returns the handler one render's browser talks to: the
	// workspace as userID's one-shot read-only session, which lives at most
	// ttl. end drops the session and everything minted under it.
	OpenPreview(userID string, ttl time.Duration) (h http.Handler, end func(), err error)
}

// Request is one render. URL is the workspace's own address of the page (as
// the person's browser would open it); the sandbox reaches no other host,
// whatever the page asks for.
type Request struct {
	UserID   string
	URL      string
	Width    int
	Height   int
	WaitMs   int  // extra settle time after the page went quiet
	FullPage bool // the whole scroll height, not the viewport
}

// Entry is one console line, page error or failed request.
type Entry map[string]any

// Frame is one frame of the page (a tile in the shell).
type Frame struct {
	URL      string `json:"url"`
	Snapshot string `json:"snapshot"`
}

// Result is what a render saw.
type Result struct {
	OK             bool           `json:"ok"`
	Error          string         `json:"error,omitempty"`
	Detail         string         `json:"detail,omitempty"`
	URL            string         `json:"url"`
	Title          string         `json:"title"`
	Status         *int           `json:"status"`
	NavError       string         `json:"nav_error,omitempty"`
	Console        []Entry        `json:"console"`
	PageErrors     []Entry        `json:"page_errors"`
	RequestsFailed []Entry        `json:"requests_failed"`
	Snapshot       string         `json:"snapshot"`
	Frames         []Frame        `json:"frames"`
	TimedOut       bool           `json:"timed_out,omitempty"`
	Timing         map[string]int `json:"timing,omitempty"`
	RSSKB          int            `json:"rss_kb,omitempty"`
	PSSKB          int            `json:"pss_kb,omitempty"`
	Procs          int            `json:"procs,omitempty"`
	PNGBase64      string         `json:"png,omitempty"`
	PNG            []byte         `json:"-"`
	Width          int            `json:"width,omitempty"`
	Height         int            `json:"height,omitempty"`
	WallMs         int            `json:"wall_ms,omitempty"`
}

// Service renders previews. The zero value is unusable: set Host and Rootfs.
type Service struct {
	Host    Host
	Rootfs  string        // the base rootfs confine runs over (availability check)
	Timeout time.Duration // one render, hard; 0 = 45 s
	PerUser int           // concurrent renders per person; 0 = 1
	Total   int           // concurrent renders; 0 = 2

	mu     sync.Mutex
	byUser map[string]int
	all    int
}

// Available says why previews can't run here, or nil. A workspace without
// isolation has no sandbox to put a browser in: rendering tile code as
// xbind, on the host's network, is exactly what D78 forbids.
func (s *Service) Available() error {
	switch {
	case s == nil || s.Host == nil:
		return fmt.Errorf("%w: not set up", ErrUnavailable)
	case !confine.Isolated():
		return fmt.Errorf("%w: this xbind runs without isolation (--isolate), so there is no sandbox to run the browser in", ErrUnavailable)
	}
	for _, p := range []string{nodePath, browsers} {
		if _, err := os.Stat(filepath.Join(s.Rootfs, p)); err != nil {
			return fmt.Errorf("%w: the rootfs has no Chromium (%s is missing; the xbin image ships Playwright and Chromium)", ErrUnavailable, p)
		}
	}
	return nil
}

func (s *Service) acquire(user string) (func(), error) {
	per, total := max(s.PerUser, 1), s.Total
	if total <= 0 {
		total = 2
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byUser == nil {
		s.byUser = map[string]int{}
	}
	if s.byUser[user] >= per || s.all >= total {
		return nil, ErrBusy
	}
	s.byUser[user]++
	s.all++
	return func() {
		s.mu.Lock()
		s.all--
		if s.byUser[user]--; s.byUser[user] <= 0 {
			delete(s.byUser, user)
		}
		s.mu.Unlock()
	}, nil
}

// Render renders req.URL as req.UserID.
func (s *Service) Render(ctx context.Context, req Request) (*Result, error) {
	if err := s.Available(); err != nil {
		return nil, err
	}
	release, err := s.acquire(req.UserID)
	if err != nil {
		return nil, err
	}
	defer release()
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTTL
	}
	port, err := urlPort(req.URL)
	if err != nil {
		return nil, err
	}
	h, end, err := s.Host.OpenPreview(req.UserID, timeout+5*time.Second)
	if err != nil {
		return nil, err
	}
	defer end()

	dir, sock, stop, err := Serve(h)
	if err != nil {
		return nil, err
	}
	defer stop()
	start := time.Now()
	res, err := run(ctx, dir, timeout, map[string]any{
		"url": req.URL, "sock": sock, "port": port,
		"viewport":  map[string]int{"width": clamp(req.Width, 240, 2560, 1280), "height": clamp(req.Height, 240, 2560, 800)},
		"wait_ms":   clamp(req.WaitMs, 0, 15000, 0),
		"budget_ms": int((timeout - 3*time.Second) / time.Millisecond),
		"full_page": req.FullPage,
	})
	if err != nil {
		return nil, err
	}
	res.WallMs = int(time.Since(start) / time.Millisecond)
	return res, nil
}

// Serve serves h on a unix socket in a fresh private directory that also
// holds the runner: the directory is what a render's sandbox gets, read-only.
// stop closes the listener, its connections, and removes the directory.
func Serve(h http.Handler) (dir, sock string, stop func(), err error) {
	if dir, err = os.MkdirTemp("", "xbin-preview-*"); err != nil {
		return "", "", nil, err
	}
	if err = os.Chmod(dir, 0o755); err != nil { // #nosec: the sandbox's uid must traverse it; the socket's mode is the gate
		_ = os.RemoveAll(dir)
		return "", "", nil, err
	}
	if err = os.WriteFile(filepath.Join(dir, "runner.mjs"), runnerJS, 0o444); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", nil, err
	}
	sock = filepath.Join(dir, "x.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return dir, sock, func() {
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	}, nil
}

// run executes the runner in a throwaway sandbox: nothing bound in but dir
// (read-only: the runner and the socket), an empty network namespace, no
// capabilities. The answer comes over stdout only — xbind opens no file the
// sandbox could have written.
func run(ctx context.Context, dir string, timeout time.Duration, req map[string]any) (*Result, error) {
	arg, _ := json.Marshal(req)
	out, err := confine.Run(ctx, confine.Cmd{
		Argv:      []string{nodePath, filepath.Join(dir, "runner.mjs"), string(arg)},
		Binds:     []sandbox.Bind{confine.RO(dir)},
		Env:       []string{"PLAYWRIGHT_BROWSERS_PATH=" + browsers, "HOME=/tmp", "XDG_RUNTIME_DIR=/tmp"},
		Net:       confine.NetNone,
		Timeout:   timeout,
		MaxOutput: 2 * maxPNG,
	})
	i := bytes.LastIndex(out.Stdout, []byte(mark))
	if i < 0 {
		if errors.Is(err, confine.ErrUnavailable) {
			return nil, fmt.Errorf("%w: the sandbox would not start: %v", ErrUnavailable, err)
		}
		return nil, fmt.Errorf("the preview browser gave no answer: %v: %s", err, clip(out.Stderr, 600))
	}
	line := out.Stdout[i+len(mark):]
	if j := bytes.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	var res Result
	if err := json.Unmarshal(line, &res); err != nil {
		return nil, fmt.Errorf("the preview browser's answer is unreadable: %v", err)
	}
	if !res.OK {
		if res.Error == "browser-missing" || res.Error == "playwright-missing" {
			return nil, fmt.Errorf("%w: %s: %s", ErrUnavailable, res.Error, res.Detail)
		}
		return &res, nil
	}
	if res.PNGBase64 != "" {
		// The picture was drawn by a browser running untrusted pages: take
		// it only as a PNG of sane size, re-encoded here (no stray chunks).
		raw, err := base64.StdEncoding.DecodeString(res.PNGBase64)
		res.PNGBase64 = ""
		if err != nil || len(raw) > maxPNG {
			return nil, errors.New("the preview browser's picture is unreadable or too large")
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil || cfg.Width > maxSide || cfg.Height > 8*maxSide {
			return nil, errors.New("the preview browser's picture is not a PNG of a sane size")
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, errors.New("the preview browser's picture does not decode")
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		res.PNG, res.Width, res.Height = buf.Bytes(), cfg.Width, cfg.Height
	}
	res.scrub()
	return &res, nil
}

// Credentials the page's URLs carry — a frame token (?frame=), the tile-
// origin exchange's parameters, an asset token or a path ticket in a path —
// never reach the caller. All of them die with the render's session, before
// Render returns; this keeps even the dead ones out of a model's context.
var (
	secretQuery = regexp.MustCompile(`([?&](?:frame|xbin_ticket|xbin_begin|xbin_state)=)[^&#\s"']+`)
	secretPath  = regexp.MustCompile(`(/(?:c|api)/~)[^/\s"']+`)
)

func scrubText(s string) string {
	return secretPath.ReplaceAllString(secretQuery.ReplaceAllString(s, "${1}…"), "${1}…")
}

func (r *Result) scrub() {
	r.URL, r.Snapshot, r.NavError = scrubText(r.URL), scrubText(r.Snapshot), scrubText(r.NavError)
	for i := range r.Frames {
		r.Frames[i].URL, r.Frames[i].Snapshot = scrubText(r.Frames[i].URL), scrubText(r.Frames[i].Snapshot)
	}
	for _, list := range [][]Entry{r.Console, r.PageErrors, r.RequestsFailed} {
		for _, e := range list {
			for k, v := range e {
				if s, ok := v.(string); ok {
					e[k] = scrubText(s)
				}
			}
		}
	}
}

func clamp(v, lo, hi, def int) int {
	if v == 0 {
		return def
	}
	return min(max(v, lo), hi)
}

func clip(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

// urlPort is the port the sandbox's forwarder listens on for raw: the URL's
// own, so a loopback workspace URL works unchanged inside.
func urlPort(raw string) (int, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return 0, fmt.Errorf("not a page address: %q", raw)
	}
	if p := u.Port(); p != "" {
		n, err := net.LookupPort("tcp", p)
		if err != nil || n < 1024 {
			return 0, fmt.Errorf("the preview browser can't open port %s", p)
		}
		return n, nil
	}
	return 18080, nil // a default-port URL goes through the resolver rule (map_all)
}
