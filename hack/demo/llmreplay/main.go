// llmreplay records an agent's model traffic once and replays it for every
// retake: a real run with real keys is recorded, and each take after it gets
// the very same model responses — streamed event by event on the recorded
// clock (or scaled, or instant) — so footage is reproducible and a take
// costs nothing.
//
// It is an HTTP proxy speaking the OpenAI-compatible API (chat/completions
// and responses, streamed or not, plus /v1/models and the rest as lookups)
// and the Anthropic Messages API (what Claude Code sends to
// ANTHROPIC_BASE_URL). Traffic is split into lanes by a path prefix
// (/lane/<name>/v1/…) or an X-Replay-Lane header, so llm-gw's calls and a
// sandboxed Claude Code's never interleave.
//
//	llmreplay record      -cassette FILE [-listen ADDR] [-append | -overwrite]
//	llmreplay replay      -cassette FILE [-listen ADDR] [-timing original|instant|F] [-max-wait D]
//	                      [-drift S] [-window N] [-on-miss error|passthrough] [-keep-errors]
//	llmreplay passthrough [-listen ADDR]
//	llmreplay inspect     [-v] FILE
//
// (-detach [-log FILE] on the first three: background it, return once it
// listens.) Upstreams and keys come from the environment (upstreamsFromEnv). README.md
// has the wiring (llm-gw, Claude Code in a coding sandbox), how matching
// works, and the security notes: cassettes hold prompts and model output and
// live under the main checkout's .film-media/replay/, never in git.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultListen = "127.0.0.1:9398"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch cmd := os.Args[1]; cmd {
	case "record", "replay", "passthrough":
		os.Exit(serveCmd(cmd, os.Args[2:], os.Getenv))
	case "inspect":
		os.Exit(inspectCmd(os.Args[2:], os.Stdout, os.Stderr))
	case "help", "-h", "-help", "--help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `llmreplay — record an agent's model calls once, replay them for every retake

  llmreplay record      -cassette FILE [-listen ADDR] [-append | -overwrite]
  llmreplay replay      -cassette FILE [-listen ADDR] [-timing original|instant|F] [-max-wait D]
                        [-drift S] [-window N] [-on-miss error|passthrough] [-keep-errors]
  llmreplay passthrough [-listen ADDR]
  llmreplay inspect     [-v] FILE

record, replay and passthrough also take -detach [-log FILE]: start in the
background and return once listening (or at once if one already listens).

Point clients at http://ADDR/lane/<name> (llm-gw backend: …/lane/agent/v1,
Claude Code: ANTHROPIC_BASE_URL=…/lane/claude). Upstreams (record,
passthrough): LLMREPLAY_OPENAI_BASE_URL + OPENAI_API_KEY,
LLMREPLAY_ANTHROPIC_BASE_URL + ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN);
no key: the caller's own credentials go through. LLMREPLAY_TOKEN: callers
must send it as their API key. Control: GET /_llmreplay/status,
POST /_llmreplay/reset[?lane=] (or SIGHUP) to start a take over.
See hack/demo/llmreplay/README.md.
`)
}

func serveCmd(mode string, args []string, getenv func(string) string) int {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	opt := options{mode: mode}
	fs.StringVar(&opt.listen, "listen", defaultListen, "address to listen on (keep it on loopback in record mode: whoever reaches it spends your keys)")
	if mode != "passthrough" {
		fs.StringVar(&opt.cassette, "cassette", "", "the cassette file (JSON Lines; under the main checkout's .film-media/replay/)")
	}
	allowTracked := false
	if mode == "record" {
		fs.BoolVar(&opt.appendRec, "append", false, "add to an existing cassette (its lanes' sequences go on)")
		fs.BoolVar(&opt.overwrite, "overwrite", false, "record over an existing cassette")
		fs.BoolVar(&allowTracked, "allow-tracked", false, "write the cassette even where git would track it")
	}
	detach, logPath := false, ""
	fs.BoolVar(&detach, "detach", false, "run in the background (a new session) and return once it listens — or at once when one already serves -listen; prints to stderr only (safe in a Claude Code SessionStart hook)")
	fs.StringVar(&logPath, "log", "", "with -detach: the log file (default: llmreplay-<port>.log in the temp dir)")
	timing := "original"
	if mode == "replay" {
		fs.StringVar(&timing, "timing", timing, "original, instant, or a factor every recorded pause is multiplied by (0.5 = twice as fast)")
		fs.DurationVar(&opt.maxWait, "max-wait", 0, "cap on any single pause (e.g. 3s; 0 = none)")
		fs.Float64Var(&opt.drift, "drift", 0.8, "report a match less similar than this (0..1) as DRIFT")
		fs.IntVar(&opt.window, "window", 8, "how far past the take's furthest call a fallback match may reach")
		fs.StringVar(&opt.onMiss, "on-miss", "error", "a request nothing recorded fits: error (400 in the API's shape) or passthrough (to the upstream, unrecorded)")
		fs.BoolVar(&opt.keepErrors, "keep-errors", false, "replay recorded 429s, 5xxs and failed calls too (default: left out, as the client's retry got the real answer)")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "llmreplay %s: unexpected arguments: %s\n", mode, strings.Join(fs.Args(), " "))
		return 2
	}
	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)
	var err error
	if opt.timing, err = parseTiming(timing); err != nil {
		fmt.Fprintln(os.Stderr, "llmreplay:", err)
		return 2
	}
	if mode == "replay" && (opt.onMiss != "error" && opt.onMiss != "passthrough") {
		fmt.Fprintln(os.Stderr, "llmreplay: -on-miss is error or passthrough")
		return 2
	}
	if mode != "passthrough" && opt.cassette == "" {
		fmt.Fprintf(os.Stderr, "llmreplay %s: -cassette FILE is required (e.g. /home/magik6k/buxon/.film-media/replay/take.jsonl)\n", mode)
		return 2
	}
	opt.token = strings.TrimSpace(getenv("LLMREPLAY_TOKEN"))
	if detach {
		return detachSelf(mode, opt, logPath, args)
	}
	ups := upstreamsFromEnv(getenv)
	if err := checkLoop(opt.listen, ups); err != nil && (mode != "replay" || opt.onMiss == "passthrough") {
		fmt.Fprintln(os.Stderr, "llmreplay:", err)
		return 2
	}
	s := newServer(opt, ups, logger)

	switch mode {
	case "record":
		if !allowTracked {
			if err := gitGuard(opt.cassette); err != nil {
				fmt.Fprintln(os.Stderr, "llmreplay:", err)
				return 2
			}
		}
		rec, next, err := openRecorder(opt.cassette, opt.appendRec, opt.overwrite)
		if err != nil {
			fmt.Fprintln(os.Stderr, "llmreplay:", err)
			return 1
		}
		defer rec.close()
		s.rec, s.seq = rec, next
	case "replay":
		c, err := loadCassette(opt.cassette)
		if err != nil {
			fmt.Fprintln(os.Stderr, "llmreplay:", err)
			return 1
		}
		if c.Skipped > 0 {
			logger.Printf("%s: %d unreadable lines skipped (a recording cut off mid-write?)", opt.cassette, c.Skipped)
		}
		s.play = newPlayer(c, opt)
	}

	ln, err := net.Listen("tcp", opt.listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llmreplay:", err)
		return 1
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 5 * time.Minute}
	banner(logger, s, ln.Addr().String())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for {
		select {
		case err := <-done:
			if !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "llmreplay:", err)
				return 1
			}
			return 0
		case sig := <-sigs:
			if sig == syscall.SIGHUP {
				if s.play != nil {
					s.play.reset("")
					logger.Printf("SIGHUP: the take starts over")
				}
				continue
			}
			logger.Printf("%v: stopping", sig)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = srv.Shutdown(ctx)
			cancel()
			if s.rec != nil {
				for lane, n := range s.rec.counts() {
					logger.Printf("recorded %d exchanges in lane %s", n, lane)
				}
			}
			return 0
		}
	}
}

func banner(logger *log.Logger, s *server, addr string) {
	base := "http://" + addr
	switch s.opt.mode {
	case "record":
		logger.Printf("llmreplay record on %s → %s", base, s.opt.cassette)
	case "replay":
		t := "original timing"
		switch {
		case s.opt.timing == 0:
			t = "instant"
		case s.opt.timing != 1:
			t = fmt.Sprintf("pauses ×%g", s.opt.timing)
		}
		if s.opt.maxWait > 0 {
			t += fmt.Sprintf(", none over %v", s.opt.maxWait)
		}
		logger.Printf("llmreplay replay on %s ← %s: %s; %s", base, s.opt.cassette, s.play.summary(), t)
	default:
		logger.Printf("llmreplay passthrough on %s", base)
	}
	if s.opt.mode != "replay" {
		for _, up := range []*upstream{&s.ups.openai, &s.ups.anthropic} {
			auth := "the caller's own credentials"
			switch {
			case up.key != "":
				auth = "the key in $" + up.keyEnv
			case s.opt.token != "":
				auth = "NO key (set one: with LLMREPLAY_TOKEN the caller's own isn't passed on)"
			}
			logger.Printf("  %s upstream %s with %s", up.name, up.base, auth)
		}
	}
	if s.opt.token != "" {
		logger.Printf("  callers must send $LLMREPLAY_TOKEN as their API key")
	}
	logger.Printf("  llm-gw backend base URL: %s/lane/agent/v1   Claude Code: ANTHROPIC_BASE_URL=%s/lane/claude", base, base)
}

// detachSelf starts this command again without -detach, in a session of
// its own with its output in a log, and returns once it listens; an
// llmreplay already answering on -listen is left as it is. Everything it
// says goes to stderr: a Claude Code SessionStart hook's stdout would land
// in the model's context (and in the recorded prompt).
func detachSelf(mode string, opt options, logPath string, args []string) int {
	if probe(opt.listen, opt.token) {
		fmt.Fprintf(os.Stderr, "llmreplay: one already serves %s\n", opt.listen)
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "llmreplay:", err)
		return 1
	}
	child := []string{mode}
	for _, a := range args {
		if f := strings.TrimLeft(a, "-"); f == "detach" || strings.HasPrefix(f, "detach=") {
			continue
		}
		child = append(child, a)
	}
	if logPath == "" {
		_, port, _ := net.SplitHostPort(opt.listen)
		logPath = filepath.Join(os.TempDir(), "llmreplay-"+port+".log")
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llmreplay:", err)
		return 1
	}
	defer lf.Close()
	cmd := exec.Command(exe, child...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "llmreplay:", err)
		return 1
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			fmt.Fprintf(os.Stderr, "llmreplay: the %s server stopped (%v) — see %s\n", mode, err, logPath)
			return 1
		case <-time.After(50 * time.Millisecond):
		}
		if probe(opt.listen, opt.token) {
			fmt.Fprintf(os.Stderr, "llmreplay %s on http://%s (pid %d, log %s)\n", mode, opt.listen, cmd.Process.Pid, logPath)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "llmreplay: not listening on %s after 60s — see %s\n", opt.listen, logPath)
	return 1
}

// probe says whether an llmreplay answers on addr.
func probe(addr, token string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if isUnspecified(host) {
		host = "127.0.0.1"
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+net.JoinHostPort(host, port)+controlPrefix+"status", nil)
	if err != nil {
		return false
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer r.Body.Close()
	var st struct {
		Mode string `json:"mode"`
	}
	return r.StatusCode == http.StatusOK && json.NewDecoder(r.Body).Decode(&st) == nil && st.Mode != ""
}

// parseTiming reads -timing: original (1), instant (0) or a factor
// ("0.5", "0.5x") every recorded pause is multiplied by.
func parseTiming(s string) (float64, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "original", "real":
		return 1, nil
	case "instant", "none":
		return 0, nil
	}
	v := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(s), "×"), "x")
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("-timing %q: original, instant, or a factor for every recorded pause (0.5 = twice as fast)", s)
	}
	return f, nil
}

// checkLoop refuses an upstream that is this proxy itself (a shell exporting
// the proxy's address for its clients, read back as the upstream).
func checkLoop(listen string, ups upstreams) error {
	lh, lp, err := net.SplitHostPort(listen)
	if err != nil {
		return nil
	}
	for _, up := range []upstream{ups.openai, ups.anthropic} {
		u, err := url.Parse(up.base)
		if err != nil || u.Host == "" {
			return fmt.Errorf("the %s upstream %q is not a URL", up.name, up.base)
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
		}
		h := u.Hostname()
		if port == lp && (h == lh || isLoopback(h) && (isLoopback(lh) || isUnspecified(lh))) {
			return fmt.Errorf("the %s upstream %s is this proxy (listening on %s): set LLMREPLAY_%s_BASE_URL to the real API", up.name, up.base, listen, strings.ToUpper(up.name))
		}
	}
	return nil
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isUnspecified(h string) bool {
	ip := net.ParseIP(h)
	return h == "" || ip != nil && ip.IsUnspecified()
}

// gitGuard refuses to record where git would track the cassette: it holds
// prompts and model output (and whatever an agent read), which never belong
// in a repository. Outside any work tree, or where git can't say, it lets
// the path be.
func gitGuard(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			break
		}
		if filepath.Dir(d) == d {
			return nil
		}
	}
	for {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			break
		}
		dir = filepath.Dir(dir)
	}
	err = exec.Command("git", "-C", dir, "check-ignore", "-q", abs).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return fmt.Errorf("%s is inside a git work tree and git would track it: a cassette holds prompts and model output — record under the main checkout's .film-media/replay/ (git-excluded), or pass -allow-tracked", path)
	}
	return nil
}
