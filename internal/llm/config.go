package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/fsutil"
	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

// Settings is what one source says about the connector: the admin's store
// (data/llm/config.json) or the host's deployment file (XBIN_LLM_CONFIG).
// Everything is optional; a source that leaves a setting out says nothing
// about it.
type Settings struct {
	// Enabled is the master switch (default on once an upstream exists).
	Enabled *bool `json:"enabled,omitempty"`
	// People says which signed-in people may call the connector directly
	// (a browser session, a device, bx): admins (the default), everyone or
	// nobody. Components are admitted by their binding, never by this.
	People    string              `json:"people,omitempty"`
	Upstreams map[string]Upstream `json:"upstreams,omitempty"`
	Defaults  Defaults            `json:"defaults"`
	// Aliases maps a model name clients send to a model of an upstream
	// ("<upstream>/<model>"); the alias "*" catches every unknown name.
	Aliases map[string]string `json:"aliases,omitempty"`
	Quotas  QuotaRules        `json:"quotas"`
	// Sessions are the switches for coding tools in terminals and agent
	// sessions (sessions.go).
	Sessions Sessions `json:"sessions"`
	// Concurrency is how many calls one caller (a person; a component, for
	// calls nobody acts in) may have in flight; 0 is DefaultConcurrency.
	Concurrency int `json:"concurrency,omitempty"`
}

// DefaultConcurrency and MaxConcurrency bound Settings.Concurrency.
const (
	DefaultConcurrency = 16
	MaxConcurrency     = 1024
)

// Defaults are the workspace's default models: Model for a call that names
// none (and for xbin/default), Roles for the named jobs — "agent" (the
// workspace agent), "harness" (coding harnesses), any other an admin adds —
// each asked for as xbin/<role> and falling back to Model.
type Defaults struct {
	Model string            `json:"model,omitempty"`
	Roles map[string]string `json:"roles,omitempty"`
}

// Upstream is one place models are served from.
type Upstream struct {
	Kind    string `json:"kind,omitempty"` // "openai": an OpenAI-compatible API (the only kind)
	BaseURL string `json:"baseURL"`
	// HasKey (the admin's store): the upstream's key is kept in xbind's
	// vault. KeyFile (the host's file): the file the key is read from.
	HasKey  bool   `json:"hasKey,omitempty"`
	KeyFile string `json:"keyFile,omitempty"`
	// Models are the models offered through this upstream; empty passes the
	// upstream's own /models list (and any model name) through.
	Models []string `json:"models,omitempty"`
	// Private marks an upstream that keeps prompts inside the organisation;
	// org constraints will filter on it (the CallPolicy hook).
	Private bool              `json:"private,omitempty"`
	Prices  sdkllm.PriceTable `json:"prices,omitempty"`
}

// The people switch.
const (
	PeopleAdmins   = "admins"
	PeopleEveryone = "everyone"
	PeopleNobody   = "nobody"
)

// KindOpenAI is the one upstream kind: an OpenAI-compatible API.
const KindOpenAI = "openai"

// RoleDefault names the general default among the xbin/<role> models.
const RoleDefault = "default"

// pseudoUpstream is the reserved prefix of the role models (xbin/default,
// xbin/agent, …): no upstream may take the name.
const pseudoUpstream = "xbin"

// AliasAny is the alias that catches every model name nothing else resolves.
const AliasAny = "*"

var (
	upstreamNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)
	roleNameRE     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

const maxModelName = 200

func validModelName(what, m string) error {
	if m == "" || len(m) > maxModelName || strings.ContainsAny(m, " \t\r\n") {
		return fmt.Errorf("%s: a model name is 1–%d characters without spaces", what, maxModelName)
	}
	return nil
}

// validUpstream checks one upstream as either source writes it.
func validUpstream(name string, u Upstream) error {
	if !upstreamNameRE.MatchString(name) || name == pseudoUpstream {
		return fmt.Errorf("upstream name %q: lowercase letters, digits, . _ - (at most 40), and not %q", name, pseudoUpstream)
	}
	if u.Kind != "" && u.Kind != KindOpenAI {
		return fmt.Errorf("upstream %s: kind %q — the one kind is %q (an OpenAI-compatible API)", name, u.Kind, KindOpenAI)
	}
	if err := validBaseURL(u.BaseURL); err != nil {
		return fmt.Errorf("upstream %s: %w", name, err)
	}
	seen := map[string]bool{}
	for _, m := range u.Models {
		if err := validModelName("upstream "+name+" models", m); err != nil {
			return err
		}
		if seen[m] {
			return fmt.Errorf("upstream %s: model %q is listed twice", name, m)
		}
		seen[m] = true
	}
	for m, p := range u.Prices {
		if m == "" || !p.Valid() {
			return fmt.Errorf("upstream %s: the price of %q: USD per million tokens, never negative", name, m)
		}
	}
	return nil
}

// validBaseURL: http or https with a host, and nothing a request could
// carry a secret or a redirect in.
func validBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("baseURL: want http(s)://host[:port][/path] (for example https://api.openai.com/v1), got %q", raw)
	}
	return nil
}

// validate checks a source as a whole (the parts shared by both sources).
func (s Settings) validate() error {
	switch s.People {
	case "", PeopleAdmins, PeopleEveryone, PeopleNobody:
	default:
		return fmt.Errorf("people: %q — admins, everyone or nobody", s.People)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Upstreams)) {
		if err := validUpstream(name, s.Upstreams[name]); err != nil {
			return err
		}
	}
	if s.Defaults.Model != "" {
		if err := validModelName("defaults.model", s.Defaults.Model); err != nil {
			return err
		}
	}
	for role, m := range s.Defaults.Roles {
		if !roleNameRE.MatchString(role) || role == RoleDefault {
			return fmt.Errorf("defaults.roles: role %q — a lowercase name (letters, digits, -), and not %q (that is defaults.model)", role, RoleDefault)
		}
		if err := validModelName("defaults.roles."+role, m); err != nil {
			return err
		}
	}
	for a, target := range s.Aliases {
		if err := validAlias(a, target); err != nil {
			return err
		}
		if up := aliasUpstream(a, s.Upstreams); up != "" {
			return shadowErr(a, up)
		}
	}
	if s.Concurrency < 0 || s.Concurrency > MaxConcurrency {
		return fmt.Errorf("concurrency: 1–%d calls in flight per caller (0: the default, %d)", MaxConcurrency, DefaultConcurrency)
	}
	return s.Quotas.validate()
}

// aliasUpstream is the upstream an alias's name would shadow: an alias
// named "<upstream>/…" reads as a model of that upstream, and an explicit
// model name is never an alias's to redirect.
func aliasUpstream[U any](alias string, upstreams map[string]U) string {
	if up, rest, ok := strings.Cut(alias, "/"); ok && rest != "" {
		if _, has := upstreams[up]; has {
			return up
		}
	}
	return ""
}

func shadowErr(alias, upstream string) error {
	return fmt.Errorf("alias %q: names under %s/ are the upstream %s's models — an alias can't stand in for one", alias, upstream, upstream)
}

func validAlias(alias, target string) error {
	if alias != AliasAny {
		if err := validModelName("alias "+alias, alias); err != nil {
			return err
		}
	}
	if strings.HasPrefix(alias, pseudoUpstream+"/") {
		return fmt.Errorf("alias %q: names under %s/ are the default models' (set them with defaults)", alias, pseudoUpstream)
	}
	return validModelName("alias "+alias+"'s target", target)
}

// ---- the admin's store ------------------------------------------------------

// storeSchema is data/llm/config.json's schema.
const storeSchema = 1

// storeFile is data/llm/config.json: the settings an admin made, with who
// last changed them. Upstream keys are not here: they are in xbind's vault
// (keys.go), and an upstream only says it has one (hasKey).
type storeFile struct {
	Schema int `json:"schema"`
	Settings
	Set int64  `json:"set,omitempty"` // unix: the last change
	By  string `json:"by,omitempty"`
}

// errStoreNewer: a store written by a newer xbind is read by nobody and
// never written over.
var errStoreNewer = errors.New("data/llm/config.json was written by a newer xbind: the LLM connector's admin settings are not read (and not overwritten) by this one")

func loadStore(dir string) (storeFile, error) {
	f := storeFile{Schema: storeSchema}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("data/llm/config.json: %w", err)
	}
	if f.Schema > storeSchema {
		return f, errStoreNewer
	}
	f.Schema = storeSchema
	return f, nil
}

func saveStore(dir string, f storeFile) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, "config.json"), append(b, '\n'), 0o600)
}

// clone copies s deeply enough that a change to the copy never reaches s.
func (s Settings) clone() Settings {
	out := s
	if s.Enabled != nil {
		v := *s.Enabled
		out.Enabled = &v
	}
	out.Upstreams = make(map[string]Upstream, len(s.Upstreams))
	for k, u := range s.Upstreams {
		u.Models = slices.Clone(u.Models)
		u.Prices = cloneMap(u.Prices)
		out.Upstreams[k] = u
	}
	out.Defaults.Roles = cloneMap(s.Defaults.Roles)
	out.Aliases = cloneMap(s.Aliases)
	out.Quotas = s.Quotas.clone()
	return out
}

func cloneMap[V any](m map[string]V) map[string]V {
	if m == nil {
		return nil
	}
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---- the two sources, merged ------------------------------------------------

// LockReason is why a locked setting can't be changed through the API.
const LockReason = "set by your host"

// effUpstream is an upstream in force: the admin's, or the host's (whose
// key was read from its file).
type effUpstream struct {
	Upstream
	Name    string
	Host    bool
	hostKey string // the host file's key ("" = none, or the admin's in the vault)
}

// snapshot is the configuration in force: the admin's settings with the
// host's laid over them. Whatever the host's file sets is locked.
type snapshot struct {
	enabled   bool
	people    string
	upstreams map[string]effUpstream
	defaults  Defaults
	aliases   map[string]string
	quotas    QuotaRules
	sessions  sessionsInForce // the switches for coding tools (sessions.go)
	// concurrency is the per-caller cap in force (never 0).
	concurrency int

	locks   map[string]bool // setting path → set by the host
	lockAll bool            // the host's file locks every setting
	host    bool            // a host file is in force
	hostErr string          // why the host file's last reload was refused (the earlier one stays in force)
}

// locked reports whether the setting at path is the host's.
func (s *snapshot) locked(path string) bool { return s.lockAll || s.locks[path] }

// configured: at least one upstream exists.
func (s *snapshot) configured() bool { return len(s.upstreams) > 0 }

// upstreamNames are the upstreams in force, sorted.
func (s *snapshot) upstreamNames() []string { return slices.Sorted(maps.Keys(s.upstreams)) }

// merge lays the host's settings over the admin's.
func merge(admin Settings, host *hostConfig) *snapshot {
	if host != nil && host.LockAll {
		admin = Settings{} // every setting is the host's: the admin's are not in force
	}
	s := &snapshot{enabled: true, people: PeopleAdmins, upstreams: map[string]effUpstream{}, aliases: map[string]string{},
		locks: map[string]bool{}, defaults: Defaults{Roles: map[string]string{}},
		sessions: sessionsInForce{Agents: true, Terminals: true}}
	s.sessions.lay(admin.Sessions, func(string) {})
	if admin.Enabled != nil {
		s.enabled = *admin.Enabled
	}
	if admin.People != "" {
		s.people = admin.People
	}
	for name, u := range admin.Upstreams {
		s.upstreams[name] = effUpstream{Upstream: u, Name: name}
	}
	s.defaults.Model = admin.Defaults.Model
	for r, m := range admin.Defaults.Roles {
		s.defaults.Roles[r] = m
	}
	for a, t := range admin.Aliases {
		s.aliases[a] = t
	}
	s.quotas = admin.Quotas.clone()
	s.concurrency = DefaultConcurrency
	if admin.Concurrency > 0 {
		s.concurrency = admin.Concurrency
	}
	if host == nil {
		return s
	}
	h := host.Settings
	s.host, s.lockAll = true, host.LockAll
	if h.Enabled != nil {
		s.enabled, s.locks["enabled"] = *h.Enabled, true
	}
	if h.People != "" {
		s.people, s.locks["people"] = h.People, true
	}
	for name, u := range h.Upstreams {
		s.upstreams[name] = effUpstream{Upstream: u, Name: name, Host: true, hostKey: host.keys[name]}
		s.locks["upstreams."+name] = true
	}
	if h.Defaults.Model != "" {
		s.defaults.Model, s.locks["defaults.model"] = h.Defaults.Model, true
	}
	for r, m := range h.Defaults.Roles {
		s.defaults.Roles[r], s.locks["defaults.roles."+r] = m, true
	}
	for a, t := range h.Aliases {
		s.aliases[a], s.locks["aliases."+a] = t, true
	}
	s.sessions.lay(h.Sessions, func(path string) { s.locks[path] = true })
	if h.Quotas.Workspace != nil {
		s.quotas.Workspace, s.locks["quotas.workspace"] = h.Quotas.Workspace, true
	}
	if h.Quotas.Default != nil {
		// the host's default is a ceiling: an admin's own per-user and
		// per-org entries narrow it, never widen it (quota.go)
		s.quotas.Default, s.locks["quotas.default"] = h.Quotas.Default, true
		s.quotas.ceiling = h.Quotas.Default
	}
	if h.Quotas.Users != nil {
		s.quotas.Users, s.locks["quotas.users"] = cloneMap(h.Quotas.Users), true
		s.quotas.hostUsers = true
	}
	if h.Quotas.Orgs != nil {
		s.quotas.Orgs, s.locks["quotas.orgs"] = cloneMap(h.Quotas.Orgs), true
		s.quotas.hostOrgs = true
	}
	if h.Concurrency > 0 {
		s.concurrency, s.locks["concurrency"] = h.Concurrency, true
	}
	return s
}

// problems lists what in the configuration can't work as written, for the
// admin's views: a default or an alias naming no upstream in force.
func (s *snapshot) problems() []string {
	var out []string
	check := func(what, target string) {
		if target == "" {
			return
		}
		if _, _, err := s.resolve(target); err != nil {
			out = append(out, what+": "+err.Error())
		}
	}
	if !s.configured() {
		return nil
	}
	check("the default model", s.defaults.Model)
	for _, r := range slices.Sorted(maps.Keys(s.defaults.Roles)) {
		check("the "+r+" model", s.defaults.Roles[r])
	}
	for _, a := range slices.Sorted(maps.Keys(s.aliases)) {
		if up := aliasUpstream(a, s.upstreams); up != "" {
			out = append(out, "alias "+a+": never used — names under "+up+"/ are the upstream's own models")
			continue
		}
		if a != AliasAny && len(s.offering(a)) > 0 {
			out = append(out, "alias "+a+": never used — an upstream lists a model of that name")
			continue
		}
		check("alias "+a, s.aliases[a])
	}
	return out
}
