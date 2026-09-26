package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The browser half of the native commands: bx runs its embedded
// native-probe.mjs with node, and the probe drives headless Chromium through
// Playwright against a loopback proxy in front of xbind.

//go:embed native-probe.mjs
var nativeProbeJS []byte

// probeConfig is native-probe.mjs's input (see the header there).
type probeConfig struct {
	Base    string          `json:"base"`
	Mode    string          `json:"mode"`
	Tiles   []string        `json:"tiles"`
	Theme   string          `json:"theme,omitempty"`
	Text    string          `json:"text,omitempty"`
	Width   int             `json:"width"`
	Height  int             `json:"height"`
	Scale   int             `json:"scale"`
	Out     string          `json:"out,omitempty"`
	Full    bool            `json:"full,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Steps   json.RawMessage `json:"steps,omitempty"`
	Timeout int64           `json:"timeout"`
	Settle  int64           `json:"settle"`
}

type probeMsg struct {
	Kind    string `json:"kind,omitempty"`
	Level   string `json:"level,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Where   string `json:"where,omitempty"`
}

// probeResult is one tile's headless run.
type probeResult struct {
	Tile        string          `json:"tile"`
	OK          bool            `json:"ok"`
	Status      int             `json:"status"`
	LoadError   string          `json:"loadError,omitempty"`
	Tree        json.RawMessage `json:"tree,omitempty"`
	FirstTreeMs *float64        `json:"firstTreeMs"`
	Settled     bool            `json:"settled"`
	Stats       struct {
		Nodes int            `json:"nodes"`
		Depth int            `json:"depth"`
		Bytes int            `json:"bytes"`
		Prims map[string]int `json:"prims"`
	} `json:"stats"`
	Unknown     []string       `json:"unknown"`
	Needs       map[string]int `json:"needs"`
	Features    []string       `json:"features"`
	Errors      []probeMsg     `json:"errors"`
	Diagnostics []probeMsg     `json:"diagnostics"`
	Console     []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"console"`
	PageErrors []string          `json:"pageErrors"`
	Requests   []json.RawMessage `json:"requests,omitempty"`
	Unmatched  []string          `json:"unmatched,omitempty"`
	Warnings   []string          `json:"warnings"`
	Shot       string            `json:"shot,omitempty"`
}

// errNoBrowser: node, Playwright or Chromium is missing — lint degrades to
// its static checks; tree and preview can't run.
type errNoBrowser struct{ why string }

func (e *errNoBrowser) Error() string {
	return "headless run unavailable: " + e.why
}

// runProbe runs the embedded probe over the tiles and returns their results
// in order. known is every component path (for the proxy's lending rule).
//
// One tile at a time, each in a browser of its own behind a proxy of its
// own that lends bx's credential to THAT tile's document and files only:
// one proxy for the whole run lent it to every probed tile's paths whichever
// page asked, so while one tile's page ran it could raw-fetch another
// probed tile's runtime document (/c/<other>/?native=1) with bx's
// credential and lift that tile's frame token out of it — ACAO: null lets
// an opaque page read the answer. A tile's page never outlives its run.
func runProbe(cfg probeConfig, known []string) ([]probeResult, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, &errNoBrowser{"node was not found on PATH (the terminal rootfs ships node and Playwright's Chromium; elsewhere install node, then `npm i -g playwright && npx playwright install chromium`, or set PLAYWRIGHT_DIR)"}
	}
	dir, err := os.MkdirTemp("", "bx-native-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "native-probe.mjs")
	if err := os.WriteFile(script, nativeProbeJS, 0o600); err != nil {
		return nil, err
	}
	var out []probeResult
	for i, tile := range cfg.Tiles {
		one := cfg
		one.Tiles = []string{tile}
		res, err := probeTile(node, script, filepath.Join(dir, fmt.Sprintf("config-%d.json", i)), one, known)
		if err != nil {
			return nil, err
		}
		out = append(out, res...)
	}
	return out, nil
}

// probeTile runs the probe for cfg's one tile behind its own proxy.
func probeTile(node, script, cfgFile string, cfg probeConfig, known []string) ([]probeResult, error) {
	base, stop, err := startNativeProxy(cfg.Tiles, known)
	if err != nil {
		return nil, err
	}
	defer stop()
	cfg.Base = base
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgFile, b, 0o600); err != nil {
		return nil, err
	}
	return probeExec(node, script, cfgFile, cfg)
}

// probeExec runs node on the probe with one config file (a variable: tests
// stand in for the browser).
var probeExec = func(node, script, cfgFile string, cfg probeConfig) ([]probeResult, error) {
	limit := time.Duration(cfg.Timeout)*time.Millisecond*time.Duration(len(cfg.Tiles)+1) + time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script, cfgFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && ee.ExitCode() == 3:
		return nil, &errNoBrowser{strings.TrimSpace(stderr.String())}
	case ctx.Err() != nil:
		return nil, fmt.Errorf("headless run timed out after %s", limit)
	case err != nil:
		return nil, fmt.Errorf("headless run failed: %v\n%s", err, strings.TrimSpace(stderr.String()))
	}
	var res struct {
		Results []probeResult `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("headless run: bad output (%v)\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return res.Results, nil
}

// --- the loopback proxy ---

// startNativeProxy serves a loopback origin in front of xbind for the
// headless browser. Chromium can't use bx's transport (a unix gateway
// inside sandboxes) or hold bx's token, so the proxy does both: it forwards
// everything, and lends bx's credential only to what an app opening the tile
// would load with the user's session — GET/HEAD of the tile's own document
// and files (lendsCredential). runProbe gives each tile its own. The tile's code talks to xbind with the frame
// token its runtime document was minted, exactly as in the app; it never
// holds bx's token, and nothing it requests elsewhere is authenticated by bx.
func startNativeProxy(tiles, known []string) (string, func(), error) {
	target, rt, err := upstream()
	if err != nil {
		return "", nil, err
	}
	tok := ownerToken()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Header.Del("Cookie")
			if tok != "" && lendsCredential(pr.In.Method, pr.In.URL.Path, tiles, known) {
				pr.Out.Header.Set("Authorization", "Bearer "+tok)
			}
		},
		Transport: rt,
		ErrorLog:  log.New(io.Discard, "", 0),
	}
	srv := &http.Server{Handler: rp, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }, nil
}

// upstream is bx's own route to xbind (transport()) as a proxy target.
func upstream() (*url.URL, http.RoundTripper, error) {
	base, client := transport()
	u, err := url.Parse(base)
	if err != nil {
		return nil, nil, fmt.Errorf("XBIN_URL %q: %v", base, err)
	}
	rt := client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	return u, rt, nil
}

// lendsCredential: bx's token rides only on a GET/HEAD of a canonical path
// inside one of the probed tiles — owned by that tile, not by a component
// nested under it (known lists every component path).
func lendsCredential(method, p string, tiles, known []string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	if c != p || !strings.HasPrefix(p, "/c/") {
		return false
	}
	rel := strings.TrimPrefix(p, "/c/")
	owner := ""
	for _, k := range append(append([]string{}, known...), tiles...) {
		if (rel == k || strings.HasPrefix(rel, k+"/")) && len(k) > len(owner) {
			owner = k
		}
	}
	for _, t := range tiles {
		if owner == t {
			return true
		}
	}
	return false
}
