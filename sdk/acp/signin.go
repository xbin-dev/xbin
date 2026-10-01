package acp

// Signing a CLI in for a person who never sees its terminal (D178): a
// provider's Signin says what to run where the agent runs (its $HOME keeps
// the login) and how to read what that prints — the sign-in page to open,
// the prompt that asks for the code the page shows, and the lines that say
// it worked, refused a code or failed. Signin.Scan reads that output; web/
// signin-scan.js is its twin in the browser (the Agent tab's guided
// sign-in), tested against the same captures (testdata/signin).

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Signin is how a client drives a provider CLI's own sign-in for a person:
// run Command (or Argv with Env) where the agent runs, read what it prints
// with Scan, open URL for the person, write the code they paste and a
// newline when the CLI asks for it (Code), and take Done — or the command
// ending — as the answer. It saves nothing itself: the CLI keeps the login
// in its $HOME, as it does when someone signs in at a terminal.
type Signin struct {
	// Command is the sign-in as one shell line. Argv is the same without a
	// shell, for a client that spawns it, and Env what it adds to the
	// environment.
	Command string            `json:"command"`
	Argv    []string          `json:"argv"`
	Env     map[string]string `json:"env,omitempty"`
	// TTY: the sign-in needs a terminal. False: it prints the URL and reads
	// the code over pipes too (on a terminal it may draw the URL as an OSC 8
	// link — Scan reads both).
	TTY bool `json:"tty"`
	// Fallback is a shell line for a CLI too old for Command — which then
	// exits without printing a sign-in URL: an interactive sign-in a person
	// drives in a terminal.
	Fallback string `json:"fallback,omitempty"`
	// URL is a regular expression (RE2 and JavaScript alike) the sign-in
	// page's address matches; Hosts are the hosts it may be on (a subdomain
	// of one counts). Only an https URL on one of them is ever offered.
	URL   string   `json:"url"`
	Hosts []string `json:"hosts"`
	// The markers, each a substring of one line of output: Code — the CLI
	// asks for the code the sign-in page shows; Invalid — it refused a code
	// as malformed and waits for another; Done — it signed in; Fail — it
	// gave up (that line says why, and the CLI exits).
	Code    string `json:"code"`
	Invalid string `json:"invalid,omitempty"`
	Done    string `json:"done"`
	Fail    string `json:"fail"`
}

// SigninState is what a sign-in has printed so far (Signin.Scan).
type SigninState struct {
	URL     string // the sign-in page ("" until printed); an https URL on Hosts
	Link    bool   // URL is an OSC 8 link's target: whole as printed (text may still be arriving)
	Code    bool   // the CLI asked for the code
	Invalid int    // codes it refused as malformed
	Done    bool   // it said it signed in
	Failed  string // the line that said it failed ("" while none did)
	Last    string // the last line of text: what to show when it ends saying none of these
}

// claudeSignin is Claude Code's: `claude auth login` (2.1.126 and later:
// one sign-in, no onboarding, a pasted code, plain over pipes) — not
// `claude /login`, which on a fresh $HOME signs in twice (the onboarding,
// then the command). --claudeai picks the subscription sign-in (its
// default) and makes a CLI without `auth` fail at once (an unknown option)
// rather than start a session; such a CLI is left to Fallback, whose
// onboarding asks for the sign-in and then exits.
var claudeSignin = &Signin{
	Command: "claude auth login --claudeai", Argv: []string{"claude", "auth", "login", "--claudeai"},
	Fallback: "claude /exit",
	URL:      `https://\S+/oauth/authorize\?\S+`, Hosts: []string{"claude.com", "claude.ai", "anthropic.com"},
	Code: "Paste code here if prompted", Invalid: "Invalid code", Done: "Login successful", Fail: "Login failed",
}

// Allowed reports whether u may be offered as the sign-in page: https, on
// one of Hosts (or a subdomain of one), matching URL.
func (s Signin) Allowed(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.User != nil || p.Host == "" {
		return false
	}
	host := strings.ToLower(p.Hostname())
	ok := false
	for _, h := range s.Hosts {
		h = strings.ToLower(h)
		if host == h || strings.HasSuffix(host, "."+h) {
			ok = true
		}
	}
	if !ok {
		return false
	}
	re, err := regexp.Compile(`^(?:` + s.URL + `)$`)
	return err == nil && re.MatchString(u)
}

// Scan reads out — all a sign-in has printed so far, a terminal's bytes or
// a pipe's — into what it said. The URL is the newest acceptable one: an
// OSC 8 link's target first (a TUI draws a long URL as one link per
// screen row, each pointing at the whole of it), else one in the text —
// rejoined when the CLI broke it over full-width lines.
func (s Signin) Scan(out []byte) SigninState {
	lines, links := screenText(out)
	var st SigninState
	for i := len(links) - 1; i >= 0 && st.URL == ""; i-- {
		if s.Allowed(links[i]) {
			st.URL, st.Link = links[i], true
		}
	}
	if st.URL == "" {
		st.URL = s.textURL(lines)
	}
	for _, l := range lines {
		if s.Code != "" && strings.Contains(l, s.Code) {
			st.Code = true
		}
		if s.Invalid != "" {
			st.Invalid += strings.Count(l, s.Invalid)
		}
		if s.Done != "" && strings.Contains(l, s.Done) {
			st.Done = true
		}
		if s.Fail != "" && st.Failed == "" {
			if i := strings.Index(l, s.Fail); i >= 0 {
				st.Failed = strings.TrimSpace(l[i:])
			}
		}
		if t := strings.TrimSpace(l); t != "" {
			st.Last = t
		}
	}
	return st
}

// urlChar: what a URL is made of (RFC 3986's unreserved, reserved and %).
func urlChar(r rune) bool {
	return r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		strings.ContainsRune("-._~:/?#[]@!$&'()*+,;=%", r))
}

func allURL(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !urlChar(r) {
			return false
		}
	}
	return true
}

// textURL is the newest acceptable URL in the text. A URL running to the
// end of its line continues on the lines after it while those are made of
// URL characters alone and the line before was as long as the first (a
// hard wrap breaks every row at the same width; the last row is shorter).
func (s Signin) textURL(lines []string) string {
	re, err := regexp.Compile(s.URL)
	if err != nil {
		return ""
	}
	found := ""
	for i, l := range lines {
		at := strings.Index(l, "https://")
		if at < 0 {
			continue
		}
		cand := l[at:]
		if end := strings.IndexFunc(cand, func(r rune) bool { return !urlChar(r) }); end >= 0 {
			cand = cand[:end] // ends inside its line
		} else if width := utf8.RuneCountInString(l); width > 0 {
			for j := i + 1; j < len(lines); j++ {
				next := strings.TrimSpace(lines[j])
				if !allURL(next) {
					break
				}
				cand += next
				if utf8.RuneCountInString(strings.TrimRight(lines[j], " ")) < width {
					break // a shorter row: the last of the wrap
				}
			}
		}
		if m := re.FindString(cand); m != "" && s.Allowed(m) {
			found = m
		}
	}
	return found
}

// screenText turns terminal output into lines of text and the OSC 8 link
// targets in it: CSI sequences go (a cursor move along the line becomes a
// space — a TUI spaces words that way), OSC strings go (8's target is
// kept), CR LF and a lone CR end a line, other controls go.
func screenText(b []byte) (lines, links []string) {
	var cur strings.Builder
	flush := func() { lines = append(lines, cur.String()); cur.Reset() }
	n := len(b)
	for i := 0; i < n; i++ {
		c := b[i]
		switch {
		case c == 0x1b && i+1 < n:
			switch b[i+1] {
			case '[': // CSI: parameters, then a final byte @…~
				j := i + 2
				for j < n && (b[j] < 0x40 || b[j] > 0x7e) {
					j++
				}
				if j < n && (b[j] == 'G' || b[j] == 'C') && cur.Len() > 0 {
					cur.WriteByte(' ')
				}
				i = j
			case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: a string up to BEL or ESC \
				j := i + 2
				for j < n && b[j] != 0x07 && !(b[j] == 0x1b && j+1 < n && b[j+1] == '\\') {
					j++
				}
				if b[i+1] == ']' {
					if body := string(b[i+2 : min(j, n)]); strings.HasPrefix(body, "8;") {
						if k := strings.IndexByte(body[2:], ';'); k >= 0 && body[2+k+1:] != "" && j < n {
							links = append(links, body[2+k+1:])
						}
					}
				}
				if j < n && b[j] == 0x1b {
					j++ // the ST's backslash
				}
				i = j
			case '(', ')', '*', '+', '#', ' ', '%': // a designation: one more byte
				i += 2
			default: // ESC 7, ESC 8, ESC =, ESC c, …
				i++
			}
		case c == 0x1b: // a lone ESC at the end
		case c == '\r':
			for i+1 < n && b[i+1] == '\r' {
				i++
			}
			if i+1 < n && b[i+1] == '\n' {
				i++
			}
			flush()
		case c == '\n':
			flush()
		case c == '\t':
			cur.WriteByte(' ')
		case c < 0x20 || c == 0x7f:
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		flush()
	}
	return lines, links
}
