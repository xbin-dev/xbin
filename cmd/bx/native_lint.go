package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Static checks of `bx lint --native` (docs/bx.md "Native UIs"): what can be
// known about a tile's native UI without running it — the entry exists and
// the manifest agrees, every import resolves the way the runtime document
// will resolve it, no raw colours where the vocabulary takes tokens. They
// read through xbind (the files a browser would load), so they see exactly
// what the app gets, overlays and all.

// lintFinding is one line of a lint report.
type lintFinding struct {
	Level   string `json:"level"` // ok | info | warn | error
	Where   string `json:"where,omitempty"`
	Message string `json:"message"`
}

// nativeComp is the slice of /api/xbin/components the native commands use.
type nativeComp struct {
	Path        string `json:"path"`
	Template    bool   `json:"template"`
	Chrome      bool   `json:"chrome"`
	State       string `json:"state"`
	ManifestErr string `json:"manifestError"`
	Native      *struct {
		Entry string `json:"entry"`
	} `json:"native"`
}

func (c nativeComp) entry() string {
	if c.Native == nil {
		return ""
	}
	return c.Native.Entry
}

// fetchFn GETs an xbind path with bx's credential.
type fetchFn func(p string) (status int, body []byte, err error)

func xbindGet(p string) (int, []byte, error) {
	resp, err := api("GET", p, nil)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, err
}

// noNativeReason is the runtime document's 404 text for a tile that simply
// has no native UI (internal/server/native.go) — as opposed to a broken
// declaration, which is an error.
const noNativeReason = "this tile has no native app UI"

const maxLintModules = 200

var reImportMap = regexp.MustCompile(`(?s)<script type="importmap">(.*?)</script>`)

// importMap is the document's import map: bare specifiers → URLs.
type importMap struct {
	Imports map[string]string `json:"imports"`
}

// resolve maps a bare specifier the way browsers do: an exact key, else
// the longest key ending in "/" that prefixes it.
func (m importMap) resolve(spec string) (string, bool) {
	if v, ok := m.Imports[spec]; ok {
		return v, true
	}
	best := ""
	for k := range m.Imports {
		if strings.HasSuffix(k, "/") && strings.HasPrefix(spec, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return "", false
	}
	return m.Imports[best] + spec[len(best):], true
}

func isBare(spec string) bool {
	return !strings.HasPrefix(spec, "/") && !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") && !strings.Contains(spec, "://")
}

// staticLint checks one tile. entry is its native entry from /components
// ("" when it has none).
func staticLint(get fetchFn, c nativeComp) []lintFinding {
	var out []lintFinding
	add := func(level, where, format string, a ...any) {
		out = append(out, lintFinding{Level: level, Where: where, Message: fmt.Sprintf(format, a...)})
	}
	if c.ManifestErr != "" {
		add("error", "xbin.json", "%s", c.ManifestErr)
	}
	docPath := "/c/" + c.Path + "/?native=1"
	st, doc, err := get(docPath)
	if err != nil {
		add("error", "", "runtime document: %v", err)
		return out
	}
	if c.entry() == "" || st == http.StatusNotFound {
		why := strings.TrimSpace(string(doc))
		switch {
		case st == http.StatusNotFound && strings.HasPrefix(why, noNativeReason):
			add("info", "", "no native UI: the app shows its web page (add native.js to give it one)")
		case st == http.StatusNotFound:
			add("error", "xbin.json", "native: %s", why)
		default:
			add("info", "", "no native UI")
		}
		return out
	}
	if st != http.StatusOK {
		add("error", "", "runtime document %s: HTTP %d %s", docPath, st, firstLine(string(doc)))
		return out
	}
	var im importMap
	if m := reImportMap.FindSubmatch(doc); m != nil {
		_ = json.Unmarshal(m[1], &im)
	}

	tileRoot := "/c/" + c.Path + "/"
	entryURL := tileRoot + c.entry()
	type mod struct {
		url, from string
		line      int
	}
	queue := []mod{{url: entryURL}}
	seen := map[string]bool{entryURL: true}
	checked := map[string]int{} // non-local URL → status
	modules, imports := 0, 0
	usesRuntime := false
	rel := func(u string) string { return strings.TrimPrefix(u, tileRoot) }

	for len(queue) > 0 && modules < maxLintModules {
		m := queue[0]
		queue = queue[1:]
		st, body, err := get(m.url)
		where := ""
		if m.from != "" {
			where = fmt.Sprintf("%s:%d", rel(m.from), m.line)
		}
		switch {
		case err != nil:
			add("error", where, "%s: %v", rel(m.url), err)
			continue
		case st != http.StatusOK:
			if m.from == "" {
				add("error", "", "native entry %s: HTTP %d", c.entry(), st)
			} else {
				add("error", where, "import %s does not resolve (HTTP %d)", rel(m.url), st)
			}
			continue
		}
		modules++
		name := rel(m.url)
		if line, msg := jsSyntax(body); msg != "" {
			add("error", fmt.Sprintf("%s:%d", name, line), "syntax: %s", msg)
		}
		sc := jsScan(string(body))
		for _, h := range rawColours(sc, m.url == entryURL) {
			w := fmt.Sprintf("%s:%d", name, h.Line)
			if h.Attr != "" {
				add("warn", w, "raw colour %s=%q: native UIs take tokens (tone=muted|accent|ok|warn|danger), never colours", h.Attr, h.Value)
			} else {
				add("warn", w, "raw colour %q: native UIs take tokens (tone=…), never colours", h.Value)
			}
		}
		for _, imp := range jsImports(sc) {
			imports++
			w := fmt.Sprintf("%s:%d", name, imp.Line)
			target := imp.Spec
			if isBare(imp.Spec) {
				t, ok := im.resolve(imp.Spec)
				if !ok {
					add("error", w, "bare import %q is not in the import map (use a path, or /vendor/…)", imp.Spec)
					continue
				}
				target = t
			}
			if strings.Contains(target, "://") {
				add("warn", w, "remote import %s: the app's runtime may not reach it; vendor it into the tile", target)
				continue
			}
			base, _ := url.Parse(m.url)
			ref, err := url.Parse(target)
			if err != nil {
				add("error", w, "import %q: %v", imp.Spec, err)
				continue
			}
			abs := base.ResolveReference(ref).String()
			if path.Clean(strings.SplitN(abs, "?", 2)[0]) == "/vendor/xb-native.js" {
				usesRuntime = true
			}
			if strings.HasPrefix(abs, tileRoot) {
				if !seen[abs] {
					seen[abs] = true
					queue = append(queue, mod{url: abs, from: m.url, line: imp.Line})
				}
				continue
			}
			if strings.HasPrefix(abs, "/c/") {
				add("warn", w, "import %s reaches into another tile: it loads only while that tile allows it", abs)
			}
			if _, ok := checked[abs]; !ok {
				s, _, err := get(abs)
				if err != nil {
					s = -1
				}
				checked[abs] = s
			}
			if s := checked[abs]; s != http.StatusOK {
				add("error", w, "import %s does not resolve (HTTP %d)", abs, s)
			}
		}
	}
	if len(queue) > 0 {
		add("warn", "", "stopped after %d modules", maxLintModules)
	}
	if modules == 0 {
		return out
	}
	if !usesRuntime {
		add("warn", c.entry(), "nothing imports /vendor/xb-native.js — a native UI renders with its html/render")
	}
	errs := 0
	for _, f := range out {
		if f.Level == "error" {
			errs++
		}
	}
	if errs == 0 {
		add("ok", "", "entry %s: %d module(s), %d import(s) resolve", c.entry(), modules, imports)
	}
	return out
}

// jsSyntax parse-checks a module; tests replace it.
var jsSyntax = nodeSyntax

// nodeSyntax runs `node --check` on a module's source (as an ES module) and
// returns the line and message of a syntax error — "" when it parses, or
// when node is missing (the headless run would report it, less precisely).
func nodeSyntax(src []byte) (int, string) {
	node, err := exec.LookPath("node")
	if err != nil {
		return 0, ""
	}
	f, err := os.CreateTemp("", "bx-check-*.mjs")
	if err != nil {
		return 0, ""
	}
	defer os.Remove(f.Name())
	_, err = f.Write(src)
	if cerr := f.Close(); err != nil || cerr != nil {
		return 0, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "--check", f.Name()).CombinedOutput()
	if err == nil || ctx.Err() != nil {
		return 0, ""
	}
	return parseNodeCheck(string(out), f.Name())
}

// parseNodeCheck reads `node --check` output: "<file>:<line>", the source
// line, a caret, then "SyntaxError: <message>".
var reCheckErr = regexp.MustCompile(`(?m)^(\w*Error): (.+)$`)

func parseNodeCheck(out, file string) (int, string) {
	line := 0
	if i := strings.Index(out, file+":"); i >= 0 {
		rest := out[i+len(file)+1:]
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		line, _ = strconv.Atoi(rest[:j])
	}
	if m := reCheckErr.FindStringSubmatch(out); m != nil {
		return line, m[2]
	}
	return line, firstLine(out)
}

var reStackAt = regexp.MustCompile(`(?m)^\s*at (?:.*\()?(/[^\s()]+:\d+(?::\d+)?)\)?\s*$`)

// stackWhere is the first frame of a stack that names a path on xbind
// (/c/<tile>/file.js:line:col), "" when there is none.
func stackWhere(stack string) string {
	if m := reStackAt.FindStringSubmatch(stack); m != nil {
		return m[1]
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// runtimeFindings turns a headless run into lint lines.
func runtimeFindings(r *probeResult) []lintFinding {
	var out []lintFinding
	add := func(level, where, format string, a ...any) {
		out = append(out, lintFinding{Level: level, Where: where, Message: fmt.Sprintf(format, a...)})
	}
	if r.LoadError != "" {
		if r.Status != 0 {
			add("error", "", "runtime document (HTTP %d): %s", r.Status, r.LoadError)
		} else {
			add("error", "", "runtime document: %s", r.LoadError)
		}
		return out
	}
	for _, e := range r.Errors {
		add("error", e.Where, "%s: %s", e.Kind, e.Message)
	}
	for _, d := range r.Diagnostics {
		lvl := d.Level
		if lvl != "error" && lvl != "warn" {
			lvl = "info"
		}
		add(lvl, d.Where, "%s: %s", d.Code, d.Message)
	}
	for _, e := range r.PageErrors {
		add("error", stackWhere(e), "uncaught: %s", firstLine(e))
	}
	for i, c := range r.Console {
		if i == 5 {
			add("info", "", "… %d more console lines", len(r.Console)-5)
			break
		}
		// warn, not error: a failed fetch logs here too; the runtime's own
		// errors are reported above
		add("warn", "", "console.%s: %s", c.Type, firstLine(c.Text))
	}
	// the runtime names unknown tags itself (unknown-tag); the tree's
	// leftovers are the ones it let through unvalidated
	var unknown []string
	for _, u := range r.Unknown {
		named := false
		for _, d := range r.Diagnostics {
			if d.Code == "unknown-tag" && strings.Contains(d.Message, "<"+u+">") {
				named = true
			}
		}
		if !named {
			unknown = append(unknown, u)
		}
	}
	if len(unknown) > 0 {
		add("error", "", "primitives the vocabulary doesn't know: %s", strings.Join(unknown, ", "))
	}
	for _, w := range r.Warnings {
		add("warn", "", "%s", w)
	}
	if len(r.Tree) == 0 || string(r.Tree) == "null" {
		add("error", "", "rendered nothing: no tree (the app would show the web page)")
		return out
	}
	if r.FirstTreeMs != nil && *r.FirstTreeMs > 5000 {
		add("warn", "", "first tree after %.0f ms: the app gives up after 5 s and shows the web page — render a placeholder before awaiting data", *r.FirstTreeMs)
	}
	ms := "?"
	if r.FirstTreeMs != nil {
		ms = fmt.Sprintf("%.0f", *r.FirstTreeMs)
	}
	s := r.Stats
	add("ok", "", "rendered: first tree in %s ms; %d nodes, depth %d, %s", ms, s.Nodes, s.Depth, humanBytes(s.Bytes))
	if len(r.Needs) > 0 {
		names := make([]string, 0, len(r.Needs))
		for k := range r.Needs {
			names = append(names, k)
		}
		sort.Strings(names)
		byRev := map[int][]string{}
		var revs []int
		for _, k := range names {
			rv := r.Needs[k]
			if byRev[rv] == nil {
				revs = append(revs, rv)
			}
			byRev[rv] = append(byRev[rv], k)
		}
		sort.Ints(revs)
		var parts []string
		for _, rv := range revs {
			parts = append(parts, fmt.Sprintf("rev %d: %s", rv, strings.Join(byRev[rv], " ")))
		}
		add("info", "", "needs app primitives %s", strings.Join(parts, "; "))
	}
	if len(r.Features) > 0 {
		add("info", "", "needs app features: %s", strings.Join(r.Features, ", "))
	}
	return out
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}
