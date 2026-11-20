package llm

import (
	"maps"
	"slices"
	"time"

	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// view.go — what the management routes answer. No view carries a key: an
// admin-set key shows as a hint (its last four characters) while the vault
// is open, a host's key only as "set".

// Lock marks a setting the host's file sets.
type Lock struct {
	Locked bool   `json:"locked"`
	Reason string `json:"reason"`
}

// KeyView says whether an upstream has a key, never the key.
type KeyView struct {
	Set    bool   `json:"set"`
	Source string `json:"source,omitempty"` // vault (an admin's) | host (the host's file)
	Hint   string `json:"hint,omitempty"`   // "…abcd": an admin's key, while the vault is open
}

// UpstreamView is one upstream in force.
type UpstreamView struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	BaseURL string            `json:"baseURL"`
	Key     KeyView           `json:"key"`
	Models  []string          `json:"models"` // empty: the upstream's own list is passed through
	Private bool              `json:"private"`
	Prices  sdkllm.PriceTable `json:"prices,omitempty"`
	// ResponsesVia: how Responses calls are served — auto | upstream | chat
	// (D244); Translating: the upstream answered that it has no Responses
	// API, so they are translated to chat completions for now.
	ResponsesVia string `json:"responsesVia"`
	Translating  bool   `json:"translating,omitempty"`
	// TranslatingSince: when that was first found (RFC 3339); setting
	// responsesVia again forgets it.
	TranslatingSince string `json:"translatingSince,omitempty"`
	// Reasoning is the upstream's own description of its models' reasoning
	// (upstreams.<name>.reasoning, D249), keyed like prices. What a listed
	// model's record is — this, the upstream's own list, xbind's table — is
	// on each model of GET /v1/models (effort).
	Reasoning map[string]EffortRecord `json:"reasoning,omitempty"`
	// ReplayOff: its models found to refuse the reasoning a translated call
	// sends back (in memory until reset — POST …/replay-reset — or a restart).
	ReplayOff map[string]ReplayOff `json:"replayOff,omitempty"`
	Source    string               `json:"source"` // admin | host
	Locked    bool                 `json:"locked"`
	Reason    string               `json:"reason,omitempty"`
}

// HostView says whether a deployment file is in force.
type HostView struct {
	Active  bool   `json:"active"`
	LockAll bool   `json:"lockAll,omitempty"`
	Error   string `json:"error,omitempty"` // the latest reload was refused; the earlier file stays in force
}

// ConfigView is GET /llm/config: the configuration in force, the admin's
// and the host's merged, with what is locked.
type ConfigView struct {
	Configured bool              `json:"configured"`
	Enabled    bool              `json:"enabled"`
	People     string            `json:"people"`
	Upstreams  []UpstreamView    `json:"upstreams"`
	Defaults   Defaults          `json:"defaults"`
	Aliases    map[string]string `json:"aliases"`
	Quotas     QuotaRules        `json:"quotas"`
	// Sessions are the switches for coding tools in agent sessions and
	// terminals, as in force (sessions.go).
	Sessions sessionsInForce `json:"sessions"`
	// Concurrency is how many calls one caller may have in flight.
	Concurrency int `json:"concurrency"`
	// Locks maps a setting's path (enabled, people, upstreams.<name>,
	// defaults.model, defaults.roles.<role>, aliases.<alias>, concurrency,
	// sessions.agents|terminals|openaiEnv,
	// quotas.workspace|default|users|orgs; "*" when the host locks every
	// setting) to its lock.
	Locks    map[string]Lock `json:"locks"`
	Host     HostView        `json:"host"`
	Vault    string          `json:"vault"` // open | sealed | locked | plaintext
	Problems []string        `json:"problems"`
	// StoreError: the admin's own settings file can't be read (they are not
	// in force, and nothing is written over the file).
	StoreError string `json:"storeError,omitempty"`
	// Notices: what the write this view answers did beyond what was asked
	// (an upstream's key removed because its address changed).
	Notices []string `json:"notices,omitempty"`
	Set     int64    `json:"set,omitempty"`
	By      string   `json:"by,omitempty"`
	// ReasoningPresets are the descriptions an admin can give a model in one
	// pick (PUT /llm/upstreams/<name> {reasoning: {<model>: {preset}}}).
	ReasoningPresets []ReasoningPreset `json:"reasoningPresets"`
}

// ReasoningPreset is one of them: its name and what it says.
type ReasoningPreset struct {
	Name   string       `json:"name"`
	Effort EffortRecord `json:"effort"`
}

func reasoningPresets() []ReasoningPreset {
	out := make([]ReasoningPreset, 0, len(EffortPresetNames))
	for _, n := range EffortPresetNames {
		out = append(out, ReasoningPreset{Name: n, Effort: effortPresets[n].clone()})
	}
	return out
}

func lockOf() Lock { return Lock{Locked: true, Reason: LockReason} }

func locksView(snap *snapshot) map[string]Lock {
	out := map[string]Lock{}
	if snap.lockAll {
		out["*"] = lockOf()
	}
	for p := range snap.locks {
		out[p] = lockOf()
	}
	return out
}

func (s *Service) upstreamView(snap *snapshot, u effUpstream) UpstreamView {
	v := UpstreamView{Name: u.Name, Kind: KindOpenAI, BaseURL: u.BaseURL, Models: slices.Clone(u.Models), Private: u.Private,
		Prices: u.Prices, Source: "admin", ResponsesVia: u.ResponsesVia}
	for m, rec := range u.Reasoning {
		if v.Reasoning == nil {
			v.Reasoning = map[string]EffortRecord{}
		}
		rec = rec.clone()
		rec.Source, rec.Send = EffortFromAdmin, nil // a send patch is server-side configuration: not shown
		if u.Host {
			rec.Source = EffortFromHost
		}
		v.Reasoning[m] = rec
	}
	v.ReplayOff = s.noReplay.of(u.Name)
	if v.Models == nil {
		v.Models = []string{}
	}
	if v.ResponsesVia == "" {
		v.ResponsesVia = ViaAuto
		if at := s.wires.since(Target{Name: u.Name, BaseURL: u.BaseURL}, s.now()); !at.IsZero() {
			v.Translating, v.TranslatingSince = true, at.UTC().Format(time.RFC3339)
		}
	}
	switch {
	case u.Host:
		v.Source, v.Locked, v.Reason = "host", true, LockReason
		v.Key = KeyView{Set: u.hostKey != "", Source: "host"}
		if !v.Key.Set {
			v.Key.Source = ""
		}
	case u.HasKey:
		v.Key = KeyView{Set: true, Source: "vault"}
		if k, err := s.keys.get(u.Name); err == nil {
			v.Key.Hint = hint(k)
		}
	}
	return v
}

func (s *Service) configView() ConfigView {
	snap := s.current()
	s.mu.Lock()
	set, by, storeErr := s.store.Set, s.store.By, s.storeErr
	s.mu.Unlock()
	v := ConfigView{Configured: snap.configured(), Enabled: snap.enabled, People: snap.people, Upstreams: []UpstreamView{},
		Defaults: Defaults{Model: snap.defaults.Model, Roles: maps.Clone(snap.defaults.Roles)}, Aliases: maps.Clone(snap.aliases),
		Quotas: snap.quotas.clone(), Sessions: snap.sessions, Locks: locksView(snap), Host: HostView{Active: snap.host, LockAll: snap.lockAll, Error: snap.hostErr},
		Vault: s.keys.state(), Problems: snap.problems(), Set: set, By: by, Concurrency: snap.concurrency, ReasoningPresets: reasoningPresets()}
	if v.Problems == nil {
		v.Problems = []string{}
	}
	if storeErr != nil {
		v.StoreError = storeErr.Error()
	}
	for _, name := range snap.upstreamNames() {
		v.Upstreams = append(v.Upstreams, s.upstreamView(snap, snap.upstreams[name]))
	}
	return v
}

// QuotaView is a caller's quotas as they stand: their own limits and the
// workspace's, each with what is used and when it resets.
type QuotaView struct {
	User      string   `json:"user,omitempty"`
	Orgs      []string `json:"orgs,omitempty"`
	Limits    []Limit  `json:"limits"`
	Workspace []Limit  `json:"workspace"`
	State     string   `json:"state"` // ok | warn | stop: the worst of them
}

func (s *Service) quotaView(snap *snapshot, c Caller) QuotaView {
	person, workspace := s.limitsFor(snap, c)
	v := QuotaView{User: c.User, Limits: person, Workspace: workspace, State: "ok"}
	if s.opt.OrgsOf != nil && c.User != "" && c.User != OwnerUser {
		v.Orgs = s.opt.OrgsOf(c.User)
	}
	if v.Limits == nil {
		v.Limits = []Limit{}
	}
	if v.Workspace == nil {
		v.Workspace = []Limit{}
	}
	switch verdict := verdictOf(person, workspace); {
	case verdict.Stop != nil:
		v.State = ActionStop
	case verdict.Warn != nil:
		v.State = ActionWarn
	}
	return v
}

// StatusView is GET /llm: the connector as the caller meets it.
type StatusView struct {
	Configured bool `json:"configured"`
	Enabled    bool `json:"enabled"`
	// Callable: the caller's calls would be admitted. Reason and Code say
	// why not.
	Callable bool   `json:"callable"`
	Reason   string `json:"reason,omitempty"`
	Code     string `json:"code,omitempty"`
	// URL is the OpenAI-compatible base (an SDK's base_url, under the
	// workspace's address); Ref what a tile's llm slot binds to.
	URL      string   `json:"url"`
	Ref      string   `json:"ref"`
	People   string   `json:"people"`
	Host     bool     `json:"host"` // the host's file sets (some of) it
	Defaults Defaults `json:"defaults"`
	// Sessions: what coding tools in agent sessions and terminals get.
	Sessions  sessionsInForce `json:"sessions"`
	User      string          `json:"user,omitempty"`
	Component string          `json:"component,omitempty"`
	Admin     bool            `json:"admin,omitempty"`
	Quota     *QuotaView      `json:"quota,omitempty"`
}

// The connector's address under xbind and its binding ref (the broker's
// LLMRef, repeated here: the broker doesn't import this package).
const (
	BaseURL = "/api/xbin/llm/v1"
	Ref     = "xbin:llm"
)
