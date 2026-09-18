// web.go — the web lane's tools: web_search (DuckDuckGo's HTML endpoint) and
// web_fetch (a URL as readable text). Both are read-only and go straight out,
// not through the xbin gateway, so they work only when the owner binds the
// `net` interface; unbound, the sandbox has no egress and they report
// themselves unavailable instead of failing the run.
//
// They are offered ONLY to runs in the web lane (Config.Toolset), which in turn
// get no internal reach — see toolSpecs and runTool. A run that could read the
// owner's systems AND make arbitrary web requests could be steered by injected
// content into sending that data out in a URL or a query.
package main

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func webToolSpecs() []toolSpec {
	return []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "web_search", Description: "Search the web (DuckDuckGo). Returns titles, URLs and snippets. Needs the workspace's internet binding; reports unavailable without it.",
			Parameters: obj([]string{"query"}, map[string]any{"query": strProp("search terms")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "web_fetch", Description: "Fetch a URL and return its readable text (tags stripped, capped). Needs the internet binding.",
			Parameters: obj([]string{"url"}, map[string]any{"url": strProp("absolute http(s) URL")}),
		}},
	}
}

// webClient goes straight out (NOT through the xbin gateway): with no net
// binding the sandbox has zero egress and dials fail — mapped to a friendly
// "unavailable" result so the model adapts instead of erroring the run.
var webClient = &http.Client{Timeout: 25 * time.Second}

func webUnavailable(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	s := err.Error()
	if strings.Contains(s, "connection refused") || strings.Contains(s, "no such host") ||
		strings.Contains(s, "network is unreachable") || strings.Contains(s, "i/o timeout") {
		return "(web access unavailable — no internet binding on this tile; ask the owner to bind net=internet in the Interfaces tab)", true
	}
	return "", false
}

func toolWebFetch(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("need an absolute http(s) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "xbin-agent/1.0")
	resp, err := webClient.Do(req)
	if msg, ok := webUnavailable(err); ok {
		return msg, nil
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	txt := htmlToText(string(raw))
	if txt == "" {
		txt = "(empty or non-text response)"
	}
	return fmt.Sprintf("HTTP %d · %s\n%s", resp.StatusCode, u.String(), clip(txt, 20000)), nil
}

func toolWebSearch(ctx context.Context, query string) (string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", fmt.Errorf("need a query")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://html.duckduckgo.com/html/?q="+url.QueryEscape(q), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; xbin-agent/1.0)")
	resp, err := webClient.Do(req)
	if msg, ok := webUnavailable(err); ok {
		return msg, nil
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	results := parseDDG(string(raw), 8)
	if len(results) == 0 {
		return "(no results parsed — try web_fetch on a known site)", nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.title, r.url, r.snippet)
	}
	return strings.TrimSpace(b.String()), nil
}

type ddgResult struct{ title, url, snippet string }

var (
	ddgLinkRe    = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?s)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	tagRe        = regexp.MustCompile(`<[^>]*>`)
	scriptRe     = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	spaceRe      = regexp.MustCompile(`[ \t\r\f]+`)
	nlRe         = regexp.MustCompile(`\n{3,}`)
)

// parseDDG extracts results from the html.duckduckgo.com page. Result hrefs
// are redirect links carrying the real URL in the uddg= param.
func parseDDG(page string, max int) []ddgResult {
	links := ddgLinkRe.FindAllStringSubmatch(page, -1)
	snips := ddgSnippetRe.FindAllStringSubmatch(page, -1)
	var out []ddgResult
	for i, l := range links {
		if len(out) >= max {
			break
		}
		href := html.UnescapeString(l[1])
		if u, err := url.Parse(href); err == nil {
			if real := u.Query().Get("uddg"); real != "" {
				href = real
			}
		}
		r := ddgResult{title: stripTags(l[2]), url: href}
		if i < len(snips) {
			r.snippet = clip(stripTags(snips[i][1]), 300)
		}
		if r.title != "" && r.url != "" {
			out = append(out, r)
		}
	}
	return out
}

func stripTags(s string) string {
	s = html.UnescapeString(tagRe.ReplaceAllString(s, " "))
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// htmlToText renders a page to plain-ish text: scripts/styles dropped, tags
// to spaces, entities unescaped, whitespace collapsed.
func htmlToText(s string) string {
	s = scriptRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spaceRe.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	s = strings.Join(lines, "\n")
	s = nlRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
