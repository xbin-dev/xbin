package llm

import "strings"

// Price is a model's cost in USD per 1,000,000 tokens. CachedIn prices the
// part of the input read from a prompt cache; when it is nil those tokens
// cost In. Reasoning tokens are output tokens and cost Out.
type Price struct {
	In       float64  `json:"in"`
	Out      float64  `json:"out"`
	CachedIn *float64 `json:"cachedIn,omitempty"`
}

// Valid reports a price no part of which is negative.
func (p Price) Valid() bool {
	return p.In >= 0 && p.Out >= 0 && (p.CachedIn == nil || *p.CachedIn >= 0)
}

// Cost is what u costs at p, in USD.
func (p Price) Cost(u Usage) float64 {
	cached := min(max(u.CachedTokens, 0), max(u.InputTokens, 0))
	cachedPrice := p.In
	if p.CachedIn != nil {
		cachedPrice = *p.CachedIn
	}
	return (float64(max(u.InputTokens, 0)-cached)*p.In + float64(cached)*cachedPrice + float64(max(u.OutputTokens, 0))*p.Out) / 1e6
}

// PriceTable maps a model id to its price.
type PriceTable map[string]Price

// Lookup finds model's price: its exact id, else the id after its last "/"
// (a price for "gpt-5" covers an aggregator's "openai/gpt-5"), else the
// longest entry ending in "*" that prefixes it ("gpt-5*").
func (t PriceTable) Lookup(model string) (Price, bool) {
	if p, ok := t[model]; ok {
		return p, true
	}
	bare := model
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		bare = model[i+1:]
		if p, ok := t[bare]; ok {
			return p, true
		}
	}
	bestKey, bestLen := "", -1
	for k := range t {
		pre, wild := strings.CutSuffix(k, "*")
		if !wild || !(strings.HasPrefix(model, pre) || strings.HasPrefix(bare, pre)) {
			continue
		}
		if len(pre) > bestLen || (len(pre) == bestLen && k < bestKey) {
			bestKey, bestLen = k, len(pre)
		}
	}
	if bestLen >= 0 {
		return t[bestKey], true
	}
	return Price{}, false
}

// Cost is what u costs for model, in USD; ok is false when the table has
// no price for it (the cost is unknown, not zero).
func (t PriceTable) Cost(model string, u Usage) (cost float64, ok bool) {
	p, ok := t.Lookup(model)
	if !ok {
		return 0, false
	}
	return p.Cost(u), true
}
