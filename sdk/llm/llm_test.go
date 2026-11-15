package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures: *-fake.* were recorded from hack/fakeopenai (the scripted
// upstream the harness and the connector's integration test use);
// *-openai.sse are written to OpenAI's published stream shapes — event
// lines on the Responses wire, "usage": null on every chunk but the last,
// cached and reasoning token details. None was captured from a provider.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var streamCases = []struct {
	file string
	want Usage
}{
	{"chat-stream-fake.sse", Usage{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 9, TotalTokens: 120}},
	{"responses-stream-fake.sse", Usage{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 7, TotalTokens: 120}},
	{"chat-stream-openai.sse", Usage{InputTokens: 2048, OutputTokens: 310, CachedTokens: 1024, ReasoningTokens: 192, TotalTokens: 2358}},
	{"responses-stream-openai.sse", Usage{InputTokens: 4096, OutputTokens: 640, CachedTokens: 3072, ReasoningTokens: 512, TotalTokens: 4736}},
}

// A relay writes whatever it read: the usage must come out the same however
// the stream is cut into writes, down to one byte at a time.
func TestStreamUsageAnyChunking(t *testing.T) {
	for _, c := range streamCases {
		raw := fixture(t, c.file)
		for _, size := range []int{1, 2, 7, 64, 4096, len(raw)} {
			var s StreamUsage
			for i := 0; i < len(raw); i += size {
				if n, err := s.Write(raw[i:min(i+size, len(raw))]); err != nil || n != min(size, len(raw)-i) {
					t.Fatalf("%s: Write = %d, %v", c.file, n, err)
				}
			}
			got, ok := s.Usage()
			if !ok || got != c.want {
				t.Errorf("%s in %d-byte writes: %+v, %v; want %+v", c.file, size, got, ok, c.want)
			}
		}
	}
}

// A relay whose stream broke off before its usage estimates from the
// events it relayed, and from a plain body's size.
func TestEventAndByteCounts(t *testing.T) {
	raw := fixture(t, "chat-stream-fake.sse")
	var s StreamUsage
	_, _ = s.Write(raw[:len(raw)/2])
	half := s.Events()
	_, _ = s.Write(raw[len(raw)/2:])
	if _, ok := s.Usage(); !ok || half == 0 || s.Events() != int64(bytes.Count(raw, []byte("data:"))) || half >= s.Events() {
		t.Errorf("events: %d at half, %d at the end, %d data lines", half, s.Events(), bytes.Count(raw, []byte("data:")))
	}
	var b BodyUsage
	_, _ = b.Write([]byte("0123456789"))
	_, _ = b.Write([]byte("abc"))
	if b.Bytes() != 13 {
		t.Errorf("bytes: %d", b.Bytes())
	}
}

func TestStreamUsageNone(t *testing.T) {
	// a chat stream whose request didn't ask for usage, and whose text
	// mentions the word
	var s StreamUsage
	_, _ = s.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\\\"usage\\\": {\\\"prompt_tokens\\\": 5\"}}],\"usage\":null}\n\ndata: [DONE]\n\n"))
	if u, ok := s.Usage(); ok {
		t.Errorf("a stream without a usage block reported %+v", u)
	}
}

// A response.completed event carrying a long answer is one line past what
// the scanner holds: its usage is still read, from the line's tail.
func TestStreamUsageOversizedLine(t *testing.T) {
	long := strings.Repeat("word ", usageLineMax/4)
	ev := map[string]any{"type": "response.completed", "response": map[string]any{
		"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": long}}}},
		"usage": map[string]any{"input_tokens": 9000, "input_tokens_details": map[string]any{"cached_tokens": 8000}, "output_tokens": 70000,
			"output_tokens_details": map[string]any{"reasoning_tokens": 12}, "total_tokens": 79000}}}
	// encoding/json sorts map keys: "usage" comes after "status" and "output", at the end, as providers send it
	b, _ := json.Marshal(ev)
	if len(b) <= usageLineMax {
		t.Fatalf("the event is %d bytes: not past the line cap", len(b))
	}
	var s StreamUsage
	_, _ = s.Write([]byte("event: response.completed\ndata: "))
	_, _ = s.Write(b)
	_, _ = s.Write([]byte("\n\n"))
	if len(s.line) > usageLineMax || cap(s.line) > 2*usageLineMax+len(b)/64 {
		t.Errorf("the scanner holds %d bytes (cap %d)", len(s.line), cap(s.line))
	}
	want := Usage{InputTokens: 9000, OutputTokens: 70000, CachedTokens: 8000, ReasoningTokens: 12, TotalTokens: 79000}
	if got, ok := s.Usage(); !ok || got != want {
		t.Errorf("oversized line: %+v, %v; want %+v", got, ok, want)
	}
}

func TestUsageFromBody(t *testing.T) {
	for _, c := range []struct {
		file string
		want Usage
	}{
		{"chat-body-fake.json", Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}},
		{"responses-body-fake.json", Usage{InputTokens: 100, OutputTokens: 20, ReasoningTokens: 7, TotalTokens: 120}},
	} {
		raw := fixture(t, c.file)
		if got, ok := UsageFromBody(raw); !ok || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v", c.file, got, ok, c.want)
		}
		var b BodyUsage
		_, _ = b.Write(raw[:10])
		_, _ = b.Write(raw[10:])
		if got, ok := b.Usage(); !ok || got != c.want {
			t.Errorf("%s through BodyUsage: %+v, %v", c.file, got, ok)
		}
	}
	for _, none := range []string{``, `{}`, `{"usage":null}`, `{"usage":"n/a"}`, `{"usage":{}}`, `not json "usage": {`, `{"error":{"message":"x"}}`} {
		if u, ok := UsageFromBody([]byte(none)); ok {
			t.Errorf("%q: usage %+v", none, u)
		}
	}
	// DeepSeek's cache field and OpenRouter's cost
	u, ok := UsageFromBody([]byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":10,"prompt_cache_hit_tokens":900,"cost":0.0042}}`))
	if !ok || u.CachedTokens != 900 || u.TotalTokens != 1010 || u.Cost == nil || *u.Cost != 0.0042 {
		t.Errorf("provider variants: %+v, %v", u, ok)
	}
	// parts never exceed their wholes
	u, _ = UsageFromBody([]byte(`{"usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":99},"output_tokens_details":{"reasoning_tokens":99}}}`))
	if u.CachedTokens != 10 || u.ReasoningTokens != 5 {
		t.Errorf("clamping: %+v", u)
	}
}

func TestBodyUsageOversized(t *testing.T) {
	var b BodyUsage
	_, _ = b.Write([]byte(`{"choices":[{"message":{"content":"`))
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < usageBodyMax/len(chunk)+2; i++ {
		_, _ = b.Write(chunk)
	}
	_, _ = b.Write([]byte(`"}}],"usage":{"prompt_tokens":7,"completion_tokens":300000,"total_tokens":300007,"prompt_tokens_details":{"cached_tokens":3}}}`))
	want := Usage{InputTokens: 7, OutputTokens: 300000, CachedTokens: 3, TotalTokens: 300007}
	if got, ok := b.Usage(); !ok || got != want {
		t.Errorf("a body past the cap: %+v, %v; want %+v", got, ok, want)
	}
	if len(b.buf) > usageBodyMax {
		t.Errorf("BodyUsage holds %d bytes", len(b.buf))
	}
}

func TestReadSSE(t *testing.T) {
	var events, datas []string
	err := ReadSSE(bytes.NewReader(fixture(t, "responses-stream-openai.sse")), func(ev string, data []byte) error {
		events = append(events, ev)
		var e ResponsesEvent
		if err := json.Unmarshal(data, &e); err != nil {
			return err
		}
		if e.Type != ev {
			t.Errorf("event line %q, type %q", ev, e.Type)
		}
		if e.Type == EventOutputTextDelta {
			datas = append(datas, e.Delta)
		}
		if e.Type == EventCompleted && (e.Response == nil || e.Response.Usage == nil || e.Response.Usage.Usage().CachedTokens != 3072) {
			t.Errorf("response.completed: %+v", e.Response)
		}
		return nil
	})
	if err != nil || len(events) != 7 || strings.Join(datas, "") != "Counted once." {
		t.Fatalf("responses stream: %v, %d events, text %q", err, len(events), datas)
	}

	// a chat stream: no event names, [DONE] ends it, nothing after is read
	text, n := "", 0
	err = ReadSSE(strings.NewReader(string(fixture(t, "chat-stream-openai.sse"))+"data: {\"after\":\"done\"}\n\n"), func(ev string, data []byte) error {
		n++
		var c ChatChunk
		if ev != "" || json.Unmarshal(data, &c) != nil {
			t.Errorf("chunk %d: event %q, %s", n, ev, data)
		}
		for _, ch := range c.Choices {
			text += ch.Delta.Text()
		}
		return nil
	})
	if err != nil || n != 5 || text != `The "usage" field is { reported last.` {
		t.Fatalf("chat stream: %v, %d chunks, text %q", err, n, text)
	}

	// fn's error stops the read; CRLF lines, a comment, multi-line data and
	// a last event without its blank line all read
	stop := errors.New("stop")
	if err := ReadSSE(strings.NewReader("data: 1\n\ndata: 2\n\n"), func(string, []byte) error { return stop }); err != stop {
		t.Errorf("fn's error: %v", err)
	}
	var got []string
	_ = ReadSSE(strings.NewReader(": keep-alive\r\n\r\nevent: a\r\ndata: one\r\ndata: two\r\n\r\ndata:three"), func(ev string, d []byte) error {
		got = append(got, ev+"|"+string(d))
		return nil
	})
	if strings.Join(got, ",") != "a|one\ntwo,|three" {
		t.Errorf("framing: %q", got)
	}
}

func TestPrices(t *testing.T) {
	cached := 0.125
	tab := PriceTable{"gpt-5": {In: 1.25, Out: 10, CachedIn: &cached}, "claude-*": {In: 3, Out: 15}, "claude-opus-*": {In: 15, Out: 75}, "local": {}}
	u := Usage{InputTokens: 2_000_000, CachedTokens: 1_000_000, OutputTokens: 500_000, ReasoningTokens: 400_000}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if c, ok := tab.Cost("gpt-5", u); !ok || !near(c, 1.25+0.125+5) {
		t.Errorf("gpt-5: %v, %v", c, ok)
	}
	if c, ok := tab.Cost("openai/gpt-5", u); !ok || !near(c, 6.375) {
		t.Errorf("an aggregator's id: %v, %v", c, ok)
	}
	if c, ok := tab.Cost("claude-sonnet-5", u); !ok || !near(c, 6+7.5) { // no cached price: cached tokens cost In
		t.Errorf("wildcard: %v, %v", c, ok)
	}
	if p, ok := tab.Lookup("anthropic/claude-opus-5"); !ok || p.In != 15 {
		t.Errorf("the longest wildcard wins: %+v, %v", p, ok)
	}
	if c, ok := tab.Cost("local", u); !ok || c != 0 {
		t.Errorf("a free model is priced at 0, not unknown: %v, %v", c, ok)
	}
	if _, ok := tab.Cost("gpt-4o", u); ok {
		t.Error("an unlisted model has no price")
	}
	if (Price{In: -1}).Valid() || !(Price{In: 1, Out: 2}).Valid() {
		t.Error("Valid")
	}
	// odd usage never prices negative
	if c := (Price{In: 1, Out: 1}).Cost(Usage{InputTokens: 10, CachedTokens: 50, OutputTokens: -3}); c < 0 {
		t.Errorf("negative cost %v", c)
	}
}

func TestModels(t *testing.T) {
	raw := `{"object":"list","data":[
		{"id":"gpt-5","object":"model","created":1763100000,"owned_by":"openai","context_length":400000,"pricing":{"prompt":"0.00000125"}},
		{"id":"qwen","max_model_len":"32768"},
		{"id":"or","top_provider":{"context_length":200000},"created":"soon"},
		{"id":"fast","owned_by":"alias","alias_of":"gpt-5"}]}`
	var l ModelList
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Data) != 4 || l.Data[0].ContextWindow() != 400000 || l.Data[1].ContextWindow() != 32768 || l.Data[2].ContextWindow() != 200000 ||
		l.Data[3].ContextWindow() != 0 || l.Data[3].AliasOf != "gpt-5" || l.Data[2].Created != 0 {
		t.Fatalf("models: %+v", l.Data)
	}
	// a relay's rewrite keeps the provider's own members
	l.Data[0].ID = "openai/gpt-5"
	out, _ := json.Marshal(l.Data[0])
	var back map[string]any
	_ = json.Unmarshal(out, &back)
	if back["id"] != "openai/gpt-5" || back["context_length"] != float64(400000) || back["pricing"] == nil || back["owned_by"] != "openai" {
		t.Errorf("round trip: %s", out)
	}
}

func TestJoinURL(t *testing.T) {
	for _, c := range [][3]string{
		{"https://api.openai.com", PathChat, "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/", PathModels, "https://api.openai.com/v1/models"},
		{"https://openrouter.ai/api/v1", PathChat, "https://openrouter.ai/api/v1/chat/completions"},
		{"https://openrouter.ai/api/v1/", PathResponses, "https://openrouter.ai/api/v1/responses"},
		{"http://127.0.0.1:11434/v1", PathModels, "http://127.0.0.1:11434/v1/models"},
	} {
		if got := JoinURL(c[0], c[1]); got != c[2] {
			t.Errorf("JoinURL(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestRewriteRequest(t *testing.T) {
	in := `{"model":"fast","stream":true,"messages":[{"role":"user","content":"hi"}],"temperature":0.2,"x_vendor":{"a":[1,2.50]},"stream_options":{"continuous_usage_stats":true}}`
	out, info, err := RewriteRequest([]byte(in), "gpt-5", true)
	if err != nil || info.Model != "fast" || !info.Stream {
		t.Fatalf("info %+v, %v", info, err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(out, &m)
	if string(m["model"]) != `"gpt-5"` || string(m["temperature"]) != "0.2" || string(m["x_vendor"]) != `{"a":[1,2.50]}` ||
		string(m["stream_options"]) != `{"continuous_usage_stats":true,"include_usage":true}` || string(m["messages"]) != `[{"role":"user","content":"hi"}]` {
		t.Errorf("rewritten: %s", out)
	}
	// the body is always written anew: a member sent twice goes out once, with the value that was read
	twice := `{"stream":true,"model":"gpt-5","input":"hi","stream":false}`
	out, info, err = RewriteRequest([]byte(twice), "gpt-5", false)
	if err != nil || info.Stream || string(out) != `{"input":"hi","model":"gpt-5","stream":false}` {
		t.Errorf("a duplicated member: %s, %+v, %v", out, info, err)
	}
	// "stream" is a boolean: anything a lenient upstream might still read as true is refused
	for _, bad := range []string{`"true"`, `1`, `"yes"`, `{}`, `[true]`} {
		if _, err := OpenRequest([]byte(`{"model":"m","stream":` + bad + `}`)); !errors.Is(err, ErrBadStream) {
			t.Errorf("stream %s: %v", bad, err)
		}
	}
	if r, err := OpenRequest([]byte(`{"model":"m","stream":null,"background":true}`)); err != nil || r.Stream || string(r.Member("background")) != "true" || r.Member("absent") != nil {
		t.Errorf("stream null: %+v, %v", r, err)
	}
	for _, bad := range []string{`{"model":5}`, `{"model":["m"]}`} {
		if _, err := OpenRequest([]byte(bad)); !errors.Is(err, ErrBadModel) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// a client's include_usage:false is overridden; a non-streaming call gains no stream_options
	out, _, _ = RewriteRequest([]byte(`{"model":"m","stream":true,"stream_options":{"include_usage":false}}`), "", true)
	if !strings.Contains(string(out), `"include_usage":true`) {
		t.Errorf("include_usage: %s", out)
	}
	out, _, _ = RewriteRequest([]byte(`{"model":"m"}`), "", true)
	if strings.Contains(string(out), "stream_options") {
		t.Errorf("not streaming: %s", out)
	}
	if _, _, err := RewriteRequest([]byte(`[1]`), "m", true); err == nil {
		t.Error("a body that is no object is an error")
	}
	if _, err := OpenRequest([]byte(`null`)); !errors.Is(err, ErrNotObject) {
		t.Errorf("null: %v", err)
	}
}

func TestErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, 429, Error{Message: "quota reached", Type: ErrTypeQuota, Code: "xbin_quota_exceeded", Extra: map[string]any{"resets": "2026-11-15T00:00:00Z"}})
	if rec.Code != 429 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("answer: %d %v", rec.Code, rec.Header())
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	e := raw["error"]
	if e["message"] != "quota reached" || e["type"] != ErrTypeQuota || e["code"] != "xbin_quota_exceeded" || e["resets"] != "2026-11-15T00:00:00Z" {
		t.Errorf("body: %s", rec.Body)
	}
	if _, has := e["param"]; !has {
		t.Error("param is written (null) like OpenAI's")
	}
	got, ok := ParseError(rec.Body.Bytes())
	if !ok || got.Message != "quota reached" || got.Extra["resets"] != "2026-11-15T00:00:00Z" || got.Error() != "quota reached (xbin_quota_exceeded)" {
		t.Errorf("ParseError: %+v, %v", got, ok)
	}
	// a provider's bare string, a numeric code, and bodies without an error
	if e, ok := ParseError([]byte(`{"error":"model overloaded"}`)); !ok || e.Message != "model overloaded" {
		t.Errorf("string error: %+v, %v", e, ok)
	}
	if e, ok := ParseError([]byte(`{"error":{"message":"no","code":404}}`)); !ok || e.Code != float64(404) {
		t.Errorf("numeric code: %+v, %v", e, ok)
	}
	for _, none := range []string{``, `<html>`, `{}`, `{"error":null}`, `{"error":{}}`} {
		if _, ok := ParseError([]byte(none)); ok {
			t.Errorf("%q parsed as an error", none)
		}
	}
	for status, want := range map[int]string{400: ErrTypeInvalidRequest, 401: ErrTypeAuthentication, 403: ErrTypePermission, 404: ErrTypeNotFound,
		429: ErrTypeRateLimit, 500: ErrTypeAPI, 502: ErrTypeAPI, 503: ErrTypeUnavailable} {
		if got := TypeForStatus(status); got != want {
			t.Errorf("TypeForStatus(%d) = %q, want %q", status, got, want)
		}
	}
}

// ReadData takes each data line by itself: the wires' streams read as with
// ReadSSE, and so does a server that leaves out the blank lines, pads its
// lines or names no event.
func TestReadData(t *testing.T) {
	for _, f := range []string{"chat-stream-openai.sse", "chat-stream-fake.sse", "responses-stream-openai.sse", "responses-stream-fake.sse"} {
		var want, got []string
		if err := ReadSSE(bytes.NewReader(fixture(t, f)), func(_ string, d []byte) error { want = append(want, string(d)); return nil }); err != nil {
			t.Fatal(err)
		}
		if err := ReadData(bytes.NewReader(fixture(t, f)), func(d []byte) error { got = append(got, string(d)); return nil }); err != nil {
			t.Fatal(err)
		}
		if len(want) == 0 || strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: ReadData read %d payloads, ReadSSE %d", f, len(got), len(want))
		}
	}
	var got []string
	in := ": keep-alive\r\nevent: a\r\ndata: {\"n\":1}\r\ndata:{\"n\":2}\n  data:   {\"n\":3}  \ndata:\n\nid: 7\ndata: [DONE]\ndata: {\"after\":true}\n"
	if err := ReadData(strings.NewReader(in), func(d []byte) error { got = append(got, string(d)); return nil }); err != nil ||
		strings.Join(got, ",") != `{"n":1},{"n":2},{"n":3}` {
		t.Errorf("framing: %q, %v", got, err)
	}
	// a last line without its newline is read; fn's error stops the read
	got = nil
	_ = ReadData(strings.NewReader("data: 1\ndata: 2"), func(d []byte) error { got = append(got, string(d)); return nil })
	stop := errors.New("stop")
	if err := ReadData(strings.NewReader("data: 1\ndata: 2\n"), func([]byte) error { return stop }); err != stop || strings.Join(got, ",") != "1,2" {
		t.Errorf("fn's error: %v; last line: %q", err, got)
	}
	// a read error is returned as it is, and a line past MaxSSELine ends both readers
	broken := errors.New("broken")
	if err := ReadData(io.MultiReader(strings.NewReader("data: 1\n"), errReader{broken}), func([]byte) error { return nil }); err != broken {
		t.Errorf("a read error: %v", err)
	}
	long := io.MultiReader(strings.NewReader("data: "), io.LimitReader(zeros{}, MaxSSELine+64<<10))
	if err := ReadData(long, func([]byte) error { return nil }); err != ErrSSELineTooLong {
		t.Errorf("a line too long: %v", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = '0'
	}
	return len(p), nil
}

// What a server writes with the types: a chunk's choice and a usage block's
// details are named, so a fake upstream or a translating proxy builds them.
func TestEmittedShapes(t *testing.T) {
	stop := "stop"
	b, _ := json.Marshal(ChatChunk{Choices: []ChatChunkChoice{{Delta: ChatReplyFields{Content: json.RawMessage(`"hi"`)}, FinishReason: &stop}}})
	if string(b) != `{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}` {
		t.Errorf("chunk: %s", b)
	}
	b, _ = json.Marshal(ChatChunk{Choices: []ChatChunkChoice{}, Usage: &ChatUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
		CompletionTokensDetails: &OutputDetails{ReasoningTokens: 9}}})
	if string(b) != `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"completion_tokens_details":{"reasoning_tokens":9}}}` {
		t.Errorf("usage chunk: %s", b)
	}
	b, _ = json.Marshal(ResponsesUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120, InputTokensDetails: &InputDetails{CachedTokens: 64}})
	if string(b) != `{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":64}}` {
		t.Errorf("responses usage: %s", b)
	}
}

// The request types marshal to what the providers take, and the reply
// types read what they send.
func TestWireTypes(t *testing.T) {
	req := ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}.WithStreamUsage()
	b, _ := json.Marshal(req)
	if string(b) != `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}` {
		t.Errorf("chat request: %s", b)
	}
	rb, _ := json.Marshal(ResponsesRequest{Model: "m", Input: []any{InputMessage{Role: "user", Content: "hi"},
		FunctionCallOutput{Type: "function_call_output", CallID: "c1", Output: "ok"}}})
	if string(rb) != `{"model":"m","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c1","output":"ok"}],"store":false}` {
		t.Errorf("responses request: %s", rb)
	}
	var cr ChatResponse
	if err := json.Unmarshal(fixture(t, "chat-body-fake.json"), &cr); err != nil || len(cr.Choices) != 1 ||
		cr.Choices[0].Message.Text() != "Hello from the fake model." || cr.Choices[0].Message.ReasoningText() != "Considering the greeting…" ||
		cr.Usage == nil || cr.Usage.Usage().TotalTokens != 120 {
		t.Errorf("chat response: %+v, %v", cr, err)
	}
	var rr Response
	if err := json.Unmarshal(fixture(t, "responses-body-fake.json"), &rr); err != nil || rr.Status != "completed" || len(rr.Output) != 2 {
		t.Fatalf("response: %+v, %v", rr, err)
	}
	var item OutputItem
	if err := json.Unmarshal(rr.Output[1], &item); err != nil || item.Type != "message" || item.Content[0].Text != "Hello from the fake model." {
		t.Errorf("output item: %+v, %v", item, err)
	}
	// an upstream failure inside a stream
	var c ChatChunk
	if err := json.Unmarshal([]byte(`{"error":{"message":"overloaded","code":529}}`), &c); err != nil || c.Error == nil || c.Error.Message != "overloaded" {
		t.Errorf("error chunk: %+v, %v", c, err)
	}
}
