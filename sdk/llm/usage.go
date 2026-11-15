package llm

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
)

// Usage is one call's token usage, the same shape for both wires.
// InputTokens counts the whole prompt, the part read from a prompt cache
// (CachedTokens) included; OutputTokens the whole completion, the part
// spent reasoning (ReasoningTokens) included — as the providers bill them.
type Usage struct {
	InputTokens     int64 `json:"inputTokens"`
	OutputTokens    int64 `json:"outputTokens"`
	CachedTokens    int64 `json:"cachedTokens,omitempty"`
	ReasoningTokens int64 `json:"reasoningTokens,omitempty"`
	TotalTokens     int64 `json:"totalTokens"`
	// Cost is what the provider itself says the call cost, in USD, when it
	// says (OpenRouter's usage.cost); nil otherwise. A price table
	// (PriceTable.Cost) is the other source.
	Cost *float64 `json:"cost,omitempty"`
}

// Zero reports a usage that counted nothing.
func (u Usage) Zero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0
}

// Add returns u plus v (costs add when either side has one).
func (u Usage) Add(v Usage) Usage {
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
	u.CachedTokens += v.CachedTokens
	u.ReasoningTokens += v.ReasoningTokens
	u.TotalTokens += v.TotalTokens
	if u.Cost != nil || v.Cost != nil {
		c := 0.0
		if u.Cost != nil {
			c += *u.Cost
		}
		if v.Cost != nil {
			c += *v.Cost
		}
		u.Cost = &c
	}
	return u
}

// InputDetails is the part of a usage block that breaks the input down
// (prompt_tokens_details on the chat wire, input_tokens_details on the
// Responses wire).
type InputDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

// OutputDetails breaks the output down (completion_tokens_details,
// output_tokens_details).
type OutputDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// ChatUsage is a Chat Completions usage block.
type ChatUsage struct {
	PromptTokens            int64          `json:"prompt_tokens"`
	CompletionTokens        int64          `json:"completion_tokens"`
	TotalTokens             int64          `json:"total_tokens"`
	PromptTokensDetails     *InputDetails  `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *OutputDetails `json:"completion_tokens_details,omitempty"`
	// PromptCacheHitTokens is DeepSeek's name for the cached part.
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens,omitempty"`
	// Cost is OpenRouter's (USD), when usage accounting is on.
	Cost *float64 `json:"cost,omitempty"`
}

// Usage is u in the shared shape.
func (u ChatUsage) Usage() Usage {
	out := Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens, Cost: u.Cost}
	if u.PromptTokensDetails != nil {
		out.CachedTokens = u.PromptTokensDetails.CachedTokens
	}
	if out.CachedTokens == 0 {
		out.CachedTokens = u.PromptCacheHitTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out.fixed()
}

// ResponsesUsage is a Responses API usage block.
type ResponsesUsage struct {
	InputTokens         int64          `json:"input_tokens"`
	OutputTokens        int64          `json:"output_tokens"`
	TotalTokens         int64          `json:"total_tokens"`
	InputTokensDetails  *InputDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *OutputDetails `json:"output_tokens_details,omitempty"`
}

// Usage is u in the shared shape.
func (u ResponsesUsage) Usage() Usage {
	out := Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
	if u.InputTokensDetails != nil {
		out.CachedTokens = u.InputTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return out.fixed()
}

// fixed fills a total the provider left out and keeps the parts within
// their wholes (a provider's odd numbers never make a negative price).
func (u Usage) fixed() Usage {
	u.InputTokens, u.OutputTokens = max(u.InputTokens, 0), max(u.OutputTokens, 0)
	if u.TotalTokens < u.InputTokens+u.OutputTokens {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	u.CachedTokens = min(max(u.CachedTokens, 0), u.InputTokens)
	u.ReasoningTokens = min(max(u.ReasoningTokens, 0), u.OutputTokens)
	return u
}

// anyUsage reads a usage block of either wire: the Responses names win
// when both are there (no provider sends both).
type anyUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
}

// usageOf decodes one usage block (the value of a "usage" key).
func usageOf(raw json.RawMessage) (Usage, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return Usage{}, false
	}
	var a anyUsage
	if json.Unmarshal(raw, &a) != nil {
		return Usage{}, false
	}
	switch {
	case a.InputTokens != nil || a.OutputTokens != nil:
		var u ResponsesUsage
		if json.Unmarshal(raw, &u) != nil {
			return Usage{}, false
		}
		return u.Usage(), true
	case a.PromptTokens != nil || a.CompletionTokens != nil:
		var u ChatUsage
		if json.Unmarshal(raw, &u) != nil {
			return Usage{}, false
		}
		return u.Usage(), true
	}
	return Usage{}, false
}

// UsageFromBody reads the usage of a non-streaming response body of either
// wire, or of one stream event's data: a top-level "usage" (a chat
// completion, a chat chunk, a response), else "response.usage" (a
// response.completed event). ok is false when it carries none.
func UsageFromBody(body []byte) (Usage, bool) {
	if !hasUsageObject(body) {
		return Usage{}, false
	}
	var v struct {
		Usage    json.RawMessage `json:"usage"`
		Response *struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &v) != nil {
		return Usage{}, false
	}
	if u, ok := usageOf(v.Usage); ok {
		return u, true
	}
	if v.Response != nil {
		return usageOf(v.Response.Usage)
	}
	return Usage{}, false
}

var usageKey = []byte(`"usage"`)

// hasUsageObject reports a "usage" key whose value is an object, without
// decoding: most stream chunks carry none, or "usage": null.
func hasUsageObject(b []byte) bool {
	for {
		i := bytes.Index(b, usageKey)
		if i < 0 {
			return false
		}
		b = b[i+len(usageKey):]
		for j := 0; j < len(b); j++ {
			switch b[j] {
			case ' ', '\t', '\r', '\n', ':':
				continue
			case '{':
				return true
			}
			break
		}
	}
}

// The tail of a response too large to hold whole still ends in its usage
// block. The LAST match wins: a Responses stream may mention usage as null
// before its final event, and its final event carries the block near its
// end.
var (
	tailChatRE      = regexp.MustCompile(`"prompt_tokens"\s*:\s*(\d+)[\s\S]*?"completion_tokens"\s*:\s*(\d+)`)
	tailResponsesRE = regexp.MustCompile(`"input_tokens"\s*:\s*(\d+)[\s\S]*?"output_tokens"\s*:\s*(\d+)`)
	tailCachedRE    = regexp.MustCompile(`"cached_tokens"\s*:\s*(\d+)`)
	tailReasoningRE = regexp.MustCompile(`"reasoning_tokens"\s*:\s*(\d+)`)
)

// UsageFromTail scrapes usage from the last bytes of a response that was
// not kept whole. It is the fallback: no JSON is decoded, so only the
// counts are read.
func UsageFromTail(tail []byte) (Usage, bool) {
	for _, re := range []*regexp.Regexp{tailResponsesRE, tailChatRE} {
		all := re.FindAllSubmatchIndex(tail, -1)
		if len(all) == 0 {
			continue
		}
		m := all[len(all)-1]
		u := Usage{InputTokens: atoi(tail[m[2]:m[3]]), OutputTokens: atoi(tail[m[4]:m[5]])}
		rest := tail[m[0]:]
		if c := tailCachedRE.FindSubmatch(rest); c != nil {
			u.CachedTokens = atoi(c[1])
		}
		if r := tailReasoningRE.FindSubmatch(rest); r != nil {
			u.ReasoningTokens = atoi(r[1])
		}
		return u.fixed(), true
	}
	return Usage{}, false
}

func atoi(b []byte) int64 {
	n, _ := strconv.ParseInt(string(b), 10, 64)
	return n
}
