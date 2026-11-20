package llm

// replay.go — what the connector remembers of upstreams that refuse the
// reasoning a translated call sends back (D249, docs/llm.md "Reasoning
// effort"). A Responses request translated to chat completions sends a
// model's earlier reasoning back to it as reasoning_content. An upstream
// that refuses the field (a 400 whose body names it, and the same request
// without it then works) is remembered, per upstream and model, in memory:
// later translated calls to it send none, and say so (input.reasoning in
// X-XBin-LLM-Dropped). The admin sees it on the model's row and can reset
// it; a restart forgets it. A refusal that names no such field — a caller
// can provoke a 400 of its own — is never remembered.

import (
	"bytes"
	"net/http"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/server"
)

// maxReplayOff bounds the memory: a refusal past it is not remembered.
const maxReplayOff = 1024

// replayField is the chat field a translation sends a model's reasoning
// back in.
const replayField = "reasoning_content"

// ReplayOff is one model whose reasoning is not sent back: since when, and
// the field its upstream refused.
type ReplayOff struct {
	Since time.Time `json:"since"`
	Field string    `json:"field"`
}

type replayMemo struct {
	mu  sync.Mutex
	off map[string]map[string]ReplayOff // upstream → model → since
}

func (m *replayMemo) refused(upstream, model string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.off[upstream][model]
	return ok
}

func (m *replayMemo) remember(upstream, model, field string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, ms := range m.off {
		n += len(ms)
	}
	if n >= maxReplayOff {
		return
	}
	if m.off == nil {
		m.off = map[string]map[string]ReplayOff{}
	}
	if m.off[upstream] == nil {
		m.off[upstream] = map[string]ReplayOff{}
	}
	if _, ok := m.off[upstream][model]; !ok {
		m.off[upstream][model] = ReplayOff{Since: now, Field: field}
	}
}

// reset forgets model on upstream ("" model: all of upstream's); whether
// anything was forgotten.
func (m *replayMemo) reset(upstream, model string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ms, ok := m.off[upstream]
	if !ok {
		return false
	}
	if model == "" {
		delete(m.off, upstream)
		return true
	}
	if _, ok := ms[model]; !ok {
		return false
	}
	delete(ms, model)
	if len(ms) == 0 {
		delete(m.off, upstream)
	}
	return true
}

func (m *replayMemo) of(upstream string) map[string]ReplayOff {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.off[upstream]) == 0 {
		return nil
	}
	out := make(map[string]ReplayOff, len(m.off[upstream]))
	for k, v := range m.off[upstream] {
		out[k] = v
	}
	return out
}

// namesField reports whether an upstream's refusal (its body, as it gave
// it) names field — the one thing that tells "it won't take the reasoning
// sent back" from any other 400.
func namesField(body []byte, field string) bool {
	return bytes.Contains(body, []byte(field))
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
