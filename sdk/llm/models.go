package llm

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// Model is one entry of GET /v1/models. Providers add fields of their own
// (context sizes, prices, capabilities); Extra keeps every member this type
// has no field for, and MarshalJSON writes them back, so a relay passes a
// provider's model objects through whole.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"` // "model"
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
	// AliasOf names the model an alias stands for (llm-gw and the connector
	// list their aliases as models).
	AliasOf string                     `json:"alias_of,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// ModelList is the answer of GET /v1/models.
type ModelList struct {
	Object string  `json:"object"` // "list"
	Data   []Model `json:"data"`
}

// UnmarshalJSON reads a model object member by member: the list is the
// provider's, and a member of an unexpected type costs only that member,
// never the whole list.
func (m *Model) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = Model{}
	for k, v := range raw {
		switch k {
		case "id":
			m.ID = rawString(v)
		case "object":
			m.Object = rawString(v)
		case "owned_by":
			m.OwnedBy = rawString(v)
		case "alias_of":
			m.AliasOf = rawString(v)
		case "created":
			m.Created = rawInt(v)
		default:
			if m.Extra == nil {
				m.Extra = map[string]json.RawMessage{}
			}
			m.Extra[k] = v
		}
	}
	return nil
}

// MarshalJSON writes the known members and Extra's beside them.
func (m Model) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 5+len(m.Extra))
	for k, v := range m.Extra {
		out[k] = v
	}
	out["id"] = m.ID
	if m.Object != "" {
		out["object"] = m.Object
	}
	if m.Created != 0 {
		out["created"] = m.Created
	}
	if m.OwnedBy != "" {
		out["owned_by"] = m.OwnedBy
	}
	if m.AliasOf != "" {
		out["alias_of"] = m.AliasOf
	}
	return json.Marshal(out)
}

// contextKeys are where OpenAI-compatible model lists put a context size:
// Anthropic (max_input_tokens), OpenRouter and Together (context_length,
// and top_provider.context_length), Groq (context_window), vLLM
// (max_model_len), LM Studio (max_context_length), Gemini
// (inputTokenLimit), and xbin's own (contextWindow).
var contextKeys = []string{"contextWindow", "max_input_tokens", "context_length", "context_window",
	"max_model_len", "max_context_length", "inputTokenLimit"}

// ContextWindow is the model's input limit in tokens as its provider lists
// it, 0 when it doesn't say.
func (m Model) ContextWindow() int {
	for _, k := range contextKeys {
		if n := rawInt(m.Extra[k]); n > 0 {
			return int(n)
		}
	}
	var tp struct {
		ContextLength json.RawMessage `json:"context_length"`
	}
	if raw := m.Extra["top_provider"]; len(raw) > 0 && json.Unmarshal(raw, &tp) == nil {
		return int(max(rawInt(tp.ContextLength), 0))
	}
	return 0
}

// rawInt is a JSON number (or a number in a string) as an integer, 0 for
// anything else.
func rawInt(r json.RawMessage) int64 {
	if len(r) == 0 {
		return 0
	}
	var n json.Number
	if json.Unmarshal(r, &n) != nil {
		return 0
	}
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := n.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && math.Abs(f) < 1<<53 {
		return int64(f)
	}
	return 0
}

// JoinURL joins a provider's base URL with one of the wire's paths
// (PathChat, PathResponses, PathModels). Base URLs are written both bare
// ("https://api.openai.com") and with the version prefix
// ("https://openrouter.ai/api/v1", what most OpenAI SDKs take): the /v1 is
// never doubled.
func JoinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

// RequestInfo is what a relay reads from a request body of either wire.
type RequestInfo struct {
	Model  string
	Stream bool
}

// Request is a client's request body of either wire, opened by a relay:
// read what it asks for, then build the body the upstream gets, in one
// decode.
type Request struct {
	RequestInfo
	m map[string]json.RawMessage
}

// The errors of OpenRequest.
var (
	ErrNotObject = errors.New("llm: the request body is not a JSON object")
	// ErrBadStream: "stream" is neither true, false nor null. A relay that
	// read a string or a number as "not streaming" while its upstream read
	// it as streaming would meter nothing.
	ErrBadStream = errors.New(`llm: "stream" must be true or false`)
	ErrBadModel  = errors.New(`llm: "model" must be a string`)
)

// OpenRequest decodes body's top level. A member written twice keeps its
// last value, and Body writes each member once, so the upstream reads
// exactly what the relay read.
func OpenRequest(body []byte) (*Request, error) {
	r := &Request{}
	if err := json.Unmarshal(body, &r.m); err != nil {
		return nil, err
	}
	if r.m == nil {
		return nil, ErrNotObject
	}
	if raw, has := r.m["model"]; has && string(raw) != "null" {
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &r.Model) != nil {
			return nil, ErrBadModel
		}
	}
	switch string(r.m["stream"]) {
	case "", "null", "false":
	case "true":
		r.Stream = true
	default:
		return nil, ErrBadStream
	}
	return r, nil
}

// Member is the raw value of a top-level member (nil when absent).
func (r *Request) Member(name string) json.RawMessage { return r.m[name] }

// Body is the request for an upstream, no member the client sent dropped:
// model, when not "", replaces the body's; streamUsage makes a streamed
// Chat Completions call end with its usage chunk
// (stream_options.include_usage, other stream options kept) — pass it for
// PathChat only, the Responses wire reports usage on its own. It is always
// written anew from what OpenRequest read (members in sorted order, each
// once), never the client's own bytes.
func (r *Request) Body(model string, streamUsage bool) ([]byte, error) {
	m := make(map[string]json.RawMessage, len(r.m)+1)
	for k, v := range r.m {
		m[k] = v
	}
	if model != "" {
		m["model"], _ = json.Marshal(model)
	}
	if streamUsage && r.Stream {
		opts := map[string]json.RawMessage{}
		_ = json.Unmarshal(r.m["stream_options"], &opts)
		opts["include_usage"] = json.RawMessage("true")
		m["stream_options"], _ = json.Marshal(opts)
	}
	return json.Marshal(m)
}

// RewriteRequest is OpenRequest and Body in one call: the body to send,
// and what the client asked for (its own model name, whether it streams).
func RewriteRequest(body []byte, model string, streamUsage bool) ([]byte, RequestInfo, error) {
	r, err := OpenRequest(body)
	if err != nil {
		return nil, RequestInfo{}, err
	}
	out, err := r.Body(model, streamUsage)
	return out, r.RequestInfo, err
}
