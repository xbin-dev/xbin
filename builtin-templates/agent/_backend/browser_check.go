// browser_check.go — a real browser in the sandbox (D136): browser_check
// loads a page in a headless Chromium inside the bound sandbox, with
// Playwright, and reports what happened as raw facts — the final URL and
// title, console messages, page errors, failed or blocked requests, a pruned
// accessibility snapshot, screenshots at the moments asked for, and the JSON
// a short script returned. No model reads the page in between (WatchPoint's
// measured values; WebArena/AgentOccam's pruned accessibility tree;
// ArtifactsBench's screenshots over time; Chrome DevTools MCP's console and
// network).
//
// The runner (browser_runner.mjs, embedded) is written into the sandbox once
// per version — its name carries its hash — under the sandbox user's
// ~/.cache/xbin-browser, and run with the contract's `run` (bounded time and
// output; the manager kills it at its timeout or when the call ends). Node is
// called by its absolute path in the xbin image (/usr/local/node/bin/node,
// not on the tile-sandbox PATH before D134), else whatever `node` the PATH
// has; a sandbox without Node, Playwright or a Chromium gets that said in so
// many words.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

//go:embed browser_runner.mjs
var browserRunnerJS []byte

var browserRunnerName = "runner-" + shaHex(browserRunnerJS)[:12] + ".mjs"

const (
	browserMark      = "@@XBIN_BROWSER_CHECK@@"
	browserShotsLead = "screenshots (shown to you after these tool results): "
	browserResultMax = 12 << 10 // the JSON the model gets
	browserShotMax   = 8 << 20  // one screenshot copied in
	browserShotsMax  = 4
	browserWaitMax   = 20000
	browserScriptMax = 8 << 10
)

// browserLaunch finds node and runs the runner ($0) with its request ($1).
// Tests swap it for a fake runner.
var browserLaunch = `n=/usr/local/node/bin/node
[ -x "$n" ] || n=$(command -v node 2>/dev/null) || n=
if [ -z "$n" ]; then printf '\n` + browserMark + `{"ok":false,"error":"node-missing"}\n'; exit 127; fi
exec "$n" "$0" "$1"`

func browserCheckSpec(cfg Config) toolSpec {
	egress := "unknown"
	if cfg.Sandbox != nil {
		egress = egressWords(cfg.Sandbox.Egress)
	}
	shots := " Screenshots are saved as session files (shots/…) and shown to you."
	if !cfg.feature("files") {
		shots = " (Screenshots need the session files, which are off here.)"
	}
	return toolSpec{Type: "function", Function: funcDef{
		Name: "browser_check",
		Description: "Load a page in a real headless Chromium inside your sandbox and report what happened: console, errors, failed requests, an accessibility snapshot, screenshots. " +
			"It runs in the sandbox, so the page's external loads go through the sandbox's egress (" + egress + "), and it needs Node and Playwright there (the xbin image has both). " +
			"Unlike render_html, the page's scripts run. The target is a sandbox file ('/work/index.html', './index.html'), " +
			"a session file ('session:report.html', copied into the sandbox first), or a URL the sandbox reaches ('http://localhost:8080/' — a server you started with bash background:true)." + shots +
			" script runs after the wait with Playwright's page in scope; its JSON return value comes back as it is.",
		Parameters: obj([]string{"target"}, map[string]any{
			"target":  strProp("what to load: a sandbox path, session:<file>, or an http(s) URL"),
			"wait_ms": intProp(fmt.Sprintf("how long to watch the page after its load event (default 1000, max %d)", browserWaitMax)),
			"screenshots_ms": map[string]any{"type": "array", "items": map[string]any{"type": "integer"},
				"description": fmt.Sprintf("moments after the load to take a screenshot at (default: one at the end of the wait; [] for none; up to %d)", browserShotsMax)},
			"viewport": map[string]any{"type": "object", "description": "the window size (default 1280×800)",
				"properties": map[string]any{"width": intProp("pixels"), "height": intProp("pixels")}},
			"script": strProp("optional: the body of an async function with `page` (Playwright) in scope, run after the wait; " +
				"return a JSON value, e.g. `return await page.locator('#total').textContent()`"),
		}),
	}}
}

// browserReq is what the runner reads.
type browserReq struct {
	URL           string         `json:"url"`
	WaitMs        int            `json:"wait_ms"`
	ScreenshotsMs []int          `json:"screenshots_ms"`
	OutDir        string         `json:"out_dir"`
	Viewport      map[string]int `json:"viewport,omitempty"`
	Script        string         `json:"script,omitempty"`
	BudgetMs      int            `json:"budget_ms"`
	NavTimeoutMs  int            `json:"nav_timeout_ms"`
}

// browserResult is what it answers (and, shaped, what the model reads).
type browserResult struct {
	OK              bool             `json:"ok"`
	Error           string           `json:"error,omitempty"`
	Detail          string           `json:"detail,omitempty"`
	URL             string           `json:"url"`
	Title           string           `json:"title"`
	Status          *int             `json:"status"`
	Load            map[string]any   `json:"load,omitempty"`
	Console         []map[string]any `json:"console"`
	ConsoleDropped  int              `json:"console_dropped,omitempty"`
	PageErrors      []map[string]any `json:"page_errors"`
	RequestsFailed  []map[string]any `json:"requests_failed"`
	RequestsDropped int              `json:"requests_dropped,omitempty"`
	Snapshot        string           `json:"snapshot"`
	SnapshotError   string           `json:"snapshot_error,omitempty"`
	Screenshots     []*browserShot   `json:"screenshots,omitempty"`
	Script          json.RawMessage  `json:"script,omitempty"`
	TimedOut        bool             `json:"timed_out,omitempty"`
	Note            string           `json:"note,omitempty"`
	Node            string           `json:"-"`
	Playwright      string           `json:"-"`
}

type browserShot struct {
	AtMs  int    `json:"at_ms"`
	File  string `json:"file,omitempty"` // the session file it was saved as
	Path  string `json:"path,omitempty"` // in the sandbox (dropped once copied)
	Bytes int    `json:"bytes,omitempty"`
	Error string `json:"error,omitempty"`
}

func (r *browserResult) UnmarshalJSON(b []byte) error {
	type plain browserResult
	var aux struct {
		plain
		Node       string `json:"node"`
		Playwright string `json:"playwright"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*r = browserResult(aux.plain)
	r.Node, r.Playwright = aux.Node, aux.Playwright
	return nil
}

func (ag *Agent) toolBrowserCheck(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	target := strings.TrimSpace(str(args["target"]))
	if target == "" {
		return "", fmt.Errorf("browser_check needs a target: a sandbox path, session:<file>, or an http(s) URL")
	}
	req, err := browserArgs(args, cfg.feature("files"))
	if err != nil {
		return "", err
	}
	use, err := ag.sandboxUse(ctx, rootOf(run), cfg, "")
	if err != nil {
		return "", err
	}
	base := path.Join(orStr(use.Box.Home, use.cwd()), ".cache", "xbin-browser")
	u, slug, err := ag.browserTarget(ctx, run, cfg, use, base, target)
	if err != nil {
		return "", err
	}
	req.URL = u
	runner := path.Join(base, browserRunnerName)
	if st, err := use.Conn.Stat(ctx, use.ID, runner); err != nil || st.Type != "file" || st.Size != int64(len(browserRunnerJS)) {
		if _, err := use.Conn.WriteFile(ctx, use.ID, runner, strings.NewReader(string(browserRunnerJS)), sbxWrite{Mkdirs: true, Mode: "0644"}); err != nil {
			return "", fmt.Errorf("writing the browser runner into the sandbox: %w", err)
		}
	}
	call := sanitizeUploadName(orStr(toolCallOf(ctx), fmt.Sprintf("c%d", time.Now().UnixNano())))
	req.OutDir = path.Join(base, "out", fmt.Sprintf("%d-%s", run.ID, call))
	// The budget: what the tool's deadline leaves, less time to copy the
	// screenshots in.
	left := time.Until(toolDeadline(ctx, 100*time.Second)) - 10*time.Second
	if left < 10*time.Second {
		return "", fmt.Errorf("too little time left for a browser check (%s) — raise the tool timeout", fmtDur(left))
	}
	req.BudgetMs = int(min(left, 90*time.Second) / time.Millisecond)
	req.NavTimeoutMs = min(20000, req.BudgetMs/2)
	rj, _ := json.Marshal(req)
	res, err := use.Conn.Run(ctx, use.ID, sbxRunReq{Argv: []string{"sh", "-c", browserLaunch, runner, string(rj)}, Cwd: use.cwd(),
		Env: map[string]string{"NO_COLOR": "1"}, TimeoutMs: req.BudgetMs + 8000, MaxOutput: 1 << 20})
	defer func() { // the screenshots were copied (or are lost with the call): tidy up
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = use.Conn.Remove(cctx, use.ID, req.OutDir, true)
	}()
	if err != nil {
		return "", err
	}
	out, err := parseBrowserRun(res)
	if err != nil {
		return "", err
	}
	if !out.OK {
		return "", browserUnavailable(out, use)
	}
	ag.browserShots(ctx, run, use, out, slug, target)
	markEgress(out, orStr(use.Box.effectiveEgress(), use.Binding.Egress))
	return browserText(out, u), nil
}

// browserArgs reads and bounds the tool's arguments.
func browserArgs(args map[string]any, files bool) (browserReq, error) {
	r := browserReq{WaitMs: 1000}
	if v, ok := args["wait_ms"]; ok && v != nil {
		r.WaitMs = toInt(v)
	}
	r.WaitMs = max(0, min(r.WaitMs, browserWaitMax))
	switch v := args["screenshots_ms"].(type) {
	case nil:
		r.ScreenshotsMs = []int{r.WaitMs}
	case []any:
		for _, x := range v {
			r.ScreenshotsMs = append(r.ScreenshotsMs, max(0, min(toInt(x), browserWaitMax)))
		}
		if r.ScreenshotsMs == nil {
			r.ScreenshotsMs = []int{}
		}
	default:
		return r, fmt.Errorf("screenshots_ms is a list of milliseconds after the load, e.g. [0, 1500]")
	}
	if len(r.ScreenshotsMs) > browserShotsMax {
		return r, fmt.Errorf("at most %d screenshots per check (asked for %d)", browserShotsMax, len(r.ScreenshotsMs))
	}
	if !files {
		r.ScreenshotsMs = []int{} // nowhere to keep them
	}
	if vp, ok := args["viewport"].(map[string]any); ok {
		w, h := toInt(vp["width"]), toInt(vp["height"])
		if w > 0 || h > 0 {
			r.Viewport = map[string]int{"width": max(320, min(orInt(w, 1280), 2560)), "height": max(240, min(orInt(h, 800), 2560))}
		}
	}
	r.Script = str(args["script"])
	if len(r.Script) > browserScriptMax {
		return r, fmt.Errorf("script is %s — keep it under %s (put a longer one in a file and use bash + node)", humanBytes(len(r.Script)), humanBytes(browserScriptMax))
	}
	return r, nil
}

func orInt(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

var slugBad = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// browserTarget turns the target into a URL the runner loads, and a slug to
// name its screenshots by. A session file is copied into the sandbox first.
func (ag *Agent) browserTarget(ctx context.Context, run *Run, cfg Config, use *sbxUse, base, target string) (string, string, error) {
	slug := func(s string) string {
		s = strings.Trim(slugBad.ReplaceAllString(s, "-"), "-.")
		if len(s) > 40 {
			s = s[:40]
		}
		return orStr(s, "page")
	}
	if lt := strings.ToLower(target); strings.HasPrefix(lt, "http://") || strings.HasPrefix(lt, "https://") {
		u, err := url.Parse(target)
		if err != nil || u.Host == "" {
			return "", "", fmt.Errorf("target %q is not a URL", target)
		}
		return u.String(), slug(u.Host + "-" + strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path))), nil
	}
	r, err := parseFileRef(target, true)
	if err != nil {
		return "", "", err
	}
	if r.sandbox {
		p, err := use.path(r.path)
		if err != nil {
			return "", "", err
		}
		st, err := use.Conn.Stat(ctx, use.ID, p)
		if err != nil {
			return "", "", ag.sandboxMiss(run, p, err)
		}
		if st.Type == "dir" {
			p = path.Join(p, "index.html")
		}
		return (&url.URL{Scheme: "file", Path: p}).String(), slug(strings.TrimSuffix(path.Base(p), path.Ext(p))), nil
	}
	f, err := ag.sessionFile(ctx, run, cfg, r)
	if err != nil {
		return "", "", err
	}
	data := []byte(f.Content)
	if f.Binary {
		if data, err = ag.readBlob(ctx, f.Blob); err != nil {
			return "", "", fmt.Errorf("reading %s: %w", f.Path, err)
		}
	}
	p := path.Join(base, "session", fmt.Sprint(run.ID), f.Path)
	if _, err := use.Conn.WriteFile(ctx, use.ID, p, strings.NewReader(string(data)), sbxWrite{Mkdirs: true}); err != nil {
		return "", "", fmt.Errorf("copying %s into the sandbox: %w", f.Path, err)
	}
	return (&url.URL{Scheme: "file", Path: p}).String(), slug(strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))), nil
}

// parseBrowserRun finds the runner's answer in its output.
func parseBrowserRun(res *sbxRunResult) (*browserResult, error) {
	var all string
	if res.Stdout != nil {
		all = res.Stdout.Head + res.Stdout.Tail
	}
	i := strings.LastIndex(all, browserMark)
	if i < 0 {
		why := "it gave no answer"
		switch {
		case res.TimedOut:
			why = "it ran out of time"
		case res.ExitCode != nil:
			why = fmt.Sprintf("it exited %d without an answer", *res.ExitCode)
		case res.Signal != "":
			why = "it was killed (" + res.Signal + ")"
		}
		tail := ""
		if res.Stderr != nil {
			tail = strings.TrimSpace(res.Stderr.Tail)
			if tail == "" {
				tail = strings.TrimSpace(res.Stderr.Head)
			}
		}
		if tail != "" {
			tail = ":\n" + clip(tail, 1500)
		}
		return nil, fmt.Errorf("the browser runner failed — %s%s", why, tail)
	}
	line, _, _ := strings.Cut(all[i+len(browserMark):], "\n")
	var out browserResult
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		return nil, fmt.Errorf("the browser runner's answer didn't read: %v", err)
	}
	return &out, nil
}

// browserUnavailable says, in so many words, what the sandbox lacks.
func browserUnavailable(out *browserResult, use *sbxUse) error {
	img := ""
	if use.Box.Image.ID != "" {
		img = fmt.Sprintf(" (image %s)", use.Box.Image.ID)
	}
	switch out.Error {
	case "node-missing":
		return fmt.Errorf("browser_check is not available in this sandbox%s: it has no Node.js (looked for /usr/local/node/bin/node and node on the PATH). "+
			"Install Node and Playwright there (npm i -D playwright && npx playwright install --with-deps chromium), or use a sandbox on the xbin image", img)
	case "playwright-missing":
		return fmt.Errorf("browser_check is not available in this sandbox%s: Node %s is there, but no Playwright — %s. "+
			"Install it in the project (npm i -D playwright && npx playwright install chromium) or globally", img, out.Node, out.Detail)
	case "browser-missing":
		return fmt.Errorf("browser_check could not start Chromium in this sandbox%s (Playwright is at %s): %s — "+
			"npx playwright install --with-deps chromium installs it", img, out.Playwright, out.Detail)
	}
	return fmt.Errorf("the browser check failed (%s): %s", orStr(out.Error, "unknown"), out.Detail)
}

// browserShots copies the screenshots into the session files, in place
// (shots/<page>-<ms>ms.png: the next check of the same page replaces them,
// keeping the earlier version), and records what they are.
func (ag *Agent) browserShots(ctx context.Context, run *Run, use *sbxUse, out *browserResult, slug, target string) {
	for _, s := range out.Screenshots {
		if s.Path == "" {
			continue
		}
		data, _, err := use.Conn.ReadFile(ctx, use.ID, s.Path, 0, 0, browserShotMax)
		if err != nil {
			s.Error, s.Path = "copying it in failed: "+err.Error(), ""
			continue
		}
		src := &fileSource{Kind: "browser", Tool: "browser_check", Call: toolCallOf(ctx), Sandbox: use.Binding.Ref,
			Box: orStr(use.Box.Name, use.Binding.Name), Target: target, AtMs: s.AtMs}
		f, _, _, err := ag.putFileData(ctx, run.ID, fmt.Sprintf("shots/%s-%dms.png", slug, s.AtMs), data, "image/png", src)
		if err != nil {
			s.Error, s.Path = "saving it failed: "+err.Error(), ""
			continue
		}
		s.File, s.Path, s.Bytes = f.Path, "", f.Bytes
	}
}

// netErrors are Chromium's words for a request the network refused.
var netErrors = []string{"ERR_NAME_NOT_RESOLVED", "ERR_INTERNET_DISCONNECTED", "ERR_NETWORK_UNREACHABLE",
	"ERR_ADDRESS_UNREACHABLE", "ERR_CONNECTION_REFUSED", "ERR_CONNECTION_TIMED_OUT", "ERR_TIMED_OUT",
	"ERR_NETWORK_ACCESS_DENIED", "ERR_CONNECTION_RESET", "ERR_NAME_RESOLUTION_FAILED"}

// markEgress names a failed request the sandbox's egress blocked, where
// that is recognisable: a network error for a host outside the sandbox when
// it has no network, or for a private address when it reaches the public
// internet only.
func markEgress(out *browserResult, egress string) {
	for _, r := range out.RequestsFailed {
		errText, _ := r["error"].(string)
		u, err := url.Parse(str(r["url"]))
		if errText == "" || err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") {
			continue
		}
		host := u.Hostname()
		if host == "localhost" || strings.HasSuffix(host, ".localhost") {
			continue
		}
		ip := net.ParseIP(host)
		if ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			continue
		}
		isNet := false
		for _, e := range netErrors {
			if strings.Contains(errText, e) {
				isNet = true
			}
		}
		switch {
		case !isNet:
		case egress == "none":
			r["blocked"] = "sandbox egress: this sandbox has no network, so the page can't load anything from outside it"
		case egress == "internet" && ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast()):
			r["blocked"] = "sandbox egress: this sandbox reaches the public internet only, not private addresses"
		}
	}
}

// browserText is the tool's result: a headline, the screenshots line
// (shownImages reads it), and the facts as JSON, within browserResultMax.
func browserText(out *browserResult, u string) string {
	var head []string
	if l := out.Load; l != nil {
		if l["state"] == "load" {
			head = append(head, fmt.Sprintf("loaded in %v ms", l["ms"]))
		} else {
			head = append(head, fmt.Sprintf("did not load: %v", l["error"]))
		}
	}
	if out.Status != nil {
		head = append(head, fmt.Sprintf("status %d", *out.Status))
	}
	errs := 0
	for _, c := range out.Console {
		if c["type"] == "error" {
			errs++
		}
	}
	head = append(head, fmt.Sprintf("%d console message(s), %d error(s)", len(out.Console)+out.ConsoleDropped, errs),
		fmt.Sprintf("%d page error(s)", len(out.PageErrors)), fmt.Sprintf("%d failed request(s)", len(out.RequestsFailed)+out.RequestsDropped))
	if out.TimedOut {
		head = append(head, "cut short by its time budget")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "browser_check %s — %s\n", u, strings.Join(head, " · "))
	var keys []string
	for _, s := range out.Screenshots {
		if s.File != "" {
			keys = append(keys, s.File)
		}
	}
	if len(keys) > 0 {
		b.WriteString(browserShotsLead + strings.Join(keys, ", ") + "\n")
	}
	b.WriteString(fitBrowserJSON(out))
	return b.String()
}

// fitBrowserJSON marshals the result, shrinking the longest parts until it
// fits: the snapshot first, then the lists (their newest entries go first;
// errors are kept before other console messages).
func fitBrowserJSON(out *browserResult) string {
	for i := 0; ; i++ {
		b, _ := json.MarshalIndent(out, "", " ")
		if len(b) <= browserResultMax || i > 12 {
			return string(b)
		}
		switch {
		case len(out.Snapshot) > 2000:
			out.Snapshot = clip(out.Snapshot, len(out.Snapshot)/2) + "\n… (snapshot cut to fit)"
		case len(out.Console) > 10:
			out.Console, out.ConsoleDropped = keepErrorsFirst(out.Console, len(out.Console)/2), out.ConsoleDropped+len(out.Console)-len(out.Console)/2
		case len(out.RequestsFailed) > 10:
			n := len(out.RequestsFailed) / 2
			out.RequestsDropped += len(out.RequestsFailed) - n
			out.RequestsFailed = out.RequestsFailed[:n]
		case len(out.PageErrors) > 5:
			out.PageErrors = out.PageErrors[:5]
		case len(out.Script) > 4000:
			out.Script = json.RawMessage(fmt.Sprintf(`{"error":"its return value (%d bytes of JSON) doesn't fit the result — return less"}`, len(out.Script)))
		default:
			out.Snapshot = clip(out.Snapshot, 500)
			for _, c := range out.Console {
				c["text"] = clip(str(c["text"]), 200)
			}
		}
	}
}

func keepErrorsFirst(cs []map[string]any, n int) []map[string]any {
	var errs, rest []map[string]any
	for _, c := range cs {
		if c["type"] == "error" {
			errs = append(errs, c)
		} else {
			rest = append(rest, c)
		}
	}
	all := append(errs, rest...)
	return all[:min(n, len(all))]
}
