package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signinCase is one capture of a real CLI's sign-in (testdata/signin, the
// same table hack/signin-scan.test.mjs reads for web/signin-scan.js).
type signinCase struct {
	File    string `json:"file"`
	About   string `json:"about"`
	URL     string `json:"url"`
	Code    bool   `json:"code"`
	Invalid int    `json:"invalid"`
	Done    bool   `json:"done"`
	Failed  string `json:"failed"`
}

func claudeSpec(t *testing.T) Signin {
	t.Helper()
	p, ok := Lookup("claude")
	if !ok || p.Signin == nil {
		t.Fatal("claude has no Signin")
	}
	return *p.Signin
}

// Claude Code 2.1.280's own output — `auth login` on a terminal and over
// pipes, the Ink flows that hard-wrap the URL (with and without OSC 8) —
// reads as the sign-in it was: the whole URL, the code prompt, a malformed
// code, a refusal.
func TestSigninScanCaptures(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "signin", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []signinCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 6 {
		t.Fatalf("%d cases", len(cases))
	}
	s := claudeSpec(t)
	for _, c := range cases {
		out, err := os.ReadFile(filepath.Join("testdata", "signin", c.File))
		if err != nil {
			t.Fatal(err)
		}
		st := s.Scan(out)
		if st.URL != c.URL || st.Code != c.Code || st.Invalid != c.Invalid || st.Done != c.Done || st.Failed != c.Failed {
			t.Errorf("%s (%s):\n got %+v\nwant url=%s code=%v invalid=%d done=%v failed=%q", c.File, c.About, st, c.URL, c.Code, c.Invalid, c.Done, c.Failed)
		}
		// output cut short mid-way never yields a partial URL: what it gives
		// is a whole one or none
		for n := 0; n < len(out); n += 97 {
			if u := s.Scan(out[:n]).URL; u != "" && u != c.URL && !strings.HasPrefix(c.URL, u) {
				t.Errorf("%s: a prefix of %d bytes gave %q", c.File, n, u)
			}
		}
	}
}

func TestSigninScanDoneAndLast(t *testing.T) {
	s := claudeSpec(t)
	ok := "Opening browser to sign in…\nIf the browser didn't open, visit: https://claude.ai/oauth/authorize?code=true&state=x\nPaste code here if prompted > Login successful.\n"
	if st := s.Scan([]byte(ok)); !st.Done || st.Failed != "" || !st.Code || st.URL != "https://claude.ai/oauth/authorize?code=true&state=x" || st.Last != "Paste code here if prompted > Login successful." {
		t.Fatalf("a sign-in that worked: %+v", st)
	}
	// a CLI too old for `auth`: no URL, no marker — the last line says why
	old := "$ claude auth login --claudeai; exit\r\nerror: unknown option '--claudeai'\r\n"
	if st := s.Scan([]byte(old)); st.URL != "" || st.Code || st.Done || st.Failed != "" || st.Last != "error: unknown option '--claudeai'" {
		t.Fatalf("an old CLI: %+v", st)
	}
	// the shell echoing the typed command is not the CLI speaking
	if st := s.Scan([]byte(" " + s.Command + "; exit\r\n")); st.Code || st.Done || st.Failed != "" || st.URL != "" {
		t.Fatalf("the echoed command: %+v", st)
	}
}

// Only an https URL on the provider's hosts is ever offered — an OSC 8
// link or plain text pointing anywhere else is passed over.
func TestSigninAllowed(t *testing.T) {
	s := claudeSpec(t)
	for _, u := range []string{
		"https://claude.ai/oauth/authorize?code=true",
		"https://claude.com/cai/oauth/authorize?x=1",
		"https://platform.claude.com/oauth/authorize?x=1",
		"https://console.anthropic.com/oauth/authorize?x=1",
	} {
		if !s.Allowed(u) {
			t.Errorf("%s refused", u)
		}
	}
	for _, u := range []string{
		"http://claude.ai/oauth/authorize?code=true",
		"https://claude.ai.evil.example/oauth/authorize?x=1",
		"https://evilclaude.ai/oauth/authorize?x=1",
		"https://claude.ai@evil.example/oauth/authorize?x=1",
		"https://evil.example/oauth/authorize?next=https://claude.ai/",
		"javascript:alert(1)//https://claude.ai/oauth/authorize?x",
		"https://claude.ai/somewhere/else",
		"",
	} {
		if s.Allowed(u) {
			t.Errorf("%s allowed", u)
		}
	}
	evil := "\x1b]8;;https://evil.example/oauth/authorize?x=1\x07click\x1b]8;;\x07\r\n" +
		"see http://claude.ai/oauth/authorize?code=1 or https://claude.ai.evil.example/oauth/authorize?x=2\r\n"
	if st := s.Scan([]byte(evil)); st.URL != "" {
		t.Fatalf("an unacceptable URL offered: %q", st.URL)
	}
	// with an evil link before a good one, the good one is offered
	mixed := evil + "\x1b]8;;https://claude.ai/oauth/authorize?ok=1\x1b\\link\x1b]8;;\x1b\\\r\n"
	if st := s.Scan([]byte(mixed)); st.URL != "https://claude.ai/oauth/authorize?ok=1" {
		t.Fatalf("mixed: %q", st.URL)
	}
}

// The text rejoin: a URL broken over full-width rows comes back whole; a
// URL that ends inside its line, or is followed by a line that isn't all
// URL characters, is taken as it stands.
func TestSigninTextRejoin(t *testing.T) {
	s := claudeSpec(t)
	u := "https://claude.ai/oauth/authorize?code=true&client_id=abc&state=0123456789abcdefghij"
	rows := []string{u[:30], u[30:60], u[60:]}
	wrapped := "Use the url below:\r\n\r\n" + strings.Join(rows, "\r\n") + "\r\n\r\nPaste code here if prompted >"
	if st := s.Scan([]byte(wrapped)); st.URL != u || !st.Code {
		t.Fatalf("wrapped: %+v", st)
	}
	indented := "  " + rows[0] + "\r\n  " + rows[1] + "\r\n  " + rows[2] + "\r\n"
	if st := s.Scan([]byte(indented)); st.URL != u {
		t.Fatalf("indented: %q", st.URL)
	}
	plain := "visit: " + u + "\nPaste code here if prompted > "
	if st := s.Scan([]byte(plain)); st.URL != u {
		t.Fatalf("one line: %q", st.URL)
	}
	// a row that's shorter than the first ends the wrap: what follows isn't the URL's
	short := rows[0] + "\r\n" + rows[1][:10] + "\r\nnext\r\n"
	if st := s.Scan([]byte(short)); st.URL != rows[0]+rows[1][:10] {
		t.Fatalf("short row: %q", st.URL)
	}
}

// The catalog's JSON carries claude's Signin as served by GET
// /agent/providers, and only claude has one.
func TestSigninCatalog(t *testing.T) {
	for _, p := range Providers() {
		if (p.Signin != nil) != (p.ID == "claude") {
			t.Errorf("%s: signin %+v", p.ID, p.Signin)
		}
	}
	s := claudeSpec(t)
	if strings.Join(s.Argv, " ") != s.Command || s.TTY || s.Fallback != "claude /exit" || len(s.Env) != 0 {
		t.Fatalf("claude's sign-in: %+v", s)
	}
	c, _ := Lookup("claude")
	if c.Env["CLAUDE_CODE_REMOTE"] != "" {
		t.Fatal("the adapter runs in Claude Code's remote-session mode")
	}
}
