package term

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
)

// covers D230 (review 3b) — the redactor on the joined stream.

// fakeRedactSecret is an obvious fake in the shape of a Claude Code token.
const fakeRedactSecret = "sk-ant-oat01-FAKE-not-a-real-token-0123456789abcdefXYZ_9f3e"

// said runs events through r and joins what left, per field.
func said(r *redactor, evs ...agent.Event) string {
	var b strings.Builder
	for _, e := range evs {
		for _, o := range r.event(e) {
			b.Write(o.Data)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func textOf(t *testing.T, out string) string {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		for _, f := range []string{"text", "outputDelta", "output"} {
			if s, ok := d[f].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

func msgDelta(text string) agent.Event {
	return agent.New(agent.EvMessageDelta, map[string]any{"role": "assistant", "text": text})
}

func TestRedactorMasksEveryFormOfTheSecret(t *testing.T) {
	sec := fakeRedactSecret
	spaced := strings.Join(strings.Split(sec, ""), " ")
	lines := strings.Join(strings.Split(sec, ""), "\n")
	wrapped := sec[:20] + "\n" + sec[20:40] + "\r\n  " + sec[40:]
	b64 := base64.StdEncoding.EncodeToString([]byte(sec))
	b64nl := base64.StdEncoding.EncodeToString([]byte(sec + "\n"))
	b64in1 := base64.StdEncoding.EncodeToString([]byte("K=" + sec + "; rest"))
	b64in2 := base64.URLEncoding.EncodeToString([]byte("TOKEN=" + sec + "\n\xff\xfe\xfb"))
	b64in3 := base64.StdEncoding.EncodeToString([]byte("x" + sec + "yz"))
	b64wrap := b64[:40] + "\n" + b64[40:]
	hx := hex.EncodeToString([]byte(sec))
	var pct strings.Builder
	for i := 0; i < len(sec); i++ {
		fmt.Fprintf(&pct, "%%%02X", sec[i])
	}
	for name, form := range map[string]string{
		"as it is": sec, "a space between every character": spaced, "a character per line": lines, "wrapped": wrapped,
		"base64": b64, "base64 of the value and a newline": b64nl, "base64 inside a longer string (1)": b64in1,
		"base64url inside a longer string (2)": b64in2, "base64 inside a longer string (3)": b64in3, "base64, wrapped": b64wrap,
		"hex": hx, "hex, upper case": strings.ToUpper(hx), "hex in pairs": strings.Join(chunks(hx, 2), " "),
		"percent-encoded": pct.String(), "query-escaped": url.QueryEscape(sec),
	} {
		r := newRedactor([]byte(sec))
		// whole, in one event of each kind
		whole := said(r,
			agent.New(agent.EvToolUpdate, map[string]any{"id": "t1", "output": "before " + form + " after"}),
			agent.New(agent.EvToolCall, map[string]any{"id": "t2", "rawInput": map[string]any{"command": []any{"echo", form}}}),
			agent.New(agent.EvPermissionRequest, map[string]any{"pid": "p", "toolCall": map[string]any{"title": form}}))
		if leaks(whole, sec) || !strings.Contains(whole, signinMask) || !strings.Contains(whole, "before ") || !strings.Contains(whole, " after") {
			t.Errorf("%s, whole: %s", name, whole)
		}
		// in pieces of every size, on each stream
		for _, size := range []int{1, 2, 3, 7, 16, 61} {
			for _, stream := range []func(string) agent.Event{
				msgDelta,
				func(s string) agent.Event { return agent.New(agent.EvThoughtDelta, map[string]any{"text": s}) },
				func(s string) agent.Event {
					return agent.New(agent.EvToolUpdate, map[string]any{"id": "t9", "outputDelta": s})
				},
			} {
				r := newRedactor([]byte(sec))
				var evs []agent.Event
				for _, c := range chunks("The value is "+form+" -- done", size) {
					evs = append(evs, stream(c))
				}
				evs = append(evs, agent.New(agent.EvTurnEnd, map[string]any{"turn": 1}))
				out := said(r, evs...)
				got := textOf(t, out)
				if leaks(out, sec) || leaks(got, sec) || !strings.HasPrefix(got, "The value is ") || !strings.HasSuffix(got, " -- done") || strings.Count(got, signinMask) != 1 {
					t.Errorf("%s, in pieces of %d: %q", name, size, got)
				}
			}
		}
	}
}

// leaks: any 12 characters of the secret in a row, or of its base64/hex.
func leaks(out, sec string) bool {
	flat, _ := project(out, false)
	for _, form := range []string{sec, base64.RawStdEncoding.EncodeToString([]byte(sec)), hex.EncodeToString([]byte(sec))} {
		core, _ := project(form, false)
		for i := 0; i+16 <= len(core); i++ {
			if strings.Contains(strings.ToLower(string(flat)), strings.ToLower(string(core[i:i+16]))) {
				return true
			}
		}
	}
	return false
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		out, s = append(out, s[:n]), s[n:]
	}
	return append(out, s)
}

// What isn't the secret leaves as it came — byte for byte where nothing
// was masked, complete where the end of a piece only looked like a start —
// and other credential-shaped strings are masked, split or not.
func TestRedactorLeavesTheRestAlone(t *testing.T) {
	r := newRedactor([]byte(fakeRedactSecret))
	plain := agent.New(agent.EvToolCall, map[string]any{"id": "task-0123456789-abcdefghij-klmnop.txt", "title": "<b> & risk-assessment-document-2026-final-version", "n": 1.50})
	if out := r.event(plain); len(out) != 1 || string(out[0].Data) != string(plain.Data) {
		t.Fatalf("an event with nothing to mask changed: %s", out[0].Data)
	}
	// pieces whose ends look like the start of the secret, and then aren't
	text := "this ends in s" + "k-ant and goes on; sk-an" + "other one; A" + "I and gs" + "k too."
	var evs []agent.Event
	for _, c := range chunks(text, 5) {
		evs = append(evs, msgDelta(c))
	}
	evs = append(evs, agent.New(agent.EvStatus, map[string]any{"status": "idle"}))
	if got := textOf(t, said(r, evs...)); got != text {
		t.Fatalf("harmless text came out as %q", got)
	}
	// the order holds: a held end leaves before the event that closes its stream
	out := r.event(msgDelta("ends with s"))
	out = append(out, r.event(agent.New(agent.EvToolCall, map[string]any{"id": "t1", "title": "next"}))...)
	if len(out) != 3 || out[1].Type != agent.EvMessageDelta || !strings.Contains(string(out[1].Data), `"text":"s"`) || !strings.Contains(string(out[1].Data), `"role":"assistant"`) || out[2].Type != agent.EvToolCall {
		t.Fatalf("the held end and its order: %v", out)
	}
	// another key of a harness's shape, whole and split
	other := "sk-proj-FAKEFAKEFAKEFAKEFAKEFAKE0000"
	for _, size := range []int{4, 9, 100} {
		var evs []agent.Event
		for _, c := range chunks("export OPENAI_API_KEY="+other+"\nok", size) {
			evs = append(evs, agent.New(agent.EvToolUpdate, map[string]any{"id": "t3", "outputDelta": c}))
		}
		evs = append(evs, agent.New(agent.EvToolUpdate, map[string]any{"id": "t3", "status": "completed", "output": "AIzaFAKEFAKEFAKEFAKEFAKEFAKE00 gsk_FAKEFAKEFAKEFAKEFAKE00"}))
		got := said(newRedactor([]byte(fakeRedactSecret)), evs...)
		if strings.Contains(got, "FAKEFAKE") || !strings.Contains(textOf(t, got), "export OPENAI_API_KEY="+shapeMask+"\nok") {
			t.Fatalf("a credential-shaped string, in pieces of %d: %s", size, got)
		}
	}
	// a session that resumes a transcript made on a sign-in it no longer
	// holds: the shapes alone
	if got := newRedactor(nil).mask("token " + fakeRedactSecret); strings.Contains(got, "FAKE") {
		t.Fatalf("shapes alone: %q", got)
	}
	var none *redactor
	if got := none.mask("x"); got != "x" || len(none.event(plain)) != 1 {
		t.Fatal("no redactor: nothing changes")
	}
}
