package main

// What matching knows about one request: its wire, model and stream flag,
// an exact hash of its body, the key of the conversation ("thread") it
// belongs to, and a normalized text of its system prompt and messages for
// the similarity check. Built from a live request at replay time and from
// each recorded request when a cassette loads (never stored: a cassette
// keeps the request body, so a better digest needs no re-recording).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The wires matching tells apart.
const (
	apiChat        = "openai-chat"        // POST …/chat/completions
	apiResponses   = "openai-responses"   // POST …/responses
	apiCompletions = "openai-completions" // POST …/completions (legacy)
	apiMessages    = "anthropic-messages" // POST …/messages
	apiOther       = "other"              // models, embeddings, count_tokens, …
)

func apiOf(path string) string {
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return apiChat
	case strings.HasSuffix(path, "/responses"):
		return apiResponses
	case strings.HasSuffix(path, "/completions"):
		return apiCompletions
	case strings.HasSuffix(path, "/messages"):
		return apiMessages
	}
	return apiOther
}

// msg is one conversation message, normalized: a role and its text (tool
// calls and results written into it).
type msg struct{ role, text string }

type digest struct {
	api, method, path, query string
	model                    string
	stream                   bool
	hash                     string   // the exact body (canonical JSON without volatileFields)
	root                     string   // the thread key: wire, model, system prompt start, first user message
	system                   string   // normalized
	msgs                     []msg    // normalized, in order
	tail                     int      // msgs[tail:] are what is new since the model last spoke
	tools                    []string // sorted tool names
}

// sequenced: a model call, matched by its place in the take. Everything else
// (GET /v1/models, embeddings, count_tokens) is a lookup: answered from the
// cassette by content, any number of times, without moving the sequence.
func (d *digest) sequenced() bool { return d.method == "POST" && d.api != apiOther }

// kind is what a recorded exchange must share with a request to answer it.
func (d *digest) kind() string {
	w := d.api
	if w == apiOther {
		w = d.path
	}
	return fmt.Sprintf("%s %s %s stream=%v", d.method, w, d.model, d.stream)
}

// volatileFields are top-level request fields that differ on every run of
// the same agent and say nothing about the conversation: Claude Code's
// metadata.user_id carries its session id, OpenAI callers set user,
// prompt_cache_key and safety_identifier.
var volatileFields = []string{"metadata", "user", "prompt_cache_key", "safety_identifier"}

func describe(method, path, query string, body []byte) *digest {
	d := &digest{api: apiOf(path), method: method, path: path, query: query, hash: exactHash(method, path, body)}
	var top map[string]json.RawMessage
	if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &top) != nil {
		d.msgs = []msg{{"body", normalize(string(body))}} // not JSON: all of it is the one message
	}
	_ = json.Unmarshal(top["model"], &d.model)
	_ = json.Unmarshal(top["stream"], &d.stream)
	var system []string
	if raw, ok := top["system"]; ok { // Anthropic: a string or text blocks
		system = append(system, partsText(raw))
	}
	if raw, ok := top["instructions"]; ok { // Responses
		system = append(system, partsText(raw))
	}
	if raw, ok := top["messages"]; ok { // Chat Completions and Anthropic
		system = append(system, d.addMessages(raw)...)
	}
	if raw, ok := top["input"]; ok { // Responses (embeddings too: a string or strings)
		system = append(system, d.addInput(raw)...)
	}
	if raw, ok := top["prompt"]; ok { // legacy completions
		d.msgs = append(d.msgs, msg{"user", normalize(partsText(raw))})
	}
	d.system = normalize(strings.Join(system, "\n"))
	for i, m := range d.msgs {
		if m.role == "assistant" {
			d.tail = i + 1
		}
	}
	d.tools = toolNames(top["tools"])
	d.root = d.rootKey()
	return d
}

// addMessages reads a messages array (Chat Completions or Anthropic shapes)
// and returns the system texts it carried.
func (d *digest) addMessages(raw json.RawMessage) (system []string) {
	var ms []struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(raw, &ms) != nil {
		return nil
	}
	for _, m := range ms {
		text := partsText(m.Content)
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, text)
			continue
		}
		for _, c := range m.ToolCalls {
			text += fmt.Sprintf(" [call %s %s]", c.Function.Name, c.Function.Arguments)
		}
		d.msgs = append(d.msgs, msg{m.Role, normalize(text)})
	}
	return system
}

// addInput reads a Responses input (a string, or items) and returns the
// system texts it carried.
func (d *digest) addInput(raw json.RawMessage) (system []string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		d.msgs = append(d.msgs, msg{"user", normalize(s)})
		return nil
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		var strs []string // embeddings: several inputs
		if json.Unmarshal(raw, &strs) == nil {
			d.msgs = append(d.msgs, msg{"user", normalize(strings.Join(strs, "\n"))})
		}
		return nil
	}
	for _, it := range items {
		typ, role := str(it["type"]), str(it["role"])
		switch {
		case typ == "reasoning": // encrypted, and the model's own: replayed as recorded
		case strings.HasSuffix(typ, "_call_output"):
			d.msgs = append(d.msgs, msg{"tool", normalize(partsText(it["output"]))})
		case strings.HasSuffix(typ, "_call"):
			args := str(it["arguments"])
			if args == "" {
				args = compact(it["input"])
			}
			d.msgs = append(d.msgs, msg{"assistant", normalize(fmt.Sprintf("[call %s %s]", str(it["name"]), args))})
		case role == "system" || role == "developer":
			system = append(system, partsText(it["content"]))
		case role != "":
			d.msgs = append(d.msgs, msg{role, normalize(partsText(it["content"]))})
		default:
			d.msgs = append(d.msgs, msg{typ, ""})
		}
	}
	return system
}

// partsText is a content's text: a string, or an array of parts of any of the
// three wires (text, tool use and results, thinking; images and documents
// by their type only).
func partsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return compact(raw)
	}
	var b strings.Builder
	for _, p := range parts {
		switch typ := str(p["type"]); typ {
		case "", "text", "input_text", "output_text":
			b.WriteString(str(p["text"]))
		case "thinking":
			b.WriteString(str(p["thinking"]))
		case "redacted_thinking":
		case "refusal":
			b.WriteString(str(p["refusal"]))
		case "tool_use", "server_tool_use":
			fmt.Fprintf(&b, "[%s %s %s]", typ, str(p["name"]), compact(p["input"]))
		case "tool_result", "web_search_tool_result":
			fmt.Fprintf(&b, "[%s %s]", typ, partsText(p["content"]))
		default: // images, documents, files: their bytes say nothing a take changes
			fmt.Fprintf(&b, "[%s]", typ)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func toolNames(raw json.RawMessage) []string {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	var names []string
	for _, t := range tools {
		name := str(t["name"])
		if name == "" {
			var f struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(t["function"], &f)
			name = f.Name
		}
		if name == "" {
			name = str(t["type"])
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// rootKey names a conversation: its wire, model and stream flag, the start of
// its system prompt and its first user message — the same for every request
// of one conversation (a turn only appends to it), and different for an
// agent's side calls (titles, summaries) and for each subagent (its task is
// its first message).
func (d *digest) rootKey() string {
	first := ""
	for _, m := range d.msgs {
		if m.role == "user" {
			first = m.text
			break
		}
	}
	sys := d.system
	if len(sys) > 120 {
		sys = sys[:120]
	}
	h := sha256.Sum256([]byte(d.kind() + "\x00" + sys + "\x00" + first))
	return hex.EncodeToString(h[:6])
}

// exactHash hashes the request as sent, minus volatileFields: JSON in
// canonical form (object keys sorted, numbers as written).
func exactHash(method, path string, body []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s %s\x00", method, path)
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if len(bytes.TrimSpace(body)) > 0 && dec.Decode(&v) == nil {
		if m, ok := v.(map[string]any); ok {
			for _, k := range volatileFields {
				delete(m, k)
			}
		}
		b, _ := json.Marshal(v)
		h.Write(b)
	} else {
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// --- normalization -----------------------------------------------------------

// masks blank out what changes between two runs of the same agent session:
// ids, dates and times, durations, long numbers (pids, ports, sizes) and
// hashes. They apply to the loose comparisons only (thread keys,
// similarity), never to the exact hash.
var masks = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Claude Code's per-request billing line at the top of its system prompt
	{regexp.MustCompile(`x-anthropic-billing-header:[^\n]*`), ""},
	{regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?`), "<date>"},
	{regexp.MustCompile(`\b(?:Mon|Tues?|Wed(?:nes)?|Thu(?:rs)?|Fri|Sat(?:ur)?|Sun)(?:day)?\b,?\s+`), ""},
	{regexp.MustCompile(`\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.?\s+\d{1,2}(?:st|nd|rd|th)?,?\s+\d{4}\b`), "<date>"},
	{regexp.MustCompile(`\b\d{1,2}\s+(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.?,?\s+\d{4}\b`), "<date>"},
	{regexp.MustCompile(`\b\d{1,2}/\d{1,2}/\d{2,4}\b`), "<date>"},
	{regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:\s?[AaPp][Mm])?\b`), "<time>"},
	{regexp.MustCompile(`\b(?:toolu|srvtoolu|call|fc|msg|resp|rs|req|chatcmpl|run|thread|file|ws|ctc)_[A-Za-z0-9_-]{6,}`), "<id>"},
	{regexp.MustCompile(`\b\d+(?:\.\d+)?\s?(?:ns|µs|us|ms|s|m|h)\b`), "<dur>"},
	{regexp.MustCompile(`\b\d{5,}\b`), "<num>"},
}

var hexRe = regexp.MustCompile(`\b[0-9a-fA-F]{7,}\b`)

// normMemo remembers normalized long texts: an agent resends its whole
// conversation on every call, so the same system prompt and messages come
// back again and again (a coding agent's run of a hundred calls would
// otherwise normalize its prefix a hundred times).
var normMemo = struct {
	sync.Mutex
	m     map[string]string
	bytes int
}{m: map[string]string{}}

func normalize(s string) string {
	if len(s) < 256 {
		return normalizeNow(s)
	}
	normMemo.Lock()
	n, ok := normMemo.m[s]
	normMemo.Unlock()
	if ok {
		return n
	}
	n = normalizeNow(s)
	normMemo.Lock()
	if normMemo.bytes > 256<<20 {
		normMemo.m, normMemo.bytes = map[string]string{}, 0
	}
	normMemo.m[s] = n
	normMemo.bytes += len(s) + len(n)
	normMemo.Unlock()
	return n
}

func normalizeNow(s string) string {
	for _, m := range masks {
		s = m.re.ReplaceAllString(s, m.repl)
	}
	s = hexRe.ReplaceAllStringFunc(s, func(h string) string {
		if strings.ContainsAny(h, "0123456789") && strings.ContainsAny(h, "abcdefABCDEF") {
			return "<hex>"
		}
		return h
	})
	return strings.Join(strings.Fields(s), " ")
}

// --- similarity --------------------------------------------------------------

// shingles is a text's set of lower-cased words and word pairs (hashed):
// forgiving enough for a reworded sentence, sharp enough that two subagents
// whose tasks differ in a few words tell apart.
type shingles map[uint64]struct{}

func shingleSet(texts ...string) shingles {
	set := shingles{}
	for _, t := range texts {
		var prev uint64
		for i, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New64a()
			h.Write([]byte(w))
			cur := h.Sum64()
			set[cur] = struct{}{}
			if i > 0 {
				set[prev*1099511628211^cur^0x9e3779b97f4a7c15] = struct{}{}
			}
			prev = cur
		}
	}
	return set
}

// jaccard is |a∩b| / |a∪b| (two empty sets are the same).
func jaccard(a, b shingles) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// texts are the digest's texts for the whole-request and the tail sets.
func (d *digest) texts() (whole, tail []string) {
	whole = append(whole, d.system)
	for i, m := range d.msgs {
		t := m.role + ": " + m.text
		whole = append(whole, t)
		if i >= d.tail {
			tail = append(tail, t)
		}
	}
	return whole, tail
}

// diffNote says where two requests part: message counts, the first message
// that differs (with a short excerpt of each side from where they differ),
// the system prompt, the tool set.
func diffNote(rec, live *digest) string {
	var notes []string
	if len(rec.msgs) != len(live.msgs) {
		notes = append(notes, fmt.Sprintf("%d messages recorded, %d live", len(rec.msgs), len(live.msgs)))
	}
	for i := 0; i < len(rec.msgs) && i < len(live.msgs); i++ {
		a, b := rec.msgs[i], live.msgs[i]
		if a == b {
			continue
		}
		if a.role != b.role {
			notes = append(notes, fmt.Sprintf("message %d is %s recorded, %s live", i+1, a.role, b.role))
			break
		}
		at := commonPrefix(a.text, b.text)
		notes = append(notes, fmt.Sprintf("message %d (%s) differs: recorded %q, live %q", i+1, a.role, excerpt(a.text, at), excerpt(b.text, at)))
		break
	}
	if rec.system != live.system {
		notes = append(notes, "the system prompt differs")
	}
	if strings.Join(rec.tools, ",") != strings.Join(live.tools, ",") {
		notes = append(notes, fmt.Sprintf("tools differ (recorded %d, live %d)", len(rec.tools), len(live.tools)))
	}
	if len(notes) == 0 {
		return "no difference after normalization"
	}
	return strings.Join(notes, "; ")
}

func commonPrefix(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// excerpt is ~70 bytes of s around offset at (where two texts part).
func excerpt(s string, at int) string {
	start := max(at-20, 0)
	for start > 0 && start < len(s) && !utf8Start(s[start]) {
		start--
	}
	end := min(start+70, len(s))
	for end < len(s) && !utf8Start(s[end]) {
		end++
	}
	out := s[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(s) {
		out += "…"
	}
	return out
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// --- small JSON helpers ------------------------------------------------------

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}
