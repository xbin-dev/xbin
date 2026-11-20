package llm

// replay.go — what the connector remembers of upstreams that refuse the
// reasoning a translated call sends back (D249, docs/llm.md "Reasoning
// effort"). A Responses request translated to chat completions sends a
// model's earlier reasoning back to it as reasoning_content. An upstream
// that refuses the field — a 400 whose error names it (namesField), the
// same request without it then working — is remembered in memory for the
// CALLER that made the request (the calling tile and the person it acts
// for, or the person calling directly) and that upstream's model (by the
// translation's tag: upstream name, base URL, model): later translated
// calls of that caller to it send none, and say so (input.reasoning in
// X-XBin-LLM-Dropped). One caller's requests never change what another
// caller sends. The admin sees it on the model's row ("replay off for N
// callers") and can reset it; a restart, and any change of the upstream,
// forgets it. A refusal that names no such field is never remembered.

import (
	"container/list"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/server"
	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// maxReplayOff bounds the memory: past it, the least recently used entry
// is forgotten (its caller sends its reasoning again, and learns again).
const maxReplayOff = 1024

// replayField is the chat field a translation sends a model's reasoning
// back in.
const replayField = "reasoning_content"

// ReplayOff is one model whose reasoning some callers' translated calls no
// longer send back: since when (the earliest), the field its upstream
// refused, and for how many callers.
type ReplayOff struct {
	Since   time.Time `json:"since"`
	Field   string    `json:"field"`
	Callers int       `json:"callers"`
}

type replayEntry struct {
	upstream, model, tag, caller, field string
	since                               time.Time
}

type replayMemo struct {
	mu    sync.Mutex
	order *list.List               // of *replayEntry, the most recently used first
	at    map[string]*list.Element // tag + caller → its element
}

// replayCaller names a caller for the memory: the calling tile and the
// person it acts for, or the person calling directly.
func replayCaller(c Caller) string { return c.Component + "\x00" + c.User }

func (m *replayMemo) refused(tag, caller string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.at[tag+"\x00"+caller]
	if ok {
		m.order.MoveToFront(el)
	}
	return ok
}

func (m *replayMemo) remember(upstream, model, tag, caller, field string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.at == nil {
		m.at, m.order = map[string]*list.Element{}, list.New()
	}
	k := tag + "\x00" + caller
	if el, ok := m.at[k]; ok {
		m.order.MoveToFront(el)
		return
	}
	m.at[k] = m.order.PushFront(&replayEntry{upstream: upstream, model: model, tag: tag, caller: caller, field: field, since: now})
	for m.order.Len() > maxReplayOff {
		e := m.order.Remove(m.order.Back()).(*replayEntry)
		delete(m.at, e.tag+"\x00"+e.caller)
	}
}

// retain keeps the entries keep says yes to.
func (m *replayMemo) retain(keep func(e *replayEntry) bool) (dropped int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.order == nil {
		return 0
	}
	for el := m.order.Front(); el != nil; {
		next := el.Next()
		if e := el.Value.(*replayEntry); !keep(e) {
			m.order.Remove(el)
			delete(m.at, e.tag+"\x00"+e.caller)
			dropped++
		}
		el = next
	}
	return dropped
}

// reset forgets model on upstream for every caller ("" model: all of
// upstream's); whether anything was forgotten.
func (m *replayMemo) reset(upstream, model string) bool {
	return m.retain(func(e *replayEntry) bool { return e.upstream != upstream || (model != "" && e.model != model) }) > 0
}

// of is upstream's models with their replay off, for the view.
func (m *replayMemo) of(upstream string) map[string]ReplayOff {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out map[string]ReplayOff
	for _, el := range m.at {
		e := el.Value.(*replayEntry)
		if e.upstream != upstream {
			continue
		}
		if out == nil {
			out = map[string]ReplayOff{}
		}
		r := out[e.model]
		if r.Callers == 0 || e.since.Before(r.Since) {
			r.Since, r.Field = e.since, e.field
		}
		r.Callers++
		out[e.model] = r
	}
	return out
}

// namesField reports whether an upstream's refusal (its body, as it gave
// it) names field — the one thing that tells "it won't take the reasoning
// sent back" from any other 400. Only the parsed error is read, never the
// raw body (an error that echoes the request would carry anything a caller
// put in it): its param naming the field (messages[1].reasoning_content),
// its code being it, or its message naming it as a word of its own outside
// any quoted span — a quoted span counts only when it is the field (or a
// path to it) alone: a field name inside quoted request text is no
// evidence.
func namesField(body []byte, field string) bool {
	e, ok := sdkllm.ParseError(body)
	if !ok {
		return false
	}
	if e.Param != nil && fieldPath(*e.Param, field) {
		return true
	}
	if code, ok := e.Code.(string); ok && fieldPath(code, field) {
		return true
	}
	return namedInMessage(e.Message, field)
}

// fieldPath reports whether s is field, or a path ending in it
// (messages.1.reasoning_content, messages[1].reasoning_content,
// body/messages/1/reasoning_content).
func fieldPath(s, field string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n'\"`") {
		return false
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '[' || r == ']' || r == '/' })
	return len(parts) > 0 && parts[len(parts)-1] == field
}

// namedInMessage reports whether msg names field as a word of its own,
// outside any quoted span, or as a quoted span of its own.
func namedInMessage(msg, field string) bool {
	type span struct{ from, to int } // [from, to): the quoted text, without its quotes
	var spans []span
	word := func(i int) bool {
		if i < 0 || i >= len(msg) {
			return false
		}
		c := msg[i]
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	for i := 0; i < len(msg); i++ {
		q := msg[i]
		if q != '\'' && q != '"' && q != '`' {
			continue
		}
		if q == '\'' && word(i-1) { // an apostrophe (it's, model's), no quote
			continue
		}
		j := i + 1
		for ; j < len(msg); j++ {
			if msg[j] == q && (q != '\'' || !word(j+1)) {
				break
			}
		}
		if j >= len(msg) { // never closed: the rest is quoted
			spans = append(spans, span{i + 1, len(msg)})
			break
		}
		spans = append(spans, span{i + 1, j})
		i = j
	}
	inside := func(at int) (span, bool) {
		for _, sp := range spans {
			if at >= sp.from && at < sp.to {
				return sp, true
			}
		}
		return span{}, false
	}
	for i := 0; ; {
		k := strings.Index(msg[i:], field)
		if k < 0 {
			return false
		}
		at := i + k
		end := at + len(field)
		i = end
		if word(at-1) || word(end) {
			continue
		}
		sp, quoted := inside(at)
		if !quoted || fieldPath(msg[sp.from:sp.to], field) {
			return true
		}
	}
}

// apiReplayReset — POST /llm/upstreams/{name}/replay-reset {model?}: the
// model's (no model: every model's of the upstream) remembered refusal is
// forgotten; its reasoning is sent back again. Admin. 404 when nothing was
// remembered.
func (s *Service) apiReplayReset(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := decode(r, &body); err != nil {
		server.WriteError(w, http.StatusBadRequest, "need {model?}", docs)
		return
	}
	if !s.noReplay.reset(r.PathValue("name"), body.Model) {
		server.WriteError(w, http.StatusNotFound, "nothing is remembered of that", docs)
		return
	}
	server.WriteJSON(w, http.StatusOK, s.configView())
}
