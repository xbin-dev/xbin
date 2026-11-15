package llm

import (
	"maps"
	"slices"

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
	Source  string            `json:"source"` // admin | host
	Locked  bool              `json:"locked"`
	Reason  string            `json:"reason,omitempty"`
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
	// Concurrency is how many calls one caller may have in flight.
	Concurrency int `json:"concurrency"`
	// Locks maps a setting's path (enabled, people, upstreams.<name>,
	// defaults.model, defaults.roles.<role>, aliases.<alias>, concurrency,
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
		Prices: u.Prices, Source: "admin"}
	if v.Models == nil {
		v.Models = []string{}
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
		Quotas: snap.quotas.clone(), Locks: locksView(snap), Host: HostView{Active: snap.host, LockAll: snap.lockAll, Error: snap.hostErr},
		Vault: s.keys.state(), Problems: snap.problems(), Set: set, By: by, Concurrency: snap.concurrency}
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
	URL       string     `json:"url"`
	Ref       string     `json:"ref"`
	People    string     `json:"people"`
	Host      bool       `json:"host"` // the host's file sets (some of) it
	Defaults  Defaults   `json:"defaults"`
	User      string     `json:"user,omitempty"`
	Component string     `json:"component,omitempty"`
	Admin     bool       `json:"admin,omitempty"`
	Quota     *QuotaView `json:"quota,omitempty"`
}

// The connector's address under xbind and its binding ref (the broker's
// LLMRef, repeated here: the broker doesn't import this package).
const (
	BaseURL = "/api/xbin/llm/v1"
	Ref     = "xbin:llm"
)
