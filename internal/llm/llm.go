// Package llm is xbind's built-in LLM connector (D226, docs/llm.md): an
// OpenAI-compatible endpoint under /api/xbin/llm/ in front of the
// workspace's model providers, so the workspace's agents, tiles and coding
// CLIs share one LLM setup, metered per person.
//
//   - Configuration has two sources: what an admin sets (data/llm/config.json;
//     upstream keys in xbind's vault, keys.go) and the host's deployment file
//     (XBIN_LLM_CONFIG, host.go), whose settings are locked (config.go).
//   - Callers are a signed-in person (admitted by the workspace's people
//     switch), a component whose llm slot is bound to the connector
//     ("xbin:llm", the broker's builtin_http.go), or the holder of an LLM
//     key (tokens.go). Every call is attributed to the acting person and the
//     component (caller.go).
//   - Usage is metered per person, component and model (meter.go) and held
//     to quotas (quota.go).
//   - The data plane sits behind one interface, Connector (plane.go): the
//     in-daemon streaming proxy today.
//
// It interprets no model output, logs no request or response body and no
// key, and only ever calls URLs that configuration names.
package llm

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Options wires a Service into the daemon.
type Options struct {
	// Dir is data/llm: config.json, keys.json, usage/.
	Dir string
	// HostFile is XBIN_LLM_CONFIG ("" = none).
	HostFile string
	// Vault is the vault barrier upstream keys are sealed under; nil with
	// AllowPlaintext is the plaintext vault mode's shape.
	Vault Vault
	// AllowPlaintext: keys may be kept in the clear when no barrier exists
	// (--insecure-vault, --no-auth).
	AllowPlaintext bool
	// IsAdmin reports a workspace admin (the broker's: the owner, an admin's
	// sign-in, the admin console). nil = nobody.
	IsAdmin func(auth.Principal) bool
	// AdminShell reports an admin's shell: a trusted chrome frame minted
	// under a workspace admin's own login (the broker's AdminShell). It may
	// dismiss the setup alert it is shown (alerts.go), and nothing else.
	// nil = none.
	AdminShell func(auth.Principal) bool
	// Bound reports a component whose llm slot is bound to the connector
	// (the broker's LLMBound). nil = none.
	Bound func(component string) bool
	// Edge says how a bound tile's principal may use its binding: EdgeFull
	// for the tile's primary, EdgeRead or EdgeBlock (with the refusal's
	// text) for a non-primary deployment, by the tile's edge policy (the
	// broker's LLMEdge). nil = every bound principal is full.
	Edge func(p auth.Principal) (mode, why string)
	// KeyOwner reports whether the person an LLM key is metered to still
	// holds what it was minted under: they exist, are enabled, and (when
	// component is not "") can still open that tile. nil = not asked.
	KeyOwner func(user, component string) bool
	// OrgsOf lists the orgs a person is an active member of (for the orgs'
	// quota overrides). nil = none.
	OrgsOf func(user string) []string
	// Policy is asked before every call (nil: every call passes) — where
	// org constraints will filter by upstream and model.
	Policy CallPolicy
	// Plane is the data plane (nil: the in-daemon Proxy).
	Plane Connector
	// Grace is how long the in-daemon Proxy keeps an upstream call whose
	// client went away, to read its usage (0: DefaultGrace).
	Grace time.Duration
	// Keys holds the LLM keys (nil: a store of its own, reached with Keys()).
	Keys KeySource
	Now  func() time.Time
	Log  *slog.Logger
}

// CallPolicy judges one call before it runs: an error refuses it, with that
// text. It is the seam for org constraints (plans/workspace-agent.md §5.6:
// "internal-data org only allows private LLMs"); nothing installs one yet.
type CallPolicy func(c Caller, u UpstreamInfo, model string) error

// UpstreamInfo is what a policy may know of an upstream.
type UpstreamInfo struct {
	Name    string
	Private bool
	Host    bool // set by the host's file
}

// Service is the connector.
type Service struct {
	opt   Options
	log   *slog.Logger
	plane Connector
	keys  *keyStore
	meter *meter
	host  *hostWatch
	// llmKeys is the store this Service made (nil when Options.Keys is
	// someone else's).
	llmKeys   *Keys
	keySource KeySource
	stopSweep chan struct{} // ends the key store's sweeper (Close)
	closeOnce sync.Once
	watch     keyWatch // minters told of their keys' refused calls (keywatch.go)
	models    modelCache

	flightMu sync.Mutex
	flight   map[string]int // caller → its calls in flight (serve.go)

	mu       sync.Mutex // serializes changes to the admin's store
	store    storeFile
	storeErr error // the store can't be read: nothing is written over it
	snap     atomic.Pointer[snapshot]
}

// New loads the connector's state. A host file that can't be read, or is
// invalid, is an error: xbind doesn't start without the limits its host
// wrote. An unreadable admin store is not — the connector then runs on the
// host's settings alone (or reads "not configured"), says so to admins, and
// leaves the file as it is.
func New(o Options) (*Service, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	s := &Service{opt: o, log: o.Log, plane: o.Plane, keySource: o.Keys,
		keys:  &keyStore{path: filepath.Join(o.Dir, "keys.json"), vault: o.Vault, allowPlain: o.AllowPlaintext},
		meter: newMeter(filepath.Join(o.Dir, "usage"), o.Log)}
	if s.plane == nil {
		p := NewProxy(o.Log)
		if o.Grace > 0 {
			p.Grace = o.Grace
		}
		s.plane = p
	}
	if s.keySource == nil {
		s.llmKeys = NewKeys(o.Now)
		s.keySource = s.llmKeys
		s.stopSweep = make(chan struct{})
		go s.llmKeys.Sweeper(s.stopSweep) // expired keys go on a timer, not on a call's path
	}
	if o.HostFile != "" {
		h, err := loadHost(o.HostFile)
		if err != nil {
			return nil, err
		}
		s.host = &hostWatch{path: o.HostFile, cur: h, checked: o.Now()}
		o.Log.Info("llm: the host's configuration is in force (its settings are locked)", "file", o.HostFile,
			"upstreams", len(h.Upstreams), "lockAll", h.LockAll)
	}
	s.store, s.storeErr = loadStore(o.Dir)
	if s.storeErr != nil {
		o.Log.Error("llm: the admin's settings can't be read; the connector runs without them", "err", s.storeErr)
		s.store = storeFile{Schema: storeSchema}
	}
	s.rebuild()
	return s, nil
}

// Close flushes the usage counters and ends every LLM key: the sessions
// they were minted for end with xbind.
func (s *Service) Close() {
	if s.llmKeys != nil {
		s.closeOnce.Do(func() { close(s.stopSweep) })
		s.llmKeys.RevokeWhere(func(KeyGrant) bool { return true })
	}
	s.meter.close()
}

// Keys is the store of LLM keys, for the packages that mint them (nil when
// Options.Keys supplied another source).
func (s *Service) Keys() *Keys { return s.llmKeys }

func (s *Service) now() time.Time { return s.opt.Now() }

// rebuild computes the configuration in force from the two sources.
func (s *Service) rebuild() {
	h, herr := s.host.current()
	snap := merge(s.store.Settings, h)
	if herr != "" {
		snap.hostErr = "the host's LLM configuration changed and could not be read; the earlier one stays in force (see xbind's log)"
	}
	s.snap.Store(snap)
	s.models.drop()
}

// current is the configuration in force, the host's file looked at again
// when it is due.
func (s *Service) current() *snapshot {
	if s.host.poll(s.now()) {
		s.mu.Lock()
		if _, herr := s.host.current(); herr != "" {
			s.log.Error("llm: the host's configuration was not reloaded; the earlier one stays in force", "err", herr)
		} else {
			s.log.Info("llm: the host's configuration was reloaded")
		}
		s.rebuild()
		s.mu.Unlock()
	}
	return s.snap.Load()
}

// Configured reports an upstream in force: what makes pickers offer the
// connector (the broker's LLMOffered).
func (s *Service) Configured() bool { return s.current().configured() }

// errNoKey: the upstream says it has a key and the vault holds none.
var errNoKey = errors.New("its key is missing from the vault (an admin sets it again: bx llm upstream add)")

// target is u as the plane calls it, its key read from where it lives: the
// host's file (already loaded) or the vault (ErrVaultSealed while sealed).
func (s *Service) target(u effUpstream) (Target, error) {
	t := Target{Name: u.Name, Kind: KindOpenAI, BaseURL: u.BaseURL}
	switch {
	case u.Host:
		t.Key = u.hostKey
	case u.HasKey:
		k, err := s.keys.get(u.Name)
		if err != nil {
			return t, err
		}
		if k == "" {
			return t, errNoKey
		}
		t.Key = k
	}
	return t, nil
}

func (s *Service) refusedByPolicy(c Caller, u effUpstream, model string) bool {
	return s.opt.Policy != nil && s.opt.Policy(c, UpstreamInfo{Name: u.Name, Private: u.Private, Host: u.Host}, model) != nil
}

// TestResult is the answer of an upstream test.
type TestResult struct {
	OK     bool   `json:"ok"`
	Model  string `json:"model,omitempty"`  // the model a call was made with
	Models int    `json:"models,omitempty"` // how many models the upstream lists (a test without a model)
	Status int    `json:"status,omitempty"`
	Ms     int64  `json:"ms"`
	Error  string `json:"error,omitempty"`
	Tokens int64  `json:"tokens,omitempty"` // what the test call used
}

// Test makes a small call through one upstream: a one-token chat
// completion with model, or — for an upstream that passes names through
// and no model given — its model list. It is a call like any other: it
// needs the connector on, passes the policy, takes one of who's slots,
// stops at who's quotas (and the workspace's), and is metered to who — so
// an admin's test can't spend what the host's settings hold back.
func (s *Service) Test(ctx context.Context, name, model string, who Caller) (TestResult, error) {
	snap := s.current()
	u, ok := snap.upstreams[name]
	if !ok {
		return TestResult{}, errNoUpstream(name)
	}
	if !snap.enabled {
		why := "the LLM connector is off: turn it on to test an upstream (bx llm on)"
		if snap.locked("enabled") {
			why = "the LLM connector is turned off by your host: its upstreams can't be tested"
		}
		return TestResult{}, &apiError{status: http.StatusConflict, msg: why}
	}
	if model != "" && validModelName("model", model) != nil {
		return TestResult{}, badRequest("model: a model name is 1–" + strconv.Itoa(maxModelName) + " characters without spaces")
	}
	if model == "" && len(u.Models) > 0 {
		model = u.Models[0]
	}
	if model != "" && len(u.Models) > 0 && !slices.Contains(u.Models, model) {
		return TestResult{}, badRequest("the upstream " + name + " doesn't offer " + model + " (it offers: " + strings.Join(u.Models, ", ") + ")")
	}
	if s.opt.Policy != nil {
		if err := s.opt.Policy(who, UpstreamInfo{Name: u.Name, Private: u.Private, Host: u.Host}, model); err != nil {
			return TestResult{}, &apiError{status: http.StatusForbidden, msg: err.Error()}
		}
	}
	release, ref := s.enter(snap, who)
	if ref == nil {
		defer release()
		ref = s.checkQuota(snap, who, http.Header{})
	}
	if ref != nil {
		return TestResult{}, &apiError{status: ref.status, msg: ref.err.Message}
	}
	start := s.now()
	res := TestResult{Model: model}
	done := func() (TestResult, error) {
		res.Ms = s.now().Sub(start).Milliseconds()
		return res, nil
	}
	t, err := s.target(u)
	if err != nil {
		res.Error = err.Error()
		return done()
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if model == "" {
		models, err := s.plane.Models(ctx, t)
		if err != nil {
			res.Error = err.Error()
			return done()
		}
		res.OK, res.Models = true, len(models)
		return done()
	}
	out, body, sent := s.testCall(ctx, t, model)
	res.Status = out.Status
	s.account(who, u, model, s.meterName(ctx, u, model), out, sent)
	switch {
	case out.OK():
		res.OK, res.Tokens = true, out.Usage.TotalTokens
	case body != "" && out.Err != "": // the answer's message, and what the plane saw (an admin's to read)
		res.Error = body + " — " + out.Err
	default:
		res.Error = body + out.Err
	}
	return done()
}
