package term

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"maps"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/agent"
)

// signinredact.go — what a session that holds a saved sign-in (D230) says
// has the sign-in masked before it leaves the session: every event passes
// here on its way to the log, and so to pages, history, a workspace agent's
// tile_agent_result and xbind's own log lines.
//
// It lowers ACCIDENTAL exposure and is not a guarantee: the coding agent,
// its model and everything it runs hold the credential and can write it
// out in a form nothing recognises. What is masked:
//
//   - the value itself, with anything that isn't a letter or digit between
//     its characters (spaces, newlines, dashes, one character per line);
//   - its base64 (either alphabet, wrapped, at any offset in a longer
//     encoded string) and its hex or %XX form, in either case;
//   - anything shaped like a harness credential (sk-…, AIza…, …) — another
//     key a tool printed, or the tail of this one;
//
// on the JOINED text of a stream, not chunk by chunk: an agent's message,
// its thinking and a tool's output arrive in pieces, and a value split
// across two of them is matched because the end of a piece that could be
// the start of a match is held back until the next piece settles it (and
// let go at the stream's end).

// needle is one form of the secret, as its letters and digits alone.
type needle struct {
	core []byte
	fold bool // matched without case (hex)
	pad  int  // base64: characters either side that hold a part of the value's first or last byte
}

// shapes are what the harnesses' credentials look like in text; shapeTail
// is the end of a text that could be the start of one.
var (
	shapes = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bgsk_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bxai-[A-Za-z0-9]{20,}`),
	}
	shapeTail = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]*|AIza[A-Za-z0-9_-]*|gsk_[A-Za-z0-9]*|xai-[A-Za-z0-9]*|s|sk|A|AI|AIz|g|gs|gsk|x|xa|xai)$`)
)

const (
	shapeMask   = "[redacted]"
	redactHold  = 8 << 10 // the most text held back for a match that may yet complete
	shapeHold   = 512     // …and for a credential-shaped string still being written
	minRedacted = 8       // a shorter secret isn't one (signins.CleanSecret asks for more)
)

// redactor masks one session's saved sign-in. Safe for concurrent use.
type redactor struct {
	mu      sync.Mutex
	needles []needle
	held    map[string]*heldText // a stream's held-back end, by stream
}

// heldText is the end of a stream not yet let out, and the event it will
// leave in.
type heldText struct {
	ev    agent.Event
	rest  map[string]any // the event's other fields
	field string
	text  string
}

// newRedactor is a redactor for secret; without one (a session that
// resumes a transcript made on a sign-in it no longer holds) it masks the
// credential shapes alone.
func newRedactor(secret []byte) *redactor {
	r := &redactor{held: map[string]*heldText{}}
	if len(secret) < minRedacted {
		return r
	}
	add := func(s []byte, fold bool, pad int) {
		if core, _ := project(string(s), fold); len(core) >= minRedacted {
			r.needles = append(r.needles, needle{core, fold, pad})
		}
	}
	add(secret, false, 0)
	add([]byte(hex.EncodeToString(secret)), true, 0) // also its %XX form: the same digits
	// base64 at each of the three offsets the value can sit at in a longer
	// string, less the characters at either end that depend on its
	// neighbours; + / - _ = are not letters or digits, so one needle serves
	// both alphabets and any wrapping
	for off := 0; off < 3; off++ {
		enc := base64.RawStdEncoding.EncodeToString(append(make([]byte, off), secret...))
		lead := []int{0, 2, 3}[off]
		trail := 0
		if (off+len(secret))%3 != 0 {
			trail = 1
		}
		if len(enc) > lead+trail {
			add([]byte(enc[lead:len(enc)-trail]), false, 3)
		}
	}
	return r
}

// drop forgets the secret's forms (the session ended).
func (r *redactor) drop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.needles {
		for i := range n.core {
			n.core[i] = 0
		}
	}
	r.needles, r.held = nil, map[string]*heldText{}
}

// project is s's letters and digits alone (lower-cased with fold), and
// where each sits in s.
func project(s string, fold bool) (core []byte, idx []int) {
	core, idx = make([]byte, 0, len(s)), make([]int, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
			if fold {
				c += 'a' - 'A'
			}
		default:
			continue
		}
		core, idx = append(core, c), append(idx, i)
	}
	return core, idx
}

func b64Char(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '+' || c == '/' || c == '-' || c == '_' || c == '='
}

// b64End marks, inside a stream's text only, a mask that stands for a
// base64 form: up to three base64 characters after it hold the rest of the
// value's last byte and may arrive with a later piece (endings).
const b64End = "\x00"

var b64Ending = regexp.MustCompile(b64End + `[A-Za-z0-9+/_=-]{0,3}`)
var b64Open = regexp.MustCompile(regexp.QuoteMeta(signinMask) + b64End + `[A-Za-z0-9+/_=-]{0,2}$`)

// maskNeedles is s with every form of the secret masked. stream: s is a
// stream's text so far, and a base64 form's mask is marked (b64End).
func (r *redactor) maskNeedles(s string, stream bool) string {
	if len(r.needles) == 0 || len(s) < minRedacted {
		return s
	}
	core, idx := project(s, false)
	var low []byte
	var spans [][3]int
	for _, n := range r.needles {
		hay := core
		if n.fold {
			if low == nil {
				low = bytes.ToLower(core)
			}
			hay = low
		}
		for from := 0; ; {
			i := bytes.Index(hay[from:], n.core)
			if i < 0 {
				break
			}
			i += from
			a, b := idx[i], idx[i+len(n.core)-1]+1
			for k := 0; k < n.pad && a > 0 && b64Char(s[a-1]); k++ {
				a--
			}
			for k := 0; k < n.pad && b < len(s) && b64Char(s[b]); k++ {
				b++
			}
			spans = append(spans, [3]int{a, b, n.pad})
			from = i + len(n.core)
		}
	}
	if len(spans) == 0 {
		return s
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var out strings.Builder
	at := 0
	for _, sp := range spans {
		if sp[0] >= at { // else it overlaps the last one: one mask covers both
			out.WriteString(s[at:sp[0]])
			out.WriteString(signinMask)
			if stream && sp[2] > 0 && sp[1] == len(s) {
				out.WriteString(b64End)
			}
		}
		at = max(at, sp[1])
	}
	out.WriteString(s[at:])
	return out.String()
}

func maskShapes(s string) string {
	for _, re := range shapes {
		s = re.ReplaceAllString(s, shapeMask)
	}
	return s
}

// mask is a whole text — one that is not a piece of a stream — masked.
func (r *redactor) mask(s string) string {
	if r == nil {
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return maskShapes(r.maskNeedles(s, false))
}

// holdFrom is where the end of s that could still become a match starts
// (len(s): nothing is held): the start of a form of the secret, or of a
// credential-shaped string, that s ends inside of.
func (r *redactor) holdFrom(s string) int {
	at := len(s)
	if loc := shapeTail.FindStringIndex(s); loc != nil && len(s)-loc[0] <= shapeHold {
		at = loc[0]
	}
	if loc := b64Open.FindStringIndex(s); loc != nil { // a base64 form's last characters may still come
		at = min(at, loc[0])
	}
	if len(r.needles) == 0 {
		return at
	}
	core, idx := project(s, false)
	var low []byte
	for _, n := range r.needles {
		hay := core
		if n.fold {
			if low == nil {
				low = bytes.ToLower(core)
			}
			hay = low
		}
		for k := min(len(n.core)-1, len(hay)); k >= 1; k-- {
			if bytes.HasSuffix(hay, n.core[:k]) {
				if p := idx[len(hay)-k]; p < at && len(s)-p <= redactHold {
					at = p
				}
				break
			}
		}
	}
	return at
}

// piece takes the next piece of a stream: what may leave now, masked, and
// what is held back for the next piece. r.mu held.
func (r *redactor) piece(held, next string) (out, hold string) {
	s := r.maskNeedles(held+next, true)
	at := r.holdFrom(s)
	return endings(maskShapes(s[:at])), s[at:]
}

// endings drops the marks of base64 masks with what is left of the value
// after them.
func endings(s string) string {
	if !strings.Contains(s, b64End) {
		return s
	}
	return b64Ending.ReplaceAllString(s, "")
}

// streamOf is the stream an event is a piece of ("" none), and the field
// its text is in.
func streamOf(e agent.Event, d map[string]any) (key, field string) {
	switch e.Type {
	case agent.EvMessageDelta:
		role, _ := d["role"].(string)
		id, _ := d["messageId"].(string)
		return "m:" + role + ":" + id, "text"
	case agent.EvThoughtDelta:
		return "t", "text"
	case agent.EvToolUpdate:
		if _, ok := d["outputDelta"].(string); ok {
			id, _ := d["id"].(string)
			return "o:" + id, "outputDelta"
		}
	}
	return "", ""
}

// event is e as it may leave the session: masked, preceded by the held
// ends of the streams e closes. A piece wholly held back leaves nothing
// (its text comes with the next one). An event nothing was masked in is
// returned as it came, byte for byte.
func (r *redactor) event(e agent.Event) []agent.Event {
	if r == nil || len(e.Data) == 0 {
		return []agent.Event{e}
	}
	dec := json.NewDecoder(bytes.NewReader(e.Data))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return []agent.Event{e}
	}
	d, _ := v.(map[string]any)
	key, field := streamOf(e, d)
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.close(e, d, key)
	changed := false
	if key != "" {
		text, _ := d[field].(string)
		delete(d, field)
		h := r.held[key]
		if h == nil {
			h = &heldText{}
		}
		say, hold := r.piece(h.text, text)
		changed = say != text
		rest := maskLeaves(r, d, &changed).(map[string]any)
		if hold != "" {
			r.held[key] = &heldText{ev: e, rest: maps.Clone(rest), field: field, text: hold}
		} else {
			delete(r.held, key)
		}
		if say == "" && e.Type != agent.EvToolUpdate {
			return out // all of it waits for the next piece
		}
		if say != "" || !changed {
			d[field] = say
		}
		v = d
	} else {
		v = maskLeaves(r, v, &changed)
	}
	if changed {
		e.Data = encodeEvent(v)
	}
	return append(out, e)
}

// close lets out the held ends of the streams e ends: a message's or a
// thought's at anything that is not its next piece; a tool's output at the
// tool's next event that isn't more output; all of them when the turn is
// over. r.mu held.
func (r *redactor) close(e agent.Event, d map[string]any, key string) []agent.Event {
	if len(r.held) == 0 {
		return nil
	}
	all := e.Type == agent.EvTurnEnd || e.Type == agent.EvStatus
	tool := ""
	if e.Type == agent.EvToolUpdate || e.Type == agent.EvToolCall {
		id, _ := d["id"].(string)
		tool = "o:" + id
	}
	var keys []string
	for k := range r.held {
		if k != key && (all || !strings.HasPrefix(k, "o:") || k == tool) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []agent.Event
	for _, k := range keys {
		h := r.held[k]
		delete(r.held, k)
		if text := endings(maskShapes(h.text)); text != "" { // nothing completed it: it was not the secret
			f := map[string]any{h.field: text}
			if id, ok := h.rest["id"]; ok && strings.HasPrefix(k, "o:") {
				f["id"] = id
			} else if !strings.HasPrefix(k, "o:") {
				for name, val := range h.rest {
					f[name] = val
				}
			}
			ev := h.ev
			ev.Seq, ev.Data = 0, encodeEvent(f)
			out = append(out, ev)
		}
	}
	return out
}

// maskLeaves is v with every string in it masked (keys are left alone).
func maskLeaves(r *redactor, v any, changed *bool) any {
	switch x := v.(type) {
	case string:
		if s := maskShapes(r.maskNeedles(x, false)); s != x {
			*changed = true
			return s
		}
	case []any:
		for i := range x {
			x[i] = maskLeaves(r, x[i], changed)
		}
	case map[string]any:
		for k := range x {
			x[k] = maskLeaves(r, x[k], changed)
		}
	}
	return v
}

func encodeEvent(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return json.RawMessage(`{}`)
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}
